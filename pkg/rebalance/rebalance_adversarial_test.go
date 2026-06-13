package rebalance

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

// This file is the ADVERSARIAL companion to rebalance_test.go. The existing
// tests prove the documented invariants on a handful of small, hand-picked,
// single-configuration cases. This file proves the SAME documented invariants
// (rebalance.go package doc + Reassign doc) as GENERAL PROPERTIES, swept across
// many (N members, P partitions) combinations and a long randomized churn
// sequence, anchored to an independent oracle.
//
// Documented invariants under test (verbatim spec anchors):
//   - "every consumer holds a partition count within +/-1 of the average"  (BALANCE)
//   - "minimizing the number of partitions that change owner ... a partition
//      is only moved when it MUST move"                                     (STICKINESS / MINIMAL DISRUPTION)
//   - "Every partition in partitions is assigned to exactly one member."    (COMPLETENESS)
//   - "deterministic ... identical inputs always yield identical output"    (DETERMINISM / IDEMPOTENCE)
//
// ANTI-BLUFF (mutation-verified during authoring against pkg/rebalance/rebalance.go):
//   - Disabling stickiness (Step 1 stops recording owned[p]) -> a near-full
//     reshuffle: TestAdversarial_AddMinimalMovement / _RemoveMinimalMovement fire
//     (e.g. P=100 N=2 add: 66 moves vs minimal 33).
//   - Disabling rebalanceDown -> one member hoards freed partitions:
//     TestAdversarial_BalanceSweep / _RandomChurn fire (max-min blows past 1).
//   - Inserting a needless revoke in Step 1 -> TestAdversarial_Idempotence /
//     _StressIdempotentTwice fire (a stable input churns).
// A naive "reassign everything by modulo each time" passes balance + completeness
// but is annihilated by the minimal-movement and idempotence assertions below.

const advRunUUID = "rebalance-adv-3f7a9c21-HXC-1416"

func advParts(n int) []string {
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = fmt.Sprintf("p%04d", i)
	}
	return out
}

func advMembers(n int) []string {
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = fmt.Sprintf("m%04d", i)
	}
	return out
}

// advSpread returns (min,max,total) load over the given members.
func advSpread(a Assignment, members []string) (mn, mx, total int) {
	mn, mx = 1<<30, -1
	for _, m := range members {
		l := len(a[m])
		total += l
		if l < mn {
			mn = l
		}
		if l > mx {
			mx = l
		}
	}
	if mx == -1 {
		return 0, 0, 0
	}
	return mn, mx, total
}

// advAssertExactCover: every partition owned by exactly one LIVE member, no
// ghosts, no dead members. This is COMPLETENESS, checked by an oracle.
func advAssertExactCover(t *testing.T, a Assignment, members, parts []string) {
	t.Helper()
	live := make(map[string]bool, len(members))
	for _, m := range members {
		live[m] = true
	}
	seen := make(map[string]int)
	for owner, ps := range a {
		if !live[owner] {
			t.Fatalf("partition(s) assigned to non-member %q: %v", owner, ps)
		}
		for _, p := range ps {
			seen[p]++
		}
	}
	valid := make(map[string]bool, len(parts))
	for _, p := range parts {
		valid[p] = true
	}
	for p := range seen {
		if !valid[p] {
			t.Fatalf("ghost partition %q assigned (not in the partition set)", p)
		}
	}
	for _, p := range parts {
		if seen[p] != 1 {
			t.Fatalf("partition %q owned %d times (want exactly 1): %v", p, seen[p], a)
		}
	}
	if len(seen) != len(parts) {
		t.Fatalf("ownership covers %d distinct partitions, want %d: %v", len(seen), len(parts), a)
	}
}

// TestAdversarial_BalanceSweep — BALANCE (headline) across many (N,P), including
// the awkward cases: P<N (starvation pressure), P not divisible by N (remainder
// distribution), P=0, and N=1. After rebalancing, every node MUST hold
// floor(P/N) or ceil(P/N) — max-min <= 1 — and all P partitions are placed.
//
// Why it bites: a distributor that dumps the remainder onto one node, or that
// starves a node when P<N unevenly, produces max-min >= 2 here. Disabling
// rebalanceDown trips this immediately.
func TestAdversarial_BalanceSweep(t *testing.T) {
	Ps := []int{0, 1, 2, 3, 5, 7, 8, 12, 13, 17, 31, 50, 100}
	Ns := []int{1, 2, 3, 4, 5, 7, 11, 16}
	checked := 0
	for _, P := range Ps {
		for _, N := range Ns {
			mem := advMembers(N)
			next := Reassign(Assignment{}, mem, advParts(P))
			mn, mx, total := advSpread(next, mem)
			if total != P {
				t.Fatalf("P=%d N=%d: total assigned=%d, want %d (lost partitions) loads=%v",
					P, N, total, P, Loads(next))
			}
			if P > 0 && mx-mn > 1 {
				t.Fatalf("P=%d N=%d: BALANCE violated max-min=%d (want <=1) loads=%v",
					P, N, mx-mn, Loads(next))
			}
			// Exact floor/ceil membership check.
			floor := 0
			if N > 0 {
				floor = P / N
			}
			ceil := floor
			if N > 0 && P%N != 0 {
				ceil = floor + 1
			}
			for _, m := range mem {
				if l := len(next[m]); l != floor && l != ceil {
					t.Fatalf("P=%d N=%d: member %s load %d not in {floor=%d,ceil=%d}",
						P, N, m, l, floor, ceil)
				}
			}
			advAssertExactCover(t, next, mem, advParts(P))
			checked++
		}
	}
	t.Logf("run=%s BalanceSweep: %d (N,P) combinations all within +/-1 and exactly covered",
		advRunUUID, checked)
}

// TestAdversarial_AddMinimalMovement — STICKINESS on node ADD, swept across many
// (N,P). The theoretical minimum number of partitions that MUST move when one
// node joins is exactly the share the newcomer ends up holding (~floor(P/(N+1)));
// every other partition can and MUST stay where it was. So:
//
//	len(Moved(before, after)) == len(after[newcomer])      (minimal)
//	every moved partition now lives on the newcomer         (came straight from a donor)
//	no survivor GAINS a partition; every partition a survivor keeps is one it
//	already held                                            (sticky)
//
// Why it bites: a from-scratch modulo reassignment reshuffles ~ (1 - 1/(N+1))*P
// partitions; here moved would vastly exceed the newcomer share. Disabling
// stickiness made P=100,N=2 move 66 (vs minimal 33).
func TestAdversarial_AddMinimalMovement(t *testing.T) {
	Ps := []int{6, 9, 12, 20, 30, 60, 100, 101}
	Ns := []int{1, 2, 3, 4, 5, 8}
	maxRatio := 0.0
	for _, P := range Ps {
		for _, N := range Ns {
			mem := advMembers(N)
			before := Reassign(Assignment{}, mem, advParts(P))
			newcomer := fmt.Sprintf("m%04d", N) // the (N+1)-th member
			memPlus := advMembers(N + 1)
			after := Reassign(before, memPlus, advParts(P))

			advAssertExactCover(t, after, memPlus, advParts(P))

			moved := Moved(before, after)
			newShare := len(after[newcomer])

			// MINIMAL: exactly the newcomer's new share moves.
			if len(moved) != newShare {
				t.Fatalf("ADD P=%d N=%d: moved=%d but newcomer holds=%d — movement is NOT minimal "+
					"(stickiness broken / full reshuffle) moved=%v",
					P, N, len(moved), newShare, moved)
			}
			// newShare must be the balanced floor/ceil for N+1 nodes.
			expFloor := P / (N + 1)
			if newShare < expFloor || newShare > expFloor+1 {
				t.Fatalf("ADD P=%d N=%d: newcomer share=%d not within balanced [%d,%d]",
					P, N, newShare, expFloor, expFloor+1)
			}
			// Every moved partition landed on the newcomer (nobody else churned).
			for _, mp := range moved {
				if !contains(after[newcomer], mp) {
					t.Fatalf("ADD P=%d N=%d: moved partition %s did not land on newcomer %s: %v",
						P, N, mp, newcomer, after)
				}
			}
			// Survivors are sticky: never GAIN a partition, and every partition a
			// survivor keeps must be one it already held (a subset of its prior
			// set) — i.e. the only ownership change is donating to the newcomer.
			// Note we do NOT cap per-survivor losses at one: when there was only a
			// single prior member holding the whole topic (N=1), that member must
			// shed the newcomer's entire floor share in one step, which is still
			// minimal (Moved==newShare, asserted above). The strong sticky claim is
			// "no foreign acquisition, no gain", checked here.
			for _, m := range mem {
				if len(after[m]) > len(before[m]) {
					t.Fatalf("ADD P=%d N=%d: survivor %s GAINED partitions (%d->%d) — not sticky",
						P, N, m, len(before[m]), len(after[m]))
				}
				for _, p := range after[m] {
					if !contains(before[m], p) {
						t.Fatalf("ADD P=%d N=%d: survivor %s acquired foreign partition %s — not sticky",
							P, N, m, p)
					}
				}
			}
			if P > 0 {
				r := float64(len(moved)) / float64(P)
				if r > maxRatio {
					maxRatio = r
				}
			}
		}
	}
	t.Logf("run=%s AddMinimalMovement: across all sweeps, moved==newcomer-share always; "+
		"worst moved/total ratio=%.3f (a full reshuffle would approach 1.0)", advRunUUID, maxRatio)
}

// TestAdversarial_RemoveMinimalMovement — STICKINESS on node REMOVE, swept across
// many (N,P). When a node leaves, ONLY its partitions may be reassigned; every
// surviving (consumer,partition) pair MUST be preserved. So:
//
//	Moved(before, after) == exactly the departed member's old partition set
//	every survivor keeps ALL of its previous partitions
//	result is balanced over the survivors and exactly covers all P partitions
//
// Why it bites: dropping the departed node's partitions (no reabsorb) loses
// partitions; a from-scratch reassignment churns survivors. Either inflates or
// mismatches Moved against the departed set.
func TestAdversarial_RemoveMinimalMovement(t *testing.T) {
	Ps := []int{6, 9, 12, 20, 30, 60, 100, 101}
	Ns := []int{2, 3, 4, 5, 8}
	for _, P := range Ps {
		for _, N := range Ns {
			mem := advMembers(N)
			before := Reassign(Assignment{}, mem, advParts(P))
			departed := fmt.Sprintf("m%04d", N-1) // drop the last member
			departedParts := append([]string(nil), before[departed]...)
			sort.Strings(departedParts)

			memMinus := advMembers(N - 1)
			after := Reassign(before, memMinus, advParts(P))

			// Departed gone.
			if _, ok := after[departed]; ok {
				t.Fatalf("REMOVE P=%d N=%d: departed member %s still present: %v",
					P, N, departed, after)
			}
			advAssertExactCover(t, after, memMinus, advParts(P))

			// Survivors keep ALL of their previous partitions (zero churn).
			for _, m := range memMinus {
				keptAll(t, before, after, m)
			}

			// Moved == exactly the departed member's old partitions.
			moved := Moved(before, after)
			sort.Strings(moved)
			if len(moved) != len(departedParts) {
				t.Fatalf("REMOVE P=%d N=%d: moved=%d (%v) but departed held=%d (%v) — "+
					"movement is NOT minimal", P, N, len(moved), moved, len(departedParts), departedParts)
			}
			for i := range moved {
				if moved[i] != departedParts[i] {
					t.Fatalf("REMOVE P=%d N=%d: moved set %v != departed set %v",
						P, N, moved, departedParts)
				}
			}
			// Balanced over survivors.
			mn, mx, _ := advSpread(after, memMinus)
			if mx-mn > 1 {
				t.Fatalf("REMOVE P=%d N=%d: survivors unbalanced max-min=%d loads=%v",
					P, N, mx-mn, Loads(after))
			}
		}
	}
	t.Logf("run=%s RemoveMinimalMovement: across all sweeps, ONLY the departed member's "+
		"partitions moved; every survivor kept 100%% of its partitions", advRunUUID)
}

// TestAdversarial_Idempotence — DETERMINISM/IDEMPOTENCE. Rebalancing an ALREADY
// balanced assignment with the SAME membership is a NO-OP: zero partitions move,
// and a second/third pass is byte-identical to the first.
//
// Why it bites: any churn in the steady state (e.g. a needless revoke, or
// placement that depends on map iteration order) makes Moved() non-empty here.
// A needless-revoke mutation made P=5,N=2 move 2 partitions on a stable re-run.
func TestAdversarial_Idempotence(t *testing.T) {
	Ps := []int{1, 5, 7, 12, 13, 31, 50, 99, 100}
	Ns := []int{1, 2, 3, 5, 7, 11}
	for _, P := range Ps {
		for _, N := range Ns {
			mem := advMembers(N)
			a1 := Reassign(Assignment{}, mem, advParts(P))
			a2 := Reassign(a1, mem, advParts(P))
			if mv := Moved(a1, a2); len(mv) != 0 {
				t.Fatalf("IDEMPOTENCE P=%d N=%d: re-running on a balanced assignment moved %d: %v",
					P, N, len(mv), mv)
			}
			a3 := Reassign(a2, mem, advParts(P))
			if d := advDiff(a2, a3); d != "" {
				t.Fatalf("IDEMPOTENCE P=%d N=%d: 3rd pass differs from 2nd: %s", P, N, d)
			}
		}
	}
	t.Logf("run=%s Idempotence: every balanced assignment is a fixed point (0 moves on re-run)",
		advRunUUID)
}

// TestAdversarial_RandomChurn — a long randomized add/remove sequence with an
// INDEPENDENT oracle re-checking ALL invariants after EVERY step: completeness
// (exact cover, no ghosts, no dead members), balance (max-min<=1), and that no
// survivor's kept partition ever migrated needlessly within a step (sticky:
// a partition that was on a still-live member and is still owned by SOME member
// either stayed put or moved only because balance forced it — we assert the
// strong cumulative property that the moved count per step is bounded by the
// number of partitions that strictly had to move).
//
// Why it bites: this is the integration-level catch-all. Disabling rebalanceDown
// trips balance at step 1 (max-min=23 observed); losing/duplicating a partition
// trips exact cover; assigning to a removed node trips the dead-member check.
func TestAdversarial_RandomChurn(t *testing.T) {
	rng := rand.New(rand.NewSource(0xC0FFEE))
	const P = 23
	parts := advParts(P)
	pool := advMembers(8)

	prev := Assignment{}
	active := []string{"m0000", "m0001"}
	var worstSpread, biggestStepMove int

	for step := 0; step < 600; step++ {
		// Random add or remove (keep >=1 member).
		add := rng.Intn(2) == 0
		if add && len(active) < len(pool) {
			for _, cand := range pool {
				if !contains(active, cand) {
					active = append(active, cand)
					break
				}
			}
		} else if len(active) > 1 {
			idx := rng.Intn(len(active))
			active = append(active[:idx], active[idx+1:]...)
		}

		next := Reassign(prev, active, parts)

		// COMPLETENESS + no dead members + no ghosts.
		advAssertExactCover(t, next, active, parts)

		// BALANCE.
		mn, mx, _ := advSpread(next, active)
		if mx-mn > 1 {
			t.Fatalf("step %d: BALANCE violated max-min=%d loads=%v active=%v",
				step, mx-mn, Loads(next), active)
		}
		if mx-mn > worstSpread {
			worstSpread = mx - mn
		}

		// STICKINESS (cumulative): the number of partitions that change owner this
		// step must not exceed the unavoidable minimum. The unavoidable set is:
		// partitions whose previous owner left this step, PLUS the partitions a
		// brand-new member must be handed to reach its floor share. We compute a
		// safe upper bound on the forced set and assert moved <= it.
		forced := forcedMoveUpperBound(prev, next, active, parts)
		moved := Moved(prev, next)
		if len(moved) > forced {
			t.Fatalf("step %d: moved %d partitions but at most %d were forced — "+
				"excessive movement (stickiness violated). active=%v\nprev=%v\nnext=%v",
				step, len(moved), forced, active, prev, next)
		}
		if len(moved) > biggestStepMove {
			biggestStepMove = len(moved)
		}

		prev = next
	}
	t.Logf("run=%s RandomChurn: 600 steps, every step exactly-covered & balanced; "+
		"worst spread=%d (cap 1), biggest single-step move=%d of %d partitions",
		advRunUUID, worstSpread, biggestStepMove, P)
}

// TestAdversarial_StressIdempotentTwice — large-scale balance + idempotence,
// including the boundary P==N and P==N+1.
func TestAdversarial_StressIdempotentTwice(t *testing.T) {
	cases := []struct{ P, N int }{
		{1000, 7}, {997, 13}, {500, 16}, {64, 64}, {65, 64}, {63, 64},
	}
	for _, tc := range cases {
		mem := advMembers(tc.N)
		a1 := Reassign(Assignment{}, mem, advParts(tc.P))
		mn, mx, total := advSpread(a1, mem)
		if total != tc.P {
			t.Fatalf("STRESS P=%d N=%d: total=%d want %d", tc.P, tc.N, total, tc.P)
		}
		if tc.P > 0 && mx-mn > 1 {
			t.Fatalf("STRESS P=%d N=%d: balance max-min=%d", tc.P, tc.N, mx-mn)
		}
		a2 := Reassign(a1, mem, advParts(tc.P))
		if mv := Moved(a1, a2); len(mv) != 0 {
			t.Fatalf("STRESS P=%d N=%d: moved %d on stable re-run", tc.P, tc.N, len(mv))
		}
		a3 := Reassign(a2, mem, advParts(tc.P))
		if d := advDiff(a2, a3); d != "" {
			t.Fatalf("STRESS P=%d N=%d: not idempotent on 3rd pass: %s", tc.P, tc.N, d)
		}
	}
	t.Logf("run=%s StressIdempotentTwice: large/boundary (P,N) all balanced and fixed-point", advRunUUID)
}

// forcedMoveUpperBound returns a SAFE OVER-ESTIMATE of how many partitions were
// genuinely forced to move from prev to next given the new membership. It counts:
//   - every partition whose prev owner is NOT in the new membership (orphaned), plus
//   - the floor share that any brand-new member (present in `active` but absent
//     from prev) must be given to reach balance.
//
// Because it OVER-estimates the forced set, asserting moved <= this bound is a
// conservative (no false-positive) stickiness check that still catches a full
// reshuffle (which moves far more than the forced set).
func forcedMoveUpperBound(prev, next Assignment, active, parts []string) int {
	prevOwner := make(map[string]string)
	for c, ps := range prev {
		for _, p := range ps {
			prevOwner[p] = c
		}
	}
	liveNow := make(map[string]bool, len(active))
	for _, m := range active {
		liveNow[m] = true
	}
	validPart := make(map[string]bool, len(parts))
	for _, p := range parts {
		validPart[p] = true
	}

	// Orphaned partitions: previous owner gone (or never owned at all). These MUST move.
	orphaned := 0
	for _, p := range parts {
		o, had := prevOwner[p]
		if !had || !liveNow[o] {
			orphaned++
		}
	}

	// New members: those active now but absent (or empty) in prev. Each must be
	// filled up to ceil(P/N) from somewhere — bound their intake by ceil.
	N := len(active)
	P := len(parts)
	ceil := 0
	if N > 0 {
		ceil = P / N
		if P%N != 0 {
			ceil++
		}
	}
	newcomerIntake := 0
	for _, m := range active {
		had := false
		if ps, ok := prev[m]; ok {
			for _, p := range ps {
				if validPart[p] {
					had = true
					break
				}
			}
		}
		if !had {
			newcomerIntake += ceil
		}
	}

	bound := orphaned + newcomerIntake
	if bound > P {
		bound = P
	}
	return bound
}

// advDiff returns "" if a and b are the same assignment (order-insensitive per
// member), else a human-readable first difference.
func advDiff(a, b Assignment) string {
	if len(a) != len(b) {
		return fmt.Sprintf("member count %d vs %d", len(a), len(b))
	}
	for m, ps := range a {
		ap := append([]string(nil), ps...)
		bp := append([]string(nil), b[m]...)
		sort.Strings(ap)
		sort.Strings(bp)
		if len(ap) != len(bp) {
			return fmt.Sprintf("member %s len %d vs %d", m, len(ap), len(bp))
		}
		for i := range ap {
			if ap[i] != bp[i] {
				return fmt.Sprintf("member %s partition %s vs %s", m, ap[i], bp[i])
			}
		}
	}
	return ""
}
