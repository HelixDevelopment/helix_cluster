package antientropy

// Adversarial, mutation-verified audit of the anti-entropy RECONCILIATION
// contract (HXC-1410 audit). The reconcile() driver and its accounting helpers
// live in reconcile_test.go; this file reuses them and pins the contract from
// every adversarial angle the existing suite does not cover:
//
//   - convergence across many divergence SHAPES (not just the one fixed 1005-key
//     scenario): one-sided, both-sided, conflict-only, single-key, empty sides;
//   - no-op on identical at small/odd sizes (the digest fast path must not depend
//     on a power-of-two leaf count);
//   - idempotence: reconcile twice, second round MUST move nothing;
//   - exact one-sided delta accounting and direction;
//   - strict version precedence for the conflict winner (deterministic), and the
//     symmetric case (A-newer and B-newer both resolve to the higher version);
//   - EDGE: both empty, one empty.
//
// PLUS the load-bearing convergence property whose absence this audit surfaced
// as HXC-1759 (now FIXED):
//
//   *** Equal-version, different-value keys are flagged divergent by MerkleDiff.
//   *** Before the fix, reconcile() transferred NOTHING and the replicas never
//   *** converged, because VersionedValue.newer (strictly-greater-version) had NO
//   *** deterministic tie-break for equal versions — a permanent split-brain in
//   *** both reconcile and the production read-repair path (Coordinator.Read).
//   *** The fix adds a value tie-break (version tie -> lexicographically-greater
//   *** value wins), making (version,value) a total order; both paths now
//   *** converge equal-version conflicts. Proven by
//   *** TestAE_ConflictEqualVersion_Converges below.

import (
	"fmt"
	"testing"
)

const advUUID = "antientropy-adversarial-HXC-1410"

// rootsEqual is the cheap convergence oracle (independent of assertConverged).
func rootsEqual(a, b *Replica) bool {
	return NewMerkleTree(a.Snapshot()).Root() == NewMerkleTree(b.Snapshot()).Root()
}

// seedShared writes n identical key/value@v1 pairs to both replicas.
func seedShared(a, b *Replica, n int) {
	for i := 0; i < n; i++ {
		k := fmt.Sprintf("s-%04d", i)
		vv := VersionedValue{Value: fmt.Sprintf("v-%04d", i), Version: 1}
		a.apply(k, vv)
		b.apply(k, vv)
	}
}

// -----------------------------------------------------------------------------
// CENTERPIECE 1: convergence across many divergence shapes.
// -----------------------------------------------------------------------------

// TestAE_Converges_ManyShapes drives reconcile() over a table of qualitatively
// different divergences and asserts, for EACH, full bidirectional convergence
// (identical Merkle root + identical key sets + identical values) AND that the
// number of entries that crossed the wire equals the true (strict-version)
// difference count — never the shared-key bulk.
//
// Mutation that bites: skipping the apply of a received key in reconcile()'s
// "present only on one side" branches breaks convergence here (a pulled/pushed
// key never lands); always-transfer breaks the transfer-count assertion.
func TestAE_Converges_ManyShapes(t *testing.T) {
	type shape struct {
		name       string
		shared     int
		build      func(a, b *Replica)
		wantMoved  int      // entries that must cross the wire (strict-version diffs)
		mustOnA    []string // keys that must end up present on A (pulled)
		mustOnB    []string // keys that must end up present on B (pushed)
		wantWinner map[string]string
	}

	shapes := []shape{
		{
			name:   "one-sided/A-has-extras",
			shared: 64,
			build: func(a, b *Replica) {
				a.apply("xa-1", VersionedValue{Value: "A1", Version: 1})
				a.apply("xa-2", VersionedValue{Value: "A2", Version: 1})
			},
			wantMoved: 2,
			mustOnB:   []string{"xa-1", "xa-2"},
		},
		{
			name:   "one-sided/B-has-extras",
			shared: 64,
			build: func(a, b *Replica) {
				b.apply("xb-1", VersionedValue{Value: "B1", Version: 1})
				b.apply("xb-2", VersionedValue{Value: "B2", Version: 1})
				b.apply("xb-3", VersionedValue{Value: "B3", Version: 1})
			},
			wantMoved: 3,
			mustOnA:   []string{"xb-1", "xb-2", "xb-3"},
		},
		{
			name:   "both-sided-disjoint",
			shared: 100,
			build: func(a, b *Replica) {
				a.apply("ua", VersionedValue{Value: "UA", Version: 1})
				b.apply("ub", VersionedValue{Value: "UB", Version: 1})
			},
			wantMoved: 2,
			mustOnA:   []string{"ub"},
			mustOnB:   []string{"ua"},
		},
		{
			name:   "conflict-only/B-newer",
			shared: 32,
			build: func(a, b *Replica) {
				a.apply("s-0005", VersionedValue{Value: "A-stale", Version: 2})
				b.apply("s-0005", VersionedValue{Value: "B-fresh", Version: 7})
			},
			wantMoved:  1,
			wantWinner: map[string]string{"s-0005": "B-fresh"},
		},
		{
			name:   "conflict-only/A-newer",
			shared: 32,
			build: func(a, b *Replica) {
				a.apply("s-0009", VersionedValue{Value: "A-fresh", Version: 9})
				b.apply("s-0009", VersionedValue{Value: "B-stale", Version: 4})
			},
			wantMoved:  1,
			wantWinner: map[string]string{"s-0009": "A-fresh"},
		},
		{
			name:   "mixed/extras+conflicts-both-directions",
			shared: 256,
			build: func(a, b *Replica) {
				a.apply("ea", VersionedValue{Value: "EA", Version: 1})
				b.apply("eb", VersionedValue{Value: "EB", Version: 1})
				a.apply("s-0001", VersionedValue{Value: "Awins", Version: 6}) // A newer
				b.apply("s-0002", VersionedValue{Value: "Bwins", Version: 6}) // B newer
			},
			wantMoved:  4,
			mustOnA:    []string{"eb"},
			mustOnB:    []string{"ea"},
			wantWinner: map[string]string{"s-0001": "Awins", "s-0002": "Bwins"},
		},
		{
			name:      "single-key-only-on-A",
			shared:    0,
			build:     func(a, b *Replica) { a.apply("solo", VersionedValue{Value: "S", Version: 1}) },
			wantMoved: 1,
			mustOnB:   []string{"solo"},
		},
		{
			name:      "one-empty-side/A-empty-B-has-data",
			shared:    0,
			build:     func(a, b *Replica) { seedShared(b, NewReplica("sink"), 10) }, // only B gets seeded
			wantMoved: 10,
			mustOnA:   []string{"s-0000", "s-0009"},
		},
	}

	for _, sh := range shapes {
		t.Run(sh.name, func(t *testing.T) {
			a := NewReplica("A")
			b := NewReplica("B")
			seedShared(a, b, sh.shared)
			sh.build(a, b)

			if rootsEqual(a, b) && sh.wantMoved != 0 {
				t.Fatalf("precondition: replicas already converged but shape expects %d diffs", sh.wantMoved)
			}

			res := reconcile(a, b)

			if res.totalEntries != sh.wantMoved {
				t.Fatalf("transferred %d entries, want %d (diffKeys=%v)", res.totalEntries, sh.wantMoved, res.diffKeys)
			}
			// Never a wholesale copy: a transfer at/above shared scale is a bluff.
			if sh.shared > 0 && res.totalEntries >= sh.shared {
				t.Fatalf("transfer %d reached dataset scale %d: copy-everything, not anti-entropy",
					res.totalEntries, sh.shared)
			}

			assertConverged(t, a, b)

			for _, k := range sh.mustOnA {
				if _, ok := a.Get(k); !ok {
					t.Fatalf("A missing pulled key %q after reconcile", k)
				}
			}
			for _, k := range sh.mustOnB {
				if _, ok := b.Get(k); !ok {
					t.Fatalf("B missing pushed key %q after reconcile", k)
				}
			}
			for k, wantVal := range sh.wantWinner {
				va, _ := a.Get(k)
				vb, _ := b.Get(k)
				if va.Value != wantVal || vb.Value != wantVal {
					t.Fatalf("conflict key %q wrong winner: A=%q B=%q want %q", k, va.Value, vb.Value, wantVal)
				}
			}

			// Independent post-condition: a fresh diff over the converged stores
			// must be empty.
			if d := MerkleDiff(NewMerkleTree(a.Snapshot()), NewMerkleTree(b.Snapshot())); len(d) != 0 {
				t.Fatalf("post-reconcile diff must be empty, got %v", d)
			}
			t.Logf("run=%s shape=%q moved=%d toA=%d toB=%d converged=true",
				advUUID, sh.name, res.totalEntries, res.keysToA, res.keysToB)
		})
	}
}

// -----------------------------------------------------------------------------
// CENTERPIECE 2: no-op on identical, including odd / non-power-of-two sizes.
// -----------------------------------------------------------------------------

// TestAE_NoOpOnIdentical_Sizes proves the zero-transfer fast path holds for
// awkward leaf counts (1, 2, 3, 5, 7, 13, 17, 31) — the carry-up of odd nodes in
// buildLevel must still yield identical roots and an empty exchange.
//
// Mutation that bites: forcing reconcile to always transfer (or MerkleDiff to
// return all keys) makes totalEntries > 0 here.
func TestAE_NoOpOnIdentical_Sizes(t *testing.T) {
	for _, n := range []int{1, 2, 3, 5, 7, 13, 17, 31} {
		a := NewReplica("A")
		b := NewReplica("B")
		seedShared(a, b, n)

		if !rootsEqual(a, b) {
			t.Fatalf("n=%d: identical replicas must share a Merkle root", n)
		}
		res := reconcile(a, b)
		if len(res.diffKeys) != 0 {
			t.Fatalf("n=%d: identical replicas must yield empty diff, got %v", n, res.diffKeys)
		}
		if res.totalEntries != 0 {
			t.Fatalf("n=%d: identical replicas must transfer ZERO entries, got %d", n, res.totalEntries)
		}
		assertConverged(t, a, b)
	}
	// Edge: both empty -> identical, zero transfer.
	a, b := NewReplica("A"), NewReplica("B")
	if !rootsEqual(a, b) {
		t.Fatalf("two empty replicas must share the zero root")
	}
	if res := reconcile(a, b); res.totalEntries != 0 || len(res.diffKeys) != 0 {
		t.Fatalf("empty/empty must be a total no-op, got transfers=%d diff=%v", res.totalEntries, res.diffKeys)
	}
	t.Logf("run=%s noOpOnIdentical sizes+empty all zero-transfer", advUUID)
}

// -----------------------------------------------------------------------------
// IDEMPOTENCE: a second round after convergence moves nothing.
// -----------------------------------------------------------------------------

// TestAE_Idempotent_SecondRoundIsNoOp reconciles a richly-divergent pair, then
// reconciles again and asserts the second round transfers nothing and the diff
// is empty — converged state stays converged.
//
// Mutation that bites: if apply reported "changed" even when it wrote an equal
// value, the second round would re-transfer and this fails.
func TestAE_Idempotent_SecondRoundIsNoOp(t *testing.T) {
	a := NewReplica("A")
	b := NewReplica("B")
	seedShared(a, b, 200)
	a.apply("only-a", VersionedValue{Value: "x", Version: 1})
	b.apply("only-b", VersionedValue{Value: "y", Version: 1})
	a.apply("s-0050", VersionedValue{Value: "Awins", Version: 4})
	b.apply("s-0051", VersionedValue{Value: "Bwins", Version: 4})

	first := reconcile(a, b)
	assertConverged(t, a, b)
	if first.totalEntries != 4 {
		t.Fatalf("round 1 expected 4 transfers, got %d (%v)", first.totalEntries, first.diffKeys)
	}

	second := reconcile(a, b)
	if second.totalEntries != 0 || len(second.diffKeys) != 0 {
		t.Fatalf("round 2 must be a no-op, got transfers=%d diff=%v", second.totalEntries, second.diffKeys)
	}
	// A third round for good measure.
	third := reconcile(a, b)
	if third.totalEntries != 0 {
		t.Fatalf("round 3 must still be a no-op, got %d", third.totalEntries)
	}
	t.Logf("run=%s idempotent r1=%d r2=%d r3=%d", advUUID, first.totalEntries, second.totalEntries, third.totalEntries)
}

// -----------------------------------------------------------------------------
// ONE-SIDED DELTA: exact direction + count.
// -----------------------------------------------------------------------------

// TestAE_OneSidedDelta_ExactDirection asserts that a key present only on A flows
// A->B (and is counted in keysToB, not keysToA) and vice-versa — the delta moves
// in the correct direction and only the delta moves.
func TestAE_OneSidedDelta_ExactDirection(t *testing.T) {
	a := NewReplica("A")
	b := NewReplica("B")
	seedShared(a, b, 20)
	a.apply("from-a", VersionedValue{Value: "FA", Version: 1})
	b.apply("from-b", VersionedValue{Value: "FB", Version: 1})

	res := reconcile(a, b)
	if res.keysToB != 1 {
		t.Fatalf("expected exactly 1 push A->B, got %d", res.keysToB)
	}
	if res.keysToA != 1 {
		t.Fatalf("expected exactly 1 pull B->A, got %d", res.keysToA)
	}
	if res.totalEntries != 2 {
		t.Fatalf("expected 2 total entries moved, got %d", res.totalEntries)
	}
	if vv, ok := b.Get("from-a"); !ok || vv.Value != "FA" {
		t.Fatalf("A's key did not flow to B: %+v ok=%v", vv, ok)
	}
	if vv, ok := a.Get("from-b"); !ok || vv.Value != "FB" {
		t.Fatalf("B's key did not flow to A: %+v ok=%v", vv, ok)
	}
	assertConverged(t, a, b)
	t.Logf("run=%s oneSidedDelta toA=%d toB=%d", advUUID, res.keysToA, res.keysToB)
}

// -----------------------------------------------------------------------------
// CONFLICT RESOLUTION: strict higher-version wins, deterministically, both ways.
// -----------------------------------------------------------------------------

// TestAE_ConflictResolution_HigherVersionWins drives the same conflict key with
// A-newer and (separately) B-newer and asserts the higher version wins on BOTH
// replicas — the documented, deterministic last-write-wins-by-version rule.
//
// Mutation that bites: inverting the winner (picking the LOWER version) makes
// both sub-cases converge to the wrong value and fail.
func TestAE_ConflictResolution_HigherVersionWins(t *testing.T) {
	cases := []struct {
		name       string
		aVer, bVer uint64
		aVal, bVal string
		wantVal    string
	}{
		{"A-newer", 9, 3, "A9", "B3", "A9"},
		{"B-newer", 2, 8, "A2", "B8", "B8"},
		{"A-newer-by-one", 6, 5, "A6", "B5", "A6"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := NewReplica("A")
			b := NewReplica("B")
			a.apply("k", VersionedValue{Value: tc.aVal, Version: tc.aVer})
			b.apply("k", VersionedValue{Value: tc.bVal, Version: tc.bVer})

			res := reconcile(a, b)
			if res.totalEntries != 1 {
				t.Fatalf("conflict must move exactly 1 entry, moved %d", res.totalEntries)
			}
			va, _ := a.Get("k")
			vb, _ := b.Get("k")
			if va.Value != tc.wantVal || vb.Value != tc.wantVal {
				t.Fatalf("winner wrong: A=%q B=%q want %q", va.Value, vb.Value, tc.wantVal)
			}
			// Winning version must be the max of the two.
			wantVer := tc.aVer
			if tc.bVer > wantVer {
				wantVer = tc.bVer
			}
			if va.Version != wantVer || vb.Version != wantVer {
				t.Fatalf("winning version wrong: A=%d B=%d want %d", va.Version, vb.Version, wantVer)
			}
			assertConverged(t, a, b)
		})
	}
	t.Logf("run=%s conflictResolution higherVersionWins both directions", advUUID)
}

// -----------------------------------------------------------------------------
// FINDING (characterization): equal-version, different-value => NO convergence.
// -----------------------------------------------------------------------------

// TestAE_ConflictEqualVersion_Converges proves the fix for HXC-1759: an
// equal-version, different-value conflict now CONVERGES deterministically.
//
// SETUP: A holds k="AAA"@5, B holds k="BBB"@5 — same version, different value.
//
// Before the fix, VersionedValue.newer was strictly greater-than, so neither
// side was "newer", apply was a no-op on both, and the replicas stayed in a
// permanent split-brain — even though MerkleDiff correctly flagged k as
// divergent (an anti-entropy system that detects a divergence it cannot heal).
//
// The fix adds a deterministic value tie-break to newer (version tie ->
// lexicographically-greater value wins), making (version, value) a total order.
// Both reconcile and the production read-repair path now converge equal-version
// conflicts to the same winner ("BBB" > "AAA").
func TestAE_ConflictEqualVersion_Converges(t *testing.T) {
	a := NewReplica("A")
	b := NewReplica("B")
	a.apply("k", VersionedValue{Value: "AAA", Version: 5})
	b.apply("k", VersionedValue{Value: "BBB", Version: 5})

	// MerkleDiff correctly sees the divergence.
	diff := MerkleDiff(NewMerkleTree(a.Snapshot()), NewMerkleTree(b.Snapshot()))
	if fmt.Sprint(diff) != "[k]" {
		t.Fatalf("precondition: MerkleDiff should flag [k] as divergent, got %v", diff)
	}

	res := reconcile(a, b)

	// The equal-version conflict now crosses the wire and is resolved.
	if res.totalEntries == 0 {
		t.Fatalf("REGRESSION: equal-version conflict transferred 0 entries — the " +
			"deterministic tie-break is missing; replicas would stay split-brained")
	}

	// Both replicas converge to the lexicographically-greater value, roots match.
	va, _ := a.Get("k")
	vb, _ := b.Get("k")
	if !rootsEqual(a, b) {
		t.Fatalf("convergence failed: A=%q B=%q, Merkle roots differ", va.Value, vb.Value)
	}
	if va.Value != "BBB" || vb.Value != "BBB" {
		t.Fatalf("wrong deterministic winner: A=%q B=%q, want both %q (lex-greater value)",
			va.Value, vb.Value, "BBB")
	}

	// Idempotent: a second round is a clean no-op (already converged).
	res2 := reconcile(a, b)
	if res2.totalEntries != 0 || !rootsEqual(a, b) {
		t.Fatalf("not idempotent after convergence: secondRoundMoved=%d rootsEqual=%v",
			res2.totalEntries, rootsEqual(a, b))
	}

	// The PRODUCTION read-repair path converges the same way. Use a fresh pair so
	// the read sees the original equal-version divergence.
	a2 := NewReplica("A")
	b2 := NewReplica("B")
	a2.apply("k", VersionedValue{Value: "AAA", Version: 5})
	b2.apply("k", VersionedValue{Value: "BBB", Version: 5})
	c := NewCoordinator(a2, b2)
	rr := c.Read("k")
	if rr.Value != "BBB" {
		t.Fatalf("read-repair picked the wrong winner: got %q want %q", rr.Value, "BBB")
	}
	if len(rr.Repaired) == 0 || !rootsEqual(a2, b2) {
		t.Fatalf("read-repair did not converge equal-version replicas: repaired=%v rootsEqual=%v",
			rr.Repaired, rootsEqual(a2, b2))
	}

	t.Logf("run=%s FIX=equal-version-conflict-converges A=%q B=%q rootsEqual=%v "+
		"reconcileMoved=%d secondRoundMoved=%d readWinner=%q readRepaired=%v",
		advUUID, va.Value, vb.Value, rootsEqual(a, b), res.totalEntries, res2.totalEntries,
		rr.Value, rr.Repaired)
}
