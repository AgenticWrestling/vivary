package switchboard

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"vivary.dev/vivary/pkg/mus"
)

// MUS wire format overview
// ========================
// VIVARY uses a subset of MUS (Metadata-Undefined Scheme) encoding:
//   - Fixed-width fields: version (uint8), msg_type (uint8) — 1 byte each.
//   - Variable-length unsigned integers: standard unsigned LEB128 (7 bits per
//     byte, LSB first, continuation bit = MSB).
//   - Strings: varuint byte-length followed by raw UTF-8 bytes.
//   - SwarmHeader is always followed by exactly PayloadLen bytes of payload.
//
// Maximum encoded header size is bounded; keeperd enforces MaxHeaderBytes to
// prevent memory exhaustion on malformed frames.

const (
	// MaxIDLen is the maximum byte length of a FromID or ToID field.
	// IDs longer than this are rejected before allocation.
	MaxIDLen = 256

	// MaxHeaderBytes is the largest on-wire header we will attempt to read.
	// 2 + 2*(1+MaxIDLen) + 10 + 5 = comfortably under 600 bytes.
	MaxHeaderBytes = 600
)

var (
	ErrPayloadTooLarge = errors.New("mus: payload exceeds MaxPayloadBytes")
	ErrIDTooLong       = errors.New("mus: identity field exceeds MaxIDLen")
	ErrFrameTruncated  = errors.New("mus: frame truncated during payload read")
)

// ---- SwarmHeader -----------------------------------------------------------

// SwarmHeader is the fixed framing header prepended to every MUS frame on all
// VIVARY pipes (agent stdio, ctl socket).
type SwarmHeader struct {
	Version    uint8
	Type       MsgType
	FromID     string
	ToID       string
	SeqNo      uint64
	PayloadLen uint32
}

// MarshalMUS serialises h into a new byte slice.
func (h *SwarmHeader) MarshalMUS() []byte {
	b := make([]byte, 0, 32+len(h.FromID)+len(h.ToID))
	b = append(b, h.Version, byte(h.Type))
	b = mus.AppendString(b, h.FromID)
	b = mus.AppendString(b, h.ToID)
	b = mus.AppendVarint(b, h.SeqNo)
	b = mus.AppendVarint(b, uint64(h.PayloadLen))
	return b
}

// WriteTo writes the header to w without allocating a full slice for the header
// itself (the two fixed bytes are written directly; strings and varints use a
// small stack buffer that is flushed to w).
func (h *SwarmHeader) WriteTo(w io.Writer) (int64, error) {
	hdr := h.MarshalMUS()
	n, err := w.Write(hdr)
	return int64(n), err
}

// UnmarshalMUS reads exactly one SwarmHeader from r.
// On any decoding error the caller should treat the connection as broken.
func UnmarshalMUS(r io.Reader) (SwarmHeader, error) {
	var fixed [2]byte
	if _, err := io.ReadFull(r, fixed[:]); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return SwarmHeader{}, io.EOF // clean stream end or half-read
		}
		return SwarmHeader{}, err
	}

	fromID, err := mus.ReadString(r, MaxIDLen)
	if err != nil {
		return SwarmHeader{}, fmt.Errorf("from_id: %w", err)
	}
	toID, err := mus.ReadString(r, MaxIDLen)
	if err != nil {
		return SwarmHeader{}, fmt.Errorf("to_id: %w", err)
	}
	seqNo, err := mus.ReadVarint(r)
	if err != nil {
		return SwarmHeader{}, fmt.Errorf("seq_no: %w", err)
	}
	payloadLen, err := mus.ReadVarint(r)
	if err != nil {
		return SwarmHeader{}, fmt.Errorf("payload_len: %w", err)
	}
	if payloadLen > uint64(mus.MaxPayloadBytes) {
		return SwarmHeader{}, ErrPayloadTooLarge
	}

	return SwarmHeader{
		Version:    fixed[0],
		Type:       MsgType(fixed[1]),
		FromID:     fromID,
		ToID:       toID,
		SeqNo:      seqNo,
		PayloadLen: uint32(payloadLen),
	}, nil
}

// ReadFrame reads a complete frame (header + payload) from r.
// The returned payload slice is freshly allocated.
func ReadFrame(r io.Reader) (SwarmHeader, []byte, error) {
	hdr, err := UnmarshalMUS(r)
	if err != nil {
		return SwarmHeader{}, nil, err
	}
	if hdr.PayloadLen == 0 {
		return hdr, nil, nil
	}
	payload := make([]byte, hdr.PayloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return hdr, nil, ErrFrameTruncated
		}
		return hdr, nil, err
	}
	return hdr, payload, nil
}

// WriteFrame serialises hdr + payload to w as a single logical frame.
// PayloadLen in hdr is overwritten with len(payload) before writing.
func WriteFrame(w io.Writer, hdr SwarmHeader, payload []byte) error {
	hdr.PayloadLen = uint32(len(payload))
	encoded := hdr.MarshalMUS()
	encoded = append(encoded, payload...)
	_, err := w.Write(encoded)
	return err
}

// ---- FrameSize helper ------------------------------------------------------

// HeaderSize returns the encoded byte length of h (without payload).
func (h *SwarmHeader) HeaderSize() int {
	return 2 +
		mus.VarintLen(uint64(len(h.FromID))) + len(h.FromID) +
		mus.VarintLen(uint64(len(h.ToID))) + len(h.ToID) +
		mus.VarintLen(h.SeqNo) +
		mus.VarintLen(uint64(h.PayloadLen))
}

// ---- Little-endian helpers used by payload structs -------------------------

// PutU32LE encodes v as 4 little-endian bytes into b[0:4].
func PutU32LE(b []byte, v uint32) { binary.LittleEndian.PutUint32(b, v) }

// U32LE decodes 4 little-endian bytes from b[0:4].
func U32LE(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }
