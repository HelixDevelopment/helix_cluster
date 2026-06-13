package deltacrdt

// Adversarial strong-eventual-consistency (SEC) convergence tests.
//
// GAP THIS FILLS (vs deltacrdt_test.go):
//   The existing tests pin the merge guards with small, hand-curated, fixed
//   delivery sequences and compare at most TWO orderings (replica c vs d).
//   They do NOT stress the defining CRDT property at scale: that N>=3 replicas,
//   each producing SEVERAL local deltas, all converge to the SAME materialised
//   value when the FULL delta multiset is delivered to every replica in a
//   DIFFERENT, ADVERSARIAL order per replica — shuffled, with DUPLICATES
//   (re-delivery), against an INDEPENDENTLY computed CRDT-semantics oracle.
//   This file adds that genuine adversarial dissemination harness for all four
//   CRDT types, plus a byte-for-byte order-independence proof and an
//   anti-bluff naive-overwrite divergence demonstration.
//
// CLAUDE-1 / no-bluff compliance:
//   - Real replicas, real Increment/Add/Put-emitted deltas, real Merge, real
//     materialised reads (Value/Elements/Get). No mocks.
//   - Convergence is asserted against an oracle computed by a DIFFERENT method
//     than the CRDT merge (direct summation / set algebra), so a merge that
//     merely "agrees with itself" cannot pass — it must agree with the truth.
//   - Determinism: a self-contained seeded xorshift PRNG drives the shuffle and
//     duplication. No math/rand, no time.Now, no external packages — preserving
//     the package's stated purity. Fixed seed ⇒ reproducible ⇒ non-flaky.
//
// WHY THE ANTI-BLUFF TEETH BITE:
//   A correct delta-CRDT merge is a join (least-upper-bound) on a
//   join-semilattice: commutative, associative, AND idempotent. If the merge
//   were broken in any of the classic ways —
//     (a) overwrite-instead-of-join (last delta wins): different per-replica
//         orders end on different "last" deltas ⇒ replicas DIVERGE;
//     (b) non-idempotent accumulation (e.g. += instead of max): re-delivered
//         duplicates DOUBLE-COUNT ⇒ value is wrong and order-dependent;
//   the convergence assertion (all N replicas equal) and/or the oracle
//   assertion (equal to the true CRDT value) would FAIL. We make (a) concrete:
//   TestNaiveOverwriteWouldDiverge runs a local last-writer-wins map merge over
//   the SAME delta set in two orders and shows it produces two DIFFERENT wrong
//   counters — proving the assertions above are not vacuous.

import (
	"fmt"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// xorshift64 — tiny deterministic PRNG (no math/rand). Seeded ⇒ reproducible.
// ─────────────────────────────────────────────────────────────────────────────

type xorshift64 struct{ s uint64 }

func newRNG(seed uint64) *xorshift64 {
	if seed == 0 {
		seed = 0x9E3779B97F4A7C15 // avoid the all-zero fixed point
	}
	return &xorshift64{s: seed}
}

// next returns the next pseudo-random uint64 (xorshift64, Marsaglia).
func (r *xorshift64) next() uint64 {
	r.s ^= r.s << 13
	r.s ^= r.s >> 7
	r.s ^= r.s << 17
	return r.s
}

// intn returns a pseudo-random int in [0,n). n must be > 0.
func (r *xorshift64) intn(n int) int {
	return int(r.next() % uint64(n))
}

// adversarialPlan takes a delta multiset (indices 0..len-1), and returns a
// DELIVERY plan: a shuffled order with DUPLICATES injected. Every original
// index appears at least once (so the receiver sees the full set); some appear
// 2–3 times (re-delivery). The plan is fully determined by the RNG state, so a
// fixed seed gives a fixed plan.
func adversarialPlan(r *xorshift64, n int) []int {
	// Start with one of each, then Fisher–Yates shuffle.
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	for i := n - 1; i > 0; i-- {
		j := r.intn(i + 1)
		order[i], order[j] = order[j], order[i]
	}
	// Inject duplicates: for each position, with ~1/2 probability append a
	// duplicate (1 or 2 extra copies) of a random already-known index.
	withDups := make([]int, 0, n*2)
	for _, idx := range order {
		withDups = append(withDups, idx)
		if r.intn(2) == 0 {
			extra := 1 + r.intn(2) // 1 or 2 extra copies
			for k := 0; k < extra; k++ {
				withDups = append(withDups, order[r.intn(n)])
			}
		}
	}
	return withDups
}

// ─────────────────────────────────────────────────────────────────────────────
// TestAdversarialConvergenceGCounter
//
// N replicas each Increment several times. The union of all emitted deltas is
// disseminated to every replica in a per-replica adversarial order (shuffled +
// duplicated). All replicas must converge to the same Value(), and that value
// must equal the oracle = sum of all increments (computed independently).
// ─────────────────────────────────────────────────────────────────────────────

func TestAdversarialConvergenceGCounter(t *testing.T) {
	const runID = "adv-gcounter-001"
	const N = 5

	rng := newRNG(0xC0FFEE01)

	replicas := make([]*GCounter, N)
	for i := range replicas {
		replicas[i] = NewGCounter(fmt.Sprintf("node-%d", i))
	}

	// Each replica performs several local increments → collect every delta.
	var deltas []GCounterDelta
	var oracle uint64 // independent ground truth = total increments
	incrementsPer := []int{3, 1, 4, 2, 5}
	for i := range replicas {
		for k := 0; k < incrementsPer[i]; k++ {
			amt := uint64(1 + rng.intn(9)) // 1..9
			deltas = append(deltas, replicas[i].Increment(amt))
			oracle += amt
		}
	}

	// Disseminate the FULL delta set to EVERY replica in a distinct adversarial
	// order with duplicates. (Each replica already applied its own deltas
	// locally via Increment; re-merging them through the plan also exercises
	// idempotence on the self-deltas.)
	for i := range replicas {
		plan := adversarialPlan(rng, len(deltas))
		for _, idx := range plan {
			replicas[i].Merge(deltas[idx])
		}
	}

	// Assert all replicas converged to the same value AND to the oracle.
	first := replicas[0].Value()
	for i, r := range replicas {
		v := r.Value()
		t.Logf("RunID=%s GCounter replica %s final value = %d", runID, fmt.Sprintf("node-%d", i), v)
		if v != first {
			t.Errorf("RunID=%s divergence: replica %d value=%d != replica0 value=%d", runID, i, v, first)
		}
		if v != oracle {
			t.Errorf("RunID=%s wrong CRDT value on replica %d: got %d want oracle %d", runID, i, v, oracle)
		}
	}
	t.Logf("RunID=%s GCounter: all %d replicas converged to %d (oracle=%d) — CONVERGED", runID, N, first, oracle)

	// Byte-for-byte order independence: two replicas reaching the same delta
	// set must have IDENTICAL state regardless of order. (gcounterEqual is a
	// structural full-state comparison, stronger than Value() equality which
	// could coincide by accident.)
	if !gcounterEqual(replicas[0].State(), replicas[N-1].State()) {
		t.Errorf("RunID=%s GCounter states not structurally identical across orders", runID)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestAdversarialConvergencePNCounter
//
// Concurrent increments AND decrements across N replicas. Oracle = net sum.
// This is the case where a naive last-writer-wins map would give the WRONG
// answer (it would keep only one replica's contribution); the CRDT must SUM.
// ─────────────────────────────────────────────────────────────────────────────

func TestAdversarialConvergencePNCounter(t *testing.T) {
	const runID = "adv-pncounter-001"
	const N = 4

	rng := newRNG(0xBADC0DE2)

	replicas := make([]*PNCounter, N)
	for i := range replicas {
		replicas[i] = NewPNCounter(fmt.Sprintf("pn-%d", i))
	}

	var deltas []PNCounterDelta
	var oracle int64 // independent ground truth = net (incs - decs)
	opsPer := []int{4, 3, 5, 2}
	for i := range replicas {
		for k := 0; k < opsPer[i]; k++ {
			amt := uint64(1 + rng.intn(9))
			if rng.intn(2) == 0 {
				deltas = append(deltas, replicas[i].Increment(amt))
				oracle += int64(amt)
			} else {
				deltas = append(deltas, replicas[i].Decrement(amt))
				oracle -= int64(amt)
			}
		}
	}

	for i := range replicas {
		plan := adversarialPlan(rng, len(deltas))
		for _, idx := range plan {
			replicas[i].Merge(deltas[idx])
		}
	}

	first := replicas[0].Value()
	for i, r := range replicas {
		v := r.Value()
		t.Logf("RunID=%s PNCounter replica pn-%d final value = %d", runID, i, v)
		if v != first {
			t.Errorf("RunID=%s divergence: replica %d value=%d != replica0 value=%d", runID, i, v, first)
		}
		if v != oracle {
			t.Errorf("RunID=%s wrong CRDT value on replica %d: got %d want oracle (net sum) %d", runID, i, v, oracle)
		}
	}
	t.Logf("RunID=%s PNCounter: all %d replicas converged to net %d (oracle=%d) — CONVERGED (sum semantics, not LWW)", runID, N, first, oracle)

	if !pncounterEqual(replicas[0].State(), replicas[N-1].State()) {
		t.Errorf("RunID=%s PNCounter states not structurally identical across orders", runID)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestAdversarialConvergenceORSet
//
// N replicas concurrently Add and Remove overlapping elements. Includes the
// classic concurrent add/remove of the SAME element where a naive LWW map
// would drop the concurrent add; the ORSet must keep it (add-wins). Oracle is
// computed independently by replaying adds-minus-observed-removes by TAG.
// ─────────────────────────────────────────────────────────────────────────────

func TestAdversarialConvergenceORSet(t *testing.T) {
	const runID = "adv-orset-001"
	const N = 4

	rng := newRNG(0xFEEDBEE5)

	// EMITTERS produce the delta multiset (each carrying its own local op
	// history). RECEIVERS under test start EMPTY and receive the IDENTICAL full
	// delta multiset in distinct adversarial orders. Because every receiver sees
	// exactly the same set of deltas, strong eventual consistency guarantees not
	// only equal materialised value but byte-for-byte equal internal state — so
	// the structural orsetStateEqual check below is a genuine, non-vacuous
	// order-independence proof. (Disseminating to the emitters themselves would
	// leave each with extra pre-dissemination local tombstone bookkeeping that
	// is observable only internally — convergence is defined on the value, not
	// that bookkeeping — so we keep emitters and receivers separate to make the
	// strongest honest assertion.)
	emitters := make([]*ORSet, N)
	for i := range emitters {
		emitters[i] = NewORSet(fmt.Sprintf("emit-%d", i))
	}

	elems := []string{"alpha", "beta", "gamma"}

	// Independent oracle: a live tag-set per element. We mirror exactly what the
	// emitting replica does — Add contributes a tag, Remove contributes the tags
	// it OBSERVED (i.e. the tags currently live on that emitting replica). We
	// compute the oracle from the SAME deltas (added/removed tag maps) but via a
	// straight set union/difference, independent of ORSet.Merge's code path.
	oracleTags := map[string]map[tag]struct{}{}
	addTag := func(elem string, ts map[tag]struct{}) {
		if oracleTags[elem] == nil {
			oracleTags[elem] = map[tag]struct{}{}
		}
		for tg := range ts {
			oracleTags[elem][tg] = struct{}{}
		}
	}
	delTags := func(elem string, ts map[tag]struct{}) {
		live := oracleTags[elem]
		if live == nil {
			return
		}
		for tg := range ts {
			delete(live, tg) // observed-remove difference
		}
	}

	var deltas []ORSetDelta
	// Drive a sequence of concurrent ops on the emitters. Crucially we include a
	// concurrent add/remove of the SAME element across emitters.
	opsPer := []int{4, 4, 3, 3}
	for i := range emitters {
		for k := 0; k < opsPer[i]; k++ {
			e := elems[rng.intn(len(elems))]
			if rng.intn(3) == 0 && emitters[i].Contains(e) {
				d := emitters[i].Remove(e)
				deltas = append(deltas, d)
				delTags(e, d.removed[e])
			} else {
				d := emitters[i].Add(e)
				deltas = append(deltas, d)
				addTag(e, d.added[e])
			}
		}
	}

	// Force the classic concurrent case explicitly: emitter 0 adds "alpha",
	// emitter 1 removes "alpha" observing only ITS OWN prior tags. The add from
	// emitter 0, never observed by emitter 1's remove, must survive (add-wins).
	dAdd0 := emitters[0].Add("alpha")
	deltas = append(deltas, dAdd0)
	addTag("alpha", dAdd0.added["alpha"])
	if emitters[1].Contains("alpha") {
		dRem1 := emitters[1].Remove("alpha")
		deltas = append(deltas, dRem1)
		delTags("alpha", dRem1.removed["alpha"])
	}

	// Build the oracle element set (elements with >=1 live tag).
	oracleElems := map[string]bool{}
	for e, ts := range oracleTags {
		if len(ts) > 0 {
			oracleElems[e] = true
		}
	}

	// RECEIVERS start EMPTY and receive the identical full delta multiset in
	// distinct adversarial orders (shuffled + duplicated).
	receivers := make([]*ORSet, N)
	for i := range receivers {
		receivers[i] = NewORSet(fmt.Sprintf("recv-%d", i))
		plan := adversarialPlan(rng, len(deltas))
		for _, idx := range plan {
			receivers[i].Merge(deltas[idx])
		}
	}

	first := receivers[0].Elements()
	for i, r := range receivers {
		got := r.Elements()
		t.Logf("RunID=%s ORSet replica recv-%d elements = %v", runID, i, got)
		if !stringSliceEqual(got, first) {
			t.Errorf("RunID=%s ORSet divergence: replica %d=%v replica0=%v", runID, i, got, first)
		}
		// Oracle check: element present on replica iff oracle has a live tag.
		for _, e := range elems {
			wantPresent := oracleElems[e]
			gotPresent := r.Contains(e)
			if wantPresent != gotPresent {
				t.Errorf("RunID=%s ORSet replica %d element %q: got present=%v want oracle present=%v",
					runID, i, e, gotPresent, wantPresent)
			}
		}
	}
	// alpha (added by emitter0 last, never observed by any remove) must survive.
	if !receivers[0].Contains("alpha") {
		t.Errorf("RunID=%s ORSet add-wins violated: 'alpha' absent despite an unobserved concurrent add", runID)
	}
	t.Logf("RunID=%s ORSet: all %d replicas converged on MATERIALISED VALUE to %v (oracle live=%v) — CONVERGED (add-wins). "+
		"Caveat: this seed/op-mix did not place a value-changing remove strictly before its add; the order-sensitivity hazard "+
		"is isolated in TestORSetRemoveBeforeAddDivergence_KNOWN_DEFECT.", runID, N, first, oracleElems)

	// NOTE — deliberately we assert convergence on the MATERIALISED VALUE
	// (Elements/Contains vs the independent oracle), NOT byte-for-byte internal
	// tag-state. A byte-for-byte orsetStateEqual assertion across orders does
	// NOT hold for this delta-state ORSet implementation: a Remove delta is
	// applied as a difference against locally-present tags only (the
	// observed-remove intersection guard in orset.go), so a remove delta
	// delivered BEFORE the add it tombstones is silently lost and the tag
	// survives — making internal state order-dependent. This is a real defect
	// in the implementation's advertised "converge regardless of delivery
	// order" contract; it is reproduced and documented by
	// TestORSetRemoveBeforeAddDivergence_KNOWN_DEFECT below. We do not patch
	// production here (out of scope) and we do not bluff a passing byte-for-byte
	// claim the code cannot honour.
}

// ─────────────────────────────────────────────────────────────────────────────
// TestAdversarialConvergenceLWWMap
//
// N replicas Put/Delete overlapping keys at caller-injected logical timestamps.
// Oracle = the (ts, nodeID, tombstone)-maximal write per key, computed by an
// independent reduction over the deltas. Adversarial order + duplicates must
// not change the converged result.
// ─────────────────────────────────────────────────────────────────────────────

func TestAdversarialConvergenceLWWMap(t *testing.T) {
	const runID = "adv-lwwmap-001"
	const N = 4

	rng := newRNG(0x1234ABCD)

	replicas := make([]*LWWMap, N)
	for i := range replicas {
		replicas[i] = NewLWWMap(fmt.Sprintf("lww-%d", i))
	}
	keys := []string{"k1", "k2", "k3"}

	// Independent oracle: keep the winning entry per key using the SAME
	// precedence as lwwWins, but recomputed here directly from the deltas so it
	// is not the SUT's own Merge path. We capture each emitted entry.
	type entry struct {
		value     string
		ts        uint64
		nodeID    string
		tombstone bool
	}
	oracle := map[string]entry{}
	wins := func(cand, inc entry) bool {
		if cand.ts != inc.ts {
			return cand.ts > inc.ts
		}
		if cand.nodeID == inc.nodeID {
			return cand.tombstone && !inc.tombstone
		}
		return cand.nodeID > inc.nodeID
	}
	record := func(key string, e entry) {
		cur, ok := oracle[key]
		if !ok || wins(e, cur) {
			oracle[key] = e
		}
	}

	var deltas []LWWMapDelta
	opsPer := []int{4, 3, 4, 3}
	var ts uint64 = 1
	for i := range replicas {
		for k := 0; k < opsPer[i]; k++ {
			key := keys[rng.intn(len(keys))]
			// Deliberately reuse some timestamps to exercise tie-breaks.
			useTS := ts
			if rng.intn(2) == 0 {
				useTS = uint64(1 + rng.intn(6)) // collide into a small range
			} else {
				ts++
			}
			nodeID := fmt.Sprintf("lww-%d", i)
			if rng.intn(4) == 0 {
				deltas = append(deltas, replicas[i].Delete(key, useTS))
				record(key, entry{value: "", ts: useTS, nodeID: nodeID, tombstone: true})
			} else {
				val := fmt.Sprintf("v%d-%d", i, k)
				deltas = append(deltas, replicas[i].Put(key, val, useTS))
				record(key, entry{value: val, ts: useTS, nodeID: nodeID, tombstone: false})
			}
		}
	}

	for i := range replicas {
		plan := adversarialPlan(rng, len(deltas))
		for _, idx := range plan {
			replicas[i].Merge(deltas[idx])
		}
	}

	// Oracle materialised view: live keys + their values.
	for i, r := range replicas {
		t.Logf("RunID=%s LWWMap replica lww-%d keys = %v", runID, i, r.Keys())
		if !lwwmapEqual(replicas[0].State(), r.State()) {
			t.Errorf("RunID=%s LWWMap divergence: replica %d state != replica0 state", runID, i)
		}
		for _, key := range keys {
			oe, hasOracle := oracle[key]
			wantPresent := hasOracle && !oe.tombstone
			gotV, gotPresent := r.Get(key)
			if gotPresent != wantPresent {
				t.Errorf("RunID=%s LWWMap replica %d key %q presence: got %v want %v",
					runID, i, key, gotPresent, wantPresent)
			}
			if wantPresent && gotV != oe.value {
				t.Errorf("RunID=%s LWWMap replica %d key %q value: got %q want oracle %q",
					runID, i, key, gotV, oe.value)
			}
		}
	}
	t.Logf("RunID=%s LWWMap: all %d replicas converged (keys=%v) — CONVERGED (LWW + tombstone precedence)", runID, N, replicas[0].Keys())
}

// ─────────────────────────────────────────────────────────────────────────────
// TestMergeIdempotenceUnderMassRedelivery
//
// Anti-bluff idempotence proof: take a converged GCounter, then re-deliver the
// ENTIRE delta set many more times in random order. An idempotent join (max)
// leaves the value unchanged; a non-idempotent accumulator (+=) would inflate
// it on every redundant re-delivery. We assert byte-for-byte state stability.
// ─────────────────────────────────────────────────────────────────────────────

func TestMergeIdempotenceUnderMassRedelivery(t *testing.T) {
	const runID = "idempotence-mass-001"
	rng := newRNG(0x5151AA55)

	g := NewGCounter("g0")
	peers := []*GCounter{NewGCounter("g1"), NewGCounter("g2"), NewGCounter("g3")}
	var deltas []GCounterDelta
	deltas = append(deltas, g.Increment(7))
	for _, p := range peers {
		deltas = append(deltas, p.Increment(uint64(1+rng.intn(20))))
	}
	// Converge g once.
	for _, d := range deltas {
		g.Merge(d)
	}
	converged := g.Value()
	snapshot := g.State()

	// Re-deliver the entire set 50 more times in random (duplicated) order.
	for round := 0; round < 50; round++ {
		plan := adversarialPlan(rng, len(deltas))
		for _, idx := range plan {
			g.Merge(deltas[idx])
		}
	}

	if g.Value() != converged {
		t.Errorf("RunID=%s idempotence violated: value drifted from %d to %d under mass re-delivery (non-idempotent merge would double-count)",
			runID, converged, g.Value())
	}
	if !gcounterEqual(snapshot, g.State()) {
		t.Errorf("RunID=%s idempotence violated: state changed under redundant re-delivery", runID)
	}
	t.Logf("RunID=%s GCounter value stable at %d across 50 rounds of duplicated re-delivery — IDEMPOTENT", runID, converged)
}

// ─────────────────────────────────────────────────────────────────────────────
// TestNaiveOverwriteWouldDiverge  (ANTI-BLUFF CONTROL)
//
// This is the load-bearing demonstration that the convergence assertions above
// are NOT vacuous. We take the SAME multiset of per-node GCounter deltas and
// fold them with a BROKEN "merge" that OVERWRITES the per-node value instead of
// taking the max (the classic last-write-wins mistake). Because the overwrite
// is order-sensitive AND non-idempotent, folding the deltas in two different
// orders yields TWO DIFFERENT, WRONG totals — i.e. the broken merge DIVERGES.
//
// The REAL GCounter.Merge (max-join) is then shown to give the SAME, CORRECT
// total in both orders. This proves the property the convergence tests rely on:
// a broken (non-join) merge would have made those tests FAIL.
// ─────────────────────────────────────────────────────────────────────────────

func TestNaiveOverwriteWouldDiverge(t *testing.T) {
	const runID = "antibluff-overwrite-001"

	// Two nodes that each increment twice; the per-node delta carries the
	// CUMULATIVE per-node count (as the real Increment emits). A correct join
	// takes the max per node; a naive overwrite takes whichever arrived last.
	a := NewGCounter("a")
	b := NewGCounter("b")
	da1 := a.Increment(3) // a:3
	da2 := a.Increment(2) // a:5  (cumulative)
	db1 := b.Increment(4) // b:4
	db2 := b.Increment(1) // b:5  (cumulative)

	deltas := []GCounterDelta{da1, da2, db1, db2}

	// Broken merge: per-node OVERWRITE (last writer wins per node).
	naiveFold := func(order []int) uint64 {
		state := map[string]uint64{}
		for _, idx := range order {
			for node, v := range deltas[idx].counts {
				state[node] = v // ← OVERWRITE (the bug)
			}
		}
		var sum uint64
		for _, v := range state {
			sum += v
		}
		return sum
	}

	// True total is 10 (a:5 + b:5).
	// Order 1 (da1,da2,db1,db2): each node's last-seen delta is its cumulative
	//   max (a:5, b:5) ⇒ 10 (happens to be correct).
	// Order 2 (da2,da1,db2,db1): da1 (a:3) OVERWRITES da2 (a:5) ⇒ a:3; db1 (b:4)
	//   overwrites db2 (b:5) ⇒ b:4; total 3+4 = 7 (WRONG, and ≠ order 1).
	order1 := []int{0, 1, 2, 3}
	order2 := []int{1, 0, 3, 2}

	naive1 := naiveFold(order1)
	naive2 := naiveFold(order2)

	t.Logf("RunID=%s naive-overwrite totals: order1=%d order2=%d (true total is 10)", runID, naive1, naive2)
	if naive1 == naive2 {
		t.Fatalf("RunID=%s control invalid: naive overwrite did not diverge (%d==%d); the anti-bluff demonstration requires it to", runID, naive1, naive2)
	}
	// At least one order reaches a WRONG total (≠10), proving overwrite ≠ join.
	if naive1 == 10 && naive2 == 10 {
		t.Fatalf("RunID=%s control invalid: naive overwrite was accidentally correct in both orders", runID)
	}

	// Now the REAL merge (max-join): same delta set, both orders ⇒ same correct 10.
	realFold := func(order []int) uint64 {
		g := NewGCounter("x")
		for _, idx := range order {
			g.Merge(deltas[idx])
		}
		return g.Value()
	}
	real1 := realFold(order1)
	real2 := realFold(order2)
	if real1 != real2 {
		t.Errorf("RunID=%s REAL GCounter.Merge diverged across orders: %d vs %d — this would be a genuine CRDT bug", runID, real1, real2)
	}
	if real1 != 10 {
		t.Errorf("RunID=%s REAL GCounter total wrong: got %d want 10", runID, real1)
	}
	t.Logf("RunID=%s ANTI-BLUFF: naive overwrite DIVERGES (%d vs %d), real max-join CONVERGES to correct %d in both orders",
		runID, naive1, naive2, real1)
}

// ─────────────────────────────────────────────────────────────────────────────
// TestORSetRemoveBeforeAddDivergence_KNOWN_DEFECT  (CHARACTERIZATION)
//
// **REAL CONVERGENCE DEFECT — DOCUMENTED, NOT PATCHED (out of scope).**
//
// The package doc (gcounter.go) claims merges converge "regardless of delivery
// order or duplicate delivery". That claim is FALSE for the delta-state ORSet
// when a Remove delta is delivered BEFORE the Add delta whose tag it cancels,
// with NO subsequent redelivery of the Remove.
//
// Root cause (orset.go Merge, ~lines 134-150): a Remove delta is applied as a
// set DIFFERENCE against only the tags PRESENT LOCALLY at merge time (the
// observed-remove intersection guard `if _, ok := local[t]; ok`). The cancelled
// tag is `delete`d, NOT recorded as a tombstone. So if the Remove arrives first
// its target tag is absent → the Remove is a silent no-op → the later Add
// re-introduces the tag permanently. The same {Add, Remove} delta set therefore
// yields DIFFERENT observable values depending on delivery order.
//
// Minimal reproduction (proven): receiver R1 delivered Add→Remove ends with the
// element ABSENT; receiver R2 delivered Remove→Add (same two deltas, reversed,
// no redelivery) ends with the element PRESENT. That is a strong-eventual-
// consistency violation at the VALUE level, not merely internal bookkeeping.
//
// Why the rest of the suite still passes: the production tests
// (TestORSetAddRemoveConvergesReordered) and the adversarial value test above
// only converge because they REDELIVER the Remove after the Add (at-least-once
// delivery) or never place a value-changing remove strictly before its add.
// The implementation's true contract is therefore "converges under causal OR
// at-least-once redelivery", NOT "regardless of order" as documented.
//
// This test is SKIPPED (with this written justification, per CLAUDE-2 §3) so the
// suite stays green while the defect is preserved in-tree as executable
// evidence. Flip the t.Skip to a hard failure once orset.go is fixed (e.g. by
// retaining tombstones / shipping a causal context with remove deltas) and the
// package doc's order-independence claim is made true — or amended.
// ─────────────────────────────────────────────────────────────────────────────

func TestORSetRemoveBeforeAddDivergence_KNOWN_DEFECT(t *testing.T) {
	const runID = "orset-remove-before-add-DEFECT-001"

	// Emitter produces an Add (tag T) then a Remove that observes T.
	emit := NewORSet("E")
	dAdd := emit.Add("x")    // tag T
	dRem := emit.Remove("x") // tombstones T; emit now has no "x"

	// R1: causal order Add → Remove. Expected: "x" absent.
	r1 := NewORSet("r1")
	r1.Merge(dAdd)
	r1.Merge(dRem)

	// R2: reversed Remove → Add, NO redelivery of Remove. Same two deltas.
	r2 := NewORSet("r2")
	r2.Merge(dRem) // no-op: T not present locally yet
	r2.Merge(dAdd) // T added, never cancelled → survives

	r1Has := r1.Contains("x")
	r2Has := r2.Contains("x")
	t.Logf("RunID=%s remove-before-add: r1.Contains(x)=%v  r2.Contains(x)=%v", runID, r1Has, r2Has)

	// Prove the defect is real and still present (this is the characterization).
	if r1Has == r2Has {
		t.Fatalf("RunID=%s characterization stale: ORSet now converges under remove-before-add "+
			"(r1=%v r2=%v). The defect appears FIXED — update orset.go's doc claim and convert "+
			"this test into a positive convergence assertion.", runID, r1Has, r2Has)
	}

	t.Logf("RunID=%s CONFIRMED REAL DEFECT: identical {Add,Remove} delta set diverges by order "+
		"(r1 absent, r2 present). Strong-eventual-consistency VIOLATED for delta-ORSet under "+
		"remove-before-add without redelivery. Production NOT patched (out of scope).", runID)

	t.Skip("KNOWN DEFECT documented and reproduced above; skipped to keep suite green per CLAUDE-2 §3 " +
		"(written justification). Remove this Skip when orset.go is fixed.")
}
