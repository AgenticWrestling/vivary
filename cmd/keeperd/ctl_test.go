package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vivary.dev/vivary/internal/audit"
	"vivary.dev/vivary/internal/capabilities"
	"vivary.dev/vivary/internal/chromproxy"
	"vivary.dev/vivary/internal/ctl"
	"vivary.dev/vivary/internal/switchboard"
	"vivary.dev/vivary/pkg/mus"
)

// newTestDaemon creates an in-process daemon with a temp workspace.
// Returns the daemon, its socket path, and a cancel func.
func newTestDaemon(t *testing.T) (*daemon, string, context.CancelFunc) {
	t.Helper()
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "keeper.sock")
	dbPath := filepath.Join(dir, "audit.db")

	auditDB, err := audit.Open(dbPath)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { auditDB.Close() })

	reg := capabilities.NewRegistry()
	reg.Register(&capabilities.FilesystemFileWrite{})
	dispatcher := capabilities.NewDispatcher(reg)

	log := newLogger("error") // suppress noise in tests
	router := switchboard.NewRouter(log, func(ev switchboard.SecurityEvent) {})

	cfg := DefaultOrchestratorConfig(dir)
	cfg.SocketPath = sockPath

	d := &daemon{
		cfg:        cfg,
		log:        log,
		auditDB:    auditDB,
		dispatcher: dispatcher,
		router:     router,
		agents:     make(map[string]*agentState),
		startedAt:  time.Now(),
	}
	router.RegisterHandler(d.handleFrame)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

	if err := d.listenCtl(ctx); err != nil {
		cancel()
		t.Fatalf("listenCtl: %v", err)
	}

	// Wait briefly for the socket to appear.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	return d, sockPath, cancel
}

// ctlClient is a minimal ctl connection helper for tests.
type ctlClient struct {
	conn net.Conn
	seq  uint64
}

func dialCtl(t *testing.T, sockPath string) *ctlClient {
	t.Helper()
	conn, err := net.DialTimeout("unix", sockPath, time.Second)
	if err != nil {
		t.Fatalf("dial ctl: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return &ctlClient{conn: conn}
}

func (c *ctlClient) send(t *testing.T, msgType switchboard.MsgType, payload []byte) {
	t.Helper()
	c.seq++
	hdr := switchboard.SwarmHeader{
		Version: 0, Type: msgType,
		FromID: ctl.CtlIdentity, ToID: "keeper",
		SeqNo: c.seq,
	}
	if err := switchboard.WriteFrame(c.conn, hdr, payload); err != nil {
		t.Fatalf("send frame type=%v: %v", msgType, err)
	}
}

func (c *ctlClient) recv(t *testing.T) (switchboard.SwarmHeader, []byte) {
	t.Helper()
	c.conn.SetReadDeadline(time.Now().Add(2 * time.Second)) //nolint:errcheck
	hdr, payload, err := switchboard.ReadFrame(c.conn)
	if err != nil {
		t.Fatalf("recv frame: %v", err)
	}
	return hdr, payload
}

func decodeCapabilityResponse(t *testing.T, buf *bytes.Buffer) capabilities.CapabilityResponsePayload {
	t.Helper()
	hdr, payload, err := switchboard.ReadFrame(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("read capability response: %v", err)
	}
	if hdr.Type != switchboard.MsgType_CapabilityResponse {
		t.Fatalf("frame type = %v, want CapabilityResponse", hdr.Type)
	}
	var resp capabilities.CapabilityResponsePayload
	if err := resp.UnmarshalMUS(bytes.NewReader(payload)); err != nil {
		t.Fatalf("unmarshal capability response: %v", err)
	}
	return resp
}

func fetchCtlStatus(t *testing.T, sockPath string) ctl.StatusPayload {
	t.Helper()
	c := dialCtl(t, sockPath)
	c.send(t, switchboard.MsgType_CtlStatus, nil)
	hdr, payload := c.recv(t)
	if hdr.Type != switchboard.MsgType_CtlStatus {
		t.Fatalf("expected CtlStatus, got %v", hdr.Type)
	}
	var status ctl.StatusPayload
	if err := status.UnmarshalMUS(bytes.NewReader(payload)); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	return status
}

func findAgentStatus(t *testing.T, status ctl.StatusPayload, agentID string) ctl.AgentStatus {
	t.Helper()
	for i := range status.Agents {
		if status.Agents[i].ID == agentID {
			return status.Agents[i]
		}
	}
	t.Fatalf("agent %q not found in status", agentID)
	return ctl.AgentStatus{}
}

func registerTestAgentPipe(t *testing.T, d *daemon, agentID string) *io.PipeWriter {
	t.Helper()
	pr, pw := io.Pipe()
	pipe := &switchboard.Pipe{
		AgentID: agentID,
		Reader:  pr,
		Writer:  io.Discard,
		Limiter: switchboard.NewByteRateLimiter(1 << 20),
	}
	d.mu.Lock()
	d.agents[agentID] = &agentState{id: agentID, pipe: pipe}
	d.mu.Unlock()
	d.router.AddPipe(context.Background(), pipe)
	t.Cleanup(func() {
		_ = pw.Close()
		d.router.RemovePipe(agentID)
	})
	return pw
}

func registerIntegrationAgentPipe(t *testing.T, d *daemon, agentID string) (*io.PipeReader, *io.PipeWriter) {
	t.Helper()
	wardToKeeperReader, wardToKeeperWriter := io.Pipe()
	keeperToWardReader, keeperToWardWriter := io.Pipe()
	pipe := &switchboard.Pipe{
		AgentID: agentID,
		Reader:  wardToKeeperReader,
		Writer:  keeperToWardWriter,
		Limiter: switchboard.NewByteRateLimiter(1 << 20),
	}
	d.mu.Lock()
	d.agents[agentID] = &agentState{id: agentID, pipe: pipe}
	d.mu.Unlock()
	d.router.AddPipe(context.Background(), pipe)
	t.Cleanup(func() {
		_ = wardToKeeperWriter.Close()
		_ = keeperToWardReader.Close()
		d.router.RemovePipe(agentID)
	})
	return keeperToWardReader, wardToKeeperWriter
}

func registerResponsePipe(t *testing.T, d *daemon, agentID string, w io.Writer) {
	t.Helper()
	pipe := &switchboard.Pipe{AgentID: agentID, Reader: bytes.NewReader(nil), Writer: w}
	d.router.AddPipe(context.Background(), pipe)
	t.Cleanup(func() { d.router.RemovePipe(agentID) })
}

func queryDeniedSecurityEvents(t *testing.T, d *daemon, agentID string) []audit.SecurityEventRecord {
	t.Helper()
	events, err := d.auditDB.QuerySecurityEvents(audit.SecurityEventFilter{Agent: agentID, Kind: "capability_denied"})
	if err != nil {
		t.Fatalf("query security events: %v", err)
	}
	return events
}

func sendAgentFrame(t *testing.T, w io.Writer, agentID string, msgType switchboard.MsgType, seq uint64, payload []byte) {
	t.Helper()
	hdr := switchboard.SwarmHeader{Version: 0, Type: msgType, FromID: agentID, ToID: "keeper", SeqNo: seq}
	if err := switchboard.WriteFrame(w, hdr, payload); err != nil {
		t.Fatalf("write frame type=%v: %v", msgType, err)
	}
}

func waitForAgentState(t *testing.T, d *daemon, agentID string, ok func(*agentState) bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		d.mu.RLock()
		state := d.agents[agentID]
		ready := state != nil && ok(state)
		d.mu.RUnlock()
		if ready {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("agent state for %q did not reach expected condition", agentID)
}

func queryFramesByType(t *testing.T, d *daemon, msgType string, agentID string) []audit.FrameRecord {
	t.Helper()
	records, err := d.auditDB.QueryFrames(audit.FrameFilter{MsgType: msgType, AgentID: agentID})
	if err != nil {
		t.Fatalf("query frames: %v", err)
	}
	return records
}

// ---- Tests -----------------------------------------------------------------

// TestCtlSocket_Ping verifies that a Ping is answered with a Pong.
func TestCtlSocket_Ping(t *testing.T) {
	_, sockPath, cancel := newTestDaemon(t)
	defer cancel()

	c := dialCtl(t, sockPath)
	c.send(t, switchboard.MsgType_Ping, nil)
	hdr, _ := c.recv(t)
	if hdr.Type != switchboard.MsgType_Pong {
		t.Errorf("expected Pong, got %v", hdr.Type)
	}
}

// TestCtlSocket_Status verifies that CtlStatus returns a valid StatusPayload.
func TestCtlSocket_Status(t *testing.T) {
	_, sockPath, cancel := newTestDaemon(t)
	defer cancel()

	c := dialCtl(t, sockPath)
	c.send(t, switchboard.MsgType_CtlStatus, nil)
	hdr, payload := c.recv(t)

	if hdr.Type != switchboard.MsgType_CtlStatus {
		t.Fatalf("expected CtlStatus response, got %v", hdr.Type)
	}
	var status ctl.StatusPayload
	if err := status.UnmarshalMUS(bytes.NewReader(payload)); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if status.DaemonVersion == "" {
		t.Error("daemon_version is empty")
	}
	if status.UptimeSeconds < 0 {
		t.Error("negative uptime")
	}
}

// TestCtlSocket_AgentList verifies that CtlAgentList returns an empty list initially.
func TestCtlSocket_AgentList(t *testing.T) {
	_, sockPath, cancel := newTestDaemon(t)
	defer cancel()

	c := dialCtl(t, sockPath)
	c.send(t, switchboard.MsgType_CtlAgentList, nil)
	hdr, payload := c.recv(t)

	if hdr.Type != switchboard.MsgType_CtlAgentList {
		t.Fatalf("expected CtlAgentList response, got %v", hdr.Type)
	}
	var list ctl.AgentListPayload
	if err := list.UnmarshalMUS(bytes.NewReader(payload)); err != nil {
		t.Fatalf("unmarshal agent list: %v", err)
	}
	if len(list.Agents) != 0 {
		t.Errorf("expected empty agent list, got %d agents", len(list.Agents))
	}
}

// TestCtlSocket_Subscribe verifies that CtlSubscribe is acknowledged.
func TestCtlSocket_Subscribe(t *testing.T) {
	_, sockPath, cancel := newTestDaemon(t)
	defer cancel()

	c := dialCtl(t, sockPath)
	c.send(t, switchboard.MsgType_CtlSubscribe, nil)
	hdr, payload := c.recv(t)

	if hdr.Type != switchboard.MsgType_CtlSubscribe {
		t.Fatalf("expected CtlSubscribe ack, got %v", hdr.Type)
	}
	if len(payload) == 0 || payload[0] != 1 {
		t.Errorf("subscribe ack not ok: payload=%v", payload)
	}
}

// TestCtlSocket_AgentCreateInvalidID verifies that a bad agent ID is rejected.
func TestCtlSocket_AgentCreateInvalidID(t *testing.T) {
	_, sockPath, cancel := newTestDaemon(t)
	defer cancel()

	c := dialCtl(t, sockPath)
	req := ctl.AgentCreatePayload{
		ID:       "INVALID_ID!", // uppercase + special chars
		Template: "any",
	}
	c.send(t, switchboard.MsgType_CtlAgentCreate, req.MarshalMUS())
	hdr, respPayload := c.recv(t)

	if hdr.Type != switchboard.MsgType_CtlAgentCreate {
		t.Fatalf("expected CtlAgentCreate response, got %v", hdr.Type)
	}
	if len(respPayload) == 0 {
		t.Fatal("empty response payload")
	}
	if respPayload[0] != 0 {
		t.Errorf("expected error code 0, got %d", respPayload[0])
	}
	errStr, err := mus.ReadString(bytes.NewReader(respPayload[1:]), 1024)
	if err != nil {
		t.Fatalf("failed to decode error string: %v", err)
	}
	if errStr == "" {
		t.Error("expected non-empty error message")
	}
}

// TestCtlSocket_IdentityRejection verifies that non-ctl FromID disconnects the client.
func TestCtlSocket_IdentityRejection(t *testing.T) {
	_, sockPath, cancel := newTestDaemon(t)
	defer cancel()

	conn, err := net.DialTimeout("unix", sockPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Send a frame claiming to be an agent, not "ctl".
	hdr := switchboard.SwarmHeader{
		Version: 0, Type: switchboard.MsgType_Ping,
		FromID: "rogue-agent", ToID: "keeper",
		SeqNo: 1,
	}
	if err := switchboard.WriteFrame(conn, hdr, nil); err != nil {
		t.Fatal(err)
	}

	// Server should close the connection without responding.
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)) //nolint:errcheck
	_, _, err = switchboard.ReadFrame(conn)
	if err == nil {
		t.Error("expected connection close after rogue identity, got successful read")
	}
}

// TestKeeperRuntimeState verifies that CompletionEvent and FailureEvent frames
// update agentState, and that CtlStatus reflects those values accurately.
func TestKeeperRuntimeState_CompletionUpdatesStatus(t *testing.T) {
	d, sockPath, cancel := newTestDaemon(t)
	defer cancel()

	// Register a fake agent so handleCompletionEvent can find it.
	const agentID = "test-agent-01"
	d.mu.Lock()
	d.agents[agentID] = &agentState{id: agentID}
	d.mu.Unlock()

	// Inject a CompletionEvent frame directly into handleFrame (bypassing Ward).
	ev := audit.CompletionEvent{
		AgentID:      agentID,
		PromptSeq:    7,
		Model:        "claude-sonnet-4-5",
		InputTokens:  100,
		OutputTokens: 42,
		CostUSD:      0.0012,
		Outcome:      "success",
		ToolCalls:    3,
	}
	payload, err := audit.MarshalEvent(&ev)
	if err != nil {
		t.Fatalf("marshal completion event: %v", err)
	}
	d.handleFrame(switchboard.Frame{
		Header: switchboard.SwarmHeader{
			Type:   switchboard.MsgType_CompletionEvent,
			FromID: agentID,
			ToID:   "keeper",
			SeqNo:  1,
		},
		Payload: payload,
	})

	// Now request CtlStatus and verify the runtime fields match.
	c := dialCtl(t, sockPath)
	c.send(t, switchboard.MsgType_CtlStatus, nil)
	hdr, respPayload := c.recv(t)

	if hdr.Type != switchboard.MsgType_CtlStatus {
		t.Fatalf("expected CtlStatus, got %v", hdr.Type)
	}
	var status ctl.StatusPayload
	if err := status.UnmarshalMUS(bytes.NewReader(respPayload)); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}

	var found *ctl.AgentStatus
	for i := range status.Agents {
		if status.Agents[i].ID == agentID {
			found = &status.Agents[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("agent %q not found in status", agentID)
	}
	if found.LastOutcome != "success" {
		t.Errorf("LastOutcome = %q, want %q", found.LastOutcome, "success")
	}
	if found.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", found.InputTokens)
	}
	if found.OutputTokens != 42 {
		t.Errorf("OutputTokens = %d, want 42", found.OutputTokens)
	}
	if found.ToolCalls != 3 {
		t.Errorf("ToolCalls = %d, want 3", found.ToolCalls)
	}
	if found.LastEventAt == "" {
		t.Error("LastEventAt should be non-empty after a completion event")
	}
	if found.CostUSD == "" {
		t.Error("CostUSD should be non-empty after a completion event with non-zero cost")
	}
}

func TestKeeperRuntimeState_FailureUpdatesStatus(t *testing.T) {
	d, sockPath, cancel := newTestDaemon(t)
	defer cancel()

	const agentID = "test-agent-02"
	d.mu.Lock()
	d.agents[agentID] = &agentState{id: agentID}
	d.mu.Unlock()

	fev := audit.FailureEvent{
		AgentID:   agentID,
		PromptSeq: 3,
		Kind:      "loop_detected",
		Detail:    "capability X repeated 5 times",
	}
	payload, err := audit.MarshalEvent(&fev)
	if err != nil {
		t.Fatalf("marshal failure event: %v", err)
	}
	d.handleFrame(switchboard.Frame{
		Header: switchboard.SwarmHeader{
			Type:   switchboard.MsgType_FailureEvent,
			FromID: agentID,
			ToID:   "keeper",
			SeqNo:  1,
		},
		Payload: payload,
	})

	c := dialCtl(t, sockPath)
	c.send(t, switchboard.MsgType_CtlAgentList, nil)
	hdr, respPayload := c.recv(t)
	if hdr.Type != switchboard.MsgType_CtlAgentList {
		t.Fatalf("expected CtlAgentList, got %v", hdr.Type)
	}
	var list ctl.AgentListPayload
	if err := list.UnmarshalMUS(bytes.NewReader(respPayload)); err != nil {
		t.Fatalf("unmarshal agent list: %v", err)
	}

	var found *ctl.AgentStatus
	for i := range list.Agents {
		if list.Agents[i].ID == agentID {
			found = &list.Agents[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("agent %q not in list", agentID)
	}
	if found.LastOutcome != "loop_detected" {
		t.Errorf("LastOutcome = %q, want %q", found.LastOutcome, "loop_detected")
	}
	if found.LastEventAt == "" {
		t.Error("LastEventAt should be set after a failure event")
	}
}

func TestKeeperWardPipe_FailureEventUpdatesCtlStatus(t *testing.T) {
	d, sockPath, cancel := newTestDaemon(t)
	defer cancel()

	const agentID = "test-agent-pipe"
	pw := registerTestAgentPipe(t, d, agentID)

	fev := audit.FailureEvent{
		AgentID:   agentID,
		PromptSeq: 17,
		Kind:      "malformed_tool_call",
		Detail:    "malformed JSON: invalid character 'x' looking for beginning of value",
	}
	payload, err := audit.MarshalEvent(&fev)
	if err != nil {
		t.Fatalf("marshal failure event: %v", err)
	}
	sendAgentFrame(t, pw, agentID, switchboard.MsgType_FailureEvent, 1, payload)

	waitForAgentState(t, d, agentID, func(state *agentState) bool {
		return state.lastOutcome == "malformed_tool_call" && !state.lastEventAt.IsZero()
	})

	status := fetchCtlStatus(t, sockPath)
	found := findAgentStatus(t, status, agentID)
	if found.State != "running" {
		t.Fatalf("State = %q, want running", found.State)
	}
	if found.LastOutcome != "malformed_tool_call" {
		t.Fatalf("LastOutcome = %q, want malformed_tool_call", found.LastOutcome)
	}
	if found.LastEventAt == "" {
		t.Fatal("LastEventAt should be set after failure event from ward pipe")
	}
}

func TestKeeperWardPipe_CompletionEventUpdatesCtlStatus(t *testing.T) {
	d, sockPath, cancel := newTestDaemon(t)
	defer cancel()

	const agentID = "test-agent-pipe-success"
	pw := registerTestAgentPipe(t, d, agentID)

	cev := audit.CompletionEvent{
		AgentID:      agentID,
		PromptSeq:    23,
		Model:        "claude-sonnet-4-5",
		InputTokens:  120,
		OutputTokens: 55,
		CostUSD:      0.0025,
		Outcome:      "success",
		ToolCalls:    4,
	}
	payload, err := audit.MarshalEvent(&cev)
	if err != nil {
		t.Fatalf("marshal completion event: %v", err)
	}
	sendAgentFrame(t, pw, agentID, switchboard.MsgType_CompletionEvent, 1, payload)

	waitForAgentState(t, d, agentID, func(state *agentState) bool {
		return state.lastOutcome == "success" && state.inputTokens == 120 && state.outputTokens == 55 && state.toolCalls == 4 && !state.lastEventAt.IsZero()
	})

	status := fetchCtlStatus(t, sockPath)
	found := findAgentStatus(t, status, agentID)
	if found.State != "running" {
		t.Fatalf("State = %q, want running", found.State)
	}
	if found.LastOutcome != "success" {
		t.Fatalf("LastOutcome = %q, want success", found.LastOutcome)
	}
	if found.InputTokens != 120 {
		t.Fatalf("InputTokens = %d, want 120", found.InputTokens)
	}
	if found.OutputTokens != 55 {
		t.Fatalf("OutputTokens = %d, want 55", found.OutputTokens)
	}
	if found.ToolCalls != 4 {
		t.Fatalf("ToolCalls = %d, want 4", found.ToolCalls)
	}
	if found.CostUSD == "" {
		t.Fatal("CostUSD should be set after completion event from ward pipe")
	}
	if found.LastEventAt == "" {
		t.Fatal("LastEventAt should be set after completion event from ward pipe")
	}
}

func TestBrowserCapabilityRequest_DeniedWritesSecurityEvent(t *testing.T) {
	d, _, cancel := newTestDaemon(t)
	defer cancel()
	d.ctx = context.Background()

	d.dispatcher.Register(&capabilities.BrowserPageRead{ChromeProxy: func(context.Context, string, string, chromproxy.WhitelistPolicy, string, int) (string, error) {
		t.Fatal("proxy should not be called when capability-layer whitelist denies")
		return "", nil
	}})

	const agentID = "browser-agent-deny"
	d.dispatcher.SetACL(&capabilities.ACL{
		AgentID: agentID,
		Entries: []capabilities.ACLEntry{{
			CapabilityName: capabilities.BrowserPageReadName,
			Constraints: []capabilities.ScopeConstraint{{
				Entity:      "Link",
				Constraints: capabilities.ConstraintSet{"domain": {"example.com"}},
			}},
		}},
	})

	var out bytes.Buffer
	registerResponsePipe(t, d, agentID, &out)

	args, _ := json.Marshal(capabilities.Browser_Page_Read{URL: "https://evil.com/page"})
	req := capabilities.CapabilityRequestPayload{Capability: capabilities.BrowserPageReadName, Args: args}
	d.handleFrame(switchboard.Frame{Header: switchboard.SwarmHeader{Version: 0, Type: switchboard.MsgType_CapabilityRequest, FromID: agentID, ToID: "keeper", SeqNo: 11}, Payload: req.MarshalMUS()})

	resp := decodeCapabilityResponse(t, &out)
	if resp.OK || resp.ErrorCode != "capability_denied" {
		t.Fatalf("expected capability_denied, got ok=%v code=%s", resp.OK, resp.ErrorCode)
	}

	events := queryDeniedSecurityEvents(t, d, agentID)
	if len(events) != 1 {
		t.Fatalf("want 1 security event, got %d", len(events))
	}
	if !strings.Contains(events[0].Detail, "cap=Browser_Page_Read") {
		t.Fatalf("unexpected detail: %q", events[0].Detail)
	}
	if !strings.Contains(events[0].Detail, "not in browser whitelist") {
		t.Fatalf("unexpected detail: %q", events[0].Detail)
	}
}

func TestBrowserCapabilityRequest_AllowedDoesNotWriteSecurityEvent(t *testing.T) {
	d, _, cancel := newTestDaemon(t)
	defer cancel()
	d.ctx = context.Background()

	d.dispatcher.Register(&capabilities.BrowserPageRead{ChromeProxy: func(_ context.Context, agentID, targetURL string, policy chromproxy.WhitelistPolicy, waitFor string, maxChars int) (string, error) {
		if agentID != "browser-agent-allow" {
			t.Fatalf("agentID = %q", agentID)
		}
		if targetURL != "https://example.com/page" {
			t.Fatalf("targetURL = %q", targetURL)
		}
		if len(policy.Domains) != 1 || policy.Domains[0] != "example.com" {
			t.Fatalf("policy.Domains = %#v", policy.Domains)
		}
		return "browser text", nil
	}})

	const agentID = "browser-agent-allow"
	d.dispatcher.SetACL(&capabilities.ACL{
		AgentID: agentID,
		Entries: []capabilities.ACLEntry{{
			CapabilityName: capabilities.BrowserPageReadName,
			Constraints: []capabilities.ScopeConstraint{{
				Entity:      "Link",
				Constraints: capabilities.ConstraintSet{"domain": {"example.com"}},
			}},
		}},
	})

	var out bytes.Buffer
	registerResponsePipe(t, d, agentID, &out)

	args, _ := json.Marshal(capabilities.Browser_Page_Read{URL: "https://example.com/page"})
	req := capabilities.CapabilityRequestPayload{Capability: capabilities.BrowserPageReadName, Args: args}
	d.handleFrame(switchboard.Frame{Header: switchboard.SwarmHeader{Version: 0, Type: switchboard.MsgType_CapabilityRequest, FromID: agentID, ToID: "keeper", SeqNo: 12}, Payload: req.MarshalMUS()})

	resp := decodeCapabilityResponse(t, &out)
	if !resp.OK {
		t.Fatalf("expected OK response, got code=%s detail=%s", resp.ErrorCode, resp.ErrorDetail)
	}
	if !bytes.Contains(resp.Data, []byte("browser text")) {
		t.Fatalf("expected response data to contain browser text, got %s", resp.Data)
	}

	events := queryDeniedSecurityEvents(t, d, agentID)
	if len(events) != 0 {
		t.Fatalf("expected no capability_denied security events, got %d", len(events))
	}
}

func TestCtlPromptRun_CompletionUpdatesStatusAndAudit(t *testing.T) {
	d, sockPath, cancel := newTestDaemon(t)
	defer cancel()

	const agentID = "prompt-run-agent"
	keeperToWardReader, wardToKeeperWriter := registerIntegrationAgentPipe(t, d, agentID)

	done := make(chan struct{})
	go func() {
		defer close(done)
		hdr, payload, err := switchboard.ReadFrame(keeperToWardReader)
		if err != nil {
			t.Errorf("fake ward read prompt: %v", err)
			return
		}
		if hdr.Type != switchboard.MsgType_CtlPrompt {
			t.Errorf("prompt frame type = %v, want CtlPrompt", hdr.Type)
			return
		}
		var prompt ctl.PromptPayload
		if err := prompt.UnmarshalMUS(bytes.NewReader(payload)); err != nil {
			t.Errorf("decode prompt payload: %v", err)
			return
		}
		if prompt.AgentID != agentID || prompt.Seq != 44 || prompt.Text != "write a page summary" {
			t.Errorf("unexpected prompt payload: %+v", prompt)
			return
		}

		cev := audit.CompletionEvent{
			AgentID:      agentID,
			PromptSeq:    prompt.Seq,
			Model:        "claude-sonnet-4-5",
			InputTokens:  31,
			OutputTokens: 9,
			CostUSD:      0.0007,
			Outcome:      "success",
			ToolCalls:    1,
		}
		b, err := audit.MarshalEvent(&cev)
		if err != nil {
			t.Errorf("marshal completion event: %v", err)
			return
		}
		sendAgentFrame(t, wardToKeeperWriter, agentID, switchboard.MsgType_CompletionEvent, 1, b)
	}()

	c := dialCtl(t, sockPath)
	prompt := ctl.PromptPayload{AgentID: agentID, Seq: 44, Text: "write a page summary"}
	c.send(t, switchboard.MsgType_CtlPrompt, prompt.MarshalMUS())
	hdr, payload := c.recv(t)
	if hdr.Type != switchboard.MsgType_CtlPrompt {
		t.Fatalf("expected CtlPrompt ack, got %v", hdr.Type)
	}
	if len(payload) == 0 || payload[0] != 1 {
		t.Fatalf("prompt ack not ok: %v", payload)
	}
	<-done

	waitForAgentState(t, d, agentID, func(state *agentState) bool {
		return state.lastPromptSeq == 44 && state.lastOutcome == "success" && state.toolCalls == 1 && !state.lastEventAt.IsZero()
	})

	status := fetchCtlStatus(t, sockPath)
	found := findAgentStatus(t, status, agentID)
	if found.LastPromptSeq != 44 {
		t.Fatalf("LastPromptSeq = %d, want 44", found.LastPromptSeq)
	}
	if found.LastOutcome != "success" {
		t.Fatalf("LastOutcome = %q, want success", found.LastOutcome)
	}
	if found.InputTokens != 31 || found.OutputTokens != 9 {
		t.Fatalf("tokens = (%d,%d), want (31,9)", found.InputTokens, found.OutputTokens)
	}

	completionFrames := queryFramesByType(t, d, switchboard.MsgType_CompletionEvent.String(), agentID)
	if len(completionFrames) != 1 {
		t.Fatalf("want 1 completion frame, got %d", len(completionFrames))
	}
	if len(completionFrames[0].Payload) == 0 {
		t.Fatal("completion event payload should be stored in audit log")
	}
	storedCompletion, err := audit.UnmarshalCompletion(completionFrames[0].Payload)
	if err != nil {
		t.Fatalf("decode stored completion payload: %v", err)
	}
	if storedCompletion.PromptSeq != 44 || storedCompletion.ToolCalls != 1 {
		t.Fatalf("unexpected stored completion payload: %+v", storedCompletion)
	}

	promptFrames := queryFramesByType(t, d, switchboard.MsgType_CtlPrompt.String(), ctl.CtlIdentity)
	if len(promptFrames) != 1 {
		t.Fatalf("want 1 ctl prompt frame, got %d", len(promptFrames))
	}
	if len(promptFrames[0].Payload) == 0 {
		t.Fatal("ctl prompt payload should be stored in audit log")
	}
	var storedPrompt ctl.PromptPayload
	if err := storedPrompt.UnmarshalMUS(bytes.NewReader(promptFrames[0].Payload)); err != nil {
		t.Fatalf("decode stored prompt payload: %v", err)
	}
	if storedPrompt.AgentID != agentID || storedPrompt.Seq != 44 {
		t.Fatalf("unexpected stored prompt payload: %+v", storedPrompt)
	}
}

func TestCtlPromptRun_FailureUpdatesStatusAndAudit(t *testing.T) {
	d, sockPath, cancel := newTestDaemon(t)
	defer cancel()

	const agentID = "prompt-run-agent-fail"
	keeperToWardReader, wardToKeeperWriter := registerIntegrationAgentPipe(t, d, agentID)

	done := make(chan struct{})
	go func() {
		defer close(done)
		hdr, payload, err := switchboard.ReadFrame(keeperToWardReader)
		if err != nil {
			t.Errorf("fake ward read prompt: %v", err)
			return
		}
		if hdr.Type != switchboard.MsgType_CtlPrompt {
			t.Errorf("prompt frame type = %v, want CtlPrompt", hdr.Type)
			return
		}
		var prompt ctl.PromptPayload
		if err := prompt.UnmarshalMUS(bytes.NewReader(payload)); err != nil {
			t.Errorf("decode prompt payload: %v", err)
			return
		}
		if prompt.AgentID != agentID || prompt.Seq != 45 || prompt.Text != "summarize the failure" {
			t.Errorf("unexpected prompt payload: %+v", prompt)
			return
		}

		fev := audit.FailureEvent{
			AgentID:   agentID,
			PromptSeq: prompt.Seq,
			Kind:      "malformed_tool_call",
			Detail:    "malformed JSON from tool socket",
		}
		b, err := audit.MarshalEvent(&fev)
		if err != nil {
			t.Errorf("marshal failure event: %v", err)
			return
		}
		sendAgentFrame(t, wardToKeeperWriter, agentID, switchboard.MsgType_FailureEvent, 1, b)
	}()

	c := dialCtl(t, sockPath)
	prompt := ctl.PromptPayload{AgentID: agentID, Seq: 45, Text: "summarize the failure"}
	c.send(t, switchboard.MsgType_CtlPrompt, prompt.MarshalMUS())
	hdr, payload := c.recv(t)
	if hdr.Type != switchboard.MsgType_CtlPrompt {
		t.Fatalf("expected CtlPrompt ack, got %v", hdr.Type)
	}
	if len(payload) == 0 || payload[0] != 1 {
		t.Fatalf("prompt ack not ok: %v", payload)
	}
	<-done

	waitForAgentState(t, d, agentID, func(state *agentState) bool {
		return state.lastPromptSeq == 45 && state.lastOutcome == "malformed_tool_call" && !state.lastEventAt.IsZero()
	})

	status := fetchCtlStatus(t, sockPath)
	found := findAgentStatus(t, status, agentID)
	if found.LastPromptSeq != 45 {
		t.Fatalf("LastPromptSeq = %d, want 45", found.LastPromptSeq)
	}
	if found.LastOutcome != "malformed_tool_call" {
		t.Fatalf("LastOutcome = %q, want malformed_tool_call", found.LastOutcome)
	}
	if found.LastEventAt == "" {
		t.Fatal("LastEventAt should be set after failure event")
	}

	failureFrames := queryFramesByType(t, d, switchboard.MsgType_FailureEvent.String(), agentID)
	if len(failureFrames) != 1 {
		t.Fatalf("want 1 failure frame, got %d", len(failureFrames))
	}
	if len(failureFrames[0].Payload) == 0 {
		t.Fatal("failure event payload should be stored in audit log")
	}
	storedFailure, err := audit.UnmarshalFailure(failureFrames[0].Payload)
	if err != nil {
		t.Fatalf("decode stored failure payload: %v", err)
	}
	if storedFailure.PromptSeq != 45 || storedFailure.Kind != "malformed_tool_call" {
		t.Fatalf("unexpected stored failure payload: %+v", storedFailure)
	}

	promptFrames := queryFramesByType(t, d, switchboard.MsgType_CtlPrompt.String(), ctl.CtlIdentity)
	if len(promptFrames) != 1 {
		t.Fatalf("want 1 ctl prompt frame, got %d", len(promptFrames))
	}
	if len(promptFrames[0].Payload) == 0 {
		t.Fatal("ctl prompt payload should be stored in audit log")
	}
	var storedPrompt ctl.PromptPayload
	if err := storedPrompt.UnmarshalMUS(bytes.NewReader(promptFrames[0].Payload)); err != nil {
		t.Fatalf("decode stored prompt payload: %v", err)
	}
	if storedPrompt.AgentID != agentID || storedPrompt.Seq != 45 {
		t.Fatalf("unexpected stored prompt payload: %+v", storedPrompt)
	}
}

func TestCtlPromptRun_BrowserAllowRoundTrip(t *testing.T) {
	d, sockPath, cancel := newTestDaemon(t)
	defer cancel()
	d.ctx = context.Background()

	d.dispatcher.Register(&capabilities.BrowserPageRead{ChromeProxy: func(_ context.Context, agentID, targetURL string, policy chromproxy.WhitelistPolicy, waitFor string, maxChars int) (string, error) {
		if agentID != "browser-prompt-allow" {
			t.Fatalf("agentID = %q", agentID)
		}
		if targetURL != "https://example.com/page" {
			t.Fatalf("targetURL = %q", targetURL)
		}
		if len(policy.Domains) != 1 || policy.Domains[0] != "example.com" {
			t.Fatalf("policy.Domains = %#v", policy.Domains)
		}
		return "browser text", nil
	}})

	const agentID = "browser-prompt-allow"
	d.dispatcher.SetACL(&capabilities.ACL{
		AgentID: agentID,
		Entries: []capabilities.ACLEntry{{
			CapabilityName: capabilities.BrowserPageReadName,
			Constraints: []capabilities.ScopeConstraint{{
				Entity:      "Link",
				Constraints: capabilities.ConstraintSet{"domain": {"example.com"}},
			}},
		}},
	})

	keeperToWardReader, wardToKeeperWriter := registerIntegrationAgentPipe(t, d, agentID)
	done := make(chan struct{})
	go func() {
		defer close(done)
		hdr, _, err := switchboard.ReadFrame(keeperToWardReader)
		if err != nil {
			t.Errorf("fake ward read prompt: %v", err)
			return
		}
		if hdr.Type != switchboard.MsgType_CtlPrompt {
			t.Errorf("prompt frame type = %v, want CtlPrompt", hdr.Type)
			return
		}

		req := capabilities.CapabilityRequestPayload{
			Capability: capabilities.BrowserPageReadName,
			Args:       []byte(`{"url":"https://example.com/page"}`),
		}
		sendAgentFrame(t, wardToKeeperWriter, agentID, switchboard.MsgType_CapabilityRequest, 1, req.MarshalMUS())

		respHdr, respPayload, err := switchboard.ReadFrame(keeperToWardReader)
		if err != nil {
			t.Errorf("fake ward read capability response: %v", err)
			return
		}
		if respHdr.Type != switchboard.MsgType_CapabilityResponse {
			t.Errorf("response frame type = %v, want CapabilityResponse", respHdr.Type)
			return
		}
		var resp capabilities.CapabilityResponsePayload
		if err := resp.UnmarshalMUS(bytes.NewReader(respPayload)); err != nil {
			t.Errorf("decode capability response: %v", err)
			return
		}
		if !resp.OK || !bytes.Contains(resp.Data, []byte("browser text")) {
			t.Errorf("unexpected capability response: ok=%v data=%s code=%s detail=%s", resp.OK, resp.Data, resp.ErrorCode, resp.ErrorDetail)
			return
		}

		cev := audit.CompletionEvent{AgentID: agentID, PromptSeq: 46, Outcome: "success", ToolCalls: 1}
		b, err := audit.MarshalEvent(&cev)
		if err != nil {
			t.Errorf("marshal completion event: %v", err)
			return
		}
		sendAgentFrame(t, wardToKeeperWriter, agentID, switchboard.MsgType_CompletionEvent, 2, b)
	}()

	c := dialCtl(t, sockPath)
	prompt := ctl.PromptPayload{AgentID: agentID, Seq: 46, Text: "read the page"}
	c.send(t, switchboard.MsgType_CtlPrompt, prompt.MarshalMUS())
	hdr, payload := c.recv(t)
	if hdr.Type != switchboard.MsgType_CtlPrompt || len(payload) == 0 || payload[0] != 1 {
		t.Fatalf("prompt ack not ok: hdr=%v payload=%v", hdr.Type, payload)
	}
	<-done

	status := fetchCtlStatus(t, sockPath)
	found := findAgentStatus(t, status, agentID)
	if found.LastOutcome != "success" {
		t.Fatalf("LastOutcome = %q, want success", found.LastOutcome)
	}

	denials := queryDeniedSecurityEvents(t, d, agentID)
	if len(denials) != 0 {
		t.Fatalf("expected no denial events, got %d", len(denials))
	}
}

func TestCtlPromptRun_BrowserDenyRoundTrip(t *testing.T) {
	d, sockPath, cancel := newTestDaemon(t)
	defer cancel()
	d.ctx = context.Background()

	d.dispatcher.Register(&capabilities.BrowserPageRead{ChromeProxy: func(context.Context, string, string, chromproxy.WhitelistPolicy, string, int) (string, error) {
		t.Fatal("proxy should not be called when browser scope denies")
		return "", nil
	}})

	const agentID = "browser-prompt-deny"
	d.dispatcher.SetACL(&capabilities.ACL{
		AgentID: agentID,
		Entries: []capabilities.ACLEntry{{
			CapabilityName: capabilities.BrowserPageReadName,
			Constraints: []capabilities.ScopeConstraint{{
				Entity:      "Link",
				Constraints: capabilities.ConstraintSet{"domain": {"example.com"}},
			}},
		}},
	})

	keeperToWardReader, wardToKeeperWriter := registerIntegrationAgentPipe(t, d, agentID)
	done := make(chan struct{})
	go func() {
		defer close(done)
		hdr, _, err := switchboard.ReadFrame(keeperToWardReader)
		if err != nil {
			t.Errorf("fake ward read prompt: %v", err)
			return
		}
		if hdr.Type != switchboard.MsgType_CtlPrompt {
			t.Errorf("prompt frame type = %v, want CtlPrompt", hdr.Type)
			return
		}

		req := capabilities.CapabilityRequestPayload{
			Capability: capabilities.BrowserPageReadName,
			Args:       []byte(`{"url":"https://evil.com/page"}`),
		}
		sendAgentFrame(t, wardToKeeperWriter, agentID, switchboard.MsgType_CapabilityRequest, 1, req.MarshalMUS())

		respHdr, respPayload, err := switchboard.ReadFrame(keeperToWardReader)
		if err != nil {
			t.Errorf("fake ward read capability response: %v", err)
			return
		}
		if respHdr.Type != switchboard.MsgType_CapabilityResponse {
			t.Errorf("response frame type = %v, want CapabilityResponse", respHdr.Type)
			return
		}
		var resp capabilities.CapabilityResponsePayload
		if err := resp.UnmarshalMUS(bytes.NewReader(respPayload)); err != nil {
			t.Errorf("decode capability response: %v", err)
			return
		}
		if resp.OK || resp.ErrorCode != "capability_denied" {
			t.Errorf("unexpected denial response: ok=%v code=%s detail=%s", resp.OK, resp.ErrorCode, resp.ErrorDetail)
			return
		}

		fev := audit.FailureEvent{AgentID: agentID, PromptSeq: 47, Kind: "capability_denied", Detail: resp.ErrorDetail}
		b, err := audit.MarshalEvent(&fev)
		if err != nil {
			t.Errorf("marshal failure event: %v", err)
			return
		}
		sendAgentFrame(t, wardToKeeperWriter, agentID, switchboard.MsgType_FailureEvent, 2, b)
	}()

	c := dialCtl(t, sockPath)
	prompt := ctl.PromptPayload{AgentID: agentID, Seq: 47, Text: "try the denied page"}
	c.send(t, switchboard.MsgType_CtlPrompt, prompt.MarshalMUS())
	hdr, payload := c.recv(t)
	if hdr.Type != switchboard.MsgType_CtlPrompt || len(payload) == 0 || payload[0] != 1 {
		t.Fatalf("prompt ack not ok: hdr=%v payload=%v", hdr.Type, payload)
	}
	<-done

	status := fetchCtlStatus(t, sockPath)
	found := findAgentStatus(t, status, agentID)
	if found.LastOutcome != "capability_denied" {
		t.Fatalf("LastOutcome = %q, want capability_denied", found.LastOutcome)
	}

	denials := queryDeniedSecurityEvents(t, d, agentID)
	if len(denials) != 1 {
		t.Fatalf("expected 1 denial event, got %d", len(denials))
	}
	if !strings.Contains(denials[0].Detail, "cap=Browser_Page_Read") {
		t.Fatalf("unexpected denial detail: %q", denials[0].Detail)
	}
}

// TestAuditPayloadPolicy verifies that shouldAuditPayload correctly delegates
// to the Capability.AuditPayload() method for CapabilityRequest frames.
func TestAuditPayloadPolicy_KnownCapabilityTrue(t *testing.T) {
	d, _, cancel := newTestDaemon(t)
	defer cancel()

	// FilesystemFileWrite.AuditPayload() == true, so payload should be stored.
	payload := capabilities.CapabilityRequestPayload{
		Capability: capabilities.FilesystemFileWriteName,
		Args:       []byte(`{}`),
	}
	f := switchboard.Frame{
		Header:  switchboard.SwarmHeader{Type: switchboard.MsgType_CapabilityRequest},
		Payload: payload.MarshalMUS(),
	}
	if !d.shouldAuditPayload(f) {
		t.Error("expected shouldAuditPayload=true for FilesystemFileWrite")
	}
}

func TestAuditPayloadPolicy_UnknownCapabilityConservative(t *testing.T) {
	d, _, cancel := newTestDaemon(t)
	defer cancel()

	// Unknown capability name should default to true (audit conservatively).
	payload := capabilities.CapabilityRequestPayload{
		Capability: "Email_Message_Send",
		Args:       []byte(`{}`),
	}
	f := switchboard.Frame{
		Header:  switchboard.SwarmHeader{Type: switchboard.MsgType_CapabilityRequest},
		Payload: payload.MarshalMUS(),
	}
	if !d.shouldAuditPayload(f) {
		t.Error("expected shouldAuditPayload=true for unknown capability (conservative fallback)")
	}
}

func TestAuditPayloadPolicy_NonCapabilityFrameAlwaysTrue(t *testing.T) {
	d, _, cancel := newTestDaemon(t)
	defer cancel()

	for _, msgType := range []switchboard.MsgType{
		switchboard.MsgType_CompletionEvent,
		switchboard.MsgType_FailureEvent,
		switchboard.MsgType_Ping,
	} {
		f := switchboard.Frame{Header: switchboard.SwarmHeader{Type: msgType}}
		if !d.shouldAuditPayload(f) {
			t.Errorf("expected shouldAuditPayload=true for %v (telemetry frame)", msgType)
		}
	}
}

// TestAuditPayloadPolicy_SuppressedCapability verifies that a capability
// with AuditPayload()==false causes shouldAuditPayload to return false.
// We register a stub capability that suppresses payload logging.
func TestAuditPayloadPolicy_SuppressedCapability(t *testing.T) {
	d, _, cancel := newTestDaemon(t)
	defer cancel()

	// Register a stub with AuditPayload()==false.
	d.dispatcher.Register(&suppressedCap{})

	payload := capabilities.CapabilityRequestPayload{
		Capability: "Test_Sensitive_Read",
		Args:       []byte(`{}`),
	}
	f := switchboard.Frame{
		Header:  switchboard.SwarmHeader{Type: switchboard.MsgType_CapabilityRequest},
		Payload: payload.MarshalMUS(),
	}
	if d.shouldAuditPayload(f) {
		t.Error("expected shouldAuditPayload=false for capability with AuditPayload()==false")
	}
}

func TestAuditPayloadPolicy_SuppressedCapability_OmitsStoredPayload(t *testing.T) {
	d, _, cancel := newTestDaemon(t)
	defer cancel()
	d.ctx = context.Background()

	d.dispatcher.Register(&suppressedCap{})
	const agentID = "audit-suppressed-agent"
	d.dispatcher.SetACL(&capabilities.ACL{
		AgentID: agentID,
		Entries: []capabilities.ACLEntry{{
			CapabilityName: "Test_Sensitive_Read",
		}},
	})

	var out bytes.Buffer
	registerResponsePipe(t, d, agentID, &out)

	args := []byte(`{"secret":"top-secret"}`)
	req := capabilities.CapabilityRequestPayload{Capability: "Test_Sensitive_Read", Args: args}
	d.handleFrame(switchboard.Frame{
		Header:  switchboard.SwarmHeader{Version: 0, Type: switchboard.MsgType_CapabilityRequest, FromID: agentID, ToID: "keeper", SeqNo: 77},
		Payload: req.MarshalMUS(),
	})

	resp := decodeCapabilityResponse(t, &out)
	if !resp.OK {
		t.Fatalf("expected OK response, got code=%s detail=%s", resp.ErrorCode, resp.ErrorDetail)
	}

	frames := queryFramesByType(t, d, switchboard.MsgType_CapabilityRequest.String(), agentID)
	if len(frames) != 1 {
		t.Fatalf("want 1 capability request frame, got %d", len(frames))
	}
	if len(frames[0].Payload) != 0 {
		t.Fatalf("expected suppressed payload to be omitted, got %d bytes", len(frames[0].Payload))
	}
}

func TestAuditPayloadPolicy_KnownCapability_StoresPayload(t *testing.T) {
	d, _, cancel := newTestDaemon(t)
	defer cancel()
	d.ctx = context.Background()

	const agentID = "audit-filesystem-agent"
	d.dispatcher.SetACL(&capabilities.ACL{
		AgentID: agentID,
		Entries: []capabilities.ACLEntry{{
			CapabilityName: capabilities.FilesystemFileWriteName,
			Constraints: []capabilities.ScopeConstraint{{
				Entity:      "File",
				Constraints: capabilities.ConstraintSet{"path-prefix": {t.TempDir()}},
			}},
		}},
	})

	var out bytes.Buffer
	registerResponsePipe(t, d, agentID, &out)

	args := []byte(`{"path":"note.txt","content":"hello"}`)
	req := capabilities.CapabilityRequestPayload{Capability: capabilities.FilesystemFileWriteName, Args: args}
	encoded := req.MarshalMUS()
	d.handleFrame(switchboard.Frame{
		Header:  switchboard.SwarmHeader{Version: 0, Type: switchboard.MsgType_CapabilityRequest, FromID: agentID, ToID: "keeper", SeqNo: 78},
		Payload: encoded,
	})

	resp := decodeCapabilityResponse(t, &out)
	if !resp.OK {
		t.Fatalf("expected OK response, got code=%s detail=%s", resp.ErrorCode, resp.ErrorDetail)
	}

	frames := queryFramesByType(t, d, switchboard.MsgType_CapabilityRequest.String(), agentID)
	if len(frames) != 1 {
		t.Fatalf("want 1 capability request frame, got %d", len(frames))
	}
	if !bytes.Equal(frames[0].Payload, encoded) {
		t.Fatalf("expected stored payload to match original request bytes")
	}
}

// suppressedCap is a test-only capability with AuditPayload()==false.
type suppressedCap struct{}

func (s *suppressedCap) Name() string       { return "Test_Sensitive_Read" }
func (s *suppressedCap) Explain() string    { return "{}" }
func (s *suppressedCap) AuditPayload() bool { return false }
func (s *suppressedCap) Execute(_ context.Context, _ capabilities.Request) (capabilities.Response, error) {
	return capabilities.Response{OK: true}, nil
}

// TestCtlSocket_MultipleCommands verifies sequential requests on one connection.
func TestCtlSocket_MultipleCommands(t *testing.T) {
	_, sockPath, cancel := newTestDaemon(t)
	defer cancel()

	c := dialCtl(t, sockPath)

	// Ping
	c.send(t, switchboard.MsgType_Ping, nil)
	hdr, _ := c.recv(t)
	if hdr.Type != switchboard.MsgType_Pong {
		t.Errorf("step 1: want Pong, got %v", hdr.Type)
	}

	// Status
	c.send(t, switchboard.MsgType_CtlStatus, nil)
	hdr, _ = c.recv(t)
	if hdr.Type != switchboard.MsgType_CtlStatus {
		t.Errorf("step 2: want CtlStatus, got %v", hdr.Type)
	}

	// AgentList
	c.send(t, switchboard.MsgType_CtlAgentList, nil)
	hdr, _ = c.recv(t)
	if hdr.Type != switchboard.MsgType_CtlAgentList {
		t.Errorf("step 3: want CtlAgentList, got %v", hdr.Type)
	}
}
