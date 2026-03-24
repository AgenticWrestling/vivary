package main

// provisioning.go implements Phase 1.4: agent workspace provisioning.
//
// Full provisioning sequence (Linux only):
//  1. Allocate a UID range from the host subuid pool.
//  2. Create a Btrfs subvolume as a snapshot of the template subvolume.
//  3. Bind-mount the Ward binary read-only into the subvolume at /usr/bin/ward.
//  4. Write the agent's agent.kdl into the subvolume.
//  5. Create a per-agent veth pair and apply nftables LLM-only egress rules.
//  6. Configure cgroup v2 limits (cpu.weight, memory.max).
//  7. Spawn systemd-nspawn with Ward as the init process.
//  8. Open the Ward's stdin/stdout as a MUS pipe and register with the Router.
//
// In non-Linux environments (macOS/WSL2 test runs) the nspawn step is replaced
// by a direct Ward subprocess spawn for development convenience.

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"vivary.dev/vivary/internal/capabilities"
	"vivary.dev/vivary/internal/ctl"
	"vivary.dev/vivary/internal/runtime"
	"vivary.dev/vivary/internal/switchboard"
)

// agentCreate is called from dispatchCtl when a CtlAgentCreate frame arrives.
// It replaces the stub in main.go.
func (d *daemon) agentCreate(ctx context.Context, req ctl.AgentCreatePayload) error {
	if req.ID == "" {
		return fmt.Errorf("agent ID is required")
	}
	if err := ValidateAgentID(req.ID); err != nil {
		return err
	}

	d.mu.Lock()
	if _, exists := d.agents[req.ID]; exists {
		d.mu.Unlock()
		return fmt.Errorf("agent %q already exists", req.ID)
	}
	// Reserve the slot early to prevent a race on concurrent creates.
	d.agents[req.ID] = &agentState{id: req.ID}
	d.mu.Unlock()

	// Cleanup stack: executed in reverse on any error path.
	var (
		cleanup []func()
		success bool
	)
	defer func() {
		if success {
			return
		}
		for i := len(cleanup) - 1; i >= 0; i-- {
			cleanup[i]()
		}
		d.mu.Lock()
		delete(d.agents, req.ID)
		d.mu.Unlock()
	}()

	agentCfg := AgentConfig{
		ID:             req.ID,
		Provider:       req.Provider,
		CPUShares:      req.CPUShares,
		MemoryMaxBytes: req.MemoryMaxBytes,
	}
	if agentCfg.CPUShares == 0 {
		agentCfg.CPUShares = 1024
	}

	subvolPath, err := d.runtime.ProvisionSubvolume(req.ID, req.Template)
	if err != nil {
		return fmt.Errorf("provision subvolume: %w", err)
	}
	cleanup = append(cleanup, func() {
		_ = d.runtime.DestroySubvolume(req.ID, subvolPath)
	})

	// Write agent.kdl into the subvolume.
	if err := writeAgentKDL(subvolPath, agentCfg); err != nil {
		return fmt.Errorf("write agent.kdl: %w", err)
	}

	// Install per-capability symlinks inside the container so cap-cli is
	// reachable under each capability name.
	if err := d.runtime.InstallCapabilityCLIs(subvolPath, d.dispatcher.Names()); err != nil {
		return fmt.Errorf("install capability CLIs: %w", err)
	}

	// Install capability ACL.
	acl := &capabilities.ACL{AgentID: req.ID}
	// TODO: read capability list from agent.kdl / AgentCreatePayload extension.
	// For the MVP: grant both MVP capabilities with default scopes.
	acl.Entries = []capabilities.ACLEntry{
		{
			CapabilityName: capabilities.FilesystemFileWriteName,
			Constraints: []capabilities.ScopeConstraint{{
				Entity:      "File",
				Constraints: capabilities.ConstraintSet{"path-prefix": {filepath.Join(subvolPath, "output")}},
			}},
		},
		{
			CapabilityName: capabilities.BrowserPageReadName,
			Constraints: []capabilities.ScopeConstraint{{
				Entity:      "Link",
				Constraints: capabilities.ConstraintSet{"domain": {"en.wikipedia.org", "github.com"}},
			}},
		},
	}
	d.dispatcher.SetACL(acl)
	cleanup = append(cleanup, func() {
		d.dispatcher.RemoveACL(req.ID)
	})

	d.mu.Lock()
	d.agents[req.ID] = &agentState{
		id:         req.ID,
		subvolPath: subvolPath,
		cfg:        agentCfg,
	}
	d.mu.Unlock()

	// Spawn the Ward process.
	if err := d.spawnAgent(ctx, req.ID, subvolPath, agentCfg); err != nil {
		return fmt.Errorf("spawn agent: %w", err)
	}

	success = true
	d.log.Info("agent provisioned", "id", req.ID, "subvol", subvolPath)
	return nil
}

// spawnAgent starts the Ward process for agentID.
func (d *daemon) spawnAgent(ctx context.Context, agentID, subvolPath string, cfg AgentConfig) error {
	cmd, err := d.runtime.SpawnWard(ctx, agentID, subvolPath, runtime.WardConfig{
		CPUShares:      cfg.CPUShares,
		MemoryMaxBytes: cfg.MemoryMaxBytes,
		Provider:       cfg.Provider,
		LogLevel:       d.cfg.LogLevel,
	})
	if err != nil {
		return err
	}

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

	// Apply per-agent nftables egress rules, restricted to the provider IPs.
	allowedIPs, err := resolveProviderIPs(d.cfg.ProvidersFile, cfg.Provider, d.log)
	if err != nil {
		d.log.Warn("provider IP resolution failed; using port-443-only fallback",
			"agent", agentID, "provider", cfg.Provider, "err", err)
	}
	if err := d.runtime.ApplyNetworkRules(agentID, allowedIPs); err != nil {
		d.log.Warn("nftables egress rules failed (non-fatal)", "agent", agentID, "err", err)
	}

	return nil
}

// writeAgentKDL writes agent.kdl into the subvolume root.
func writeAgentKDL(subvolPath string, cfg AgentConfig) error {
	var sb strings.Builder
	sb.WriteString("// Generated by keeperd — do not edit manually\n\n")
	fmt.Fprintf(&sb, "id %q\n", cfg.ID)
	if cfg.Provider != "" {
		fmt.Fprintf(&sb, "provider %q\n", cfg.Provider)
	}
	fmt.Fprintf(&sb, "cpu-shares %d\n", cfg.CPUShares)
	if cfg.MemoryMaxBytes > 0 {
		fmt.Fprintf(&sb, "memory-max-bytes %d\n", cfg.MemoryMaxBytes)
	}
	if len(cfg.Capabilities) > 0 {
		sb.WriteString("\ncapabilities {\n")
		for _, c := range cfg.Capabilities {
			if c.Scope != "" {
				fmt.Fprintf(&sb, "    %s scope=%q\n", c.Name, c.Scope)
			} else {
				fmt.Fprintf(&sb, "    %s\n", c.Name)
			}
		}
		sb.WriteString("}\n")
	}
	return os.WriteFile(filepath.Join(subvolPath, "agent.kdl"), []byte(sb.String()), 0o640)
}

// agentDestroy is fully implemented — tears down nspawn, removes the subvolume.
func (d *daemon) agentDestroy(req ctl.AgentDestroyPayload) error {

	d.mu.Lock()
	agent, exists := d.agents[req.ID]
	if !exists {
		d.mu.Unlock()
		return fmt.Errorf("agent %q not found", req.ID)
	}
	delete(d.agents, req.ID)
	d.mu.Unlock()

	d.router.RemovePipe(req.ID)
	d.dispatcher.RemoveACL(req.ID)

	// Tear down agent isolation.
	_ = d.runtime.RemoveNetworkRules(req.ID)
	if rt, ok := d.runtime.(interface{ Terminate(string) error }); ok {
		_ = rt.Terminate(req.ID)
	}

	// Remove the subvolume.
	if agent.subvolPath != "" {
		_ = d.runtime.DestroySubvolume(req.ID, agent.subvolPath)
	}

	d.log.Info("agent destroyed", "id", req.ID)
	return nil
}

// resolveProviderIPs looks up the provider by name in providers.kdl, extracts
// the API hostname, and resolves it to IP addresses.  Returns nil (no error)
// when the provider name is empty or the file does not exist.
func resolveProviderIPs(providersFile, providerName string, log interface {
	Warn(string, ...any)
}) ([]string, error) {
	if providerName == "" {
		return nil, nil
	}
	providers, err := LoadProvidersConfig(providersFile)
	if err != nil {
		return nil, err
	}
	p, ok := providers[providerName]
	if !ok {
		return nil, fmt.Errorf("provider %q not found in %s", providerName, providersFile)
	}
	if p.APIURL == "" {
		return nil, fmt.Errorf("provider %q has no api-url", providerName)
	}
	u, err := url.Parse(p.APIURL)
	if err != nil {
		return nil, fmt.Errorf("provider %q invalid api-url: %w", providerName, err)
	}
	host := u.Hostname()
	addrs, err := net.LookupHost(host)
	if err != nil {
		return nil, fmt.Errorf("provider %q: resolve %q: %w", providerName, host, err)
	}
	// Keep only IPv4 addresses (nftables "table ip" handles IPv4 only).
	var ipv4 []string
	for _, a := range addrs {
		if net.ParseIP(a).To4() != nil {
			ipv4 = append(ipv4, a)
		}
	}
	if len(ipv4) == 0 {
		log.Warn("provider has no IPv4 addresses; falling back to port-443-only rules",
			"provider", providerName, "host", host)
		return nil, nil
	}
	return ipv4, nil
}
