package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	chromedapi "vivary.dev/vivary/internal/chromed"
	"vivary.dev/vivary/internal/chromproxy"
	"vivary.dev/vivary/internal/switchboard"
)

type fakePageReader struct {
	callCount int
	lastAgent string
	lastURL   string
	text      string
	err       error
}

func (f *fakePageReader) ReadPage(_ context.Context, agentID, targetURL string, _ chromproxy.WhitelistPolicy, _ string, _ int) (string, error) {
	f.callCount++
	f.lastAgent = agentID
	f.lastURL = targetURL
	return f.text, f.err
}

type fakeChromedClient struct {
	acquireCount int
	releaseCount int
	lastProxyURL string
	acquireResp  chromedapi.AcquireResponse
	acquireErr   error
	releaseErr   error
}

type fakeChromedSocketServer struct {
	mu             sync.Mutex
	acquireCount   int
	releaseCount   int
	lastAcquireReq chromedapi.AcquireRequest
	socketPath     string
	ln             net.Listener
}

func startFakeChromedSocketServer(t *testing.T, acquireResp chromedapi.AcquireResponse) *fakeChromedSocketServer {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "chromed.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen fake chromed socket: %v", err)
	}
	srv := &fakeChromedSocketServer{socketPath: sock, ln: ln}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				hdr, payload, err := switchboard.ReadFrame(conn)
				if err != nil {
					return
				}
				respHdr := switchboard.SwarmHeader{Version: 0, Type: hdr.Type, FromID: chromedapi.Identity, ToID: hdr.FromID, SeqNo: hdr.SeqNo}
				switch hdr.Type {
				case chromedapi.MsgTypeAcquire:
					var req chromedapi.AcquireRequest
					if err := req.UnmarshalMUS(bytes.NewReader(payload)); err != nil {
						_ = switchboard.WriteFrame(conn, respHdr, (&chromedapi.AcquireResponse{OK: false, Error: err.Error()}).MarshalMUS())
						return
					}
					srv.mu.Lock()
					srv.acquireCount++
					srv.lastAcquireReq = req
					srv.mu.Unlock()
					_ = switchboard.WriteFrame(conn, respHdr, (&acquireResp).MarshalMUS())
				case chromedapi.MsgTypeRelease:
					srv.mu.Lock()
					srv.releaseCount++
					srv.mu.Unlock()
					_ = switchboard.WriteFrame(conn, respHdr, (&chromedapi.ReleaseResponse{OK: true}).MarshalMUS())
				case chromedapi.MsgTypeStatus:
					_ = switchboard.WriteFrame(conn, respHdr, (&chromedapi.StatusResponse{Found: true, Running: true, DebugAddr: acquireResp.DebugAddr, ProfileDir: acquireResp.ProfileDir}).MarshalMUS())
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		_ = os.Remove(sock)
	})
	return srv
}

func (f *fakeChromedClient) Acquire(_ context.Context, req chromedapi.AcquireRequest) (chromedapi.AcquireResponse, error) {
	f.acquireCount++
	f.lastProxyURL = req.ProxyServer
	if f.acquireErr != nil {
		return chromedapi.AcquireResponse{}, f.acquireErr
	}
	return f.acquireResp, nil
}

func (f *fakeChromedClient) Release(context.Context, string) (chromedapi.ReleaseResponse, error) {
	f.releaseCount++
	if f.releaseErr != nil {
		return chromedapi.ReleaseResponse{}, f.releaseErr
	}
	return chromedapi.ReleaseResponse{OK: true}, nil
}

func (f *fakeChromedClient) Status(context.Context, string) (chromedapi.StatusResponse, error) {
	return chromedapi.StatusResponse{}, nil
}

func TestBrowserManagerRelease(t *testing.T) {
	fake := &fakeChromedClient{}
	alloc := newProxyAllocator("127.0.0.1", nil)
	mgr := newBrowserManager(fake, alloc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mgr.proxyByID["agent1"] = &fakePageReader{}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	mgr.proxies.instances["agent1"] = &proxyInstance{url: "http://127.0.0.1:1", listener: ln, server: &http.Server{}}
	if err := mgr.Release(context.Background(), "agent1"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if fake.releaseCount != 1 {
		t.Fatalf("release count = %d, want 1", fake.releaseCount)
	}
	if _, ok := mgr.proxyByID["agent1"]; ok {
		t.Fatal("proxy cache entry not removed")
	}
}

func TestBrowserManagerRelease_PropagatesErrors(t *testing.T) {
	fake := &fakeChromedClient{releaseErr: errors.New("boom")}
	mgr := newBrowserManager(fake, nil, nil)
	if err := mgr.Release(context.Background(), "agent1"); err == nil {
		t.Fatal("expected release error")
	}
}

func TestBrowserManagerReadPage_AcquiresAndCachesProxy(t *testing.T) {
	fake := &fakeChromedClient{acquireResp: chromedapi.AcquireResponse{OK: true, DebugAddr: "127.0.0.1:45555", ProfileDir: "/tmp/agent1"}}
	reader := &fakePageReader{text: "page text"}
	alloc := newProxyAllocator("127.0.0.1", nil)
	mgr := newBrowserManager(fake, alloc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mgr.newProxy = func(string) pageReader { return reader }

	policy := chromproxy.WhitelistPolicy{Domains: []string{"example.com"}}
	text, err := mgr.ReadPage(context.Background(), "agent1", "https://example.com/page", policy, "networkidle", 1024)
	if err != nil {
		t.Fatalf("ReadPage first call: %v", err)
	}
	if text != "page text" {
		t.Fatalf("text = %q, want page text", text)
	}
	text, err = mgr.ReadPage(context.Background(), "agent1", "https://example.com/again", policy, "networkidle", 1024)
	if err != nil {
		t.Fatalf("ReadPage second call: %v", err)
	}
	if fake.acquireCount != 1 {
		t.Fatalf("acquire count = %d, want 1", fake.acquireCount)
	}
	if !strings.HasPrefix(fake.lastProxyURL, "http://127.0.0.1:87") {
		t.Fatalf("proxy URL = %q", fake.lastProxyURL)
	}
	if reader.callCount != 2 {
		t.Fatalf("reader call count = %d, want 2", reader.callCount)
	}
}

func TestBrowserManagerReadPage_PropagatesAcquireErrors(t *testing.T) {
	fake := &fakeChromedClient{acquireErr: errors.New("no chromed")}
	mgr := newBrowserManager(fake, newProxyAllocator("127.0.0.1", nil), nil)
	_, err := mgr.ReadPage(context.Background(), "agent1", "https://example.com/page", chromproxy.WhitelistPolicy{Domains: []string{"example.com"}}, "networkidle", 1024)
	if err == nil {
		t.Fatal("expected acquire error")
	}
}

func TestBrowserManagerReadPage_UsesRealChromedSocketClient(t *testing.T) {
	srv := startFakeChromedSocketServer(t, chromedapi.AcquireResponse{OK: true, DebugAddr: "127.0.0.1:45555", ProfileDir: "/tmp/agent1"})
	reader := &fakePageReader{text: "page text"}
	mgr := newBrowserManager(&chromedapi.Client{SocketPath: srv.socketPath}, newProxyAllocator("127.0.0.1", nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
	mgr.newProxy = func(string) pageReader { return reader }

	text, err := mgr.ReadPage(context.Background(), "agent1", "https://example.com/page", chromproxy.WhitelistPolicy{Domains: []string{"example.com"}}, "networkidle", 1024)
	if err != nil {
		t.Fatalf("ReadPage: %v", err)
	}
	if text != "page text" {
		t.Fatalf("text = %q, want page text", text)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.acquireCount != 1 {
		t.Fatalf("acquire count = %d, want 1", srv.acquireCount)
	}
	if !strings.HasPrefix(srv.lastAcquireReq.ProxyServer, "http://127.0.0.1:87") {
		t.Fatalf("proxy server = %q", srv.lastAcquireReq.ProxyServer)
	}
}
