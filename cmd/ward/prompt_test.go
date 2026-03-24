package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vivary.dev/vivary/internal/capabilities"
)

func init() {
	// Ensure capRegistry is populated for tests.
	capRegistry = capabilities.GeneratedRegistry()
}

func TestBuildSystemPrompt_NoFile(t *testing.T) {
	prompt := buildSystemPrompt("/nonexistent/agent.kdl")
	if prompt != "" {
		t.Errorf("missing agent.kdl should return empty prompt, got %q", prompt)
	}
}

func TestBuildSystemPrompt_WithCapabilities(t *testing.T) {
	kdl := `
id "test-agent"
provider "anthropic"
cpu-shares 1024

capabilities {
    Browser_Page_Read
    Filesystem_File_Write
}
`
	f := filepath.Join(t.TempDir(), "agent.kdl")
	if err := os.WriteFile(f, []byte(kdl), 0o644); err != nil {
		t.Fatal(err)
	}

	prompt := buildSystemPrompt(f)
	if prompt == "" {
		t.Fatal("expected non-empty system prompt")
	}
	if !strings.Contains(prompt, "Browser_Page_Read") {
		t.Error("system prompt missing Browser_Page_Read")
	}
	if !strings.Contains(prompt, "Filesystem_File_Write") {
		t.Error("system prompt missing Filesystem_File_Write")
	}
	// Should contain JSON schema content.
	if !strings.Contains(prompt, "```json") {
		t.Error("system prompt missing JSON schema blocks")
	}
}

func TestBuildSystemPrompt_EmptyCapabilities(t *testing.T) {
	kdl := `
id "test-agent"
provider "anthropic"
cpu-shares 1024
`
	f := filepath.Join(t.TempDir(), "agent.kdl")
	if err := os.WriteFile(f, []byte(kdl), 0o644); err != nil {
		t.Fatal(err)
	}

	prompt := buildSystemPrompt(f)
	if prompt != "" {
		t.Errorf("empty capabilities should return empty prompt, got len=%d", len(prompt))
	}
}

func TestBuildSystemPrompt_UnknownCapability(t *testing.T) {
	kdl := `
id "test-agent"
capabilities {
    Nonexistent_Tool_Call
}
`
	f := filepath.Join(t.TempDir(), "agent.kdl")
	if err := os.WriteFile(f, []byte(kdl), 0o644); err != nil {
		t.Fatal(err)
	}

	prompt := buildSystemPrompt(f)
	if !strings.Contains(prompt, "Nonexistent_Tool_Call") {
		t.Error("unknown capability should still appear in prompt")
	}
	if !strings.Contains(prompt, "schema unavailable") {
		t.Error("unknown capability should note unavailable schema")
	}
}

func TestCountCapLines(t *testing.T) {
	prompt := "### Browser_Page_Read\nschema\n\n### Filesystem_File_Write\nschema\n"
	if n := countCapLines(prompt); n != 2 {
		t.Errorf("countCapLines = %d, want 2", n)
	}
}
