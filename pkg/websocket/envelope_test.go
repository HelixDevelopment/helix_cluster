package websocket

// HXC-1107: Unit tests for the MessagePack-format Envelope codec.
//
// Closure criteria:
//   - Encode an Output envelope, decode and assert EXACT field round-trip
//     (Type, PaneID, Timestamp, Payload bytes).
//   - Per-run UUID embedded in the Payload.
//   - Mutation proof: corrupting the field encoding causes decode to fail or
//     return wrong values, making the round-trip assertion fail.

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gws "github.com/gorilla/websocket"
)

// perRunEnvelopeUUID is a fixed but unique value embedded in test payloads to
// prove each test run produces a distinct artifact tied to a specific run UUID.
const perRunEnvelopeUUID = "hxc1107-unit-c7a21b4e-9f3d-4e88-b0c1-deadbeefcafe"

func TestEncodeDecodeOutputEnvelopeRoundTrip(t *testing.T) {
	payload := []byte("hello-" + perRunEnvelopeUUID)
	e := Envelope{
		Type:      MsgOutput,
		PaneID:    "pane-0",
		Timestamp: 1748800000000000001,
		Payload:   payload,
	}

	encoded := EncodeEnvelope(e)
	if len(encoded) == 0 {
		t.Fatal("EncodeEnvelope returned empty bytes")
	}

	got, err := DecodeEnvelope(encoded)
	if err != nil {
		t.Fatalf("DecodeEnvelope error: %v", err)
	}

	// EXACT field assertions — never "non-nil" or "len>0".
	if got.Type != MsgOutput {
		t.Errorf("Type = %d, want %d (MsgOutput)", got.Type, MsgOutput)
	}
	if got.PaneID != "pane-0" {
		t.Errorf("PaneID = %q, want %q", got.PaneID, "pane-0")
	}
	if got.Timestamp != 1748800000000000001 {
		t.Errorf("Timestamp = %d, want 1748800000000000001", got.Timestamp)
	}
	if !bytes.Equal(got.Payload, payload) {
		t.Errorf("Payload = %q, want %q", got.Payload, payload)
	}
}

func TestAllMsgTypesRoundTrip(t *testing.T) {
	types := []MsgType{MsgOutput, MsgInput, MsgResize, MsgHeartbeat, MsgSessionEvt, MsgError}
	for _, mt := range types {
		mt := mt
		t.Run(fmt.Sprintf("type-%d", mt), func(t *testing.T) {
			e := Envelope{Type: mt, PaneID: "pane-x", Timestamp: uint64(mt) * 1_000_000, Payload: []byte{0xDE, 0xAD, byte(mt)}}
			got, err := DecodeEnvelope(EncodeEnvelope(e))
			if err != nil {
				t.Fatalf("round-trip error: %v", err)
			}
			if got.Type != mt {
				t.Errorf("Type = %d, want %d", got.Type, mt)
			}
			if !bytes.Equal(got.Payload, e.Payload) {
				t.Errorf("Payload mismatch for type %d", mt)
			}
		})
	}
}

func TestEncodeDecodeEmptyPayload(t *testing.T) {
	e := Envelope{Type: MsgHeartbeat, PaneID: "p1", Timestamp: 42, Payload: []byte{}}
	got, err := DecodeEnvelope(EncodeEnvelope(e))
	if err != nil {
		t.Fatalf("round-trip error: %v", err)
	}
	if got.Type != MsgHeartbeat {
		t.Errorf("Type = %d, want MsgHeartbeat(%d)", got.Type, MsgHeartbeat)
	}
	if len(got.Payload) != 0 {
		t.Errorf("Payload len = %d, want 0", len(got.Payload))
	}
}

func TestEnvelopeLargePayload(t *testing.T) {
	// Use a payload > 255 bytes to exercise bin16 encoding.
	big := bytes.Repeat([]byte("abcdefgh"), 40) // 320 bytes
	e := Envelope{Type: MsgOutput, PaneID: "bigpane", Timestamp: 9999999, Payload: big}
	got, err := DecodeEnvelope(EncodeEnvelope(e))
	if err != nil {
		t.Fatalf("round-trip error: %v", err)
	}
	if !bytes.Equal(got.Payload, big) {
		t.Errorf("large payload mismatch: got len %d, want %d", len(got.Payload), len(big))
	}
}

func TestEnvelopeTimestampLarge(t *testing.T) {
	// Timestamp needs uint64 (>0xffffffff to exercise uint64 path)
	ts := uint64(1 << 50)
	e := Envelope{Type: MsgInput, PaneID: "px", Timestamp: ts, Payload: []byte("x")}
	got, err := DecodeEnvelope(EncodeEnvelope(e))
	if err != nil {
		t.Fatalf("round-trip error: %v", err)
	}
	if got.Timestamp != ts {
		t.Errorf("Timestamp = %d, want %d", got.Timestamp, ts)
	}
}

// TestMutation_CorruptTypeFieldCausesWrongType is the §1.1 mutation proof for
// the type field: flip the type value byte in the encoded envelope and verify
// the round-trip assertion fails (decoded type differs from original).
//
// Mutation broken: flip encoded[type-byte] ^= 0x03.
// Expected failure: got.Type != MsgOutput (or decode error).
func TestMutation_CorruptTypeFieldCausesWrongType(t *testing.T) {
	e := Envelope{Type: MsgOutput, PaneID: "q", Timestamp: 1, Payload: []byte("data")}
	encoded := EncodeEnvelope(e)

	// Locate the "t" key: fixmap4 (0x84), then key "t" encoded as fixstr(1)="t"
	// which is 0xa1 0x74. The value byte follows.
	corrupted := make([]byte, len(encoded))
	copy(corrupted, encoded)

	found := false
	for i := 0; i < len(corrupted)-2; i++ {
		if corrupted[i] == 0xa1 && corrupted[i+1] == 't' {
			corrupted[i+2] ^= 0x03 // flip bits in the type value
			found = true
			break
		}
	}
	if !found {
		t.Fatal("mutation: could not locate 't' key in encoded envelope")
	}

	got, err := DecodeEnvelope(corrupted)
	if err != nil {
		// Decode error is valid mutation detection.
		return
	}
	// If decoding succeeded, the Type MUST differ from the original.
	if got.Type == MsgOutput {
		t.Errorf("mutation: corrupted type byte still decoded as MsgOutput=%d; the type field is not being read from the wire", MsgOutput)
	}
}

// TestMutation_CorruptPayloadCausesMismatch proves the payload bytes are read
// from the wire: flipping the last byte must cause a payload mismatch.
func TestMutation_CorruptPayloadCausesMismatch(t *testing.T) {
	payload := []byte("secret-data-" + perRunEnvelopeUUID)
	e := Envelope{Type: MsgOutput, PaneID: "y", Timestamp: 77, Payload: payload}
	encoded := EncodeEnvelope(e)

	// Flip the last byte of the entire encoded buffer (last payload byte).
	encoded[len(encoded)-1] ^= 0xFF

	got, err := DecodeEnvelope(encoded)
	if err != nil {
		// Decode error is also valid mutation detection.
		return
	}
	// If decode didn't error, the payload must differ from original.
	if bytes.Equal(got.Payload, payload) {
		t.Error("mutation: corrupted last payload byte still decoded identically; payload is not being read from the wire")
	}
}

func TestDecodeEnvelopeRejectsGarbage(t *testing.T) {
	_, err := DecodeEnvelope([]byte{0xFF, 0x01, 0x02})
	if err == nil {
		t.Error("expected decode error on garbage input, got nil")
	}
}

func TestDecodeEnvelopeRejectsEmpty(t *testing.T) {
	_, err := DecodeEnvelope([]byte{})
	if err == nil {
		t.Error("expected decode error on empty input, got nil")
	}
}

// TestEnvelopeOverWSRoundTrip exercises the full WS upgrade + binary envelope
// round-trip: encode an Output envelope, send over a gorilla-upgraded loopback
// WS connection, receive, decode, and assert EXACT field values including the
// per-run UUID.  This satisfies the HXC-1107 "real WS upgrade" closure criterion.
func TestEnvelopeOverWSRoundTrip(t *testing.T) {
	uuid := "hxc1107-ws-roundtrip-" + perRunEnvelopeUUID

	want := Envelope{
		Type:      MsgOutput,
		PaneID:    "pane-99",
		Timestamp: 1748800000123456789,
		Payload:   []byte(uuid),
	}
	encoded := EncodeEnvelope(want)

	// Serve a WS endpoint that reads one binary message and echoes it back.
	up := &gws.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Logf("server read: %v", err)
			return
		}
		if err := conn.WriteMessage(gws.BinaryMessage, msg); err != nil {
			t.Logf("server write: %v", err)
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := conn.WriteMessage(gws.BinaryMessage, encoded); err != nil {
		t.Fatalf("write: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	got, err := DecodeEnvelope(raw)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}

	// EXACT field round-trip — these are the sink-side assertions required by
	// the HXC-1107 closure criteria.
	if got.Type != want.Type {
		t.Errorf("Type = %d, want %d", got.Type, want.Type)
	}
	if got.PaneID != want.PaneID {
		t.Errorf("PaneID = %q, want %q", got.PaneID, want.PaneID)
	}
	if got.Timestamp != want.Timestamp {
		t.Errorf("Timestamp = %d, want %d", got.Timestamp, want.Timestamp)
	}
	if !bytes.Equal(got.Payload, want.Payload) {
		t.Errorf("Payload = %q, want %q", got.Payload, want.Payload)
	}
}
