package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"vivary.dev/vivary/internal/audit"
	"vivary.dev/vivary/internal/ctl"
	"vivary.dev/vivary/internal/switchboard"
	"vivary.dev/vivary/pkg/mus"
)

type fakeTransport struct {
	msgs        chan tea.Msg
	subscribed  bool
	statusAsked bool
	pinged      bool
	promptAgent string
	promptText  string
	promptSeq   uint64
	closed      bool
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{msgs: make(chan tea.Msg, 8)}
}

func (f *fakeTransport) Messages() <-chan tea.Msg { return f.msgs }
func (f *fakeTransport) Subscribe() error         { f.subscribed = true; return nil }
func (f *fakeTransport) RequestStatus() error     { f.statusAsked = true; return nil }
func (f *fakeTransport) Ping() error              { f.pinged = true; return nil }
func (f *fakeTransport) Close() error             { f.closed = true; close(f.msgs); return nil }
func (f *fakeTransport) SendPrompt(agentID, text string, seq uint64) error {
	f.promptAgent = agentID
	f.promptText = text
	f.promptSeq = seq
	return nil
}

func TestTUIStatusAndCompletionView(t *testing.T) {
	transport := newFakeTransport()
	model := newTUIModel("./keeper.sock", transport)

	updated, _ := model.Update(tuiStatusMsg{Status: ctl.StatusPayload{
		DaemonVersion: "dev",
		UptimeSeconds: 12,
		Agents:        []ctl.AgentStatus{{ID: "agent-1", State: "running", LastPromptSeq: 7}},
	}})
	m := updated.(tuiModel)

	updated, _ = m.Update(tuiCompletionMsg{Event: audit.CompletionEvent{
		AgentID:      "agent-1",
		PromptSeq:    7,
		InputTokens:  13,
		OutputTokens: 21,
		CostUSD:      0.0042,
		Outcome:      "success",
	}})
	m = updated.(tuiModel)
	view := m.View()

	assertContains(t, view, "agent:       agent-1")
	assertContains(t, view, "outcome:     success")
	assertContains(t, view, "last cost:   $0.0042")
	assertContains(t, view, "last tokens: in=13 out=21")
	assertContains(t, view, "completion: success")
}

func TestTUIStatusView_UsesKeeperOwnedRuntimeFields(t *testing.T) {
	transport := newFakeTransport()
	model := newTUIModel("./keeper.sock", transport)

	updated, _ := model.Update(tuiStatusMsg{Status: ctl.StatusPayload{
		DaemonVersion: "dev",
		UptimeSeconds: 12,
		Agents: []ctl.AgentStatus{{
			ID:            "agent-1",
			State:         "running",
			LastPromptSeq: 23,
			LastEventAt:   "2026-03-24T12:34:56Z",
			LastOutcome:   "success",
			InputTokens:   120,
			OutputTokens:  55,
			CostUSD:       "0.002500",
			ToolCalls:     4,
		}},
	}})
	m := updated.(tuiModel)
	view := m.View()

	assertContains(t, view, "prompt seq:  23")
	assertContains(t, view, "last event:  2026-03-24T12:34:56Z")
	assertContains(t, view, "outcome:     success")
	assertContains(t, view, "last cost:   $0.002500")
	assertContains(t, view, "last tokens: in=120 out=55")
	assertContains(t, view, "tool calls:  4")
}

func TestTUIPromptEntryAndSend(t *testing.T) {
	transport := newFakeTransport()
	model := newTUIModel("./keeper.sock", transport)
	model.agents = []ctl.AgentStatus{{ID: "agent-1", State: "idle"}}

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m := updated.(tuiModel)
	if !m.promptMode {
		t.Fatalf("expected prompt mode")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m = updated.(tuiModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	m = updated.(tuiModel)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	if cmd == nil {
		t.Fatalf("expected send command")
	}
	msg := cmd()
	if msg != nil {
		if _, ok := msg.(tuiErrMsg); ok {
			t.Fatalf("unexpected send error: %#v", msg)
		}
	}
	if transport.promptAgent != "agent-1" || transport.promptText != "hi" || transport.promptSeq == 0 {
		t.Fatalf("unexpected prompt dispatch: agent=%q text=%q seq=%d", transport.promptAgent, transport.promptText, transport.promptSeq)
	}
}

func TestTUIFailureUpdatesSummary(t *testing.T) {
	transport := newFakeTransport()
	model := newTUIModel("./keeper.sock", transport)
	model.agents = []ctl.AgentStatus{{ID: "agent-1", State: "running"}}
	model.syncSnapshots()

	updated, _ := model.Update(tuiFailureMsg{Event: audit.FailureEvent{
		AgentID:   "agent-1",
		PromptSeq: 99,
		Kind:      "capability_denied",
		Detail:    "Browser_Page_Read denied",
	}})
	m := updated.(tuiModel)
	view := m.View()

	assertContains(t, view, "prompt seq:  99")
	assertContains(t, view, "capability_denied: Browser_Page_Read denied")
	assertContains(t, view, "last cost:   n/a")
}

func TestDecodeTUIFrame(t *testing.T) {
	payload, err := audit.MarshalEvent(&audit.CompletionEvent{AgentID: "agent-1", PromptSeq: 3, Outcome: "success"})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	msg := decodeTUIFrame(frameHeader(switchboard.MsgType_CompletionEvent), payload)
	completion, ok := msg.(tuiCompletionMsg)
	if !ok {
		t.Fatalf("expected completion msg, got %#v", msg)
	}
	if completion.Event.AgentID != "agent-1" || completion.Event.PromptSeq != 3 {
		t.Fatalf("unexpected completion event: %#v", completion.Event)
	}
}

func TestDecodeTUIFrame_StatusUsesMUS(t *testing.T) {
	status := ctl.StatusPayload{
		DaemonVersion: "dev",
		UptimeSeconds: 12,
		Agents:        []ctl.AgentStatus{{ID: "agent-1", LastOutcome: "success", ToolCalls: 2}},
	}
	payload := status.MarshalMUS()
	msg := decodeTUIFrame(frameHeader(switchboard.MsgType_CtlStatus), payload)
	statusMsg, ok := msg.(tuiStatusMsg)
	if !ok {
		t.Fatalf("expected status msg, got %#v", msg)
	}
	if statusMsg.Status.DaemonVersion != "dev" || len(statusMsg.Status.Agents) != 1 || statusMsg.Status.Agents[0].ToolCalls != 2 {
		t.Fatalf("unexpected status payload: %#v", statusMsg.Status)
	}
}

func TestDecodeTUIFrame_AckUsesMUS(t *testing.T) {
	msg := decodeTUIFrame(frameHeader(switchboard.MsgType_CtlSubscribe), []byte{1})
	ack, ok := msg.(tuiSubscribeAckMsg)
	if !ok {
		t.Fatalf("expected subscribe ack, got %#v", msg)
	}
	if !ack.OK {
		t.Fatal("expected successful ack")
	}

	errPayload := append([]byte{0}, mus.AppendString(nil, "prompt denied")...)
	msg = decodeTUIFrame(frameHeader(switchboard.MsgType_CtlPrompt), errPayload)
	promptAck, ok := msg.(tuiPromptAckMsg)
	if !ok {
		t.Fatalf("expected prompt ack, got %#v", msg)
	}
	if promptAck.OK || promptAck.Error != "prompt denied" {
		t.Fatalf("unexpected prompt ack: %#v", promptAck)
	}
}

func frameHeader(tpe switchboard.MsgType) switchboard.SwarmHeader {
	return switchboard.SwarmHeader{Version: 0, Type: tpe, FromID: "keeper", ToID: ctl.CtlIdentity, SeqNo: 1}
}

func assertContains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("expected view to contain %q\nview:\n%s", want, got)
	}
}
