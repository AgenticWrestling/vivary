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
	"bytes"
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
	d.auditFrame(f)

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

func (d *daemon) auditFrame(f switchboard.Frame) {
	var stored []byte
	if d.shouldAuditPayload(f) {
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
}

// handleCapabilityRequest dispatches a CapabilityRequest from a Ward and writes
// the CapabilityResponse back to the same agent pipe.
func (d *daemon) handleCapabilityRequest(f switchboard.Frame) {
	payload, err := decodeCapabilityRequest(f.Payload)
	if err != nil {
		d.sendCapabilityDenied(f, "malformed request: "+err.Error())
		return
	}

	resp := d.dispatchCapabilityRequest(f, payload)
	d.auditCapabilityDenial(f.Header.FromID, payload.Capability, resp)
	d.sendCapabilityResponse(f, resp)
}

func decodeCapabilityRequest(payload []byte) (capabilities.CapabilityRequestPayload, error) {
	var req capabilities.CapabilityRequestPayload
	err := req.UnmarshalMUS(bytes.NewReader(payload))
	return req, err
}

func (d *daemon) dispatchCapabilityRequest(f switchboard.Frame, payload capabilities.CapabilityRequestPayload) capabilities.Response {
	ctx, cancel := context.WithTimeout(d.ctx, 30*time.Second)
	defer cancel()

	// bridge legacy JSON Args until all capabilities are fully MUS
	var jsonArgs json.RawMessage
	_ = json.Unmarshal(payload.Args, &jsonArgs)

	resp, err := d.dispatcher.Dispatch(ctx, capabilities.Request{
		Name:    payload.Capability,
		AgentID: f.Header.FromID,
		SeqNo:   f.Header.SeqNo,
		Args:    jsonArgs,
	})
	if err != nil {
		d.log.Error("capability execution error", "cap", payload.Capability, "agent", f.Header.FromID, "err", err)
		return capabilities.DeniedResponse(fmt.Sprintf("internal error: %v", err))
	}
	return resp
}

func (d *daemon) auditCapabilityDenial(agentID, capability string, resp capabilities.Response) {
	if !resp.OK && resp.ErrorCode == "capability_denied" {
		_ = d.auditDB.WriteSecurityEvent(
			time.Now(), agentID, "capability_denied",
			fmt.Sprintf("cap=%s detail=%s", capability, resp.ErrorDetail),
		)
	}
}

func (d *daemon) sendCapabilityResponse(f switchboard.Frame, resp capabilities.Response) {
	respPayload := capabilities.CapabilityResponsePayload{
		OK:          resp.OK,
		Data:        resp.Data, // bridge JSON Data for now
		ErrorCode:   resp.ErrorCode,
		ErrorDetail: resp.ErrorDetail,
	}
	respHdr := switchboard.SwarmHeader{
		Version: 0, Type: switchboard.MsgType_CapabilityResponse,
		FromID: "keeper", ToID: f.Header.FromID,
		SeqNo: f.Header.SeqNo, // echo SeqNo so Ward can correlate
	}
	if err := d.router.Send(f.Header.FromID, respHdr, respPayload.MarshalMUS()); err != nil {
		d.log.Error("failed to send capability response", "agent", f.Header.FromID, "err", err)
	}
}

func (d *daemon) sendCapabilityDenied(f switchboard.Frame, detail string) {
	resp := capabilities.DeniedResponse(detail)
	respPayload := capabilities.CapabilityResponsePayload{
		OK:          resp.OK,
		ErrorCode:   resp.ErrorCode,
		ErrorDetail: resp.ErrorDetail,
	}
	_ = d.router.Send(f.Header.FromID, switchboard.SwarmHeader{
		Version: 0, Type: switchboard.MsgType_CapabilityResponse,
		FromID: "keeper", ToID: f.Header.FromID,
		SeqNo: f.Header.SeqNo,
	}, respPayload.MarshalMUS())
}

// handleCompletionEvent records the event, updates agent runtime state, and
// pushes it to ctl subscribers.
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
	d.updateAgentCompletionState(ev)
	d.pushToCtlSubscribers(f)
}

// handleFailureEvent records the event, updates agent runtime state, and
// pushes it to ctl subscribers.
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
	d.updateAgentFailureState(ev)
	d.pushToCtlSubscribers(f)
}

func (d *daemon) updateAgentCompletionState(ev audit.CompletionEvent) {
	d.mu.Lock()
	if a, ok := d.agents[ev.AgentID]; ok {
		a.lastEventAt = time.Now()
		a.lastOutcome = ev.Outcome
		a.model = ev.Model
		a.inputTokens = ev.InputTokens
		a.outputTokens = ev.OutputTokens
		a.costUSD = ev.CostUSD
		a.toolCalls = ev.ToolCalls
		a.lastFailureDetail = "" // clear on success
	}
	d.mu.Unlock()
}

func (d *daemon) updateAgentFailureState(ev audit.FailureEvent) {
	d.mu.Lock()
	if a, ok := d.agents[ev.AgentID]; ok {
		a.lastEventAt = time.Now()
		a.lastOutcome = ev.Kind
		a.lastFailureDetail = ev.Detail
	}
	d.mu.Unlock()
}

// shouldAuditPayload returns true if f's payload should be written to the
// audit WAL.
//
// For CapabilityRequest frames the decision is delegated to the registered
// capability's AuditPayload() method so that high-sensitivity categories
// (Email, Messaging, Document, Database) can suppress payload storage.
// All other frame types (CompletionEvent, FailureEvent, control frames) are
// always audited — they carry telemetry, not user content.
func (d *daemon) shouldAuditPayload(f switchboard.Frame) bool {
	if f.Header.Type != switchboard.MsgType_CapabilityRequest {
		return true
	}
	// Peek at the capability name without full payload decode.
	var payload capabilities.CapabilityRequestPayload
	if err := payload.UnmarshalMUS(bytes.NewReader(f.Payload)); err != nil {
		return true // malformed frame; audit conservatively
	}
	cap, ok := d.dispatcher.Lookup(payload.Capability)
	if !ok {
		return true // unknown capability; audit conservatively
	}
	return cap.AuditPayload()
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
