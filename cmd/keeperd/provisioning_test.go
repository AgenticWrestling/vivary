package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"vivary.dev/vivary/internal/audit"
	"vivary.dev/vivary/internal/capabilities"
	chromedapi "vivary.dev/vivary/internal/chromed"
	"vivary.dev/vivary/internal/chromproxy"
	"vivary.dev/vivary/internal/ctl"
	agentruntime "vivary.dev/vivary/internal/runtime"
	"vivary.dev/vivary/internal/switchboard"
	"vivary.dev/vivary/pkg/mus"
)

type fakeBrowserReleaseClient struct{ releaseCount int }

func (f *fakeBrowserReleaseClient) Acquire(context.Context, chromedapi.AcquireRequest) (chromedapi.AcquireResponse, error) {
	return chromedapi.AcquireResponse{}, nil
}
func (f *fakeBrowserReleaseClient) Release(context.Context, string) (chromedapi.ReleaseResponse, error) {
	f.releaseCount++
	return chromedapi.ReleaseResponse{OK: true}, nil
}
func (f *fakeBrowserReleaseClient) Status(context.Context, string) (chromedapi.StatusResponse, error) {
	return chromedapi.StatusResponse{}, nil
}

type recordingRuntime struct {
	provisionSubvolume func(agentID, templatePath string) (string, error)
	installCLIs        func(subvolPath string, capNames []string) error
	spawnWard          func(ctx context.Context, agentID, subvolPath string, cfg agentruntime.WardConfig) (*exec.Cmd, error)
	applyNetworkRules  func(agentID string, allowedIPs []string) error
	removeNetworkRules func(agentID string) error
	destroySubvolume   func(agentID, subvolPath string) error
	terminate          func(agentID string) error

	mu                sync.Mutex
	destroyedSubvols  []string
	removedNetworks   []string
	terminatedAgents  []string
	installedSubvols  []string
	spawnedAgents     []string
	appliedRuleAgents []string
	appliedAllowedIPs [][]string
}

func (r *recordingRuntime) ProvisionSubvolume(agentID, templatePath string) (string, error) {
	if r.provisionSubvolume != nil {
		return r.provisionSubvolume(agentID, templatePath)
	}
	return "", fmt.Errorf("ProvisionSubvolume not configured")
}

func (r *recordingRuntime) DestroySubvolume(agentID, subvolPath string) error {
	r.mu.Lock()
	r.destroyedSubvols = append(r.destroyedSubvols, subvolPath)
	r.mu.Unlock()
	if r.destroySubvolume != nil {
		return r.destroySubvolume(agentID, subvolPath)
	}
	return nil
}

func (r *recordingRuntime) InstallCapabilityCLIs(subvolPath string, capNames []string) error {
	r.mu.Lock()
	r.installedSubvols = append(r.installedSubvols, subvolPath)
	r.mu.Unlock()
	if r.installCLIs != nil {
		return r.installCLIs(subvolPath, capNames)
	}
	return nil
}

func (r *recordingRuntime) SpawnWard(ctx context.Context, agentID, subvolPath string, cfg agentruntime.WardConfig) (*exec.Cmd, error) {
	r.mu.Lock()
	r.spawnedAgents = append(r.spawnedAgents, agentID)
	r.mu.Unlock()
	if r.spawnWard != nil {
		return r.spawnWard(ctx, agentID, subvolPath, cfg)
	}
	return nil, fmt.Errorf("SpawnWard not configured")
}

func (r *recordingRuntime) ApplyNetworkRules(agentID string, allowedIPs []string) error {
	r.mu.Lock()
	r.appliedRuleAgents = append(r.appliedRuleAgents, agentID)
	r.appliedAllowedIPs = append(r.appliedAllowedIPs, append([]string(nil), allowedIPs...))
	r.mu.Unlock()
	if r.applyNetworkRules != nil {
		return r.applyNetworkRules(agentID, allowedIPs)
	}
	return nil
}

func (r *recordingRuntime) RemoveNetworkRules(agentID string) error {
	r.mu.Lock()
	r.removedNetworks = append(r.removedNetworks, agentID)
	r.mu.Unlock()
	if r.removeNetworkRules != nil {
		return r.removeNetworkRules(agentID)
	}
	return nil
}

func (r *recordingRuntime) Terminate(agentID string) error {
	r.mu.Lock()
	r.terminatedAgents = append(r.terminatedAgents, agentID)
	r.mu.Unlock()
	if r.terminate != nil {
		return r.terminate(agentID)
	}
	return nil
}

func TestLoadTemplateAgentConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "agent.kdl"), []byte("id \"template-agent\"\nbrowser {\n    headless true\n}\n"), 0o644); err != nil {
		t.Fatalf("write agent.kdl: %v", err)
	}
	cfg, err := loadTemplateAgentConfig(root)
	if err != nil {
		t.Fatalf("loadTemplateAgentConfig: %v", err)
	}
	if !cfg.Browser.Headless {
		t.Fatal("Headless = false, want true")
	}
}

func TestWriteAgentKDL_WritesBrowserSettings(t *testing.T) {
	root := t.TempDir()
	if err := writeAgentKDL(root, AgentConfig{ID: "agent-browser", Browser: AgentBrowserConfig{Headless: true}, CPUShares: 1024, SchemaErrorRetries: 2}); err != nil {
		t.Fatalf("writeAgentKDL: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "agent.kdl"))
	if err != nil {
		t.Fatalf("read agent.kdl: %v", err)
	}
	if !strings.Contains(string(data), "browser {") || !strings.Contains(string(data), "headless true") {
		t.Fatalf("agent.kdl missing browser headless block:\n%s", string(data))
	}
	if !strings.Contains(string(data), "schema-error-retries 2") {
		t.Fatalf("agent.kdl missing schema-error-retries:\n%s", string(data))
	}
}

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
	reg.Register(&capabilities.BrowserPageRead{
		ChromeProxy: func(_ context.Context, agentID, targetURL string, policy chromproxy.WhitelistPolicy, waitFor string, maxChars int) (string, error) {
			return "stub text", nil
		},
	})
	dispatcher := capabilities.NewDispatcher(reg)

	stub := &agentruntime.StubRuntime{
		ProvisionFunc: func(agentID, template string) (string, error) {
			p := filepath.Join(dir, "agents", agentID)
			if err := os.MkdirAll(filepath.Join(p, "output"), 0o750); err != nil {
				return "", err
			}
			if template != "" {
				tplKDL := filepath.Join(template, "agent.kdl")
				if _, err := os.Stat(tplKDL); err == nil {
					data, _ := os.ReadFile(tplKDL)
					_ = os.WriteFile(filepath.Join(p, "agent.kdl"), data, 0o644)
				}
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
func TestProvisioning_InstallsACLFromTemplate(t *testing.T) {
	d, sockPath, cancel := newTestDaemonWithRuntime(t)
	defer cancel()

	// Prepare a template with specific capabilities and scopes.
	root := t.TempDir()
	kdl := `id "tpl"
capabilities "Browser_Page_Read" {
    Link {
        domain "example.com"
        domain-suffix "wikipedia.org"
        path-prefix "/wiki"
    }
}
`
	if err := os.WriteFile(filepath.Join(root, "agent.kdl"), []byte(kdl), 0o644); err != nil {
		t.Fatal(err)
	}

	c := dialCtl(t, sockPath)
	req := ctl.AgentCreatePayload{ID: "agent-scoped", Template: root}
	c.send(t, switchboard.MsgType_CtlAgentCreate, req.MarshalMUS())
	_, payload := c.recv(t)
	if len(payload) == 0 || payload[0] != 1 {
		t.Fatalf("create failed")
	}

	// Verify the ACL entry.
	resp, err := d.dispatcher.Dispatch(context.Background(), capabilities.Request{
		Name:    capabilities.BrowserPageReadName,
		AgentID: "agent-scoped",
		SeqNo:   1,
		Args:    (&capabilities.Browser_Page_Read{URL: "https://en.wikipedia.org/wiki/Go"}).MarshalMUS(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("expected allowed, got %s %s", resp.ErrorCode, resp.ErrorDetail)
	}

	// Verify denial for out-of-scope URL.
	resp, err = d.dispatcher.Dispatch(context.Background(), capabilities.Request{
		Name:    capabilities.BrowserPageReadName,
		AgentID: "agent-scoped",
		SeqNo:   2,
		Args:    (&capabilities.Browser_Page_Read{URL: "https://en.wikipedia.org/other"}).MarshalMUS(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK {
		t.Fatal("expected denial for out-of-scope URL")
	}
}

func TestProvisioning_ProvisionSubvolumeFailure(t *testing.T) {
	d, _, cancel := newTestDaemonWithRuntime(t)
	defer cancel()

	rt := &recordingRuntime{
		provisionSubvolume: func(agentID, templatePath string) (string, error) {
			return "", fmt.Errorf("btrfs boom")
		},
	}
	d.runtime = rt

	err := d.agentCreate(context.Background(), ctl.AgentCreatePayload{ID: "agent-subvol-fail"})
	if err == nil || !strings.Contains(err.Error(), "provision subvolume") {
		t.Fatalf("expected provision subvolume error, got %v", err)
	}

	d.mu.RLock()
	_, exists := d.agents["agent-subvol-fail"]
	d.mu.RUnlock()
	if exists {
		t.Fatal("agent should not exist after subvolume failure")
	}
}

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
		Args:    (&capabilities.Filesystem_File_Write{Path: "x.txt", Content: "y"}).MarshalMUS(),
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
		Args:    (&capabilities.Filesystem_File_Write{Path: "x.txt", Content: "y"}).MarshalMUS(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || resp.ErrorCode != "capability_denied" {
		t.Fatalf("expected capability_denied after destroy, got ok=%v code=%s", resp.OK, resp.ErrorCode)
	}
}

func TestProvisioningSpawnFailureCleansUpStateACLAndSubvolume(t *testing.T) {
	d, _, cancel := newTestDaemonWithRuntime(t)
	defer cancel()

	subvolPath := filepath.Join(t.TempDir(), "agents", "agent-fail")
	if err := os.MkdirAll(filepath.Join(subvolPath, "output"), 0o750); err != nil {
		t.Fatal(err)
	}
	rt := &recordingRuntime{
		provisionSubvolume: func(agentID, templatePath string) (string, error) { return subvolPath, nil },
		spawnWard: func(ctx context.Context, agentID, subvolPath string, cfg agentruntime.WardConfig) (*exec.Cmd, error) {
			return nil, fmt.Errorf("spawn boom")
		},
	}
	d.runtime = rt
	browserClient := &fakeBrowserReleaseClient{}
	d.browserMgr = newBrowserManager(browserClient, nil, nil)

	err := d.agentCreate(context.Background(), ctl.AgentCreatePayload{ID: "agent-fail"})
	if err == nil || !strings.Contains(err.Error(), "spawn agent") {
		t.Fatalf("expected spawn agent error, got %v", err)
	}

	d.mu.RLock()
	_, exists := d.agents["agent-fail"]
	d.mu.RUnlock()
	if exists {
		t.Fatal("agent should be removed from daemon state after spawn failure")
	}

	resp, dispatchErr := d.dispatcher.Dispatch(context.Background(), capabilities.Request{
		Name:    capabilities.FilesystemFileWriteName,
		AgentID: "agent-fail",
		SeqNo:   1,
		Args:    (&capabilities.Filesystem_File_Write{Path: "x.txt", Content: "y"}).MarshalMUS(),
	})
	if dispatchErr != nil {
		t.Fatal(dispatchErr)
	}
	if resp.OK || resp.ErrorCode != "capability_denied" || resp.ErrorDetail != `no ACL registered for agent "agent-fail"` {
		t.Fatalf("expected ACL cleanup after failure, got ok=%v code=%s detail=%s", resp.OK, resp.ErrorCode, resp.ErrorDetail)
	}

	rt.mu.Lock()
	destroyed := append([]string(nil), rt.destroyedSubvols...)
	rt.mu.Unlock()
	if len(destroyed) != 1 || destroyed[0] != subvolPath {
		t.Fatalf("expected subvolume cleanup for %q, got %#v", subvolPath, destroyed)
	}
}

func TestProvisioningCapabilityInstallFailureCleansUpStateAndSubvolume(t *testing.T) {
	d, _, cancel := newTestDaemonWithRuntime(t)
	defer cancel()

	subvolPath := filepath.Join(t.TempDir(), "agents", "agent-install-fail")
	if err := os.MkdirAll(filepath.Join(subvolPath, "output"), 0o750); err != nil {
		t.Fatal(err)
	}
	rt := &recordingRuntime{
		provisionSubvolume: func(agentID, templatePath string) (string, error) { return subvolPath, nil },
		installCLIs: func(subvolPath string, capNames []string) error {
			return fmt.Errorf("install boom")
		},
	}
	d.runtime = rt

	err := d.agentCreate(context.Background(), ctl.AgentCreatePayload{ID: "agent-install-fail"})
	if err == nil || !strings.Contains(err.Error(), "install capability CLIs") {
		t.Fatalf("expected install capability CLIs error, got %v", err)
	}

	d.mu.RLock()
	_, exists := d.agents["agent-install-fail"]
	d.mu.RUnlock()
	if exists {
		t.Fatal("agent should be removed from daemon state after CLI install failure")
	}

	resp, dispatchErr := d.dispatcher.Dispatch(context.Background(), capabilities.Request{
		Name:    capabilities.FilesystemFileWriteName,
		AgentID: "agent-install-fail",
		SeqNo:   1,
		Args:    (&capabilities.Filesystem_File_Write{Path: "x.txt", Content: "y"}).MarshalMUS(),
	})
	if dispatchErr != nil {
		t.Fatal(dispatchErr)
	}
	if resp.OK || resp.ErrorCode != "capability_denied" || resp.ErrorDetail != `no ACL registered for agent "agent-install-fail"` {
		t.Fatalf("expected no ACL after install failure, got ok=%v code=%s detail=%s", resp.OK, resp.ErrorCode, resp.ErrorDetail)
	}

	rt.mu.Lock()
	installed := append([]string(nil), rt.installedSubvols...)
	destroyed := append([]string(nil), rt.destroyedSubvols...)
	rt.mu.Unlock()
	if len(installed) != 1 || installed[0] != subvolPath {
		t.Fatalf("expected CLI install attempt for %q, got %#v", subvolPath, installed)
	}
	if len(destroyed) != 1 || destroyed[0] != subvolPath {
		t.Fatalf("expected subvolume cleanup for %q, got %#v", subvolPath, destroyed)
	}
}

func TestProvisioningDestroyCallsNetworkTerminateAndSubvolumeCleanup(t *testing.T) {
	d, sockPath, cancel := newTestDaemonWithRuntime(t)
	defer cancel()

	rt := &recordingRuntime{
		provisionSubvolume: func(agentID, templatePath string) (string, error) {
			p := filepath.Join(t.TempDir(), "agents", agentID)
			if err := os.MkdirAll(filepath.Join(p, "output"), 0o750); err != nil {
				return "", err
			}
			return p, nil
		},
		spawnWard: func(ctx context.Context, agentID, subvolPath string, cfg agentruntime.WardConfig) (*exec.Cmd, error) {
			return exec.Command("sleep", "1"), nil
		},
	}
	d.runtime = rt
	browserClient := &fakeBrowserReleaseClient{}
	d.browserMgr = newBrowserManager(browserClient, nil, nil)

	c := dialCtl(t, sockPath)
	payload := sendCreate(t, c, "agent-clean")
	if len(payload) == 0 || payload[0] != 1 {
		errStr, _ := mus.ReadString(bytes.NewReader(payload[1:]), 1024)
		t.Fatalf("agentCreate failed: %s", errStr)
	}

	payload = sendDestroy(t, c, "agent-clean")
	if len(payload) == 0 || payload[0] != 1 {
		errStr, _ := mus.ReadString(bytes.NewReader(payload[1:]), 1024)
		t.Fatalf("agentDestroy failed: %s", errStr)
	}

	rt.mu.Lock()
	removedNetworks := append([]string(nil), rt.removedNetworks...)
	terminatedAgents := append([]string(nil), rt.terminatedAgents...)
	destroyedSubvols := append([]string(nil), rt.destroyedSubvols...)
	rt.mu.Unlock()

	if len(removedNetworks) != 1 || removedNetworks[0] != "agent-clean" {
		t.Fatalf("expected network teardown for agent-clean, got %#v", removedNetworks)
	}
	if len(terminatedAgents) != 1 || terminatedAgents[0] != "agent-clean" {
		t.Fatalf("expected terminate for agent-clean, got %#v", terminatedAgents)
	}
	if len(destroyedSubvols) != 1 || !strings.Contains(destroyedSubvols[0], "agent-clean") {
		t.Fatalf("expected subvolume destroy for agent-clean, got %#v", destroyedSubvols)
	}
	if browserClient.releaseCount != 1 {
		t.Fatalf("expected browser release for agent-clean, got %d", browserClient.releaseCount)
	}

	resp, err := d.dispatcher.Dispatch(context.Background(), capabilities.Request{
		Name:    capabilities.FilesystemFileWriteName,
		AgentID: "agent-clean",
		SeqNo:   1,
		Args:    (&capabilities.Filesystem_File_Write{Path: "x.txt", Content: "y"}).MarshalMUS(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || resp.ErrorCode != "capability_denied" {
		t.Fatalf("expected ACL removal after destroy, got ok=%v code=%s", resp.OK, resp.ErrorCode)
	}
}

func TestProvisioningNetworkRuleFailureIsNonFatal(t *testing.T) {
	d, sockPath, cancel := newTestDaemonWithRuntime(t)
	defer cancel()

	rt := &recordingRuntime{
		provisionSubvolume: func(agentID, templatePath string) (string, error) {
			p := filepath.Join(t.TempDir(), "agents", agentID)
			if err := os.MkdirAll(filepath.Join(p, "output"), 0o750); err != nil {
				return "", err
			}
			return p, nil
		},
		spawnWard: func(ctx context.Context, agentID, subvolPath string, cfg agentruntime.WardConfig) (*exec.Cmd, error) {
			return exec.Command("sleep", "1"), nil
		},
		applyNetworkRules: func(agentID string, allowedIPs []string) error {
			return fmt.Errorf("nft boom")
		},
	}
	d.runtime = rt
	d.cfg.ProvidersFile = filepath.Join(t.TempDir(), "missing-providers.kdl")

	c := dialCtl(t, sockPath)
	payload := sendCreate(t, c, "agent-netwarn")
	if len(payload) == 0 || payload[0] != 1 {
		errStr, _ := mus.ReadString(bytes.NewReader(payload[1:]), 1024)
		t.Fatalf("agentCreate failed: %s", errStr)
	}

	d.mu.RLock()
	agent, exists := d.agents["agent-netwarn"]
	d.mu.RUnlock()
	if !exists || agent == nil || agent.pipe == nil {
		t.Fatal("agent should still be provisioned despite network rule failure")
	}

	rt.mu.Lock()
	appliedAgents := append([]string(nil), rt.appliedRuleAgents...)
	appliedIPs := append([][]string(nil), rt.appliedAllowedIPs...)
	rt.mu.Unlock()
	if len(appliedAgents) != 1 || appliedAgents[0] != "agent-netwarn" {
		t.Fatalf("expected ApplyNetworkRules call for agent-netwarn, got %#v", appliedAgents)
	}
	if len(appliedIPs) != 1 || appliedIPs[0] != nil {
		t.Fatalf("expected nil allowedIPs fallback on provider resolution failure, got %#v", appliedIPs)
	}
}

func TestResolveProviderIPs_ProviderNotFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.kdl")
	if err := os.WriteFile(path, []byte("provider anthropic {\n    api-url https://api.anthropic.com\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := resolveProviderIPs(path, "openai", newLogger("error"))
	if err == nil || !strings.Contains(err.Error(), `provider "openai" not found`) {
		t.Fatalf("expected provider-not-found error, got %v", err)
	}
}
