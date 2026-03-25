package main

import "testing"

func TestWithPreferredPATHPrependsToolDirs(t *testing.T) {
	env := []string{"HOME=/tmp/demo", "PATH=/nix/store/systemd/bin"}
	out := withPreferredPATH(append([]string(nil), env...))
	got := ""
	for _, kv := range out {
		if len(kv) > 5 && kv[:5] == "PATH=" {
			got = kv
			break
		}
	}
	want := "PATH=/usr/local/bin:/usr/bin:/bin:/nix/store/systemd/bin"
	if got != want {
		t.Fatalf("PATH = %q, want %q", got, want)
	}
}

func TestWithPreferredPATHAddsWhenMissing(t *testing.T) {
	out := withPreferredPATH([]string{"HOME=/tmp/demo"})
	found := false
	for _, kv := range out {
		if kv == "PATH=/usr/local/bin:/usr/bin:/bin" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected preferred PATH to be added, got %#v", out)
	}
}
