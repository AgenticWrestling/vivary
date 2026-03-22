package switchboard

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/bits"
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

	// MaxPayloadBytes is the largest payload keeperd will accept in one frame.
	// 4 MiB is generous for capability responses; adjust per capability class.
	MaxPayloadBytes = 4 * 1024 * 1024

	// MaxHeaderBytes is the largest on-wire header we will attempt to read.
	// 2 + 2*(1+MaxIDLen) + 10 + 5 = comfortably under 600 bytes.
	MaxHeaderBytes = 600
)

var (
	ErrPayloadTooLarge  = errors.New("mus: payload exceeds MaxPayloadBytes")
	ErrIDTooLong        = errors.New("mus: identity field exceeds MaxIDLen")
	ErrVarintOverflow   = errors.New("mus: varint overflow (>10 bytes)")
	ErrUnexpectedEOF    = errors.New("mus: unexpected EOF reading header")
	ErrFrameTruncated   = errors.New("mus: frame truncated during payload read")
)

// ---- varint helpers --------------------------------------------------------

// appendVarint appends the LEB128 encoding of v to b and returns the result.
func appendVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

// varintLen returns the number of bytes needed to encode v.
func varintLen(v uint64) int {
	if v == 0 {
		return 1
	}
	return (bits.Len64(v) + 6) / 7
}

// readVarint reads a single LEB128 unsigned integer from r.
// Returns ErrVarintOverflow if more than 10 bytes are consumed.
func readVarint(r io.Reader) (uint64, error) {
	var result uint64
	var shift uint
	buf := [1]byte{}
	for i := 0; i < 10; i++ {
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return 0, ErrUnexpectedEOF
			}
			return 0, err
		}
		b := buf[0]
		result |= uint64(b&0x7F) << shift
		if b < 0x80 {
			return result, nil
		}
		shift += 7
	}
	return 0, ErrVarintOverflow
}

// ---- string helpers --------------------------------------------------------

// appendString encodes s as varuint-length + UTF-8 bytes into b.
func appendString(b []byte, s string) []byte {
	b = appendVarint(b, uint64(len(s)))
	return append(b, s...)
}

// readString reads a length-prefixed string from r.
// Returns ErrIDTooLong if the decoded length exceeds maxLen.
func readString(r io.Reader, maxLen int) (string, error) {
	n, err := readVarint(r)
	if err != nil {
		return "", fmt.Errorf("reading string length: %w", err)
	}
	if n > uint64(maxLen) {
		return "", ErrIDTooLong
	}
	if n == 0 {
		return "", nil
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return "", ErrUnexpectedEOF
		}
		return "", err
	}
	return string(buf), nil
}

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
	b = appendString(b, h.FromID)
	b = appendString(b, h.ToID)
	b = appendVarint(b, h.SeqNo)
	b = appendVarint(b, uint64(h.PayloadLen))
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

	fromID, err := readString(r, MaxIDLen)
	if err != nil {
		return SwarmHeader{}, fmt.Errorf("from_id: %w", err)
	}
	toID, err := readString(r, MaxIDLen)
	if err != nil {
		return SwarmHeader{}, fmt.Errorf("to_id: %w", err)
	}
	seqNo, err := readVarint(r)
	if err != nil {
		return SwarmHeader{}, fmt.Errorf("seq_no: %w", err)
	}
	payloadLen, err := readVarint(r)
	if err != nil {
		return SwarmHeader{}, fmt.Errorf("payload_len: %w", err)
	}
	if payloadLen > MaxPayloadBytes {
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
		varintLen(uint64(len(h.FromID))) + len(h.FromID) +
		varintLen(uint64(len(h.ToID))) + len(h.ToID) +
		varintLen(h.SeqNo) +
		varintLen(uint64(h.PayloadLen))
}

// ---- Little-endian helpers used by payload structs -------------------------

// PutU32LE encodes v as 4 little-endian bytes into b[0:4].
func PutU32LE(b []byte, v uint32) { binary.LittleEndian.PutUint32(b, v) }

// U32LE decodes 4 little-endian bytes from b[0:4].
func U32LE(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }
