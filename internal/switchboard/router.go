package switchboard

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// ---- Rate limiter ----------------------------------------------------------

// ByteRateLimiter enforces a rolling bytes-per-second limit on a single pipe.
// Frames are counted *before* MUS decoding so a flood of malformed bytes is
// caught early.
type ByteRateLimiter struct {
	mu          sync.Mutex
	windowStart time.Time
	bytesInWindow uint64
	limitPerSec   uint64
}

// NewByteRateLimiter creates a limiter with the given bytes-per-second budget.
func NewByteRateLimiter(bytesPerSec uint64) *ByteRateLimiter {
	return &ByteRateLimiter{
		windowStart: time.Now(),
		limitPerSec: bytesPerSec,
	}
}

// Allow returns true if n additional bytes are within budget for the current
// 1-second window.  The window resets after each second.
func (l *ByteRateLimiter) Allow(n uint64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.Sub(l.windowStart) >= time.Second {
		l.windowStart = now
		l.bytesInWindow = 0
	}
	l.bytesInWindow += n
	return l.bytesInWindow <= l.limitPerSec
}

// ---- Pipe ------------------------------------------------------------------

// Pipe represents one stdio connection to a Ward (or the ctl socket).
type Pipe struct {
	AgentID   string // identity stamped into FromID on all inbound frames
	Reader    *bufio.Reader
	Writer    io.Writer
	Limiter   *ByteRateLimiter
	lastSeqNo atomic.Uint64
}

// ---- Router ----------------------------------------------------------------

// Frame is a decoded MUS frame with its payload, ready to route.
type Frame struct {
	Header  SwarmHeader
	Payload []byte
}

// Handler is called by the router for every successfully decoded, validated
// frame.  It runs in the router's read goroutine; implementations must not
// block for significant time.
type Handler func(frame Frame)

// SecurityEvent is emitted to the audit log for every enforcement action.
type SecurityEvent struct {
	Time    time.Time
	AgentID string
	Kind    string // "identity_overwrite", "seq_no_rewind", "pipe_flood", "payload_too_large"
	Detail  string
}

// Router reads frames from a set of Pipes, enforces identity/SeqNo/rate rules,
// and delivers valid frames to registered handlers.
//
// MVP: designed for one agent pipe + one ctl pipe.  All fields are protected by
// internal locking; Pipes may be added/removed while the router is running.
type Router struct {
	mu       sync.RWMutex
	pipes    map[string]*Pipe  // keyed by AgentID
	handlers []Handler
	security func(SecurityEvent) // nil = discard

	log *slog.Logger
}

// NewRouter creates an idle Router.  Call Run to start processing.
func NewRouter(log *slog.Logger, securitySink func(SecurityEvent)) *Router {
	return &Router{
		pipes:    make(map[string]*Pipe),
		security: securitySink,
		log:      log,
	}
}

// RegisterHandler adds h to the set of handlers called for every valid frame.
// Must be called before Run or under external synchronisation.
func (r *Router) RegisterHandler(h Handler) {
	r.mu.Lock()
	r.handlers = append(r.handlers, h)
	r.mu.Unlock()
}

// AddPipe registers p and starts a reader goroutine for it.
// ctx cancellation causes the reader goroutine to exit cleanly.
func (r *Router) AddPipe(ctx context.Context, p *Pipe) {
	r.mu.Lock()
	r.pipes[p.AgentID] = p
	r.mu.Unlock()
	go r.readLoop(ctx, p)
}

// RemovePipe deregisters p by AgentID (does not close underlying io).
func (r *Router) RemovePipe(agentID string) {
	r.mu.Lock()
	delete(r.pipes, agentID)
	r.mu.Unlock()
}

// Send writes a frame to the pipe identified by toID.
func (r *Router) Send(toID string, hdr SwarmHeader, payload []byte) error {
	r.mu.RLock()
	p, ok := r.pipes[toID]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("router: no pipe registered for %q", toID)
	}
	return WriteFrame(p.Writer, hdr, payload)
}

// readLoop is the per-pipe goroutine.  It reads frames, enforces invariants,
// and dispatches to handlers.
func (r *Router) readLoop(ctx context.Context, p *Pipe) {
	for {
		// Respect context cancellation between frames.
		select {
		case <-ctx.Done():
			return
		default:
		}

		// --- byte-rate gate (raw bytes) ---
		// We peek exactly 2 fixed bytes first to sample the stream rate before
		// doing heap allocation for IDs.  If the limiter is exceeded at this
		// point we drop the whole connection read until the window resets.
		_, err := p.Reader.Peek(2)
		if err != nil {
			if err != io.EOF {
				r.log.Warn("pipe read error", "agent", p.AgentID, "err", err)
			}
			return
		}

		if p.Limiter != nil && !p.Limiter.Allow(2) {
			r.emit(SecurityEvent{
				Time: time.Now(), AgentID: p.AgentID,
				Kind:   "pipe_flood",
				Detail: "byte-rate limit exceeded; draining connection",
			})
			// Drain any readable data to avoid blocking the writer inside the
			// container, then exit the read loop.  keeperd will close the pipe
			// when it processes the pipe_flood event.
			go io.Copy(io.Discard, p.Reader) //nolint:errcheck
			return
		}

		hdr, payload, err := ReadFrame(p.Reader)
		if err != nil {
			if err == io.EOF {
				return // clean close
			}
			r.log.Warn("frame decode error", "agent", p.AgentID, "err", err)
			return
		}

		// --- identity enforcement ---
		if hdr.FromID != p.AgentID {
			r.emit(SecurityEvent{
				Time: time.Now(), AgentID: p.AgentID,
				Kind:   "identity_overwrite",
				Detail: fmt.Sprintf("claimed %q, overwritten to %q", hdr.FromID, p.AgentID),
			})
			hdr.FromID = p.AgentID
		}

		// --- SeqNo monotonicity ---
		for {
			prev := p.lastSeqNo.Load()
			if hdr.SeqNo <= prev && prev > 0 {
				r.emit(SecurityEvent{
					Time: time.Now(), AgentID: p.AgentID,
					Kind:   "seq_no_rewind",
					Detail: fmt.Sprintf("seq %d <= prev %d; frame dropped", hdr.SeqNo, prev),
				})
				goto nextFrame
			}
			if p.lastSeqNo.CompareAndSwap(prev, hdr.SeqNo) {
				break
			}
		}

		// --- apply byte-rate to full frame size ---
		if p.Limiter != nil {
			frameBytes := uint64(hdr.HeaderSize()) + uint64(hdr.PayloadLen)
			if !p.Limiter.Allow(frameBytes) {
				r.emit(SecurityEvent{
					Time: time.Now(), AgentID: p.AgentID,
					Kind:   "pipe_flood",
					Detail: fmt.Sprintf("frame %d bytes exceeds window budget", frameBytes),
				})
				goto nextFrame
			}
		}

		r.dispatch(Frame{Header: hdr, Payload: payload})

	nextFrame:
	}
}

func (r *Router) dispatch(f Frame) {
	r.mu.RLock()
	hs := r.handlers
	r.mu.RUnlock()
	for _, h := range hs {
		h(f)
	}
}

func (r *Router) emit(ev SecurityEvent) {
	if r.security != nil {
		r.security(ev)
	}
	r.log.Warn("security event", "kind", ev.Kind, "agent", ev.AgentID, "detail", ev.Detail)
}
