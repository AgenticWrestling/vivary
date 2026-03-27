package main

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"vivary.dev/vivary/internal/audit"
	"vivary.dev/vivary/internal/ctl"
	"vivary.dev/vivary/internal/switchboard"
	"vivary.dev/vivary/pkg/mus"
)

type tuiTransport interface {
	Messages() <-chan tea.Msg
	Subscribe() error
	RequestStatus() error
	SendPrompt(agentID, text string, seq uint64) error
	Ping() error
	Close() error
}

type tuiStatusMsg struct{ Status ctl.StatusPayload }
type tuiSubscribeAckMsg struct {
	OK    bool
	Error string
}
type tuiPromptAckMsg struct {
	OK    bool
	Error string
}
type tuiPongMsg struct{ At time.Time }
type tuiCompletionMsg struct{ Event audit.CompletionEvent }
type tuiFailureMsg struct{ Event audit.FailureEvent }
type tuiErrMsg struct{ Err error }
type tuiTickMsg time.Time

type liveTransport struct {
	conn   netConn
	msgs   chan tea.Msg
	sendMu sync.Mutex
	seq    switchboard.SeqCounter
	close  sync.Once
}

type netConn interface {
	Read(b []byte) (n int, err error)
	Write(b []byte) (n int, err error)
	Close() error
}

func newLiveTransport(socketPath string) (*liveTransport, error) {
	conn, err := dialKeeper(socketPath)
	if err != nil {
		return nil, err
	}
	t := &liveTransport{
		conn: conn,
		msgs: make(chan tea.Msg, 64),
	}
	go t.readLoop()
	return t, nil
}

func (t *liveTransport) Messages() <-chan tea.Msg { return t.msgs }

func (t *liveTransport) Close() error {
	var err error
	t.close.Do(func() {
		err = t.conn.Close()
	})
	return err
}

func (t *liveTransport) Subscribe() error {
	return t.send(switchboard.MsgType_CtlSubscribe, nil)
}

func (t *liveTransport) RequestStatus() error {
	return t.send(switchboard.MsgType_CtlStatus, nil)
}

func (t *liveTransport) SendPrompt(agentID, text string, seq uint64) error {
	payload := ctl.PromptPayload{AgentID: agentID, Seq: seq, Text: text}
	return t.send(switchboard.MsgType_CtlPrompt, payload.MarshalMUS())
}

func (t *liveTransport) Ping() error {
	return t.send(switchboard.MsgType_Ping, nil)
}

func (t *liveTransport) send(msgType switchboard.MsgType, payload []byte) error {
	t.sendMu.Lock()
	defer t.sendMu.Unlock()
	hdr := switchboard.SwarmHeader{
		Version: 0,
		Type:    msgType,
		FromID:  ctl.CtlIdentity,
		ToID:    "keeper",
		SeqNo:   t.seq.Next(),
	}
	return switchboard.WriteFrame(t.conn, hdr, payload)
}

func (t *liveTransport) readLoop() {
	defer close(t.msgs)
	for {
		hdr, payload, err := switchboard.ReadFrame(t.conn)
		if err != nil {
			t.msgs <- tuiErrMsg{Err: err}
			return
		}
		t.msgs <- decodeTUIFrame(hdr, payload)
	}
}

func decodeTUIFrame(hdr switchboard.SwarmHeader, payload []byte) tea.Msg {
	switch hdr.Type {
	case switchboard.MsgType_CtlSubscribe:
		ack, err := decodeAckPayload(payload)
		if err != nil {
			return tuiErrMsg{Err: fmt.Errorf("decode subscribe ack: %w", err)}
		}
		return tuiSubscribeAckMsg(ack)
	case switchboard.MsgType_CtlStatus:
		var status ctl.StatusPayload
		if err := status.UnmarshalMUS(bytes.NewReader(payload)); err != nil {
			return tuiErrMsg{Err: fmt.Errorf("decode status: %w", err)}
		}
		return tuiStatusMsg{Status: status}
	case switchboard.MsgType_CtlPrompt:
		ack, err := decodeAckPayload(payload)
		if err != nil {
			return tuiErrMsg{Err: fmt.Errorf("decode prompt ack: %w", err)}
		}
		return tuiPromptAckMsg(ack)
	case switchboard.MsgType_Pong:
		return tuiPongMsg{At: time.Now()}
	case switchboard.MsgType_CompletionEvent:
		ev, err := audit.UnmarshalCompletion(payload)
		if err != nil {
			return tuiErrMsg{Err: fmt.Errorf("decode completion event: %w", err)}
		}
		return tuiCompletionMsg{Event: ev}
	case switchboard.MsgType_FailureEvent:
		ev, err := audit.UnmarshalFailure(payload)
		if err != nil {
			return tuiErrMsg{Err: fmt.Errorf("decode failure event: %w", err)}
		}
		return tuiFailureMsg{Event: ev}
	default:
		return tuiErrMsg{Err: fmt.Errorf("unexpected frame type: %s", hdr.Type)}
	}
}

type agentSnapshot struct {
	Status         ctl.AgentStatus
	LastCompletion *audit.CompletionEvent
	LastFailure    *audit.FailureEvent
}

type tuiModel struct {
	transport      tuiTransport
	socketPath     string
	width          int
	height         int
	daemonVersion  string
	uptimeSeconds  int64
	uptimeFetched  time.Time
	agents         []ctl.AgentStatus
	agentMetrics   map[string]*agentSnapshot
	selected       int
	connected      bool
	promptMode     bool
	promptInput    string
	flash          string
	lastPing       string
	lastFrameError string
	quitting       bool
}

func newTUIModel(socketPath string, transport tuiTransport) tuiModel {
	return tuiModel{
		transport:    transport,
		socketPath:   socketPath,
		agentMetrics: make(map[string]*agentSnapshot),
	}
}

func runTUI(socketPath string) {
	transport, err := newLiveTransport(socketPath)
	if err != nil {
		fatal("cannot connect to keeperd at %q: %v", socketPath, err)
	}
	defer transport.Close()
	p := tea.NewProgram(newTUIModel(socketPath, transport), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fatal("tui: %v", err)
	}
}

func (m tuiModel) Init() tea.Cmd {
	cmds := []tea.Cmd{
		m.sendCmd(m.transport.Subscribe),
		m.sendCmd(m.transport.RequestStatus),
		m.waitForMessage(),
		tickCmd(),
	}
	return tea.Batch(cmds...)
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tuiTickMsg:
		return m, tickCmd()
	case tuiErrMsg:
		m.connected = false
		m.lastFrameError = msg.Err.Error()
		m.flash = "ctl connection lost"
		return m, nil
	case tuiSubscribeAckMsg:
		m.connected = msg.OK
		if msg.OK {
			m.flash = "live event stream connected"
		} else {
			m.flash = "subscribe failed: " + msg.Error
		}
		return m, m.waitForMessage()
	case tuiStatusMsg:
		m.connected = true
		m.daemonVersion = msg.Status.DaemonVersion
		m.uptimeSeconds = msg.Status.UptimeSeconds
		m.uptimeFetched = time.Now()
		m.agents = msg.Status.Agents
		m.syncSnapshots()
		m.flash = fmt.Sprintf("status refreshed: %d agent(s)", len(msg.Status.Agents))
		return m, m.waitForMessage()
	case tuiCompletionMsg:
		m.connected = true
		snap := m.ensureSnapshot(msg.Event.AgentID)
		snap.LastCompletion = &msg.Event
		snap.LastFailure = nil
		snap.Status.ID = msg.Event.AgentID
		snap.Status.LastPromptSeq = msg.Event.PromptSeq
		snap.Status.LastEventAt = time.Now().Format(time.RFC3339)
		snap.Status.LastOutcome = msg.Event.Outcome
		snap.Status.Model = msg.Event.Model
		snap.Status.InputTokens = msg.Event.InputTokens
		snap.Status.OutputTokens = msg.Event.OutputTokens
		snap.Status.CostUSD = fmt.Sprintf("%.6f", msg.Event.CostUSD)
		snap.Status.ToolCalls = msg.Event.ToolCalls
		snap.Status.LastFailureDetail = ""
		m.flash = fmt.Sprintf("completion: %s seq %d", msg.Event.AgentID, msg.Event.PromptSeq)
		return m, m.waitForMessage()
	case tuiFailureMsg:
		m.connected = true
		snap := m.ensureSnapshot(msg.Event.AgentID)
		snap.LastFailure = &msg.Event
		snap.Status.ID = msg.Event.AgentID
		snap.Status.LastPromptSeq = msg.Event.PromptSeq
		snap.Status.LastEventAt = time.Now().Format(time.RFC3339)
		snap.Status.LastOutcome = msg.Event.Kind
		snap.Status.LastFailureDetail = msg.Event.Detail
		m.flash = fmt.Sprintf("failure: %s %s", msg.Event.AgentID, msg.Event.Kind)
		return m, m.waitForMessage()
	case tuiPromptAckMsg:
		if msg.OK {
			m.flash = "prompt queued"
			m.promptInput = ""
			m.promptMode = false
		} else {
			m.flash = "prompt rejected: " + msg.Error
		}
		return m, m.waitForMessage()
	case tuiPongMsg:
		m.lastPing = msg.At.Format(time.RFC3339)
		m.flash = "keeperd pong received"
		return m, m.waitForMessage()
	default:
		return m, nil
	}
}

func (m tuiModel) View() string {
	var b strings.Builder
	b.WriteString("VIVARY\n")
	b.WriteString("single-agent detail view\n\n")
	b.WriteString(fmt.Sprintf("socket:      %s\n", m.socketPath))
	b.WriteString(fmt.Sprintf("connected:   %s\n", yesNo(m.connected)))
	b.WriteString(fmt.Sprintf("daemon:      %s\n", orDefault(m.daemonVersion, "unknown")))
	b.WriteString(fmt.Sprintf("uptime:      %s\n", m.currentUptime()))
	if m.lastPing != "" {
		b.WriteString(fmt.Sprintf("last ping:   %s\n", m.lastPing))
	}
	b.WriteString("\n")

	if len(m.agents) == 0 {
		b.WriteString("No agents provisioned yet.\n")
	} else {
		agent := m.selectedAgent()
		snap := m.agentMetrics[agent.ID]
		b.WriteString(fmt.Sprintf("agent:       %s\n", agent.ID))
		b.WriteString(fmt.Sprintf("state:       %s\n", orDefault(agent.State, snap.Status.State)))
		if snap.Status.Model != "" {
			b.WriteString(fmt.Sprintf("model:       %s\n", snap.Status.Model))
		}
		b.WriteString(fmt.Sprintf("prompt seq:  %d\n", maxUint64(agent.LastPromptSeq, snap.Status.LastPromptSeq)))
		b.WriteString(fmt.Sprintf("last event:  %s\n", m.lastEventSummary(snap)))
		b.WriteString(fmt.Sprintf("outcome:     %s\n", m.lastOutcomeSummary(snap)))
		if snap.Status.LastFailureDetail != "" {
			b.WriteString(fmt.Sprintf("error:       %s\n", snap.Status.LastFailureDetail))
		}
		b.WriteString(fmt.Sprintf("last cost:   %s\n", m.lastCostSummary(snap)))
		b.WriteString(fmt.Sprintf("last tokens: %s\n", m.lastTokenSummary(snap)))
		b.WriteString(fmt.Sprintf("tool calls:  %s\n", m.lastToolCallSummary(snap)))
		b.WriteString("\n")
		if m.promptMode {
			b.WriteString("prompt> " + m.promptInput + "\n\n")
		} else {
			b.WriteString("Press p to queue a prompt for the selected agent.\n\n")
		}
		if len(m.agents) > 1 {
			b.WriteString("agents:\n")
			for i, a := range m.agents {
				marker := ' '
				if i == m.selected {
					marker = '>'
				}
				b.WriteString(fmt.Sprintf(" %c %s (%s)\n", marker, a.ID, a.State))
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("Shortcuts: [r]efresh [g] ping [p] prompt [j/k] select [a] approvals [d] debug [q] quit\n")
	if m.flash != "" {
		b.WriteString("\n" + m.flash + "\n")
	}
	if m.lastFrameError != "" {
		b.WriteString("last error: " + m.lastFrameError + "\n")
	}
	return b.String()
}

func (m tuiModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.promptMode {
		switch msg.Type {
		case tea.KeyEsc:
			m.promptMode = false
			m.promptInput = ""
			m.flash = "prompt cancelled"
			return m, nil
		case tea.KeyEnter:
			agent := m.selectedAgent()
			if agent.ID == "" || strings.TrimSpace(m.promptInput) == "" {
				m.flash = "prompt requires a selected agent and non-empty text"
				return m, nil
			}
			text := strings.TrimSpace(m.promptInput)
			seq := uint64(time.Now().UnixMilli())
			m.flash = fmt.Sprintf("sending prompt to %s", agent.ID)
			return m, m.sendCmd(func() error { return m.transport.SendPrompt(agent.ID, text, seq) })
		case tea.KeyBackspace, tea.KeyDelete:
			if len(m.promptInput) > 0 {
				m.promptInput = m.promptInput[:len(m.promptInput)-1]
			}
			return m, nil
		case tea.KeyRunes, tea.KeySpace:
			m.promptInput += msg.String()
			return m, nil
		default:
			return m, nil
		}
	}

	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "r":
		m.flash = "requesting fresh status"
		return m, m.sendCmd(m.transport.RequestStatus)
	case "g":
		m.flash = "pinging keeperd"
		return m, m.sendCmd(m.transport.Ping)
	case "p":
		if len(m.agents) == 0 {
			m.flash = "no agent selected"
			return m, nil
		}
		m.promptMode = true
		m.promptInput = ""
		m.flash = "enter prompt text, press Enter to send"
		return m, nil
	case "j", "down":
		if len(m.agents) > 0 {
			m.selected = (m.selected + 1) % len(m.agents)
		}
		return m, nil
	case "k", "up":
		if len(m.agents) > 0 {
			m.selected = (m.selected - 1 + len(m.agents)) % len(m.agents)
		}
		return m, nil
	case "a":
		m.flash = "approval workflow not yet implemented in keeperd"
		return m, nil
	case "d":
		m.flash = "debug tip: use vivlog for MUS/audit inspection"
		return m, nil
	default:
		return m, nil
	}
}

func (m tuiModel) waitForMessage() tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-m.transport.Messages()
		if !ok {
			return tuiErrMsg{Err: fmt.Errorf("ctl stream closed")}
		}
		return msg
	}
}

func (m tuiModel) sendCmd(fn func() error) tea.Cmd {
	return func() tea.Msg {
		if err := fn(); err != nil {
			return tuiErrMsg{Err: err}
		}
		return nil
	}
}

func (m *tuiModel) syncSnapshots() {
	if len(m.agents) == 0 {
		m.selected = 0
		return
	}
	selectedID := ""
	if m.selected >= 0 && m.selected < len(m.agents) {
		selectedID = m.agents[m.selected].ID
	}
	for _, agent := range m.agents {
		snap := m.ensureSnapshot(agent.ID)
		snap.Status = agent
	}
	if selectedID == "" {
		m.selected = 0
		return
	}
	for i, agent := range m.agents {
		if agent.ID == selectedID {
			m.selected = i
			return
		}
	}
	m.selected = 0
}

func (m *tuiModel) ensureSnapshot(agentID string) *agentSnapshot {
	snap, ok := m.agentMetrics[agentID]
	if !ok {
		snap = &agentSnapshot{}
		m.agentMetrics[agentID] = snap
	}
	return snap
}

func (m tuiModel) selectedAgent() ctl.AgentStatus {
	if len(m.agents) == 0 || m.selected >= len(m.agents) || m.selected < 0 {
		return ctl.AgentStatus{}
	}
	return m.agents[m.selected]
}

func (m tuiModel) currentUptime() string {
	if m.uptimeFetched.IsZero() {
		return "unknown"
	}
	return fmtDuration(time.Duration(m.uptimeSeconds)*time.Second + time.Since(m.uptimeFetched))
}

func (m tuiModel) lastEventSummary(snap *agentSnapshot) string {
	if snap == nil {
		return "none"
	}
	if snap.LastFailure != nil {
		return fmt.Sprintf("%s: %s", snap.LastFailure.Kind, snap.LastFailure.Detail)
	}
	if snap.LastCompletion != nil {
		return fmt.Sprintf("completion: %s", snap.LastCompletion.Outcome)
	}
	if snap.Status.LastEventAt != "" {
		return snap.Status.LastEventAt
	}
	return "none"
}

func (m tuiModel) lastCostSummary(snap *agentSnapshot) string {
	if snap == nil {
		return "n/a"
	}
	if snap.Status.CostUSD != "" {
		return "$" + snap.Status.CostUSD
	}
	if snap.LastCompletion == nil {
		return "n/a"
	}
	return fmt.Sprintf("$%.4f", snap.LastCompletion.CostUSD)
}

func (m tuiModel) lastTokenSummary(snap *agentSnapshot) string {
	if snap == nil {
		return "n/a"
	}
	if snap.Status.InputTokens != 0 || snap.Status.OutputTokens != 0 {
		return fmt.Sprintf("in=%d out=%d", snap.Status.InputTokens, snap.Status.OutputTokens)
	}
	if snap.LastCompletion == nil {
		return "n/a"
	}
	return fmt.Sprintf("in=%d out=%d", snap.LastCompletion.InputTokens, snap.LastCompletion.OutputTokens)
}

func (m tuiModel) lastOutcomeSummary(snap *agentSnapshot) string {
	if snap == nil || snap.Status.LastOutcome == "" {
		return "none"
	}
	return snap.Status.LastOutcome
}

func (m tuiModel) lastToolCallSummary(snap *agentSnapshot) string {
	if snap == nil {
		return "0"
	}
	return fmt.Sprintf("%d", snap.Status.ToolCalls)
}

func decodeAckPayload(payload []byte) (tuiSubscribeAckMsg, error) {
	if len(payload) == 0 {
		return tuiSubscribeAckMsg{}, fmt.Errorf("empty ack payload")
	}
	if payload[0] == 1 {
		return tuiSubscribeAckMsg{OK: true}, nil
	}
	errText, err := mus.ReadString(bytes.NewReader(payload[1:]), 1024)
	if err != nil {
		return tuiSubscribeAckMsg{}, err
	}
	return tuiSubscribeAckMsg{OK: false, Error: errText}, nil
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tuiTickMsg(t) })
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func maxUint64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}
