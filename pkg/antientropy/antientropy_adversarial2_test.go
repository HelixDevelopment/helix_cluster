package antientropy

// Second adversarial pass (HXC-1410 deep pass 2). The first adversarial file
// pinned the equal-version *two-replica* tiebreak (HXC-1759). This file attacks
// the OTHER convergence surfaces that pass missed, with mutation-proven,
// sink-side assertions:
//
//   1. THREE-WAY, ORDER-INDEPENDENT convergence: N replicas with equal-version
//      AND differing-version conflicts must converge to the SAME state regardless
//      of the pairwise reconcile order/permutation. A non-total precedence would
//      diverge here even though the 2-replica case converges.
//   2. CROSS-PATH AGREEMENT: the read-repair path (Coordinator.Read) and the
//      reconcile path must pick the SAME winning VALUE for every conflict shape.
//      If they disagreed, a cluster would converge to different states depending
//      on which anti-entropy mechanism ran — silent divergence.
//   3. apply COMMUTATIVITY: replaying the same multiset of writes in any order
//      yields the same winner (precedence is a total order => commutative).
//   4. STALE-HINT NON-CLOBBER: a buffered hint must never overwrite a fresher
//      value the replica took directly after recovery (version-aware apply).
//   5. CONTRACT PIN — value-based convergence: Snapshot()/Merkle deliberately
//      hash (key,value) and DROP the version, so version-only differences are
//      intentionally invisible to reconcile. This is pinned so a future "fix"
//      that makes reconcile chase versions (breaking the O(differences) Merkle
//      contract) is caught. read-repair still agrees on the user-visible VALUE.
//   6. CONTRACT PIN — single-threaded model: the package ships NO concurrency
//      primitives (plain maps); concurrency is a documented non-contract. A
//      single-threaded reconcile/read sequence is consistent. (A -race probe
//      confirmed Coordinator.Read mutates under read-repair, so concurrent use
//      races — LATENT RISK, recorded, NOT silently "fixed" with a mutex since
//      that is a feature change beyond the stated contract.)

import (
	"fmt"
	"testing"
)

const adv2UUID = "antientropy-adversarial2-HXC-1410-pass2"

func root(r *Replica) string { return fmt.Sprintf("%x", NewMerkleTree(r.Snapshot()).Root()) }

// reconcileToFixedPoint runs all-pairs reconcile rounds until nothing moves.
func reconcileToFixedPoint(t *testing.T, rs []*Replica) {
	t.Helper()
	for round := 0; round < 20; round++ {
		moved := 0
		for i := 0; i < len(rs); i++ {
			for j := i + 1; j < len(rs); j++ {
				moved += reconcile(rs[i], rs[j]).totalEntries
			}
		}
		if moved == 0 {
			return
		}
	}
	t.Fatalf("did not reach a fixed point in 20 rounds (oscillation => non-total precedence)")
}

// TestAE2_ThreeWayConvergence_OrderIndependent is the centerpiece: three
// replicas carrying equal-version conflicts (the dangerous case), version-skew
// conflicts, and one-sided keys must converge to a SINGLE state, and that state
// must be IDENTICAL for two opposite pairwise-reconcile orderings.
//
// Mutation that bites: removing the equal-version value tiebreak from newer()
// makes the "conf" key (A=AAA@5 B=BBB@5 C=CCC@5) un-healable — replicas never
// reach a fixed point / never share a root, failing here even though a flat
// 2-replica equal-version test might be arranged to pass.
func TestAE2_ThreeWayConvergence_OrderIndependent(t *testing.T) {
	build := func() (*Replica, *Replica, *Replica) {
		a, b, c := NewReplica("A"), NewReplica("B"), NewReplica("C")
		for i := 0; i < 8; i++ {
			k := fmt.Sprintf("s%02d", i)
			vv := VersionedValue{Value: fmt.Sprintf("v%02d", i), Version: 1}
			a.apply(k, vv)
			b.apply(k, vv)
			c.apply(k, vv)
		}
		// Equal-version, three distinct values: only a total order can heal this.
		a.apply("conf", VersionedValue{Value: "AAA", Version: 5})
		b.apply("conf", VersionedValue{Value: "BBB", Version: 5})
		c.apply("conf", VersionedValue{Value: "CCC", Version: 5})
		// Version-skew conflict: numeric max must win.
		a.apply("ver", VersionedValue{Value: "a", Version: 3})
		b.apply("ver", VersionedValue{Value: "b", Version: 7})
		c.apply("ver", VersionedValue{Value: "c", Version: 5})
		// One-sided keys, both directions.
		a.apply("onlyA", VersionedValue{Value: "x", Version: 1})
		c.apply("onlyC", VersionedValue{Value: "z", Version: 1})
		return a, b, c
	}

	a1, b1, c1 := build()
	reconcileToFixedPoint(t, []*Replica{a1, b1, c1})
	if root(a1) != root(b1) || root(b1) != root(c1) {
		t.Fatalf("order1: 3 replicas did not converge: A=%s B=%s C=%s", root(a1), root(b1), root(c1))
	}

	a2, b2, c2 := build()
	reconcileToFixedPoint(t, []*Replica{c2, b2, a2}) // opposite pair ordering
	if root(a2) != root(b2) || root(b2) != root(c2) {
		t.Fatalf("order2: 3 replicas did not converge: A=%s B=%s C=%s", root(a2), root(b2), root(c2))
	}

	if root(a1) != root(a2) {
		ca, _ := a1.Get("conf")
		cb, _ := a2.Get("conf")
		t.Fatalf("DIVERGENCE BY ORDER: order1 root=%s order2 root=%s (conf: %q vs %q)",
			root(a1), root(a2), ca.Value, cb.Value)
	}
	// Sink-side: the deterministic winners (lex-max value @ equal version; numeric-max version).
	conf, _ := a1.Get("conf")
	ver, _ := a1.Get("ver")
	if conf.Value != "CCC" {
		t.Fatalf("equal-version 3-way winner wrong: got %q want CCC (lex-max)", conf.Value)
	}
	if ver.Value != "b" || ver.Version != 7 {
		t.Fatalf("version-skew winner wrong: got %q@%d want b@7", ver.Value, ver.Version)
	}
	t.Logf("run=%s threeWay converged root=%s conf=%q ver=%q@%d",
		adv2UUID, root(a1), conf.Value, ver.Value, ver.Version)
}

// TestAE2_ReadAndReconcileAgreeOnValue proves the two independent anti-entropy
// paths NEVER converge a conflict to different user-visible VALUES. (Version-only
// differences are intentionally invisible to value-based Merkle — see the
// contract-pin test below — so we compare the VALUE end users observe.)
//
// Mutation that bites: removing the equal-version value tiebreak makes reconcile
// leave equal-version conflicts unhealed (values stay split) while read-repair
// still folds them via its first-pass scan order — the two paths then disagree,
// firing the "reconcile left VALUES divergent" / "VALUE DISAGREE" guards.
func TestAE2_ReadAndReconcileAgreeOnValue(t *testing.T) {
	vals := []string{"a", "b", "z"}
	vers := []uint64{1, 2, 3}
	versionOnly := 0
	for _, av := range vals {
		for _, aver := range vers {
			for _, bv := range vals {
				for _, bver := range vers {
					a, b := NewReplica("A"), NewReplica("B")
					a.apply("k", VersionedValue{Value: av, Version: aver})
					b.apply("k", VersionedValue{Value: bv, Version: bver})
					reconcile(a, b)
					ra, _ := a.Get("k")
					rb, _ := b.Get("k")
					if ra.Value != rb.Value {
						t.Fatalf("reconcile left VALUES divergent: A=%s@%d B=%s@%d => A=%q B=%q",
							av, aver, bv, bver, ra.Value, rb.Value)
					}

					a2, b2 := NewReplica("A"), NewReplica("B")
					a2.apply("k", VersionedValue{Value: av, Version: aver})
					b2.apply("k", VersionedValue{Value: bv, Version: bver})
					rr := NewCoordinator(a2, b2).Read("k")

					if ra.Value != rr.Value {
						t.Fatalf("VALUE DISAGREE A=%s@%d B=%s@%d: reconcile=%q read=%q",
							av, aver, bv, bver, ra.Value, rr.Value)
					}
					if ra.Version != rr.Version {
						versionOnly++ // same value, version metadata differs — allowed
					}
				}
			}
		}
	}
	t.Logf("run=%s read/reconcile value-agreement: 0 value disagreements, %d version-only (expected)",
		adv2UUID, versionOnly)
}

// TestAE2_ApplyCommutative proves precedence is a TOTAL ORDER by showing apply is
// commutative: any rotation of the same write multiset yields the same winner.
//
// Mutation that bites: without a total order, an equal-version pair has no
// deterministic winner, so the surviving value depends on arrival order and the
// rotations disagree.
func TestAE2_ApplyCommutative(t *testing.T) {
	writes := []VersionedValue{
		{Value: "a", Version: 1}, {Value: "z", Version: 1}, // equal-version pair
		{Value: "m", Version: 3}, {Value: "b", Version: 3}, // equal-version pair (higher)
		{Value: "q", Version: 2},
	}
	ref := NewReplica("ref")
	for _, w := range writes {
		ref.apply("k", w)
	}
	want, _ := ref.Get("k")
	if want.Value != "m" || want.Version != 3 {
		t.Fatalf("reference winner wrong: got %+v want m@3", want)
	}
	for shift := 0; shift < len(writes); shift++ {
		r := NewReplica("r")
		for i := 0; i < len(writes); i++ {
			r.apply("k", writes[(i+shift)%len(writes)])
		}
		got, _ := r.Get("k")
		if got != want {
			t.Fatalf("non-commutative apply: shift=%d got %+v want %+v (precedence not total)", shift, got, want)
		}
	}
	t.Logf("run=%s apply commutative; winner=%+v", adv2UUID, want)
}

// TestAE2_StaleHintNeverClobbers proves hinted-handoff replay respects the
// version-aware apply: a hint buffered while down must NOT overwrite a fresher
// value the replica accepted directly after recovery.
//
// Mutation that bites: if replayHints wrote hints unconditionally (bypassing
// apply's last-write-wins), the stale old@1 hint would clobber the fresh new@5.
func TestAE2_StaleHintNeverClobbers(t *testing.T) {
	other := NewReplica("o")
	r := NewReplica("r")
	c := NewCoordinator(other, r)

	r.SetUp(false)
	c.Write("k", "old", 1) // buffered hint for r
	if n := c.PendingHints("r"); n != 1 {
		t.Fatalf("expected 1 buffered hint, got %d", n)
	}
	r.SetUp(true)
	c.Write("k", "new", 5) // r takes the fresher write directly
	if vv, _ := r.Get("k"); vv.Value != "new" || vv.Version != 5 {
		t.Fatalf("precondition: r should hold new@5, got %+v", vv)
	}
	delivered := c.ReplayHints(r)
	if delivered != 1 {
		t.Fatalf("expected 1 hint replayed, got %d", delivered)
	}
	vv, _ := r.Get("k")
	if vv.Value != "new" || vv.Version != 5 {
		t.Fatalf("CLOBBER: stale hint old@1 overwrote fresh new@5, got %+v", vv)
	}
	if n := c.PendingHints("r"); n != 0 {
		t.Fatalf("hint buffer must be cleared after replay, got %d", n)
	}
	t.Logf("run=%s stale hint rejected; r holds %+v", adv2UUID, vv)
}

// TestAE2_Contract_ValueBasedConvergence PINS a deliberate design boundary:
// Snapshot()/Merkle hash (key,value) and DROP the version, so two replicas that
// agree on VALUE but differ in VERSION are "converged" (equal Merkle root) and
// reconcile is intentionally a no-op for them. This is the O(differences)
// efficiency contract. If a future change makes Snapshot/Merkle version-aware,
// this test fires — forcing an explicit, reviewed decision rather than a silent
// blow-up of the transfer cost.
func TestAE2_Contract_ValueBasedConvergence(t *testing.T) {
	a, b := NewReplica("A"), NewReplica("B")
	a.apply("k", VersionedValue{Value: "same", Version: 1})
	b.apply("k", VersionedValue{Value: "same", Version: 9}) // same value, different version

	if root(a) != root(b) {
		t.Fatalf("CONTRACT CHANGED: value-equal/version-skew replicas have different Merkle roots; "+
			"Merkle is now version-aware (A=%s B=%s) — reconsider the O(differences) transfer contract",
			root(a), root(b))
	}
	res := reconcile(a, b)
	if res.totalEntries != 0 || len(res.diffKeys) != 0 {
		t.Fatalf("CONTRACT CHANGED: reconcile now transfers version-only differences "+
			"(moved=%d diff=%v); this breaks value-based convergence", res.totalEntries, res.diffKeys)
	}
	// Versions stay as they were — reconcile did not touch them.
	va, _ := a.Get("k")
	vb, _ := b.Get("k")
	if va.Version != 1 || vb.Version != 9 || va.Value != "same" || vb.Value != "same" {
		t.Fatalf("unexpected post-state A=%+v B=%+v", va, vb)
	}
	// read-repair, by contrast, IS version-aware and folds to the higher version,
	// but to the SAME value — so end users still see one consistent value.
	rr := NewCoordinator(NewReplica("A2"), NewReplica("B2"))
	rr.replicas[0].apply("k", VersionedValue{Value: "same", Version: 1})
	rr.replicas[1].apply("k", VersionedValue{Value: "same", Version: 9})
	got := rr.Read("k")
	if got.Value != "same" {
		t.Fatalf("read-repair value disagreed with reconcile: got %q want same", got.Value)
	}
	t.Logf("run=%s value-based convergence pinned: reconcile no-op on version-only skew, "+
		"read winner value=%q@%d", adv2UUID, got.Value, got.Version)
}

// TestAE2_Contract_SingleThreadedConsistency PINS the single-threaded model:
// the package ships NO concurrency primitives (plain maps), so concurrency is a
// documented NON-contract. Used as designed (one goroutine), a Read followed by
// reconcile is fully consistent. A separate -race probe confirmed Coordinator.Read
// mutates under read-repair, so concurrent callers race — recorded as a LATENT
// RISK; no mutex is added here because that is a feature beyond the stated
// contract.
func TestAE2_Contract_SingleThreadedConsistency(t *testing.T) {
	a, b, d := NewReplica("a"), NewReplica("b"), NewReplica("d")
	a.apply("k", VersionedValue{Value: "old", Version: 1})
	b.apply("k", VersionedValue{Value: "NEW", Version: 9})
	d.apply("k", VersionedValue{Value: "old", Version: 1})
	c := NewCoordinator(a, b, d)

	res := c.Read("k")
	if res.Value != "NEW" || res.Version != 9 {
		t.Fatalf("read winner wrong: got %q@%d want NEW@9", res.Value, res.Version)
	}
	// Read repaired all up replicas to the newest value (sink-side).
	for _, r := range []*Replica{a, b, d} {
		if vv, _ := r.Get("k"); vv.Value != "NEW" || vv.Version != 9 {
			t.Fatalf("read-repair did not converge %s: %+v", r.ID, vv)
		}
	}
	// A following reconcile is a clean no-op (already converged).
	if r := reconcile(a, b); r.totalEntries != 0 {
		t.Fatalf("post-read reconcile should be a no-op, moved %d", r.totalEntries)
	}
	t.Logf("run=%s single-threaded read+reconcile consistent; repaired=%v", adv2UUID, res.Repaired)
}
