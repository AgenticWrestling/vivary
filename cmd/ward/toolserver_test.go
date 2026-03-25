package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"vivary.dev/vivary/internal/audit"
	"vivary.dev/vivary/internal/capabilities"
	"vivary.dev/vivary/internal/ctl"
	"vivary.dev/vivary/internal/switchboard"
)

// ---- Helpers ---------------------------------------------------------------

func newTestWardForServer(t *testing.T) *ward {
	t.Helper()
	return &ward{
		agentID: "test-agent",
		log:     slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}
}

// startTestToolServer starts a toolServer on a temp Unix socket and returns
// its path.  The server is shut down when the test ends.
func startTestToolServer(t *testing.T, w *ward) string {
	t.Helper()
	// Initialize the capability registry for tests.
	capRegistry = capabilities.GeneratedRegistry()

	sockPath := filepath.Join(t.TempDir(), "tool.sock")
	ts := &toolServer{sockPath: sockPath, w: w}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = ts.run(ctx) }()

	// Wait for socket to appear (up to 1 s).
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			return sockPath
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("toolServer socket did not appear within 1 s")
	return ""
}

func dialToolSocket(t *testing.T, sockPath string) *net.UnixConn {
	t.Helper()
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial tool socket: %v", err)
	}
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		conn.Close()
		t.Fatal("tool socket connection is not a UnixConn")
	}
	t.Cleanup(func() { unixConn.Close() })
	return unixConn
}

func readToolResponse(t *testing.T, conn *net.UnixConn) ToolResponse {
	t.Helper()
	var resp ToolResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

func sendRawToolPayload(t *testing.T, sockPath string, payload []byte) ToolResponse {
	t.Helper()
	conn := dialToolSocket(t, sockPath)
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write request: %v", err)
	}
	_ = conn.CloseWrite()
	return readToolResponse(t, conn)
}

func newPromptTestWard(t *testing.T) (*ward, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	w := newTestWardForServer(t)
	w.pipe = &musPipe{w: &out, agentID: w.agentID, log: w.log}
	return w, &out
}

func writeFakeClaude(t *testing.T, content string) string {
	t.Helper()
	tmp := t.TempDir()
	claudePath := filepath.Join(tmp, "claude")
	if err := os.WriteFile(claudePath, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))
	return tmp
}

func readFailureEventFromBuffer(t *testing.T, out *bytes.Buffer) audit.FailureEvent {
	t.Helper()
	hdr, payload, err := switchboard.ReadFrame(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("read emitted frame: %v", err)
	}
	if hdr.Type != switchboard.MsgType_FailureEvent {
		t.Fatalf("frame type = %v, want FailureEvent", hdr.Type)
	}
	ev, err := audit.UnmarshalFailure(payload)
	if err != nil {
		t.Fatalf("decode failure event: %v", err)
	}
	return ev
}

type frameCaptureWriter struct {
	mu       sync.Mutex
	ward     *ward
	frames   [][]byte
	requests []capabilities.CapabilityRequestPayload
}

func (w *frameCaptureWriter) Write(p []byte) (int, error) {
	copyBuf := append([]byte(nil), p...)
	hdr, payload, err := switchboard.ReadFrame(bytes.NewReader(copyBuf))
	if err != nil {
		return 0, err
	}
	if hdr.Type == switchboard.MsgType_CapabilityRequest {
		var req capabilities.CapabilityRequestPayload
		if err := req.UnmarshalMUS(bytes.NewReader(payload)); err != nil {
			return 0, err
		}
		w.mu.Lock()
		w.requests = append(w.requests, req)
		w.mu.Unlock()

		respData, _ := json.Marshal(map[string]string{"text": "page text"})
		respPayload := capabilities.CapabilityResponsePayload{OK: true, Data: respData}
		w.ward.deliverCapabilityResponse(switchboard.SwarmHeader{Type: switchboard.MsgType_CapabilityResponse, SeqNo: hdr.SeqNo}, respPayload.MarshalMUS())
		return len(p), nil
	}
	w.mu.Lock()
	w.frames = append(w.frames, copyBuf)
	w.mu.Unlock()
	return len(p), nil
}

func (w *frameCaptureWriter) singleFrame(t *testing.T) (switchboard.SwarmHeader, []byte, error) {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.frames) != 1 {
		t.Fatalf("captured %d frames, want 1", len(w.frames))
	}
	return switchboard.ReadFrame(bytes.NewReader(w.frames[0]))
}

func (w *frameCaptureWriter) singleRequest(t *testing.T) capabilities.CapabilityRequestPayload {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.requests) != 1 {
		t.Fatalf("captured %d capability requests, want 1", len(w.requests))
	}
	return w.requests[0]
}

// toolRoundTrip dials sockPath, sends req as JSON, signals EOF, and returns
// the decoded ToolResponse.
func toolRoundTrip(t *testing.T, sockPath string, req ToolRequest) ToolResponse {
	t.Helper()
	data, _ := json.Marshal(req)
	return sendRawToolPayload(t, sockPath, data)
}

// ---- isSchemaOnly unit tests -----------------------------------------------

func TestIsSchemaOnly_True(t *testing.T) {
	if !isSchemaOnly(json.RawMessage(`{"__schema_only":true}`)) {
		t.Error("expected true for schema-only sentinel")
	}
}

func TestIsSchemaOnly_ExtraKey(t *testing.T) {
	// Must have exactly one key.
	if isSchemaOnly(json.RawMessage(`{"__schema_only":true,"extra":"x"}`)) {
		t.Error("extra key should make isSchemaOnly return false")
	}
}

func TestIsSchemaOnly_False(t *testing.T) {
	cases := []json.RawMessage{
		json.RawMessage(`{}`),
		json.RawMessage(`{"url":"https://example.com"}`),
		json.RawMessage(`null`),
		nil,
		json.RawMessage(`not-json`),
	}
	for _, c := range cases {
		if isSchemaOnly(c) {
			t.Errorf("isSchemaOnly(%s) should be false", c)
		}
	}
}

// ---- parentDir unit tests --------------------------------------------------

func TestParentDir(t *testing.T) {
	cases := []struct{ path, want string }{
		{"/run/ward-tool.sock", "/run"},
		{"/a/b/c", "/a/b"},
		{"relative/path", "relative"},
		{"nodir", "."},
		{"", "."},
	}
	for _, tc := range cases {
		got := parentDir(tc.path)
		if got != tc.want {
			t.Errorf("parentDir(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// ---- Loop detection via executeTool ----------------------------------------

// TestExecuteTool_LoopDetected verifies that executeTool returns loop_detected
// and sets activeAbort when the loop detector fires.
//
// We pre-populate the detector to threshold-1 via direct ld.check() calls so
// the single executeTool call trips the threshold without needing a real pipe.
func TestExecuteTool_LoopDetected(t *testing.T) {
	const threshold = 3
	w := newTestWardForServer(t)
	w.loopThreshold = threshold

	ld := newLoopDetector(threshold)
	w.activeLoop.Store(ld)
	// No activeCmd — executeTool handles a nil cmd gracefully.

	args := json.RawMessage(`{"url":"https://example.com"}`)

	// Warm the detector to threshold-1 without going through executeTool.
	for range threshold - 1 {
		ld.check("Browser_Page_Read", args)
	}

	// This executeTool call should now trigger loop detection immediately,
	// before any attempt to reach keeperd via the (nil) pipe.
	resp := w.executeTool(context.Background(), ToolRequest{
		Capability: "Browser_Page_Read",
		Args:       args,
	})

	if resp.OK {
		t.Fatal("expected OK=false on loop_detected")
	}
	if resp.ErrorCode != "loop_detected" {
		t.Errorf("error_code = %q, want %q", resp.ErrorCode, "loop_detected")
	}
	// activeAbort must be set so runLLMSubprocess emits the right failure kind.
	abort := w.activeAbort.Load()
	if abort == nil || *abort != "loop_detected" {
		t.Errorf("activeAbort = %v, want pointer to \"loop_detected\"", abort)
	}
}

func TestExecuteTool_SchemaInvalid_MissingRequiredField(t *testing.T) {
	w := newTestWardForServer(t)
	resp := w.executeTool(context.Background(), ToolRequest{
		Capability: "Browser_Page_Read",
		Args:       json.RawMessage(`{"wait_for":"networkidle"}`),
	})

	if resp.OK {
		t.Fatal("expected OK=false for schema-invalid args")
	}
	if resp.ErrorCode != "schema_invalid" {
		t.Fatalf("error_code = %q, want %q", resp.ErrorCode, "schema_invalid")
	}
	if !strings.Contains(resp.ErrorDetail, `missing required field "url"`) {
		t.Fatalf("unexpected error detail: %q", resp.ErrorDetail)
	}
	abort := w.activeAbort.Load()
	if abort == nil || *abort != "schema_invalid" {
		t.Fatalf("activeAbort = %v, want pointer to \"schema_invalid\"", abort)
	}
}

func TestExecuteTool_SchemaInvalid_UnknownField(t *testing.T) {
	w := newTestWardForServer(t)
	resp := w.executeTool(context.Background(), ToolRequest{
		Capability: "Filesystem_File_Write",
		Args:       json.RawMessage(`{"path":"out.txt","content":"ok","extra":true}`),
	})

	if resp.OK {
		t.Fatal("expected OK=false for unknown field")
	}
	if resp.ErrorCode != "schema_invalid" {
		t.Fatalf("error_code = %q, want %q", resp.ErrorCode, "schema_invalid")
	}
	if !strings.Contains(resp.ErrorDetail, `unknown field "extra"`) {
		t.Fatalf("unexpected error detail: %q", resp.ErrorDetail)
	}
}

func TestToolServer_MalformedJSON_SetsAbortKind(t *testing.T) {
	w := newTestWardForServer(t)
	sockPath := startTestToolServer(t, w)

	resp := sendRawToolPayload(t, sockPath, []byte("this is not json"))
	if resp.OK {
		t.Error("expected OK=false for malformed JSON")
	}
	abort := w.activeAbort.Load()
	if abort == nil || *abort != "malformed_tool_call" {
		t.Fatalf("activeAbort = %v, want pointer to \"malformed_tool_call\"", abort)
	}
}

func TestHandlePrompt_EmitsFailureEventForAbortedPrompt(t *testing.T) {
	writeFakeClaude(t, "#!/bin/sh\nsleep 5\n")
	w, out := newPromptTestWard(t)

	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if w.activeCmd.Load() != nil {
				w.abortActivePrompt("schema_invalid")
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	prompt := ctl.PromptPayload{AgentID: w.agentID, Seq: 42, Text: "test prompt"}
	w.handlePrompt(context.Background(), prompt.MarshalMUS())

	ev := readFailureEventFromBuffer(t, out)
	if ev.Kind != "schema_invalid" {
		t.Fatalf("failure kind = %q, want %q", ev.Kind, "schema_invalid")
	}
	if ev.PromptSeq != 42 {
		t.Fatalf("prompt seq = %d, want 42", ev.PromptSeq)
	}
}

func TestHandlePrompt_EmitsMalformedToolCallFailureViaToolSocket(t *testing.T) {
	writeFakeClaude(t, "#!/bin/sh\nsleep 5\n")
	w, out := newPromptTestWard(t)
	sockPath := filepath.Join(t.TempDir(), "ward-tool.sock")
	w.toolSockPath = sockPath

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ts := &toolServer{sockPath: sockPath, w: w}
	go func() { _ = ts.run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := os.Stat(sockPath); err != nil {
		t.Fatalf("tool socket did not appear: %v", err)
	}

	prompt := ctl.PromptPayload{AgentID: w.agentID, Seq: 99, Text: "test prompt"}
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.handlePrompt(context.Background(), prompt.MarshalMUS())
	}()

	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if w.activeCmd.Load() != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if w.activeCmd.Load() == nil {
		t.Fatal("LLM subprocess did not become active")
	}

	resp := sendRawToolPayload(t, sockPath, []byte("this is not json"))
	if resp.OK {
		t.Fatal("malformed tool payload unexpectedly succeeded")
	}
	if resp.ErrorCode != "bad_request" {
		t.Fatalf("error code = %q, want %q", resp.ErrorCode, "bad_request")
	}

	<-done

	ev := readFailureEventFromBuffer(t, out)
	if ev.Kind != "malformed_tool_call" {
		t.Fatalf("failure kind = %q, want %q", ev.Kind, "malformed_tool_call")
	}
	if ev.PromptSeq != 99 {
		t.Fatalf("prompt seq = %d, want 99", ev.PromptSeq)
	}
}

func TestHandlePrompt_ExecutesToolAndEmitsCompletionEvent(t *testing.T) {
	capRegistry = capabilities.GeneratedRegistry()
	writeFakeClaude(t, "#!/bin/sh\nsleep 1\nprintf '%s\n' '{\"type\":\"tool_use\"}' '{\"type\":\"message_stop\",\"usage\":{\"input_tokens\":12,\"output_tokens\":7}}'\n")
	w := newTestWardForServer(t)
	capture := &frameCaptureWriter{ward: w}
	w.pipe = &musPipe{w: capture, agentID: w.agentID, log: w.log}
	sockPath := startTestToolServer(t, w)
	w.toolSockPath = sockPath

	prompt := ctl.PromptPayload{AgentID: w.agentID, Seq: 55, Text: "test prompt"}
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.handlePrompt(context.Background(), prompt.MarshalMUS())
	}()

	resp := toolRoundTrip(t, sockPath, ToolRequest{
		Capability: "Browser_Page_Read",
		Args:       json.RawMessage(`{"url":"https://example.com/page"}`),
	})
	if !resp.OK {
		t.Fatalf("tool round trip failed: %s %s", resp.ErrorCode, resp.ErrorDetail)
	}
	<-done

	req := capture.singleRequest(t)
	if req.Capability != "Browser_Page_Read" {
		t.Fatalf("capability = %q, want Browser_Page_Read", req.Capability)
	}
	if !bytes.Contains(req.Args, []byte(`"url":"https://example.com/page"`)) {
		t.Fatalf("unexpected capability args: %s", req.Args)
	}

	hdr, payload, err := capture.singleFrame(t)
	if err != nil {
		t.Fatalf("read captured frame: %v", err)
	}
	if hdr.Type != switchboard.MsgType_CompletionEvent {
		t.Fatalf("frame type = %v, want CompletionEvent", hdr.Type)
	}
	ev, err := audit.UnmarshalCompletion(payload)
	if err != nil {
		t.Fatalf("decode completion event: %v", err)
	}
	if ev.PromptSeq != 55 {
		t.Fatalf("prompt seq = %d, want 55", ev.PromptSeq)
	}
	if ev.Outcome != "success" {
		t.Fatalf("outcome = %q, want success", ev.Outcome)
	}
	if ev.ToolCalls != 1 {
		t.Fatalf("tool calls = %d, want 1", ev.ToolCalls)
	}
	if ev.InputTokens != 12 || ev.OutputTokens != 7 {
		t.Fatalf("tokens = (%d,%d), want (12,7)", ev.InputTokens, ev.OutputTokens)
	}
}

// ---- toolServer integration tests ------------------------------------------

func TestToolServer_MalformedJSON(t *testing.T) {
	w := newTestWardForServer(t)
	sockPath := startTestToolServer(t, w)

	resp := sendRawToolPayload(t, sockPath, []byte("this is not json"))
	if resp.OK {
		t.Error("expected OK=false for malformed JSON")
	}
	if resp.ErrorCode != "bad_request" {
		t.Errorf("error_code = %q, want %q", resp.ErrorCode, "bad_request")
	}
}

func TestToolServer_MissingCapabilityName(t *testing.T) {
	w := newTestWardForServer(t)
	sockPath := startTestToolServer(t, w)

	resp := toolRoundTrip(t, sockPath, ToolRequest{
		Capability: "", // omitted
		Args:       json.RawMessage(`{"url":"https://example.com"}`),
	})
	if resp.OK {
		t.Error("expected OK=false for missing capability name")
	}
	if resp.ErrorCode != "bad_request" {
		t.Errorf("error_code = %q, want %q", resp.ErrorCode, "bad_request")
	}
}

func TestToolServer_SchemaOnly_KnownCapability(t *testing.T) {
	w := newTestWardForServer(t)
	sockPath := startTestToolServer(t, w)

	resp := toolRoundTrip(t, sockPath, ToolRequest{
		Capability: "Browser_Page_Read",
		Args:       json.RawMessage(`{"__schema_only":true}`),
	})
	if !resp.OK {
		t.Fatalf("schema-only for known capability should succeed: %s %s", resp.ErrorCode, resp.ErrorDetail)
	}
	// Data must contain a "schema" key with non-empty value.
	var data map[string]string
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatalf("response data not a JSON object: %v", err)
	}
	if data["schema"] == "" {
		t.Error("expected non-empty schema in response data")
	}
}

func TestToolServer_SchemaOnly_UnknownCapability(t *testing.T) {
	w := newTestWardForServer(t)
	sockPath := startTestToolServer(t, w)

	resp := toolRoundTrip(t, sockPath, ToolRequest{
		Capability: "Nonexistent_Cap_Do",
		Args:       json.RawMessage(`{"__schema_only":true}`),
	})
	if resp.OK {
		t.Error("expected OK=false for unknown capability schema request")
	}
	if resp.ErrorCode != "not_found" {
		t.Errorf("error_code = %q, want %q", resp.ErrorCode, "not_found")
	}
}
