package mus

import (
	"bytes"
	"testing"
)

func TestVarintRoundTrip(t *testing.T) {
	tests := []uint64{
		0, 1, 127, 128, 255, 256, 16383, 16384,
		0xFFFFFFFFFFFFFFFF,
		0x1234567890ABCDEF,
	}

	for _, want := range tests {
		encoded := AppendVarint(nil, want)
		got, err := ReadVarint(bytes.NewReader(encoded))
		if err != nil {
			t.Errorf("ReadVarint(%d): %v", want, err)
			continue
		}
		if got != want {
			t.Errorf("ReadVarint(%d) = %d, want %d", want, got, want)
		}
	}
}

func TestStringRoundTrip(t *testing.T) {
	tests := []string{
		"",
		"hello",
		"VIVARY",
		"Long string with spaces and special characters: !@#$%^&*()",
		"\x00\x01\x02\x03",
	}

	for _, want := range tests {
		encoded := AppendString(nil, want)
		got, err := ReadString(bytes.NewReader(encoded), 1024)
		if err != nil {
			t.Errorf("ReadString(%q): %v", want, err)
			continue
		}
		if got != want {
			t.Errorf("ReadString(%q) = %q, want %q", want, got, want)
		}
	}
}

func TestReadString_TooLong(t *testing.T) {
	s := "too long"
	encoded := AppendString(nil, s)
	_, err := ReadString(bytes.NewReader(encoded), uint64(len(s)-1))
	if err != ErrStringTooLong {
		t.Errorf("ReadString error = %v, want %v", err, ErrStringTooLong)
	}
}

func FuzzReadVarint(f *testing.F) {
	f.Add([]byte{0x00})
	f.Add([]byte{0x80, 0x01})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01})

	f.Fuzz(func(t *testing.T, data []byte) {
		r := bytes.NewReader(data)
		_, _ = ReadVarint(r)
	})
}
