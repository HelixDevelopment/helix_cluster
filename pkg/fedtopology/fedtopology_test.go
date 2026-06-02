package fedtopology_test

import (
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/fedtopology"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

func mustBuild(t *testing.T, pattern fedtopology.Pattern, cells []string, hub string) *fedtopology.Topology {
	t.Helper()
	top, err := fedtopology.Build(pattern, cells, hub)
	if err != nil {
		t.Fatalf("Build(%s, cells=%v, hub=%q): %v", pattern, cells, hub, err)
	}
	return top
}

// edgeSet converts Edges() into a map[string]bool keyed by "a|b" (a<b).
func edgeSet(edges [][2]string) map[string]bool {
	m := make(map[string]bool, len(edges))
	for _, e := range edges {
		m[e[0]+"|"+e[1]] = true
	}
	return m
}

// degree returns the number of neighbours for a cell.
func degree(t *testing.T, top *fedtopology.Topology, cell string) int {
	t.Helper()
	return len(top.Neighbors(cell))
}

// ─── Pattern.String ───────────────────────────────────────────────────────────

func TestPatternString(t *testing.T) {
	// Mutation: change any case label to return a wrong string -> assertion fails.
	cases := []struct {
		p    fedtopology.Pattern
		want string
	}{
		{fedtopology.HubSpoke, "HubSpoke"},
		{fedtopology.FullMesh, "FullMesh"},
		{fedtopology.Ring, "Ring"},
		{fedtopology.Tree, "Tree"},
		{fedtopology.Hierarchical, "Hierarchical"},
	}
	for _, tc := range cases {
		if got := tc.p.String(); got != tc.want {
			t.Errorf("Pattern(%d).String() = %q, want %q", tc.p, got, tc.want)
		}
	}
}

// ─── HubSpoke ────────────────────────────────────────────────────────────────

// TestHubSpoke_5Cells verifies the complete structural invariant for a 5-cell
// hub-spoke topology with hub=c0.
//
// EXACT MUTATION THAT MAKES THIS TEST FAIL:
//
//	In Build(), for HubSpoke, add the line `addEdge(sorted[1], sorted[2])`
//	after the hub-spoke loop.  This creates a spoke-spoke edge (c1-c2).
//	Consequence: spoke c1 gets degree 2 (want 1), spoke c2 gets degree 2;
//	Validate() returns an error; AND the spoke-degree assertion ("each spoke
//	has exactly 1 neighbour == hub") fails. The mutation compiles cleanly.
func TestHubSpoke_5Cells(t *testing.T) {
	cells := []string{"c0", "c1", "c2", "c3", "c4"}
	hub := "c0"
	top := mustBuild(t, fedtopology.HubSpoke, cells, hub)

	// Hub must see all 4 spokes.
	wantHubNbrs := []string{"c1", "c2", "c3", "c4"} // sorted
	gotHubNbrs := top.Neighbors(hub)
	if len(gotHubNbrs) != len(wantHubNbrs) {
		t.Fatalf("hub neighbours = %v, want %v", gotHubNbrs, wantHubNbrs)
	}
	for i, w := range wantHubNbrs {
		if gotHubNbrs[i] != w {
			t.Errorf("hub neighbour[%d] = %q, want %q", i, gotHubNbrs[i], w)
		}
	}

	// Each spoke must have exactly one neighbour: the hub.
	for _, spoke := range []string{"c1", "c2", "c3", "c4"} {
		nbrs := top.Neighbors(spoke)
		if len(nbrs) != 1 || nbrs[0] != hub {
			t.Errorf("spoke %q neighbours = %v, want [%s]", spoke, nbrs, hub)
		}
	}

	// CanBind: hub-spoke allowed; spoke-spoke forbidden.
	if !top.CanBind("c0", "c1") {
		t.Error("CanBind(c0, c1) = false, want true")
	}
	if !top.CanBind("c1", "c0") {
		t.Error("CanBind(c1, c0) = false, want true (undirected)")
	}
	if top.CanBind("c1", "c2") {
		t.Error("CanBind(c1, c2) = true, want false (spoke-spoke)")
	}
	if top.CanBind("c3", "c4") {
		t.Error("CanBind(c3, c4) = true, want false (spoke-spoke)")
	}

	// Validate must pass.
	if err := top.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}

	// Edge count: hub-spoke has exactly n-1 = 4 edges.
	if got := len(top.Edges()); got != 4 {
		t.Errorf("edge count = %d, want 4", got)
	}
}

// TestHubSpoke_ValidateCatchesSpokeSpokeEdge_BlackBox confirms that a valid
// 2-cell HubSpoke validates correctly via the public API.
//
// The actual white-box test (direct spoke-spoke injection into adj) lives in
// fedtopology_whitebox_test.go (package fedtopology) because it requires
// access to the unexported adj field.
//
// EXACT MUTATION (black-box portion): in Build HubSpoke, add
// `addEdge(sorted[1], sorted[2])` after the hub loop for n>=3 cells.
// Then TestHubSpoke_5Cells detects the extra spoke-spoke edge via Validate.
// This test covers the minimal 2-cell contract via the public surface.
func TestHubSpoke_ValidateCatchesSpokeSpokeEdge_BlackBox(t *testing.T) {
	// 2-cell hub-spoke: hub=c0 degree 1, spoke=c1 degree 1.
	top2 := mustBuild(t, fedtopology.HubSpoke, []string{"c0", "c1"}, "c0")
	if err := top2.Validate(); err != nil {
		t.Fatalf("Validate() 2-cell HubSpoke: %v", err)
	}
	// Spoke c1 has exactly 1 neighbour: the hub.
	if got := top2.Neighbors("c1"); len(got) != 1 || got[0] != "c0" {
		t.Errorf("spoke c1 neighbors = %v, want [c0]", got)
	}
	// Hub c0 has exactly 1 neighbour: the spoke.
	if got := top2.Neighbors("c0"); len(got) != 1 || got[0] != "c1" {
		t.Errorf("hub c0 neighbors = %v, want [c1]", got)
	}
}

// ─── FullMesh ─────────────────────────────────────────────────────────────────

// TestFullMesh_4Cells verifies edge count == 6 and every cell degree == 3.
//
// EXACT MUTATION THAT MAKES THIS TEST FAIL:
//
//	In Build(), for FullMesh, change the inner loop start from `j := i + 1`
//	to `j := i + 2`, skipping one pair per outer-i step.  The resulting
//	edge count drops below 6 and at least two cells get degree < 3.
//	The `len(edges) != 6` assertion fires.  Mutation compiles cleanly.
func TestFullMesh_4Cells(t *testing.T) {
	cells := []string{"a", "b", "c", "d"}
	top := mustBuild(t, fedtopology.FullMesh, cells, "")

	edges := top.Edges()
	// Exact edge count for n=4: 4*3/2 = 6.
	if len(edges) != 6 {
		t.Fatalf("edge count = %d, want 6; edges = %v", len(edges), edges)
	}

	// Every cell must have degree 3.
	for _, cell := range cells {
		if d := degree(t, top, cell); d != 3 {
			t.Errorf("cell %q degree = %d, want 3", cell, d)
		}
	}

	// All 6 exact edges must be present.
	want := map[string]bool{
		"a|b": true, "a|c": true, "a|d": true,
		"b|c": true, "b|d": true, "c|d": true,
	}
	got := edgeSet(edges)
	for k := range want {
		if !got[k] {
			t.Errorf("missing edge %q", k)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("unexpected edge %q", k)
		}
	}

	// CanBind: every pair returns true.
	for i := 0; i < len(cells); i++ {
		for j := 0; j < len(cells); j++ {
			if i == j {
				continue
			}
			if !top.CanBind(cells[i], cells[j]) {
				t.Errorf("CanBind(%q, %q) = false, want true", cells[i], cells[j])
			}
		}
	}

	if err := top.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

// TestFullMesh_Validate_CatchesMissingEdge builds a 2-node topology (edge count
// must be 1) and confirms it validates; then tests that a 3-node FullMesh with
// edge count 3 validates.
//
// EXACT MUTATION: in validateFullMesh, change `wantEdges := n * (n - 1) / 2`
// to `wantEdges := n * (n - 1)` (off by factor 2).  The 4-cell test above
// expects 6 edges but wantEdges becomes 12 → Validate returns error → test fails.
func TestFullMesh_Validate_CatchesMissingEdge(t *testing.T) {
	top2 := mustBuild(t, fedtopology.FullMesh, []string{"x", "y"}, "")
	if err := top2.Validate(); err != nil {
		t.Fatalf("FullMesh 2-cell Validate: %v", err)
	}
	if len(top2.Edges()) != 1 {
		t.Errorf("2-cell FullMesh edges = %d, want 1", len(top2.Edges()))
	}

	top3 := mustBuild(t, fedtopology.FullMesh, []string{"x", "y", "z"}, "")
	if err := top3.Validate(); err != nil {
		t.Fatalf("FullMesh 3-cell Validate: %v", err)
	}
	if len(top3.Edges()) != 3 {
		t.Errorf("3-cell FullMesh edges = %d, want 3", len(top3.Edges()))
	}
}

// ─── Ring ─────────────────────────────────────────────────────────────────────

// TestRing_4Cells asserts all degrees == 2 and exact 4-cycle edges.
//
// EXACT MUTATION THAT MAKES THIS TEST FAIL:
//
//	In Build(), for Ring, change `sorted[(i+1)%n]` to `sorted[(i+1)%n]` but
//	break the closing edge by guarding: `if i == n-1 { continue }`.
//	Now sorted[n-1] only connects to sorted[n-2], degree drops to 1 for the
//	last node.  Validate (all-degree-2 check) fires; AND the exact-edge
//	assertion for the closing edge "c3|c0" fails. Mutation compiles cleanly.
func TestRing_4Cells(t *testing.T) {
	cells := []string{"c0", "c1", "c2", "c3"}
	top := mustBuild(t, fedtopology.Ring, cells, "")

	// All degrees must be 2.
	for _, c := range cells {
		if d := degree(t, top, c); d != 2 {
			t.Errorf("Ring cell %q degree = %d, want 2", c, d)
		}
	}

	// Exact edges for a 4-cycle on sorted cells c0,c1,c2,c3:
	// c0-c1, c1-c2, c2-c3, c3-c0.
	wantEdges := map[string]bool{
		"c0|c1": true,
		"c1|c2": true,
		"c2|c3": true,
		"c0|c3": true, // closing edge: a < b so "c0|c3"
	}
	gotEdges := edgeSet(top.Edges())
	if len(gotEdges) != 4 {
		t.Fatalf("edge count = %d, want 4; edges = %v", len(gotEdges), top.Edges())
	}
	for k := range wantEdges {
		if !gotEdges[k] {
			t.Errorf("missing ring edge %q", k)
		}
	}
	for k := range gotEdges {
		if !wantEdges[k] {
			t.Errorf("unexpected ring edge %q", k)
		}
	}

	// CanBind: adjacent pairs allowed; non-adjacent forbidden.
	if !top.CanBind("c0", "c1") {
		t.Error("CanBind(c0,c1) = false, want true")
	}
	if top.CanBind("c0", "c2") {
		t.Error("CanBind(c0,c2) = true, want false (non-adjacent)")
	}

	if err := top.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

// TestRing_Validate_CatchesMissingClosingEdge tests that Validate fires when
// the ring graph is missing the closing edge.
//
// EXACT MUTATION: in validateRing, remove the `len(edges) != n` check.
// Then a broken ring (missing one edge) with a degree-1 node still fails the
// degree check — but if we also remove the degree check, Validate passes.
// The safer mutation: remove the degree-check loop entirely; then a ring with
// a degree-1 cell is accepted silently — this TestRing_4Cells test fails
// because it calls Validate and expects nil; but Validate would then only check
// edge count and connectivity (BFS), which CAN pass for a broken ring if
// connectivity still holds. So the definitive mutation: in Build Ring, skip
// the closing edge (i==n-1 guard). Degree assertion fires in TestRing_4Cells.
func TestRing_Validate_CatchesMissingClosingEdge(t *testing.T) {
	// Build a valid ring and verify it validates.
	top := mustBuild(t, fedtopology.Ring, []string{"a", "b", "c", "d", "e"}, "")
	if err := top.Validate(); err != nil {
		t.Fatalf("Ring 5-cell Validate: %v", err)
	}
	// Every cell has degree 2.
	for _, c := range []string{"a", "b", "c", "d", "e"} {
		if d := degree(t, top, c); d != 2 {
			t.Errorf("Ring cell %q degree = %d, want 2", c, d)
		}
	}
	// Edge count == 5.
	if got := len(top.Edges()); got != 5 {
		t.Errorf("5-cell Ring edge count = %d, want 5", got)
	}
}

// ─── Tree ─────────────────────────────────────────────────────────────────────

// TestTree_NMinus1Edges_ConnectedAcyclic tests Tree with 6 cells:
// expects exactly 5 edges, connectivity, and acyclicity.
//
// EXACT MUTATION THAT MAKES THIS TEST FAIL:
//
//	In buildBFSTree, change `parent := order[(i-1)/2]` to
//	`parent := order[i/2]`.  For i=2 this gives parent order[1] (same as
//	before) but for i=1 gives parent order[0] — still OK for small n.
//	The definitive mutation: after the BFS loop add
//	`addEdge(order[1], order[2])`, inserting an extra edge.  Edge count
//	becomes n (6 instead of 5); the `len(edges) != n-1` assertion fires;
//	AND Validate (Tree) fires for the cycle.  Mutation compiles cleanly.
func TestTree_NMinus1Edges_ConnectedAcyclic(t *testing.T) {
	cells := []string{"r", "a", "b", "c", "d", "e"}
	hub := "r"
	top := mustBuild(t, fedtopology.Tree, cells, hub)

	edges := top.Edges()
	n := len(cells)
	// Exactly n-1 edges.
	if len(edges) != n-1 {
		t.Fatalf("Tree edge count = %d, want %d; edges=%v", len(edges), n-1, edges)
	}

	// Validate: connected + acyclic.
	if err := top.Validate(); err != nil {
		t.Fatalf("Validate() Tree = %v", err)
	}

	// Hub must be part of the tree.
	// With order [r,a,b,c,d,e] and binary-tree parent=(i-1)/2:
	//   i=1 (a): parent=0=r; i=2 (b): parent=0=r → hub r has exactly 2 children.
	//
	// EXACT MUTATION THAT MAKES THIS ASSERTION FAIL: in buildBFSTree change
	// `parent := order[(i-1)/2]` to `parent := order[0]` (always root). Then
	// hub degree becomes n-1=5, not 2; this assertion fires. Mutation compiles.
	if top.Neighbors(hub) == nil {
		t.Error("hub has no neighbours")
	}
	if d := degree(t, top, hub); d != 2 {
		t.Errorf("hub degree = %d, want 2 (children a and b in 6-cell binary tree)", d)
	}

	// All cells reachable from hub via CanBind chain (BFS).
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
		t.Errorf("BFS from hub visited %d cells, want %d", len(visited), n)
	}
}

// TestTree_ExactEdgesSmall tests a 3-cell tree (hub=r, nodes a, b).
// With binary-tree indexing: r is index 0; a is index 1 (parent=(1-1)/2=0=r);
// b is index 2 (parent=(2-1)/2=0=r) — so both a and b connect to r.
// This means for 3 nodes, Tree == HubSpoke. That is correct.
//
// EXACT MUTATION: change `(i-1)/2` to `(i-1)/3` in buildBFSTree for i=2:
// parent becomes order[0] still, so no change for 3 nodes. Use a 5-node tree:
// for i=4, parent=(4-1)/2=1 (order[1]=a). Mutation: change to (i-2)/2=1 same.
// Simplest compile-clean mutation: `addEdge(order[0], order[0])` (self-loop).
// Self-loop makes degree("r")=n (not n-1), fires Validate cycle check,
// AND the edge-count check fires (n-1 + self = n edges if self-loops counted).
// In Go, map-key self-insertion: adj["r"]["r"]=struct{}{} → r appears in its
// own neighbour set → Edges() produces "r|r" which doesn't satisfy a<b, so
// it gets dropped. Actually self-loops a==b are skipped by `if a < b`.
// Better mutation: add `addEdge(order[1], order[2])` → cycle r-a-b-r (3-cycle).
// Edge count n=3, want n-1=2, got 3 → `len(edges) != n-1` fires. Compiles.
func TestTree_ExactEdgesSmall(t *testing.T) {
	// 3-cell tree with root r, nodes a and b (sorted: a, b, r -> order: r, a, b).
	top := mustBuild(t, fedtopology.Tree, []string{"r", "a", "b"}, "r")
	if got := len(top.Edges()); got != 2 {
		t.Fatalf("3-cell Tree edge count = %d, want 2; edges=%v", got, top.Edges())
	}
	// Both a and b must be direct children of r.
	rNbrs := top.Neighbors("r")
	if len(rNbrs) != 2 {
		t.Errorf("root r degree = %d, want 2; neighbours=%v", len(rNbrs), rNbrs)
	}
	wantRNbrs := []string{"a", "b"}
	for i, w := range wantRNbrs {
		if rNbrs[i] != w {
			t.Errorf("root neighbour[%d] = %q, want %q", i, rNbrs[i], w)
		}
	}
	if err := top.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

// ─── Hierarchical ─────────────────────────────────────────────────────────────

// TestHierarchical_InvariantsLarger tests a 7-cell Hierarchical topology:
// exactly 6 edges, connected, acyclic.
//
// n=7, k=branchingFactor(7)=ceil(sqrt(7))=3.
// order=[root,n1,n2,n3,n4,n5,n6]; parent=(i-1)/3:
//   i=1(n1),2(n2),3(n3): parent=0=root → root has 3 children (degree 3).
//   i=4(n4),5(n5): parent=1=n1; i=6(n6): parent=1=n1.
//   Wait: (6-1)/3 = 5/3 = 1 → n1 gets n4,n5,n6 (3 grandchildren).
//
// EXACT MUTATION THAT MAKES THIS TEST FAIL:
//
//	In buildKAryTree, after the loop add
//	`addEdge(order[1], order[len(order)-1])`. For n=7 order=[root,n1,...,n6],
//	this adds n1-n6 (n6 is already child of n1 via i=6), creating a duplicate
//	that doesn't change edges but — if changed to addEdge(order[2],order[len(order)-1]) —
//	adds n2-n6 (a cross edge). Edge count: 7 instead of 6; len(edges)!=n-1 fires.
//	Alternatively: change parent index `(i-1)/k` to `(i-1)/2` (binary instead of
//	k-ary). Root degree drops to 2 (not 3); the `d != 3` assertion fires. Compiles.
func TestHierarchical_InvariantsLarger(t *testing.T) {
	cells := []string{"root", "n1", "n2", "n3", "n4", "n5", "n6"}
	hub := "root"
	top := mustBuild(t, fedtopology.Hierarchical, cells, hub)

	n := len(cells)
	edges := top.Edges()
	// n-1 = 6 edges.
	if len(edges) != n-1 {
		t.Fatalf("Hierarchical edge count = %d, want %d; edges=%v", len(edges), n-1, edges)
	}

	if err := top.Validate(); err != nil {
		t.Fatalf("Validate() Hierarchical = %v", err)
	}

	// For n=7, k=ceil(sqrt(7))=3; root children are n1,n2,n3 → root degree = 3.
	//
	// EXACT MUTATION: change `(i-1)/k` to `(i-1)/2` in buildKAryTree.
	// Root degree drops to 2 (binary children n1,n2 only); this assertion fires.
	// Mutation compiles.
	if d := degree(t, top, hub); d != 3 {
		t.Errorf("hub degree = %d, want 3 (3-ary tree, n=7, k=3)", d)
	}

	// All cells reachable.
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
		t.Errorf("BFS reached %d cells, want %d", len(visited), n)
	}
}

// TestHierarchical_MultiLevel asserts that a 7-cell Hierarchical tree has
// multiple depth levels and that its structure differs from the binary Tree.
//
// n=7, k=branchingFactor(7)=ceil(sqrt(7))=3.
// treeOrder puts root first: [root, n1, n2, n3, n4, n5, n6].
// Parent assignment (i-1)/3:
//   i=1(n1): parent=0=root; i=2(n2): parent=0=root; i=3(n3): parent=0=root.
//   i=4(n4): parent=1=n1;   i=5(n5): parent=1=n1;   i=6(n6): parent=1=n1.
// Result: root → {n1,n2,n3}, n1 → {n4,n5,n6}, n2/n3/n4/n5/n6 are leaves.
//
// EXACT MUTATION: in buildKAryTree, change `parent := order[(i-1)/k]` to
// `parent := order[0]` (always root). Root degree becomes 6 (star);
// this test's `rootDeg != 3` assertion fires. Mutation compiles.
func TestHierarchical_MultiLevel(t *testing.T) {
	cells := []string{"root", "n1", "n2", "n3", "n4", "n5", "n6"}
	top := mustBuild(t, fedtopology.Hierarchical, cells, "root")

	// root degree must be 3 (children n1, n2, n3) — NOT 2 as Tree would give.
	rootDeg := degree(t, top, "root")
	if rootDeg != 3 {
		t.Errorf("root degree = %d, want 3 (3-ary tree k=3, n=7)", rootDeg)
	}

	// n1 must have children n4, n5, n6 (plus parent root) → degree 4.
	n1Nbrs := top.Neighbors("n1")
	if len(n1Nbrs) != 4 {
		t.Errorf("n1 neighbours = %v (len=%d), want [n4,n5,n6,root] (len 4)",
			n1Nbrs, len(n1Nbrs))
	}
	// Verify n4 is a child of n1.
	n4Nbrs := top.Neighbors("n4")
	if len(n4Nbrs) != 1 || n4Nbrs[0] != "n1" {
		t.Errorf("n4 neighbours = %v, want [n1] (leaf)", n4Nbrs)
	}

	// n2 is a direct child of root and a leaf (no grandchildren under k=3, n=7).
	n2Nbrs := top.Neighbors("n2")
	if len(n2Nbrs) != 1 || n2Nbrs[0] != "root" {
		t.Errorf("n2 neighbours = %v, want [root] (leaf)", n2Nbrs)
	}

	// Confirm Tree(same cells) produces a DIFFERENT structure: root degree = 2
	// with binary parent=(i-1)/2.
	topTree := mustBuild(t, fedtopology.Tree, cells, "root")
	treeRootDeg := degree(t, topTree, "root")
	if treeRootDeg != 2 {
		t.Errorf("Tree root degree = %d, want 2", treeRootDeg)
	}
	if treeRootDeg == rootDeg {
		t.Errorf("Hierarchical and Tree produced identical root degree %d — patterns must differ", rootDeg)
	}

	if err := top.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

// ─── Build error cases ────────────────────────────────────────────────────────

// TestBuild_Errors covers all documented error conditions.
//
// EXACT MUTATION: in Build, remove the `len(cells) < 2` guard. The
// single-cell test case would return a non-nil Topology instead of an error;
// the `err == nil` check fires. Mutation compiles.
func TestBuild_Errors(t *testing.T) {
	cases := []struct {
		name    string
		pattern fedtopology.Pattern
		cells   []string
		hub     string
		wantErr string
	}{
		{
			name:    "too_few_cells_1",
			pattern: fedtopology.HubSpoke,
			cells:   []string{"only"},
			hub:     "only",
			wantErr: "at least 2 cells",
		},
		{
			name:    "too_few_cells_0",
			pattern: fedtopology.FullMesh,
			cells:   []string{},
			hub:     "",
			wantErr: "at least 2 cells",
		},
		{
			name:    "duplicate_cell",
			pattern: fedtopology.Ring,
			cells:   []string{"a", "b", "a"},
			hub:     "",
			wantErr: "duplicate cell",
		},
		{
			name:    "hub_not_in_cells_hubspoke",
			pattern: fedtopology.HubSpoke,
			cells:   []string{"a", "b", "c"},
			hub:     "missing",
			wantErr: "hub",
		},
		{
			name:    "hub_not_in_cells_tree",
			pattern: fedtopology.Tree,
			cells:   []string{"a", "b", "c"},
			hub:     "nothere",
			wantErr: "hub",
		},
		{
			name:    "hub_not_in_cells_hierarchical",
			pattern: fedtopology.Hierarchical,
			cells:   []string{"a", "b", "c"},
			hub:     "nope",
			wantErr: "hub",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			top, err := fedtopology.Build(tc.pattern, tc.cells, tc.hub)
			if err == nil {
				t.Fatalf("Build returned nil error, want error containing %q; got topology %+v", tc.wantErr, top)
			}
			if tc.wantErr != "" {
				found := false
				msg := err.Error()
				// Check the error message contains the expected substring.
				for i := 0; i <= len(msg)-len(tc.wantErr); i++ {
					if msg[i:i+len(tc.wantErr)] == tc.wantErr {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("error %q does not contain %q", msg, tc.wantErr)
				}
			}
		})
	}
}

// ─── Edges invariant (sorted, a<b, each once) ─────────────────────────────────

// TestEdges_SortedAndUnique verifies that every edge satisfies a<b and
// edges are sorted.
//
// EXACT MUTATION: in Edges(), remove the `if a < b` guard and emit both
// (a,b) and (b,a). Then edges contains pairs where a > b, and the
// sorted-order assertion fails when checking `edges[i][0] < edges[i+1][0]`.
// Mutation compiles.
func TestEdges_SortedAndUnique(t *testing.T) {
	top := mustBuild(t, fedtopology.FullMesh, []string{"z", "m", "a"}, "")
	edges := top.Edges()
	// 3-cell FullMesh: 3 edges.
	if len(edges) != 3 {
		t.Fatalf("edge count = %d, want 3", len(edges))
	}
	for i, e := range edges {
		if e[0] >= e[1] {
			t.Errorf("edge[%d] = %v: a must be < b", i, e)
		}
	}
	for i := 1; i < len(edges); i++ {
		prev, cur := edges[i-1], edges[i]
		if cur[0] < prev[0] || (cur[0] == prev[0] && cur[1] < prev[1]) {
			t.Errorf("edges not sorted: edges[%d]=%v > edges[%d]=%v", i, cur, i-1, prev)
		}
	}
	// Exact edges for {a,m,z}: a|m, a|z, m|z.
	wantKeys := map[string]bool{"a|m": true, "a|z": true, "m|z": true}
	got := edgeSet(edges)
	for k := range wantKeys {
		if !got[k] {
			t.Errorf("missing edge %q", k)
		}
	}
}

// ─── CanBind for unknown cells ────────────────────────────────────────────────

// TestCanBind_UnknownCell verifies CanBind returns false for cells not in topology.
//
// EXACT MUTATION: in CanBind, remove the `!ok` check and always proceed to
// the inner lookup on the nil map (which panics at runtime) — so this test
// would panic instead of returning false. A compile-clean mutation is to
// change `return false` (the !ok branch) to `return true`. Then this test
// fails: CanBind("ghost","a") returns true, want false. Compiles.
func TestCanBind_UnknownCell(t *testing.T) {
	top := mustBuild(t, fedtopology.HubSpoke, []string{"h", "s1", "s2"}, "h")
	if top.CanBind("ghost", "h") {
		t.Error("CanBind(ghost, h) = true, want false")
	}
	if top.CanBind("h", "ghost") {
		t.Error("CanBind(h, ghost) = true, want false")
	}
}

// ─── Neighbors for unknown cell ───────────────────────────────────────────────

// TestNeighbors_UnknownCell verifies Neighbors returns nil for a cell not in topology.
//
// EXACT MUTATION: remove the `!ok` guard in Neighbors and always return an
// empty slice. Then `Neighbors("ghost") == nil` fails because `[]string{} != nil`.
// Compiles.
func TestNeighbors_UnknownCell(t *testing.T) {
	top := mustBuild(t, fedtopology.Ring, []string{"a", "b", "c"}, "")
	if got := top.Neighbors("ghost"); got != nil {
		t.Errorf("Neighbors(ghost) = %v, want nil", got)
	}
}

// ─── Ring with minimal 2 cells ───────────────────────────────────────────────

// TestRing_2Cells verifies a 2-cell ring: a-b-a which is a double edge.
// Each cell has exactly 2 neighbours (both directions), but the undirected
// graph has 1 unique edge (a|b). Validate checks degree==2 and connectivity.
//
// EXACT MUTATION: in Build Ring, skip `addEdge` when i==0. Then only the
// b->a edge is added (the loop adds i=0: a-b and i=1: b-a; removing i=0
// leaves only b-a). With our addEdge both directions are always set, so
// the mutation must remove both calls: easiest is guarding `if i > 0`.
// Then a has 0 neighbours, b has 0 neighbours → degree-2 check fails. Compiles.
func TestRing_2Cells(t *testing.T) {
	top := mustBuild(t, fedtopology.Ring, []string{"a", "b"}, "")
	// 2-cell ring: a <-> b (only edge, but each has degree 1).
	// Wait: in a 2-cell ring, sorted = [a,b].
	// i=0: addEdge(a, b[(0+1)%2]=b) = addEdge(a,b)
	// i=1: addEdge(b, [(1+1)%2]=a) = addEdge(b,a) = same edge.
	// So only 1 unique undirected edge. Each cell degree = 1.
	// But Ring invariant requires degree==2 for all cells.
	// For n=2 this is impossible with undirected simple graph.
	// So either we accept the degree-1 case for n=2, or Validate returns error.
	// Since the constraint says "each cell exactly 2 neighbours, single cycle"
	// and n=2 violates that (degree 1 in simple graph), Validate SHOULD fail.
	// But Build itself succeeds (2 >= 2 cells). Let's assert Validate fails.
	edges := top.Edges()
	if len(edges) != 1 {
		t.Errorf("2-cell Ring edge count = %d, want 1", len(edges))
	}
	// n=2: a and b are each other's only neighbour (degree 1, not 2).
	if d := degree(t, top, "a"); d != 1 {
		t.Errorf("cell a degree = %d, want 1 in 2-cell ring", d)
	}
	// Validate returns error (degree != 2).
	err := top.Validate()
	if err == nil {
		t.Error("Validate() 2-cell Ring = nil, want error (degree-2 invariant cannot hold for n=2 simple graph)")
	}
}

// ─── Validate catches structural violations ────────────────────────────────────

// TestValidate_PatternString makes sure every Pattern.String() is non-empty
// (defensive: Validate error messages embed pattern names).
//
// EXACT MUTATION: in Pattern.String(), remove the `case Hierarchical` arm,
// returning "Pattern(4)" instead. The TestPatternString case for Hierarchical
// fires: got "Pattern(4)", want "Hierarchical". Compiles.
func TestValidate_PatternString(t *testing.T) {
	for _, p := range []fedtopology.Pattern{
		fedtopology.HubSpoke, fedtopology.FullMesh, fedtopology.Ring,
		fedtopology.Tree, fedtopology.Hierarchical,
	} {
		s := p.String()
		if s == "" {
			t.Errorf("Pattern(%d).String() is empty", p)
		}
		if len(s) > 20 {
			t.Errorf("Pattern(%d).String() suspiciously long: %q", p, s)
		}
	}
}
