package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	chromedapi "vivary.dev/vivary/internal/chromed"
	"vivary.dev/vivary/internal/switchboard"
)

type fakeProcess struct {
	waitCh chan error
	killed bool
}

func (p *fakeProcess) Kill() error {
	p.killed = true
	select {
	case <-p.waitCh:
	default:
		close(p.waitCh)
	}
	return nil
}

func (p *fakeProcess) Wait() error {
	err, ok := <-p.waitCh
	if !ok {
		return nil
	}
	return err
}

func (p *fakeProcess) Pid() int { return 4242 }

func newTestManager(t *testing.T) (*manager, *fakeProcess) {
	t.Helper()
	proc := &fakeProcess{waitCh: make(chan error)}
	mgr := newManager(t.TempDir(), "/usr/bin/google-chrome-beta", "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	mgr.allocDebug = func(string) (string, error) { return "127.0.0.1:45555", nil }
	mgr.start = func(context.Context, launchSpec) (managedProcess, error) { return proc, nil }
	mgr.waitReady = func(context.Context, string) error { return nil }
	return mgr, proc
}

func TestManagerAcquireStatusRelease(t *testing.T) {
	mgr, proc := newTestManager(t)

	acq, err := mgr.Acquire(context.Background(), chromedapi.AcquireRequest{AgentID: "agent1", ProxyServer: "http://127.0.0.1:7777", TimeoutSec: 1})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if !acq.OK || acq.DebugAddr != "127.0.0.1:45555" {
		t.Fatalf("unexpected acquire response: %+v", acq)
	}
	if _, err := os.Stat(acq.ProfileDir); err != nil {
		t.Fatalf("profile dir not created: %v", err)
	}

	status, err := mgr.Status("agent1")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.Found || status.DebugAddr != acq.DebugAddr {
		t.Fatalf("unexpected status: %+v", status)
	}

	rel, err := mgr.Release("agent1")
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if !rel.OK {
		t.Fatalf("unexpected release response: %+v", rel)
	}
	if !proc.killed {
		t.Fatal("expected process to be killed on release")
	}

	status, err = mgr.Status("agent1")
	if err != nil {
		t.Fatalf("Status after release: %v", err)
	}
	if status.Found {
		t.Fatalf("expected no instance after release, got %+v", status)
	}
}

func TestManagerAcquirePassesHeadlessFlagAndUsesAgentProfileDir(t *testing.T) {
	mgr, proc := newTestManager(t)
	var got launchSpec
	mgr.start = func(_ context.Context, spec launchSpec) (managedProcess, error) {
		got = spec
		return proc, nil
	}

	_, err := mgr.Acquire(context.Background(), chromedapi.AcquireRequest{
		AgentID:     "agent-visible",
		ProxyServer: "http://127.0.0.1:7777",
		TimeoutSec:  1,
		Headless:    false,
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if got.Headless {
		t.Fatal("Headless = true, want false")
	}
	if !strings.HasSuffix(got.ProfileDir, filepath.Join("agent-visible")) {
		t.Fatalf("ProfileDir = %q, want suffix %q", got.ProfileDir, filepath.Join("agent-visible"))
	}
}

func TestManagerAcquireReadyFailureKillsProcess(t *testing.T) {
	mgr, proc := newTestManager(t)
	mgr.waitReady = func(context.Context, string) error { return errors.New("not ready") }

	_, err := mgr.Acquire(context.Background(), chromedapi.AcquireRequest{AgentID: "agent1", ProxyServer: "http://127.0.0.1:7777", TimeoutSec: 1})
	if err == nil {
		t.Fatal("expected acquire error")
	}
	if !proc.killed {
		t.Fatal("expected process to be killed after readiness failure")
	}
}

func chromedRoundTrip[T interface{ MarshalMUS() []byte }, R interface{ UnmarshalMUS(io.Reader) error }](t *testing.T, socketPath string, msgType switchboard.MsgType, payload T, resp R) {
	t.Helper()
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial chromed: %v", err)
	}
	defer conn.Close()
	hdr := switchboard.SwarmHeader{Version: 0, Type: msgType, FromID: "keeper", ToID: chromedapi.Identity, SeqNo: 1}
	if err := switchboard.WriteFrame(conn, hdr, payload.MarshalMUS()); err != nil {
		t.Fatalf("write request: %v", err)
	}
	respHdr, respPayload, err := switchboard.ReadFrame(conn)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if respHdr.Type != msgType {
		t.Fatalf("response type = %v, want %v", respHdr.Type, msgType)
	}
	if err := resp.UnmarshalMUS(strings.NewReader(string(respPayload))); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func TestServerAcquireStatusReleaseRoundTrip(t *testing.T) {
	mgr, proc := newTestManager(t)
	socketPath := filepath.Join(t.TempDir(), "chromed.sock")
	srv := &server{socketPath: socketPath, manager: mgr, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	var acq chromedapi.AcquireResponse
	chromedRoundTrip(t, socketPath, chromedapi.MsgTypeAcquire, &chromedapi.AcquireRequest{AgentID: "agent1", ProxyServer: "http://127.0.0.1:7777", TimeoutSec: 1}, &acq)
	if !acq.OK {
		t.Fatalf("acquire failed: %+v", acq)
	}

	var status chromedapi.StatusResponse
	chromedRoundTrip(t, socketPath, chromedapi.MsgTypeStatus, &chromedapi.StatusRequest{AgentID: "agent1"}, &status)
	if !status.Found || status.DebugAddr != acq.DebugAddr {
		t.Fatalf("unexpected status: %+v", status)
	}

	var rel chromedapi.ReleaseResponse
	chromedRoundTrip(t, socketPath, chromedapi.MsgTypeRelease, &chromedapi.ReleaseRequest{AgentID: "agent1"}, &rel)
	if !rel.OK {
		t.Fatalf("release failed: %+v", rel)
	}
	if !proc.killed {
		t.Fatal("expected process kill on release")
	}
}

func TestServerStopsWhenContainerStops(t *testing.T) {
	mgr, _ := newTestManager(t)
	socketPath := filepath.Join(t.TempDir(), "chromed.sock")
	checkCount := 0
	srv := &server{
		socketPath:      socketPath,
		manager:         mgr,
		log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		containerName:   "vivary",
		monitorInterval: 20 * time.Millisecond,
		checkContainer: func(context.Context, string) (bool, error) {
			checkCount++
			return checkCount < 2, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected chromed socket to disappear after container stop")
}
