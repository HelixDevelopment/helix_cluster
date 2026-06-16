package node

// Adversarial SECOND pass for internal/node — node agent registry/server.
//
// The first pass (node_adversarial_test.go) covered concurrency conservation,
// identity/clobber, stale-node verdicts and health lifecycle. This pass goes
// deeper into ISOLATION/FIDELITY of the registry and server-reported liveness:
//
//   (A) REGISTRY ALIASING (REAL BUG, fixed): memRegistry stored and handed back
//       the caller's live *helixv1.Node pointer. A caller (or a second reader)
//       mutating that pointer silently corrupted the stored node. The sibling
//       DiscoveryRegistry/EtcdRegistry are isolated via serialization; memRegistry
//       was the odd one out. Fixed by deep-cloning on Register/Lookup/List. These
//       tests fail (corruption observed) on the unfixed store-side path.
//
//   (B) HEARTBEAT FIDELITY (pins): UpdateNodeStatus with empty Status is a pure
//       heartbeat that refreshes liveness without rewriting node fields; a
//       status-only update must not lose the heartbeat.
//
//   (C) LIVENESS off-by-one + re-register: StaleNodes boundary is strict-greater;
//       a re-registered node must shed its prior staleness.
//
//   (D) REGISTER STATUS CONTRACT (pin): RegisterNode forces Status="active" — a
//       documented join semantic. Pinned so a silent change is caught.
//
//   (E) LATENT RISK (documented, not failing): a node present only in a shared
//       registry (never registered through THIS Server) is visible to ListNodes
//       but invisible to StaleNodes — a liveness gap for shared backends. Pinned
//       as the current contract so a future change is noticed.
//
// ANTI-HANG: pure in-memory, no sockets, capped goroutines, fixed iterations;
// installable nowFunc clock instead of sleeps.

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
)

// ---------------------------------------------------------------------------
// (A) REGISTRY ALIASING — store-side isolation.
//
// Register must store an isolated copy. After Register, mutating the caller's
// own pointer must NOT change what the registry returns. On unfixed prod the
// registry aliased the caller pointer, so this corruption was observable.
// ---------------------------------------------------------------------------

func TestAdversarial2_MemRegistry_RegisterIsolatesCallerPointer(t *testing.T) {
	reg := newMemRegistry()
	ctx := context.Background()

	input := &helixv1.Node{
		Id:     "iso-1",
		Region: "orig",
		Labels: map[string]string{"a": "1"},
	}
	if err := reg.Register(ctx, input); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Caller keeps mutating its own pointer AFTER registration.
	input.Region = "HACKED"
	if input.Labels != nil {
		input.Labels["a"] = "999"
	}

	got, ok, err := reg.Lookup(ctx, "iso-1")
	if err != nil || !ok {
		t.Fatalf("lookup: ok=%v err=%v", ok, err)
	}
	if got.Region != "orig" {
		t.Errorf("ALIASING: registry Region corrupted by caller mutation: got %q want %q", got.Region, "orig")
	}
	if got.Labels["a"] != "1" {
		t.Errorf("ALIASING: registry Labels corrupted by caller mutation: got %q want %q", got.Labels["a"], "1")
	}
}

// (A') READ-SIDE isolation: two Lookups must not alias each other or the store.
// Mutating one returned node must not be visible to a later Lookup.
func TestAdversarial2_MemRegistry_LookupIsolatesReaders(t *testing.T) {
	reg := newMemRegistry()
	ctx := context.Background()
	if err := reg.Register(ctx, &helixv1.Node{Id: "r1", Region: "orig", Labels: map[string]string{"a": "1"}}); err != nil {
		t.Fatal(err)
	}

	g1, _, _ := reg.Lookup(ctx, "r1")
	g1.Region = "MUT"
	if g1.Labels != nil {
		g1.Labels["a"] = "999"
	}

	g2, ok, _ := reg.Lookup(ctx, "r1")
	if !ok {
		t.Fatal("second lookup missing")
	}
	if g2.Region != "orig" {
		t.Errorf("READ-ALIAS: second reader saw mutation, Region=%q want %q", g2.Region, "orig")
	}
	if g2.Labels["a"] != "1" {
		t.Errorf("READ-ALIAS: second reader saw label mutation, a=%q want %q", g2.Labels["a"], "1")
	}
}

// (A'') List entries must be isolated from the store too.
func TestAdversarial2_MemRegistry_ListIsolatesStore(t *testing.T) {
	reg := newMemRegistry()
	ctx := context.Background()
	if err := reg.Register(ctx, &helixv1.Node{Id: "l1", Region: "orig"}); err != nil {
		t.Fatal(err)
	}
	all, err := reg.List(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("list: n=%d err=%v", len(all), err)
	}
	all[0].Region = "MUT"

	got, _, _ := reg.Lookup(ctx, "l1")
	if got.Region != "orig" {
		t.Errorf("LIST-ALIAS: mutating List() result corrupted store, Region=%q want %q", got.Region, "orig")
	}
}

// (A''') End-to-end via the Server: GetNode result mutation must not leak into
// a subsequent GetNode. This is the scheduler-visible path.
func TestAdversarial2_Server_GetNodeResultIsolated(t *testing.T) {
	srv := NewServer()
	ctx := context.Background()
	if _, err := srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{
		Node: &helixv1.Node{Id: "g1", Region: "eu", Labels: map[string]string{"k": "v"}},
	}); err != nil {
		t.Fatal(err)
	}
	a, err := srv.GetNode(ctx, &helixv1.GetNodeRequest{NodeId: "g1"})
	if err != nil {
		t.Fatal(err)
	}
	a.Region = "TAMPERED"
	if a.Labels != nil {
		a.Labels["k"] = "TAMPERED"
	}
	b, err := srv.GetNode(ctx, &helixv1.GetNodeRequest{NodeId: "g1"})
	if err != nil {
		t.Fatal(err)
	}
	if b.Region != "eu" || b.Labels["k"] != "v" {
		t.Errorf("SERVER-ALIAS: GetNode result aliases store; second read Region=%q Labels[k]=%q want eu/v",
			b.Region, b.Labels["k"])
	}
}

// ---------------------------------------------------------------------------
// (B) HEARTBEAT FIDELITY: a pure heartbeat (empty Status) refreshes liveness
// and does NOT clobber the stored node's existing status/region.
// ---------------------------------------------------------------------------

func TestAdversarial2_PureHeartbeat_RefreshesLivenessNoFieldClobber(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	orig := nowFunc
	defer func() { nowFunc = orig }()

	srv := NewServer()
	ctx := context.Background()

	nowFunc = func() time.Time { return base }
	if _, err := srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{
		Node: &helixv1.Node{Id: "hb", Region: "eu"},
	}); err != nil {
		t.Fatal(err)
	}

	// Advance 100s and send a PURE heartbeat (no Status, no Health).
	nowFunc = func() time.Time { return base.Add(100 * time.Second) }
	out, err := srv.UpdateNodeStatus(ctx, &helixv1.UpdateNodeStatusRequest{NodeId: "hb"})
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	// Field fidelity: status set by RegisterNode ("active") and region preserved.
	if out.Region != "eu" {
		t.Errorf("HEARTBEAT clobbered Region: got %q want eu", out.Region)
	}
	if out.Status != "active" {
		t.Errorf("HEARTBEAT clobbered Status: got %q want active", out.Status)
	}

	// Liveness refreshed: at +120s the node is NOT stale past a 30s threshold
	// (last-seen is 100s, only 20s ago).
	last, ok := srv.LastSeen("hb")
	if !ok || !last.Equal(base.Add(100*time.Second)) {
		t.Errorf("HEARTBEAT did not refresh lastSeen: got %v ok=%v", last, ok)
	}
	nowFunc = func() time.Time { return base.Add(120 * time.Second) }
	for _, id := range srv.StaleNodes(30 * time.Second) {
		if id == "hb" {
			t.Errorf("HEARTBEAT fail-closed: heartbeated node reported stale")
		}
	}
}

// ---------------------------------------------------------------------------
// (C) LIVENESS: re-register sheds prior staleness; boundary is strict-greater.
// ---------------------------------------------------------------------------

func TestAdversarial2_ReRegister_ShedsStaleness(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	orig := nowFunc
	defer func() { nowFunc = orig }()

	srv := NewServer()
	ctx := context.Background()

	nowFunc = func() time.Time { return base }
	if _, err := srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{Node: &helixv1.Node{Id: "rr"}}); err != nil {
		t.Fatal(err)
	}
	// Age it well past threshold.
	nowFunc = func() time.Time { return base.Add(10 * time.Minute) }
	stale := srv.StaleNodes(30 * time.Second)
	if len(stale) != 1 || stale[0] != "rr" {
		t.Fatalf("setup: expected rr stale, got %v", stale)
	}
	// Re-register at the new now; staleness must clear.
	if _, err := srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{Node: &helixv1.Node{Id: "rr"}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range srv.StaleNodes(30 * time.Second) {
		if id == "rr" {
			t.Errorf("LIVENESS: re-registered node still reported stale")
		}
	}
}

// StaleNodes boundary is strict-greater: a node whose age equals the threshold
// exactly is NOT stale; one nanosecond past IS. Pins the off-by-one edge.
func TestAdversarial2_StaleNodes_StrictGreaterBoundary(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	orig := nowFunc
	defer func() { nowFunc = orig }()

	srv := NewServer()
	ctx := context.Background()
	nowFunc = func() time.Time { return base }
	if _, err := srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{Node: &helixv1.Node{Id: "edge"}}); err != nil {
		t.Fatal(err)
	}

	const thr = 30 * time.Second
	// Exactly at threshold => not stale (now.Sub(seen) == thr, not > thr).
	nowFunc = func() time.Time { return base.Add(thr) }
	if contains(srv.StaleNodes(thr), "edge") {
		t.Errorf("BOUNDARY: node at exactly threshold reported stale (want NOT stale)")
	}
	// One ns past => stale.
	nowFunc = func() time.Time { return base.Add(thr + time.Nanosecond) }
	if !contains(srv.StaleNodes(thr), "edge") {
		t.Errorf("BOUNDARY: node 1ns past threshold NOT reported stale (detection silent)")
	}
}

// ---------------------------------------------------------------------------
// (D) PIN: RegisterNode forces Status="active" (documented join semantic).
// A caller-supplied status is intentionally overwritten. Pinned so a silent
// change to this contract surfaces in review.
// ---------------------------------------------------------------------------

func TestAdversarial2_RegisterNode_ForcesActiveStatus_PIN(t *testing.T) {
	srv := NewServer()
	ctx := context.Background()
	if _, err := srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{
		Node: &helixv1.Node{Id: "p1", Status: "draining"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := srv.GetNode(ctx, &helixv1.GetNodeRequest{NodeId: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "active" {
		t.Errorf("PIN: RegisterNode status contract changed: got %q want active", got.Status)
	}
}

// ---------------------------------------------------------------------------
// (E) LATENT RISK PIN: a node present only in a shared registry (not registered
// through THIS Server) is visible to ListNodes but invisible to StaleNodes.
// This is the CURRENT contract (StaleNodes only inspects server-tracked
// lastSeen). Pinned so a future liveness-coverage change is noticed. NOT a
// failing assertion — it documents the gap.
// ---------------------------------------------------------------------------

func TestAdversarial2_StaleNodes_SharedRegistryGap_PIN(t *testing.T) {
	reg := newMemRegistry()
	ctx := context.Background()
	// Pre-populate as a shared backend would, bypassing the Server.
	if err := reg.Register(ctx, &helixv1.Node{Id: "ghost", Region: "r"}); err != nil {
		t.Fatal(err)
	}
	srv := NewServerWithRegistry(reg)

	resp, err := srv.ListNodes(ctx, &helixv1.ListNodesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsNode(resp.Nodes, "ghost") {
		t.Fatalf("setup: ghost not listed")
	}
	// Documented current behavior: ghost was never heartbeated through THIS
	// server, so StaleNodes cannot see it (no lastSeen entry).
	if contains(srv.StaleNodes(time.Nanosecond), "ghost") {
		t.Errorf("contract change: ghost now reported stale (server gained shared-registry liveness coverage)")
	}
	if _, ok := srv.LastSeen("ghost"); ok {
		t.Errorf("contract change: ghost now tracked in lastSeen")
	}
}

// ---------------------------------------------------------------------------
// (A'''') Concurrency under -race: registers + reads + field mutation of the
// RETURNED node must not race the store now that reads are cloned.
// Run a SINGLE -race pass over this package to exercise it.
// ---------------------------------------------------------------------------

func TestAdversarial2_ConcurrentReadMutate_NoStoreRace(t *testing.T) {
	srv := NewServer()
	ctx := context.Background()
	const n = 6
	for i := 0; i < n; i++ {
		if _, err := srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{
			Node: &helixv1.Node{Id: fmt.Sprintf("c-%d", i), Region: "eu", Labels: map[string]string{"k": "v"}},
		}); err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup
	const iters = 200
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				id := fmt.Sprintf("c-%d", (i+seed)%n)
				if node, err := srv.GetNode(ctx, &helixv1.GetNodeRequest{NodeId: id}); err == nil {
					// Mutate the RETURNED node aggressively; with isolation this
					// can never touch the store or another reader.
					node.Region = fmt.Sprintf("z-%d", i)
					if node.Labels != nil {
						node.Labels["k"] = fmt.Sprintf("m-%d", i)
					}
				}
			}
		}(w)
	}
	wg.Wait()

	// Store fidelity preserved: every node still reads back its original values.
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("c-%d", i)
		got, err := srv.GetNode(ctx, &helixv1.GetNodeRequest{NodeId: id})
		if err != nil {
			t.Fatalf("final get %s: %v", id, err)
		}
		if got.Region != "eu" || got.Labels["k"] != "v" {
			t.Errorf("STORE CORRUPTED via returned-pointer mutation: %s Region=%q Labels[k]=%q want eu/v",
				id, got.Region, got.Labels["k"])
		}
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func containsNode(ns []*helixv1.Node, id string) bool {
	for _, n := range ns {
		if n.Id == id {
			return true
		}
	}
	return false
}
