package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPreferExistingPath(t *testing.T) {
	dir := t.TempDir()
	preferred := filepath.Join(dir, "preferred")
	fallback := filepath.Join(dir, "fallback")
	if err := os.WriteFile(preferred, []byte("x"), 0o644); err != nil {
		t.Fatalf("write preferred: %v", err)
	}
	if got := preferExistingPath(preferred, fallback); got != preferred {
		t.Fatalf("preferExistingPath() = %q, want %q", got, preferred)
	}
	if err := os.Remove(preferred); err != nil {
		t.Fatalf("remove preferred: %v", err)
	}
	if got := preferExistingPath(preferred, fallback); got != fallback {
		t.Fatalf("preferExistingPath() = %q, want %q", got, fallback)
	}
}
