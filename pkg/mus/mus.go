package mus

import (
	"errors"
	"io"
	"math/bits"
)

var (
	ErrVarintOverflow = errors.New("mus: varint overflow (>10 bytes)")
	ErrUnexpectedEOF  = errors.New("mus: unexpected EOF")
	ErrStringTooLong  = errors.New("mus: string too long")
)

const (
	// MaxPayloadBytes is the largest payload keeperd will accept in one frame.
	// 4 MiB is generous for capability responses; adjust per capability class.
	MaxPayloadBytes = 4 * 1024 * 1024
)

// ---- Varint ----------------------------------------------------------------

// AppendVarint appends the LEB128 encoding of v to b and returns the result.
func AppendVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

// VarintLen returns the number of bytes needed to encode v.
func VarintLen(v uint64) int {
	if v == 0 {
		return 1
	}
	return (bits.Len64(v) + 6) / 7
}

// ReadVarint reads a single LEB128 unsigned integer from r.
func ReadVarint(r io.Reader) (uint64, error) {
	var result uint64
	var shift uint
	buf := [1]byte{}
	for i := 0; i < 10; i++ {
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return 0, io.ErrUnexpectedEOF
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

// ---- String ----------------------------------------------------------------

// AppendString encodes s as varuint-length + UTF-8 bytes into b.
func AppendString(b []byte, s string) []byte {
	b = AppendVarint(b, uint64(len(s)))
	return append(b, s...)
}

// ReadString reads a length-prefixed string from r.
func ReadString(r io.Reader, maxLen uint64) (string, error) {
	n, err := ReadVarint(r)
	if err != nil {
		return "", err
	}
	if n > maxLen {
		return "", ErrStringTooLong
	}
	if n == 0 {
		return "", nil
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return "", io.ErrUnexpectedEOF
		}
		return "", err
	}
	return string(buf), nil
}
