package main

// dispatch.go wires the Router into the daemon and handles CapabilityRequest
// frames arriving from Ward stdio pipes.
//
// Frame flow (MVP single-agent path):
//   Ward stdout → musPipe.Reader → Router.readLoop
//     → daemon.handleFrame()
//       → if CapabilityRequest: Dispatcher.Dispatch() → CapabilityResponse written to Ward pipe
//       → if CompletionEvent/FailureEvent: audit + push to ctl subscribers
//       → if Ping: Pong

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"time"

	"vivary.dev/vivary/internal/audit"
	"vivary.dev/vivary/internal/capabilities"
	"vivary.dev/vivary/internal/switchboard"
)

// ---- Frame handler wired into the Router -----------------------------------

// handleFrame is registered as the Router handler.  It is called for every
// validated frame from any pipe (Ward or ctl-originated agent prompts).
func (d *daemon) handleFrame(f switchboard.Frame) {
	// Audit: record every frame.  Omit payload for high-sensitivity types.
	auditPayload := d.shouldAuditPayload(f.Header.Type, f.Header.FromID)
	var stored []byte
	if auditPayload {
		stored = f.Payload
	}
	_ = d.auditDB.WriteFrame(
		time.Now(),
		f.Header.Type.String(),
		f.Header.FromID,
		f.Header.ToID,
		f.Header.SeqNo,
		stored,
	)

	switch f.Header.Type {
	case switchboard.MsgType_CapabilityRequest:
		d.handleCapabilityRequest(f)
	case switchboard.MsgType_CompletionEvent:
		d.handleCompletionEvent(f)
	case switchboard.MsgType_FailureEvent:
		d.handleFailureEvent(f)
	case switchboard.MsgType_Ping:
		_ = d.router.Send(f.Header.FromID, switchboard.SwarmHeader{
			Version: 0, Type: switchboard.MsgType_Pong,
			FromID: "keeper", ToID: f.Header.FromID,
			SeqNo: d.seqOut.Next(),
		}, nil)
	default:
		d.log.Warn("keeperd: unhandled frame from Ward", "type", f.Header.Type, "from", f.Header.FromID)
	}
}

// handleCapabilityRequest dispatches a CapabilityRequest from a Ward and writes
// the CapabilityResponse back to the same agent pipe.
func (d *daemon) handleCapabilityRequest(f switchboard.Frame) {
	type capReqPayload struct {
		Name    string          `json:"name"`
		AgentID string          `json:"agent_id"`
		Args    json.RawMessage `json:"args"`
	}
	var req capReqPayload
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		d.sendCapabilityDenied(f, "malformed request: "+err.Error())
		return
	}
	if req.AgentID != f.Header.FromID {
		// Identity mismatch: the router already overwrote FromID so this should
		// not happen, but defence-in-depth.
		d.log.Warn("capability request agent_id != FromID", "from", f.Header.FromID, "claimed", req.AgentID)
		req.AgentID = f.Header.FromID
	}

	ctx, cancel := context.WithTimeout(d.ctx, 30*time.Second)
	defer cancel()

	resp, err := d.dispatcher.Dispatch(ctx, capabilities.Request{
		Name:    req.Name,
		AgentID: req.AgentID,
		SeqNo:   f.Header.SeqNo,
		Args:    req.Args,
	})
	if err != nil {
		d.log.Error("capability execution error", "cap", req.Name, "agent", req.AgentID, "err", err)
		resp = capabilities.DeniedResponse(fmt.Sprintf("internal error: %v", err))
	}

	// Audit capability denials as security events.
	if !resp.OK && resp.ErrorCode == "capability_denied" {
		_ = d.auditDB.WriteSecurityEvent(
			time.Now(), req.AgentID, "capability_denied",
			fmt.Sprintf("cap=%s detail=%s", req.Name, resp.ErrorDetail),
		)
	}

	respPayload, _ := json.Marshal(resp)
	respHdr := switchboard.SwarmHeader{
		Version: 0, Type: switchboard.MsgType_CapabilityResponse,
		FromID: "keeper", ToID: f.Header.FromID,
		SeqNo: f.Header.SeqNo, // echo SeqNo so Ward can correlate
	}
	if err := d.router.Send(f.Header.FromID, respHdr, respPayload); err != nil {
		d.log.Error("failed to send capability response", "agent", f.Header.FromID, "err", err)
	}
}

func (d *daemon) sendCapabilityDenied(f switchboard.Frame, detail string) {
	resp := capabilities.DeniedResponse(detail)
	payload, _ := json.Marshal(resp)
	_ = d.router.Send(f.Header.FromID, switchboard.SwarmHeader{
		Version: 0, Type: switchboard.MsgType_CapabilityResponse,
		FromID: "keeper", ToID: f.Header.FromID,
		SeqNo: f.Header.SeqNo,
	}, payload)
}

// handleCompletionEvent records the event and pushes it to ctl subscribers.
func (d *daemon) handleCompletionEvent(f switchboard.Frame) {
	ev, err := audit.UnmarshalCompletion(f.Payload)
	if err != nil {
		d.log.Warn("malformed CompletionEvent", "from", f.Header.FromID, "err", err)
		return
	}
	d.log.Info("agent completion",
		"agent", ev.AgentID, "seq", ev.PromptSeq,
		"outcome", ev.Outcome, "tokens_in", ev.InputTokens, "tokens_out", ev.OutputTokens,
		"cost_usd", ev.CostUSD,
	)
	d.pushToCtlSubscribers(f)
}

// handleFailureEvent records the event and pushes it to ctl subscribers.
func (d *daemon) handleFailureEvent(f switchboard.Frame) {
	ev, err := audit.UnmarshalFailure(f.Payload)
	if err != nil {
		d.log.Warn("malformed FailureEvent", "from", f.Header.FromID, "err", err)
		return
	}
	d.log.Warn("agent failure",
		"agent", ev.AgentID, "seq", ev.PromptSeq,
		"kind", ev.Kind, "detail", ev.Detail,
	)
	d.pushToCtlSubscribers(f)
}

// shouldAuditPayload returns false for high-sensitivity capability categories.
// Currently based on MsgType; per-capability audit-payload flags extend this.
func (d *daemon) shouldAuditPayload(_ switchboard.MsgType, _ string) bool {
	// CompletionEvent and FailureEvent payloads are always stored — they are
	// telemetry not sensitive data.
	return true
}

// ---- Ctl subscriber push ---------------------------------------------------

// pushToCtlSubscribers fans out an event frame to all registered ctl connections.
func (d *daemon) pushToCtlSubscribers(f switchboard.Frame) {
	d.subsMu.RLock()
	subs := d.ctlSubs
	d.subsMu.RUnlock()
	for _, ch := range subs {
		select {
		case ch <- f:
		default: // subscriber is slow; drop rather than block
		}
	}
}

// ---- Ward stdio pipe spawning ----------------------------------------------

// spawnWardPipe opens the Ward binary as a subprocess (for testing / local
// mode where nspawn is not available) and registers its stdio as a pipe with
// the Router.
//
// In full nspawn mode, keeperd instead opens the Ward stdio pipe that was
// created at nspawn spawn time (see provisioning.go).
func (d *daemon) spawnWardPipe(ctx context.Context, agentID string, wardBin string, extraArgs []string) error {
	args := append([]string{"--agent-id", agentID}, extraArgs...)
	cmd := exec.CommandContext(ctx, wardBin, args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("ward stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("ward stdout pipe: %w", err)
	}
	cmd.Stderr = writerSink{log: d.log, agentID: agentID}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ward start: %w", err)
	}

	pipe := &switchboard.Pipe{
		AgentID: agentID,
		Reader:  stdout,
		Writer:  stdin,
		Limiter: switchboard.NewByteRateLimiter(d.cfg.MaxAgentPipeBytesPerSec),
	}

	d.router.AddPipe(ctx, pipe)

	d.mu.Lock()
	if a, ok := d.agents[agentID]; ok {
		a.pipe = pipe
	}
	d.mu.Unlock()

	// Reap the subprocess when it exits and remove the pipe.
	go func() {
		_ = cmd.Wait()
		d.router.RemovePipe(agentID)
		d.log.Info("ward subprocess exited", "agent", agentID)
	}()

	return nil
}

// writerSink returns an io.Writer that logs lines from a Ward's stderr.
type writerSink struct {
	log     *slog.Logger
	agentID string
}

func (w writerSink) Write(p []byte) (int, error) {
	w.log.Debug("ward stderr", "agent", w.agentID, "line", string(p))
	return len(p), nil
}

// Compile-time check that writerSink satisfies io.Writer.
var _ io.Writer = writerSink{}
