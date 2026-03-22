package main

import (
	"fmt"
	"os"
	"strconv"
)

// NOTE: KDL parsing is handled with a minimal hand-rolled parser here so that
// the MVP can build without a mandatory external KDL library dependency.  The
// KDL spec (https://kdl.dev) is straightforward for the config subset we need.
// A full KDL library can be substituted once go.sum is set up.

// OrchestratorConfig is parsed from orchestrator.kdl in the workspace root.
type OrchestratorConfig struct {
	// SocketPath is the Unix domain socket path for the ctl interface.
	// Default: <WorkspaceRoot>/keeper.sock
	SocketPath string

	// AuditDBPath is the path to the SQLite audit database.
	// Default: <WorkspaceRoot>/audit.db
	AuditDBPath string

	// ChromeRemoteDebugAddr is host:port for headless Chrome CDP.
	// Default: 127.0.0.1:9222
	ChromeRemoteDebugAddr string

	// VaultPath is the path to the encrypted credential store.
	// Default: <WorkspaceRoot>/vault.enc
	VaultPath string

	// MaxAgentPipeBytesPerSec is the byte-rate limit per agent stdio pipe.
	// Default: 1 MiB/s
	MaxAgentPipeBytesPerSec uint64

	// LogLevel controls structured log verbosity: "debug", "info", "warn", "error".
	LogLevel string

	// ProvidersFile is the path to providers.kdl.
	// Default: <WorkspaceRoot>/providers.kdl
	ProvidersFile string
}

// ProviderConfig is one entry from providers.kdl.
type ProviderConfig struct {
	// Name is the provider identifier used in agent.kdl.
	Name string

	// APIURL is the canonical HTTPS endpoint (e.g. "https://api.anthropic.com").
	// The hostname is resolved at agent spawn time for nftables egress rules.
	APIURL string

	// Description is a human-readable label.
	Description string
}

// DefaultOrchestratorConfig returns the config with all defaults populated.
func DefaultOrchestratorConfig(workspaceRoot string) OrchestratorConfig {
	return OrchestratorConfig{
		SocketPath:              workspaceRoot + "/keeper.sock",
		AuditDBPath:             workspaceRoot + "/audit.db",
		ChromeRemoteDebugAddr:   "127.0.0.1:9222",
		VaultPath:               workspaceRoot + "/vault.enc",
		MaxAgentPipeBytesPerSec: 1 * 1024 * 1024,
		LogLevel:                "info",
		ProvidersFile:           workspaceRoot + "/providers.kdl",
	}
}

// LoadOrchestratorConfig reads orchestrator.kdl from workspaceRoot, applying
// values on top of the defaults.  Returns the defaults if the file does not
// exist (not an error).
func LoadOrchestratorConfig(workspaceRoot string) (OrchestratorConfig, error) {
	cfg := DefaultOrchestratorConfig(workspaceRoot)
	path := workspaceRoot + "/orchestrator.kdl"
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("config: read %q: %w", path, err)
	}
	if err := parseOrchestratorKDL(data, &cfg); err != nil {
		return cfg, fmt.Errorf("config: parse %q: %w", path, err)
	}
	return cfg, nil
}

// parseOrchestratorKDL is a minimal KDL parser for the orchestrator config.
// It handles single-line nodes of the form:
//
//	node-name "value"
//	node-name 12345
//
// Multi-line and nested nodes are not needed for orchestrator.kdl.
func parseOrchestratorKDL(data []byte, cfg *OrchestratorConfig) error {
	lines := splitLines(string(data))
	for _, line := range lines {
		line = trimComment(line)
		if line == "" {
			continue
		}
		node, val, ok := parseSimpleNode(line)
		if !ok {
			continue // skip unrecognised or multi-arg nodes
		}
		switch node {
		case "socket-path":
			cfg.SocketPath = val
		case "audit-db":
			cfg.AuditDBPath = val
		case "chrome-debug-addr":
			cfg.ChromeRemoteDebugAddr = val
		case "vault-path":
			cfg.VaultPath = val
		case "max-pipe-bytes-per-sec":
			n, err := strconv.ParseUint(val, 10, 64)
			if err != nil {
				return fmt.Errorf("max-pipe-bytes-per-sec: %w", err)
			}
			cfg.MaxAgentPipeBytesPerSec = n
		case "log-level":
			cfg.LogLevel = val
		case "providers-file":
			cfg.ProvidersFile = val
		}
	}
	return nil
}

// LoadProvidersConfig reads providers.kdl and returns a map from provider name
// to ProviderConfig.  Returns an empty map (not an error) if the file does not
// exist, so that keeperd still starts without a providers file.
func LoadProvidersConfig(path string) (map[string]ProviderConfig, error) {
	providers := make(map[string]ProviderConfig)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return providers, nil
	}
	if err != nil {
		return nil, fmt.Errorf("providers: read %q: %w", path, err)
	}
	if err := parseProvidersKDL(data, providers); err != nil {
		return nil, fmt.Errorf("providers: parse %q: %w", path, err)
	}
	return providers, nil
}

// parseProvidersKDL parses a providers.kdl file into the supplied map.
// It handles blocks of the form:
//
//	provider "name" {
//	    api-url     "https://..."
//	    description "..."
//	}
func parseProvidersKDL(data []byte, out map[string]ProviderConfig) error {
	var current *ProviderConfig
	for _, line := range splitLines(string(data)) {
		line = trimComment(line)
		if line == "" {
			continue
		}
		if line == "}" {
			if current != nil && current.Name != "" {
				out[current.Name] = *current
			}
			current = nil
			continue
		}
		// provider "name" {
		if len(line) > 9 && line[:9] == "provider " {
			rest := trimSpace(line[9:])
			// strip trailing " {" if present
			if len(rest) > 1 && rest[len(rest)-1] == '{' {
				rest = trimSpace(rest[:len(rest)-1])
			}
			name := unquote(rest)
			current = &ProviderConfig{Name: name}
			continue
		}
		if current != nil {
			node, val, ok := parseSimpleNode(line)
			if !ok {
				continue
			}
			switch node {
			case "api-url":
				current.APIURL = val
			case "description":
				current.Description = val
			}
		}
	}
	// Handle unterminated block (shouldn't happen in valid KDL).
	if current != nil && current.Name != "" {
		out[current.Name] = *current
	}
	return nil
}

// unquote strips surrounding double-quotes from a KDL string value.
func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// AgentConfig is parsed from <agent-subvolume>/agent.kdl.
type AgentConfig struct {
	// ID is the agent identifier (must match the directory name).
	ID string

	// Provider is the name of the LLM provider (from providers.kdl).
	// Controls which network endpoint the agent's nftables egress allows.
	// Example: "anthropic", "openai", "google".
	Provider string

	// Capabilities lists the capability names and optional scopes allowed.
	// KDL representation:
	//   capabilities {
	//     Browser_Page_Read scope="https://example.com"
	//     Filesystem_File_Write scope="/output"
	//   }
	Capabilities []AgentCapabilityEntry

	// CPUShares is the cgroup cpu.shares value (default 1024).
	CPUShares uint32

	// MemoryMaxBytes is the cgroup memory.max limit (0 = unlimited).
	MemoryMaxBytes uint64
}

// AgentCapabilityEntry is one entry in agent.kdl's capabilities block.
type AgentCapabilityEntry struct {
	Name  string
	Scope string
}

// ParseAgentKDL parses an agent.kdl file.
func ParseAgentKDL(data []byte) (AgentConfig, error) {
	cfg := AgentConfig{CPUShares: 1024}
	var inCaps bool
	for _, line := range splitLines(string(data)) {
		line = trimComment(line)
		if line == "" {
			continue
		}
		if line == "capabilities {" {
			inCaps = true
			continue
		}
		if line == "}" {
			inCaps = false
			continue
		}
		if inCaps {
			entry, ok := parseCapabilityEntry(line)
			if ok {
				cfg.Capabilities = append(cfg.Capabilities, entry)
			}
			continue
		}
		node, val, ok := parseSimpleNode(line)
		if !ok {
			continue
		}
		switch node {
		case "id":
			cfg.ID = val
		case "provider":
			cfg.Provider = val
		case "cpu-shares":
			n, _ := strconv.ParseUint(val, 10, 32)
			cfg.CPUShares = uint32(n)
		case "memory-max-bytes":
			n, _ := strconv.ParseUint(val, 10, 64)
			cfg.MemoryMaxBytes = n
		}
	}
	return cfg, nil
}

// ---- minimal KDL helpers ---------------------------------------------------

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, trimSpace(s[start:i]))
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, trimSpace(s[start:]))
	}
	return lines
}

func trimComment(s string) string {
	for i := 0; i < len(s)-1; i++ {
		if s[i] == '/' && s[i+1] == '/' {
			return trimSpace(s[:i])
		}
	}
	return s
}

func trimSpace(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

// parseSimpleNode parses "node-name \"value\"" or "node-name 1234".
// Returns (node, value, true) on success.
func parseSimpleNode(s string) (node, value string, ok bool) {
	// Find first whitespace to split node name from value.
	i := 0
	for i < len(s) && s[i] != ' ' && s[i] != '\t' {
		i++
	}
	if i == len(s) {
		return "", "", false // no value
	}
	node = s[:i]
	rest := trimSpace(s[i:])
	if len(rest) >= 2 && rest[0] == '"' && rest[len(rest)-1] == '"' {
		return node, rest[1 : len(rest)-1], true
	}
	return node, rest, true
}

// parseCapabilityEntry parses lines like:
//
//	Browser_Page_Read scope="https://example.com,https://other.com"
func parseCapabilityEntry(s string) (AgentCapabilityEntry, bool) {
	i := 0
	for i < len(s) && s[i] != ' ' && s[i] != '\t' {
		i++
	}
	name := s[:i]
	if name == "" {
		return AgentCapabilityEntry{}, false
	}
	rest := trimSpace(s[i:])
	scope := ""
	const scopePrefix = `scope="`
	if idx := indexString(rest, scopePrefix); idx >= 0 {
		start := idx + len(scopePrefix)
		end := indexByte(rest[start:], '"')
		if end >= 0 {
			scope = rest[start : start+end]
		}
	}
	return AgentCapabilityEntry{Name: name, Scope: scope}, true
}

func indexString(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

