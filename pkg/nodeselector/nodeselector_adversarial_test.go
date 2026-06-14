package nodeselector

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
)

// advRunID returns a random hex run identifier so each adversarial run is
// traceable in logs (CLAUDE-1 observability). Named distinctly from the helper
// in nodeselector_test.go to avoid redeclaration within the package.
func advRunID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return hex.EncodeToString(b[:])
}

// ---------------------------------------------------------------------------
// CENTERPIECE 1: AND-not-OR across MULTIPLE required labels.
//
// The existing suite only exercises a SINGLE required label. That cannot catch
// an OR-collapse in the RequireLabels loop (e.g. a `return true` on first
// satisfied key instead of `return false` on first unsatisfied key): with one
// label, OR and AND are indistinguishable. Here we require TWO labels and prove
// that satisfying only ONE of them is NOT enough — the node MUST be excluded.
// This is the scheduling-gate fail-open probe for labels.
// ---------------------------------------------------------------------------

func TestMatches_MultiLabel_AllMustHold_NotAnyMatch(t *testing.T) {
	id := advRunID(t)
	sel := NodeSelector{
		RequireLabels: map[string]string{"zone": "us-east", "tier": "gold"},
	}

	cases := []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		// Both required labels present and correct -> match.
		{"both-correct", map[string]string{"zone": "us-east", "tier": "gold"}, true},
		// Only zone correct, tier wrong value -> MUST be excluded (AND, not OR).
		{"only-zone-tier-wrong", map[string]string{"zone": "us-east", "tier": "silver"}, false},
		// Only zone present, tier key absent -> MUST be excluded.
		{"only-zone-tier-absent", map[string]string{"zone": "us-east"}, false},
		// Only tier correct, zone wrong value -> MUST be excluded.
		{"only-tier-zone-wrong", map[string]string{"zone": "eu-west", "tier": "gold"}, false},
		// Only tier present, zone key absent -> MUST be excluded.
		{"only-tier-zone-absent", map[string]string{"tier": "gold"}, false},
		// Neither correct -> excluded.
		{"neither", map[string]string{"zone": "eu-west", "tier": "silver"}, false},
		// Both present plus an irrelevant extra label -> still matches (extras OK).
		{"both-plus-extra", map[string]string{"zone": "us-east", "tier": "gold", "rack": "r9"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := Node{GPUCount: 8, VRAMGiB: 100, Model: "H100", Labels: tc.labels}
			got := Matches(sel, node)
			t.Logf("run=%s case=%s matched=%v labels=%v", id, tc.name, got, tc.labels)
			if got != tc.want {
				t.Fatalf("run=%s case %q: Matches=%v want=%v (multi-label must be conjunctive; satisfying only some required labels must NOT match)",
					id, tc.name, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CENTERPIECE 2: every entry in ExcludeModels is load-bearing (OR over the
// blacklist). The existing suite excludes a single model, so a bug that only
// checks the FIRST excluded entry (or breaks out early) would not be caught.
// Here the blacklist has THREE entries and we prove a node matching ANY of them
// (including the last) is rejected, while a non-listed model passes.
// ---------------------------------------------------------------------------

func TestMatches_ExcludeModels_EveryEntryRejects(t *testing.T) {
	id := advRunID(t)
	sel := NodeSelector{ExcludeModels: []string{"A100", "V100", "T4"}}

	cases := []struct {
		name  string
		model string
		want  bool // true => should match (i.e. NOT excluded)
	}{
		{"first-excluded", "A100", false},
		{"middle-excluded", "V100", false},
		{"last-excluded", "T4", false}, // bites an early-break / first-only check
		{"not-listed", "H100", true},
		{"empty-model-not-listed", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := Node{GPUCount: 8, VRAMGiB: 100, Model: tc.model}
			got := Matches(sel, node)
			t.Logf("run=%s case=%s model=%q matched=%v", id, tc.name, tc.model, got)
			if got != tc.want {
				t.Fatalf("run=%s case %q (model=%q): Matches=%v want=%v (every ExcludeModels entry must reject)",
					id, tc.name, tc.model, got, tc.want)
			}
		})
	}
}

// ExcludeModels matching is case-sensitive exact equality: a differently-cased
// model is NOT excluded. This pins the documented semantics (== on Model) so a
// future "normalize to lower" change is a conscious, tested decision rather than
// a silent behavior drift that could either fail-open or fail-closed.
func TestMatches_ExcludeModels_CaseSensitive(t *testing.T) {
	id := advRunID(t)
	sel := NodeSelector{ExcludeModels: []string{"A100"}}

	// Lowercase "a100" does not equal "A100" -> not excluded -> matches.
	lower := Node{GPUCount: 8, VRAMGiB: 100, Model: "a100"}
	if !Matches(sel, lower) {
		t.Fatalf("run=%s lowercase model %q must NOT be excluded by %q (exact-equality contract)", id, "a100", "A100")
	}
	// Exact case is excluded.
	exact := Node{GPUCount: 8, VRAMGiB: 100, Model: "A100"}
	if Matches(sel, exact) {
		t.Fatalf("run=%s exact-case model %q must be excluded", id, "A100")
	}
	t.Logf("run=%s case-sensitivity pinned: a100 matches, A100 excluded", id)
}

// ---------------------------------------------------------------------------
// CENTERPIECE 3: numeric boundary / off-by-one for both numeric constraints.
// The comparison is `node.X < sel.MinX` -> reject; i.e. >= passes. We pin the
// exact boundary triplet (min-1 fails, min passes, min+1 passes) for BOTH
// GPUCount and VRAMGiB. An inverted comparison (> vs <), or an off-by-one
// (<= instead of <), flips the boundary case and fails here.
// ---------------------------------------------------------------------------

func TestMatches_NumericBoundaries_GPUandVRAM(t *testing.T) {
	id := advRunID(t)

	t.Run("gpu-count", func(t *testing.T) {
		sel := NodeSelector{MinGPUCount: 4}
		below := Node{GPUCount: 3, VRAMGiB: 100}
		eq := Node{GPUCount: 4, VRAMGiB: 100}
		above := Node{GPUCount: 5, VRAMGiB: 100}
		if Matches(sel, below) {
			t.Fatalf("run=%s GPUCount=3 < Min=4 must NOT match (requested-below-available rejects)", id)
		}
		if !Matches(sel, eq) {
			t.Fatalf("run=%s GPUCount=4 == Min=4 must match (boundary equality passes)", id)
		}
		if !Matches(sel, above) {
			t.Fatalf("run=%s GPUCount=5 > Min=4 must match", id)
		}
		t.Logf("run=%s gpu boundary pinned: 3 reject, 4 accept, 5 accept", id)
	})

	t.Run("vram", func(t *testing.T) {
		sel := NodeSelector{MinVRAMGiB: 80}
		below := Node{GPUCount: 8, VRAMGiB: 79}
		eq := Node{GPUCount: 8, VRAMGiB: 80}
		above := Node{GPUCount: 8, VRAMGiB: 81}
		if Matches(sel, below) {
			t.Fatalf("run=%s VRAMGiB=79 < Min=80 must NOT match", id)
		}
		if !Matches(sel, eq) {
			t.Fatalf("run=%s VRAMGiB=80 == Min=80 must match (boundary equality passes)", id)
		}
		if !Matches(sel, above) {
			t.Fatalf("run=%s VRAMGiB=81 > Min=80 must match", id)
		}
		t.Logf("run=%s vram boundary pinned: 79 reject, 80 accept, 81 accept", id)
	})
}

// ---------------------------------------------------------------------------
// CENTERPIECE 4: simultaneous multi-dimension violation. A node that fails
// MORE THAN ONE constraint at once must still be excluded, and the FULL-stack
// conjunction must accept only when EVERY dimension is satisfied. This guards
// against any reordering/short-circuit bug that could let a partly-satisfying
// node through.
// ---------------------------------------------------------------------------

func TestFilter_FullConjunction_OnlyAllSatisfyingSurvives(t *testing.T) {
	id := advRunID(t)
	sel := NodeSelector{
		MinGPUCount:   2,
		MinVRAMGiB:    80,
		ExcludeModels: []string{"A100"},
		RequireLabels: map[string]string{"zone": "us-east"},
	}

	nodes := []Node{
		// Satisfies everything -> survives.
		{ID: "all-ok", GPUCount: 2, VRAMGiB: 80, Model: "H100", Labels: map[string]string{"zone": "us-east"}},
		// Fails GPU AND VRAM (two dims) -> excluded.
		{ID: "gpu+vram", GPUCount: 1, VRAMGiB: 10, Model: "H100", Labels: map[string]string{"zone": "us-east"}},
		// Fails model AND label (two dims) -> excluded.
		{ID: "model+label", GPUCount: 4, VRAMGiB: 90, Model: "A100", Labels: map[string]string{"zone": "eu-west"}},
		// Fails all four dims -> excluded.
		{ID: "all-bad", GPUCount: 0, VRAMGiB: 0, Model: "A100", Labels: map[string]string{}},
	}

	after := Filter(sel, nodes)
	t.Logf("run=%s survivors=%v", id, idsOf(after))

	if len(after) != 1 || !containsID(after, "all-ok") {
		t.Fatalf("run=%s expected only 'all-ok' to survive, got %v", id, idsOf(after))
	}
	for _, bad := range []string{"gpu+vram", "model+label", "all-bad"} {
		if containsID(after, bad) {
			t.Fatalf("run=%s multi-violation node %q must be excluded but survived (%v)", id, bad, idsOf(after))
		}
	}
}

// ---------------------------------------------------------------------------
// EDGE / NO-PANIC pinning: nil and empty inputs.
//   - nil node Labels with a RequireLabels selector -> excluded, no panic.
//   - nil ExcludeModels / nil RequireLabels in selector -> treated as empty
//     (impose no constraint), no panic.
//   - nil node slice into Filter -> non-nil empty result, no panic.
//   - empty selector matches nodes with nil Labels.
// ---------------------------------------------------------------------------

func TestMatches_NilAndEmptyInputs_NoPanic(t *testing.T) {
	id := advRunID(t)

	// Required label but node has nil Labels map -> missing key -> excluded.
	selReq := NodeSelector{RequireLabels: map[string]string{"zone": "us-east"}}
	nodeNilLabels := Node{GPUCount: 8, VRAMGiB: 100, Model: "H100", Labels: nil}
	if Matches(selReq, nodeNilLabels) {
		t.Fatalf("run=%s node with nil Labels must NOT satisfy a required label (missing key != satisfied)", id)
	}

	// Selector with nil slices/maps imposes no label/exclude constraint.
	selNilConstraints := NodeSelector{MinGPUCount: 1, ExcludeModels: nil, RequireLabels: nil}
	if !Matches(selNilConstraints, Node{GPUCount: 1, VRAMGiB: 0, Model: "anything", Labels: nil}) {
		t.Fatalf("run=%s nil ExcludeModels/RequireLabels must impose no constraint", id)
	}

	// Empty selector matches a node with nil Labels.
	if !Matches(NodeSelector{}, Node{Labels: nil}) {
		t.Fatalf("run=%s empty selector must match a node with nil Labels", id)
	}

	t.Logf("run=%s nil/empty input handling pinned without panic", id)
}

func TestFilter_NilNodeSlice_ReturnsNonNilEmpty(t *testing.T) {
	id := advRunID(t)
	out := Filter(NodeSelector{MinGPUCount: 1}, nil)
	if out == nil {
		t.Fatalf("run=%s Filter(nil) must return non-nil slice", id)
	}
	if len(out) != 0 {
		t.Fatalf("run=%s Filter(nil) must be empty, got %d", id, len(out))
	}
	t.Logf("run=%s Filter(nil) -> non-nil empty slice", id)
}

// TestFilter_PreservesInputOrder pins that survivors are returned in input
// order (documented contract), independent of which nodes are dropped between
// them.
func TestFilter_PreservesInputOrder(t *testing.T) {
	id := advRunID(t)
	sel := NodeSelector{MinGPUCount: 2}
	nodes := []Node{
		{ID: "a", GPUCount: 4},
		{ID: "drop1", GPUCount: 1},
		{ID: "b", GPUCount: 8},
		{ID: "drop2", GPUCount: 0},
		{ID: "c", GPUCount: 2},
	}
	after := Filter(sel, nodes)
	want := []string{"a", "b", "c"}
	if len(after) != len(want) {
		t.Fatalf("run=%s expected %v, got %v", id, want, idsOf(after))
	}
	for i := range want {
		if after[i].ID != want[i] {
			t.Fatalf("run=%s order not preserved: want %v got %v", id, want, idsOf(after))
		}
	}
	t.Logf("run=%s input order preserved: %v", id, idsOf(after))
}
