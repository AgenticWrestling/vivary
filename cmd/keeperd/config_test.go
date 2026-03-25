package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProvidersConfig_Basic(t *testing.T) {
	dir := t.TempDir()
	content := `
// providers.kdl test fixture
 provider anthropic {
     api-url     "https://api.anthropic.com"
     description "Anthropic Claude API"
 }

 provider openai {
     api-url     "https://api.openai.com"
     description "OpenAI API"
 }
`
	path := filepath.Join(dir, "providers.kdl")
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}

	providers, err := LoadProvidersConfig(path)
	if err != nil {
		t.Fatalf("LoadProvidersConfig: %v", err)
	}
	if len(providers) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(providers))
	}

	anthropic, ok := providers["anthropic"]
	if !ok {
		t.Fatal("missing 'anthropic' provider")
	}
	if anthropic.APIURL != "https://api.anthropic.com" {
		t.Errorf("anthropic api-url = %q, want https://api.anthropic.com", anthropic.APIURL)
	}
	if anthropic.Description != "Anthropic Claude API" {
		t.Errorf("anthropic description = %q", anthropic.Description)
	}

	openai, ok := providers["openai"]
	if !ok {
		t.Fatal("missing 'openai' provider")
	}
	if openai.APIURL != "https://api.openai.com" {
		t.Errorf("openai api-url = %q", openai.APIURL)
	}
}

func TestLoadProvidersConfig_MissingFile(t *testing.T) {
	providers, err := LoadProvidersConfig("/nonexistent/providers.kdl")
	if err != nil {
		t.Fatalf("missing file should return empty map, not error: %v", err)
	}
	if len(providers) != 0 {
		t.Errorf("expected empty map for missing file, got %d entries", len(providers))
	}
}

func TestLoadProvidersConfig_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.kdl")
	if err := os.WriteFile(path, []byte("// empty\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	providers, err := LoadProvidersConfig(path)
	if err != nil {
		t.Fatalf("empty file should not error: %v", err)
	}
	if len(providers) != 0 {
		t.Errorf("expected 0 providers, got %d", len(providers))
	}
}

func TestParseAgentKDL_Provider(t *testing.T) {
	kdl := `id "test-agent"
provider "anthropic"
cpu-shares 2048
browser {
    headless true
}
`
	cfg, err := ParseAgentKDL([]byte(kdl))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ID != "test-agent" {
		t.Errorf("id = %q, want test-agent", cfg.ID)
	}
	if cfg.Provider != "anthropic" {
		t.Errorf("provider = %q, want anthropic", cfg.Provider)
	}
	if cfg.CPUShares != 2048 {
		t.Errorf("cpu-shares = %d, want 2048", cfg.CPUShares)
	}
	if !cfg.Browser.Headless {
		t.Fatal("browser.headless = false, want true")
	}
}

func TestValidAgentID(t *testing.T) {
	valid := []string{"a", "agent1", "my-agent", "a1-b2-c3"}
	for _, id := range valid {
		if err := ValidateAgentID(id); err != nil {
			t.Errorf("ValidateAgentID(%q) error: %v, want nil", id, err)
		}
	}
	invalid := []string{"", "A", "my_agent", "-start", "too-long-" + string(make([]byte, 60))}
	for _, id := range invalid {
		if err := ValidateAgentID(id); err == nil {
			t.Errorf("ValidateAgentID(%q) = nil, want error", id)
		}
	}
}
