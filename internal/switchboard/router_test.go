package switchboard

import (
	"bytes"
	"context"
	"log/slog"
	"io"
	"os"
	"sync"
	"testing"
	"time"
)

// testLogger returns a discard logger for test use.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// writeFrameTo serialises a MUS frame into a bytes.Buffer for use as a pipe reader.
func writeFrameTo(t *testing.T, hdr SwarmHeader, payload []byte) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	if err := WriteFrame(&buf, hdr, payload); err != nil {
		t.Fatalf("writeFrameTo: %v", err)
	}
	return &buf
}

// ---- Identity stamping -----------------------------------------------------

// TestRouterIdentityStamping verifies that a frame claiming a foreign FromID
// is silently corrected to the pipe's registered AgentID and that a security
// event is emitted.
func TestRouterIdentityStamping(t *testing.T) {
	var secEvents []SecurityEvent
	var secMu sync.Mutex
	router := NewRouter(testLogger(), func(ev SecurityEvent) {
		secMu.Lock()
		secEvents = append(secEvents, ev)
		secMu.Unlock()
	})

	// Build a frame with FromID = "impersonator" on a pipe registered as "real-agent".
	hdr := SwarmHeader{
		Version: 0, Type: MsgType_Ping,
		FromID: "impersonator", ToID: "keeper",
		SeqNo: 1,
	}
	buf := writeFrameTo(t, hdr, nil)
	buf.WriteByte(0) // sentinel EOF — causes clean readLoop exit

	var got []Frame
	var gotMu sync.Mutex
	router.RegisterHandler(func(f Frame) {
		gotMu.Lock()
		got = append(got, f)
		gotMu.Unlock()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pipe := &Pipe{
		AgentID: "real-agent",
		Reader:  buf,
		Writer:  io.Discard,
	}
	router.AddPipe(ctx, pipe)

	// Wait for the frame to be processed.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		gotMu.Lock()
		n := len(got)
		gotMu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	gotMu.Lock()
	defer gotMu.Unlock()
	if len(got) == 0 {
		t.Fatal("no frames delivered to handler")
	}
	if got[0].Header.FromID != "real-agent" {
		t.Errorf("FromID = %q, want %q", got[0].Header.FromID, "real-agent")
	}

	secMu.Lock()
	defer secMu.Unlock()
	if len(secEvents) == 0 {
		t.Error("expected identity_overwrite security event, got none")
	} else if secEvents[0].Kind != "identity_overwrite" {
		t.Errorf("security event kind = %q, want identity_overwrite", secEvents[0].Kind)
	}
}

// TestRouterIdentityCorrect verifies no security event when FromID is correct.
func TestRouterIdentityCorrect(t *testing.T) {
	var secEvents []SecurityEvent
	var secMu sync.Mutex
	router := NewRouter(testLogger(), func(ev SecurityEvent) {
		secMu.Lock()
		secEvents = append(secEvents, ev)
		secMu.Unlock()
	})

	hdr := SwarmHeader{
		Version: 0, Type: MsgType_Ping,
		FromID: "agent-x", ToID: "keeper",
		SeqNo: 1,
	}
	buf := writeFrameTo(t, hdr, nil)

	var got []Frame
	var gotMu sync.Mutex
	router.RegisterHandler(func(f Frame) {
		gotMu.Lock()
		got = append(got, f)
		gotMu.Unlock()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pipe := &Pipe{AgentID: "agent-x", Reader: buf, Writer: io.Discard}
	router.AddPipe(ctx, pipe)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		gotMu.Lock()
		n := len(got)
		gotMu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	secMu.Lock()
	defer secMu.Unlock()
	for _, ev := range secEvents {
		if ev.Kind == "identity_overwrite" {
			t.Errorf("unexpected identity_overwrite event: %+v", ev)
		}
	}
}

// ---- SeqNo monotonicity ----------------------------------------------------

// ---- Byte-rate limiter -----------------------------------------------------

func TestByteRateLimiter_AllowWithinBudget(t *testing.T) {
	l := NewByteRateLimiter(1000)
	if !l.Allow(500) {
		t.Error("500 bytes should be allowed on a fresh 1000 B/s limiter")
	}
	if !l.Allow(499) {
		t.Error("cumulative 999 bytes should stay within budget of 1000")
	}
}

func TestByteRateLimiter_ExactLimit(t *testing.T) {
	l := NewByteRateLimiter(100)
	if !l.Allow(100) {
		t.Error("exactly 100 bytes should be allowed")
	}
	if l.Allow(1) {
		t.Error("1 byte beyond the 100 B/s budget should be denied")
	}
}

func TestByteRateLimiter_WindowReset(t *testing.T) {
	l := NewByteRateLimiter(100)
	l.Allow(100) // exhaust the window
	if l.Allow(1) {
		t.Error("should be denied within the same window")
	}
	// Backdate windowStart to simulate a new second.
	l.mu.Lock()
	l.windowStart = l.windowStart.Add(-2 * time.Second)
	l.mu.Unlock()
	if !l.Allow(50) {
		t.Error("budget should be fully refreshed after window reset")
	}
}

// TestRouterPipeFlood verifies that a pipe whose first 2-byte peek exceeds the
// rate budget emits a pipe_flood security event and stops reading.
func TestRouterPipeFlood(t *testing.T) {
	var secEvents []SecurityEvent
	var secMu sync.Mutex
	router := NewRouter(testLogger(), func(ev SecurityEvent) {
		secMu.Lock()
		secEvents = append(secEvents, ev)
		secMu.Unlock()
	})

	var got []Frame
	var gotMu sync.Mutex
	router.RegisterHandler(func(f Frame) {
		gotMu.Lock()
		got = append(got, f)
		gotMu.Unlock()
	})

	hdr := SwarmHeader{
		Version: 0, Type: MsgType_Ping,
		FromID: "flood-agent", ToID: "keeper",
		SeqNo: 1,
	}
	buf := writeFrameTo(t, hdr, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// A limit of 1 byte/sec means Allow(2) for the peek will immediately fail.
	pipe := &Pipe{
		AgentID: "flood-agent",
		Reader:  buf,
		Writer:  io.Discard,
		Limiter: NewByteRateLimiter(1),
	}
	router.AddPipe(ctx, pipe)

	time.Sleep(50 * time.Millisecond)

	secMu.Lock()
	defer secMu.Unlock()
	found := false
	for _, ev := range secEvents {
		if ev.Kind == "pipe_flood" {
			found = true
		}
	}
	if !found {
		t.Error("expected pipe_flood security event, got none")
	}
	// Frame must NOT be delivered when the pipe is flooded.
	gotMu.Lock()
	defer gotMu.Unlock()
	if len(got) > 0 {
		t.Error("no frames should be delivered after pipe_flood")
	}
}

// TestRouterSeqNoMonotonicity verifies that a frame with a non-increasing SeqNo
// is dropped and a seq_no_rewind security event is emitted.
func TestRouterSeqNoMonotonicity(t *testing.T) {
	var secEvents []SecurityEvent
	var secMu sync.Mutex
	router := NewRouter(testLogger(), func(ev SecurityEvent) {
		secMu.Lock()
		secEvents = append(secEvents, ev)
		secMu.Unlock()
	})

	// Write two frames: seq=5, then seq=3 (rewind).
	var buf bytes.Buffer
	for _, seq := range []uint64{5, 3} {
		hdr := SwarmHeader{
			Version: 0, Type: MsgType_Ping,
			FromID: "agent-y", ToID: "keeper",
			SeqNo: seq,
		}
		if err := WriteFrame(&buf, hdr, nil); err != nil {
			t.Fatal(err)
		}
	}

	var got []Frame
	var gotMu sync.Mutex
	router.RegisterHandler(func(f Frame) {
		gotMu.Lock()
		got = append(got, f)
		gotMu.Unlock()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pipe := &Pipe{AgentID: "agent-y", Reader: &buf, Writer: io.Discard}
	router.AddPipe(ctx, pipe)

	// Wait until the pipe drains.
	time.Sleep(100 * time.Millisecond)

	gotMu.Lock()
	nDelivered := len(got)
	gotMu.Unlock()

	// The second frame (seq=3 rewind) must be dropped.
	if nDelivered > 1 {
		t.Errorf("expected at most 1 delivered frame, got %d", nDelivered)
	}

	secMu.Lock()
	defer secMu.Unlock()
	hasRewind := false
	for _, ev := range secEvents {
		if ev.Kind == "seq_no_rewind" {
			hasRewind = true
		}
	}
	if !hasRewind {
		t.Error("expected seq_no_rewind security event, got none")
	}
}
