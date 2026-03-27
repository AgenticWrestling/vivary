package capabilities

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"vivary.dev/vivary/internal/chromproxy"
)

// fsCtx returns a context containing a path-prefix constraint for scope.
func fsCtx(scope string) context.Context {
	if scope == "" {
		return context.Background()
	}
	return contextWithConstraints(context.Background(), []ScopeConstraint{{
		Entity:      "File",
		Constraints: ConstraintSet{"path-prefix": {scope}},
	}})
}

// ---- ACL tests (TestCapabilityACL) -----------------------------------------

func TestCapabilityACL_AllowedCapability(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&FilesystemFileWrite{})

	scope := t.TempDir()
	d := NewDispatcher(reg)
	d.SetACL(&ACL{
		AgentID: "agent-1",
		Entries: []ACLEntry{{
			CapabilityName: FilesystemFileWriteName,
			Constraints: []ScopeConstraint{{
				Entity:      "File",
				Constraints: ConstraintSet{"path-prefix": {scope}},
			}},
		}},
	})

	args, _ := json.Marshal(Filesystem_File_Write{
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

	args, _ := json.Marshal(Filesystem_File_Write{Path: "x.txt", Content: "data"})
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
	ctx := fsCtx(scope)

	args, _ := json.Marshal(Filesystem_File_Write{Path: "result.txt", Content: "test data"})
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
	ctx := fsCtx(scope)

	for _, badPath := range []string{
		"../escape.txt",
		"../../etc/passwd",
		"sub/../../outside.txt",
	} {
		args, _ := json.Marshal(Filesystem_File_Write{Path: badPath, Content: "x"})
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
	ctx := fsCtx(scope)

	args, _ := json.Marshal(Filesystem_File_Write{Path: "/etc/passwd", Content: "x"})
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
	ctx := context.Background() // no constraints → no scope

	args, _ := json.Marshal(Filesystem_File_Write{Path: "out.txt", Content: "x"})
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
	ctx := fsCtx(scope)

	for _, content := range []string{"line1\n", "line2\n"} {
		args, _ := json.Marshal(Filesystem_File_Write{Path: "log.txt", Content: content, Append: true})
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

// ---- Symlink escape tests (TestFilesystemWrite_Symlink*) -------------------

func TestFilesystemWrite_SymlinkInsideScope(t *testing.T) {
	scope := t.TempDir()
	// Create a real file inside scope, then symlink to it within scope.
	real := filepath.Join(scope, "real.txt")
	if err := os.WriteFile(real, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(scope, "link.txt")); err != nil {
		t.Fatal(err)
	}

	cap := &FilesystemFileWrite{}
	ctx := fsCtx(scope)
	args, _ := json.Marshal(Filesystem_File_Write{Path: "link.txt", Content: "via link"})
	resp, err := cap.Execute(ctx, Request{Args: args, AgentID: "a", SeqNo: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("symlink within scope should be allowed: %s %s", resp.ErrorCode, resp.ErrorDetail)
	}
}

func TestFilesystemWrite_SymlinkEscapeScope(t *testing.T) {
	scope := t.TempDir()
	outside := t.TempDir()
	// Create a symlink inside scope pointing at an outside directory.
	if err := os.Symlink(outside, filepath.Join(scope, "escape")); err != nil {
		t.Fatal(err)
	}

	cap := &FilesystemFileWrite{}
	ctx := fsCtx(scope)
	args, _ := json.Marshal(Filesystem_File_Write{Path: "escape", Content: "x"})
	resp, err := cap.Execute(ctx, Request{Args: args, AgentID: "a", SeqNo: 1})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK {
		t.Fatal("symlink pointing outside scope should be denied")
	}
}

func TestFilesystemWrite_SymlinkIntermediateDirEscape(t *testing.T) {
	scope := t.TempDir()
	outside := t.TempDir()
	// An intermediate directory component is itself a symlink to outside.
	if err := os.Symlink(outside, filepath.Join(scope, "subdir")); err != nil {
		t.Fatal(err)
	}

	cap := &FilesystemFileWrite{}
	ctx := fsCtx(scope)
	args, _ := json.Marshal(Filesystem_File_Write{Path: "subdir/file.txt", Content: "x"})
	resp, err := cap.Execute(ctx, Request{Args: args, AgentID: "a", SeqNo: 1})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK {
		t.Fatal("write through symlinked intermediate dir escaping scope should be denied")
	}
}

func TestFilesystemWrite_SymlinkLoop(t *testing.T) {
	scope := t.TempDir()
	// Create a circular symlink: a → b → a
	a := filepath.Join(scope, "a")
	b := filepath.Join(scope, "b")
	if err := os.Symlink(b, a); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(a, b); err != nil {
		t.Fatal(err)
	}

	cap := &FilesystemFileWrite{}
	ctx := fsCtx(scope)
	args, _ := json.Marshal(Filesystem_File_Write{Path: "a", Content: "loop"})
	resp, err := cap.Execute(ctx, Request{Args: args, AgentID: "a", SeqNo: 1})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK {
		t.Fatal("symlink loop should be denied")
	}
}

// ---- BrowserPageRead.Execute integration tests (TestCapabilityACL / PLAN §3.2) ----
//
// These exercise the full Execute path (scope check + proxy call) to verify
// that both the capability-layer and proxy-layer whitelist checks fire
// correctly.

// stubProxy returns a ChromeProxy function whose behaviour is controlled by the
// test: allow is called on allowed requests, deny signals unexpected calls.
func stubProxy(t *testing.T, wantAllow bool) func(ctx context.Context, agentID, targetURL string, policy chromproxy.WhitelistPolicy, waitFor string, maxChars int) (string, error) {
	t.Helper()
	return func(_ context.Context, _, _ string, _ chromproxy.WhitelistPolicy, _ string, _ int) (string, error) {
		if !wantAllow {
			t.Error("stubProxy called unexpectedly — scope check should have denied before reaching the proxy")
		}
		return "stub page text", nil
	}
}

func TestBrowserPageRead_Execute_EmptyConstraintsDenies(t *testing.T) {
	cap := &BrowserPageRead{ChromeProxy: stubProxy(t, false)}
	ctx := context.Background() // no constraints
	args, _ := json.Marshal(Browser_Page_Read{URL: "https://example.com/page"})

	resp, err := cap.Execute(ctx, Request{Name: BrowserPageReadName, AgentID: "a", SeqNo: 1, Args: args})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK {
		t.Fatal("empty constraints should deny the request")
	}
	if resp.ErrorCode != "capability_denied" {
		t.Errorf("error_code = %q, want capability_denied", resp.ErrorCode)
	}
}

func TestBrowserPageRead_Execute_DomainConstraintAllows(t *testing.T) {
	cap := &BrowserPageRead{ChromeProxy: stubProxy(t, true)}
	ctx := contextWithConstraints(context.Background(), []ScopeConstraint{{
		Entity:      "Link",
		Constraints: ConstraintSet{"domain": {"example.com"}},
	}})
	args, _ := json.Marshal(Browser_Page_Read{URL: "https://example.com/page"})

	resp, err := cap.Execute(ctx, Request{Name: BrowserPageReadName, AgentID: "a", SeqNo: 1, Args: args})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("domain constraint should allow: %s %s", resp.ErrorCode, resp.ErrorDetail)
	}
}

func TestBrowserPageRead_Execute_DomainConstraintDeniesOther(t *testing.T) {
	cap := &BrowserPageRead{ChromeProxy: stubProxy(t, false)}
	ctx := contextWithConstraints(context.Background(), []ScopeConstraint{{
		Entity:      "Link",
		Constraints: ConstraintSet{"domain": {"other.com"}},
	}})
	args, _ := json.Marshal(Browser_Page_Read{URL: "https://example.com/page"})

	resp, err := cap.Execute(ctx, Request{Name: BrowserPageReadName, AgentID: "a", SeqNo: 1, Args: args})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK {
		t.Fatal("domain mismatch should deny")
	}
}

func TestBrowserPageRead_Execute_ECSConstraintAllows(t *testing.T) {
	cap := &BrowserPageRead{ChromeProxy: stubProxy(t, true)}
	ctx := contextWithConstraints(context.Background(), []ScopeConstraint{
		{Entity: "Link", Constraints: map[string][]string{
			"domain-suffix": {"wikipedia.org"},
		}},
	})
	args, _ := json.Marshal(Browser_Page_Read{URL: "https://en.wikipedia.org/wiki/Test"})

	resp, err := cap.Execute(ctx, Request{Name: BrowserPageReadName, AgentID: "a", SeqNo: 1, Args: args})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("ECS domain-suffix constraint should allow: %s %s", resp.ErrorCode, resp.ErrorDetail)
	}
}

func TestBrowserPageRead_Execute_PassesTypedWhitelistPolicyToProxy(t *testing.T) {
	var gotPolicy chromproxy.WhitelistPolicy
	var gotAgentID, gotURL, gotWaitFor string
	var gotMaxChars int
	cap := &BrowserPageRead{ChromeProxy: func(_ context.Context, agentID, targetURL string, policy chromproxy.WhitelistPolicy, waitFor string, maxChars int) (string, error) {
		gotAgentID = agentID
		gotURL = targetURL
		gotPolicy = policy
		gotWaitFor = waitFor
		gotMaxChars = maxChars
		return "stub page text", nil
	}}
	ctx := contextWithConstraints(context.Background(), []ScopeConstraint{
		{Entity: "Link", Constraints: map[string][]string{
			"domain":        {"example.com:8443"},
			"domain-suffix": {"wikipedia.org"},
			"path-prefix":   {"/allowed"},
		}},
		{Entity: "File", Constraints: map[string][]string{"path-prefix": {"/tmp/ignored"}}},
	})
	args, _ := json.Marshal(Browser_Page_Read{URL: "https://example.com:8443/allowed/page"})

	resp, err := cap.Execute(ctx, Request{Name: BrowserPageReadName, AgentID: "agent-browser", SeqNo: 1, Args: args})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("expected allowed browser read, got %s %s", resp.ErrorCode, resp.ErrorDetail)
	}
	if gotAgentID != "agent-browser" {
		t.Fatalf("agentID = %q, want agent-browser", gotAgentID)
	}
	if gotURL != "https://example.com:8443/allowed/page" {
		t.Fatalf("targetURL = %q", gotURL)
	}
	if gotWaitFor != "networkidle" {
		t.Fatalf("waitFor = %q, want networkidle", gotWaitFor)
	}
	if gotMaxChars != 32768 {
		t.Fatalf("maxChars = %d, want 32768", gotMaxChars)
	}
	if len(gotPolicy.Domains) != 1 || gotPolicy.Domains[0] != "example.com:8443" {
		t.Fatalf("Domains = %#v", gotPolicy.Domains)
	}
	if len(gotPolicy.DomainSuffixes) != 1 || gotPolicy.DomainSuffixes[0] != "wikipedia.org" {
		t.Fatalf("DomainSuffixes = %#v", gotPolicy.DomainSuffixes)
	}
	if len(gotPolicy.PathPrefixes) != 1 || gotPolicy.PathPrefixes[0] != "/allowed" {
		t.Fatalf("PathPrefixes = %#v", gotPolicy.PathPrefixes)
	}
}

func TestBrowserPageRead_Execute_ECSConstraintDeniesOtherDomain(t *testing.T) {
	cap := &BrowserPageRead{ChromeProxy: stubProxy(t, false)}
	ctx := contextWithConstraints(context.Background(), []ScopeConstraint{
		{Entity: "Link", Constraints: map[string][]string{
			"domain-suffix": {"wikipedia.org"},
		}},
	})
	args, _ := json.Marshal(Browser_Page_Read{URL: "https://evil.com/page"})

	resp, err := cap.Execute(ctx, Request{Name: BrowserPageReadName, AgentID: "a", SeqNo: 1, Args: args})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK {
		t.Fatal("domain not matching ECS constraint should be denied")
	}
}

func TestBrowserPageRead_Execute_ProxyDenySurfacesAsChromeError(t *testing.T) {
	cap := &BrowserPageRead{ChromeProxy: func(_ context.Context, _, _ string, _ chromproxy.WhitelistPolicy, _ string, _ int) (string, error) {
		return "", errors.New(`URL "https://example.com/page" not permitted for agent "a"`)
	}}
	ctx := contextWithConstraints(context.Background(), []ScopeConstraint{{
		Entity:      "Link",
		Constraints: ConstraintSet{"domain": {"example.com"}},
	}})
	args, _ := json.Marshal(Browser_Page_Read{URL: "https://example.com/page"})

	resp, err := cap.Execute(ctx, Request{Name: BrowserPageReadName, AgentID: "a", SeqNo: 1, Args: args})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK {
		t.Fatal("proxy deny should return an error response")
	}
	if resp.ErrorCode != "chrome_error" {
		t.Fatalf("error_code = %q, want chrome_error", resp.ErrorCode)
	}
	if resp.ErrorDetail == "" {
		t.Fatal("expected proxy deny detail to be preserved")
	}
}

func TestBrowserPageRead_Execute_NilProxyReturnsUnavailable(t *testing.T) {
	cap := &BrowserPageRead{ChromeProxy: nil}
	ctx := contextWithConstraints(context.Background(), []ScopeConstraint{{
		Entity:      "Link",
		Constraints: ConstraintSet{"domain": {"example.com"}},
	}})
	args, _ := json.Marshal(Browser_Page_Read{URL: "https://example.com/page"})

	resp, err := cap.Execute(ctx, Request{Name: BrowserPageReadName, AgentID: "a", SeqNo: 1, Args: args})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || resp.ErrorCode != "unavailable" {
		t.Fatalf("nil proxy should return unavailable, got ok=%v code=%s", resp.OK, resp.ErrorCode)
	}
}

// ---- URL constraint matching tests -----------------------------------------

func TestURLMatchesConstraints(t *testing.T) {
	cases := []struct {
		name        string
		url         string
		constraints []ScopeConstraint
		ok          bool
	}{
		{
			name: "domain exact match",
			url:  "https://example.com/page",
			constraints: []ScopeConstraint{{Entity: "Link", Constraints: ConstraintSet{
				"domain": {"example.com"},
			}}},
			ok: true,
		},
		{
			name: "domain mismatch",
			url:  "https://other.com/page",
			constraints: []ScopeConstraint{{Entity: "Link", Constraints: ConstraintSet{
				"domain": {"example.com"},
			}}},
			ok: false,
		},
		{
			name: "domain-suffix match subdomain",
			url:  "https://en.wikipedia.org/wiki/Go",
			constraints: []ScopeConstraint{{Entity: "Link", Constraints: ConstraintSet{
				"domain-suffix": {"wikipedia.org"},
			}}},
			ok: true,
		},
		{
			name: "domain-suffix exact match",
			url:  "https://wikipedia.org/",
			constraints: []ScopeConstraint{{Entity: "Link", Constraints: ConstraintSet{
				"domain-suffix": {"wikipedia.org"},
			}}},
			ok: true,
		},
		{
			name: "domain-suffix no subdomain confusion",
			url:  "https://fakewikipedia.org/",
			constraints: []ScopeConstraint{{Entity: "Link", Constraints: ConstraintSet{
				"domain-suffix": {"wikipedia.org"},
			}}},
			ok: false,
		},
		{
			name:        "no constraints denies",
			url:         "https://example.com/",
			constraints: nil,
			ok:          false,
		},
		{
			name:        "malformed url denies",
			url:         "not-a-url",
			constraints: []ScopeConstraint{{Entity: "Link", Constraints: ConstraintSet{"domain": {"example.com"}}}},
			ok:          false,
		},
		{
			name: "path-prefix requires anchored domain",
			url:  "https://evil.com/allowed/page",
			constraints: []ScopeConstraint{{Entity: "Link", Constraints: ConstraintSet{
				"domain":      {"example.com"},
				"path-prefix": {"/allowed"},
			}}},
			ok: false,
		},
		{
			name: "path-prefix with matching domain allows",
			url:  "https://example.com/allowed/page",
			constraints: []ScopeConstraint{{Entity: "Link", Constraints: ConstraintSet{
				"domain":      {"example.com"},
				"path-prefix": {"/allowed"},
			}}},
			ok: true,
		},
		{
			name: "path-prefix mismatch with matching domain denies",
			url:  "https://example.com/disallowed",
			constraints: []ScopeConstraint{{Entity: "Link", Constraints: ConstraintSet{
				"domain":      {"example.com"},
				"path-prefix": {"/allowed"},
			}}},
			ok: false,
		},
		{
			name: "path-prefix with matching domain-suffix allows",
			url:  "https://en.wikipedia.org/allowed/page",
			constraints: []ScopeConstraint{{Entity: "Link", Constraints: ConstraintSet{
				"domain-suffix": {"wikipedia.org"},
				"path-prefix":    {"/allowed"},
			}}},
			ok: true,
		},
		{
			name: "path-prefix mismatch with matching domain-suffix denies",
			url:  "https://en.wikipedia.org/wiki/Go",
			constraints: []ScopeConstraint{{Entity: "Link", Constraints: ConstraintSet{
				"domain-suffix": {"wikipedia.org"},
				"path-prefix":    {"/allowed"},
			}}},
			ok: false,
		},
		{
			name: "wrong entity ignored",
			url:  "https://example.com/",
			constraints: []ScopeConstraint{{Entity: "File", Constraints: ConstraintSet{
				"domain": {"example.com"},
			}}},
			ok: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := urlMatchesConstraints(tc.url, tc.constraints)
			if got != tc.ok {
				t.Errorf("urlMatchesConstraints(%q) = %v, want %v", tc.url, got, tc.ok)
			}
		})
	}
}
