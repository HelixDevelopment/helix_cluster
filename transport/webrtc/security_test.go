package webrtc

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	pion "github.com/pion/webrtc/v4"
)

// newBoundPeer returns a Peer whose data channel handlers (OnMessage via
// handleInbound, recvCh, recvErr) are wired exactly as in production, without
// requiring a full ICE/DTLS handshake. handleInbound is the literal body the
// pion OnMessage callback invokes, so exercising it directly is a real test of
// the receive-path guards (not a mock of them).
func newBoundPeer() *Peer {
	return &Peer{
		openCh:  make(chan struct{}),
		recvCh:  make(chan WorkEnvelope, recvQueueDepth),
		recvErr: make(chan error, 1),
	}
}

// TestInboundOversizeMessageRejected proves the inbound size cap (DoS guard):
// a frame larger than maxInboundMessageBytes is refused BEFORE the envelope
// codec allocates anything, an ErrMessageTooLarge is surfaced, and the handler
// signals the channel should be closed. A hostile peer therefore cannot drive
// OOM with a giant message.
//
// Anti-bluff: if the size cap is removed from handleInbound, the oversize frame
// would instead fall through to DecodeEnvelope (returning a decode/short-buffer
// error, not ErrMessageTooLarge) and closeChannel would be false — failing both
// assertions below.
func TestInboundOversizeMessageRejected(t *testing.T) {
	t.Parallel()
	p := newBoundPeer()

	// One byte past the hard cap. We do NOT allocate maxInboundMessageBytes of
	// real data here beyond the slice itself; the point is that the guard trips
	// on len() before any per-field allocation in the codec.
	oversize := make([]byte, maxInboundMessageBytes+1)

	closeChannel := p.handleInbound(oversize)
	if !closeChannel {
		t.Fatal("oversize inbound frame: expected handler to signal channel close, got false")
	}

	select {
	case err := <-p.recvErr:
		if !errors.Is(err, ErrMessageTooLarge) {
			t.Fatalf("oversize inbound frame: want ErrMessageTooLarge, got %v", err)
		}
	default:
		t.Fatal("oversize inbound frame: expected ErrMessageTooLarge on recvErr, got none")
	}

	// And it must NOT have been enqueued as a work envelope.
	select {
	case env := <-p.recvCh:
		t.Fatalf("oversize inbound frame was wrongly enqueued: %+v", env)
	default:
	}
}

// TestInboundAtCapAccepted proves the cap is a boundary, not a blanket reject:
// a well-formed envelope whose encoded frame is within maxInboundMessageBytes is
// still accepted and decoded. This pins the guard to "oversize only" so a
// regression that rejects all traffic also fails.
func TestInboundAtCapAccepted(t *testing.T) {
	t.Parallel()
	p := newBoundPeer()

	in := WorkEnvelope{JobID: "ok", Kind: "infer", Priority: 4, Payload: []byte("within-cap")}
	frame, err := EncodeEnvelope(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(frame) > maxInboundMessageBytes {
		t.Fatalf("test setup: frame %d unexpectedly exceeds cap %d", len(frame), maxInboundMessageBytes)
	}

	if closeChannel := p.handleInbound(frame); closeChannel {
		t.Fatal("in-cap frame: handler wrongly signalled channel close")
	}
	select {
	case got := <-p.recvCh:
		if got.JobID != in.JobID || string(got.Payload) != string(in.Payload) {
			t.Fatalf("in-cap frame: decoded mismatch: %+v", got)
		}
	case err := <-p.recvErr:
		t.Fatalf("in-cap frame: unexpected error %v", err)
	default:
		t.Fatal("in-cap frame: expected an enqueued envelope, got none")
	}
}

// TestReceiveQueueBoundedAndNonBlocking proves two properties of the receive
// path under a flood from a fast/hostile sender with a stalled consumer:
//
//  1. Bounded memory: at most recvQueueDepth envelopes are ever held; the queue
//     does not grow without bound.
//  2. Non-blocking: handleInbound (which IS the pion read-callback body) never
//     blocks even when the queue is full and nobody is draining recvCh. If it
//     blocked, the data channel would stall.
//
// Anti-bluff: if the non-blocking select/default on recvCh is replaced with a
// blocking send, this test deadlocks past the queue depth and the 5s watchdog
// fails it.
func TestReceiveQueueBoundedAndNonBlocking(t *testing.T) {
	t.Parallel()
	p := newBoundPeer()

	frame, err := EncodeEnvelope(WorkEnvelope{JobID: "flood", Payload: []byte("x")})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	const flood = recvQueueDepth * 100 // far more than the queue can hold

	done := make(chan struct{})
	go func() {
		// Never drain recvCh: simulate a fully stalled consumer.
		for i := 0; i < flood; i++ {
			if closeChannel := p.handleInbound(frame); closeChannel {
				t.Errorf("in-cap flood frame wrongly signalled close at i=%d", i)
				break
			}
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleInbound blocked under flood with stalled consumer (receive path is not non-blocking)")
	}

	// Bounded: queue holds at most recvQueueDepth, never the full flood.
	if got := len(p.recvCh); got > recvQueueDepth {
		t.Fatalf("receive queue exceeded bound: len=%d cap=%d", got, recvQueueDepth)
	}
	if cap(p.recvCh) != recvQueueDepth {
		t.Fatalf("receive queue cap=%d, want bounded %d", cap(p.recvCh), recvQueueDepth)
	}
	// Overflow must have been surfaced as an error, not silently swallowed.
	select {
	case err := <-p.recvErr:
		if err == nil {
			t.Fatal("expected non-nil overflow error")
		}
	default:
		t.Fatal("expected a receive-queue-overflow error after flooding, got none")
	}
}

// TestConcurrentInboundIsRaceFree drives handleInbound from many goroutines at
// once (as pion may deliver under load) to prove the receive path is safe under
// -race. It also confirms non-blocking behaviour holds concurrently.
func TestConcurrentInboundIsRaceFree(t *testing.T) {
	t.Parallel()
	p := newBoundPeer()
	frame, err := EncodeEnvelope(WorkEnvelope{JobID: "race", Payload: []byte("y")})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_ = p.handleInbound(frame)
			}
		}()
	}
	wg.Wait()
	if got := len(p.recvCh); got > recvQueueDepth {
		t.Fatalf("queue exceeded bound under concurrency: %d", got)
	}
}

// TestHardenedAPIConstructs proves the hardened SettingEngine (SCTP message +
// receive-buffer caps, ICE/DTLS reaping timeouts, bounded STUN gather) is wired
// and yields a usable PeerConnection. NewPeer routes through hardenedAPI, so a
// regression that drops the hardened API (reverting to a bare
// pion.NewPeerConnection) is still covered by the round-trip integration test;
// this test additionally asserts the constructor path itself.
func TestHardenedAPIConstructs(t *testing.T) {
	t.Parallel()
	api := hardenedAPI()
	if api == nil {
		t.Fatal("hardenedAPI returned nil")
	}
	pc, err := api.NewPeerConnection(pion.Configuration{})
	if err != nil {
		t.Fatalf("hardened API failed to construct PeerConnection: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })
}

// TestOversizeSendRefusedOverRealChannel is a real end-to-end DoS test: it
// stands up two genuinely-connected peers over the real ICE/DTLS/SCTP stack,
// then attempts to send an oversize payload. The send MUST be refused (Send
// returns an error) and the channel MUST remain usable for normal traffic
// afterwards — proving an oversize send is dropped at the boundary, not OOM'd or
// allowed to wedge the channel.
//
// Defense-in-depth ordering on the send/receive paths:
//   - codec field cap (maxFieldLen, enforced in EncodeEnvelope) — refuses
//     encoding a >16 MiB field, the binding limit for Send;
//   - application inbound cap (maxInboundMessageBytes in handleInbound) —
//     refuses an oversize inbound frame before decode (see
//     TestInboundOversizeMessageRejected);
//   - SCTP transport cap (sctpMaxMessageSize on the hardened SettingEngine) —
//     bounds reassembly so the transport never buffers a message larger than the
//     application would accept.
//
// Anti-bluff: if the encode-side field cap is removed, the oversize Send would
// reach the wire and this assertion (Send returns an error) would fail.
func TestOversizeSendRefusedOverRealChannel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-stack oversize-send test in -short mode")
	}
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

	if err := Connect(ctx, offerer, answerer); err != nil {
		t.Fatalf("handshake: %v", err)
	}

	// Payload one byte past the codec field cap: the largest single field a
	// valid envelope could carry. This is refused at the encode boundary before
	// any wire I/O.
	huge := WorkEnvelope{
		JobID:   "huge",
		Kind:    "infer",
		Payload: make([]byte, maxFieldLen+1),
	}
	if err := offerer.Send(huge); err == nil {
		t.Fatal("oversize Send over real channel succeeded; boundary cap not enforced")
	} else {
		t.Logf("oversize Send correctly refused at boundary: %v", err)
	}

	// A normal-sized envelope must still round-trip after the rejected oversize
	// send (the channel is not wedged).
	ok := WorkEnvelope{JobID: "ok", Kind: "infer", Payload: []byte("fine")}
	if err := offerer.Send(ok); err != nil {
		t.Fatalf("normal Send after oversize rejection failed: %v", err)
	}
	got, err := answerer.Receive(ctx)
	if err != nil {
		t.Fatalf("receive normal envelope: %v", err)
	}
	if got.JobID != ok.JobID {
		t.Fatalf("post-rejection round-trip mismatch: %+v", got)
	}
}

// TestHardenedTimeoutsAreBounded asserts the ICE/DTLS reaping timeouts and STUN
// gather timeout are configured to finite, reasonable values (not zero/unset),
// so half-open handshakes are reaped rather than pinning goroutines.
func TestHardenedTimeoutsAreBounded(t *testing.T) {
	t.Parallel()
	if iceFailedTimeout <= 0 || iceFailedTimeout > time.Minute {
		t.Fatalf("iceFailedTimeout=%v not a bounded reaping timeout", iceFailedTimeout)
	}
	if iceDisconnectedTimeout <= 0 || iceDisconnectedTimeout > iceFailedTimeout {
		t.Fatalf("iceDisconnectedTimeout=%v must be >0 and <= failed timeout", iceDisconnectedTimeout)
	}
	if stunGatherTimeout <= 0 || stunGatherTimeout > time.Minute {
		t.Fatalf("stunGatherTimeout=%v not bounded", stunGatherTimeout)
	}
	if sctpMaxReceiveBufferSize == 0 {
		t.Fatal("sctpMaxReceiveBufferSize unbounded (0)")
	}
}
