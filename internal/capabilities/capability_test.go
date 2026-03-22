package capabilities

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// ---- ACL tests (TestCapabilityACL) -----------------------------------------

func TestCapabilityACL_AllowedCapability(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&FilesystemFileWrite{})

	d := NewDispatcher(reg)
	d.SetACL(&ACL{
		AgentID: "agent-1",
		Entries: []ACLEntry{{CapabilityName: FilesystemFileWriteName, Scope: t.TempDir()}},
	})

	args, _ := json.Marshal(FilesystemFileWriteArgs{
		Path: "out.txt", Content: "hello",
	})
	resp, err := d.Dispatch(context.Background(), Request{
		Name: FilesystemFileWriteName, AgentID: "agent-1", SeqNo: 1, Args: args,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("expected OK, got error_code=%s detail=%s", resp.ErrorCode, resp.ErrorDetail)
	}
}

func TestCapabilityACL_UnauthorisedCapability(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&FilesystemFileWrite{})

	d := NewDispatcher(reg)
	d.SetACL(&ACL{
		AgentID: "agent-1",
		Entries: []ACLEntry{}, // no entries — nothing allowed
	})

	args, _ := json.Marshal(FilesystemFileWriteArgs{Path: "x.txt", Content: "data"})
	resp, err := d.Dispatch(context.Background(), Request{
		Name: FilesystemFileWriteName, AgentID: "agent-1", SeqNo: 2, Args: args,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || resp.ErrorCode != "capability_denied" {
		t.Fatalf("expected capability_denied, got ok=%v code=%s", resp.OK, resp.ErrorCode)
	}
}

func TestCapabilityACL_UnknownAgent(t *testing.T) {
	reg := NewRegistry()
	d := NewDispatcher(reg)
	// No ACL registered for "ghost".

	resp, err := d.Dispatch(context.Background(), Request{
		Name: FilesystemFileWriteName, AgentID: "ghost", SeqNo: 3,
		Args: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || resp.ErrorCode != "capability_denied" {
		t.Fatalf("expected capability_denied for unknown agent, got ok=%v code=%s", resp.OK, resp.ErrorCode)
	}
}

// ---- Filesystem write tests (TestFilesystemWrite) --------------------------

func TestFilesystemWrite_AllowedWrite(t *testing.T) {
	scope := t.TempDir()
	cap := &FilesystemFileWrite{}
	ctx := contextWithScope(context.Background(), scope)

	args, _ := json.Marshal(FilesystemFileWriteArgs{Path: "result.txt", Content: "test data"})
	resp, err := cap.Execute(ctx, Request{
		Name: FilesystemFileWriteName, AgentID: "a", SeqNo: 1, Args: args,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("expected OK: %s %s", resp.ErrorCode, resp.ErrorDetail)
	}
	got, err := os.ReadFile(filepath.Join(scope, "result.txt"))
	if err != nil || string(got) != "test data" {
		t.Fatalf("file content mismatch: %q %v", got, err)
	}
}

func TestFilesystemWrite_PathTraversal(t *testing.T) {
	scope := t.TempDir()
	cap := &FilesystemFileWrite{}
	ctx := contextWithScope(context.Background(), scope)

	for _, badPath := range []string{
		"../escape.txt",
		"../../etc/passwd",
		"sub/../../outside.txt",
	} {
		args, _ := json.Marshal(FilesystemFileWriteArgs{Path: badPath, Content: "x"})
		resp, err := cap.Execute(ctx, Request{
			Name: FilesystemFileWriteName, AgentID: "a", SeqNo: 1, Args: args,
		})
		if err != nil {
			t.Fatal(err)
		}
		if resp.OK {
			t.Errorf("path traversal %q should be denied but was allowed", badPath)
		}
	}
}

func TestFilesystemWrite_AbsolutePath(t *testing.T) {
	scope := t.TempDir()
	cap := &FilesystemFileWrite{}
	ctx := contextWithScope(context.Background(), scope)

	args, _ := json.Marshal(FilesystemFileWriteArgs{Path: "/etc/passwd", Content: "x"})
	resp, err := cap.Execute(ctx, Request{
		Name: FilesystemFileWriteName, AgentID: "a", SeqNo: 1, Args: args,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK {
		t.Fatal("absolute path should be denied")
	}
}

func TestFilesystemWrite_NoScope(t *testing.T) {
	cap := &FilesystemFileWrite{}
	ctx := contextWithScope(context.Background(), "") // empty scope

	args, _ := json.Marshal(FilesystemFileWriteArgs{Path: "out.txt", Content: "x"})
	resp, err := cap.Execute(ctx, Request{
		Name: FilesystemFileWriteName, AgentID: "a", SeqNo: 1, Args: args,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK {
		t.Fatal("write without scope should be denied")
	}
}

func TestFilesystemWrite_Append(t *testing.T) {
	scope := t.TempDir()
	cap := &FilesystemFileWrite{}
	ctx := contextWithScope(context.Background(), scope)

	for _, content := range []string{"line1\n", "line2\n"} {
		args, _ := json.Marshal(FilesystemFileWriteArgs{Path: "log.txt", Content: content, Append: true})
		resp, err := cap.Execute(ctx, Request{Args: args, AgentID: "a", SeqNo: 1})
		if err != nil || !resp.OK {
			t.Fatalf("append failed: %v %s", err, resp.ErrorDetail)
		}
	}
	got, _ := os.ReadFile(filepath.Join(scope, "log.txt"))
	if string(got) != "line1\nline2\n" {
		t.Fatalf("append content wrong: %q", got)
	}
}

// ---- Browser URL scope tests -----------------------------------------------

func TestBrowserPageRead_URLScope(t *testing.T) {
	cases := []struct {
		url   string
		scope string
		ok    bool
	}{
		{"https://example.com/page", "https://example.com", true},
		{"https://example.com/page", "https://other.com", false},
		{"https://example.com/page", "", false},
		{"https://a.com", "https://b.com,https://a.com", true},
		{"not-a-url", "https://example.com", false},
		{"http://example.com", "https://example.com", false}, // scheme mismatch
	}
	for _, tc := range cases {
		got := urlMatchesScope(tc.url, tc.scope)
		if got != tc.ok {
			t.Errorf("urlMatchesScope(%q, %q) = %v, want %v", tc.url, tc.scope, got, tc.ok)
		}
	}
}
