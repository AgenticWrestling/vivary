package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"vivary.dev/vivary/internal/audit"
	"vivary.dev/vivary/internal/ctl"
	"vivary.dev/vivary/internal/switchboard"
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
	assertContains(t, view, "last cost:   $0.0042")
	assertContains(t, view, "last tokens: in=13 out=21")
	assertContains(t, view, "completion: success")
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

func frameHeader(tpe switchboard.MsgType) switchboard.SwarmHeader {
	return switchboard.SwarmHeader{Version: 0, Type: tpe, FromID: "keeper", ToID: ctl.CtlIdentity, SeqNo: 1}
}

func assertContains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("expected view to contain %q\nview:\n%s", want, got)
	}
}
