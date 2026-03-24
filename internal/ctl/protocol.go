package ctl

import (
	"fmt"
	"io"

	"vivary.dev/vivary/pkg/mus"
)

// Reserved identity for the operator control socket.
const CtlIdentity = "ctl"

// ---- Payload Interfaces ----------------------------------------------------

type MUSPayload interface {
	MarshalMUS() []byte
	UnmarshalMUS(r io.Reader) error
}

// ---- Request payloads (ctl → keeperd) --------------------------------------

type SubscribePayload struct{}

func (p *SubscribePayload) MarshalMUS() []byte           { return nil }
func (p *SubscribePayload) UnmarshalMUS(r io.Reader) error { return nil }

type AgentCreatePayload struct {
	ID             string
	Provider       string
	Template       string
	CPUShares      uint32
	MemoryMaxBytes uint64
}

func (p *AgentCreatePayload) MarshalMUS() []byte {
	var b []byte
	b = mus.AppendString(b, p.ID)
	b = mus.AppendString(b, p.Provider)
	b = mus.AppendString(b, p.Template)
	b = mus.AppendVarint(b, uint64(p.CPUShares))
	b = mus.AppendVarint(b, p.MemoryMaxBytes)
	return b
}

func (p *AgentCreatePayload) UnmarshalMUS(r io.Reader) error {
	var err error
	if p.ID, err = mus.ReadString(r, 64); err != nil {
		return err
	}
	if p.Provider, err = mus.ReadString(r, 64); err != nil {
		return err
	}
	if p.Template, err = mus.ReadString(r, 1024); err != nil {
		return err
	}
	v, err := mus.ReadVarint(r)
	if err != nil {
		return err
	}
	p.CPUShares = uint32(v)
	if p.MemoryMaxBytes, err = mus.ReadVarint(r); err != nil {
		return err
	}
	return nil
}

type AgentDestroyPayload struct {
	ID string
}

func (p *AgentDestroyPayload) MarshalMUS() []byte {
	return mus.AppendString(nil, p.ID)
}

func (p *AgentDestroyPayload) UnmarshalMUS(r io.Reader) error {
	var err error
	p.ID, err = mus.ReadString(r, 64)
	return err
}

type PromptPayload struct {
	AgentID string
	Seq     uint64
	Text    string
}

func (p *PromptPayload) MarshalMUS() []byte {
	var b []byte
	b = mus.AppendString(b, p.AgentID)
	b = mus.AppendVarint(b, p.Seq)
	b = mus.AppendString(b, p.Text)
	return b
}

func (p *PromptPayload) UnmarshalMUS(r io.Reader) error {
	var err error
	if p.AgentID, err = mus.ReadString(r, 64); err != nil {
		return err
	}
	if p.Seq, err = mus.ReadVarint(r); err != nil {
		return err
	}
	p.Text, err = mus.ReadString(r, mus.MaxPayloadBytes)
	return err
}

type ApprovalPayload struct {
	RequestID uint64
	Granted   bool
	Reason    string
}

func (p *ApprovalPayload) MarshalMUS() []byte {
	var b []byte
	b = mus.AppendVarint(b, p.RequestID)
	if p.Granted {
		b = append(b, 1)
	} else {
		b = append(b, 0)
	}
	b = mus.AppendString(b, p.Reason)
	return b
}

func (p *ApprovalPayload) UnmarshalMUS(r io.Reader) error {
	var err error
	if p.RequestID, err = mus.ReadVarint(r); err != nil {
		return err
	}
	var fixed [1]byte
	if _, err := io.ReadFull(r, fixed[:]); err != nil {
		return err
	}
	p.Granted = fixed[0] != 0
	p.Reason, err = mus.ReadString(r, 1024)
	return err
}

// ---- Response/push payloads (keeperd → ctl) --------------------------------

type StatusPayload struct {
	DaemonVersion string
	UptimeSeconds int64
	Agents        []AgentStatus
}

func (p *StatusPayload) MarshalMUS() []byte {
	var b []byte
	b = mus.AppendString(b, p.DaemonVersion)
	b = mus.AppendVarint(b, uint64(p.UptimeSeconds))
	b = mus.AppendVarint(b, uint64(len(p.Agents)))
	for _, a := range p.Agents {
		b = append(b, a.marshalMUS()...)
	}
	return b
}

func (p *StatusPayload) UnmarshalMUS(r io.Reader) error {
	var err error
	if p.DaemonVersion, err = mus.ReadString(r, 64); err != nil {
		return err
	}
	upt, err := mus.ReadVarint(r)
	if err != nil {
		return err
	}
	p.UptimeSeconds = int64(upt)
	n, err := mus.ReadVarint(r)
	if err != nil {
		return err
	}
	p.Agents = make([]AgentStatus, n)
	for i := range n {
		if err := p.Agents[i].unmarshalMUS(r); err != nil {
			return err
		}
	}
	return nil
}

type AgentStatus struct {
	ID            string
	State         string
	LastPromptSeq uint64
	LastEventAt   string // RFC3339 timestamp of last event; empty if no event yet
	LastOutcome   string // "success", "loop_detected", "subprocess_crash", etc.
	InputTokens   uint32
	OutputTokens  uint32
	CostUSD       string // formatted float; empty if unknown
	ToolCalls     uint32
}

func (a *AgentStatus) marshalMUS() []byte {
	var b []byte
	b = mus.AppendString(b, a.ID)
	b = mus.AppendString(b, a.State)
	b = mus.AppendVarint(b, a.LastPromptSeq)
	b = mus.AppendString(b, a.LastEventAt)
	b = mus.AppendString(b, a.LastOutcome)
	b = mus.AppendVarint(b, uint64(a.InputTokens))
	b = mus.AppendVarint(b, uint64(a.OutputTokens))
	b = mus.AppendString(b, a.CostUSD)
	b = mus.AppendVarint(b, uint64(a.ToolCalls))
	return b
}

func (a *AgentStatus) unmarshalMUS(r io.Reader) error {
	var err error
	if a.ID, err = mus.ReadString(r, 64); err != nil {
		return err
	}
	if a.State, err = mus.ReadString(r, 32); err != nil {
		return err
	}
	if a.LastPromptSeq, err = mus.ReadVarint(r); err != nil {
		return err
	}
	if a.LastEventAt, err = mus.ReadString(r, 64); err != nil {
		return err
	}
	if a.LastOutcome, err = mus.ReadString(r, 64); err != nil {
		return err
	}
	v, err := mus.ReadVarint(r)
	if err != nil {
		return err
	}
	a.InputTokens = uint32(v)
	if v, err = mus.ReadVarint(r); err != nil {
		return err
	}
	a.OutputTokens = uint32(v)
	if a.CostUSD, err = mus.ReadString(r, 32); err != nil {
		return err
	}
	if v, err = mus.ReadVarint(r); err != nil {
		return err
	}
	a.ToolCalls = uint32(v)
	return nil
}

type AgentListPayload struct {
	Agents []AgentStatus
}

func (p *AgentListPayload) MarshalMUS() []byte {
	var b []byte
	b = mus.AppendVarint(b, uint64(len(p.Agents)))
	for _, a := range p.Agents {
		b = append(b, a.marshalMUS()...)
	}
	return b
}

func (p *AgentListPayload) UnmarshalMUS(r io.Reader) error {
	n, err := mus.ReadVarint(r)
	if err != nil {
		return err
	}
	p.Agents = make([]AgentStatus, n)
	for i := range n {
		if err := p.Agents[i].unmarshalMUS(r); err != nil {
			return err
		}
	}
	return nil
}

// Telemetry/Audit events (same as defined in internal/audit but mirrored here
// for ctl protocol consistency if needed, or we just use audit types).
// For now, keep it simple and use JSON for the event push payloads to ctl,
// or also migrate them to MUS. Let's migrate them to MUS too.

type CompletionEventPayload struct {
	AgentID       string
	PromptSeq     uint64
	Model         string
	InputTokens   uint32
	OutputTokens  uint32
	CostUSD       float64
	Outcome       string
	ToolCalls     uint32
}

func (p *CompletionEventPayload) MarshalMUS() []byte {
	var b []byte
	b = mus.AppendString(b, p.AgentID)
	b = mus.AppendVarint(b, p.PromptSeq)
	b = mus.AppendString(b, p.Model)
	b = mus.AppendVarint(b, uint64(p.InputTokens))
	b = mus.AppendVarint(b, uint64(p.OutputTokens))
	// float64 via bits
	// b = mus.AppendVarint(b, math.Float64bits(p.CostUSD)) 
	// To keep codec simple, maybe just encode as micro-cents or string?
	// Let's use string for now to avoid math import in switchboard if not needed.
	// Actually switchboard doesn't have math.
	b = mus.AppendString(b, fmt.Sprintf("%f", p.CostUSD))
	b = mus.AppendString(b, p.Outcome)
	b = mus.AppendVarint(b, uint64(p.ToolCalls))
	return b
}

func (p *CompletionEventPayload) UnmarshalMUS(r io.Reader) error {
	var err error
	if p.AgentID, err = mus.ReadString(r, 64); err != nil { return err }
	if p.PromptSeq, err = mus.ReadVarint(r); err != nil { return err }
	if p.Model, err = mus.ReadString(r, 64); err != nil { return err }
	v, err := mus.ReadVarint(r); if err != nil { return err }; p.InputTokens = uint32(v)
	v, err = mus.ReadVarint(r); if err != nil { return err }; p.OutputTokens = uint32(v)
	costStr, err := mus.ReadString(r, 64); if err != nil { return err }
	fmt.Sscanf(costStr, "%f", &p.CostUSD)
	if p.Outcome, err = mus.ReadString(r, 32); err != nil { return err }
	v, err = mus.ReadVarint(r); if err != nil { return err }; p.ToolCalls = uint32(v)
	return nil
}

type FailureEventPayload struct {
	AgentID   string
	PromptSeq uint64
	Kind      string
	Detail    string
}

func (p *FailureEventPayload) MarshalMUS() []byte {
	var b []byte
	b = mus.AppendString(b, p.AgentID)
	b = mus.AppendVarint(b, p.PromptSeq)
	b = mus.AppendString(b, p.Kind)
	b = mus.AppendString(b, p.Detail)
	return b
}

func (p *FailureEventPayload) UnmarshalMUS(r io.Reader) error {
	var err error
	if p.AgentID, err = mus.ReadString(r, 64); err != nil { return err }
	if p.PromptSeq, err = mus.ReadVarint(r); err != nil { return err }
	if p.Kind, err = mus.ReadString(r, 32); err != nil { return err }
	p.Detail, err = mus.ReadString(r, 4096)
	return err
}
