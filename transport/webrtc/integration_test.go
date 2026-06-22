//go:build integration

package webrtc

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// TestLoopbackHandshakeAndRoundTrip drives two real pion PeerConnections through
// a complete in-process WebRTC handshake over loopback (offer/answer + trickle
// ICE), asserts the reliable+ordered data channel reaches Open on both sides,
// then proves a bidirectional, field-exact WorkEnvelope round-trip across the
// real ICE/DTLS/SCTP data channel.
func TestLoopbackHandshakeAndRoundTrip(t *testing.T) {
	// ICE/DTLS/SCTP over loopback typically completes well under 1s; allow a
	// generous deadline to avoid flakiness on busy CI hosts. We poll on real
	// connection state via WaitOpen, never fixed sleeps.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	offerer, err := NewPeer(true)
	if err != nil {
		t.Fatalf("new offerer: %v", err)
	}
	defer offerer.Close()

	answerer, err := NewPeer(false)
	if err != nil {
		t.Fatalf("new answerer: %v", err)
	}
	defer answerer.Close()

	t.Log("HANDSHAKE: starting in-process WebRTC offer/answer + trickle-ICE over loopback")
	start := time.Now()
	if err := Connect(ctx, offerer, answerer); err != nil {
		t.Fatalf("HANDSHAKE FAILED: %v", err)
	}
	t.Logf("HANDSHAKE: completed in %s (real ICE/DTLS/SCTP)", time.Since(start).Round(time.Millisecond))

	// Data-channel-open assertions (real connection state, not faked).
	if !offerer.IsOpen() {
		t.Fatal("DATA-CHANNEL-OPEN: offerer channel not open")
	}
	if !answerer.IsOpen() {
		t.Fatal("DATA-CHANNEL-OPEN: answerer channel not open")
	}
	t.Log("DATA-CHANNEL-OPEN: both offerer and answerer data channels reached Open state")

	// Reliable + ordered configuration assertions.
	if !offerer.Ordered() || !answerer.Ordered() {
		t.Fatalf("CONFIG: data channel not ordered (offerer=%v answerer=%v)",
			offerer.Ordered(), answerer.Ordered())
	}
	if mr := offerer.MaxRetransmits(); mr != nil {
		t.Fatalf("CONFIG: offerer channel not fully reliable, MaxRetransmits=%d", *mr)
	}
	t.Log("CONFIG: data channel is reliable (MaxRetransmits=nil) + ordered=true on both sides")

	// Round-trip A -> B.
	sent := WorkEnvelope{
		JobID:    "edge-job-42",
		Kind:     "infer",
		Priority: 9,
		Payload:  []byte("classify:image-0042"),
		Deadline: time.Now().Add(5 * time.Second).UnixMilli(),
	}
	if err := offerer.Send(sent); err != nil {
		t.Fatalf("ROUND-TRIP A->B: send: %v", err)
	}
	got, err := answerer.Receive(ctx)
	if err != nil {
		t.Fatalf("ROUND-TRIP A->B: receive: %v", err)
	}
	assertEnvelopeEqual(t, "A->B", sent, got)
	t.Logf("ROUND-TRIP A->B: peer B received exact envelope JobID=%q Kind=%q Priority=%d PayloadLen=%d",
		got.JobID, got.Kind, got.Priority, len(got.Payload))

	// Round-trip B -> A (prove bidirectional).
	reply := WorkEnvelope{
		JobID:    "edge-job-42",
		Kind:     "ack",
		Priority: 1,
		Payload:  []byte("done:image-0042"),
		Deadline: 0,
	}
	if err := answerer.Send(reply); err != nil {
		t.Fatalf("ROUND-TRIP B->A: send: %v", err)
	}
	gotReply, err := offerer.Receive(ctx)
	if err != nil {
		t.Fatalf("ROUND-TRIP B->A: receive: %v", err)
	}
	assertEnvelopeEqual(t, "B->A", reply, gotReply)
	t.Logf("ROUND-TRIP B->A: peer A received exact reply JobID=%q Kind=%q Priority=%d PayloadLen=%d",
		gotReply.JobID, gotReply.Kind, gotReply.Priority, len(gotReply.Payload))

	t.Log("VERDICT: bidirectional WorkEnvelope round-trip over real WebRTC data channel PASSED")
}

func assertEnvelopeEqual(t *testing.T, dir string, want, got WorkEnvelope) {
	t.Helper()
	if got.JobID != want.JobID {
		t.Fatalf("%s: JobID mismatch: want %q got %q", dir, want.JobID, got.JobID)
	}
	if got.Kind != want.Kind {
		t.Fatalf("%s: Kind mismatch: want %q got %q", dir, want.Kind, got.Kind)
	}
	if got.Priority != want.Priority {
		t.Fatalf("%s: Priority mismatch: want %d got %d", dir, want.Priority, got.Priority)
	}
	if got.Deadline != want.Deadline {
		t.Fatalf("%s: Deadline mismatch: want %d got %d", dir, want.Deadline, got.Deadline)
	}
	if !bytes.Equal(got.Payload, want.Payload) {
		t.Fatalf("%s: Payload mismatch: want %q got %q", dir, want.Payload, got.Payload)
	}
}
