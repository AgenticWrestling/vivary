package chromed

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"

	"vivary.dev/vivary/internal/switchboard"
	"vivary.dev/vivary/pkg/mus"
)

const Identity = "chromed"

const (
	MsgTypeAcquire switchboard.MsgType = 0x30
	MsgTypeRelease switchboard.MsgType = 0x31
	MsgTypeStatus  switchboard.MsgType = 0x32
)

type AcquireRequest struct {
	AgentID     string
	ProxyServer string
	TimeoutSec  uint32
	Headless    bool
}

func (p *AcquireRequest) MarshalMUS() []byte {
	var b []byte
	b = mus.AppendString(b, p.AgentID)
	b = mus.AppendString(b, p.ProxyServer)
	b = mus.AppendVarint(b, uint64(p.TimeoutSec))
	if p.Headless {
		b = append(b, 1)
	} else {
		b = append(b, 0)
	}
	return b
}

func (p *AcquireRequest) UnmarshalMUS(r io.Reader) error {
	var err error
	if p.AgentID, err = mus.ReadString(r, 128); err != nil {
		return err
	}
	if p.ProxyServer, err = mus.ReadString(r, 1024); err != nil {
		return err
	}
	v, err := mus.ReadVarint(r)
	if err != nil {
		return err
	}
	p.TimeoutSec = uint32(v)
	var fixed [1]byte
	if _, err := io.ReadFull(r, fixed[:]); err != nil {
		return err
	}
	p.Headless = fixed[0] != 0
	return nil
}

type AcquireResponse struct {
	OK         bool
	DebugAddr  string
	ProfileDir string
	Error      string
}

func (p *AcquireResponse) MarshalMUS() []byte {
	var b []byte
	if p.OK {
		b = append(b, 1)
	} else {
		b = append(b, 0)
	}
	b = mus.AppendString(b, p.DebugAddr)
	b = mus.AppendString(b, p.ProfileDir)
	b = mus.AppendString(b, p.Error)
	return b
}

func (p *AcquireResponse) UnmarshalMUS(r io.Reader) error {
	var fixed [1]byte
	if _, err := io.ReadFull(r, fixed[:]); err != nil {
		return err
	}
	p.OK = fixed[0] != 0
	var err error
	if p.DebugAddr, err = mus.ReadString(r, 256); err != nil {
		return err
	}
	if p.ProfileDir, err = mus.ReadString(r, 4096); err != nil {
		return err
	}
	p.Error, err = mus.ReadString(r, 4096)
	return err
}

type ReleaseRequest struct{ AgentID string }

func (p *ReleaseRequest) MarshalMUS() []byte { return mus.AppendString(nil, p.AgentID) }
func (p *ReleaseRequest) UnmarshalMUS(r io.Reader) error {
	var err error
	p.AgentID, err = mus.ReadString(r, 128)
	return err
}

type ReleaseResponse struct {
	OK    bool
	Error string
}

func (p *ReleaseResponse) MarshalMUS() []byte {
	var b []byte
	if p.OK {
		b = append(b, 1)
	} else {
		b = append(b, 0)
	}
	b = mus.AppendString(b, p.Error)
	return b
}

func (p *ReleaseResponse) UnmarshalMUS(r io.Reader) error {
	var fixed [1]byte
	if _, err := io.ReadFull(r, fixed[:]); err != nil {
		return err
	}
	p.OK = fixed[0] != 0
	var err error
	p.Error, err = mus.ReadString(r, 4096)
	return err
}

type StatusRequest struct{ AgentID string }

func (p *StatusRequest) MarshalMUS() []byte { return mus.AppendString(nil, p.AgentID) }
func (p *StatusRequest) UnmarshalMUS(r io.Reader) error {
	var err error
	p.AgentID, err = mus.ReadString(r, 128)
	return err
}

type StatusResponse struct {
	Found      bool
	Running    bool
	DebugAddr  string
	ProfileDir string
	Error      string
}

func (p *StatusResponse) MarshalMUS() []byte {
	var b []byte
	if p.Found {
		b = append(b, 1)
	} else {
		b = append(b, 0)
	}
	if p.Running {
		b = append(b, 1)
	} else {
		b = append(b, 0)
	}
	b = mus.AppendString(b, p.DebugAddr)
	b = mus.AppendString(b, p.ProfileDir)
	b = mus.AppendString(b, p.Error)
	return b
}

func (p *StatusResponse) UnmarshalMUS(r io.Reader) error {
	var fixed [2]byte
	if _, err := io.ReadFull(r, fixed[:]); err != nil {
		return err
	}
	p.Found = fixed[0] != 0
	p.Running = fixed[1] != 0
	var err error
	if p.DebugAddr, err = mus.ReadString(r, 256); err != nil {
		return err
	}
	if p.ProfileDir, err = mus.ReadString(r, 4096); err != nil {
		return err
	}
	p.Error, err = mus.ReadString(r, 4096)
	return err
}

func ResponseTypeFor(msgType switchboard.MsgType) (switchboard.MsgType, error) {
	switch msgType {
	case MsgTypeAcquire, MsgTypeRelease, MsgTypeStatus:
		return msgType, nil
	default:
		return 0, fmt.Errorf("unsupported chromed msg type %v", msgType)
	}
}

type Client struct{ SocketPath string }

func (c *Client) Acquire(ctx context.Context, req AcquireRequest) (AcquireResponse, error) {
	var resp AcquireResponse
	err := c.roundTrip(ctx, MsgTypeAcquire, req.MarshalMUS(), &resp)
	if err != nil {
		return AcquireResponse{}, err
	}
	if !resp.OK {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

func (c *Client) Release(ctx context.Context, agentID string) (ReleaseResponse, error) {
	var resp ReleaseResponse
	err := c.roundTrip(ctx, MsgTypeRelease, (&ReleaseRequest{AgentID: agentID}).MarshalMUS(), &resp)
	if err != nil {
		return ReleaseResponse{}, err
	}
	if !resp.OK && resp.Error != "" {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

func (c *Client) Status(ctx context.Context, agentID string) (StatusResponse, error) {
	var resp StatusResponse
	err := c.roundTrip(ctx, MsgTypeStatus, (&StatusRequest{AgentID: agentID}).MarshalMUS(), &resp)
	if err != nil {
		return StatusResponse{}, err
	}
	if resp.Error != "" {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

func (c *Client) roundTrip(ctx context.Context, msgType switchboard.MsgType, payload []byte, resp interface{ UnmarshalMUS(io.Reader) error }) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.SocketPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	hdr := switchboard.SwarmHeader{Version: 0, Type: msgType, FromID: "keeper", ToID: Identity, SeqNo: 1}
	if err := switchboard.WriteFrame(conn, hdr, payload); err != nil {
		return err
	}
	respHdr, respPayload, err := switchboard.ReadFrame(conn)
	if err != nil {
		return err
	}
	if respHdr.Type != msgType {
		return fmt.Errorf("unexpected response type %v", respHdr.Type)
	}
	return resp.UnmarshalMUS(bytes.NewReader(respPayload))
}
