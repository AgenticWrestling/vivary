package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxRuntime_InstallCapabilityCLIs(t *testing.T) {
	root := t.TempDir()
	hostCapwrap := filepath.Join(t.TempDir(), "capwrap-host")
	if err := os.WriteFile(hostCapwrap, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write host capwrap: %v", err)
	}
	r := &LinuxRuntime{CapwrapBinaryPath: hostCapwrap}

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
	b, err := os.ReadFile(filepath.Join(root, "usr", "bin", "capwrap"))
	if err != nil {
		t.Fatalf("read installed capwrap: %v", err)
	}
	if string(b) != "#!/bin/sh\nexit 0\n" {
		t.Fatalf("installed capwrap contents = %q", string(b))
	}
}

func TestLinuxRuntime_InstallCapabilityCLIs_Idempotent(t *testing.T) {
	root := t.TempDir()
	hostCapwrap := filepath.Join(t.TempDir(), "capwrap-host")
	if err := os.WriteFile(hostCapwrap, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write host capwrap: %v", err)
	}
	r := &LinuxRuntime{CapwrapBinaryPath: hostCapwrap}
	caps := []string{"Browser_Page_Read"}

	// Install twice — should not fail on the second call.
	if err := r.InstallCapabilityCLIs(root, caps); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if err := r.InstallCapabilityCLIs(root, caps); err != nil {
		t.Fatalf("second install (idempotent): %v", err)
	}
}

func TestLinuxRuntime_InstallCapabilityCLIs_UsesCopiedCapwrap(t *testing.T) {
	root := t.TempDir()
	hostCapwrap := filepath.Join(t.TempDir(), "capwrap-host")
	if err := os.WriteFile(hostCapwrap, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write host capwrap: %v", err)
	}
	r := &LinuxRuntime{CapwrapBinaryPath: hostCapwrap}

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
