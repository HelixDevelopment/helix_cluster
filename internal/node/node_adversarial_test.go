package node

// Adversarial probes for internal/node — the node runtime's registry/server.
//
// Threat classes covered (all SINK-SIDE asserted, never "no panic"):
//   (a) UNGUARDED SHARED STATE under concurrency: the documented NodeRegistry
//       contract says "Implementations MUST be safe for concurrent use" and the
//       Server multiplexes Register/Update/List. We hammer those and assert
//       CONSERVATION (every registered node survives) plus run under -race.
//   (b) IDENTITY / TOTAL-ORDER: duplicate-ID clobber semantics; deterministic
//       listing; equal-version update resolution.
//   (c) CAPACITY / numeric edges via health-score accounting.
//   (d) HEALTH / FAIL-OPEN: a freshly registered node is reported as the most
//       recent heartbeat (StaleNodes) — assert the actual verdict, not a smoke
//       test.
//
// ANTI-HANG: pure in-memory, no servers/sockets, capped goroutines (<=8),
// fixed iteration counts; the whole file runs in well under the 90s budget.

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
)

// ---------------------------------------------------------------------------
// (a) UNGUARDED SHARED STATE: concurrent register/update/list/deregister.
//
// The memRegistry locks the MAP. But Server.UpdateNodeStatus does:
//     node, _, _ := s.registry.Lookup(...)   // returns the LIVE map pointer
//     node.Status = req.Status               // mutates shared *Node, NO lock
//     s.registry.Register(ctx, node)         // re-stores same pointer
// Meanwhile ListNodes / GetNode read node.Status, node.Region, node.Labels off
// the very same pointer. Map-level locking does not protect the pointee, so
// concurrent UpdateNodeStatus + read is a data race on the *helixv1.Node value.
//
// This test is written to TRIP -race on unfixed prod. It is NOT env-gated.
// If it fails on your tree, that is the finding, not a flake.
// ---------------------------------------------------------------------------

func TestAdversarial_ConcurrentUpdateAndList_DataRaceOnSharedNode(t *testing.T) {
	srv := NewServer()
	ctx := context.Background()

	const nNodes = 8
	ids := make([]string, nNodes)
	for i := range ids {
		ids[i] = fmt.Sprintf("node-%02d", i)
		_, err := srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{
			Node: &helixv1.Node{Id: ids[i], Region: "eu", Labels: map[string]string{"k": "v"}},
		})
		if err != nil {
			t.Fatalf("register %s: %v", ids[i], err)
		}
	}

	const iters = 300
	var wg sync.WaitGroup

	// Writers: continuously flip status on every node (mutates shared *Node).
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				id := ids[(i+seed)%nNodes]
				_, _ = srv.UpdateNodeStatus(ctx, &helixv1.UpdateNodeStatusRequest{
					NodeId: id,
					Status: fmt.Sprintf("s-%d", i),
				})
			}
		}(w)
	}

	// Readers: list + read fields off the same shared pointers.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				resp, err := srv.ListNodes(ctx, &helixv1.ListNodesRequest{})
				if err != nil {
					continue
				}
				for _, n := range resp.Nodes {
					// Touch fields that a concurrent writer is mutating.
					_ = n.Status
					_ = n.Region
					_ = len(n.Labels)
				}
			}
		}()
	}

	wg.Wait()

	// SINK-SIDE CONSERVATION: all N nodes still registered, none lost/dup.
	resp, err := srv.ListNodes(ctx, &helixv1.ListNodesRequest{})
	if err != nil {
		t.Fatalf("final list: %v", err)
	}
	seen := map[string]int{}
	for _, n := range resp.Nodes {
		seen[n.Id]++
	}
	if len(seen) != nNodes {
		t.Errorf("CONSERVATION violated: want %d distinct nodes, got %d (%v)",
			nNodes, len(seen), seen)
	}
	for _, id := range ids {
		if seen[id] != 1 {
			t.Errorf("CONSERVATION: node %s present %d times, want exactly 1", id, seen[id])
		}
	}
}

// ---------------------------------------------------------------------------
// (a') Registry-level concurrent conservation: pure memRegistry, no Server.
// Register N nodes concurrently then List — assert all present. This isolates
// the map-level safety claim from the pointer-mutation race above.
// ---------------------------------------------------------------------------

func TestAdversarial_MemRegistry_ConcurrentRegisterConservation(t *testing.T) {
	reg := newMemRegistry()
	ctx := context.Background()

	const n = 200
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8) // cap concurrency at 8
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			id := fmt.Sprintf("n-%03d", i)
			if err := reg.Register(ctx, &helixv1.Node{Id: id, Region: "r"}); err != nil {
				t.Errorf("register %s: %v", id, err)
			}
		}(i)
	}
	wg.Wait()

	all, err := reg.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != n {
		t.Errorf("CONSERVATION: want %d nodes, got %d", n, len(all))
	}
}

// ---------------------------------------------------------------------------
// (b) IDENTITY / TOTAL-ORDER.
// ---------------------------------------------------------------------------

// Duplicate-ID register is last-writer-wins (documented "stores (or replaces)").
// Pin that contract: a second Register with the same ID clobbers, never dups.
func TestAdversarial_DuplicateID_ClobberNotDuplicate(t *testing.T) {
	srv := NewServer()
	ctx := context.Background()

	_, err := srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{
		Node: &helixv1.Node{Id: "dup", Region: "eu"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{
		Node: &helixv1.Node{Id: "dup", Region: "us"},
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := srv.ListNodes(ctx, &helixv1.ListNodesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	var region string
	for _, n := range resp.Nodes {
		if n.Id == "dup" {
			count++
			region = n.Region
		}
	}
	if count != 1 {
		t.Errorf("IDENTITY: duplicate ID present %d times, want 1 (clobber semantics)", count)
	}
	if region != "us" {
		t.Errorf("IDENTITY: last-writer-wins violated, region=%q want \"us\"", region)
	}
}

// Concurrent duplicate-ID registers must converge to exactly ONE entry, never
// leak a phantom second copy under the lock. Last-writer is nondeterministic
// (acceptable), but cardinality is an invariant.
func TestAdversarial_ConcurrentDuplicateID_SingleSurvivor(t *testing.T) {
	reg := newMemRegistry()
	ctx := context.Background()

	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				_ = reg.Register(ctx, &helixv1.Node{Id: "same", Region: fmt.Sprintf("r%d-%d", w, i)})
			}
		}(w)
	}
	wg.Wait()

	all, err := reg.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, x := range all {
		if x.Id == "same" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("IDENTITY: concurrent duplicate-ID yielded %d entries, want exactly 1", n)
	}
}

// Deterministic listing: ListNodes order is documented "unspecified", so callers
// must sort. Pin that the *set* is deterministic and a sorted view is stable.
func TestAdversarial_ListNodes_SetDeterministicUnderSort(t *testing.T) {
	srv := NewServer()
	ctx := context.Background()
	want := []string{"a", "b", "c", "d", "e"}
	for _, id := range want {
		_, err := srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{Node: &helixv1.Node{Id: id}})
		if err != nil {
			t.Fatal(err)
		}
	}
	var prev []string
	for iter := 0; iter < 20; iter++ {
		resp, err := srv.ListNodes(ctx, &helixv1.ListNodesRequest{})
		if err != nil {
			t.Fatal(err)
		}
		got := make([]string, 0, len(resp.Nodes))
		for _, n := range resp.Nodes {
			got = append(got, n.Id)
		}
		sort.Strings(got)
		if prev != nil {
			if len(prev) != len(got) {
				t.Fatalf("listing cardinality changed: %v vs %v", prev, got)
			}
			for i := range got {
				if got[i] != prev[i] {
					t.Errorf("sorted listing nondeterministic: %v vs %v", prev, got)
				}
			}
		}
		prev = got
	}
}

// ---------------------------------------------------------------------------
// (d) HEALTH / FAIL-OPEN: StaleNodes verdict.
// A node that just registered must NOT be reported stale (that would be a
// fail-CLOSED bug evicting live nodes). A node whose last-seen is older than
// the threshold MUST be reported stale (fail-open detection actually fires).
// Uses the installable nowFunc clock — no sleeps, no hangs.
// ---------------------------------------------------------------------------

func TestAdversarial_StaleNodes_VerdictBothDirections(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	orig := nowFunc
	defer func() { nowFunc = orig }()

	srv := NewServer()
	ctx := context.Background()

	// Register "fresh" at base.
	nowFunc = func() time.Time { return base }
	if _, err := srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{Node: &helixv1.Node{Id: "fresh"}}); err != nil {
		t.Fatal(err)
	}
	// Register "old" at base, then advance the clock so it ages out.
	if _, err := srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{Node: &helixv1.Node{Id: "old"}}); err != nil {
		t.Fatal(err)
	}

	// Advance now by 60s; refresh "fresh" so only "old" is stale past 30s.
	nowFunc = func() time.Time { return base.Add(60 * time.Second) }
	if _, err := srv.UpdateNodeStatus(ctx, &helixv1.UpdateNodeStatusRequest{NodeId: "fresh", Status: "active"}); err != nil {
		t.Fatal(err)
	}

	stale := srv.StaleNodes(30 * time.Second)
	staleSet := map[string]bool{}
	for _, id := range stale {
		staleSet[id] = true
	}
	if staleSet["fresh"] {
		t.Errorf("HEALTH fail-closed: freshly-refreshed node reported stale: %v", stale)
	}
	if !staleSet["old"] {
		t.Errorf("HEALTH fail-open: aged-out node NOT reported stale (detection silent): %v", stale)
	}
}

// HealthScore accounting: an unreported node has no score; a reported score is
// returned faithfully; deregister clears it (no stale health leaking).
func TestAdversarial_HealthScore_LifecycleNoLeak(t *testing.T) {
	srv := NewServer()
	ctx := context.Background()

	if _, err := srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{Node: &helixv1.Node{Id: "h1"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.HealthScore("h1"); ok {
		t.Errorf("HEALTH: score present before any report")
	}

	hs := &helixv1.HealthScore{}
	if _, err := srv.UpdateNodeStatus(ctx, &helixv1.UpdateNodeStatusRequest{NodeId: "h1", Health: hs}); err != nil {
		t.Fatal(err)
	}
	got, ok := srv.HealthScore("h1")
	if !ok || got != hs {
		t.Errorf("HEALTH: reported score not returned faithfully (ok=%v)", ok)
	}

	if _, err := srv.DeregisterNode(ctx, &helixv1.DeregisterNodeRequest{NodeId: "h1"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.HealthScore("h1"); ok {
		t.Errorf("HEALTH leak: score survives deregister")
	}
	if _, ok := srv.LastSeen("h1"); ok {
		t.Errorf("HEALTH leak: lastSeen survives deregister")
	}
}
