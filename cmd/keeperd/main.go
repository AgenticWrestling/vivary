// keeperd is the VIVARY policy daemon.
//
// It owns the control-plane Unix socket, provisions agent workspaces, routes
// MUS frames between the ctl socket and Ward stdio pipes, enforces capability
// ACLs, and writes all frames to the SQLite audit WAL.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"vivary.dev/vivary/internal/audit"
	"vivary.dev/vivary/internal/capabilities"
	"vivary.dev/vivary/internal/chromproxy"
	"vivary.dev/vivary/internal/ctl"
	agentruntime "vivary.dev/vivary/internal/runtime"
	"vivary.dev/vivary/internal/switchboard"
	"vivary.dev/vivary/pkg/mus"
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

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Launch headless Chrome sidecar (non-fatal if unavailable).
	stopChrome, chromeErr := launchChrome(ctx, cfg, log)
	if chromeErr != nil {
		log.Warn("chrome sidecar unavailable; Browser_Page_Read will fail", "err", chromeErr)
	}
	defer stopChrome()

	chromeProxy := chromproxy.New(cfg.ChromeRemoteDebugAddr)

	reg := capabilities.NewRegistry()
	reg.Register(&capabilities.BrowserPageRead{
		ChromeProxy: chromeProxy.ReadPage,
	})
	reg.Register(&capabilities.FilesystemFileWrite{})

	dispatcher := capabilities.NewDispatcher(reg)

	router := switchboard.NewRouter(log, func(ev switchboard.SecurityEvent) {
		_ = auditDB.WriteSecurityEvent(ev.Time, ev.AgentID, ev.Kind, ev.Detail)
	})

	var rt agentruntime.ContainerRuntime
	if os.Getenv("VIVARY_STUB_RUNTIME") != "" || runtime.GOOS != "linux" {
		rt = &agentruntime.StubRuntime{}
	} else {
		rt = &agentruntime.LinuxRuntime{
			NspawnRootBase:    "/var/lib/vivary/agents",
			WardBinaryPath:    "/usr/lib/vivary/ward",
			CapwrapBinaryPath: "/usr/lib/vivary/capwrap",
		}
	}

	d := &daemon{
		ctx:        ctx,
		cfg:        cfg,
		log:        log,
		auditDB:    auditDB,
		dispatcher: dispatcher,
		router:     router,
		runtime:    rt,
		agents:     make(map[string]*agentState),
		startedAt:  time.Now(),
	}
	router.RegisterHandler(d.handleFrame)

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

	// Runtime state — updated on each CompletionEvent or FailureEvent from Ward.
	// Protected by daemon.mu.
	lastPromptSeq uint64
	lastEventAt   time.Time
	lastOutcome   string // "success", failure kind, etc.
	inputTokens   uint32
	outputTokens  uint32
	costUSD       float64
	toolCalls     uint32
}

type daemon struct {
	ctx        context.Context // cancelled on SIGINT/SIGTERM
	cfg        OrchestratorConfig
	log        *slog.Logger
	auditDB    *audit.DB
	dispatcher *capabilities.Dispatcher
	router     *switchboard.Router
	runtime    agentruntime.ContainerRuntime
	seqOut     switchboard.SeqCounter

	mu        sync.RWMutex
	agents    map[string]*agentState
	startedAt time.Time

	subsMu  sync.RWMutex
	ctlSubs []chan switchboard.Frame // push channels for CtlSubscribe connections
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
// If the client sends CtlSubscribe, keeperd will push CompletionEvent and
// FailureEvent frames to it for the lifetime of the connection.
func (d *daemon) handleCtlConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	d.log.Debug("ctl connection accepted")

	var seqOut switchboard.SeqCounter

	// pushCh receives event frames to forward to this subscriber.
	// It is registered on CtlSubscribe and deregistered on disconnect.
	pushCh := make(chan switchboard.Frame, 64)
	var subscribed bool

	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()

	// Push goroutine: forwards queued events to the ctl connection.
	go func() {
		for {
			select {
			case <-connCtx.Done():
				return
			case f := <-pushCh:
				hdr := switchboard.SwarmHeader{
					Version: 0, Type: f.Header.Type,
					FromID: "keeper", ToID: ctl.CtlIdentity,
					SeqNo: seqOut.Next(),
				}
				if err := switchboard.WriteFrame(conn, hdr, f.Payload); err != nil {
					connCancel()
					return
				}
			}
		}
	}()

	for {
		select {
		case <-connCtx.Done():
			return
		default:
		}

		hdr, payload, err := switchboard.ReadFrame(conn)
		if err != nil {
			return
		}

		// Enforce ctl identity.
		if hdr.FromID != ctl.CtlIdentity {
			d.log.Warn("ctl: non-ctl FromID rejected", "from", hdr.FromID)
			return
		}

		_ = d.auditDB.WriteFrame(time.Now(), hdr.Type.String(), hdr.FromID, hdr.ToID, hdr.SeqNo, payload)

		// CtlSubscribe is handled here rather than in dispatchCtl because it
		// needs access to pushCh and the subscription list.
		if hdr.Type == switchboard.MsgType_CtlSubscribe && !subscribed {
			subscribed = true
			d.subsMu.Lock()
			d.ctlSubs = append(d.ctlSubs, pushCh)
			d.subsMu.Unlock()
			// Deregister on disconnect.
			defer func() {
				d.subsMu.Lock()
				for i, ch := range d.ctlSubs {
					if ch == pushCh {
						d.ctlSubs = append(d.ctlSubs[:i], d.ctlSubs[i+1:]...)
						break
					}
				}
				d.subsMu.Unlock()
			}()
			// Ack the subscribe.
			ack := switchboard.SwarmHeader{
				Version: 0, Type: switchboard.MsgType_CtlSubscribe,
				FromID: "keeper", ToID: ctl.CtlIdentity, SeqNo: seqOut.Next(),
			}
			_ = switchboard.WriteFrame(conn, ack, []byte{1}) // 1 = ok
			continue
		}

		resp, respPayload := d.dispatchCtl(ctx, hdr, payload)
		if resp.Type == 0 {
			continue
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
	r := bytes.NewReader(payload)

	switch hdr.Type {
	case switchboard.MsgType_CtlStatus:
		resp.Type = switchboard.MsgType_CtlStatus
		return resp, d.buildStatus()

	case switchboard.MsgType_CtlAgentList:
		resp.Type = switchboard.MsgType_CtlAgentList
		return resp, d.buildAgentList()

	case switchboard.MsgType_CtlAgentCreate:
		var req ctl.AgentCreatePayload
		if err := req.UnmarshalMUS(r); err != nil {
			return resp, errorPayload(err)
		}
		err := d.agentCreate(ctx, req)
		resp.Type = switchboard.MsgType_CtlAgentCreate
		if err != nil {
			return resp, errorPayload(err)
		}
		return resp, []byte{1}

	case switchboard.MsgType_CtlAgentDestroy:
		var req ctl.AgentDestroyPayload
		if err := req.UnmarshalMUS(r); err != nil {
			return resp, errorPayload(err)
		}
		err := d.agentDestroy(req)
		resp.Type = switchboard.MsgType_CtlAgentDestroy
		if err != nil {
			return resp, errorPayload(err)
		}
		return resp, []byte{1}

	case switchboard.MsgType_CtlPrompt:
		err := d.dispatchPrompt(payload)
		resp.Type = switchboard.MsgType_CtlPrompt
		if err != nil {
			return resp, errorPayload(err)
		}
		return resp, []byte{1}

	case switchboard.MsgType_Ping:
		resp.Type = switchboard.MsgType_Pong
		return resp, nil

	case switchboard.MsgType_CtlApproval:
		resp.Type = switchboard.MsgType_CtlApproval
		return resp, errorPayload(fmt.Errorf("approval gate not yet implemented (Phase 2)"))

	default:
		d.log.Warn("ctl: unhandled msg type", "type", hdr.Type)
		return switchboard.SwarmHeader{}, nil
	}
}

// ---- Agent lifecycle (see provisioning.go for agentCreate/agentDestroy) ----

func (d *daemon) dispatchPrompt(payload []byte) error {
	var req ctl.PromptPayload
	if err := req.UnmarshalMUS(bytes.NewReader(payload)); err != nil {
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

	// Record the prompt seq in agent state before forwarding.
	d.mu.Lock()
	if a, ok := d.agents[req.AgentID]; ok {
		a.lastPromptSeq = req.Seq
	}
	d.mu.Unlock()

	// Forward the prompt to Ward as a CtlPrompt MUS frame.
	hdr := switchboard.SwarmHeader{
		Version: 0, Type: switchboard.MsgType_CtlPrompt,
		FromID: "keeper", ToID: req.AgentID,
		SeqNo: d.seqOut.Next(),
	}
	if err := d.router.Send(req.AgentID, hdr, payload); err != nil {
		return fmt.Errorf("forward prompt to agent %q: %w", req.AgentID, err)
	}
	d.log.Info("prompt forwarded to ward", "agent", req.AgentID, "seq", req.Seq)
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
	return p.MarshalMUS()
}

func (d *daemon) buildAgentList() []byte {
	d.mu.RLock()
	agents := d.buildAgentStatusList()
	d.mu.RUnlock()
	p := ctl.AgentListPayload{Agents: agents}
	return p.MarshalMUS()
}

func (d *daemon) buildAgentStatusList() []ctl.AgentStatus {
	statuses := make([]ctl.AgentStatus, 0, len(d.agents))
	for _, a := range d.agents {
		state := "idle"
		if a.pipe != nil {
			state = "running"
		}
		var lastEventAt, costUSD string
		if !a.lastEventAt.IsZero() {
			lastEventAt = a.lastEventAt.UTC().Format(time.RFC3339)
		}
		if a.costUSD != 0 {
			costUSD = fmt.Sprintf("%f", a.costUSD)
		}
		statuses = append(statuses, ctl.AgentStatus{
			ID:            a.id,
			State:         state,
			LastPromptSeq: a.lastPromptSeq,
			LastEventAt:   lastEventAt,
			LastOutcome:   a.lastOutcome,
			InputTokens:   a.inputTokens,
			OutputTokens:  a.outputTokens,
			CostUSD:       costUSD,
			ToolCalls:     a.toolCalls,
		})
	}
	return statuses
}

// ---- Helpers ---------------------------------------------------------------

func errorPayload(err error) []byte {
	var b []byte
	b = append(b, 0) // 0 = not ok
	b = mus.AppendString(b, err.Error())
	return b
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
