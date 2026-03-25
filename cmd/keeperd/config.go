package main

import (
	"vivary.dev/vivary/internal/config"
)

// OrchestratorConfig is an alias for internal/config.OrchestratorConfig.
type OrchestratorConfig = config.OrchestratorConfig

// ProviderConfig is an alias for internal/config.ProviderConfig.
type ProviderConfig = config.ProviderConfig

// AgentConfig is an alias for internal/config.AgentConfig.
type AgentConfig = config.AgentConfig

// AgentCapabilityEntry is an alias for internal/config.AgentCapabilityEntry.
type AgentCapabilityEntry = config.AgentCapabilityEntry

// AgentBrowserConfig is an alias for internal/config.AgentBrowserConfig.
type AgentBrowserConfig = config.AgentBrowserConfig

// DefaultOrchestratorConfig returns the config with all defaults populated.
func DefaultOrchestratorConfig(workspaceRoot string) OrchestratorConfig {
	return config.DefaultOrchestratorConfig(workspaceRoot)
}

// LoadOrchestratorConfig reads orchestrator.kdl from workspaceRoot.
func LoadOrchestratorConfig(workspaceRoot string) (OrchestratorConfig, error) {
	return config.LoadOrchestratorConfig(workspaceRoot)
}

// LoadProvidersConfig reads providers.kdl.
func LoadProvidersConfig(path string) (map[string]ProviderConfig, error) {
	return config.LoadProvidersConfig(path)
}

// ParseAgentKDL parses an agent.kdl file.
func ParseAgentKDL(data []byte) (AgentConfig, error) {
	return config.ParseAgentKDL(data)
}

// ValidateAgentID returns an error if id is invalid.
func ValidateAgentID(id string) error {
	return config.ValidateAgentID(id)
}
