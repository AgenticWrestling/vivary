package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"vivary.dev/vivary/internal/audit"
	"vivary.dev/vivary/internal/capabilities"
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

// suppressedCap is a test-only capability with AuditPayload()==false.
type suppressedCap struct{}

func (s *suppressedCap) Name() string                                              { return "Test_Sensitive_Read" }
func (s *suppressedCap) Explain() string                                           { return "{}" }
func (s *suppressedCap) AuditPayload() bool                                        { return false }
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
