package main

// toolserver.go implements the Ward-side Unix socket that capability CLI
// binaries connect to when the LLM subprocess invokes them as bash tools.
//
// Architecture inside the nspawn container:
//
//   keeperd ←[MUS stdio]→ Ward
//                          │ starts
//                          ▼
//                   Claude subprocess
//                          │ runs capability CLIs as bash tools
//                          ▼
//   capwrap binary ←[Unix socket]→ Ward toolserver
//                                        │ validates args
//                                        │ sends MUS CapabilityRequest
//                                        ▼
//                                    keeperd ACL/dispatch
//
// The tool socket is created at $WARD_TOOL_SOCK (default: /run/ward-tool.sock).
// Capability CLI binaries connect to it, send a JSON ToolRequest, and read a
// JSON ToolResponse.  The Ward validates args, forwards to keeperd via MUS, and
// writes back the response.
//
// Protocol (newline-delimited JSON, one request per connection):
//   → {"capability":"Browser_Page_Read","args":{"url":"https://example.com"}}
//   ← {"ok":true,"data":{"text":"..."}}
//   ← {"ok":false,"error_code":"capability_denied","error_detail":"..."}

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"vivary.dev/vivary/internal/capabilities"
)

const defaultToolSockPath = "/run/ward-tool.sock"

// toolServerEnvKey is the env var capability CLIs read to find the socket path.
const toolServerEnvKey = "WARD_TOOL_SOCK"

// ToolRequest is the JSON struct sent by capability CLI binaries.
type ToolRequest struct {
	Capability string          `json:"capability"`
	Args       json.RawMessage `json:"args"`
}

// ToolResponse is the JSON struct returned to the capability CLI binary.
type ToolResponse struct {
	OK          bool            `json:"ok"`
	Data        json.RawMessage `json:"data,omitempty"`
	ErrorCode   string          `json:"error_code,omitempty"`
	ErrorDetail string          `json:"error_detail,omitempty"`
}

// toolServer listens on a Unix socket inside the nspawn container and handles
// capability CLI requests.
type toolServer struct {
	sockPath string
	w        *ward
}

// run starts the tool socket listener.  It returns when ctx is cancelled.
func (s *toolServer) run(ctx context.Context) error {
	_ = os.Remove(s.sockPath)
	if err := os.MkdirAll(parentDir(s.sockPath), 0o755); err != nil {
		return fmt.Errorf("mkdir tool sock dir: %w", err)
	}

	ln, err := net.Listen("unix", s.sockPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.sockPath, err)
	}
	defer func() {
		ln.Close()
		os.Remove(s.sockPath)
	}()

	// Restrict to owner — only Ward (and its child processes) should connect.
	if err := os.Chmod(s.sockPath, 0o600); err != nil {
		return fmt.Errorf("chmod tool sock: %w", err)
	}

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				s.w.log.Warn("tool server accept error", "err", err)
				continue
			}
		}
		go s.handleConn(ctx, conn)
	}
}

func (s *toolServer) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))

	data, err := io.ReadAll(io.LimitReader(conn, 256*1024))
	if err != nil {
		return
	}

	var req ToolRequest
	if err := json.Unmarshal(data, &req); err != nil {
		s.w.abortActivePrompt("malformed_tool_call")
		writeToolResponse(conn, ToolResponse{
			OK: false, ErrorCode: "bad_request",
			ErrorDetail: "malformed JSON: " + err.Error(),
		})
		return
	}
	if req.Capability == "" {
		s.w.abortActivePrompt("malformed_tool_call")
		writeToolResponse(conn, ToolResponse{
			OK: false, ErrorCode: "bad_request", ErrorDetail: "capability name is required",
		})
		return
	}

	// Schema-only request (from capwrap --help): return the static Explain() string.
	if isSchemaOnly(req.Args) {
		schema := s.w.getCapabilitySchema(req.Capability)
		if schema == "" {
			writeToolResponse(conn, ToolResponse{
				OK: false, ErrorCode: "not_found",
				ErrorDetail: "capability " + req.Capability + " not registered",
			})
			return
		}
		data, _ := json.Marshal(map[string]string{"schema": schema})
		writeToolResponse(conn, ToolResponse{OK: true, Data: data})
		return
	}

	resp := s.w.executeTool(ctx, req)
	writeToolResponse(conn, resp)
}

func writeToolResponse(conn net.Conn, resp ToolResponse) {
	b, _ := json.Marshal(resp)
	b = append(b, '\n')
	_, _ = conn.Write(b)
}

// executeTool validates args and forwards the capability request to keeperd
// via the MUS pipe.  It blocks until keeperd responds or the context expires.
//
// Loop detection fires here, before keeperd is hit, so a repeated call is
// rejected immediately.  When a loop is detected, the active LLM subprocess
// is killed and activeAbort is set so runLLMSubprocess can emit the correct
// FailureEvent kind.
func (w *ward) executeTool(ctx context.Context, req ToolRequest) ToolResponse {
	if req.Args == nil {
		req.Args = json.RawMessage(`{}`)
	}

	// Loop detection: check before dispatching to keeperd.
	if ld := w.activeLoop.Load(); ld != nil {
		if ld.check(req.Capability, req.Args) {
			kind := "loop_detected"
			w.activeAbort.Store(&kind)
			if cmd := w.activeCmd.Load(); cmd != nil {
				_ = cmd.Process.Kill()
			}
			return ToolResponse{
				OK:          false,
				ErrorCode:   "loop_detected",
				ErrorDetail: fmt.Sprintf("capability %q repeated with identical args above threshold", req.Capability),
			}
		}
	}

	if err := validateToolArgs(req.Capability, req.Args); err != nil {
		w.abortActivePrompt("schema_invalid")
		return ToolResponse{OK: false, ErrorCode: "schema_invalid", ErrorDetail: err.Error()}
	}

	// Build the CapabilityRequest payload.
	reqPayload := capabilities.CapabilityRequestPayload{
		Capability: req.Capability,
		Args:       req.Args, // bridge JSON Args
	}

	// Allocate sequence number and register pending slot.
	seqNo := w.pipe.seq.Next()
	respCh := w.registerPending(seqNo)
	defer w.removePending(seqNo)

	// Send to keeperd.
	if err := w.pipe.sendWithSeq(seqNo, reqPayload.MarshalMUS()); err != nil {
		return ToolResponse{OK: false, ErrorCode: "pipe_error", ErrorDetail: err.Error()}
	}

	// Wait for response.
	select {
	case <-ctx.Done():
		return ToolResponse{OK: false, ErrorCode: "timeout", ErrorDetail: "context cancelled"}
	case <-time.After(60 * time.Second):
		return ToolResponse{OK: false, ErrorCode: "timeout", ErrorDetail: "keeperd response timeout"}
	case cr := <-respCh:
		var capResp capabilities.CapabilityResponsePayload
		if err := capResp.UnmarshalMUS(bytes.NewReader(cr.payload)); err != nil {
			return ToolResponse{OK: false, ErrorCode: "decode_error", ErrorDetail: err.Error()}
		}

		// bridge JSON Data for now
		var jsonData json.RawMessage
		_ = json.Unmarshal(capResp.Data, &jsonData)

		return ToolResponse{
			OK: capResp.OK, Data: jsonData,
			ErrorCode: capResp.ErrorCode, ErrorDetail: capResp.ErrorDetail,
		}
	}
}

func (w *ward) abortActivePrompt(kind string) {
	if kind == "" {
		return
	}
	w.activeAbort.Store(&kind)
	if cmd := w.activeCmd.Load(); cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// isSchemaOnly returns true if args contains only the sentinel __schema_only key.
func isSchemaOnly(args json.RawMessage) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(args, &m); err != nil {
		return false
	}
	_, ok := m["__schema_only"]
	return ok && len(m) == 1
}

func parentDir(p string) string {
	last := strings.LastIndex(p, "/")
	if last < 0 {
		return "."
	}
	return p[:last]
}
