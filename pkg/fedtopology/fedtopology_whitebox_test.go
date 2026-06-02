// White-box tests for fedtopology.  Being in package fedtopology (not
// fedtopology_test) they have direct access to unexported fields, which is
// required to surgically inject structural violations that the public Build API
// correctly refuses to produce.
package fedtopology

import (
	"strings"
	"testing"
)

// TestHubSpoke_ValidateCatchesSpokeSpokeEdge directly injects a spoke-spoke
// edge into a built HubSpoke topology's unexported adj map, then asserts that
// Validate returns a non-nil error whose message contains "spoke-neighbour".
//
// This test exercises the code path in validateHubSpoke at lines 279-284
// (the `for nb := range nbrs` inner loop that checks nb != t.Hub).
//
// EXACT MUTATION THAT MAKES THIS TEST FAIL: in validateHubSpoke, remove the
// entire inner loop:
//
//	for nb := range nbrs {
//	    if nb != t.Hub {
//	        errs = append(errs, ...)
//	    }
//	}
//
// With the loop removed, the injected spoke-spoke edge is invisible to Validate
// and it returns nil; this test fails because it expects a non-nil error.
// The mutation compiles cleanly (the outer spoke-degree check still runs).
func TestHubSpoke_ValidateCatchesSpokeSpokeEdge(t *testing.T) {
	// Build a valid 4-cell HubSpoke: hub=h, spokes={s1,s2,s3}.
	top, err := Build(HubSpoke, []string{"h", "s1", "s2", "s3"}, "h")
	if err != nil {
		t.Fatalf("Build HubSpoke: %v", err)
	}

	// Sanity: the freshly-built topology is valid.
	if err := top.Validate(); err != nil {
		t.Fatalf("pre-injection Validate() = %v, want nil", err)
	}

	// Directly inject a spoke-spoke edge s1<->s2 into the unexported adj map.
	// This is the structural violation that Build deliberately never produces,
	// and which only a white-box test can introduce to exercise validateHubSpoke.
	top.adj["s1"]["s2"] = struct{}{}
	top.adj["s2"]["s1"] = struct{}{}

	// Now s1 has degree 2 (hub + s2) and s2 has degree 2 (hub + s1).
	// Validate MUST detect both the degree violation and the spoke-neighbour.
	gotErr := top.Validate()
	if gotErr == nil {
		t.Fatal("Validate() = nil after spoke-spoke injection, want non-nil error")
	}

	// The error message must specifically mention "spoke-neighbour" — asserting
	// the spoke-spoke detection path ran, not just the degree check.
	if !strings.Contains(gotErr.Error(), "spoke-neighbour") {
		t.Errorf("Validate() error = %q; want message containing \"spoke-neighbour\"", gotErr.Error())
	}
}

// TestHierarchical_DiffersFromTree_9Cells verifies that for n=9 the Hierarchical
// pattern (k=3) produces a strictly different adjacency structure from Tree (k=2).
//
// n=9, k=branchingFactor(9)=ceil(sqrt(9))=3.
// Tree (k=2): root children = indices 1,2 → root degree 2.
// Hierarchical (k=3): root children = indices 1,2,3 → root degree 3.
//
// EXACT MUTATION: change the Hierarchical Build case to call buildBFSTree
// instead of buildKAryTree. Then Hierarchical root degree becomes 2 (same as
// Tree); the `hierRootDeg != 3` assertion fires. Mutation compiles.
func TestHierarchical_DiffersFromTree_9Cells(t *testing.T) {
	cells := []string{"r", "a", "b", "c", "d", "e", "f", "g", "h"}
	hub := "r"

	topH, err := Build(Hierarchical, cells, hub)
	if err != nil {
		t.Fatalf("Build Hierarchical: %v", err)
	}
	topT, err := Build(Tree, cells, hub)
	if err != nil {
		t.Fatalf("Build Tree: %v", err)
	}

	// branchingFactor(9) = ceil(sqrt(9)) = 3.
	// Hierarchical: root at index 0, children at i=1,2,3 → degree 3.
	hierRootDeg := len(topH.adj[hub])
	if hierRootDeg != 3 {
		t.Errorf("Hierarchical root degree = %d, want 3 (k=3, n=9)", hierRootDeg)
	}

	// Tree: binary, root children at i=1,2 → degree 2.
	treeRootDeg := len(topT.adj[hub])
	if treeRootDeg != 2 {
		t.Errorf("Tree root degree = %d, want 2 (k=2, n=9)", treeRootDeg)
	}

	// The two patterns MUST produce different adjacency graphs.
	if hierRootDeg == treeRootDeg {
		t.Errorf("Hierarchical and Tree root degrees are both %d — patterns are identical (regression)", hierRootDeg)
	}

	// Both must still satisfy their shared structural invariants.
	if err := topH.Validate(); err != nil {
		t.Errorf("Hierarchical Validate() = %v", err)
	}
	if err := topT.Validate(); err != nil {
		t.Errorf("Tree Validate() = %v", err)
	}
}
