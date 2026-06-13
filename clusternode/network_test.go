package clusternode

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"
)

// TestRouteOverRealWebSocket is the sink-side proof (HelixConstitution CLAUDE-1
// + CLAUDE-2: real transport, not a mock) that cross-node messaging works over a
// REAL network: a real TCP socket and a real WebSocket handshake between node
// gateways, not just the in-process loopback.
//
// It brings up the 3-node cluster, starts each agent's REAL edge/gateway
// WebSocketTransport server on a loopback TCP port, then routes a message
// node-a -> node-c OVER THE REAL WEBSOCKET and asserts the user-visible
// guarantees:
//
//  1. OVER-WS DELIVERY: node-c receives the EXACT payload, byte-for-byte, off a
//     real socket (the dst-side WSClient.Recv reads it back from the wire).
//  2. REAL TCP PROOF: the route's proof carries the destination gateway's real
//     bound *net.TCPAddr on 127.0.0.1 (a genuine kernel socket) and the ws://
//     URL the source actually dialled — there is no in-process channel on this
//     path.
//  3. HANDSHAKE SURVIVAL: the route only returns a payload if the real ws://
//     dial + upgrade handshake succeeded on both hops; a failure surfaces as an
//     error, never a silent fake PASS.
//  4. ROUTING ISOLATION: a dispatch addressed to node-c is delivered ONLY to
//     node-c's sink; node-b's sink, listening concurrently on its own real
//     socket, does NOT receive it.
//
// Every wait is deadline-polling (no fixed sleeps).
func TestRouteOverRealWebSocket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cluster, err := NewNodeCluster("node-a", "node-b", "node-c")
	if err != nil {
		t.Fatalf("new cluster: %v", err)
	}
	t.Cleanup(func() { _ = cluster.Stop() })

	if err := cluster.Start(ctx); err != nil {
		t.Fatalf("start cluster: %v", err)
	}

	// Bring up every node's REAL WebSocket gateway server on a loopback TCP port.
	urls, err := cluster.StartGatewayServers(ctx)
	if err != nil {
		t.Fatalf("start gateway servers: %v", err)
	}
	for id, url := range urls {
		t.Logf("gateway WS server up: %s -> %s", id, url)
	}

	// Pre-register node-b's real sink so it is concurrently listening on its OWN
	// socket while we route to node-c. If routing leaked, node-b would receive the
	// node-c dispatch — the isolation assertion below would catch it.
	nodeB := cluster.Get("node-b")
	bSink, err := nodeB.ensureSink(ctx, "node-b")
	if err != nil {
		t.Fatalf("register node-b sink: %v", err)
	}

	// --- (1)+(2)+(3) route node-a -> node-c OVER THE REAL WEBSOCKET. ----------
	// The payload is valid JSON: the gateway wire codec carries Payload as a
	// json.RawMessage, so over a real transport it must be well-formed JSON (the
	// same contract every real WS gateway client obeys). It is delivered
	// byte-for-byte regardless of its JSON shape.
	payload := []byte(`{"msg":"over-real-websocket","from":"node-a","to":"node-c"}`)
	routeCtx, routeCancel := context.WithTimeout(ctx, 15*time.Second)
	defer routeCancel()
	got, proof, err := cluster.RouteOverNetwork(routeCtx, "node-a", "node-c", payload)
	if err != nil {
		t.Fatalf("OVER-WS FAILED: route node-a->node-c over websocket: %v", err)
	}

	// (1) byte-exact delivery off the wire.
	if got.Dest != "node-c" {
		t.Fatalf("OVER-WS FAILED: delivered to wrong dest %q", got.Dest)
	}
	if got.Source != "node-a" {
		t.Fatalf("OVER-WS FAILED: wrong source %q", got.Source)
	}
	if !bytes.Equal(got.Payload, payload) {
		t.Fatalf("OVER-WS FAILED: payload not byte-exact off the wire: got %q want %q",
			string(got.Payload), string(payload))
	}

	// (2) real-TCP proof: the destination gateway is bound to a genuine kernel
	// socket — a *net.TCPAddr on 127.0.0.1 — and the source dialled a ws:// URL.
	if !proof.Transport {
		t.Fatalf("REAL-WS PROOF FAILED: route did not report the real WebSocket transport")
	}
	if proof.TransportName != "websocket" {
		t.Fatalf("REAL-WS PROOF FAILED: transport name %q, want \"websocket\"", proof.TransportName)
	}
	tcpAddr, ok := proof.DstListenAddr.(*net.TCPAddr)
	if !ok {
		t.Fatalf("REAL-WS PROOF FAILED: dst listen addr is %T (%v), not a real *net.TCPAddr",
			proof.DstListenAddr, proof.DstListenAddr)
	}
	if !tcpAddr.IP.IsLoopback() {
		t.Fatalf("REAL-WS PROOF FAILED: dst listen addr %v is not loopback", tcpAddr)
	}
	if tcpAddr.Port == 0 {
		t.Fatalf("REAL-WS PROOF FAILED: dst listen addr has no real port: %v", tcpAddr)
	}
	if proof.DstGatewayURL == "" || proof.DstGatewayURL != urls["node-c"] {
		t.Fatalf("REAL-WS PROOF FAILED: dialled URL %q, want %q", proof.DstGatewayURL, urls["node-c"])
	}
	t.Logf("OVER-WS OK: node-a->node-c delivered byte-exact (%d bytes) over real WS to %v (url=%s)",
		len(payload), tcpAddr, proof.DstGatewayURL)

	// --- (4) ROUTING ISOLATION: node-b's sink must NOT have received it. ------
	// node-b's real socket has been listening this whole time; drain it with a
	// short deadline. A correct unicast gateway delivers the node-c dispatch only
	// to node-c, so node-b's Recv must time out (no frame), not return the frame.
	if env, err := bSink.Recv(750 * time.Millisecond); err == nil {
		t.Fatalf("ISOLATION FAILED: node-b received a dispatch addressed to node-c: id=%q dest=%q payload=%q",
			env.ID, env.Dest, string(env.Payload))
	}
	t.Logf("ISOLATION OK: node-b did NOT receive the node-c-addressed dispatch (gateway unicast held over real WS)")

	// --- repeatability over the wire: a second distinct payload also arrives. -
	payload2 := []byte(`{"msg":"second-message","seq":2}`)
	got2, _, err := cluster.RouteOverNetwork(routeCtx, "node-a", "node-c", payload2)
	if err != nil {
		t.Fatalf("OVER-WS FAILED (repeat): %v", err)
	}
	if !bytes.Equal(got2.Payload, payload2) {
		t.Fatalf("OVER-WS FAILED (repeat): payload not byte-exact: got %q want %q",
			string(got2.Payload), string(payload2))
	}
	t.Logf("OVER-WS OK (repeat): a second message also delivered byte-exact over the real socket")

	t.Logf("OVER-REAL-WEBSOCKET OK: node-a -> node-c crossed a real TCP/WS path, byte-exact, isolated, handshake-proven")
}

// TestRouteOverNetworkRequiresServer proves the over-WS path is honest about its
// preconditions: routing over the network to a node whose gateway WS server is
// NOT running fails with an error rather than silently falling back to an
// in-process shortcut and faking success.
func TestRouteOverNetworkRequiresServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cluster, err := NewNodeCluster("node-a", "node-b", "node-c")
	if err != nil {
		t.Fatalf("new cluster: %v", err)
	}
	t.Cleanup(func() { _ = cluster.Stop() })
	if err := cluster.Start(ctx); err != nil {
		t.Fatalf("start cluster: %v", err)
	}

	// No StartGatewayServers: the destination has no real WS server.
	_, _, err = cluster.RouteOverNetwork(ctx, "node-a", "node-c", []byte("x"))
	if err == nil {
		t.Fatalf("expected RouteOverNetwork to fail when dst gateway server is not running, got nil")
	}
	t.Logf("HONEST-PRECONDITION OK: over-WS route refused (no fake PASS) without a real server: %v", err)
}
