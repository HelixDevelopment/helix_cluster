package preempt

import (
	"fmt"
	"math/rand"
	"os"
	"sort"
	"testing"
)

// runUUIDAdv anchors this adversarial suite's evidence to a single forensic run.
const runUUIDAdv = "HXC-1584-preempt-adv-7d2e9b04-1c63-4f8a-bb15-2a90e7c4d318"

// oracleDisabledForIndependenceProof, when set via the HXC_PREEMPT_NO_ORACLE
// env var, disables the exact-sequence oracle match so that mutation-testing can
// prove the cross-cutting invariants (priority safety, sufficiency, minimality,
// feasibility) catch policy mutations ON THEIR OWN — not merely via the oracle.
// It defaults to false, so the real suite ALWAYS runs the full oracle match.
var oracleDisabledForIndependenceProof = os.Getenv("HXC_PREEMPT_NO_ORACLE") == "1"

// ----------------------------------------------------------------------------
// Independent oracle.
//
// oracleEligible recomputes, with zero reference to Preempt's internals, the
// set of instances that the DOCUMENTED policy permits as victims:
//
//	eligible  <=>  !SoleGlobal  AND  incoming.eff STRICTLY EXCEEDS instance.eff
//
// This is the priority-safety predicate. It is deliberately phrased with `>`
// (not `>=`) so a mutation that relaxes the production check to `>=` diverges
// from the oracle and is caught.
// ----------------------------------------------------------------------------

func eff(in Instance) int64 { return in.Value * in.Multiplier }

// oracleVictimsAndPlaced independently reproduces the documented greedy policy:
//   - candidates = eligible instances (strict-lower eff, not SoleGlobal)
//   - sorted lowest-eff first, tie-break on ORIGINAL index (stable)
//   - accumulate lowest-first, stopping the instant freed >= need
//   - if all eligible cannot reach need => not placed, no victims
//
// It returns the EXACT expected victim ID sequence and placed flag. Because it
// is written from the prose spec rather than copied from Preempt, agreement is
// load-bearing evidence, and any production mutation that changes selection
// order, eligibility, the stop condition, or the reject path diverges here.
func oracleVictimsAndPlaced(node Node, incoming Instance) (ids []string, placed bool) {
	need := incoming.Size
	incPri := eff(incoming)

	type cand struct {
		in  Instance
		idx int
	}
	var cands []cand
	for i, in := range node.Running {
		if in.SoleGlobal {
			continue
		}
		if incPri > eff(in) { // STRICT
			cands = append(cands, cand{in: in, idx: i})
		}
	}
	sort.SliceStable(cands, func(a, b int) bool {
		pa, pb := eff(cands[a].in), eff(cands[b].in)
		if pa != pb {
			return pa < pb
		}
		return cands[a].idx < cands[b].idx
	})

	freed := node.Free()
	var chosen []string
	for _, c := range cands {
		if freed >= need {
			break
		}
		chosen = append(chosen, c.in.ID)
		freed += c.in.Size
	}
	if freed < need {
		return nil, false
	}
	return chosen, true
}

// sumSize totals Size over a victim slice.
func sumSize(vs []Instance) int64 {
	var s int64
	for _, v := range vs {
		s += v.Size
	}
	return s
}

// maxEligibleFreeable independently computes the MAXIMUM capacity that could
// ever be liberated for `incoming`: current Free plus the Size of EVERY
// eligible (strict-lower-eff, non-SoleGlobal) instance. Placement is feasible
// iff this >= need. This is an implementation-independent feasibility oracle.
func maxEligibleFreeable(node Node, incoming Instance) int64 {
	total := node.Free()
	incPri := eff(incoming)
	for _, in := range node.Running {
		if in.SoleGlobal {
			continue
		}
		if incPri > eff(in) {
			total += in.Size
		}
	}
	return total
}

// assertInvariants checks every cross-cutting correctness property the
// documented policy must satisfy, for ONE (node, incoming) outcome. Each
// failure message states the bite. desc identifies the random case for replay.
func assertInvariants(t *testing.T, desc string, node Node, incoming Instance, victims []Instance, placed bool) {
	t.Helper()
	need := incoming.Size
	incPri := eff(incoming)

	// --- INVARIANT 1: PRIORITY SAFETY (the core anti-bluff invariant). -------
	// No victim may have effective priority >= the incoming's, and no victim
	// may be SoleGlobal. A priority-inversion bug (relaxing strict `>` to `>=`,
	// or selecting a higher-eff victim, or dropping the SoleGlobal guard) is
	// caught here: such a victim appears and this fires.
	for _, v := range victims {
		if eff(v) >= incPri {
			t.Fatalf("run=%s case=%s PRIORITY INVERSION: victim %q eff=%d >= incoming eff=%d (a workload was preempted by an equal-or-lower priority workload)",
				runUUIDAdv, desc, v.ID, eff(v), incPri)
		}
		if v.SoleGlobal {
			t.Fatalf("run=%s case=%s PROTECTION VIOLATION: SoleGlobal victim %q was evicted",
				runUUIDAdv, desc, v.ID)
		}
	}

	// Victims must be REAL running instances and DISTINCT (no phantom / double
	// counting that could fake sufficiency).
	seen := map[string]bool{}
	runningByID := map[string]Instance{}
	for _, in := range node.Running {
		runningByID[in.ID] = in
	}
	for _, v := range victims {
		if seen[v.ID] {
			t.Fatalf("run=%s case=%s DUPLICATE victim %q (double-counted capacity)", runUUIDAdv, desc, v.ID)
		}
		seen[v.ID] = true
		if _, ok := runningByID[v.ID]; !ok {
			t.Fatalf("run=%s case=%s PHANTOM victim %q not in node.Running", runUUIDAdv, desc, v.ID)
		}
	}

	if !placed {
		// --- INVARIANT 2: failed placement performs NO eviction. -------------
		if len(victims) != 0 {
			t.Fatalf("run=%s case=%s PARTIAL-EVICTION: placed=false but %d victims returned (%v) — a placement that cannot succeed must evict nothing",
				runUUIDAdv, desc, len(victims), victimIDs(victims))
		}
		// --- INVARIANT 3: rejection is correct (feasibility oracle). ---------
		// If we rejected, it MUST have been infeasible: even evicting every
		// eligible instance could not reach need. Bites a false rejection.
		if maxEligibleFreeable(node, incoming) >= need {
			t.Fatalf("run=%s case=%s FALSE REJECTION: placed=false but maxFreeable=%d >= need=%d (placement was feasible)",
				runUUIDAdv, desc, maxEligibleFreeable(node, incoming), need)
		}
		return
	}

	// placed == true below.

	// --- INVARIANT 4: SUFFICIENCY across the (single) resource dimension. ----
	// Freed capacity (current Free + every evicted Size) must cover need. Bites
	// a wrong feasibility calc that reports placed without actually freeing
	// enough room.
	freed := node.Free() + sumSize(victims)
	if freed < need {
		t.Fatalf("run=%s case=%s INSUFFICIENT: placed=true but freed=%d (free=%d + victims=%d) < need=%d",
			runUUIDAdv, desc, freed, node.Free(), sumSize(victims), need)
	}

	// --- INVARIANT 5: feasibility-iff (placed implies it was possible). ------
	if maxEligibleFreeable(node, incoming) < need {
		t.Fatalf("run=%s case=%s IMPOSSIBLE PLACEMENT: placed=true but maxFreeable=%d < need=%d",
			runUUIDAdv, desc, maxEligibleFreeable(node, incoming), need)
	}

	// --- INVARIANT 6: MINIMALITY / no over-eviction (greedy stop rule). ------
	// The accumulation must stop the instant it has enough room. Because victims
	// are returned in greedy add-order, the LAST element of the slice is the
	// final pick that crossed the freed>=need threshold; dropping it must leave
	// freed < need, else it was evicted needlessly. Bites removal of the
	// `if freed >= need { break }` guard, which appends an extra (final) victim
	// even though the prefix already sufficed.
	//
	// (Ordering soundness — that victims really are in greedy order — is pinned
	// separately by the exact-sequence oracle match in the caller; here we only
	// use it to name the last pick.)
	if len(victims) > 0 {
		last := victims[len(victims)-1]
		freedWithoutLast := node.Free() + sumSize(victims) - last.Size
		if freedWithoutLast >= need {
			t.Fatalf("run=%s case=%s OVER-EVICTION: freed without final victim %q (size=%d, eff=%d) = %d still >= need=%d; it was evicted needlessly (%v)",
				runUUIDAdv, desc, last.ID, last.Size, eff(last), freedWithoutLast, need, victimIDs(victims))
		}
	}
}

// ----------------------------------------------------------------------------
// TestPreempt_AdversarialOracle exhaustively fuzzes Preempt against the
// independent oracle across many randomized priority/resource configurations.
//
// For each random (node, incoming):
//  1. EXACT-MATCH the returned victim ID sequence and placed flag against
//     oracleVictimsAndPlaced (which encodes the documented greedy policy:
//     lowest-eff-first, strict eligibility, stop-when-enough, reject-cleanly).
//     This bites wrong victim selection, wrong ordering, wrong stop condition,
//     strict-vs-non-strict eligibility, and the reject/partial-eviction path.
//  2. Independently assert the cross-cutting invariants (priority safety,
//     sufficiency, feasibility-iff, minimality, no-phantom/dup victims). These
//     hold regardless of the oracle and bite priority inversion directly.
//  3. node MUST NOT be mutated (Preempt documents a non-mutating contract).
//
// ANTI-BLUFF mutation map (each verified to make this test FAIL — see report):
//   - eligibility `incPri > eff` -> `incPri >= eff`  : equal-eff victim appears
//     -> INVARIANT 1 (priority inversion) AND oracle mismatch fire.
//   - drop `if in.SoleGlobal { continue }`           : protected victim appears
//     -> INVARIANT 1 (protection) AND oracle mismatch fire.
//   - reverse sort (pa < pb -> pa > pb)              : highest-eff evicted first
//     -> oracle mismatch (wrong ID sequence) fires.
//   - drop `if freed >= need { break }`              : over-eviction
//     -> INVARIANT 6 AND oracle mismatch fire.
//   - reject `return nil,false` -> `return chosen,true` for infeasible
//     -> INVARIANT 4 (insufficient) / INVARIANT 2 (partial) AND oracle fire.
func TestPreempt_AdversarialOracle(t *testing.T) {
	const cases = 20000
	rng := rand.New(rand.NewSource(0xC0FFEE_1584)) // fixed seed => reproducible

	var (
		placedCount   int
		rejectedCount int
		evictCount    int
		soleGlobalHit int
	)

	for c := 0; c < cases; c++ {
		// Small, dense parameter ranges maximize the rate of FULL nodes and
		// ties (the adversarial sweet spots) rather than trivially-free nodes.
		nRunning := rng.Intn(6)        // 0..5 running instances
		capacity := int64(rng.Intn(9)) // 0..8

		running := make([]Instance, 0, nRunning)
		for i := 0; i < nRunning; i++ {
			in := Instance{
				ID:         fmt.Sprintf("r%d", i),
				Value:      int64(rng.Intn(5)),     // 0..4  (0 forces eff=0, a real edge)
				Multiplier: int64(rng.Intn(5)),     // 0..4
				Size:       int64(1 + rng.Intn(3)), // 1..3
				SoleGlobal: rng.Intn(4) == 0,       // ~25% protected
			}
			running = append(running, in)
			if in.SoleGlobal {
				soleGlobalHit++
			}
		}
		node := Node{Capacity: capacity, Running: running}

		incoming := Instance{
			ID:         "incoming",
			Value:      int64(rng.Intn(6)),     // 0..5
			Multiplier: int64(rng.Intn(6)),     // 0..5
			Size:       int64(1 + rng.Intn(4)), // 1..4
		}

		// Snapshot node to detect mutation.
		snapRunning := make([]Instance, len(node.Running))
		copy(snapRunning, node.Running)
		snapCap := node.Capacity

		desc := fmt.Sprintf("c=%d cap=%d running=%v incoming{v=%d m=%d size=%d eff=%d}",
			c, capacity, running, incoming.Value, incoming.Multiplier, incoming.Size, eff(incoming))

		victims, placed := Preempt(node, incoming)

		// (1) Exact match against the independent policy oracle.
		gotIDs := victimIDs(victims)
		wantIDs, wantPlaced := oracleVictimsAndPlaced(node, incoming)
		if !oracleDisabledForIndependenceProof {
			if placed != wantPlaced {
				t.Fatalf("run=%s case=%s PLACED MISMATCH: got placed=%t want=%t (victims got=%v want=%v)",
					runUUIDAdv, desc, placed, wantPlaced, gotIDs, wantIDs)
			}
			if !equalStrings(gotIDs, wantIDs) {
				t.Fatalf("run=%s case=%s VICTIM-SET MISMATCH: got=%v want=%v (documented policy = lowest-eff-first greedy)",
					runUUIDAdv, desc, gotIDs, wantIDs)
			}
		}

		// (2) Cross-cutting invariants (independent of the oracle).
		assertInvariants(t, desc, node, incoming, victims, placed)

		// (3) Non-mutation contract.
		if node.Capacity != snapCap {
			t.Fatalf("run=%s case=%s MUTATION: node.Capacity changed %d -> %d", runUUIDAdv, desc, snapCap, node.Capacity)
		}
		if !equalInstances(node.Running, snapRunning) {
			t.Fatalf("run=%s case=%s MUTATION: node.Running was modified", runUUIDAdv, desc)
		}

		if placed {
			placedCount++
			if len(victims) > 0 {
				evictCount++
			}
		} else {
			rejectedCount++
		}
	}

	// Coverage floor: the fuzz must actually exercise placements, rejections,
	// real evictions, and protected instances — otherwise a vacuous pass could
	// hide behind an all-trivial sample. Fail loudly if any bucket is empty.
	t.Logf("run=%s coverage: cases=%d placed=%d rejected=%d withEviction=%d soleGlobalSeen=%d",
		runUUIDAdv, cases, placedCount, rejectedCount, evictCount, soleGlobalHit)
	if placedCount == 0 || rejectedCount == 0 || evictCount == 0 || soleGlobalHit == 0 {
		t.Fatalf("run=%s VACUOUS FUZZ: a coverage bucket was empty (placed=%d rejected=%d evict=%d sole=%d)",
			runUUIDAdv, placedCount, rejectedCount, evictCount, soleGlobalHit)
	}
}

// TestPreempt_DeterministicTieBreak proves equal-effective-priority victims are
// chosen by the documented stable tie-break on ORIGINAL index, and that repeated
// calls are byte-for-byte identical (no map-iteration nondeterminism).
//
// Construction: a FULL node with three eligible victims all of EQUAL eff=6
// (2*3, 3*2, 6*1) plus one higher-eff spare. Incoming (eff=100) needs size 2 =>
// exactly the FIRST TWO by original index must be evicted; the third equal-eff
// instance is spared, and the order is t0 then t1.
//
// Mutation: making the tie-break non-stable (e.g. comparing on a field other
// than original index, or using a Go map) would change WHICH two equal-eff
// instances are chosen or their order -> the exact-sequence assertion fails.
func TestPreempt_DeterministicTieBreak(t *testing.T) {
	t0 := Instance{ID: "t0", Value: 2, Multiplier: 3, Size: 1}     // eff=6, idx 0
	t1 := Instance{ID: "t1", Value: 3, Multiplier: 2, Size: 1}     // eff=6, idx 1
	t2 := Instance{ID: "t2", Value: 6, Multiplier: 1, Size: 1}     // eff=6, idx 2 (spared)
	hi := Instance{ID: "hi", Value: 50, Multiplier: 2, Size: 1}    // eff=100 (ineligible: not < incoming)
	node := Node{Capacity: 4, Running: []Instance{t0, t1, t2, hi}} // Free()==0

	incoming := Instance{ID: "incoming", Value: 10, Multiplier: 10, Size: 2} // eff=100, needs 2

	t.Logf("run=%s tie-break BEFORE: free=%d t0=%d t1=%d t2=%d hi=%d incoming.eff=%d need=%d",
		runUUIDAdv, node.Free(), eff(t0), eff(t1), eff(t2), eff(hi), eff(incoming), incoming.Size)

	var first []string
	for rep := 0; rep < 50; rep++ {
		victims, placed := Preempt(node, incoming)
		if !placed {
			t.Fatalf("run=%s rep=%d expected placement, got placed=false", runUUIDAdv, rep)
		}
		got := victimIDs(victims)
		if rep == 0 {
			first = got
			t.Logf("run=%s tie-break AFTER: victims=%v", runUUIDAdv, got)
		}
		// Documented stable tie-break: lowest original index first => t0, t1.
		want := []string{"t0", "t1"}
		if !equalStrings(got, want) {
			t.Fatalf("run=%s rep=%d tie-break WRONG: got=%v want=%v (equal-eff must evict lowest original index first)",
				runUUIDAdv, rep, got, want)
		}
		if eff(hi) < eff(incoming) {
			t.Fatalf("run=%s setup error: hi must be ineligible", runUUIDAdv)
		}
		if containsID(victims, "hi") {
			t.Fatalf("run=%s rep=%d eff=100 spare must never be a victim, got %v", runUUIDAdv, rep, got)
		}
		// Determinism across repeats.
		if !equalStrings(got, first) {
			t.Fatalf("run=%s rep=%d NONDETERMINISM: got=%v != first=%v", runUUIDAdv, rep, got, first)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalInstances(a, b []Instance) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
