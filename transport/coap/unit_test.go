package coap

import (
	"bytes"
	"testing"
)

// TestEnvelopeCodec_RoundTrip proves the CBOR codec is loss-less for every
// field of WorkEnvelope, including binary payload and labels.
func TestEnvelopeCodec_RoundTrip(t *testing.T) {
	in := WorkEnvelope{
		JobID:    "job-7e3a",
		Kind:     "infer",
		Priority: 9,
		Payload:  []byte{0x00, 0x01, 0xFF, 0xFE, 0x42},
		Deadline: 1717243200000,
		Labels:   map[string]string{"zone": "edge-west", "model": "yolo-n"},
	}
	b, err := EncodeEnvelope(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	out, err := DecodeEnvelope(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.JobID != in.JobID || out.Kind != in.Kind || out.Priority != in.Priority ||
		out.Deadline != in.Deadline {
		t.Fatalf("scalar mismatch: got %+v want %+v", out, in)
	}
	if !bytes.Equal(out.Payload, in.Payload) {
		t.Fatalf("payload mismatch: got %x want %x", out.Payload, in.Payload)
	}
	if len(out.Labels) != len(in.Labels) || out.Labels["zone"] != "edge-west" {
		t.Fatalf("labels mismatch: got %v", out.Labels)
	}
}

// TestStatusCodec_RoundTrip proves the StatusReport CBOR codec is loss-less.
func TestStatusCodec_RoundTrip(t *testing.T) {
	in := StatusReport{DeviceID: "dev-42", JobID: "job-7e3a", State: "running", Battery: 76, Seq: 1234}
	b, err := EncodeStatus(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	out, err := DecodeStatus(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out != in {
		t.Fatalf("status mismatch: got %+v want %+v", out, in)
	}
}

// TestCBORIsCompact asserts the integer-keyed CBOR encoding stays small — a
// constrained-link property: a fully-populated status report fits well under
// one 802.15.4 frame (~80 bytes app payload).
func TestCBORIsCompact(t *testing.T) {
	b, err := EncodeStatus(StatusReport{DeviceID: "dev-42", JobID: "job-7e3a", State: "running", Battery: 76, Seq: 1234})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(b) > 64 {
		t.Fatalf("status CBOR unexpectedly large: %d bytes", len(b))
	}
	t.Logf("status CBOR size = %d bytes", len(b))
}

// TestResourcePaths pins the resource-path constants so a typo is caught.
func TestResourcePaths(t *testing.T) {
	if PathDispatch != "/dispatch" {
		t.Fatalf("PathDispatch = %q", PathDispatch)
	}
	if PathStatus != "/status" {
		t.Fatalf("PathStatus = %q", PathStatus)
	}
}

// TestEncodeUint checks the minimal-byte CoAP option uint encoding.
func TestEncodeUint(t *testing.T) {
	cases := []struct {
		in   uint32
		want []byte
	}{
		{0, nil},
		{1, []byte{0x01}},
		{255, []byte{0xFF}},
		{256, []byte{0x01, 0x00}},
		{0x010203, []byte{0x01, 0x02, 0x03}},
	}
	for _, c := range cases {
		got := encodeUint(c.in)
		if !bytes.Equal(got, c.want) {
			t.Fatalf("encodeUint(%d) = %x, want %x", c.in, got, c.want)
		}
	}
}

// TestMeasureWireSizes is a fast unit check that the wire-size measurement is
// internally consistent (CoAP overhead is tiny, HTTP overhead is large).
func TestMeasureWireSizes(t *testing.T) {
	env := WorkEnvelope{JobID: "job-1", Kind: "sense", Priority: 1, Payload: []byte("hi")}
	ws, err := MeasureDispatchWireSizes(env)
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	if ws.CoAPBytes <= ws.PayloadBytes {
		t.Fatalf("CoAP bytes must exceed payload: %+v", ws)
	}
	if ws.CoAPOverhead >= ws.HTTPOverhead {
		t.Fatalf("CoAP overhead (%d) must be far below HTTP overhead (%d)", ws.CoAPOverhead, ws.HTTPOverhead)
	}
	if ws.CoAPOverhead > 32 {
		t.Fatalf("CoAP framing overhead unexpectedly large: %d bytes", ws.CoAPOverhead)
	}
	t.Logf("%s", ws)
}
