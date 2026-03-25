package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunMigrateOpenClawInspect(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "openclaw.json"), []byte(`{"channels":{"telegram":{"botToken":"123:abc"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "findings.kdl")
	var stdout, stderr bytes.Buffer
	if err := runMigrate([]string{"openclaw", "inspect", "--source", root, "--out", out}, &stdout, &stderr); err != nil {
		t.Fatalf("runMigrate: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !strings.Contains(stdout.String(), "wrote OpenClaw discovery findings") {
		t.Fatalf("stdout missing success message: %s", stdout.String())
	}
	if !strings.Contains(string(data), "credential \"channels.telegram.botToken\"") {
		t.Fatalf("expected findings output to mention telegram credential\n%s", string(data))
	}
}
