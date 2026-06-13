package gateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

// TestAntiBluff_CrossTransportMisroute is the CLAUDE-1 anti-bluff anchor for the
// heterogeneous-routing integration. It proves the cross-transport assertions
// have teeth: if the gateway were to misroute a dispatch across transports
// (deliver node-A's work to node-B), the test MUST fail.
//
// The positive expectation is that a QUIC dispatch addressed to node-A reaches
// the WS node-A and ONLY node-A. To demonstrate the assertion is real, this
// test deliberately addresses the dispatch to a node that is NOT the WS
// receiver and asserts the WS receiver gets NOTHING — i.e. a routing layer that
// fanned the envelope to the wrong transport/node would be caught here.
//
// MUTATION INSTRUCTIONS (run-then-revert, evidence in anti-bluff.txt):
//
//	In gateway.go Route(), replace the unicast lookup
//	    targets := g.table.lookup(dest)
//	with a misrouting mutation that ignores dest, e.g.
//	    targets := g.table.lookup("node-A")   // BUG: ignore real dest
//	Re-run `go test -run TestAntiBluff_CrossTransportMisroute`: the WS node-A
//	receiver then WRONGLY receives the envelope addressed to "ghost-node" and
//	this test FAILS on the "MISROUTE" assertion. Revert to restore PASS.
func TestAntiBluff_CrossTransportMisroute(t *testing.T) {
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

	// WS receiver registered as node-A.
	receiver, err := DialWS(ctx, wsBase, "node-A")
	if err != nil {
		t.Fatalf("dial WS node-A: %v", err)
	}
	defer receiver.Close()

	// QUIC sender.
	sender, err := DialQUIC(ctx, qt.Addr(), "scheduler", cliCfg)
	if err != nil {
		t.Fatalf("dial QUIC scheduler: %v", err)
	}
	defer sender.Close()

	waitForRoutes(t, gw, map[string]int{"node-A": 1, "scheduler": 1})

	// Address the dispatch to a node that has NO connection. Correct routing
	// returns ErrNoRoute and delivers to nobody; a misrouting mutation that
	// ignored the destination would wrongly deliver it to the WS node-A.
	payload := json.RawMessage(`{"task":"should-not-arrive"}`)
	dispatch := Envelope{
		ID:       "antibluff-ghost",
		Kind:     KindDispatch,
		Source:   "scheduler",
		Dest:     "ghost-node",
		Payload:  payload,
		IssuedAt: time.Now().UTC(),
	}
	if err := sender.Send(dispatch); err != nil {
		t.Fatalf("QUIC sender.Send: %v", err)
	}

	// node-A (WS) MUST NOT receive an envelope addressed to ghost-node.
	if _, err := receiver.Recv(800 * time.Millisecond); err == nil {
		t.Fatalf("MISROUTE: WS node-A received a dispatch addressed to ghost-node — cross-transport routing is broken")
	} else {
		t.Logf("ANTI-BLUFF OK: WS node-A correctly received nothing for a ghost-addressed dispatch (%v)", err)
	}
}
