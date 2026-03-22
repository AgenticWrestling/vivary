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
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"vivary.dev/vivary/internal/capabilities"
	"vivary.dev/vivary/internal/ctl"
)

// wardBinaryPath is the path on the host where the Ward binary lives.
// This is bind-mounted read-only into each nspawn container.
const wardBinaryPath = "/usr/lib/vivary/ward"

// nspawnRootBase is the directory under which agent subvolumes are created.
const nspawnRootBase = "/var/lib/vivary/agents"

// ifnamesiz is the Linux IFNAMSIZ - 1 limit for interface names.
const ifnamesiz = 15

// agentCreate is called from dispatchCtl when a CtlAgentCreate frame arrives.
// It replaces the stub in main.go.
func (d *daemon) agentCreate(ctx context.Context, payload []byte) error {
	var req ctl.AgentCreatePayload
	if err := json.Unmarshal(payload, &req); err != nil {
		return fmt.Errorf("invalid AgentCreatePayload: %w", err)
	}
	if req.ID == "" {
		return fmt.Errorf("agent ID is required")
	}
	if !validAgentID(req.ID) {
		return fmt.Errorf("agent ID must match [a-z0-9][a-z0-9-]{0,62}")
	}

	d.mu.Lock()
	if _, exists := d.agents[req.ID]; exists {
		d.mu.Unlock()
		return fmt.Errorf("agent %q already exists", req.ID)
	}
	// Reserve the slot early to prevent a race on concurrent creates.
	d.agents[req.ID] = &agentState{id: req.ID}
	d.mu.Unlock()

	agentCfg := AgentConfig{
		ID:             req.ID,
		Provider:       req.Provider,
		CPUShares:      req.CPUShares,
		MemoryMaxBytes: req.MemoryMaxBytes,
	}
	if agentCfg.CPUShares == 0 {
		agentCfg.CPUShares = 1024
	}

	subvolPath, err := d.provisionSubvolume(req.ID, req.Template)
	if err != nil {
		d.mu.Lock()
		delete(d.agents, req.ID)
		d.mu.Unlock()
		return fmt.Errorf("provision subvolume: %w", err)
	}

	// Write agent.kdl into the subvolume.
	if err := writeAgentKDL(subvolPath, agentCfg); err != nil {
		return fmt.Errorf("write agent.kdl: %w", err)
	}

	// Install capability ACL.
	acl := &capabilities.ACL{AgentID: req.ID}
	// TODO: read capability list from agent.kdl / AgentCreatePayload extension.
	// For the MVP: grant both MVP capabilities with default scopes.
	acl.Entries = []capabilities.ACLEntry{
		{CapabilityName: capabilities.FilesystemFileWriteName,
			Scope: filepath.Join(subvolPath, "output")},
	}
	d.dispatcher.SetACL(acl)

	d.mu.Lock()
	d.agents[req.ID] = &agentState{
		id:         req.ID,
		subvolPath: subvolPath,
		cfg:        agentCfg,
	}
	d.mu.Unlock()

	// Spawn the Ward process (nspawn on Linux, direct subprocess in dev mode).
	if err := d.spawnAgent(ctx, req.ID, subvolPath, agentCfg); err != nil {
		return fmt.Errorf("spawn agent: %w", err)
	}

	d.log.Info("agent provisioned", "id", req.ID, "subvol", subvolPath)
	return nil
}

// provisionSubvolume creates the agent's root filesystem.
// On Linux with Btrfs it creates a subvolume snapshot of the template.
// Otherwise it falls back to a plain directory copy.
func (d *daemon) provisionSubvolume(agentID, templatePath string) (string, error) {
	target := filepath.Join(nspawnRootBase, agentID)
	if err := os.MkdirAll(nspawnRootBase, 0o750); err != nil {
		return "", err
	}

	if templatePath == "" {
		// No template: create a minimal skeleton.
		if err := os.MkdirAll(filepath.Join(target, "output"), 0o750); err != nil {
			return "", err
		}
		return target, nil
	}

	// Try Btrfs snapshot first.
	if runtime.GOOS == "linux" {
		out, err := exec.Command("btrfs", "subvolume", "snapshot", templatePath, target).CombinedOutput()
		if err == nil {
			return target, nil
		}
		d.log.Debug("btrfs snapshot failed, falling back to cp", "err", string(out))
	}

	// Fallback: recursive copy.
	if out, err := exec.Command("cp", "-a", templatePath+"/.", target).CombinedOutput(); err != nil {
		return "", fmt.Errorf("cp template: %s: %w", out, err)
	}
	return target, nil
}

// spawnAgent starts the Ward process for agentID.
// On Linux it uses systemd-nspawn; otherwise a plain subprocess.
func (d *daemon) spawnAgent(ctx context.Context, agentID, subvolPath string, cfg AgentConfig) error {
	if runtime.GOOS == "linux" {
		return d.spawnNspawn(ctx, agentID, subvolPath, cfg)
	}
	return d.spawnWardPipe(ctx, agentID, "ward", []string{"--log-level", "info"})
}

// spawnNspawn spawns an nspawn container with Ward as the init process and
// opens its stdio as the agent's MUS pipe.
func (d *daemon) spawnNspawn(ctx context.Context, agentID, subvolPath string, cfg AgentConfig) error {
	// Ensure the Ward binary exists on the host.
	if _, err := os.Stat(wardBinaryPath); err != nil {
		// Dev fallback: use the ward binary from PATH.
		wardBin, err2 := exec.LookPath("ward")
		if err2 != nil {
			return fmt.Errorf("ward binary not found at %s and not in PATH: %w", wardBinaryPath, err)
		}
		return d.spawnWardPipe(ctx, agentID, wardBin, []string{
			"--agent-id", agentID,
			"--log-level", "info",
		})
	}

	// Apply cgroup v2 limits via systemd-run wrapper around nspawn.
	// cpu.weight maps from cpu-shares: weight = shares / 1024 * 100 (clamped 1–10000).
	cpuWeight := max(1, min(10000, cfg.CPUShares/1024*100))

	nspawnArgs := []string{
		"--directory=" + subvolPath,
		"--bind-ro=" + wardBinaryPath + ":/usr/bin/ward",
		"--private-network",
		"--network-veth",
		"--machine=" + agentID,
		"-U", // user namespacing
		"--",
		"/usr/bin/ward",
		"--agent-id", agentID,
	}
	if cfg.MemoryMaxBytes > 0 {
		nspawnArgs = append([]string{
			"--property=MemoryMax=" + strconv.FormatUint(cfg.MemoryMaxBytes, 10),
			"--property=CPUWeight=" + strconv.FormatUint(uint64(cpuWeight), 10),
		}, nspawnArgs...)
		// Prepend systemd-run to apply unit properties.
		nspawnArgs = append([]string{"systemd-nspawn"}, nspawnArgs...)
		return d.spawnWardPipe(ctx, agentID, "systemd-run", nspawnArgs)
	}

	if err := d.spawnWardPipe(ctx, agentID, "systemd-nspawn", nspawnArgs); err != nil {
		return err
	}

	// Apply per-agent nftables egress rules, restricted to the provider IPs.
	allowedIPs, err := resolveProviderIPs(d.cfg.ProvidersFile, cfg.Provider, d.log)
	if err != nil {
		d.log.Warn("provider IP resolution failed; using port-443-only fallback",
			"agent", agentID, "provider", cfg.Provider, "err", err)
	}
	if err := applyVethEgressRules(agentID, allowedIPs); err != nil {
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

// ---- nftables veth egress rules --------------------------------------------

// applyVethEgressRules installs per-agent nftables forwarding rules on the
// host-side veth interface created by systemd-nspawn --network-veth.
//
// allowedIPs, if non-empty, restricts HTTPS egress to only those destination
// IPs (resolved from the configured LLM provider's API URL at spawn time).
// When allowedIPs is empty the rules fall back to allowing all TCP-443 egress.
//
// Rules always allow: established/related, DNS (UDP+TCP 53), loopback.
// Everything else from the agent veth is dropped.
func applyVethEgressRules(agentID string, allowedIPs []string) error {
	vethName := vethIfName(agentID)
	tableName := "vivary-" + agentID

	var sb strings.Builder
	fmt.Fprintf(&sb, "table ip %s {\n", tableName)
	sb.WriteString("  chain forward {\n")
	sb.WriteString("    type filter hook forward priority 0; policy accept;\n")
	fmt.Fprintf(&sb, "    iifname %q ct state established,related accept\n", vethName)
	// DNS egress (needed for the container to resolve names).
	fmt.Fprintf(&sb, "    iifname %q udp dport 53 accept\n", vethName)
	fmt.Fprintf(&sb, "    iifname %q tcp dport 53 accept\n", vethName)
	// HTTPS egress — restricted to provider IPs when known, otherwise open.
	if len(allowedIPs) > 0 {
		ipSet := strings.Join(allowedIPs, ", ")
		fmt.Fprintf(&sb, "    iifname %q ip daddr { %s } tcp dport 443 accept\n", vethName, ipSet)
	} else {
		fmt.Fprintf(&sb, "    iifname %q tcp dport 443 accept\n", vethName)
	}
	// Drop everything else from this agent's veth.
	fmt.Fprintf(&sb, "    iifname %q drop\n", vethName)
	sb.WriteString("  }\n")
	sb.WriteString("}\n")

	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(sb.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nft apply for agent %s: %s: %w", agentID, out, err)
	}
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

// removeVethEgressRules deletes the per-agent nftables table.
func removeVethEgressRules(agentID string) {
	_ = exec.Command("nft", "delete", "table", "ip", "vivary-"+agentID).Run()
}

// vethIfName returns the host-side veth interface name for an nspawn machine.
// systemd-nspawn names it "ve-<machine>" truncated to IFNAMSIZ-1 (15) chars.
func vethIfName(machine string) string {
	name := "ve-" + machine
	if len(name) > ifnamesiz {
		name = name[:ifnamesiz]
	}
	return name
}

// agentDestroy is fully implemented — tears down nspawn, removes the subvolume.
func (d *daemon) agentDestroy(payload []byte) error {
	var req ctl.AgentDestroyPayload
	if err := json.Unmarshal(payload, &req); err != nil {
		return fmt.Errorf("invalid AgentDestroyPayload: %w", err)
	}

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

	// Tear down nspawn machine and nftables rules.
	if runtime.GOOS == "linux" {
		_ = exec.Command("machinectl", "terminate", req.ID).Run()
		removeVethEgressRules(req.ID)
	}

	// Remove the subvolume.
	if agent.subvolPath != "" && agent.subvolPath != "/" {
		if runtime.GOOS == "linux" {
			if err := exec.Command("btrfs", "subvolume", "delete", agent.subvolPath).Run(); err != nil {
				// Fallback to rm if not a btrfs subvolume.
				_ = os.RemoveAll(agent.subvolPath)
			}
		} else {
			_ = os.RemoveAll(agent.subvolPath)
		}
	}

	d.log.Info("agent destroyed", "id", req.ID)
	return nil
}

// validAgentID returns true if id matches [a-z0-9][a-z0-9-]{0,62}.
func validAgentID(id string) bool {
	if len(id) == 0 || len(id) > 63 {
		return false
	}
	for i, c := range id {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '-' && i > 0:
		default:
			return false
		}
	}
	return true
}
