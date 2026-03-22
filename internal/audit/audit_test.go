package audit

import (
	"path/filepath"
	"testing"
	"time"
)

func TestVivaryLog_WriteAndTail(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now().UTC().Truncate(time.Second)
	for i := range 5 {
		err := db.WriteFrame(now, "CapabilityRequest", "agent-1", "keeper", uint64(i+1), []byte(`{"cap":"test"}`))
		if err != nil {
			t.Fatalf("WriteFrame %d: %v", i, err)
		}
	}

	records, err := db.Tail(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("Tail(3): want 3 records, got %d", len(records))
	}
}

func TestVivaryLog_QueryByAgent(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now().UTC()
	_ = db.WriteFrame(now, "CompletionEvent", "agent-1", "keeper", 1, nil)
	_ = db.WriteFrame(now, "CompletionEvent", "agent-2", "keeper", 2, nil)
	_ = db.WriteFrame(now, "FailureEvent", "agent-1", "keeper", 3, nil)

	records, err := db.QueryFrames(FrameFilter{AgentID: "agent-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("want 2 records for agent-1, got %d", len(records))
	}
	for _, r := range records {
		if r.FromID != "agent-1" {
			t.Errorf("unexpected from_id %q", r.FromID)
		}
	}
}

func TestVivaryLog_QueryByMsgType(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now().UTC()
	_ = db.WriteFrame(now, "CompletionEvent", "agent-1", "keeper", 1, nil)
	_ = db.WriteFrame(now, "FailureEvent", "agent-1", "keeper", 2, nil)
	_ = db.WriteFrame(now, "CompletionEvent", "agent-2", "keeper", 3, nil)

	records, err := db.QueryFrames(FrameFilter{MsgType: "CompletionEvent"})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("want 2 CompletionEvent records, got %d", len(records))
	}
}

func TestVivaryLog_DecodeBySeq(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	payload := []byte(`{"agent_id":"agent-1","outcome":"success"}`)
	now := time.Now().UTC()
	_ = db.WriteFrame(now, "CompletionEvent", "agent-1", "keeper", 42, payload)

	records, err := db.QueryFrames(FrameFilter{SeqNo: 42})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("want 1 record with seq_no=42, got %d", len(records))
	}
	if string(records[0].Payload) != string(payload) {
		t.Errorf("payload mismatch: %s", records[0].Payload)
	}
}

func TestVivaryLog_SecurityEvents(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	err = db.WriteSecurityEvent(time.Now(), "agent-1", "capability_denied", "cap=Browser_Page_Read url=http://evil.com")
	if err != nil {
		t.Fatalf("WriteSecurityEvent: %v", err)
	}
}

func TestCompletionEventRoundTrip(t *testing.T) {
	ev := CompletionEvent{
		AgentID: "a", PromptSeq: 7, Model: "claude-3-5-sonnet",
		InputTokens: 100, OutputTokens: 200, CostUSD: 0.003,
		ContextWindowUsedPct: 12.5, ToolCallsMade: 3, Outcome: "success",
	}
	b, err := MarshalEvent(ev)
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnmarshalCompletion(b)
	if err != nil {
		t.Fatal(err)
	}
	if got != ev {
		t.Fatalf("round-trip mismatch:\n  want %+v\n  got  %+v", ev, got)
	}
}
