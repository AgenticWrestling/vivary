package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	chromedapi "vivary.dev/vivary/internal/chromed"
	"vivary.dev/vivary/internal/switchboard"
)

type managedProcess interface {
	Kill() error
	Wait() error
	Pid() int
}

type execProcess struct {
	cmd *exec.Cmd
}

func (p *execProcess) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}

func (p *execProcess) Wait() error {
	return p.cmd.Wait()
}

func (p *execProcess) Pid() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

type launchSpec struct {
	AgentID          string
	DebugAddr        string
	ProfileDir       string
	ProxyServer      string
	ChromeBinaryPath string
}

type instance struct {
	agentID     string
	debugAddr   string
	profileDir  string
	proxyServer string
	proc        managedProcess
	running     bool
}

type manager struct {
	mu           sync.Mutex
	instances    map[string]*instance
	profileRoot  string
	chromeBinary string
	start        func(context.Context, launchSpec) (managedProcess, error)
	waitReady    func(context.Context, string) error
	allocDebug   func() (string, error)
	log          *slog.Logger
}

func newManager(profileRoot, chromeBinary string, log *slog.Logger) *manager {
	return &manager{
		instances:    make(map[string]*instance),
		profileRoot:  profileRoot,
		chromeBinary: chromeBinary,
		start:        startChromeProcess,
		waitReady:    waitForChromeReady,
		allocDebug:   allocateDebugAddr,
		log:          log,
	}
}

func (m *manager) Acquire(ctx context.Context, req chromedapi.AcquireRequest) (chromedapi.AcquireResponse, error) {
	if strings.TrimSpace(req.AgentID) == "" {
		return chromedapi.AcquireResponse{}, errors.New("agent_id is required")
	}

	m.mu.Lock()
	if inst, ok := m.instances[req.AgentID]; ok {
		resp := chromedapi.AcquireResponse{OK: true, DebugAddr: inst.debugAddr, ProfileDir: inst.profileDir}
		m.mu.Unlock()
		return resp, nil
	}
	m.mu.Unlock()

	profileDir := filepath.Join(m.profileRoot, req.AgentID)
	if err := os.MkdirAll(profileDir, 0o750); err != nil {
		return chromedapi.AcquireResponse{}, fmt.Errorf("mkdir profile dir: %w", err)
	}
	debugAddr, err := m.allocDebug()
	if err != nil {
		return chromedapi.AcquireResponse{}, fmt.Errorf("allocate debug addr: %w", err)
	}
	spec := launchSpec{
		AgentID:          req.AgentID,
		DebugAddr:        debugAddr,
		ProfileDir:       profileDir,
		ProxyServer:      req.ProxyServer,
		ChromeBinaryPath: m.chromeBinary,
	}
	proc, err := m.start(ctx, spec)
	if err != nil {
		return chromedapi.AcquireResponse{}, err
	}
	timeout := time.Duration(req.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	readyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := m.waitReady(readyCtx, debugAddr); err != nil {
		_ = proc.Kill()
		_ = proc.Wait()
		return chromedapi.AcquireResponse{}, fmt.Errorf("wait for chrome ready: %w", err)
	}

	inst := &instance{
		agentID:     req.AgentID,
		debugAddr:   debugAddr,
		profileDir:  profileDir,
		proxyServer: req.ProxyServer,
		proc:        proc,
		running:     true,
	}
	m.mu.Lock()
	m.instances[req.AgentID] = inst
	m.mu.Unlock()

	go m.reap(req.AgentID, proc)
	if m.log != nil {
		m.log.Info("chrome profile acquired", "agent", req.AgentID, "debug_addr", debugAddr, "pid", proc.Pid())
	}
	return chromedapi.AcquireResponse{OK: true, DebugAddr: debugAddr, ProfileDir: profileDir}, nil
}

func (m *manager) reap(agentID string, proc managedProcess) {
	err := proc.Wait()
	m.mu.Lock()
	inst, ok := m.instances[agentID]
	if ok && inst.proc == proc {
		delete(m.instances, agentID)
	}
	m.mu.Unlock()
	if err != nil && m.log != nil {
		m.log.Warn("chrome process exited", "agent", agentID, "err", err)
	}
}

func (m *manager) Release(agentID string) (chromedapi.ReleaseResponse, error) {
	if strings.TrimSpace(agentID) == "" {
		return chromedapi.ReleaseResponse{}, errors.New("agent_id is required")
	}
	m.mu.Lock()
	inst, ok := m.instances[agentID]
	if ok {
		delete(m.instances, agentID)
	}
	m.mu.Unlock()
	if !ok {
		return chromedapi.ReleaseResponse{OK: true}, nil
	}
	if err := inst.proc.Kill(); err != nil {
		return chromedapi.ReleaseResponse{}, fmt.Errorf("kill chrome: %w", err)
	}
	if m.log != nil {
		m.log.Info("chrome profile released", "agent", agentID, "debug_addr", inst.debugAddr)
	}
	return chromedapi.ReleaseResponse{OK: true}, nil
}

func (m *manager) Status(agentID string) (chromedapi.StatusResponse, error) {
	if strings.TrimSpace(agentID) == "" {
		return chromedapi.StatusResponse{}, errors.New("agent_id is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	inst, ok := m.instances[agentID]
	if !ok {
		return chromedapi.StatusResponse{Found: false}, nil
	}
	return chromedapi.StatusResponse{
		Found:      true,
		Running:    inst.running,
		DebugAddr:  inst.debugAddr,
		ProfileDir: inst.profileDir,
	}, nil
}

func (m *manager) ReleaseAll() {
	m.mu.Lock()
	agentIDs := make([]string, 0, len(m.instances))
	for agentID := range m.instances {
		agentIDs = append(agentIDs, agentID)
	}
	m.mu.Unlock()
	for _, agentID := range agentIDs {
		_, _ = m.Release(agentID)
	}
}

func allocateDebugAddr() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr, nil
}

func startChromeProcess(ctx context.Context, spec launchSpec) (managedProcess, error) {
	host, port, err := net.SplitHostPort(spec.DebugAddr)
	if err != nil {
		return nil, fmt.Errorf("split debug addr: %w", err)
	}
	args := []string{
		"--headless=new",
		"--disable-gpu",
		"--no-sandbox",
		"--disable-dev-shm-usage",
		"--disable-extensions",
		"--disable-background-networking",
		"--safebrowsing-disable-auto-update",
		"--no-first-run",
		"--no-default-browser-check",
		"--remote-debugging-address=" + host,
		"--remote-debugging-port=" + port,
		"--user-data-dir=" + spec.ProfileDir,
		"--proxy-server=" + spec.ProxyServer,
		"about:blank",
	}
	cmd := exec.CommandContext(ctx, spec.ChromeBinaryPath, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start chrome: %w", err)
	}
	return &execProcess{cmd: cmd}, nil
}

func waitForChromeReady(ctx context.Context, debugAddr string) error {
	client := &http.Client{Timeout: time.Second}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+debugAddr+"/json/version", nil)
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

type server struct {
	socketPath      string
	manager         *manager
	log             *slog.Logger
	containerName   string
	checkContainer  func(context.Context, string) (bool, error)
	monitorInterval time.Duration
}

func (s *server) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := os.MkdirAll(filepath.Dir(s.socketPath), 0o755); err != nil {
		return fmt.Errorf("mkdir socket dir: %w", err)
	}
	_ = os.Remove(s.socketPath)
	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.socketPath, err)
	}
	defer func() {
		_ = ln.Close()
		_ = os.Remove(s.socketPath)
		s.manager.ReleaseAll()
	}()
	if err := os.Chmod(s.socketPath, 0o660); err != nil {
		return fmt.Errorf("chmod socket: %w", err)
	}
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	if s.containerName != "" && s.checkContainer != nil {
		go s.monitorContainer(ctx, cancel)
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return fmt.Errorf("accept: %w", err)
			}
		}
		go s.handleConn(ctx, conn)
	}
}

func (s *server) monitorContainer(ctx context.Context, cancel context.CancelFunc) {
	interval := s.monitorInterval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			running, err := s.checkContainer(ctx, s.containerName)
			if err != nil {
				if s.log != nil {
					s.log.Warn("container monitor check failed", "container", s.containerName, "err", err)
				}
				continue
			}
			if !running {
				if s.log != nil {
					s.log.Info("container no longer running; stopping chromed", "container", s.containerName)
				}
				cancel()
				return
			}
		}
	}
}

func (s *server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	hdr, payload, err := switchboard.ReadFrame(conn)
	if err != nil {
		return
	}
	respType, err := chromedapi.ResponseTypeFor(hdr.Type)
	if err != nil {
		s.writeError(conn, hdr, respType, err)
		return
	}
	respHdr := switchboard.SwarmHeader{Version: 0, Type: respType, FromID: chromedapi.Identity, ToID: hdr.FromID, SeqNo: hdr.SeqNo}
	switch hdr.Type {
	case chromedapi.MsgTypeAcquire:
		var req chromedapi.AcquireRequest
		if err := req.UnmarshalMUS(bytes.NewReader(payload)); err != nil {
			s.writeAcquire(conn, respHdr, chromedapi.AcquireResponse{OK: false, Error: err.Error()})
			return
		}
		resp, err := s.manager.Acquire(ctx, req)
		if err != nil {
			resp = chromedapi.AcquireResponse{OK: false, Error: err.Error()}
		}
		s.writeAcquire(conn, respHdr, resp)
	case chromedapi.MsgTypeRelease:
		var req chromedapi.ReleaseRequest
		if err := req.UnmarshalMUS(bytes.NewReader(payload)); err != nil {
			s.writeRelease(conn, respHdr, chromedapi.ReleaseResponse{OK: false, Error: err.Error()})
			return
		}
		resp, err := s.manager.Release(req.AgentID)
		if err != nil {
			resp = chromedapi.ReleaseResponse{OK: false, Error: err.Error()}
		}
		s.writeRelease(conn, respHdr, resp)
	case chromedapi.MsgTypeStatus:
		var req chromedapi.StatusRequest
		if err := req.UnmarshalMUS(bytes.NewReader(payload)); err != nil {
			s.writeStatus(conn, respHdr, chromedapi.StatusResponse{Error: err.Error()})
			return
		}
		resp, err := s.manager.Status(req.AgentID)
		if err != nil {
			resp = chromedapi.StatusResponse{Error: err.Error()}
		}
		s.writeStatus(conn, respHdr, resp)
	default:
		s.writeError(conn, hdr, respType, fmt.Errorf("unsupported msg type %v", hdr.Type))
	}
}

func (s *server) writeAcquire(w io.Writer, hdr switchboard.SwarmHeader, resp chromedapi.AcquireResponse) {
	_ = switchboard.WriteFrame(w, hdr, resp.MarshalMUS())
}

func (s *server) writeRelease(w io.Writer, hdr switchboard.SwarmHeader, resp chromedapi.ReleaseResponse) {
	_ = switchboard.WriteFrame(w, hdr, resp.MarshalMUS())
}

func (s *server) writeStatus(w io.Writer, hdr switchboard.SwarmHeader, resp chromedapi.StatusResponse) {
	_ = switchboard.WriteFrame(w, hdr, resp.MarshalMUS())
}

func (s *server) writeError(w io.Writer, reqHdr switchboard.SwarmHeader, respType switchboard.MsgType, err error) {
	respHdr := switchboard.SwarmHeader{Version: 0, Type: respType, FromID: chromedapi.Identity, ToID: reqHdr.FromID, SeqNo: reqHdr.SeqNo}
	switch reqHdr.Type {
	case chromedapi.MsgTypeAcquire:
		s.writeAcquire(w, respHdr, chromedapi.AcquireResponse{OK: false, Error: err.Error()})
	case chromedapi.MsgTypeRelease:
		s.writeRelease(w, respHdr, chromedapi.ReleaseResponse{OK: false, Error: err.Error()})
	case chromedapi.MsgTypeStatus:
		s.writeStatus(w, respHdr, chromedapi.StatusResponse{Error: err.Error()})
	}
}

func containerRunning(ctx context.Context, containerName string) (bool, error) {
	cmd := exec.CommandContext(ctx, "lxc", "list", containerName, "--format", "json")
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}
	var entries []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &entries); err != nil {
		return false, err
	}
	if len(entries) == 0 {
		return false, nil
	}
	return strings.EqualFold(entries[0].Status, "running"), nil
}
