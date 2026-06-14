package gputopo

import (
	"errors"
	"fmt"
	"math/rand"
	"testing"
)

// ════════════════════════════════════════════════════════════════════════════
// Adversarial topology-selection contract tests.
//
// Centerpieces:
//   - Preference TOTALITY: the maximum-bandwidth valid pair is returned even
//     when it is NOT the first or last pair enumerated. Covers
//     NVLink-beats-PCIe and higher-NVLink-beats-lower-NVLink.
//   - Deterministic TIE-BREAK: equal-bandwidth candidate pairs resolve to the
//     SAME winner on every call and under every input ordering (fixed-seed
//     shuffles of freeGPUs).
//
// Plus: connectedness (no phantom pairs), fallback boundary, and guard edges.
// ════════════════════════════════════════════════════════════════════════════

// shuffleSeeded returns a copy of in permuted by a fixed seed (deterministic).
func shuffleSeeded(in []string, seed int64) []string {
	out := make([]string, len(in))
	copy(out, in)
	r := rand.New(rand.NewSource(seed))
	r.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// pairEq reports whether (gotA,gotB) is the unordered pair {wantA,wantB}.
func pairEq(gotA, gotB, wantA, wantB string) bool {
	return (gotA == wantA && gotB == wantB) || (gotA == wantB && gotB == wantA)
}

// ────────────────────────────────────────────────────────────────────────────
// CENTERPIECE 1 — Preference totality: best pair is NOT first/last enumerated.
//
// Enumeration order over sorted free GPUs is lexicographic on (i,j). We place
// the winning, highest-bandwidth link so it is neither the first nor the last
// candidate produced — proving the selector scans for the true maximum rather
// than returning a positional first/last element.
// ────────────────────────────────────────────────────────────────────────────

func TestAdversarial_PreferenceTotality_BestNotFirstOrLast(t *testing.T) {
	flushRecords()

	// Six GPUs g0..g5, all NVLink so the Kind preference is neutral and the
	// bandwidth comparison is the sole discriminator.
	// Enumeration order of disjoint pairs over sorted [g0..g5]:
	//   (g0,g1) first ... (g2,g3) middle ... (g4,g5) last.
	// Put the MAX bandwidth on the middle pair (g2,g3) so it is neither the
	// first nor the last candidate appended.
	topo := mustTopology(t,
		[]string{"g0", "g1", "g2", "g3", "g4", "g5"},
		[]Link{
			{A: "g0", B: "g1", Kind: NVLink, BandwidthGBps: 300}, // first enumerated
			{A: "g2", B: "g3", Kind: NVLink, BandwidthGBps: 900}, // middle — MAX
			{A: "g4", B: "g5", Kind: NVLink, BandwidthGBps: 600}, // last enumerated
		},
	)

	rec, err := SelectPair("run-total-mid", topo, []string{"g0", "g1", "g2", "g3", "g4", "g5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.BandwidthGBps != 900 {
		t.Errorf("BandwidthGBps: got %v, want 900 — max-bandwidth (middle) pair must win", rec.BandwidthGBps)
	}
	if !pairEq(rec.GPUA, rec.GPUB, "g2", "g3") {
		t.Errorf("pair: got (%s,%s), want {g2,g3}", rec.GPUA, rec.GPUB)
	}
}

// Same probe but the max is the LAST enumerated candidate — guards against an
// "early-exit / keep-first" mutation that would stop at the first pair.
func TestAdversarial_PreferenceTotality_BestIsLast(t *testing.T) {
	flushRecords()

	topo := mustTopology(t,
		[]string{"g0", "g1", "g2", "g3", "g4", "g5"},
		[]Link{
			{A: "g0", B: "g1", Kind: NVLink, BandwidthGBps: 300},
			{A: "g2", B: "g3", Kind: NVLink, BandwidthGBps: 600},
			{A: "g4", B: "g5", Kind: NVLink, BandwidthGBps: 900}, // MAX, last enumerated
		},
	)

	rec, err := SelectPair("run-total-last", topo, []string{"g0", "g1", "g2", "g3", "g4", "g5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.BandwidthGBps != 900 || !pairEq(rec.GPUA, rec.GPUB, "g4", "g5") {
		t.Errorf("got (%s,%s)@%v, want {g4,g5}@900 — last/max pair must win",
			rec.GPUA, rec.GPUB, rec.BandwidthGBps)
	}
}

// NVLink-beats-PCIe even when the PCIe link is enumerated FIRST and has a
// (locally) larger numeric bandwidth-within-its-pool position. The Kind
// preference is total: any NVLink pair outranks any PCIe pair.
func TestAdversarial_PreferenceTotality_NVLinkBeatsEarlierPCIe(t *testing.T) {
	flushRecords()

	// (a,b) PCIe is enumerated before (c,d) NVLink. NVLink must still win.
	topo := mustTopology(t,
		[]string{"a", "b", "c", "d"},
		[]Link{
			{A: "a", B: "b", Kind: PCIe, BandwidthGBps: 64},   // enumerated first
			{A: "c", B: "d", Kind: NVLink, BandwidthGBps: 50}, // lower number, but NVLink
		},
	)

	rec, err := SelectPair("run-kind-total", topo, []string{"a", "b", "c", "d"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Kind != NVLink {
		t.Errorf("Kind: got %v, want NVLink — Kind preference must be total, not bandwidth-driven across kinds", rec.Kind)
	}
	if !pairEq(rec.GPUA, rec.GPUB, "c", "d") {
		t.Errorf("pair: got (%s,%s), want {c,d}", rec.GPUA, rec.GPUB)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// CENTERPIECE 2 — Deterministic tie-break under input shuffles.
//
// Two equal-bandwidth NVLink pairs. The winner MUST be identical on every call
// and under every fixed-seed permutation of freeGPUs. The contracted winner is
// the lexicographically-first pair in enumeration order over the sorted GPU IDs
// (SelectPair sorts freeGPUs, so input order is irrelevant).
// ────────────────────────────────────────────────────────────────────────────

func TestAdversarial_DeterministicTieBreak_AcrossInputOrderings(t *testing.T) {
	flushRecords()

	// Equal-bandwidth (777) NVLink pairs {a,d} and {b,c}.
	// Sorted IDs: [a b c d]. Enumeration: (a,b)nolink,(a,c)nolink,(a,d)LINK,
	// (b,c)LINK,(b,d)nolink,(c,d)nolink. First eligible candidate = {a,d}.
	topo := mustTopology(t,
		[]string{"a", "b", "c", "d"},
		[]Link{
			{A: "a", B: "d", Kind: NVLink, BandwidthGBps: 777},
			{A: "b", B: "c", Kind: NVLink, BandwidthGBps: 777},
		},
	)
	free := []string{"a", "b", "c", "d"}

	const wantA, wantB = "a", "d"

	// Repeated calls with the canonical ordering — stable winner.
	for i := 0; i < 64; i++ {
		rec, err := SelectPair(fmt.Sprintf("run-tie-%d", i), topo, free)
		if err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}
		if !pairEq(rec.GPUA, rec.GPUB, wantA, wantB) {
			t.Fatalf("call %d: tie-break unstable: got {%s,%s}, want {%s,%s}",
				i, rec.GPUA, rec.GPUB, wantA, wantB)
		}
	}

	// Fixed-seed shuffles of the input slice — winner must NOT depend on input order.
	for _, seed := range []int64{1, 2, 3, 7, 11, 42, 99, 1234, 65535, 1 << 20} {
		shuffled := shuffleSeeded(free, seed)
		rec, err := SelectPair(fmt.Sprintf("run-tie-seed-%d", seed), topo, shuffled)
		if err != nil {
			t.Fatalf("seed %d (%v): unexpected error: %v", seed, shuffled, err)
		}
		if !pairEq(rec.GPUA, rec.GPUB, wantA, wantB) {
			t.Errorf("seed %d input=%v: tie-break input-order-dependent: got {%s,%s}, want {%s,%s}",
				seed, shuffled, rec.GPUA, rec.GPUB, wantA, wantB)
		}
	}
}

// Tie-break among equal-bandwidth PCIe pairs (fallback pool) is equally stable.
func TestAdversarial_DeterministicTieBreak_PCIePool(t *testing.T) {
	flushRecords()

	topo := mustTopology(t,
		[]string{"p0", "p1", "p2", "p3"},
		[]Link{
			{A: "p0", B: "p3", Kind: PCIe, BandwidthGBps: 32},
			{A: "p1", B: "p2", Kind: PCIe, BandwidthGBps: 32},
		},
	)
	free := []string{"p0", "p1", "p2", "p3"}
	// Sorted [p0 p1 p2 p3]: first eligible candidate = {p0,p3}.
	const wantA, wantB = "p0", "p3"

	for _, seed := range []int64{0, 5, 50, 500, 5000} {
		rec, err := SelectPair(fmt.Sprintf("run-pcie-tie-%d", seed), topo, shuffleSeeded(free, seed))
		if err != nil {
			t.Fatalf("seed %d: unexpected error: %v", seed, err)
		}
		if rec.Kind != PCIe {
			t.Errorf("seed %d: Kind got %v, want PCIe", seed, rec.Kind)
		}
		if !pairEq(rec.GPUA, rec.GPUB, wantA, wantB) {
			t.Errorf("seed %d: got {%s,%s}, want {%s,%s}", seed, rec.GPUA, rec.GPUB, wantA, wantB)
		}
	}
}

// ────────────────────────────────────────────────────────────────────────────
// CONNECTEDNESS — the chosen pair is always actually adjacent in the topology.
//
// Adversarial: many GPUs are free but only a sparse subset is connected, and a
// higher-"bandwidth" phantom (non-existent) link is what a buggy adjacency
// lookup might hallucinate. We assert the winner is a REAL link.
// ────────────────────────────────────────────────────────────────────────────

func TestAdversarial_ChosenPairIsActuallyConnected(t *testing.T) {
	flushRecords()

	gpus := []string{"x0", "x1", "x2", "x3", "x4", "x5", "x6", "x7"}
	links := []Link{
		{A: "x0", B: "x5", Kind: NVLink, BandwidthGBps: 500},
		{A: "x2", B: "x6", Kind: NVLink, BandwidthGBps: 800}, // winner
		{A: "x1", B: "x4", Kind: PCIe, BandwidthGBps: 32},
	}
	topo := mustTopology(t, gpus, links)

	for _, seed := range []int64{1, 13, 137, 1009} {
		rec, err := SelectPair(fmt.Sprintf("run-conn-%d", seed), topo, shuffleSeeded(gpus, seed))
		if err != nil {
			t.Fatalf("seed %d: unexpected error: %v", seed, err)
		}
		// The returned pair MUST correspond to a real link in the topology.
		l, ok := topo.Link(rec.GPUA, rec.GPUB)
		if !ok {
			t.Fatalf("seed %d: phantom pair selected: {%s,%s} has no link in topology",
				seed, rec.GPUA, rec.GPUB)
		}
		// And its recorded metadata must match the real link.
		if l.Kind != rec.Kind || l.BandwidthGBps != rec.BandwidthGBps {
			t.Errorf("seed %d: record metadata {%v,%v} != real link {%v,%v}",
				seed, rec.Kind, rec.BandwidthGBps, l.Kind, l.BandwidthGBps)
		}
		// Best NVLink here is the 800 pair {x2,x6}.
		if !pairEq(rec.GPUA, rec.GPUB, "x2", "x6") {
			t.Errorf("seed %d: got {%s,%s}, want {x2,x6}", seed, rec.GPUA, rec.GPUB)
		}
	}
}

// ────────────────────────────────────────────────────────────────────────────
// FALLBACK BOUNDARY — exactly at the NVLink/PCIe edge.
//
// Mixed topology with one NVLink pair and one PCIe pair, where freeing the
// NVLink endpoints flips the answer from NVLink to PCIe. We assert BOTH sides
// of the boundary in one test.
// ────────────────────────────────────────────────────────────────────────────

func TestAdversarial_FallbackBoundary(t *testing.T) {
	flushRecords()

	topo := mustTopology(t,
		[]string{"n0", "n1", "c0", "c1"},
		[]Link{
			{A: "n0", B: "n1", Kind: NVLink, BandwidthGBps: 600},
			{A: "c0", B: "c1", Kind: PCIe, BandwidthGBps: 64},
		},
	)

	// Mixed-free: NVLink available → NVLink chosen.
	recMixed, err := SelectPair("run-bound-mixed", topo, []string{"n0", "n1", "c0", "c1"})
	if err != nil {
		t.Fatalf("mixed: unexpected error: %v", err)
	}
	if recMixed.Kind != NVLink || !pairEq(recMixed.GPUA, recMixed.GPUB, "n0", "n1") {
		t.Errorf("mixed: got {%s,%s}@%v, want NVLink {n0,n1}", recMixed.GPUA, recMixed.GPUB, recMixed.Kind)
	}

	// NVLink endpoint busy → only PCIe pair eligible → PCIe chosen.
	recPCIe, err := SelectPair("run-bound-pcie", topo, []string{"n0", "c0", "c1"})
	if err != nil {
		t.Fatalf("pcie: unexpected error: %v", err)
	}
	if recPCIe.Kind != PCIe || !pairEq(recPCIe.GPUA, recPCIe.GPUB, "c0", "c1") {
		t.Errorf("pcie: got {%s,%s}@%v, want PCIe {c0,c1}", recPCIe.GPUA, recPCIe.GPUB, recPCIe.Kind)
	}
}

// Only-PCIe topology must fall back to PCIe (never error when a PCIe pair exists).
func TestAdversarial_OnlyPCIeFallsBack(t *testing.T) {
	flushRecords()

	topo := mustTopology(t,
		[]string{"q0", "q1", "q2", "q3"},
		[]Link{
			{A: "q0", B: "q1", Kind: PCIe, BandwidthGBps: 16},
			{A: "q2", B: "q3", Kind: PCIe, BandwidthGBps: 64}, // higher PCIe must win
		},
	)
	rec, err := SelectPair("run-only-pcie", topo, []string{"q0", "q1", "q2", "q3"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Kind != PCIe || rec.BandwidthGBps != 64 || !pairEq(rec.GPUA, rec.GPUB, "q2", "q3") {
		t.Errorf("got {%s,%s}@%v/%v, want PCIe {q2,q3}@64", rec.GPUA, rec.GPUB, rec.Kind, rec.BandwidthGBps)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// GUARD EDGES — too-few-GPUs (off-by-one), no-pair, single, empty; no panic;
// and a FAILED selection must NOT leave a record in the store.
// ────────────────────────────────────────────────────────────────────────────

func TestAdversarial_GuardEdges(t *testing.T) {
	flushRecords()

	topo := mustTopology(t,
		[]string{"g0", "g1", "g2"},
		[]Link{{A: "g0", B: "g1", Kind: NVLink, BandwidthGBps: 600}},
	)

	// Exactly two free + connected → success (boundary of the < 2 guard).
	if _, err := SelectPair("run-exactly-two", topo, []string{"g0", "g1"}); err != nil {
		t.Errorf("exactly-two connected free GPUs must succeed, got %v", err)
	}

	// Too-few cases → ErrTooFewGPUs, no record stored.
	for name, free := range map[string][]string{
		"nil":    nil,
		"empty":  {},
		"single": {"g0"},
	} {
		runID := "run-few-" + name
		_, err := SelectPair(runID, topo, free)
		if !errors.Is(err, ErrTooFewGPUs) {
			t.Errorf("%s: got err %v, want ErrTooFewGPUs", name, err)
		}
		if _, ok := RecordFor(runID); ok {
			t.Errorf("%s: failed selection must NOT store a record", name)
		}
	}

	// Two free but no link between them → ErrNoPairAvailable, no record.
	runID := "run-nopair-edge"
	_, err := SelectPair(runID, topo, []string{"g0", "g2"})
	if !errors.Is(err, ErrNoPairAvailable) {
		t.Errorf("got err %v, want ErrNoPairAvailable", err)
	}
	if _, ok := RecordFor(runID); ok {
		t.Error("no-pair failure must NOT store a record")
	}
}

// Empty topology with two "free" IDs that aren't even in the topology → no
// eligible pair, no panic.
func TestAdversarial_EmptyTopologyNoPanic(t *testing.T) {
	flushRecords()

	topo := mustTopology(t, []string{"only0", "only1"}, nil)
	_, err := SelectPair("run-empty", topo, []string{"only0", "only1"})
	if !errors.Is(err, ErrNoPairAvailable) {
		t.Errorf("empty-link topology: got %v, want ErrNoPairAvailable", err)
	}
}
