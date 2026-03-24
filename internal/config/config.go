package config

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"regexp"

	"github.com/sblinch/kdl-go"
)

var (
	validLogLevels     = map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	capNameRe          = regexp.MustCompile(`^[A-Z][A-Za-z]+_[A-Z][A-Za-z]+_[A-Z][A-Za-z]+$`)
)

// OrchestratorConfig is parsed from orchestrator.kdl in the workspace root.
type OrchestratorConfig struct {
	SocketPath              string `kdl:"socket-path"`
	AuditDBPath             string `kdl:"audit-db"`
	ChromeRemoteDebugAddr   string `kdl:"chrome-debug-addr"`
	VaultPath               string `kdl:"vault-path"`
	MaxAgentPipeBytesPerSec uint64 `kdl:"max-pipe-bytes-per-sec"`
	LogLevel                string `kdl:"log-level"`
	ProvidersFile           string `kdl:"providers-file"`
	ChromeBinaryPath        string `kdl:"chrome-binary"`
	ChromeUserDataDir       string `kdl:"chrome-user-data-dir"`
}

// ProviderConfig is one entry from providers.kdl.
type ProviderConfig struct {
	Name        string `kdl:",arg"`
	APIURL      string `kdl:"api-url"`
	Description string `kdl:"description"`
}

// AgentConfig is parsed from <agent-subvolume>/agent.kdl.
type AgentConfig struct {
	ID             string                  `kdl:"id"`
	Provider       string                  `kdl:"provider"`
	CPUShares      uint32                  `kdl:"cpu-shares"`
	MemoryMaxBytes uint64                  `kdl:"memory-max-bytes"`
	Capabilities   []AgentCapabilityEntry `kdl:"capabilities,child"`
}

// AgentCapabilityEntry is one entry in agent.kdl's capabilities block.
type AgentCapabilityEntry struct {
	Name  string `kdl:",arg"`
	Scope string `kdl:"scope,attr"`
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
		ChromeBinaryPath:        "chromium",
		ChromeUserDataDir:       workspaceRoot + "/chrome-data",
	}
}

// LoadOrchestratorConfig reads orchestrator.kdl from workspaceRoot, applying
// values on top of the defaults. Returns the defaults if the file does not
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

	if err := kdl.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("config: unmarshal %q: %w", path, err)
	}

	if err := ValidateOrchestratorConfig(cfg); err != nil {
		return cfg, err
	}

	return cfg, nil
}

// LoadProvidersConfig reads providers.kdl and returns a map from provider name
// to ProviderConfig. Returns an empty map (not an error) if the file does not
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

	doc, err := kdl.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("providers: parse %q: %w", path, err)
	}

	for _, node := range doc.Nodes {
		if node.Name.String() != "provider" {
			continue
		}
		if len(node.Arguments) == 0 {
			continue
		}
		name := unquote(node.Arguments[0].String())
		p := ProviderConfig{Name: name}
		for _, child := range node.Children {
			if len(child.Arguments) == 0 {
				continue
			}
			val := unquote(child.Arguments[0].String())
			switch child.Name.String() {
			case "api-url":
				p.APIURL = val
			case "description":
				p.Description = val
			}
		}
		providers[name] = p
	}

	return providers, nil
}

func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// ParseAgentKDL parses an agent.kdl file.
func ParseAgentKDL(data []byte) (AgentConfig, error) {
	cfg := AgentConfig{CPUShares: 1024}
	if err := kdl.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("agent config: unmarshal: %w", err)
	}
	return cfg, nil
}

// ValidateOrchestratorConfig checks for required fields and valid enum values.
func ValidateOrchestratorConfig(cfg OrchestratorConfig) error {
	if cfg.SocketPath == "" {
		return fmt.Errorf("config: socket-path is required")
	}
	if cfg.LogLevel != "" && !validLogLevels[cfg.LogLevel] {
		return fmt.Errorf("config: log-level %q is not valid (must be one of: debug, info, warn, error)", cfg.LogLevel)
	}
	if cfg.MaxAgentPipeBytesPerSec == 0 {
		return fmt.Errorf("config: max-pipe-bytes-per-sec must be > 0")
	}
	return nil
}

// ValidateProviderConfig checks that each provider has a non-empty, well-formed api-url.
func ValidateProviderConfig(providers map[string]ProviderConfig) error {
	for name, p := range providers {
		if p.APIURL == "" {
			return fmt.Errorf("provider %q: api-url is required", name)
		}
		u, err := url.Parse(p.APIURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("provider %q: api-url %q is not a valid URL", name, p.APIURL)
		}
		if u.Scheme != "https" {
			return fmt.Errorf("provider %q: api-url must use https, got %q", name, u.Scheme)
		}
	}
	return nil
}

// ValidateAgentConfig checks required fields and naming conventions.
func ValidateAgentConfig(cfg AgentConfig) error {
	if err := ValidateAgentID(cfg.ID); err != nil {
		return err
	}
	for _, cap := range cfg.Capabilities {
		if !capNameRe.MatchString(cap.Name) {
			return fmt.Errorf("capability name %q does not follow Namespace_Noun_Verb convention", cap.Name)
		}
	}
	return nil
}

// ValidateCapabilityName returns an error if name does not follow Namespace_Noun_Verb.
func ValidateCapabilityName(name string) error {
	if !capNameRe.MatchString(name) {
		return fmt.Errorf("capability name %q does not follow Namespace_Noun_Verb convention", name)
	}
	return nil
}

// ValidateAgentID returns an error if id is invalid.
func ValidateAgentID(id string) error {
	if len(id) == 0 || len(id) > 63 {
		return fmt.Errorf("agent ID must be between 1 and 63 characters")
	}
	for i, c := range id {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '-' && i > 0:
		default:
			return fmt.Errorf("agent ID contains invalid character %q at position %d", c, i)
		}
	}
	return nil
}
