package capabilities

import (
	"io"

	"vivary.dev/vivary/internal/switchboard"
	"vivary.dev/vivary/pkg/mus"
)

type ToolRequestMode uint8

const (
	ToolRequestInvoke ToolRequestMode = iota
	ToolRequestSchema
)

// ToolRequestPayload is the Ward tool-socket protocol request.
type ToolRequestPayload struct {
	Capability string
	Mode       ToolRequestMode
	Args       []byte
}

func (p *ToolRequestPayload) MarshalMUS() []byte {
	var b []byte
	b = mus.AppendString(b, p.Capability)
	b = append(b, byte(p.Mode))
	b = mus.AppendVarint(b, uint64(len(p.Args)))
	b = append(b, p.Args...)
	return b
}

func (p *ToolRequestPayload) UnmarshalMUS(r io.Reader) error {
	var err error
	if p.Capability, err = mus.ReadString(r, switchboard.MaxIDLen); err != nil {
		return err
	}
	var fixed [1]byte
	if _, err := io.ReadFull(r, fixed[:]); err != nil {
		return err
	}
	p.Mode = ToolRequestMode(fixed[0])
	l, err := mus.ReadVarint(r)
	if err != nil {
		return err
	}
	p.Args = make([]byte, l)
	_, err = io.ReadFull(r, p.Args)
	return err
}
