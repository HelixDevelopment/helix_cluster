//go:build integration

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

// TestCrossTransport_QUICtoWS is the mandated heterogeneous-routing integration
// test (HXC-1186 / CLAUDE-1). It stands up ONE gateway with TWO real network
// transports — a QUIC server (real UDP socket + TLS 1.3) and a WebSocket server
// (real TCP) — then:
//
//   - dials a real QUIC client as "scheduler";
//   - dials a real WebSocket client as the target node "node-A";
//   - sends a dispatch from the QUIC client addressed to node-A;
//   - asserts node-A receives it over the WS socket byte-exact (QUIC -> gateway
//     -> WS), proving an envelope crosses from one wire protocol to another with
//     no special-casing in the routing core;
//   - dials a second WS client "node-B" and asserts it receives NOTHING (routing
//     isolation across transports).
//
// Every hop is a real kernel socket; there are no in-memory fakes on either side.
func TestCrossTransport_QUICtoWS(t *testing.T) {
	gw := New()
	defer gw.Close()

	// --- WebSocket transport (real TCP) ---
	wst := NewWebSocketTransport(gw)
	srv := httptest.NewServer(wst.Handler())
	defer srv.Close()
	wsBase := wsURL(srv.URL, "/edge")

	// --- QUIC transport (real UDP + TLS 1.3) ---
	srvCfg, cliCfg := quicTestConfigs(t)
	qt, err := NewQUICTransport(gw, "127.0.0.1:0", srvCfg)
	if err != nil {
		t.Fatalf("start quic transport: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Real WS target + bystander.
	target, err := DialWS(ctx, wsBase, "node-A")
	if err != nil {
		t.Fatalf("dial WS target node-A: %v", err)
	}
	defer target.Close()
	bystander, err := DialWS(ctx, wsBase, "node-B")
	if err != nil {
		t.Fatalf("dial WS bystander node-B: %v", err)
	}
	defer bystander.Close()

	// Real QUIC sender.
	sender, err := DialQUIC(ctx, qt.Addr(), "scheduler", cliCfg)
	if err != nil {
		t.Fatalf("dial QUIC scheduler: %v", err)
	}
	defer sender.Close()

	// Wait until all three connections are registered server-side.
	waitForRoutes(t, gw, map[string]int{"node-A": 1, "node-B": 1, "scheduler": 1})

	// --- QUIC -> gateway -> WS dispatch ---
	payload := json.RawMessage(`{"task":"inference","model":"helix-7b","tokens":4096,"raw":"\u0000\u0001\u0002"}`)
	dispatch := Envelope{
		ID:       "xt-quic-ws-1",
		Kind:     KindDispatch,
		Source:   "scheduler",
		Dest:     "node-A",
		Payload:  payload,
		IssuedAt: time.Now().UTC(),
	}
	if err := sender.Send(dispatch); err != nil {
		t.Fatalf("QUIC sender.Send: %v", err)
	}

	got, err := target.Recv(8 * time.Second)
	if err != nil {
		t.Fatalf("node-A did not receive QUIC-originated dispatch over WS: %v", err)
	}
	if got.ID != "xt-quic-ws-1" {
		t.Fatalf("routing error: node-A got id %q want xt-quic-ws-1", got.ID)
	}
	if got.Dest != "node-A" {
		t.Fatalf("routing error: dest %q want node-A", got.Dest)
	}
	if !bytes.Equal(got.Payload, payload) {
		t.Fatalf("payload not byte-equal across QUIC->WS: got %q want %q", got.Payload, payload)
	}
	t.Logf("CROSS-TRANSPORT OK: QUIC->gateway->WS delivered id=%s dest=%s byte-exact payload=%s",
		got.ID, got.Dest, string(got.Payload))

	// --- Isolation across transports: node-B (WS) must receive nothing ---
	if _, err := bystander.Recv(500 * time.Millisecond); err == nil {
		t.Fatalf("ISOLATION VIOLATED: WS node-B received a dispatch addressed to node-A")
	} else {
		t.Logf("ISOLATION OK: WS node-B received nothing (timed out as expected): %v", err)
	}
}

// TestCrossTransport_WStoQUIC proves the reverse heterogeneous path: a real
// WebSocket client dispatches to a node attached over real QUIC, and the QUIC
// client receives the byte-exact envelope (WS -> gateway -> QUIC). Together with
// TestCrossTransport_QUICtoWS this proves bidirectional protocol bridging.
func TestCrossTransport_WStoQUIC(t *testing.T) {
	gw := New()
	defer gw.Close()

	wst := NewWebSocketTransport(gw)
	srv := httptest.NewServer(wst.Handler())
	defer srv.Close()
	wsBase := wsURL(srv.URL, "/edge")

	srvCfg, cliCfg := quicTestConfigs(t)
	qt, err := NewQUICTransport(gw, "127.0.0.1:0", srvCfg)
	if err != nil {
		t.Fatalf("start quic transport: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// QUIC-attached target node.
	target, err := DialQUIC(ctx, qt.Addr(), "node-Q", cliCfg)
	if err != nil {
		t.Fatalf("dial QUIC target node-Q: %v", err)
	}
	defer target.Close()

	// WS-attached sender.
	sender, err := DialWS(ctx, wsBase, "scheduler")
	if err != nil {
		t.Fatalf("dial WS scheduler: %v", err)
	}
	defer sender.Close()

	waitForRoutes(t, gw, map[string]int{"node-Q": 1, "scheduler": 1})

	payload := json.RawMessage(`{"task":"scan","range":"0-1024","sig":"ÿþ"}`)
	dispatch := Envelope{
		ID:       "xt-ws-quic-1",
		Kind:     KindDispatch,
		Source:   "scheduler",
		Dest:     "node-Q",
		Payload:  payload,
		IssuedAt: time.Now().UTC(),
	}
	if err := sender.Send(dispatch); err != nil {
		t.Fatalf("WS sender.Send: %v", err)
	}

	got, err := target.Recv(8 * time.Second)
	if err != nil {
		t.Fatalf("node-Q did not receive WS-originated dispatch over QUIC: %v", err)
	}
	if got.ID != "xt-ws-quic-1" || got.Dest != "node-Q" {
		t.Fatalf("routing error: got id=%q dest=%q", got.ID, got.Dest)
	}
	if !bytes.Equal(got.Payload, payload) {
		t.Fatalf("payload not byte-equal across WS->QUIC: got %q want %q", got.Payload, payload)
	}
	t.Logf("CROSS-TRANSPORT OK: WS->gateway->QUIC delivered id=%s dest=%s byte-exact payload=%s",
		got.ID, got.Dest, string(got.Payload))
}

// TestQUICFraming unit-tests the QUIC adapter's length-prefixed envelope codec
// in isolation (no sockets), so a regression in framing is caught without the
// full integration stack.
func TestQUICFraming(t *testing.T) {
	env := Envelope{
		ID:       "frame-1",
		Kind:     KindDispatch,
		Dest:     "node-X",
		Payload:  json.RawMessage(`{"k":"v","b":""}`),
		IssuedAt: time.Now().UTC(),
	}
	var buf bytes.Buffer
	if err := writeFrame(&buf, env); err != nil {
		t.Fatalf("writeFrame: %v", err)
	}
	got, err := readFrame(&buf)
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if got.ID != env.ID || got.Dest != env.Dest || !bytes.Equal(got.Payload, env.Payload) {
		t.Fatalf("frame round-trip mismatch: got %+v want %+v", got, env)
	}

	// Hello frame round-trips through the raw codec.
	hb := encodeHello("node-Y")
	node, err := decodeHello(hb)
	if err != nil || node != "node-Y" {
		t.Fatalf("hello round-trip: node=%q err=%v", node, err)
	}
}
