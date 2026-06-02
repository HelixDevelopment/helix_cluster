package nodeselector

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
)

// runUUID returns a random hex run identifier so each test run is traceable in
// logs (per CLAUDE-1 observability requirement).
func runUUID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return hex.EncodeToString(b[:])
}

// containsID reports whether the result set contains a node with the given ID.
func containsID(nodes []Node, id string) bool {
	for _, n := range nodes {
		if n.ID == id {
			return true
		}
	}
	return false
}

// mixedCandidateSet builds a candidate set where each "bad-*" node violates
// EXACTLY ONE constraint of the canonical selector, plus one fully-satisfying
// node ("good"). This lets each constraint be proven independently load-bearing.
//
// Canonical selector: MinGPUCount=2, MinVRAMGiB=80, ExcludeModels=[A100],
// RequireLabels={zone:us-east}.
func mixedCandidateSet() (NodeSelector, []Node) {
	sel := NodeSelector{
		MinGPUCount:   2,
		MinVRAMGiB:    80,
		ExcludeModels: []string{"A100"},
		RequireLabels: map[string]string{"zone": "us-east"},
	}
	nodes := []Node{
		// Fully satisfies every constraint.
		{ID: "good", GPUCount: 4, VRAMGiB: 80, Model: "H100", Labels: map[string]string{"zone": "us-east"}},
		// Fails ONLY GPU count (1 < 2); everything else passes.
		{ID: "bad-gpu", GPUCount: 1, VRAMGiB: 80, Model: "H100", Labels: map[string]string{"zone": "us-east"}},
		// Fails ONLY VRAM (79 < 80); everything else passes.
		{ID: "bad-vram", GPUCount: 4, VRAMGiB: 79, Model: "H100", Labels: map[string]string{"zone": "us-east"}},
		// Fails ONLY excluded model (A100); everything else passes.
		{ID: "bad-model", GPUCount: 4, VRAMGiB: 80, Model: "A100", Labels: map[string]string{"zone": "us-east"}},
		// Fails ONLY label value (zone=eu-west); everything else passes.
		{ID: "bad-label-val", GPUCount: 4, VRAMGiB: 80, Model: "H100", Labels: map[string]string{"zone": "eu-west"}},
		// Fails ONLY label presence (no zone key); everything else passes.
		{ID: "bad-label-missing", GPUCount: 4, VRAMGiB: 80, Model: "H100", Labels: map[string]string{"rack": "r1"}},
	}
	return sel, nodes
}

// TestFilter_MixedSet_RemovesEachViolatorKeepsSatisfier covers CLOSURE clause 1:
// a real mixed candidate set is filtered so that ONLY the fully-satisfying node
// survives, and a node failing EACH individual constraint is removed.
//
// Expected survivor count is DERIVED: exactly one node ("good") satisfies all
// constraints; the other five each violate exactly one constraint.
//
// Mutation: dropping the VRAM check lets "bad-vram" survive -> after-set would
// contain it and the count would be 2, failing this test. Dropping the exclude
// check lets "bad-model" survive likewise. Dropping the GPU or label checks
// likewise leaks their violator. Every constraint is thus load-bearing here.
func TestFilter_MixedSet_RemovesEachViolatorKeepsSatisfier(t *testing.T) {
	id := runUUID(t)
	sel, before := mixedCandidateSet()

	t.Logf("run=%s phase=before candidate_count=%d ids=%v", id, len(before), idsOf(before))

	after := Filter(sel, before)

	t.Logf("run=%s phase=after survivor_count=%d ids=%v", id, len(after), idsOf(after))

	// before is the full mixed set (6 nodes).
	if len(before) != 6 {
		t.Fatalf("run=%s setup invariant broken: expected 6 candidates, got %d", id, len(before))
	}

	// Derived expectation: exactly one survivor.
	if len(after) != 1 {
		t.Fatalf("run=%s expected exactly 1 survivor, got %d (ids=%v)", id, len(after), idsOf(after))
	}

	// The single survivor must be the fully-satisfying node.
	if !containsID(after, "good") {
		t.Fatalf("run=%s expected survivor 'good' to remain, got ids=%v", id, idsOf(after))
	}

	// Each single-constraint violator MUST be removed.
	for _, badID := range []string{"bad-gpu", "bad-vram", "bad-model", "bad-label-val", "bad-label-missing"} {
		if containsID(after, badID) {
			t.Fatalf("run=%s violator %q must be filtered out but survived (ids=%v)", id, badID, idsOf(after))
		}
	}
}

// TestFilter_EmptySelectorAndUnsatisfiable covers CLOSURE clause 2:
//   - an empty (zero-value) selector matches ALL nodes;
//   - a selector that no node can satisfy returns an empty result.
//
// Mutation: if Matches wrongly rejected on a zero MinGPUCount/MinVRAMGiB (e.g.
// using > instead of an implicit >=0), the empty-selector branch would drop
// nodes and fail. If the GPU-count comparison were inverted, the
// unsatisfiable branch would return survivors and fail.
func TestFilter_EmptySelectorAndUnsatisfiable(t *testing.T) {
	id := runUUID(t)
	_, nodes := mixedCandidateSet()

	t.Logf("run=%s phase=before candidate_count=%d", id, len(nodes))

	// Empty selector: every node passes.
	empty := NodeSelector{}
	allMatch := Filter(empty, nodes)
	t.Logf("run=%s phase=after selector=empty survivor_count=%d", id, len(allMatch))
	if len(allMatch) != len(nodes) {
		t.Fatalf("run=%s empty selector must match all %d nodes, got %d", id, len(nodes), len(allMatch))
	}

	// Unsatisfiable selector: derived to exceed every candidate's GPU count
	// (max candidate GPUCount is 4, so require 9999 -> nobody qualifies).
	impossible := NodeSelector{MinGPUCount: 9999}
	none := Filter(impossible, nodes)
	t.Logf("run=%s phase=after selector=impossible survivor_count=%d", id, len(none))
	if len(none) != 0 {
		t.Fatalf("run=%s unsatisfiable selector must match 0 nodes, got %d (ids=%v)", id, len(none), idsOf(none))
	}
}

// TestMatches_EachConstraintIndependentlyLoadBearing covers CLOSURE clause 3:
// for every constraint, a node that passes everything EXCEPT that one
// constraint is rejected by Matches, while the fully-satisfying baseline is
// accepted. This isolates each constraint as individually decisive.
//
// Mutation: removing any single constraint check in Matches flips exactly one
// of these sub-cases from reject (false) to accept (true), failing the test.
func TestMatches_EachConstraintIndependentlyLoadBearing(t *testing.T) {
	id := runUUID(t)
	sel := NodeSelector{
		MinGPUCount:   2,
		MinVRAMGiB:    80,
		ExcludeModels: []string{"A100"},
		RequireLabels: map[string]string{"zone": "us-east"},
	}

	// Baseline node satisfies all constraints and MUST match.
	baseline := Node{GPUCount: 4, VRAMGiB: 80, Model: "H100", Labels: map[string]string{"zone": "us-east"}}
	t.Logf("run=%s phase=before baseline=%+v", id, baseline)
	if !Matches(sel, baseline) {
		t.Fatalf("run=%s baseline must match all constraints but did not", id)
	}

	cases := []struct {
		name string
		node Node
	}{
		{"only-gpu-fails", Node{GPUCount: 1, VRAMGiB: 80, Model: "H100", Labels: map[string]string{"zone": "us-east"}}},
		{"only-vram-fails", Node{GPUCount: 4, VRAMGiB: 79, Model: "H100", Labels: map[string]string{"zone": "us-east"}}},
		{"only-model-excluded", Node{GPUCount: 4, VRAMGiB: 80, Model: "A100", Labels: map[string]string{"zone": "us-east"}}},
		{"only-label-wrong-value", Node{GPUCount: 4, VRAMGiB: 80, Model: "H100", Labels: map[string]string{"zone": "eu-west"}}},
		{"only-label-missing", Node{GPUCount: 4, VRAMGiB: 80, Model: "H100", Labels: map[string]string{}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Matches(sel, tc.node)
			t.Logf("run=%s phase=after case=%s matched=%v node=%+v", id, tc.name, got, tc.node)
			if got {
				t.Fatalf("run=%s case %q: node violating exactly one constraint must NOT match, but did", id, tc.name)
			}
		})
	}
}

// TestFilter_ReturnsNonNilAndDoesNotAlias guards the result-slice contract:
// the output is non-nil and is a distinct allocation from the input.
//
// Mutation: returning the input slice directly (alias) would make mutating
// out[0] also mutate nodes[0], failing the alias assertion.
func TestFilter_ReturnsNonNilAndDoesNotAlias(t *testing.T) {
	id := runUUID(t)
	nodes := []Node{
		{ID: "n0", GPUCount: 1, VRAMGiB: 10, Model: "H100"},
	}
	out := Filter(NodeSelector{}, nodes)
	if out == nil {
		t.Fatalf("run=%s Filter must return non-nil slice", id)
	}
	if len(out) != 1 {
		t.Fatalf("run=%s expected 1 survivor, got %d", id, len(out))
	}
	out[0].ID = "mutated"
	if nodes[0].ID == "mutated" {
		t.Fatalf("run=%s Filter result must not alias input slice", id)
	}
	t.Logf("run=%s alias-check passed: input_id=%s output_id=%s", id, nodes[0].ID, out[0].ID)
}

// idsOf extracts node IDs for compact logging.
func idsOf(nodes []Node) []string {
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	return ids
}
