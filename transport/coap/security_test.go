package coap

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/mux"
)

// TestDecodeEnvelope_RejectsOversized proves the body cap (DoS / unbounded
// deserialization): a body larger than MaxEnvelopeBytes is rejected before any
// CBOR work happens.
func TestDecodeEnvelope_RejectsOversized(t *testing.T) {
	oversized := make([]byte, MaxEnvelopeBytes+1)
	if _, err := DecodeEnvelope(oversized); err == nil {
		t.Fatal("DecodeEnvelope accepted a body larger than MaxEnvelopeBytes")
	}
	if _, err := DecodeStatus(oversized); err == nil {
		t.Fatal("DecodeStatus accepted a body larger than MaxEnvelopeBytes")
	}
	// A within-cap, well-formed envelope still round-trips.
	b, err := EncodeEnvelope(WorkEnvelope{JobID: "ok", Payload: []byte("x")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeEnvelope(b); err != nil {
		t.Fatalf("DecodeEnvelope rejected a valid small envelope: %v", err)
	}
}

// TestDecodeEnvelope_RejectsDeepNesting proves the hardened CBOR decode limits
// reject pathologically nested structure (defense in depth beyond the byte cap)
// without panicking — a payload nested past MaxNestedLevels (8) is refused.
func TestDecodeEnvelope_RejectsDeepNesting(t *testing.T) {
	// 12 nested single-element CBOR arrays (0x81) then a 0 — depth 12 > 8.
	var b []byte
	for i := 0; i < 12; i++ {
		b = append(b, 0x81)
	}
	b = append(b, 0x00)
	if _, err := DecodeEnvelope(b); err == nil {
		t.Fatal("hardened decoder accepted a payload nested past MaxNestedLevels")
	}
}

// TestServer_RejectsOversizedDispatch proves the server refuses an oversized
// POST /dispatch before it reaches the decoder or the user handler.
func TestServer_RejectsOversizedDispatch(t *testing.T) {
	var calls int32
	srv, err := NewServer("127.0.0.1:0", func(context.Context, WorkEnvelope) error {
		atomic.AddInt32(&calls, 1)
		return nil
	}, StatusReport{})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	cli, err := Dial(srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	// A small, valid envelope reaches the handler.
	if err := cli.Dispatch(ctx, WorkEnvelope{JobID: "ok", Payload: []byte("small")}); err != nil {
		t.Fatalf("valid dispatch failed: %v", err)
	}
	// An oversized envelope (> MaxEnvelopeBytes once encoded) must be refused.
	_ = cli.Dispatch(ctx, WorkEnvelope{JobID: "big", Payload: make([]byte, MaxEnvelopeBytes+1024)})

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("dispatch handler invoked %d times, want 1 (oversized must be rejected before the handler)", got)
	}
}

// fakeConn is a partial mux.Conn that only implements RemoteAddr (the single
// method registerObserver consults); all other methods are promoted from the
// embedded nil interface and would panic if called — they are not.
type fakeConn struct {
	mux.Conn
	addr net.Addr
}

func (f fakeConn) RemoteAddr() net.Addr { return f.addr }

// TestRegisterObserver_CoalesceAndPerSourceCap proves the observer registry is
// bounded (fixes the unbounded-registry DoS): re-registrations from the same
// (source, token) coalesce instead of growing the map, and a single source
// cannot register more than maxObserversPerSource subscriptions.
func TestRegisterObserver_CoalesceAndPerSourceCap(t *testing.T) {
	s := &Server{observers: make(map[string]*observer), bySource: make(map[string]int)}
	conn := fakeConn{addr: &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 5000}}

	// Coalesce: same (source, token) registered repeatedly stays one entry.
	for i := 0; i < 5; i++ {
		if !s.registerObserver(conn, []byte("tok-A")) {
			t.Fatal("coalescing registration was unexpectedly rejected")
		}
	}
	if len(s.observers) != 1 {
		t.Fatalf("re-registering the same (source,token) created %d observers, want 1", len(s.observers))
	}

	// Per-source cap: distinct tokens from the same source are bounded.
	admitted := 1 // tok-A already counts
	for i := 0; i < maxObserversPerSource+5; i++ {
		if s.registerObserver(conn, []byte(fmt.Sprintf("tok-%d", i))) {
			admitted++
		}
	}
	if admitted != maxObserversPerSource {
		t.Fatalf("admitted %d observers from one source, want cap %d", admitted, maxObserversPerSource)
	}
	if s.bySource[conn.addr.String()] != maxObserversPerSource {
		t.Fatalf("per-source count = %d, want %d", s.bySource[conn.addr.String()], maxObserversPerSource)
	}

	// A different source gets its own budget.
	conn2 := fakeConn{addr: &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 6000}}
	if !s.registerObserver(conn2, []byte("tok-A")) {
		t.Fatal("a fresh source was rejected despite global capacity")
	}
}
