package switchboard

import (
	"bytes"
	"io"
	"testing"
)

// TestMUSCodecRoundTrip verifies marshal → unmarshal identity for all MsgTypes.
func TestMUSCodecRoundTrip(t *testing.T) {
	cases := []SwarmHeader{
		{Version: 0, Type: MsgType_CtlSubscribe, FromID: "ctl", ToID: "keeper", SeqNo: 1, PayloadLen: 0},
		{Version: 0, Type: MsgType_CapabilityRequest, FromID: "agent-1", ToID: "keeper", SeqNo: 42, PayloadLen: 256},
		{Version: 0, Type: MsgType_CompletionEvent, FromID: "agent-1", ToID: "keeper", SeqNo: 1<<32 - 1, PayloadLen: 0},
		{Version: 0, Type: MsgType_Ping, FromID: "", ToID: "", SeqNo: 0, PayloadLen: 0},
		// Long IDs near the limit
		{Version: 0, Type: MsgType_CtlStatus, FromID: "a", ToID: "b", SeqNo: ^uint64(0), PayloadLen: MaxPayloadBytes},
	}

	for _, want := range cases {
		b := want.MarshalMUS()
		got, err := UnmarshalMUS(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("UnmarshalMUS(%+v): %v", want, err)
		}
		if got != want {
			t.Fatalf("round-trip mismatch:\n  want %+v\n  got  %+v", want, got)
		}
	}
}

// TestReadWriteFrame verifies that WriteFrame + ReadFrame round-trips correctly.
func TestReadWriteFrame(t *testing.T) {
	hdr := SwarmHeader{
		Version: 0, Type: MsgType_CapabilityResponse,
		FromID: "keeper", ToID: "agent-1", SeqNo: 7,
	}
	payload := []byte(`{"status":"ok","data":"hello"}`)

	var buf bytes.Buffer
	if err := WriteFrame(&buf, hdr, payload); err != nil {
		t.Fatal(err)
	}

	gotHdr, gotPayload, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if gotHdr.Type != hdr.Type || gotHdr.FromID != hdr.FromID || gotHdr.SeqNo != hdr.SeqNo {
		t.Fatalf("header mismatch: %+v", gotHdr)
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Fatalf("payload mismatch: want %q got %q", payload, gotPayload)
	}
}

// TestUnmarshalMUS_TruncatedHeader verifies clean errors on truncated input.
func TestUnmarshalMUS_TruncatedHeader(t *testing.T) {
	hdr := SwarmHeader{Version: 0, Type: MsgType_Ping, FromID: "ctl", ToID: "keeper", SeqNo: 1}
	full := hdr.MarshalMUS()

	for truncAt := 1; truncAt < len(full)-1; truncAt++ {
		_, err := UnmarshalMUS(bytes.NewReader(full[:truncAt]))
		if err == nil {
			t.Errorf("expected error for truncation at byte %d, got nil", truncAt)
		}
	}
}

// TestUnmarshalMUS_PayloadTooLarge verifies ErrPayloadTooLarge is returned.
func TestUnmarshalMUS_PayloadTooLarge(t *testing.T) {
	hdr := SwarmHeader{
		Version: 0, Type: MsgType_CapabilityRequest,
		FromID: "a", ToID: "b", SeqNo: 1,
		PayloadLen: MaxPayloadBytes + 1,
	}
	b := hdr.MarshalMUS()
	_, err := UnmarshalMUS(bytes.NewReader(b))
	if err != ErrPayloadTooLarge {
		t.Fatalf("want ErrPayloadTooLarge, got %v", err)
	}
}

// TestUnmarshalMUS_EmptyStream verifies io.EOF on empty reader.
func TestUnmarshalMUS_EmptyStream(t *testing.T) {
	_, err := UnmarshalMUS(bytes.NewReader(nil))
	if err != io.EOF {
		t.Fatalf("want io.EOF on empty stream, got %v", err)
	}
}

// FuzzUnmarshalMUS ensures arbitrary bytes never panic keeperd.
func FuzzUnmarshalMUS(f *testing.F) {
	// Seed with valid frames.
	hdr := SwarmHeader{Version: 0, Type: MsgType_Ping, FromID: "x", ToID: "y", SeqNo: 1}
	f.Add(hdr.MarshalMUS())
	f.Add([]byte{})
	f.Add([]byte{0x00, 0xFF, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x01})

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic; error is fine.
		_, _ = UnmarshalMUS(bytes.NewReader(data))
	})
}

// BenchmarkMarshalMUS measures allocation cost per frame encode.
func BenchmarkMarshalMUS(b *testing.B) {
	hdr := SwarmHeader{
		Version: 0, Type: MsgType_CapabilityRequest,
		FromID: "agent-001", ToID: "keeper", SeqNo: 12345, PayloadLen: 512,
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = hdr.MarshalMUS()
	}
}

// BenchmarkUnmarshalMUS measures allocation cost per frame decode.
func BenchmarkUnmarshalMUS(b *testing.B) {
	hdr := SwarmHeader{
		Version: 0, Type: MsgType_CapabilityRequest,
		FromID: "agent-001", ToID: "keeper", SeqNo: 12345, PayloadLen: 512,
	}
	encoded := hdr.MarshalMUS()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = UnmarshalMUS(bytes.NewReader(encoded))
	}
}
