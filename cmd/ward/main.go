// ward is the per-agent control process deployed inside each nspawn container.
//
// Ward is the sole interface between the LLM subprocess and the outside world.
// It:
//   - Maintains a persistent MUS stdio pipe to keeperd.
//   - Spawns claude --headless (or another AgentCLI) on each incoming prompt.
//   - Intercepts capability CLI invocations from the LLM subprocess, translates
//     them into MUS CapabilityRequest frames, and injects responses as stdout.
//   - Validates tool call arguments against registered capability schemas before
//     forwarding to keeperd.
//   - Emits CompletionEvent and FailureEvent frames on subprocess exit/failure.
//   - Enforces loop detection: repeated (capability, args) pairs above threshold
//     terminate the subprocess and emit a loop_detected FailureEvent.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"

	"vivary.dev/vivary/internal/audit"
	"vivary.dev/vivary/internal/capabilities"
	"vivary.dev/vivary/internal/ctl"
	"vivary.dev/vivary/internal/switchboard"
)

const wardVersion = "0.1.0-dev"

var (
	capRegistry map[string]string
)

func main() {
	agentID := flag.String("agent-id", "", "agent identity (required)")
	loopThreshold := flag.Int("loop-threshold", 5, "repeated tool call threshold before loop_detected")
	logLevel := flag.String("log-level", "info", "log level: debug|info|warn|error")
	toolSock := flag.String("tool-sock", defaultToolSockPath, "path for capability CLI Unix socket")
	agentKDL := flag.String("agent-kdl", "/agent.kdl", "path to agent.kdl inside the container")
	flag.Parse()

	if *agentID == "" {
		fmt.Fprintln(os.Stderr, "ward: --agent-id is required")
		os.Exit(1)
	}

	// Load the generated capability registry.
	capRegistry = capabilities.GeneratedRegistry()

	log := newLogger(*logLevel)
	log.Debug("ward starting", "version", wardVersion, "agent", *agentID)

	// Build the system prompt from agent.kdl (may be empty in dev/test mode).
	sysPrompt := buildSystemPrompt(*agentKDL)
	if sysPrompt == "" {
		log.Debug("ward: no agent.kdl found or no capabilities; LLM will have no tool context", "path", *agentKDL)
	} else {
		log.Debug("ward: system prompt built", "capabilities", countCapLines(sysPrompt))
	}

	// keeperd communicates with the Ward via its stdin/stdout (the MUS pipe).
	// Ward reads frames from os.Stdin and writes frames to os.Stdout.
	pipe := &musPipe{
		r:       os.Stdin,
		w:       os.Stdout,
		agentID: *agentID,
		log:     log,
	}

	w := &ward{
		agentID:       *agentID,
		pipe:          pipe,
		loopThreshold: *loopThreshold,
		toolSockPath:  *toolSock,
		systemPrompt:  sysPrompt,
		log:           log,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the capability CLI tool server before entering the main loop.
	ts := &toolServer{sockPath: *toolSock, w: w}
	go func() {
		if err := ts.run(ctx); err != nil && ctx.Err() == nil {
			log.Error("tool server exited", "err", err)
			cancel()
		}
	}()

	if err := w.run(ctx); err != nil {
		log.Error("ward exiting with error", "err", err)
		os.Exit(1)
	}
}

// ---- MUS pipe (Ward ↔ keeperd) ---------------------------------------------

type musPipe struct {
	mu      sync.Mutex
	r       io.Reader
	w       io.Writer
	agentID string
	seq     switchboard.SeqCounter
	log     *slog.Logger
}

func (p *musPipe) send(msgType switchboard.MsgType, toID string, payload []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	hdr := switchboard.SwarmHeader{
		Version: 0,
		Type:    msgType,
		FromID:  p.agentID,
		ToID:    toID,
		SeqNo:   p.seq.Next(),
	}
	return switchboard.WriteFrame(p.w, hdr, payload)
}

// sendWithSeq sends a CapabilityRequest frame with a pre-allocated seqNo so
// that the toolserver can register the pending slot before sending.
func (p *musPipe) sendWithSeq(seqNo uint64, payload []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	hdr := switchboard.SwarmHeader{
		Version: 0,
		Type:    switchboard.MsgType_CapabilityRequest,
		FromID:  p.agentID,
		ToID:    "keeper",
		SeqNo:   seqNo,
	}
	return switchboard.WriteFrame(p.w, hdr, payload)
}

func (p *musPipe) recv() (switchboard.SwarmHeader, []byte, error) {
	return switchboard.ReadFrame(p.r)
}

// ---- Ward ------------------------------------------------------------------

type ward struct {
	agentID       string
	pipe          *musPipe
	loopThreshold int
	toolSockPath  string
	systemPrompt  string
	log           *slog.Logger

	promptSeq atomic.Uint64

	// Per-run state set during runLLMSubprocess and read by executeTool.
	// activeLoop is the loop detector for the current prompt run.
	// activeCmd is the LLM subprocess; executeTool kills it on loop abort.
	// activeAbort carries the failure kind when executeTool aborts the run.
	activeLoop  atomic.Pointer[loopDetector]
	activeCmd   atomic.Pointer[exec.Cmd]
	activeAbort atomic.Pointer[string]
}

func (w *ward) run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		hdr, payload, err := w.pipe.recv()
		if err != nil {
			if err == io.EOF {
				w.log.Info("keeperd closed pipe; ward exiting")
				return nil
			}
			return fmt.Errorf("pipe recv: %w", err)
		}

		switch hdr.Type {
		case switchboard.MsgType_CtlPrompt:
			go w.handlePrompt(ctx, payload)

		case switchboard.MsgType_CapabilityResponse:
			// Responses are handled by the in-flight subprocess via the
			// pending response map (see handlePrompt).
			w.deliverCapabilityResponse(hdr, payload)

		case switchboard.MsgType_Ping:
			_ = w.pipe.send(switchboard.MsgType_Pong, "keeper", nil)

		default:
			w.log.Warn("ward: unhandled frame type", "type", hdr.Type)
		}
	}
}

// ---- Capability response dispatch ------------------------------------------

type capResp struct {
	hdr     switchboard.SwarmHeader
	payload []byte
}

var (
	pendingMu    sync.RWMutex
	pendingBySeq = make(map[uint64]chan capResp)
)

func (w *ward) registerPending(seqNo uint64) chan capResp {
	ch := make(chan capResp, 1)
	pendingMu.Lock()
	pendingBySeq[seqNo] = ch
	pendingMu.Unlock()
	return ch
}

func (w *ward) removePending(seqNo uint64) {
	pendingMu.Lock()
	delete(pendingBySeq, seqNo)
	pendingMu.Unlock()
}

func (w *ward) deliverCapabilityResponse(hdr switchboard.SwarmHeader, payload []byte) {
	pendingMu.RLock()
	ch := pendingBySeq[hdr.SeqNo]
	pendingMu.RUnlock()
	if ch == nil {
		w.log.Warn("ward: capability response for unknown seqno", "seq", hdr.SeqNo)
		return
	}
	select {
	case ch <- capResp{hdr: hdr, payload: payload}:
	default:
	}
}

// ---- Prompt handling -------------------------------------------------------

// handlePrompt runs in its own goroutine for each incoming CtlPrompt frame.
func (w *ward) handlePrompt(ctx context.Context, payload []byte) {
	var msg ctl.PromptPayload
	if err := msg.UnmarshalMUS(bytes.NewReader(payload)); err != nil {
		w.log.Error("ward: malformed prompt payload", "err", err)
		return
	}

	w.promptSeq.Store(msg.Seq)
	w.log.Info("ward: prompt received", "seq", msg.Seq)

	outcome, ev, ferr := w.runLLMSubprocess(ctx, msg.Text)

	if ferr != nil {
		fev := audit.FailureEvent{
			AgentID: w.agentID, PromptSeq: msg.Seq,
			Kind: ev, Detail: ferr.Error(),
		}
		b, _ := audit.MarshalEvent(&fev)
		_ = w.pipe.send(switchboard.MsgType_FailureEvent, "keeper", b)
		return
	}

	cev := audit.CompletionEvent{
		AgentID:      w.agentID,
		PromptSeq:    msg.Seq,
		Model:        outcome.model,
		InputTokens:  uint32(outcome.inputTokens),
		OutputTokens: uint32(outcome.outputTokens),
		CostUSD:      outcome.costUSD,
		Outcome:      "success",
		ToolCalls:    uint32(outcome.toolCalls),
	}
	b, _ := audit.MarshalEvent(&cev)
	_ = w.pipe.send(switchboard.MsgType_CompletionEvent, "keeper", b)
}

type llmOutcome struct {
	model        string
	inputTokens  int
	outputTokens int
	costUSD      float64
	ctxPct       float64
	toolCalls    int
}

// getCapabilitySchema returns the static JSON Schema string for a named
// capability.  Ward has a local copy of all registered schemas so that capwrap
// --help works without a keeperd round-trip.
func (w *ward) getCapabilitySchema(name string) string {
	return capRegistry[name]
}

// runLLMSubprocess spawns the LLM CLI, intercepts tool calls, and returns when
// the subprocess exits.  Returns (outcome, "", nil) on success, or
// (zero, failureKind, err) on failure.
func (w *ward) runLLMSubprocess(ctx context.Context, promptText string) (llmOutcome, string, error) {
	// claude --print passes the prompt as a positional argument and runs
	// non-interactively.  WARD_TOOL_SOCK is set so capability CLIs can reach
	// the tool server.  --system-prompt injects the capability tool context so
	// the LLM knows which tools are available and how to call them.
	args := []string{"--print", promptText, "--output-format", "stream-json"}
	if w.systemPrompt != "" {
		args = append(args, "--system-prompt", w.systemPrompt)
	}
	claudePath, err := resolveClaudePath()
	if err != nil {
		return llmOutcome{}, "subprocess_crash", fmt.Errorf("resolve LLM subprocess: %w", err)
	}
	cmd := exec.CommandContext(ctx, claudePath, args...)
	cmd.Env = append(withPreferredPATH(os.Environ()), toolServerEnvKey+"="+w.toolSockPath)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return llmOutcome{}, "subprocess_crash", fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return llmOutcome{}, "subprocess_crash", fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return llmOutcome{}, "subprocess_crash", fmt.Errorf("start LLM subprocess: %w", err)
	}

	// Publish per-run state so executeTool (called from the toolServer on a
	// separate goroutine) can perform loop detection and kill the subprocess.
	loop := newLoopDetector(w.loopThreshold)
	w.activeLoop.Store(loop)
	w.activeCmd.Store(cmd)
	w.activeAbort.Store(nil)
	defer func() {
		w.activeLoop.Store(nil)
		w.activeCmd.Store(nil)
	}()

	// Drain stderr asynchronously (Claude uses it for status messages).
	go io.Copy(io.Discard, stderrPipe) //nolint:errcheck

	// Scan stream-json output for monitoring events only.
	// Capability execution happens via capwrap → toolServer → executeTool →
	// keeperd.  The tool_use events here are emitted by Claude Code after it
	// has already dispatched the tool; we read them only to count tool calls
	// and extract usage/model metadata.  Loop detection and capability
	// dispatch both live in executeTool where they fire before keeperd is hit.
	var outcome llmOutcome
	scanner := bufio.NewScanner(stdoutPipe)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var event map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			w.log.Debug("ward: non-JSON line from subprocess", "line", line)
			continue
		}

		typeVal, ok := event["type"]
		if !ok {
			continue
		}
		var evType string
		_ = json.Unmarshal(typeVal, &evType)

		switch evType {
		case "tool_use":
			outcome.toolCalls++

		case "message_stop":
			if usage, ok := event["usage"]; ok {
				var u struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				}
				_ = json.Unmarshal(usage, &u)
				outcome.inputTokens = u.InputTokens
				outcome.outputTokens = u.OutputTokens
			}
		}
	}

	if err := cmd.Wait(); err != nil {
		// Check whether executeTool aborted the run (e.g. loop_detected).
		if kind := w.activeAbort.Swap(nil); kind != nil {
			return llmOutcome{}, *kind, fmt.Errorf("prompt aborted: %s", *kind)
		}
		if ctx.Err() != nil {
			return llmOutcome{}, "timeout", ctx.Err()
		}
		return llmOutcome{}, "subprocess_crash", err
	}

	return outcome, "", nil
}

func resolveClaudePath() (string, error) {
	for _, candidate := range []string{"/usr/local/bin/claude", "/usr/bin/claude", "/bin/claude"} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	path, err := exec.LookPath("claude")
	if err != nil {
		return "", err
	}
	return path, nil
}

func withPreferredPATH(env []string) []string {
	const preferred = "/usr/local/bin:/usr/bin:/bin"
	for i, kv := range env {
		if !strings.HasPrefix(kv, "PATH=") {
			continue
		}
		pathValue := strings.TrimPrefix(kv, "PATH=")
		if pathValue == "" {
			env[i] = "PATH=" + preferred
			return env
		}
		env[i] = "PATH=" + preferred + ":" + pathValue
		return env
	}
	return append(env, "PATH="+preferred)
}

// countCapLines counts "### " section headers in a system prompt as a proxy
// for the number of capability entries, used only for the startup log line.
func countCapLines(prompt string) int {
	n := 0
	for line := range strings.SplitSeq(prompt, "\n") {
		if strings.HasPrefix(line, "### ") {
			n++
		}
	}
	return n
}

// ---- Loop detector ---------------------------------------------------------

// loopDetector tracks (toolName, argsHash) pairs and returns true when a
// combination has been seen ≥ threshold times.
type loopDetector struct {
	threshold int
	mu        sync.Mutex
	counts    map[uint64]int
}

func newLoopDetector(threshold int) *loopDetector {
	return &loopDetector{threshold: threshold, counts: make(map[uint64]int)}
}

func (l *loopDetector) check(toolName string, args json.RawMessage) bool {
	h := fnv.New64a()
	h.Write([]byte(toolName))
	h.Write(args)
	key := h.Sum64()

	l.mu.Lock()
	defer l.mu.Unlock()
	l.counts[key]++
	return l.counts[key] >= l.threshold
}

// ---- Logger ----------------------------------------------------------------

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
