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
