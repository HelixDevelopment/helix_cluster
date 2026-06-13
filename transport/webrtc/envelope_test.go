package webrtc

import (
	"bytes"
	"errors"
	"testing"
)

func TestEnvelopeRoundTrip(t *testing.T) {
	t.Parallel()
	cases := []WorkEnvelope{
		{},
		{JobID: "job-1", Kind: "infer", Priority: 7, Payload: []byte("hello"), Deadline: 1717000000000},
		{JobID: "", Kind: "", Priority: 0, Payload: nil, Deadline: 0},
		{JobID: "x", Kind: "sense", Priority: 255, Payload: bytes.Repeat([]byte{0xAB}, 4096), Deadline: -1},
	}
	for i, in := range cases {
		b, err := EncodeEnvelope(in)
		if err != nil {
			t.Fatalf("case %d: encode: %v", i, err)
		}
		out, err := DecodeEnvelope(b)
		if err != nil {
			t.Fatalf("case %d: decode: %v", i, err)
		}
		if out.JobID != in.JobID || out.Kind != in.Kind || out.Priority != in.Priority ||
			out.Deadline != in.Deadline || !bytes.Equal(normalize(out.Payload), normalize(in.Payload)) {
			t.Fatalf("case %d: round-trip mismatch:\n in=%+v\nout=%+v", i, in, out)
		}
	}
}

// normalize treats nil and empty []byte as equal for comparison.
func normalize(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

func TestEnvelopeDeterministic(t *testing.T) {
	t.Parallel()
	e := WorkEnvelope{JobID: "abc", Kind: "infer", Priority: 3, Payload: []byte("payload"), Deadline: 42}
	b1, err := EncodeEnvelope(e)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := EncodeEnvelope(e)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b1, b2) {
		t.Fatalf("encoding not deterministic:\n%x\n%x", b1, b2)
	}
}

func TestDecodeBadMagic(t *testing.T) {
	t.Parallel()
	b, err := EncodeEnvelope(WorkEnvelope{JobID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	b[0] ^= 0xFF // corrupt magic
	if _, err := DecodeEnvelope(b); err != ErrBadMagic {
		t.Fatalf("expected ErrBadMagic, got %v", err)
	}
}

func TestDecodeShortBuffer(t *testing.T) {
	t.Parallel()
	if _, err := DecodeEnvelope([]byte{0x48, 0x58}); err != ErrShortBuffer {
		t.Fatalf("expected ErrShortBuffer, got %v", err)
	}
	// Truncate a valid frame mid-field.
	full, err := EncodeEnvelope(WorkEnvelope{JobID: "abcdef", Kind: "infer", Payload: []byte("xyz")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeEnvelope(full[:len(full)-2]); !errors.Is(err, ErrShortBuffer) {
		t.Fatalf("expected ErrShortBuffer on truncation, got %v", err)
	}
}

func TestDecodePayloadIsCopy(t *testing.T) {
	t.Parallel()
	src := WorkEnvelope{JobID: "j", Payload: []byte{1, 2, 3}}
	b, err := EncodeEnvelope(src)
	if err != nil {
		t.Fatal(err)
	}
	out, err := DecodeEnvelope(b)
	if err != nil {
		t.Fatal(err)
	}
	// Mutating the wire buffer must not affect the decoded payload.
	for i := range b {
		b[i] = 0
	}
	if !bytes.Equal(out.Payload, []byte{1, 2, 3}) {
		t.Fatalf("decoded payload aliases wire buffer: %v", out.Payload)
	}
}
