package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"vivary.dev/vivary/internal/audit"
	"vivary.dev/vivary/internal/capabilities"
	agentruntime "vivary.dev/vivary/internal/runtime"
	"vivary.dev/vivary/internal/switchboard"
	"vivary.dev/vivary/internal/ctl"
	"vivary.dev/vivary/pkg/mus"
)

// noopWard returns a SpawnFunc that spawns a subprocess which exits immediately.
// On all POSIX systems "true" exits 0; on test environments without it, we
// use "sh -c exit 0" as fallback — but "true" is always available in CI.
func noopWardFunc(_ context.Context, agentID, subvolPath string, cfg agentruntime.WardConfig) (*exec.Cmd, error) {
	return exec.Command("true"), nil
}

// newTestDaemonWithRuntime creates an in-process daemon backed by a StubRuntime
// so that provisioning calls can be exercised without nspawn or btrfs.
func newTestDaemonWithRuntime(t *testing.T) (*daemon, string, context.CancelFunc) {
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
	reg.Register(&capabilities.BrowserPageRead{})
	dispatcher := capabilities.NewDispatcher(reg)

	stub := &agentruntime.StubRuntime{
		ProvisionFunc: func(agentID, _ string) (string, error) {
			p := filepath.Join(dir, "agents", agentID)
			if err := os.MkdirAll(filepath.Join(p, "output"), 0o750); err != nil {
				return "", err
			}
			return p, nil
		},
		SpawnFunc: noopWardFunc,
	}

	log := newLogger("error")
	router := switchboard.NewRouter(log, func(ev switchboard.SecurityEvent) {})

	cfg := DefaultOrchestratorConfig(dir)
	cfg.SocketPath = sockPath

	d := &daemon{
		cfg:        cfg,
		log:        log,
		auditDB:    auditDB,
		dispatcher: dispatcher,
		router:     router,
		runtime:    stub,
		agents:     make(map[string]*agentState),
		startedAt:  time.Now(),
	}
	router.RegisterHandler(d.handleFrame)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

	if err := d.listenCtl(ctx); err != nil {
		cancel()
		t.Fatalf("listenCtl: %v", err)
	}

	// Give the socket a moment to appear.
	time.Sleep(20 * time.Millisecond)

	return d, sockPath, cancel
}

// sendCreate sends a CtlAgentCreate and returns the response payload.
func sendCreate(t *testing.T, c *ctlClient, id string) []byte {
	t.Helper()
	req := ctl.AgentCreatePayload{ID: id}
	c.send(t, switchboard.MsgType_CtlAgentCreate, req.MarshalMUS())
	hdr, payload := c.recv(t)
	if hdr.Type != switchboard.MsgType_CtlAgentCreate {
		t.Fatalf("expected CtlAgentCreate response, got %v", hdr.Type)
	}
	return payload
}

// sendDestroy sends a CtlAgentDestroy and returns the response payload.
func sendDestroy(t *testing.T, c *ctlClient, id string) []byte {
	t.Helper()
	req := ctl.AgentDestroyPayload{ID: id}
	c.send(t, switchboard.MsgType_CtlAgentDestroy, req.MarshalMUS())
	hdr, payload := c.recv(t)
	if hdr.Type != switchboard.MsgType_CtlAgentDestroy {
		t.Fatalf("expected CtlAgentDestroy response, got %v", hdr.Type)
	}
	return payload
}

// TestProvisioningRoundTrip: create an agent and immediately destroy it.
func TestProvisioningRoundTrip(t *testing.T) {
	d, sockPath, cancel := newTestDaemonWithRuntime(t)
	defer cancel()

	c := dialCtl(t, sockPath)

	// Create.
	payload := sendCreate(t, c, "agent-rt-1")
	if len(payload) == 0 || payload[0] != 1 {
		errStr, _ := mus.ReadString(bytes.NewReader(payload[1:]), 1024)
		t.Fatalf("agentCreate failed: %s", errStr)
	}

	d.mu.Lock()
	_, exists := d.agents["agent-rt-1"]
	d.mu.Unlock()
	if !exists {
		t.Fatal("agent-rt-1 not in daemon state after create")
	}

	// Destroy.
	payload = sendDestroy(t, c, "agent-rt-1")
	if len(payload) == 0 || payload[0] != 1 {
		errStr, _ := mus.ReadString(bytes.NewReader(payload[1:]), 1024)
		t.Fatalf("agentDestroy failed: %s", errStr)
	}

	d.mu.Lock()
	_, stillExists := d.agents["agent-rt-1"]
	d.mu.Unlock()
	if stillExists {
		t.Fatal("agent-rt-1 still in daemon state after destroy")
	}
}

// TestProvisioningDuplicateCreate: creating the same agent ID twice must fail.
func TestProvisioningDuplicateCreate(t *testing.T) {
	_, sockPath, cancel := newTestDaemonWithRuntime(t)
	defer cancel()

	c := dialCtl(t, sockPath)

	payload := sendCreate(t, c, "agent-dup")
	if len(payload) == 0 || payload[0] != 1 {
		t.Fatalf("first create unexpectedly failed")
	}

	payload = sendCreate(t, c, "agent-dup")
	if len(payload) == 0 || payload[0] != 0 {
		t.Fatal("second create for same ID should have failed but succeeded")
	}
}

// TestProvisioningDestroyUnknown: destroying an agent that doesn't exist must fail.
func TestProvisioningDestroyUnknown(t *testing.T) {
	_, sockPath, cancel := newTestDaemonWithRuntime(t)
	defer cancel()

	c := dialCtl(t, sockPath)

	payload := sendDestroy(t, c, "no-such-agent")
	if len(payload) == 0 || payload[0] != 0 {
		t.Fatal("destroy of unknown agent should have failed but succeeded")
	}
}

// TestProvisioningACLCleanupOnDestroy: after destroy the ACL entry must be gone.
func TestProvisioningACLCleanupOnDestroy(t *testing.T) {
	d, sockPath, cancel := newTestDaemonWithRuntime(t)
	defer cancel()

	c := dialCtl(t, sockPath)

	payload := sendCreate(t, c, "agent-acl")
	if len(payload) == 0 || payload[0] != 1 {
		t.Fatalf("create failed")
	}

	// Capability should be allowed while agent is alive.
	resp, err := d.dispatcher.Dispatch(context.Background(), capabilities.Request{
		Name:    capabilities.FilesystemFileWriteName,
		AgentID: "agent-acl",
		SeqNo:   1,
		Args:    []byte(`{"path":"x.txt","content":"y"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	// ACL allows it (but scope check will pass since provisioning sets a real subvol path-prefix)
	if resp.ErrorCode == "capability_denied" && resp.ErrorDetail == "no ACL registered for agent \"agent-acl\"" {
		t.Fatal("ACL should be installed after create")
	}

	payload = sendDestroy(t, c, "agent-acl")
	if len(payload) == 0 || payload[0] != 1 {
		t.Fatalf("destroy failed")
	}

	// After destroy, dispatching for agent-acl should get no-ACL denial.
	resp, err = d.dispatcher.Dispatch(context.Background(), capabilities.Request{
		Name:    capabilities.FilesystemFileWriteName,
		AgentID: "agent-acl",
		SeqNo:   1,
		Args:    []byte(`{"path":"x.txt","content":"y"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || resp.ErrorCode != "capability_denied" {
		t.Fatalf("expected capability_denied after destroy, got ok=%v code=%s", resp.OK, resp.ErrorCode)
	}
}
