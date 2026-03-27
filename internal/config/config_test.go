package config

import (
	"testing"
)

// ---- ValidateOrchestratorConfig -----------------------------------------------

func TestValidateOrchestratorConfig_Valid(t *testing.T) {
	cfg := DefaultOrchestratorConfig("/tmp/test")
	if err := ValidateOrchestratorConfig(cfg); err != nil {
		t.Fatalf("defaults should be valid: %v", err)
	}
}

func TestValidateOrchestratorConfig_EmptySocketPath(t *testing.T) {
	cfg := DefaultOrchestratorConfig("/tmp/test")
	cfg.SocketPath = ""
	if err := ValidateOrchestratorConfig(cfg); err == nil {
		t.Fatal("empty socket-path should be invalid")
	}
}

func TestValidateOrchestratorConfig_InvalidLogLevel(t *testing.T) {
	cfg := DefaultOrchestratorConfig("/tmp/test")
	cfg.LogLevel = "verbose"
	if err := ValidateOrchestratorConfig(cfg); err == nil {
		t.Fatal("invalid log-level should be rejected")
	}
}

func TestValidateOrchestratorConfig_ValidLogLevels(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		cfg := DefaultOrchestratorConfig("/tmp/test")
		cfg.LogLevel = level
		if err := ValidateOrchestratorConfig(cfg); err != nil {
			t.Errorf("log-level %q should be valid: %v", level, err)
		}
	}
}

func TestValidateOrchestratorConfig_ZeroMaxPipeBytes(t *testing.T) {
	cfg := DefaultOrchestratorConfig("/tmp/test")
	cfg.MaxAgentPipeBytesPerSec = 0
	if err := ValidateOrchestratorConfig(cfg); err == nil {
		t.Fatal("zero max-pipe-bytes-per-sec should be invalid")
	}
}

func TestDefaultOrchestratorConfig_ChromedDefaults(t *testing.T) {
	cfg := DefaultOrchestratorConfig("/tmp/test")
	if cfg.ChromedSocketPath == "" {
		t.Fatal("expected chromed socket default")
	}
	if cfg.ChromeProxyServer != "" {
		t.Fatalf("chrome-proxy-server default = %q, want empty for auto-detect", cfg.ChromeProxyServer)
	}
}

// ---- ValidateProviderConfig ---------------------------------------------------

func TestValidateProviderConfig_Valid(t *testing.T) {
	providers := map[string]ProviderConfig{
		"anthropic": {Name: "anthropic", APIURL: "https://api.anthropic.com"},
	}
	if err := ValidateProviderConfig(providers); err != nil {
		t.Fatalf("valid provider should pass: %v", err)
	}
}

func TestValidateProviderConfig_MissingAPIURL(t *testing.T) {
	providers := map[string]ProviderConfig{
		"anthropic": {Name: "anthropic", APIURL: ""},
	}
	if err := ValidateProviderConfig(providers); err == nil {
		t.Fatal("missing api-url should fail")
	}
}

func TestValidateProviderConfig_InvalidURL(t *testing.T) {
	providers := map[string]ProviderConfig{
		"anthropic": {Name: "anthropic", APIURL: "not-a-url"},
	}
	if err := ValidateProviderConfig(providers); err == nil {
		t.Fatal("invalid api-url should fail")
	}
}

func TestValidateProviderConfig_HTTPNotAllowed(t *testing.T) {
	providers := map[string]ProviderConfig{
		"anthropic": {Name: "anthropic", APIURL: "http://api.anthropic.com"},
	}
	if err := ValidateProviderConfig(providers); err == nil {
		t.Fatal("http api-url should fail (must be https)")
	}
}

func TestValidateProviderConfig_Empty(t *testing.T) {
	if err := ValidateProviderConfig(map[string]ProviderConfig{}); err != nil {
		t.Fatalf("empty providers should be valid: %v", err)
	}
}

// ---- ValidateCapabilityName --------------------------------------------------

func TestValidateCapabilityName_Valid(t *testing.T) {
	for _, name := range []string{
		"Browser_Page_Read",
		"Filesystem_File_Write",
		"Calendar_Event_Create",
	} {
		if err := ValidateCapabilityName(name); err != nil {
			t.Errorf("%q should be valid: %v", name, err)
		}
	}
}

func TestValidateCapabilityName_Invalid(t *testing.T) {
	for _, name := range []string{
		"browser_page_read",   // lowercase
		"BrowserPageRead",     // missing underscores
		"Browser_page_Read",   // lowercase noun
		"Browser_Page",        // only two parts
		"Browser_Page_Read_X", // four parts
		"",
	} {
		if err := ValidateCapabilityName(name); err == nil {
			t.Errorf("%q should be invalid but passed", name)
		}
	}
}

// ---- ValidateAgentConfig -----------------------------------------------------

func TestValidateAgentConfig_Valid(t *testing.T) {
	cfg := AgentConfig{
		ID:       "my-agent",
		Provider: "anthropic",
		Browser:  AgentBrowserConfig{Headless: true},
		Capabilities: []AgentCapabilityEntry{
			{Name: "Browser_Page_Read"},
			{Name: "Filesystem_File_Write"},
		},
	}
	if err := ValidateAgentConfig(cfg); err != nil {
		t.Fatalf("valid config should pass: %v", err)
	}
}

func TestParseAgentKDL_BrowserHeadless(t *testing.T) {
	cfg, err := ParseAgentKDL([]byte("id \"test-agent\"\nbrowser {\n    headless true\n}\n"))
	if err != nil {
		t.Fatalf("ParseAgentKDL: %v", err)
	}
	if !cfg.Browser.Headless {
		t.Fatal("Browser.Headless = false, want true")
	}
}

func TestParseAgentKDL_Capabilities(t *testing.T) {
	kdl := `id "test-agent"
capabilities "Browser_Page_Read" {
    Link {
        domain "example.com"
        path-prefix "/wiki"
    }
}
capabilities "Filesystem_File_Write"
`
	cfg, err := ParseAgentKDL([]byte(kdl))
	if err != nil {
		t.Fatalf("ParseAgentKDL: %v", err)
	}
	if len(cfg.Capabilities) != 2 {
		t.Fatalf("expected 2 capabilities, got %d", len(cfg.Capabilities))
	}
	if cfg.Capabilities[0].Name != "Browser_Page_Read" {
		t.Errorf("cap 0 name = %q", cfg.Capabilities[0].Name)
	}
	if len(cfg.Capabilities[0].Scopes) != 1 || cfg.Capabilities[0].Scopes[0].Entity != "Link" {
		t.Fatalf("cap 0 scopes = %v", cfg.Capabilities[0].Scopes)
	}
	if len(cfg.Capabilities[0].Scopes[0].Domains) != 1 || cfg.Capabilities[0].Scopes[0].Domains[0] != "example.com" {
		t.Errorf("cap 0 scope domains = %v", cfg.Capabilities[0].Scopes[0].Domains)
	}
	if cfg.Capabilities[1].Name != "Filesystem_File_Write" {
		t.Errorf("cap 1 name = %q", cfg.Capabilities[1].Name)
	}
}

func TestValidateAgentConfig_InvalidID(t *testing.T) {
	cfg := AgentConfig{ID: "INVALID_ID!"}
	if err := ValidateAgentConfig(cfg); err == nil {
		t.Fatal("invalid ID should fail")
	}
}

func TestValidateAgentConfig_InvalidCapabilityName(t *testing.T) {
	cfg := AgentConfig{
		ID: "my-agent",
		Capabilities: []AgentCapabilityEntry{
			{Name: "notvalid"},
		},
	}
	if err := ValidateAgentConfig(cfg); err == nil {
		t.Fatal("invalid capability name should fail")
	}
}

// ---- ValidateAgentID ---------------------------------------------------------

func TestValidateAgentID_Valid(t *testing.T) {
	for _, id := range []string{"a", "agent-1", "my-agent-abc", "a123"} {
		if err := ValidateAgentID(id); err != nil {
			t.Errorf("%q should be valid: %v", id, err)
		}
	}
}

func TestValidateAgentID_Invalid(t *testing.T) {
	for _, id := range []string{
		"",
		"UPPERCASE",
		"-starts-with-dash",
		"has space",
		"has_underscore",
	} {
		if err := ValidateAgentID(id); err == nil {
			t.Errorf("%q should be invalid but passed", id)
		}
	}
}
