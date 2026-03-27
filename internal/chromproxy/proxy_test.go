package chromproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---- WhitelistPolicy -------------------------------------------------------

func TestWhitelistPolicy_EmptyPolicyDenies(t *testing.T) {
	if (WhitelistPolicy{}).Allows("https://example.com/page") {
		t.Error("empty proxy policy should deny URLs")
	}
}

func TestWhitelistPolicy_MatchingPrefix(t *testing.T) {
	cases := []struct {
		url    string
		policy WhitelistPolicy
		want   bool
	}{
		{"https://example.com/page", WhitelistPolicy{Prefixes: []string{"https://example.com"}}, true},
		{"https://example.com/page", WhitelistPolicy{Prefixes: []string{"https://other.com"}}, false},
		{"https://sub.example.com/", WhitelistPolicy{Prefixes: []string{"https://example.com"}}, false},
		{"http://example.com/page", WhitelistPolicy{Prefixes: []string{"https://example.com"}}, false},
		{"", WhitelistPolicy{Prefixes: []string{"https://example.com"}}, false},
	}
	for _, tc := range cases {
		got := tc.policy.Allows(tc.url)
		if got != tc.want {
			t.Errorf("policy.Allows(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

func TestWhitelistPolicy_TypedConstraints(t *testing.T) {
	policy := WhitelistPolicy{
		Domains:        []string{"example.com:8443", "specific.test"},
		DomainSuffixes: []string{"wikipedia.org"},
		PathPrefixes:   []string{"/allowed", "/api/v1"},
	}
	cases := []struct {
		url  string
		want bool
	}{
		// Domain match + allowed path.
		{"https://example.com:8443/allowed/page", true},
		{"https://specific.test/api/v1/resource", true},
		// Domain match + disallowed path.
		{"https://example.com:8443/disallowed", false},
		{"https://specific.test/other", false},
		// Domain suffix match + allowed path.
		{"https://en.wikipedia.org/allowed/VIVARY", true},
		// Domain suffix match + disallowed path.
		{"https://en.wikipedia.org/wiki/VIVARY", false},
		// No domain match.
		{"https://random.test/allowed/page", false},
	}
	for _, tc := range cases {
		got := policy.Allows(tc.url)
		if got != tc.want {
			t.Errorf("policy.Allows(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

func TestWhitelistPolicy_PathPrefixesEmpty(t *testing.T) {
	// If PathPrefixes is empty, any path on an allowed domain should work.
	policy := WhitelistPolicy{
		Domains: []string{"example.com"},
	}
	cases := []struct {
		url  string
		want bool
	}{
		{"https://example.com/", true},
		{"https://example.com/any/path", true},
		{"https://other.com/", false},
	}
	for _, tc := range cases {
		got := policy.Allows(tc.url)
		if got != tc.want {
			t.Errorf("policy.Allows(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

// ---- Proxy.ReadPage — no Chrome running ------------------------------------

// TestReadPage_ChromeUnavailable verifies that ReadPage returns an error
// (not a panic) when the Chrome debug port is not reachable.
func TestReadPage_ChromeUnavailable(t *testing.T) {
	// Use a port that is definitely not in use.
	p := New("127.0.0.1:19222")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := p.ReadPage(ctx, "test-agent", "https://example.com", WhitelistPolicy{Prefixes: []string{"https://example.com"}}, "networkidle", 1024)
	if err == nil {
		t.Error("expected error when Chrome is unavailable, got nil")
	}
}

func TestReadPage_DeniedBeforeChromeDial(t *testing.T) {
	p := New("127.0.0.1:19222")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := p.ReadPage(ctx, "test-agent", "https://example.com", WhitelistPolicy{Prefixes: []string{"https://other.com"}}, "networkidle", 1024)
	if err == nil {
		t.Fatal("expected deny error, got nil")
	}
	if !strings.Contains(err.Error(), "not permitted") {
		t.Fatalf("expected deny error, got %v", err)
	}
}

// ---- Proxy.ReadPage — mock HTTP server (no actual Chrome) -----------------

// mockCDPServer simulates enough of Chrome's debug HTTP API for testing
// the proxy connection path.  It responds to /json/new with a fake target
// and then closes the WebSocket upgrade immediately.
type mockCDPServer struct {
	server *httptest.Server
}

func newMockCDPServer(t *testing.T) *mockCDPServer {
	t.Helper()
	mux := http.NewServeMux()

	// /json/new — Chrome creates a new tab and returns its target info.
	mux.HandleFunc("/json/new", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]string{
			"id":                   "fake-target-id",
			"type":                 "page",
			"webSocketDebuggerUrl": "ws://placeholder/devtools/page/fake-target-id",
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	// /json — list targets
	mux.HandleFunc("/json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &mockCDPServer{server: srv}
}

// TestReadPage_MockServer_ConnectionError verifies the proxy fails gracefully
// when Chrome returns a target but the WebSocket upgrade fails (no CDP support
// in the mock).
func TestReadPage_MockServer_ConnectionError(t *testing.T) {
	mock := newMockCDPServer(t)

	// Strip "http://" to get host:port for the proxy.
	addr := strings.TrimPrefix(mock.server.URL, "http://")
	p := New(addr)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// The proxy will contact /json/new, get a target, then try to upgrade to
	// WebSocket — which will fail because our mock doesn't implement CDP.
	_, err := p.ReadPage(ctx, "test-agent", "https://example.com", WhitelistPolicy{Prefixes: []string{"https://example.com"}}, "networkidle", 1024)
	if err == nil {
		t.Error("expected error (WebSocket upgrade fails on mock server), got nil")
	}
}

// TestReadPage_ContextCancellation verifies that ReadPage respects context cancellation.
func TestReadPage_ContextCancellation(t *testing.T) {
	p := New("127.0.0.1:19222")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := p.ReadPage(ctx, "test-agent", "https://example.com", WhitelistPolicy{Prefixes: []string{"https://example.com"}}, "networkidle", 1024)
	if err == nil {
		t.Error("expected error for cancelled context, got nil")
	}
}
