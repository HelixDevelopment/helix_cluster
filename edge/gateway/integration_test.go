//go:build integration

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestWebSocketRoundTripAndIsolation is the REAL integration test mandated by
// HXC-1186 / CLAUDE-1. It stands up a real WebSocket server on a real TCP
// listener (httptest.NewServer), dials THREE real WebSocket clients over real
// sockets, dispatches a work envelope from one client, and asserts:
//
//   - the addressed target client receives the byte-identical payload (round-trip);
//   - a non-addressed client receives NOTHING (routing isolation A != B);
//   - a status/heartbeat flows back the other direction over the real socket.
//
// No in-memory fakes are used on the WebSocket path — every hop is a real frame
// over a real kernel socket.
func TestWebSocketRoundTripAndIsolation(t *testing.T) {
	gw := New()
	wst := NewWebSocketTransport(gw)
	defer gw.Close()

	srv := httptest.NewServer(wst.Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	base := wsURL(srv.URL, "/edge")

	// Three real edge clients over real sockets.
	sender, err := DialWS(ctx, base, "scheduler")
	if err != nil {
		t.Fatalf("dial sender: %v", err)
	}
	defer sender.Close()

	target, err := DialWS(ctx, base, "node-A")
	if err != nil {
		t.Fatalf("dial target (node-A): %v", err)
	}
	defer target.Close()

	bystander, err := DialWS(ctx, base, "node-B")
	if err != nil {
		t.Fatalf("dial bystander (node-B): %v", err)
	}
	defer bystander.Close()

	// Give the server-side read pumps time to register all three connections.
	waitForRoutes(t, gw, map[string]int{"scheduler": 1, "node-A": 1, "node-B": 1})

	// --- 1. Work-dispatch round-trip: scheduler -> node-A ---
	payload := json.RawMessage(`{"task":"inference","model":"helix-7b","tokens":4096}`)
	dispatch := Envelope{
		ID:       "work-42",
		Kind:     KindDispatch,
		Source:   "scheduler",
		Dest:     "node-A",
		Payload:  payload,
		IssuedAt: time.Now().UTC(),
	}
	if err := sender.Send(dispatch); err != nil {
		t.Fatalf("sender.Send: %v", err)
	}

	got, err := target.Recv(5 * time.Second)
	if err != nil {
		t.Fatalf("node-A did not receive dispatch over real socket: %v", err)
	}
	// REAL-WS ROUND-TRIP assertions: correct routing + byte-equal payload.
	if got.ID != "work-42" {
		t.Fatalf("routing error: node-A got id %q want %q", got.ID, "work-42")
	}
	if got.Dest != "node-A" {
		t.Fatalf("routing error: dest %q want node-A", got.Dest)
	}
	if !bytes.Equal(got.Payload, payload) {
		t.Fatalf("payload not byte-equal over WS: got %q want %q", got.Payload, payload)
	}
	t.Logf("REAL-WS ROUND-TRIP OK: node-A received id=%s dest=%s payload=%s",
		got.ID, got.Dest, string(got.Payload))

	// --- 2. Routing isolation: node-B must NOT receive node-A's dispatch ---
	if _, err := bystander.Recv(500 * time.Millisecond); err == nil {
		t.Fatalf("ISOLATION VIOLATED: node-B received a frame addressed to node-A")
	} else {
		t.Logf("ISOLATION OK: node-B received nothing (read timed out as expected): %v", err)
	}

	// --- 3. Status/heartbeat flows back: node-A -> scheduler ---
	statusPayload := json.RawMessage(`{"state":"running","cpu":0.42}`)
	status := Envelope{
		ID:       "hb-1",
		Kind:     KindStatus,
		Source:   "node-A",
		Dest:     "scheduler",
		Payload:  statusPayload,
		IssuedAt: time.Now().UTC(),
	}
	if err := target.Send(status); err != nil {
		t.Fatalf("node-A status send: %v", err)
	}
	hb, err := sender.Recv(5 * time.Second)
	if err != nil {
		t.Fatalf("scheduler did not receive heartbeat over real socket: %v", err)
	}
	if hb.ID != "hb-1" || !bytes.Equal(hb.Payload, statusPayload) {
		t.Fatalf("heartbeat mismatch: got id=%s payload=%s", hb.ID, string(hb.Payload))
	}
	t.Logf("STATUS FAN-BACK OK: scheduler received heartbeat id=%s payload=%s",
		hb.ID, string(hb.Payload))
}

// TestWebSocketMissingNodeIDRejected proves the server rejects an upgrade with
// no node id rather than silently accepting an unroutable connection.
func TestWebSocketMissingNodeIDRejected(t *testing.T) {
	gw := New()
	wst := NewWebSocketTransport(gw)
	defer gw.Close()
	srv := httptest.NewServer(wst.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/edge")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing node id, got %d", resp.StatusCode)
	}
}
