package clusternode

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"
)

// TestRouteIsRepeatable proves the fix for the per-call endpoint leak in
// NodeCluster.Route: calling Route to the SAME destination repeatedly must keep
// delivering the correct envelope. Before the fix each Route did a fresh
// loop.Connect(dstID), appending another observer conn under dstID. The gateway
// delivers a dispatch to ALL conns registered under an id, so the Nth Route
// fanned its envelope to N conns — the first N-1 of them stale, with bounded
// (cap 16) inboxes that were never drained. The select in Route reads exactly
// one inbox (the latest conn's), so once enough stale conns accumulate the
// router returns ErrInboxFull and/or the wrong conn is observed. With the fix
// (one cached conn per src->dst pair, reused on every call) the inbox stays
// balanced and every Route delivers byte-exact.
//
// We route well past the loopback inbox capacity (16) to the same destination
// so the pre-fix accumulation would surface; the post-fix path stays green.
func TestRouteIsRepeatable(t *testing.T) {
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

	// Route the same src->dst pair many more times than the loopback inbox cap
	// (16) so any per-call conn accumulation would overflow a stale inbox.
	const routes = 50
	for i := 0; i < routes; i++ {
		payload := []byte(fmt.Sprintf("repeatable-route-%d", i))
		routeCtx, routeCancel := context.WithTimeout(ctx, 10*time.Second)
		got, err := cluster.Route(routeCtx, "node-a", "node-c", payload)
		routeCancel()
		if err != nil {
			t.Fatalf("route #%d node-a->node-c failed: %v", i, err)
		}
		if got.Dest != "node-c" {
			t.Fatalf("route #%d: delivered to wrong dest %q", i, got.Dest)
		}
		if got.Source != "node-a" {
			t.Fatalf("route #%d: wrong source %q", i, got.Source)
		}
		if !bytes.Equal(got.Payload, payload) {
			t.Fatalf("route #%d: payload not byte-exact: got %q want %q", i, string(got.Payload), string(payload))
		}
	}

	// Repeatability must hold for a DIFFERENT destination too (independent pair).
	for i := 0; i < routes; i++ {
		payload := []byte(fmt.Sprintf("repeatable-route-b-%d", i))
		routeCtx, routeCancel := context.WithTimeout(ctx, 10*time.Second)
		got, err := cluster.Route(routeCtx, "node-a", "node-b", payload)
		routeCancel()
		if err != nil {
			t.Fatalf("route #%d node-a->node-b failed: %v", i, err)
		}
		if !bytes.Equal(got.Payload, payload) {
			t.Fatalf("route #%d node-a->node-b: payload not byte-exact: got %q want %q", i, string(got.Payload), string(payload))
		}
	}

	t.Logf("Route is repeatable: %d routes to node-c and %d to node-b each delivered byte-exact", routes, routes)
}

// TestStartCleansUpOnError proves the fix for the agent leak in
// NodeCluster.Start: if Start fails partway through the cross-agent bring-up, it
// must tear down the subset it already started, leaving nothing running. We
// trigger a deterministic mid-loop failure by pre-starting one of the joiner
// agents directly (so NodeCluster.Start's own a.Start(ctx) for that agent
// returns "already started"), then assert (a) Start returns an error and (b)
// the anchor it DID start in this call has been stopped again.
func TestStartCleansUpOnError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cluster, err := NewNodeCluster("node-a", "node-b", "node-c")
	if err != nil {
		t.Fatalf("new cluster: %v", err)
	}
	t.Cleanup(func() { _ = cluster.Stop() })

	// Pre-start node-b out of band so the anchor (node-a) is the only agent
	// Start brings up before it hits node-b and fails with "already started".
	joiner := cluster.Get("node-b")
	if err := joiner.Start(ctx); err != nil {
		t.Fatalf("pre-start joiner: %v", err)
	}
	t.Cleanup(func() { _ = joiner.Stop() })

	anchor := cluster.Get("node-a")

	if err := cluster.Start(ctx); err == nil {
		t.Fatalf("Start was expected to fail (node-b pre-started) but returned nil")
	}

	// The anchor must NOT be left running: Start's error-path cleanup must have
	// stopped the subset it started in this call.
	anchor.mu.Lock()
	anchorStopped := anchor.stopped
	anchor.mu.Unlock()
	if !anchorStopped {
		t.Fatalf("Start leaked the anchor: node-a is still running after a failed Start")
	}

	t.Logf("Start cleaned up on error: the anchor it started was stopped before Start returned")
}
