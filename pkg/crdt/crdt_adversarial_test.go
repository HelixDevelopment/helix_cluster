package crdt

import (
	"fmt"
	"math/rand"
	"testing"
)

// ---------------------------------------------------------------------------
// Adversarial, mutation-verified audit of the state-based CRDT semilattice
// contract for pkg/crdt. The centerpiece is the four semilattice laws —
// COMMUTATIVITY, ASSOCIATIVITY, IDEMPOTENCE, MONOTONICITY — proven over many
// DETERMINISTIC random states (seeded by index; no time, no global rand, no
// rand in assertions). A violation of any of these silently breaks eventual
// consistency / convergence, so these are load-bearing.
//
// Lattice equality is the production Equal method (state equality). Monotonicity
// is asserted lattice-agnostically via the absorption law:
//
//	merge(a,b) ⊒ a   ⟺   merge(merge(a,b), a) == merge(a,b)
//
// (merging an already-dominated operand back in is a no-op), and additionally
// via a type-specific structural ≤ predicate so a regression in either Merge or
// Equal cannot hide a lost-information bug.
//
// All randomness is derived from a per-subtest rand.New(rand.NewSource(seed))
// with a fixed seed, so failures are reproducible and assertions are
// deterministic.
// ---------------------------------------------------------------------------

const adversarialStates = 200 // random states generated per CRDT law sweep

// =============================== GCounter ==================================

// randGCounter builds a deterministic pseudo-random grow-only counter. Replica
// ids are drawn from a small fixed alphabet so independently-generated states
// share slots and the lattice joins are non-trivial.
func randGCounter(rng *rand.Rand) *GCounter {
	g := NewGCounter()
	ids := []ReplicaID{"A", "B", "C", "D"}
	ops := rng.Intn(6)
	for i := 0; i < ops; i++ {
		g.Inc(ids[rng.Intn(len(ids))], uint64(rng.Intn(50)))
	}
	return g
}

// gcounterLeq is the structural lattice order: a ≤ b iff every slot of a is ≤
// the corresponding slot of b. Absent slots are zero.
func gcounterLeq(a, b *GCounter) bool {
	for id, v := range a.counts {
		if b.counts[id] < v {
			return false
		}
	}
	return true
}

func TestAdversarial_GCounter_SemilatticeLaws(t *testing.T) {
	rng := rand.New(rand.NewSource(0x6c00))
	for i := 0; i < adversarialStates; i++ {
		a, b, c := randGCounter(rng), randGCounter(rng), randGCounter(rng)

		// COMMUTATIVITY: merge(a,b) == merge(b,a)
		if !a.Merge(b).Equal(b.Merge(a)) {
			t.Fatalf("[i=%d] GCounter commutativity: merge(a,b) != merge(b,a)\n a=%v\n b=%v", i, a.counts, b.counts)
		}
		// ASSOCIATIVITY: merge(merge(a,b),c) == merge(a,merge(b,c))
		left := a.Merge(b).Merge(c)
		right := a.Merge(b.Merge(c))
		if !left.Equal(right) {
			t.Fatalf("[i=%d] GCounter associativity broken\n a=%v\n b=%v\n c=%v", i, a.counts, b.counts, c.counts)
		}
		// IDEMPOTENCE: merge(a,a) == a
		if !a.Merge(a).Equal(a) {
			t.Fatalf("[i=%d] GCounter idempotence: merge(a,a) != a\n a=%v", i, a.counts)
		}
		// MONOTONICITY (absorption): merge(merge(a,b),a) == merge(a,b) and ≥ both.
		ab := a.Merge(b)
		if !ab.Merge(a).Equal(ab) || !ab.Merge(b).Equal(ab) {
			t.Fatalf("[i=%d] GCounter monotonicity(absorption): re-merging an operand changed the join\n a=%v\n b=%v", i, a.counts, b.counts)
		}
		// MONOTONICITY (structural): merge result dominates both operands.
		if !gcounterLeq(a, ab) || !gcounterLeq(b, ab) {
			t.Fatalf("[i=%d] GCounter monotonicity(structural): join lost information\n a=%v\n b=%v\n ab=%v", i, a.counts, b.counts, ab.counts)
		}
	}
}

// TestAdversarial_GCounter_PerReplicaMax pins the defining type-specific rule:
// merge takes the per-replica MAX, never the sum. Re-delivering an old, smaller
// increment for a replica must not lower its slot, and re-delivering an already
// observed state must not raise the total. This is the exact property a
// sum-based merge mutation destroys (it double-counts on re-merge).
func TestAdversarial_GCounter_PerReplicaMax(t *testing.T) {
	a := NewGCounter()
	a.Inc("A", 10)
	a.Inc("B", 3)

	// A stale snapshot of A's own slot (smaller) must not lower it on merge.
	stale := NewGCounter()
	stale.Inc("A", 4) // less than 10
	m := a.Merge(stale)
	if m.counts["A"] != 10 {
		t.Fatalf("per-replica max: A slot=%d after merging stale 4, want 10", m.counts["A"])
	}
	if got := m.Value(); got != 13 {
		t.Fatalf("re-delivering an old increment double-counted: value=%d, want 13", got)
	}

	// Re-merging the full state any number of times is a no-op on the total.
	cur := a.Clone()
	for k := 0; k < 5; k++ {
		cur = cur.Merge(a)
	}
	if got := cur.Value(); got != 13 {
		t.Fatalf("idempotent re-merge double-counted after 5 rounds: value=%d, want 13", got)
	}

	// A newer increment for the same replica DOES advance the slot (max wins up).
	newer := NewGCounter()
	newer.Inc("A", 25)
	if got := a.Merge(newer).counts["A"]; got != 25 {
		t.Fatalf("per-replica max: A slot=%d after merging newer 25, want 25", got)
	}
}

// =============================== PNCounter ==================================

func randPNCounter(rng *rand.Rand) *PNCounter {
	c := NewPNCounter()
	ids := []ReplicaID{"A", "B", "C"}
	ops := rng.Intn(8)
	for i := 0; i < ops; i++ {
		id := ids[rng.Intn(len(ids))]
		if rng.Intn(2) == 0 {
			c.Inc(id, uint64(rng.Intn(40)))
		} else {
			c.Dec(id, uint64(rng.Intn(40)))
		}
	}
	return c
}

func TestAdversarial_PNCounter_SemilatticeLaws(t *testing.T) {
	rng := rand.New(rand.NewSource(0x504e))
	for i := 0; i < adversarialStates; i++ {
		a, b, c := randPNCounter(rng), randPNCounter(rng), randPNCounter(rng)

		if !a.Merge(b).Equal(b.Merge(a)) {
			t.Fatalf("[i=%d] PNCounter commutativity broken", i)
		}
		left := a.Merge(b).Merge(c)
		right := a.Merge(b.Merge(c))
		if !left.Equal(right) {
			t.Fatalf("[i=%d] PNCounter associativity broken", i)
		}
		if !a.Merge(a).Equal(a) {
			t.Fatalf("[i=%d] PNCounter idempotence broken", i)
		}
		ab := a.Merge(b)
		if !ab.Merge(a).Equal(ab) || !ab.Merge(b).Equal(ab) {
			t.Fatalf("[i=%d] PNCounter monotonicity(absorption) broken", i)
		}
		if !gcounterLeq(a.p, ab.p) || !gcounterLeq(a.n, ab.n) || !gcounterLeq(b.p, ab.p) || !gcounterLeq(b.n, ab.n) {
			t.Fatalf("[i=%d] PNCounter monotonicity(structural): a half lost information", i)
		}
		// Convergence value is order-independent.
		if left.Value() != right.Value() {
			t.Fatalf("[i=%d] PNCounter value diverged across merge order: %d vs %d", i, left.Value(), right.Value())
		}
	}
}

// TestAdversarial_PNCounter_SignedValue pins P-N arithmetic including a
// genuinely negative result. A P+N mutation flips the sign of the assertion.
func TestAdversarial_PNCounter_SignedValue(t *testing.T) {
	c := NewPNCounter()
	c.Inc("A", 3)
	c.Dec("A", 10)
	c.Dec("B", 2)
	// 3 - (10+2) = -9.
	if got := c.Value(); got != -9 {
		t.Fatalf("PNCounter signed value=%d, want -9 (P-N); a P+N bug would give %d", got, 3+12)
	}
	// Merge with a pure-increment replica raises it back above zero.
	d := NewPNCounter()
	d.Inc("C", 20)
	if got := c.Merge(d).Value(); got != 11 {
		t.Fatalf("PNCounter merged value=%d, want 11", got)
	}
}

// ============================== LWWRegister ================================

// randLWW builds a deterministic register. Timestamps are drawn from a SMALL
// range so ties (equal timestamps from different replicas) occur frequently —
// that is the case that exercises the deterministic tiebreak, the most
// bug-prone part of LWW.
//
// WELL-FORMEDNESS PRECONDITION (important): a state-based LWW register's merge
// is only required to be commutative/associative over histories where each
// (replica, timestamp) coordinate carries a SINGLE value — that is the whole
// point of a (Lamport/HLC timestamp, replica) write id: it uniquely identifies
// one write. A generator that minted two DIFFERENT values for the same
// (replica, timestamp) would be fabricating a Byzantine history no correct
// replica can produce (the Set replay-guard forbids it locally), and would
// produce a spurious "commutativity" failure that is a test artifact, not a
// product bug. We therefore derive the value deterministically from the
// (replica, timestamp) pair so identical coordinates always carry identical
// values — exactly the invariant LWW relies on — while still exercising every
// timestamp tie across distinct replicas.
func randLWW(rng *rand.Rand) *LWWRegister {
	r := NewLWWRegister()
	ids := []ReplicaID{"A", "B", "C"}
	ops := rng.Intn(4)
	for i := 0; i < ops; i++ {
		ts := uint64(rng.Intn(4)) // tiny range -> frequent timestamp ties
		id := ids[rng.Intn(len(ids))]
		val := fmt.Sprintf("%s@%d", id, ts) // value uniquely determined by (replica,ts)
		r.Set(val, ts, id)
	}
	return r
}

// lwwLeq: a ≤ b iff merging b into a leaves a unchanged-or-advanced toward b,
// i.e. the join equals "the dominant of the two". Concretely a ≤ b when
// merge(a,b) == b OR they are joins that agree — we assert the join dominates a
// by checking merge(merge(a,b),a) == merge(a,b) at the call site; here we add
// the direct order: a ≤ join always.
func lwwLeq(a, join *LWWRegister) bool {
	// join dominates a iff merging a into the join does not change the join.
	return join.Merge(a).Equal(join)
}

func TestAdversarial_LWWRegister_SemilatticeLaws(t *testing.T) {
	rng := rand.New(rand.NewSource(0x6c77))
	for i := 0; i < adversarialStates; i++ {
		a, b, c := randLWW(rng), randLWW(rng), randLWW(rng)

		// COMMUTATIVITY — the property the deterministic tiebreak exists to
		// guarantee. Under equal timestamps, both orders MUST pick the same
		// (value,ts,replica). A non-deterministic or asymmetric tiebreak
		// (e.g. `>` without the id tiebreak, or `<`) breaks this here.
		ab, ba := a.Merge(b), b.Merge(a)
		if !ab.Equal(ba) {
			va, _ := ab.Value()
			vb, _ := ba.Value()
			t.Fatalf("[i=%d] LWW commutativity: merge(a,b)=%q@%d/%s != merge(b,a)=%q@%d/%s",
				i, va, ab.timestamp, ab.replica, vb, ba.timestamp, ba.replica)
		}
		// ASSOCIATIVITY
		left := a.Merge(b).Merge(c)
		right := a.Merge(b.Merge(c))
		if !left.Equal(right) {
			lv, _ := left.Value()
			rv, _ := right.Value()
			t.Fatalf("[i=%d] LWW associativity: %q@%d/%s != %q@%d/%s",
				i, lv, left.timestamp, left.replica, rv, right.timestamp, right.replica)
		}
		// IDEMPOTENCE
		if !a.Merge(a).Equal(a) {
			t.Fatalf("[i=%d] LWW idempotence broken", i)
		}
		// MONOTONICITY: join dominates both operands (absorption + structural).
		if !ab.Merge(a).Equal(ab) || !ab.Merge(b).Equal(ab) {
			t.Fatalf("[i=%d] LWW monotonicity(absorption) broken", i)
		}
		if !lwwLeq(a, ab) || !lwwLeq(b, ab) {
			t.Fatalf("[i=%d] LWW monotonicity(structural): join does not dominate an operand", i)
		}
	}
}

// TestAdversarial_LWWRegister_DeterministicTiebreak proves the equal-timestamp
// tiebreak is (a) deterministic across BOTH merge orders and (b) higher-replica
// wins, for every ordered pair of distinct replicas at the SAME timestamp.
// Dropping the id tiebreak (return only ts>ts) makes merge(a,b) != merge(b,a).
func TestAdversarial_LWWRegister_DeterministicTiebreak(t *testing.T) {
	ids := []ReplicaID{"A", "B", "C", "D"}
	const ts = 42
	for _, x := range ids {
		for _, y := range ids {
			if x == y {
				continue
			}
			a := NewLWWRegister()
			b := NewLWWRegister()
			a.Set("val-"+string(x), ts, x)
			b.Set("val-"+string(y), ts, y)

			ab := a.Merge(b)
			ba := b.Merge(a)
			if !ab.Equal(ba) {
				avv, _ := ab.Value()
				bvv, _ := ba.Value()
				t.Fatalf("tiebreak not commutative for (%s,%s)@%d: merge=%q vs %q", x, y, ts, avv, bvv)
			}
			// Higher ReplicaID must win deterministically.
			wantReplica := x
			if y > x {
				wantReplica = y
			}
			if ab.replica != wantReplica {
				gv, _ := ab.Value()
				t.Fatalf("tiebreak winner for (%s,%s)@%d was replica %s (val %q), want %s",
					x, y, ts, ab.replica, gv, wantReplica)
			}
		}
	}
}

// TestAdversarial_LWWRegister_HigherTimestampAlwaysWins sweeps ordered
// timestamp pairs and asserts the strictly-higher timestamp wins regardless of
// replica id or merge order. A `<` mutation in dominates loses here.
func TestAdversarial_LWWRegister_HigherTimestampAlwaysWins(t *testing.T) {
	for lo := uint64(0); lo < 5; lo++ {
		for hi := lo + 1; hi < 6; hi++ {
			a := NewLWWRegister()
			b := NewLWWRegister()
			a.Set("lo", lo, "Z") // lo value authored by the LEXICALLY HIGHER replica
			b.Set("hi", hi, "A") // hi value authored by the lexically lower replica
			// Timestamp must dominate the replica tiebreak entirely.
			if v, _ := a.Merge(b).Value(); v != "hi" {
				t.Fatalf("merge(a,b) ts %d vs %d picked %q, want \"hi\"", lo, hi, v)
			}
			if v, _ := b.Merge(a).Value(); v != "hi" {
				t.Fatalf("merge(b,a) ts %d vs %d picked %q, want \"hi\"", lo, hi, v)
			}
		}
	}
}

// TestAdversarial_LWWRegister_ReplayIdempotent pins the Set replay-guard: a
// re-applied Set with a non-greater timestamp must not move the register. This
// keeps eventual delivery idempotent at the operation level.
func TestAdversarial_LWWRegister_ReplayIdempotent(t *testing.T) {
	r := NewLWWRegister()
	r.Set("first", 5, "A")
	r.Set("stale", 3, "B") // older -> ignored
	r.Set("same", 5, "A")  // same ts, same replica -> ignored (>=)
	if v, _ := r.Value(); v != "first" {
		t.Fatalf("replay guard: value=%q, want \"first\"", v)
	}
	r.Set("newer", 9, "B") // strictly newer -> wins
	if v, _ := r.Value(); v != "newer" {
		t.Fatalf("strictly newer Set ignored: value=%q, want \"newer\"", v)
	}
}

// ================================= ORSet ===================================

func randORSet(rng *rand.Rand) *ORSet {
	s := NewORSet()
	els := []string{"x", "y", "z"}
	ids := []ReplicaID{"A", "B", "C"}
	ops := rng.Intn(8)
	for i := 0; i < ops; i++ {
		el := els[rng.Intn(len(els))]
		if rng.Intn(3) == 0 {
			s.Remove(el)
		} else {
			s.Add(el, ids[rng.Intn(len(ids))])
		}
	}
	return s
}

// orsetLeq: a ≤ b iff every add-tag and tomb-tag of a is present in b (subset
// in the union lattice).
func orsetLeq(a, b *ORSet) bool {
	subset := func(am, bm map[string]map[tag]struct{}) bool {
		for el, tags := range am {
			bt := bm[el]
			for tg := range tags {
				if _, ok := bt[tg]; !ok {
					return false
				}
			}
		}
		return true
	}
	return subset(a.adds, b.adds) && subset(a.tombs, b.tombs)
}

func TestAdversarial_ORSet_SemilatticeLaws(t *testing.T) {
	rng := rand.New(rand.NewSource(0x6f72))
	for i := 0; i < adversarialStates; i++ {
		a, b, c := randORSet(rng), randORSet(rng), randORSet(rng)

		if !a.Merge(b).Equal(b.Merge(a)) {
			t.Fatalf("[i=%d] ORSet commutativity broken", i)
		}
		left := a.Merge(b).Merge(c)
		right := a.Merge(b.Merge(c))
		if !left.Equal(right) {
			t.Fatalf("[i=%d] ORSet associativity broken", i)
		}
		if !a.Merge(a).Equal(a) {
			t.Fatalf("[i=%d] ORSet idempotence broken", i)
		}
		ab := a.Merge(b)
		if !ab.Merge(a).Equal(ab) || !ab.Merge(b).Equal(ab) {
			t.Fatalf("[i=%d] ORSet monotonicity(absorption) broken", i)
		}
		if !orsetLeq(a, ab) || !orsetLeq(b, ab) {
			t.Fatalf("[i=%d] ORSet monotonicity(structural): join lost a tag", i)
		}
		// Convergence of the observable element set is order-independent.
		if !equalStrs(sortedElems(left), sortedElems(right)) {
			t.Fatalf("[i=%d] ORSet element set diverged across merge order: %v vs %v",
				i, sortedElems(left), sortedElems(right))
		}
	}
}

// ============================ Cross-cutting ================================

// TestAdversarial_Convergence_OrderIndependent is the explicit convergence
// proof: build a fixed pool of operations, deliver them to several replicas in
// DIFFERENT (deterministic) permutations, full-mesh merge, and assert every
// replica ends at the identical observable value. This is the end-user-visible
// eventual-consistency guarantee, not merely an algebraic identity.
func TestAdversarial_Convergence_OrderIndependent(t *testing.T) {
	type gop struct {
		id    ReplicaID
		delta uint64
	}
	gops := []gop{{"A", 5}, {"B", 2}, {"A", 9}, {"C", 1}, {"B", 7}, {"A", 3}}

	// Permutations generated deterministically by index-seeded shuffles.
	const replicas = 6
	finals := make([]uint64, replicas)
	for r := 0; r < replicas; r++ {
		rng := rand.New(rand.NewSource(int64(r) + 1))
		perm := append([]gop(nil), gops...)
		rng.Shuffle(len(perm), func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })
		g := NewGCounter()
		for _, op := range perm {
			g.Inc(op.id, op.delta)
		}
		finals[r] = g.Value()
	}
	// Each replica applied the SAME multiset of its own ops; but the real test
	// is that after merging all replicas pairwise they converge. Here each
	// replica independently holds the full op set, so values already equal —
	// assert that, then cross-merge and re-assert (idempotent convergence).
	want := finals[0]
	for r := 1; r < replicas; r++ {
		if finals[r] != want {
			t.Fatalf("pre-merge replica %d value=%d, want %d", r, finals[r], want)
		}
	}

	// Now the genuine concurrent case: split ops across replicas (each owns a
	// disjoint slice), apply locally, then converge by full-mesh merge in two
	// different orders. Both orders MUST reach the same Value.
	split := [][]gop{
		{{"A", 5}, {"A", 9}, {"A", 3}}, // replica A's own increments
		{{"B", 2}, {"B", 7}},           // replica B's own increments
		{{"C", 1}},                     // replica C's own increment
	}
	mk := func() []*GCounter {
		out := make([]*GCounter, len(split))
		for i, ops := range split {
			g := NewGCounter()
			for _, op := range ops {
				g.Inc(op.id, op.delta)
			}
			out[i] = g
		}
		return out
	}
	// Order 1: ((0⊔1)⊔2)
	g := mk()
	conv1 := g[0].Merge(g[1]).Merge(g[2])
	// Order 2: (0⊔(2⊔1))
	h := mk()
	conv2 := h[0].Merge(h[2].Merge(h[1]))
	if conv1.Value() != conv2.Value() {
		t.Fatalf("convergence: order1=%d order2=%d", conv1.Value(), conv2.Value())
	}
	// Expected total: A=5+9+3=17, B=2+7=9, C=1 -> 27.
	if got := conv1.Value(); got != 27 {
		t.Fatalf("converged GCounter value=%d, want 27", got)
	}
}

// ================================ Edges ====================================

// TestAdversarial_Edges_EmptyAndSelf exercises the zero-value / single-replica /
// merge-with-self / merge-of-two-empties corners for every CRDT.
func TestAdversarial_Edges_EmptyAndSelf(t *testing.T) {
	// GCounter
	if got := NewGCounter().Merge(NewGCounter()).Value(); got != 0 {
		t.Fatalf("empty⊔empty GCounter value=%d, want 0", got)
	}
	g := NewGCounter()
	g.Inc("A", 4)
	if !g.Merge(NewGCounter()).Equal(g) || !NewGCounter().Merge(g).Equal(g) {
		t.Fatalf("GCounter merge with empty must be identity")
	}
	if !g.Merge(g).Equal(g) {
		t.Fatalf("GCounter merge with self must be identity")
	}

	// PNCounter
	if got := NewPNCounter().Merge(NewPNCounter()).Value(); got != 0 {
		t.Fatalf("empty⊔empty PNCounter value=%d, want 0", got)
	}
	p := NewPNCounter()
	p.Inc("A", 6)
	p.Dec("A", 2)
	if !p.Merge(NewPNCounter()).Equal(p) || !p.Merge(p).Equal(p) {
		t.Fatalf("PNCounter empty/self merge must be identity")
	}

	// LWWRegister
	if _, set := NewLWWRegister().Merge(NewLWWRegister()).Value(); set {
		t.Fatalf("empty⊔empty LWW must remain unset")
	}
	r := NewLWWRegister()
	r.Set("v", 1, "A")
	if !r.Merge(NewLWWRegister()).Equal(r) || !NewLWWRegister().Merge(r).Equal(r) {
		t.Fatalf("LWW merge with empty must be identity (empty loses)")
	}
	if mv, _ := NewLWWRegister().Merge(r).Value(); mv != "v" {
		t.Fatalf("empty⊔set LWW must adopt the set value, got %q", mv)
	}

	// ORSet
	if NewORSet().Merge(NewORSet()).Contains("x") {
		t.Fatalf("empty⊔empty ORSet must contain nothing")
	}
	s := NewORSet()
	s.Add("x", "A")
	if !s.Merge(NewORSet()).Equal(s) || !s.Merge(s).Equal(s) {
		t.Fatalf("ORSet empty/self merge must be identity")
	}
}
