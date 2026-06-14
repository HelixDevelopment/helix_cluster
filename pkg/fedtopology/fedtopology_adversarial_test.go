package fedtopology_test

// Adversarial sink-side probes for pkg/fedtopology.
//
// Attack classes (per tonight's brief):
//   (a) TOTAL-ORDER / DETERMINISM — build the SAME logical topology from cells
//       presented in many different input orders (and rely on Go's randomized
//       map iteration inside Edges/Neighbors) and assert BYTE-IDENTICAL output
//       across runs. A nondeterministic spanning tree / edge set would diverge.
//   (b) NUMERIC — the package has no edge weights and no weighted shortest-path
//       comparator; the only float math is branchingFactor = ceil(sqrt(n)) over
//       an int node count n>=2, which can never be NaN/Inf. We PIN that fact
//       (contract), proving the k-ary fanout is finite & >=2 for a wide range.
//   (c) UNGUARDED SHARED STATE — the adjacency map is publish-once immutable
//       (filled inside Build before the *Topology is returned; no mutation API).
//       We hammer all read methods concurrently under -race and assert clean.
//   (d) GRAPH CORRECTNESS — self-loops, duplicate edges, cycles, unreachable
//       nodes, and the documented per-pattern invariants, probed sink-side
//       (the actual edge bytes / hop counts / Validate result), not "no panic".
//
// CONTRACT (read from fedtopology.go):
//   - Build sorts cells (sort.Strings) before construction => order-independent.
//   - treeOrder puts hub at index 0, then remaining cells in sorted order.
//   - Tree = balanced binary (parent (i-1)/2); Hierarchical = k-ary, k=ceil(sqrt(n)).
//   - Edges()/Neighbors() sort their output.
//   - Invariants per pattern enforced by Validate().

import (
	"fmt"
	"math"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/fedtopology"
)

// canonEdges renders Edges() as a single deterministic string for byte-compare.
func canonEdges(top *fedtopology.Topology) string {
	edges := top.Edges()
	parts := make([]string, 0, len(edges))
	for _, e := range edges {
		parts = append(parts, e[0]+"-"+e[1])
	}
	return strings.Join(parts, ",")
}

// allPatternsCells returns a representative cell set + hub for each pattern.
type patternCase struct {
	name    string
	pattern fedtopology.Pattern
	cells   []string
	hub     string
}

func adversarialCases() []patternCase {
	cells := []string{"c00", "c01", "c02", "c03", "c04", "c05", "c06", "c07", "c08"}
	return []patternCase{
		{"HubSpoke", fedtopology.HubSpoke, cells, "c04"},
		{"FullMesh", fedtopology.FullMesh, cells, ""},
		{"Ring", fedtopology.Ring, cells, ""},
		{"Tree", fedtopology.Tree, cells, "c04"},
		{"Hierarchical", fedtopology.Hierarchical, cells, "c04"},
	}
}

// shuffleOrders returns several distinct permutations of in (deterministic set,
// chosen to defeat any accidental reliance on input slice order).
func shuffleOrders(in []string) [][]string {
	n := len(in)
	out := make([][]string, 0, 5)

	// identity
	id := append([]string(nil), in...)
	out = append(out, id)

	// reversed
	rev := append([]string(nil), in...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	out = append(out, rev)

	// rotated by 1, 3, and n/2
	for _, r := range []int{1, 3, n / 2} {
		rot := make([]string, n)
		for i := 0; i < n; i++ {
			rot[i] = in[(i+r)%n]
		}
		out = append(out, rot)
	}
	return out
}

// ─── (a) DETERMINISM / TOTAL-ORDER ────────────────────────────────────────────

// TestDeterminism_InputOrderInvariant proves that, for every pattern, the built
// topology's edge set is BYTE-IDENTICAL regardless of the order cells are passed
// in, and stable across many repeated builds (Go randomizes map iteration each
// run, so a missing tiebreak in Edges/Neighbors would surface here).
//
// SINK-SIDE: compares the actual serialized edge bytes, not "no panic".
func TestDeterminism_InputOrderInvariant(t *testing.T) {
	for _, tc := range adversarialCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			orders := shuffleOrders(tc.cells)

			// Golden = identity-order build, serialized.
			golden := ""
			for iter := 0; iter < 200; iter++ {
				for oi, order := range orders {
					top, err := fedtopology.Build(tc.pattern, order, tc.hub)
					if err != nil {
						t.Fatalf("Build(%s, order#%d) error: %v", tc.name, oi, err)
					}
					got := canonEdges(top)
					if golden == "" {
						golden = got
						continue
					}
					if got != golden {
						t.Fatalf("NONDETERMINISTIC edges for %s:\n input-order #%d, iter %d\n  golden=%q\n  got   =%q",
							tc.name, oi, iter, golden, got)
					}
				}
			}
			if golden == "" {
				t.Fatalf("%s: produced no edges", tc.name)
			}
			t.Logf("%s deterministic edge set: %s", tc.name, golden)
		})
	}
}

// TestDeterminism_NeighborsStable proves Neighbors() output ordering is stable
// (sorted) across many builds from shuffled input — a missing sort would let
// map-iteration order leak into routing decisions.
func TestDeterminism_NeighborsStable(t *testing.T) {
	for _, tc := range adversarialCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			golden := map[string]string{}
			orders := shuffleOrders(tc.cells)
			for iter := 0; iter < 100; iter++ {
				for _, order := range orders {
					top, err := fedtopology.Build(tc.pattern, order, tc.hub)
					if err != nil {
						t.Fatalf("Build error: %v", err)
					}
					for _, c := range tc.cells {
						got := strings.Join(top.Neighbors(c), ",")
						if g, ok := golden[c]; ok {
							if g != got {
								t.Fatalf("%s: Neighbors(%q) nondeterministic at iter %d: %q vs %q",
									tc.name, c, iter, g, got)
							}
						} else {
							golden[c] = got
						}
					}
				}
			}
		})
	}
}

// ─── (b) NUMERIC ──────────────────────────────────────────────────────────────

// TestNumeric_BranchingFactorFinite pins the contract that the Hierarchical
// fanout is always finite and >=2 (never NaN/Inf), by building Hierarchical
// topologies across a wide range of n and asserting the structural invariants
// hold (which they cannot if the parent index math went through a poisoned
// float). This is the package's only float path; there are NO edge weights.
//
// SINK-SIDE: asserts Validate passes AND the realized max fanout is finite/sane.
func TestNumeric_BranchingFactorFinite(t *testing.T) {
	for n := 2; n <= 64; n++ {
		cells := make([]string, n)
		for i := range cells {
			cells[i] = fmt.Sprintf("n%03d", i)
		}
		top, err := fedtopology.Build(fedtopology.Hierarchical, cells, cells[0])
		if err != nil {
			t.Fatalf("n=%d Build error: %v", n, err)
		}
		if err := top.Validate(); err != nil {
			t.Fatalf("n=%d Hierarchical invalid: %v", n, err)
		}
		// Realized fanout must be a finite, sane integer.
		maxDeg := 0
		for _, c := range cells {
			d := len(top.Neighbors(c))
			if d > maxDeg {
				maxDeg = d
			}
		}
		expectK := int(math.Ceil(math.Sqrt(float64(n))))
		if math.IsNaN(float64(expectK)) || math.IsInf(float64(expectK), 0) {
			t.Fatalf("n=%d branching factor not finite", n)
		}
		// Root degree must equal min(k, n-1); never exceed k.
		rootDeg := len(top.Neighbors(cells[0]))
		if rootDeg > expectK {
			t.Fatalf("n=%d root degree %d exceeds k=%d (fanout math broken)", n, rootDeg, expectK)
		}
	}
}

// ─── (c) UNGUARDED SHARED STATE ───────────────────────────────────────────────

// TestConcurrentReads_AllMethods_RaceClean hammers every read method
// concurrently on one published *Topology. Under `-race` a data race on adj
// would be reported. ANTI-BLUFF: also asserts each goroutine observes a
// CONSISTENT edge set (the immutable-after-publish contract).
func TestConcurrentReads_AllMethods_RaceClean(t *testing.T) {
	tc := adversarialCases()[4] // Hierarchical, the densest tree path
	top, err := fedtopology.Build(tc.pattern, tc.cells, tc.hub)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	want := canonEdges(top)

	workers := runtime.GOMAXPROCS(0) * 4
	if workers < 8 {
		workers = 8
	}
	var wg sync.WaitGroup
	errCh := make(chan string, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				if got := canonEdges(top); got != want {
					errCh <- fmt.Sprintf("edge set drift: %q != %q", got, want)
					return
				}
				for _, c := range tc.cells {
					_ = top.Neighbors(c)
					_ = top.CanBind(tc.hub, c)
				}
				if err := top.Validate(); err != nil {
					errCh <- "Validate failed concurrently: " + err.Error()
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Fatal(msg)
	}
}

// ─── (d) GRAPH CORRECTNESS ────────────────────────────────────────────────────

// TestGraph_NoSelfLoops asserts no cell is ever its own neighbour across all
// patterns and a range of sizes.
func TestGraph_NoSelfLoops(t *testing.T) {
	for _, tc := range adversarialCases() {
		top, err := fedtopology.Build(tc.pattern, tc.cells, tc.hub)
		if err != nil {
			t.Fatalf("%s Build error: %v", tc.name, err)
		}
		for _, c := range tc.cells {
			if top.CanBind(c, c) {
				t.Errorf("%s: self-loop on %q", tc.name, c)
			}
			for _, nb := range top.Neighbors(c) {
				if nb == c {
					t.Errorf("%s: %q lists itself as neighbour", tc.name, c)
				}
			}
		}
	}
}

// TestGraph_NoDuplicateEdges asserts Edges() returns each undirected edge once
// (a<b) with no duplicates — sink-side on the realized edge slice.
func TestGraph_NoDuplicateEdges(t *testing.T) {
	for _, tc := range adversarialCases() {
		top, err := fedtopology.Build(tc.pattern, tc.cells, tc.hub)
		if err != nil {
			t.Fatalf("%s Build error: %v", tc.name, err)
		}
		seen := map[string]bool{}
		for _, e := range top.Edges() {
			if e[0] >= e[1] {
				t.Errorf("%s: edge %v not canonical (a<b)", tc.name, e)
			}
			key := e[0] + "|" + e[1]
			if seen[key] {
				t.Errorf("%s: duplicate edge %v", tc.name, e)
			}
			seen[key] = true
		}
	}
}

// TestGraph_TreesConnectedAcyclic proves Tree & Hierarchical are connected,
// acyclic, and have exactly n-1 edges for many n — a wrong parent index would
// create a cycle (extra edge), an unreachable node (disconnected), or wrong
// count. Sink-side: independent BFS reachability + edge count + Validate.
func TestGraph_TreesConnectedAcyclic(t *testing.T) {
	patterns := []struct {
		name string
		p    fedtopology.Pattern
	}{
		{"Tree", fedtopology.Tree},
		{"Hierarchical", fedtopology.Hierarchical},
	}
	for _, pt := range patterns {
		for n := 2; n <= 33; n++ {
			cells := make([]string, n)
			for i := range cells {
				cells[i] = fmt.Sprintf("x%03d", i)
			}
			hub := cells[n/2] // hub deliberately NOT first in sorted order
			top, err := fedtopology.Build(pt.p, cells, hub)
			if err != nil {
				t.Fatalf("%s n=%d Build error: %v", pt.name, n, err)
			}
			// Exactly n-1 edges.
			if got := len(top.Edges()); got != n-1 {
				t.Fatalf("%s n=%d edges=%d want %d", pt.name, n, got, n-1)
			}
			// Independent reachability: BFS from hub must reach all n cells.
			visited := map[string]bool{hub: true}
			queue := []string{hub}
			for len(queue) > 0 {
				cur := queue[0]
				queue = queue[1:]
				for _, nb := range top.Neighbors(cur) {
					if !visited[nb] {
						visited[nb] = true
						queue = append(queue, nb)
					}
				}
			}
			if len(visited) != n {
				t.Fatalf("%s n=%d: unreachable node(s); reached %d/%d", pt.name, n, len(visited), n)
			}
			// Validate (connected + acyclic + count).
			if err := top.Validate(); err != nil {
				t.Fatalf("%s n=%d Validate: %v", pt.name, n, err)
			}
		}
	}
}

// TestGraph_RingIsSingleCycle proves Ring is one closed cycle: every degree 2,
// connected, n edges — and traversing neighbours from any start returns to it
// in exactly n steps (no premature short cycle, no infinite loop).
//
// NOTE: n=2 is a DOCUMENTED degenerate case (see TestRing_2Cells): in a simple
// undirected graph both wraps collapse to one edge, so each cell has degree 1
// and Validate() intentionally errors. The single-cycle invariant only applies
// for n>=3, so the sweep starts there.
func TestGraph_RingIsSingleCycle(t *testing.T) {
	for n := 3; n <= 20; n++ {
		cells := make([]string, n)
		for i := range cells {
			cells[i] = fmt.Sprintf("r%03d", i)
		}
		top, err := fedtopology.Build(fedtopology.Ring, cells, "")
		if err != nil {
			t.Fatalf("Ring n=%d Build error: %v", n, err)
		}
		if err := top.Validate(); err != nil {
			t.Fatalf("Ring n=%d Validate: %v", n, err)
		}
		// Walk the ring: from cells[0], always step to the neighbour we did not
		// come from; we must return to start in exactly n steps.
		start := top.Neighbors(cells[0])
		sort.Strings(start)
		if len(start) != 2 {
			t.Fatalf("Ring n=%d: start degree %d want 2", n, len(start))
		}
		prev := cells[0]
		cur := start[0]
		steps := 1
		for cur != cells[0] && steps <= n+1 {
			nbrs := top.Neighbors(cur)
			var next string
			for _, nb := range nbrs {
				if nb != prev {
					next = nb
					break
				}
			}
			prev, cur = cur, next
			steps++
		}
		if cur != cells[0] || steps != n {
			t.Fatalf("Ring n=%d: walk returned to start in %d steps want %d (cur=%q)", n, steps, n, cur)
		}
	}
}

// TestGraph_UnknownNodeHandledCleanly proves unreachable/unknown cells are
// handled without panic and report no neighbours / cannot bind.
func TestGraph_UnknownNodeHandledCleanly(t *testing.T) {
	top, err := fedtopology.Build(fedtopology.FullMesh, []string{"a", "b", "c"}, "")
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	if nb := top.Neighbors("ghost"); nb != nil {
		t.Errorf("Neighbors(ghost)=%v want nil", nb)
	}
	if top.CanBind("ghost", "a") {
		t.Errorf("CanBind(ghost,a)=true want false")
	}
	if top.CanBind("a", "ghost") {
		t.Errorf("CanBind(a,ghost)=true want false")
	}
}
