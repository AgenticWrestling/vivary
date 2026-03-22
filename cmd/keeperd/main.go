// keeperd is the VIVARY policy daemon.
//
// It owns the control-plane Unix socket, provisions agent workspaces, routes
// MUS frames between the ctl socket and Ward stdio pipes, enforces capability
// ACLs, and writes all frames to the SQLite audit WAL.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"vivary.dev/vivary/internal/audit"
	"vivary.dev/vivary/internal/capabilities"
	"vivary.dev/vivary/internal/ctl"
	"vivary.dev/vivary/internal/switchboard"
)

const daemonVersion = "0.1.0-dev"

func main() {
	workspaceRoot := flag.String("workspace", ".", "path to the VIVARY workspace root")
	flag.Parse()

	root, err := filepath.Abs(*workspaceRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keeperd: invalid workspace path: %v\n", err)
		os.Exit(1)
	}

	cfg, err := LoadOrchestratorConfig(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keeperd: config error: %v\n", err)
		os.Exit(1)
	}

	log := newLogger(cfg.LogLevel)
	log.Info("keeperd starting", "version", daemonVersion, "workspace", root)

	auditDB, err := audit.Open(cfg.AuditDBPath)
	if err != nil {
		log.Error("failed to open audit DB", "err", err)
		os.Exit(1)
	}
	defer auditDB.Close()

	reg := capabilities.NewRegistry()
	reg.Register(&capabilities.BrowserPageRead{
		ChromeProxy: nil, // set when Chrome sidecar is ready (Phase 3.2)
	})
	reg.Register(&capabilities.FilesystemFileWrite{})

	dispatcher := capabilities.NewDispatcher(reg)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	d := &daemon{
		cfg:        cfg,
		log:        log,
		auditDB:    auditDB,
		dispatcher: dispatcher,
		agents:     make(map[string]*agentState),
		startedAt:  time.Now(),
	}

	// Start the ctl socket listener.
	if err := d.listenCtl(ctx); err != nil {
		log.Error("ctl socket error", "err", err)
		os.Exit(1)
	}

	log.Info("keeperd ready", "socket", cfg.SocketPath)
	<-ctx.Done()
	log.Info("keeperd shutting down")
}

// ---- Daemon state ----------------------------------------------------------

type agentState struct {
	id         string
	subvolPath string
	cfg        AgentConfig
	pipe       *switchboard.Pipe
}

type daemon struct {
	cfg        OrchestratorConfig
	log        *slog.Logger
	auditDB    *audit.DB
	dispatcher *capabilities.Dispatcher

	mu        sync.RWMutex
	agents    map[string]*agentState
	startedAt time.Time
}

// ---- Ctl socket ------------------------------------------------------------

func (d *daemon) listenCtl(ctx context.Context) error {
	// Remove stale socket file from a prior run.
	_ = os.Remove(d.cfg.SocketPath)

	ln, err := net.Listen("unix", d.cfg.SocketPath)
	if err != nil {
		return fmt.Errorf("listen %q: %w", d.cfg.SocketPath, err)
	}

	// Restrict socket permissions: only the daemon owner can connect.
	if err := os.Chmod(d.cfg.SocketPath, 0o600); err != nil {
		ln.Close()
		return fmt.Errorf("chmod socket: %w", err)
	}

	go func() {
		defer ln.Close()
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-ctx.Done():
					return
				default:
					d.log.Warn("ctl accept error", "err", err)
					continue
				}
			}
			go d.handleCtlConn(ctx, conn)
		}
	}()

	// Close listener when context is cancelled.
	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	return nil
}

// handleCtlConn services one ctl connection (one vivary CLI/TUI process).
// The connection exchanges MUS frames; the ctl identity is enforced here.
func (d *daemon) handleCtlConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	d.log.Debug("ctl connection accepted")

	var seqOut switchboard.SeqCounter

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		hdr, payload, err := switchboard.ReadFrame(conn)
		if err != nil {
			if err != switchboard.ErrUnexpectedEOF {
				d.log.Debug("ctl connection closed", "err", err)
			}
			return
		}

		// Enforce ctl identity.
		if hdr.FromID != ctl.CtlIdentity {
			d.log.Warn("ctl: non-ctl FromID rejected", "from", hdr.FromID)
			return
		}

		// Audit the ctl frame (ctl frames always store payload — no sensitive data).
		_ = d.auditDB.WriteFrame(time.Now(), hdr.Type.String(), hdr.FromID, hdr.ToID, hdr.SeqNo, payload)

		resp, respPayload := d.dispatchCtl(ctx, hdr, payload)
		if resp.Type == 0 {
			continue // no response needed
		}
		resp.FromID = "keeper"
		resp.ToID = ctl.CtlIdentity
		resp.SeqNo = seqOut.Next()

		if err := switchboard.WriteFrame(conn, resp, respPayload); err != nil {
			d.log.Warn("ctl write error", "err", err)
			return
		}
	}
}

// dispatchCtl routes a ctl-sourced frame to the appropriate handler and returns
// the response header + payload (zero-value header if no response is needed).
func (d *daemon) dispatchCtl(ctx context.Context, hdr switchboard.SwarmHeader, payload []byte) (switchboard.SwarmHeader, []byte) {
	resp := switchboard.SwarmHeader{Version: 0}

	switch hdr.Type {
	case switchboard.MsgType_CtlStatus:
		resp.Type = switchboard.MsgType_CtlStatus
		return resp, d.buildStatus()

	case switchboard.MsgType_CtlAgentList:
		resp.Type = switchboard.MsgType_CtlAgentList
		return resp, d.buildAgentList()

	case switchboard.MsgType_CtlAgentCreate:
		err := d.agentCreate(ctx, payload)
		resp.Type = switchboard.MsgType_CtlAgentCreate
		if err != nil {
			return resp, errorPayload(err)
		}
		return resp, []byte(`{"ok":true}`)

	case switchboard.MsgType_CtlAgentDestroy:
		err := d.agentDestroy(payload)
		resp.Type = switchboard.MsgType_CtlAgentDestroy
		if err != nil {
			return resp, errorPayload(err)
		}
		return resp, []byte(`{"ok":true}`)

	case switchboard.MsgType_CtlPrompt:
		err := d.dispatchPrompt(payload)
		resp.Type = switchboard.MsgType_CtlPrompt
		if err != nil {
			return resp, errorPayload(err)
		}
		return resp, []byte(`{"ok":true}`)

	case switchboard.MsgType_Ping:
		resp.Type = switchboard.MsgType_Pong
		return resp, nil

	default:
		d.log.Warn("ctl: unhandled msg type", "type", hdr.Type)
		return switchboard.SwarmHeader{}, nil
	}
}

// ---- Agent lifecycle -------------------------------------------------------

func (d *daemon) agentCreate(_ context.Context, payload []byte) error {
	// TODO Phase 1.4: parse CtlAgentCreatePayload, create Btrfs subvolume,
	// write agent.kdl, configure cgroups, spawn nspawn, open Ward stdio pipe.
	// For MVP skeleton: just register a stub agent state.
	var req ctl.AgentCreatePayload
	if err := jsonUnmarshal(payload, &req); err != nil {
		return fmt.Errorf("invalid AgentCreatePayload: %w", err)
	}
	if req.ID == "" {
		return fmt.Errorf("agent ID is required")
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.agents[req.ID]; exists {
		return fmt.Errorf("agent %q already exists", req.ID)
	}
	d.agents[req.ID] = &agentState{id: req.ID, subvolPath: req.Template}
	d.log.Info("agent created (stub)", "id", req.ID)
	return nil
}

func (d *daemon) agentDestroy(payload []byte) error {
	var req ctl.AgentDestroyPayload
	if err := jsonUnmarshal(payload, &req); err != nil {
		return fmt.Errorf("invalid AgentDestroyPayload: %w", err)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.agents[req.ID]; !exists {
		return fmt.Errorf("agent %q not found", req.ID)
	}
	delete(d.agents, req.ID)
	d.dispatcher.RemoveACL(req.ID)
	d.log.Info("agent destroyed", "id", req.ID)
	return nil
}

func (d *daemon) dispatchPrompt(payload []byte) error {
	var req ctl.PromptPayload
	if err := jsonUnmarshal(payload, &req); err != nil {
		return fmt.Errorf("invalid PromptPayload: %w", err)
	}

	d.mu.RLock()
	agent, ok := d.agents[req.AgentID]
	d.mu.RUnlock()
	if !ok {
		return fmt.Errorf("agent %q not found", req.AgentID)
	}
	if agent.pipe == nil {
		return fmt.Errorf("agent %q has no active pipe (not yet spawned)", req.AgentID)
	}

	// TODO: forward prompt to Ward via agent.pipe.
	d.log.Info("prompt dispatched (stub)", "agent", req.AgentID, "seq", req.Seq)
	return nil
}

// ---- Status / list ---------------------------------------------------------

func (d *daemon) buildStatus() []byte {
	d.mu.RLock()
	agents := d.buildAgentStatusList()
	d.mu.RUnlock()

	p := ctl.StatusPayload{
		DaemonVersion: daemonVersion,
		Agents:        agents,
		UptimeSeconds: int64(time.Since(d.startedAt).Seconds()),
	}
	return ctl.MarshalJSON(p)
}

func (d *daemon) buildAgentList() []byte {
	d.mu.RLock()
	agents := d.buildAgentStatusList()
	d.mu.RUnlock()
	return ctl.MarshalJSON(ctl.AgentListPayload{Agents: agents})
}

func (d *daemon) buildAgentStatusList() []ctl.AgentStatus {
	statuses := make([]ctl.AgentStatus, 0, len(d.agents))
	for _, a := range d.agents {
		state := "idle"
		if a.pipe != nil {
			state = "running"
		}
		statuses = append(statuses, ctl.AgentStatus{
			ID:    a.id,
			State: state,
		})
	}
	return statuses
}

// ---- Helpers ---------------------------------------------------------------

func errorPayload(err error) []byte {
	return []byte(fmt.Sprintf(`{"ok":false,"error":%q}`, err.Error()))
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
