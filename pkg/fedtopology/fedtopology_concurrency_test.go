package fedtopology_test

// Adversarial concurrency + correctness coverage for pkg/fedtopology.
//
// CONTRACT (verified by reading fedtopology.go):
//   - Topology.adj (the only map in the package) is populated ENTIRELY inside
//     Build() before the *Topology pointer is returned to the caller
//     (fedtopology.go: adj filled at lines 108-159, struct returned at 161).
//   - There is NO mutation API: the only methods are Neighbors, Edges, CanBind,
//     Validate (all read-only). Nothing edits adj after construction.
//   - "Membership change" is modelled by calling Build() again, producing a
//     brand-new independent *Topology — never an in-place edit of an existing
//     one. There are no external callers in the repo that mutate adj.
//
// Therefore the map is IMMUTABLE-after-construction (publish-once). A
// sync.RWMutex is NOT warranted: there is no concurrent map write to guard.
// These tests PROVE the read-only concurrency contract under -race and prove
// correctness (lookups, path/reachability, rebuild-after-membership-change).

import (
	"fmt"
	"runtime"
	"sort"
	"sync"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/fedtopology"
)

// reachable does a BFS over the public Neighbors() API and reports whether dst
// is reachable from src. This is the topology "path/route" computation done
// purely through the read API (the same adjacency the package exposes).
func reachable(top *fedtopology.Topology, src, dst string, universe []string) bool {
	if src == dst {
		return true
	}
	visited := map[string]bool{src: true}
	queue := []string{src}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, nb := range top.Neighbors(cur) {
			if nb == dst {
				return true
			}
			if !visited[nb] {
				visited[nb] = true
				queue = append(queue, nb)
			}
		}
	}
	return false
}

// hops returns the BFS shortest-path distance (edge count) from src to dst over
// the public Neighbors() API, or -1 if unreachable.
func hops(top *fedtopology.Topology, src, dst string) int {
	if src == dst {
		return 0
	}
	dist := map[string]int{src: 0}
	queue := []string{src}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, nb := range top.Neighbors(cur) {
			if _, seen := dist[nb]; seen {
				continue
			}
			dist[nb] = dist[cur] + 1
			if nb == dst {
				return dist[nb]
			}
			queue = append(queue, nb)
		}
	}
	return -1
}

// ─── Correctness: lookups, path/route, unknown-node handling ──────────────────

// TestTopologyPathCorrectness_HubSpoke proves the read API computes correct
// routes over a HubSpoke topology: every spoke-spoke path is exactly 2 hops
// (through the hub), spoke-hub is 1 hop, and an unknown node is handled cleanly
// (no panic, unreachable). ANTI-BLUFF: a wrong adjacency (e.g. a missing or
// extra edge) changes a hop count or reachability and this fails.
func TestTopologyPathCorrectness_HubSpoke(t *testing.T) {
	cells := []string{"hub", "s1", "s2", "s3", "s4"}
	top := mustBuild(t, fedtopology.HubSpoke, cells, "hub")

	if got := hops(top, "hub", "s3"); got != 1 {
		t.Fatalf("hub->s3 hops=%d want 1", got)
	}
	if got := hops(top, "s1", "s4"); got != 2 {
		t.Fatalf("s1->s4 hops=%d want 2 (through hub)", got)
	}
	if !reachable(top, "s1", "s2", cells) {
		t.Fatalf("s1->s2 should be reachable through hub")
	}
	// CanBind: spokes may bind only to hub, never to each other.
	if !top.CanBind("s1", "hub") || !top.CanBind("hub", "s1") {
		t.Fatalf("hub<->s1 must be bindable")
	}
	if top.CanBind("s1", "s2") {
		t.Fatalf("s1<->s2 must NOT be bindable in HubSpoke")
	}
	// Unknown node: clean handling, no panic.
	if top.Neighbors("ghost") != nil {
		t.Fatalf("Neighbors(unknown) must be nil")
	}
	if top.CanBind("ghost", "hub") {
		t.Fatalf("CanBind(unknown,...) must be false")
	}
	if hops(top, "s1", "ghost") != -1 {
		t.Fatalf("ghost must be unreachable")
	}
}

// TestTopologyPathCorrectness_Ring proves route distances around a ring are
// correct. In a 6-node ring (c0..c5 sorted) the antipode is 3 hops; an adjacent
// pair is 1 hop. ANTI-BLUFF: a missing closing edge breaks reachability/hops.
func TestTopologyPathCorrectness_Ring(t *testing.T) {
	cells := []string{"c0", "c1", "c2", "c3", "c4", "c5"}
	top := mustBuild(t, fedtopology.Ring, cells, "")

	if got := hops(top, "c0", "c1"); got != 1 {
		t.Fatalf("c0->c1 hops=%d want 1", got)
	}
	if got := hops(top, "c0", "c3"); got != 3 {
		t.Fatalf("c0->c3 hops=%d want 3 (antipode of 6-ring)", got)
	}
	if got := hops(top, "c0", "c5"); got != 1 {
		t.Fatalf("c0->c5 hops=%d want 1 (ring wraps)", got)
	}
	// Every node reachable from every other (single connected cycle).
	for _, a := range cells {
		for _, b := range cells {
			if !reachable(top, a, b, cells) {
				t.Fatalf("ring not connected: %s->%s unreachable", a, b)
			}
		}
	}
}

// TestTopologyRebuildOnMembershipChange proves the real "membership change"
// path: adding a node = rebuild with the larger cell set (new node reachable),
// removing a node = rebuild with the smaller set (old node gone, no panic on
// lookup). This is the package's actual update contract — a fresh *Topology per
// Build, never an in-place edit. ANTI-BLUFF: if Build dropped/duplicated a node,
// reachability or the unknown-node check below would fail.
func TestTopologyRebuildOnMembershipChange(t *testing.T) {
	// Initial membership.
	cells := []string{"hub", "a", "b"}
	top := mustBuild(t, fedtopology.HubSpoke, cells, "hub")
	if !reachable(top, "a", "b", cells) {
		t.Fatalf("a->b must be reachable initially")
	}
	if top.Neighbors("c") != nil {
		t.Fatalf("c not a member yet; Neighbors must be nil")
	}

	// Node "c" joins -> rebuild.
	cells2 := []string{"hub", "a", "b", "c"}
	top2 := mustBuild(t, fedtopology.HubSpoke, cells2, "hub")
	if top2.Neighbors("c") == nil {
		t.Fatalf("after join, c must be present")
	}
	if !reachable(top2, "a", "c", cells2) {
		t.Fatalf("after join, a->c must be reachable")
	}
	if err := top2.Validate(); err != nil {
		t.Fatalf("rebuilt topology must validate: %v", err)
	}
	// Old topology is independent and unchanged (publish-once snapshot).
	if top.Neighbors("c") != nil {
		t.Fatalf("original snapshot must NOT see c (independent maps)")
	}

	// Node "a" leaves -> rebuild without it.
	cells3 := []string{"hub", "b", "c"}
	top3 := mustBuild(t, fedtopology.HubSpoke, cells3, "hub")
	if top3.Neighbors("a") != nil {
		t.Fatalf("after leave, a must be gone")
	}
	if top3.CanBind("a", "hub") {
		t.Fatalf("after leave, a must not be bindable")
	}
	if !reachable(top3, "b", "c", cells3) {
		t.Fatalf("after leave, b->c must still be reachable")
	}
}

// ─── Adversarial concurrency: many readers on one shared immutable topology ───

// TestConcurrentReads_RaceClean is the core adversarial -race test. It hammers
// ONE shared *Topology with many concurrent goroutines, each calling every
// read-path method (Neighbors, Edges, CanBind, Validate) plus a BFS route
// computation over Neighbors(). Run with -race this asserts the documented
// read-only concurrency contract: concurrent reads of the immutable adj map are
// safe and never trip "concurrent map read and map write".
//
// WHY -race BITES HERE: every method ranges over the shared adj map. If any
// method mutated the map (it does not), or if the contract were actually
// "mutated at runtime" and a writer were added without a lock, the race
// detector would flag the concurrent map access. With the publish-once design,
// concurrent reads are clean — and this test stays green precisely because the
// map is never written after Build.
//
// WHY NO MUTEX IS ADDED: there is no concurrent write to guard (verified by
// reading the source: no Add/Remove/Update API; adj is sealed in Build). Adding
// an RWMutex here would guard nothing and is therefore unjustified.
func TestConcurrentReads_RaceClean(t *testing.T) {
	cells := make([]string, 0, 33)
	cells = append(cells, "hub")
	for i := 0; i < 32; i++ {
		cells = append(cells, fmt.Sprintf("cell-%02d", i)) // unique, deterministic
	}
	sort.Strings(cells)

	top := mustBuild(t, fedtopology.Tree, cells, "hub")
	if err := top.Validate(); err != nil {
		t.Fatalf("precondition: tree must validate: %v", err)
	}

	const goroutines = 64
	const iters = 300
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				// Concurrent reads across all read-path methods.
				src := cells[(g+i)%len(cells)]
				dst := cells[(g*7+i)%len(cells)]
				_ = top.Neighbors(src)
				_ = top.CanBind(src, dst)
				_ = top.Edges()
				_ = reachable(top, src, dst, cells)
				if i%50 == 0 {
					if err := top.Validate(); err != nil {
						t.Errorf("Validate under concurrency: %v", err)
						return
					}
				}
				runtime.Gosched()
			}
		}(g)
	}
	wg.Wait()
}

// TestConcurrentReadsWhileRebuilding_RaceClean models the live system: readers
// query a STABLE published snapshot while, concurrently, "membership changes"
// produce fresh topologies via Build. Each reader holds its own *Topology
// snapshot; the rebuild goroutine constructs independent ones. This is the
// real-world pattern for this package (swap the pointer, never edit in place),
// and -race proves it is data-race-free: distinct maps, no shared write.
//
// WHY -race BITES: if Build shared backing maps across instances (it does not —
// each call allocates a fresh adj), concurrent Build + read would race. Clean
// under -race confirms snapshot independence.
func TestConcurrentReadsWhileRebuilding_RaceClean(t *testing.T) {
	base := []string{"hub", "n1", "n2", "n3", "n4", "n5"}
	snapshot := mustBuild(t, fedtopology.HubSpoke, base, "hub")

	const readers = 32
	const builders = 8
	const iters = 200
	var wg sync.WaitGroup
	wg.Add(readers + builders)

	for r := 0; r < readers; r++ {
		go func(r int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				_ = snapshot.Neighbors("hub")
				_ = snapshot.CanBind("n1", "hub")
				_ = snapshot.Edges()
				if !reachable(snapshot, "n1", "n5", base) {
					t.Errorf("snapshot reachability broke under concurrency")
					return
				}
				runtime.Gosched()
			}
		}(r)
	}

	for b := 0; b < builders; b++ {
		go func(b int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				// Independent fresh topology per "membership change".
				cells := append([]string{}, base...)
				cells = append(cells, "join-"+string(rune('A'+b))+string(rune('0'+i%10)))
				fresh, err := fedtopology.Build(fedtopology.HubSpoke, cells, "hub")
				if err != nil {
					t.Errorf("rebuild Build failed: %v", err)
					return
				}
				_ = fresh.Neighbors("hub")
				runtime.Gosched()
			}
		}(b)
	}

	wg.Wait()
}
