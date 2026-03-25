package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxRuntime_InstallCapabilityCLIs(t *testing.T) {
	root := t.TempDir()
	r := &LinuxRuntime{CapwrapBinaryPath: "/usr/bin/capwrap"}

	caps := []string{"Browser_Page_Read", "Filesystem_File_Write"}
	if err := r.InstallCapabilityCLIs(root, caps); err != nil {
		t.Fatalf("InstallCapabilityCLIs: %v", err)
	}

	for _, name := range caps {
		link := filepath.Join(root, "usr", "bin", name)
		target, err := os.Readlink(link)
		if err != nil {
			t.Errorf("symlink %s missing: %v", name, err)
			continue
		}
		if target != "/usr/bin/capwrap" {
			t.Errorf("symlink %s → %q, want /usr/bin/capwrap", name, target)
		}
	}
}

func TestLinuxRuntime_InstallCapabilityCLIs_Idempotent(t *testing.T) {
	root := t.TempDir()
	r := &LinuxRuntime{CapwrapBinaryPath: "/usr/bin/capwrap"}
	caps := []string{"Browser_Page_Read"}

	// Install twice — should not fail on the second call.
	if err := r.InstallCapabilityCLIs(root, caps); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if err := r.InstallCapabilityCLIs(root, caps); err != nil {
		t.Fatalf("second install (idempotent): %v", err)
	}
}

func TestLinuxRuntime_InstallCapabilityCLIs_DefaultPath(t *testing.T) {
	root := t.TempDir()
	r := &LinuxRuntime{} // CapwrapBinaryPath empty → default

	if err := r.InstallCapabilityCLIs(root, []string{"Browser_Page_Read"}); err != nil {
		t.Fatalf("InstallCapabilityCLIs: %v", err)
	}
	target, err := os.Readlink(filepath.Join(root, "usr", "bin", "Browser_Page_Read"))
	if err != nil {
		t.Fatalf("symlink missing: %v", err)
	}
	if target != "/usr/bin/capwrap" {
		t.Errorf("got %q, want /usr/bin/capwrap", target)
	}
}

func TestStubRuntime_InstallCapabilityCLIs(t *testing.T) {
	r := &StubRuntime{}
	if err := r.InstallCapabilityCLIs("/any/path", []string{"Browser_Page_Read"}); err != nil {
		t.Errorf("StubRuntime.InstallCapabilityCLIs returned unexpected error: %v", err)
	}
}
