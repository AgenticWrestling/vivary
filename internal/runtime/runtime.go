package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// ContainerRuntime defines the interface for agent isolation and execution.
type ContainerRuntime interface {
	// ProvisionSubvolume creates a root filesystem for the agent.
	// If templatePath is provided, it should be a snapshot/copy of that template.
	ProvisionSubvolume(agentID, templatePath string) (string, error)

	// DestroySubvolume removes the agent's root filesystem.
	DestroySubvolume(agentID, subvolPath string) error

	// SpawnWard starts the Ward process for the agent.
	// On Linux this typically uses systemd-nspawn.
	SpawnWard(ctx context.Context, agentID, subvolPath string, cfg WardConfig) (*exec.Cmd, error)

	// ApplyNetworkRules configures firewall/egress rules for the agent.
	ApplyNetworkRules(agentID string, allowedIPs []string) error

	// RemoveNetworkRules tears down the agent's firewall rules.
	RemoveNetworkRules(agentID string) error
}

// WardConfig carries settings for the Ward process.
type WardConfig struct {
	CPUShares      uint32
	MemoryMaxBytes uint64
	Provider       string
	LogPath        string
	LogLevel       string
}

// LinuxRuntime is the production implementation of ContainerRuntime.
type LinuxRuntime struct {
	NspawnRootBase  string
	WardBinaryPath  string
	UseSystemdRun   bool
}

func (r *LinuxRuntime) ProvisionSubvolume(agentID, templatePath string) (string, error) {
	target := filepath.Join(r.NspawnRootBase, agentID)
	if err := os.MkdirAll(r.NspawnRootBase, 0o750); err != nil {
		return "", err
	}

	if templatePath == "" {
		if err := os.MkdirAll(filepath.Join(target, "output"), 0o750); err != nil {
			return "", err
		}
		return target, nil
	}

	// Try Btrfs snapshot.
	out, err := exec.Command("btrfs", "subvolume", "snapshot", templatePath, target).CombinedOutput()
	if err == nil {
		return target, nil
	}
	// Fallback: recursive copy.
	if out, err = exec.Command("cp", "-a", templatePath+"/.", target).CombinedOutput(); err != nil {
		return "", fmt.Errorf("cp template: %s: %w", out, err)
	}
	return target, nil
}

func (r *LinuxRuntime) DestroySubvolume(agentID, subvolPath string) error {
	if subvolPath == "" || subvolPath == "/" {
		return fmt.Errorf("invalid subvolPath: %q", subvolPath)
	}

	if err := exec.Command("btrfs", "subvolume", "delete", subvolPath).Run(); err == nil {
		return nil
	}
	// Fallback to rm if not a btrfs subvolume.
	return os.RemoveAll(subvolPath)
}

func (r *LinuxRuntime) SpawnWard(ctx context.Context, agentID, subvolPath string, cfg WardConfig) (*exec.Cmd, error) {
	wardBin := "/usr/bin/ward"
	
	// Apply cgroup v2 limits via systemd-run wrapper around nspawn.
	cpuWeight := max(1, min(10000, cfg.CPUShares/1024*100))

	nspawnArgs := []string{
		"--directory=" + subvolPath,
		"--bind-ro=" + r.WardBinaryPath + ":" + wardBin,
		"--private-network",
		"--network-veth",
		"--machine=" + agentID,
		"-U",
		"--",
		wardBin,
		"--agent-id", agentID,
		"--log-level", cfg.LogLevel,
	}

	var cmd *exec.Cmd
	if r.UseSystemdRun || cfg.MemoryMaxBytes > 0 {
		runArgs := []string{
			"--unit=vivary-agent-" + agentID,
			"--property=CPUWeight=" + strconv.FormatUint(uint64(cpuWeight), 10),
		}
		if cfg.MemoryMaxBytes > 0 {
			runArgs = append(runArgs, "--property=MemoryMax="+strconv.FormatUint(cfg.MemoryMaxBytes, 10))
		}
		runArgs = append(runArgs, "systemd-nspawn")
		runArgs = append(runArgs, nspawnArgs...)
		cmd = exec.CommandContext(ctx, "systemd-run", runArgs...)
	} else {
		cmd = exec.CommandContext(ctx, "systemd-nspawn", nspawnArgs...)
	}

	return cmd, nil
}

func (r *LinuxRuntime) ApplyNetworkRules(agentID string, allowedIPs []string) error {
	vethName := vethIfName(agentID)
	tableName := "vivary-" + agentID

	var sb strings.Builder
	fmt.Fprintf(&sb, "table ip %s {\n", tableName)
	sb.WriteString("  chain forward {\n")
	sb.WriteString("    type filter hook forward priority 0; policy accept;\n")
	fmt.Fprintf(&sb, "    iifname %q ct state established,related accept\n", vethName)
	fmt.Fprintf(&sb, "    iifname %q udp dport 53 accept\n", vethName)
	fmt.Fprintf(&sb, "    iifname %q tcp dport 53 accept\n", vethName)
	if len(allowedIPs) > 0 {
		ipSet := strings.Join(allowedIPs, ", ")
		fmt.Fprintf(&sb, "    iifname %q ip daddr { %s } tcp dport 443 accept\n", vethName, ipSet)
	} else {
		fmt.Fprintf(&sb, "    iifname %q tcp dport 443 accept\n", vethName)
	}
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

func (r *LinuxRuntime) RemoveNetworkRules(agentID string) error {
	return exec.Command("nft", "delete", "table", "ip", "vivary-"+agentID).Run()
}

func (r *LinuxRuntime) Terminate(agentID string) error {
	return exec.Command("machinectl", "terminate", agentID).Run()
}

// StubRuntime is a development/test implementation.
type StubRuntime struct {
	ProvisionFunc      func(agentID, templatePath string) (string, error)
	SpawnFunc          func(ctx context.Context, agentID, subvolPath string, cfg WardConfig) (*exec.Cmd, error)
}

func (r *StubRuntime) ProvisionSubvolume(agentID, templatePath string) (string, error) {
	if r.ProvisionFunc != nil {
		return r.ProvisionFunc(agentID, templatePath)
	}
	return filepath.Join(os.TempDir(), "vivary-agents", agentID), nil
}

func (r *StubRuntime) DestroySubvolume(agentID, subvolPath string) error {
	return os.RemoveAll(subvolPath)
}

func (r *StubRuntime) SpawnWard(ctx context.Context, agentID, subvolPath string, cfg WardConfig) (*exec.Cmd, error) {
	if r.SpawnFunc != nil {
		return r.SpawnFunc(ctx, agentID, subvolPath, cfg)
	}
	// Fallback to direct ward subprocess.
	return exec.CommandContext(ctx, "ward", "--agent-id", agentID, "--log-level", cfg.LogLevel), nil
}

func (r *StubRuntime) ApplyNetworkRules(agentID string, allowedIPs []string) error {
	return nil
}

func (r *StubRuntime) RemoveNetworkRules(agentID string) error {
	return nil
}

// ---- helpers ---------------------------------------------------------------

func vethIfName(machine string) string {
	const ifnamesiz = 15
	name := "ve-" + machine
	if len(name) > ifnamesiz {
		name = name[:ifnamesiz]
	}
	return name
}
