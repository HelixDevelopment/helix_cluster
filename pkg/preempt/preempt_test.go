package preempt

import "testing"

const runUUID = "HXC-1584-preempt-3f9c1a2e-7b4d-4c8e-9a01-d6e5f0a1b2c3"

// containsID reports whether victims includes an instance with the given ID.
func containsID(victims []Instance, id string) bool {
	for _, v := range victims {
		if v.ID == id {
			return true
		}
	}
	return false
}

// Closure 1: high-multiplier incoming onto a FULL node preempts the low
// effective-priority instance and IS placed.
//
// Mutation: if eligibility flips from strict `incPri > pri` to never-evict
// (e.g. dropping the candidate-build loop) the low instance survives and
// placed stays false -> this test fails. Also fails if the wrong (higher-pri)
// instance is chosen as victim.
func TestPreempt_HighMultiplierEvictsLowAndPlaces(t *testing.T) {
	// FULL node: capacity 2, two size-1 instances => Free()==0.
	low := Instance{ID: "low", Value: 10, Multiplier: 1, Size: 1}   // eff = 10
	high := Instance{ID: "high", Value: 10, Multiplier: 9, Size: 1} // eff = 90
	node := Node{Capacity: 2, Running: []Instance{low, high}}

	incoming := Instance{ID: "incoming", Value: 10, Multiplier: 5, Size: 1} // eff = 50

	t.Logf("run=%s BEFORE: free=%d low.eff=%d high.eff=%d incoming.eff=%d",
		runUUID, node.Free(), low.EffectivePriority(), high.EffectivePriority(),
		incoming.EffectivePriority())

	victims, placed := Preempt(node, incoming)

	t.Logf("run=%s AFTER: placed=%t victims=%v", runUUID, placed, victimIDs(victims))

	if !placed {
		t.Fatalf("run=%s expected incoming to be placed via preemption, got placed=false", runUUID)
	}
	if len(victims) != 1 {
		t.Fatalf("run=%s expected exactly 1 victim, got %d (%v)", runUUID, len(victims), victimIDs(victims))
	}
	if !containsID(victims, "low") {
		t.Fatalf("run=%s expected 'low' (eff=10) to be evicted, got %v", runUUID, victimIDs(victims))
	}
	if containsID(victims, "high") {
		t.Fatalf("run=%s 'high' (eff=90 > incoming eff=50) must NOT be evicted, got %v", runUUID, victimIDs(victims))
	}
}

// Closure 2 (negative, equal): an incoming with EQUAL effective priority does
// NOT preempt; placed=false, no victims.
//
// Mutation: changing the strict `>` to `>=` in the eligibility check makes the
// equal-priority running instance eligible, which would evict it and set
// placed=true -> this test fails.
func TestPreempt_EqualEffectivePriorityDoesNotPreempt(t *testing.T) {
	// FULL node, single size-1 instance, capacity 1 => Free()==0.
	running := Instance{ID: "running", Value: 6, Multiplier: 7, Size: 1} // eff = 42
	node := Node{Capacity: 1, Running: []Instance{running}}

	incoming := Instance{ID: "incoming", Value: 7, Multiplier: 6, Size: 1} // eff = 42 (equal)

	t.Logf("run=%s BEFORE: free=%d running.eff=%d incoming.eff=%d",
		runUUID, node.Free(), running.EffectivePriority(), incoming.EffectivePriority())

	victims, placed := Preempt(node, incoming)

	t.Logf("run=%s AFTER: placed=%t victims=%v", runUUID, placed, victimIDs(victims))

	if placed {
		t.Fatalf("run=%s equal effective priority must NOT preempt, but placed=true", runUUID)
	}
	if len(victims) != 0 {
		t.Fatalf("run=%s equal effective priority must yield no victims, got %v", runUUID, victimIDs(victims))
	}
}

// Closure 3 (negative, protected): a SoleGlobal/last-of-its-kind instance is
// NOT preempted even by a strictly higher-priority incoming.
//
// Mutation: removing the `if in.SoleGlobal { continue }` protection guard makes
// the protected instance eligible (incoming eff strictly exceeds it), which
// would evict it and set placed=true -> this test fails.
func TestPreempt_SoleGlobalInstanceIsProtected(t *testing.T) {
	// FULL node, single protected size-1 instance, capacity 1 => Free()==0.
	sole := Instance{ID: "sole", Value: 5, Multiplier: 2, Size: 1, SoleGlobal: true} // eff = 10
	node := Node{Capacity: 1, Running: []Instance{sole}}

	// Incoming strictly higher priority: 100 > 10. Without the guard it would
	// preempt; the guard must protect the sole instance.
	incoming := Instance{ID: "incoming", Value: 10, Multiplier: 10, Size: 1} // eff = 100

	t.Logf("run=%s BEFORE: free=%d sole.eff=%d sole.SoleGlobal=%t incoming.eff=%d",
		runUUID, node.Free(), sole.EffectivePriority(), sole.SoleGlobal, incoming.EffectivePriority())

	victims, placed := Preempt(node, incoming)

	t.Logf("run=%s AFTER: placed=%t victims=%v", runUUID, placed, victimIDs(victims))

	if placed {
		t.Fatalf("run=%s sole-global instance must protect node; placement must fail, but placed=true", runUUID)
	}
	if containsID(victims, "sole") {
		t.Fatalf("run=%s protected sole-global instance must NEVER be a victim, got %v", runUUID, victimIDs(victims))
	}
	if len(victims) != 0 {
		t.Fatalf("run=%s expected no victims when only candidate is protected, got %v", runUUID, victimIDs(victims))
	}
}

// TestPreempt_FreeCapacityPlacesWithoutEviction guards the no-eviction path:
// when the node already has room, the accumulation loop must stop BEFORE
// appending any victim, so nothing is evicted. The running instance here is a
// genuine eviction candidate (eff=1 < incoming eff=1? no — make it strictly
// lower so it WOULD be eligible if the guard were broken): running.eff=1,
// incoming.eff=2, and Free()==4 already covers the size-1 incoming.
//
// Mutation: weakening the loop's `if freed >= need { break }` guard (e.g. to
// `if freed > need`) — or moving the break to AFTER the append — would let the
// loop evict the eligible 'running' instance even though there is already room
// -> this test fails on len(victims) != 0. (The previously-claimed
// `node.Free() >= need` early-return mutation was a non-fact: that branch was
// behaviourally redundant with this guard and has been removed from production.)
func TestPreempt_FreeCapacityPlacesWithoutEviction(t *testing.T) {
	// running is strictly lower effective priority than incoming, so it IS an
	// eligible victim; only the freed>=need guard prevents it from being evicted.
	running := Instance{ID: "running", Value: 1, Multiplier: 1, Size: 1} // eff = 1
	node := Node{Capacity: 5, Running: []Instance{running}}              // Free()==4

	incoming := Instance{ID: "incoming", Value: 1, Multiplier: 2, Size: 1} // eff = 2 > 1

	t.Logf("run=%s BEFORE: free=%d running.eff=%d incoming.eff=%d need=%d",
		runUUID, node.Free(), running.EffectivePriority(), incoming.EffectivePriority(), incoming.Size)

	victims, placed := Preempt(node, incoming)

	t.Logf("run=%s AFTER: placed=%t victims=%v", runUUID, placed, victimIDs(victims))

	if !placed {
		t.Fatalf("run=%s incoming should be placed in free capacity, got placed=false", runUUID)
	}
	if len(victims) != 0 {
		t.Fatalf("run=%s free-capacity placement must evict nothing (eligible victim must be spared), got %v", runUUID, victimIDs(victims))
	}
}

// TestPreempt_VictimOrderingEvictsLowestEffFirst covers victim ORDERING: a FULL
// node with two eligible candidates of DIFFERENT effective priority (both
// strictly below incoming) where only ONE size-1 eviction is needed. The
// LOWEST-eff candidate must be the victim; the higher-eff eligible one is
// spared.
//
// Mutation: reversing the sort comparator (pa < pb -> pa > pb) would choose the
// higher-eff 'mid' as victim instead of 'low' -> this test fails (it expects
// exactly 'low' and asserts 'mid' is spared).
func TestPreempt_VictimOrderingEvictsLowestEffFirst(t *testing.T) {
	// FULL node: capacity 2, two size-1 instances => Free()==0. Only 1 size-1
	// slot is needed, so exactly one victim is chosen — exercising ordering.
	low := Instance{ID: "low", Value: 2, Multiplier: 1, Size: 1} // eff = 2
	mid := Instance{ID: "mid", Value: 2, Multiplier: 5, Size: 1} // eff = 10
	// List 'mid' first so a buggy comparator can't accidentally pick 'low' by
	// input order; correctness must come from the eff-priority sort.
	node := Node{Capacity: 2, Running: []Instance{mid, low}}

	incoming := Instance{ID: "incoming", Value: 2, Multiplier: 50, Size: 1} // eff = 100

	t.Logf("run=%s BEFORE: free=%d low.eff=%d mid.eff=%d incoming.eff=%d need=%d",
		runUUID, node.Free(), low.EffectivePriority(), mid.EffectivePriority(),
		incoming.EffectivePriority(), incoming.Size)

	victims, placed := Preempt(node, incoming)

	t.Logf("run=%s AFTER: placed=%t victims=%v", runUUID, placed, victimIDs(victims))

	if !placed {
		t.Fatalf("run=%s expected placement via single lowest-eff eviction, got placed=false", runUUID)
	}
	if len(victims) != 1 {
		t.Fatalf("run=%s expected exactly 1 victim (lowest eff), got %d (%v)", runUUID, len(victims), victimIDs(victims))
	}
	if !containsID(victims, "low") {
		t.Fatalf("run=%s expected 'low' (eff=2, lowest) to be evicted first, got %v", runUUID, victimIDs(victims))
	}
	if containsID(victims, "mid") {
		t.Fatalf("run=%s 'mid' (eff=10) must be spared when one eviction suffices, got %v", runUUID, victimIDs(victims))
	}
}

// TestPreempt_InsufficientCapacityRejectsWithNoEviction covers the rejection
// path: a FULL node whose eligible lower-priority candidates, even if ALL
// evicted, cannot free incoming.Size. Assert placed=false AND victims empty
// (no partial eviction).
//
// Mutation: changing the rejection `if freed < need { return nil, false }` to
// `return chosen, true` would (a) report placed=true on an impossible placement
// and (b) leak a partial victim set -> this test fails on placed or len(victims).
func TestPreempt_InsufficientCapacityRejectsWithNoEviction(t *testing.T) {
	// FULL node: capacity 3 fully used. Two eligible low-eff size-1 instances
	// (total evictable = 2) plus one protected SoleGlobal size-1 instance.
	// Incoming needs size 3, but at most 2 can ever be freed => reject.
	a := Instance{ID: "a", Value: 1, Multiplier: 1, Size: 1}                         // eff = 1, eligible
	b := Instance{ID: "b", Value: 1, Multiplier: 2, Size: 1}                         // eff = 2, eligible
	prot := Instance{ID: "prot", Value: 1, Multiplier: 1, Size: 1, SoleGlobal: true} // protected
	node := Node{Capacity: 3, Running: []Instance{a, b, prot}}                       // Free()==0

	incoming := Instance{ID: "incoming", Value: 100, Multiplier: 1, Size: 3} // eff = 100, needs 3

	t.Logf("run=%s BEFORE: free=%d a.eff=%d b.eff=%d prot.SoleGlobal=%t incoming.eff=%d need=%d",
		runUUID, node.Free(), a.EffectivePriority(), b.EffectivePriority(),
		prot.SoleGlobal, incoming.EffectivePriority(), incoming.Size)

	victims, placed := Preempt(node, incoming)

	t.Logf("run=%s AFTER: placed=%t victims=%v", runUUID, placed, victimIDs(victims))

	if placed {
		t.Fatalf("run=%s placement is impossible (max freeable=2 < need=3); placed must be false, got true", runUUID)
	}
	if len(victims) != 0 {
		t.Fatalf("run=%s impossible placement must perform NO partial eviction, got %v", runUUID, victimIDs(victims))
	}
}

// TestPreempt_MultiVictimPlacementEvictsExactSet covers multi-victim
// accumulation: an incoming whose Size requires evicting MORE THAN ONE eligible
// instance. Assert the exact victim set (the two lowest-eff) and that
// accumulation TERMINATES via the `if freed >= need { break }` guard, sparing
// the next-higher eligible instance.
//
// Mutation: removing the `if freed >= need { break }` guard would over-evict
// (it would append 'c' too) -> this test fails on len(victims) != 2 and on
// containsID(victims, "c"). Reversing the sort would also change which two are
// chosen -> fails on the exact-set assertions.
func TestPreempt_MultiVictimPlacementEvictsExactSet(t *testing.T) {
	// FULL node: capacity 3, three size-1 instances => Free()==0. Incoming
	// needs size 2 => exactly TWO lowest-eff victims, the third spared.
	a := Instance{ID: "a", Value: 1, Multiplier: 1, Size: 1} // eff = 1 (lowest)
	b := Instance{ID: "b", Value: 1, Multiplier: 2, Size: 1} // eff = 2
	c := Instance{ID: "c", Value: 1, Multiplier: 3, Size: 1} // eff = 3 (highest, eligible but spared)
	// Shuffle input order so correctness comes from the sort, not input order.
	node := Node{Capacity: 3, Running: []Instance{c, a, b}}

	incoming := Instance{ID: "incoming", Value: 100, Multiplier: 1, Size: 2} // eff = 100, needs 2

	t.Logf("run=%s BEFORE: free=%d a.eff=%d b.eff=%d c.eff=%d incoming.eff=%d need=%d",
		runUUID, node.Free(), a.EffectivePriority(), b.EffectivePriority(),
		c.EffectivePriority(), incoming.EffectivePriority(), incoming.Size)

	victims, placed := Preempt(node, incoming)

	t.Logf("run=%s AFTER: placed=%t victims=%v", runUUID, placed, victimIDs(victims))

	if !placed {
		t.Fatalf("run=%s expected placement via two evictions, got placed=false", runUUID)
	}
	if len(victims) != 2 {
		t.Fatalf("run=%s expected exactly 2 victims (the two lowest eff), got %d (%v)", runUUID, len(victims), victimIDs(victims))
	}
	if !containsID(victims, "a") || !containsID(victims, "b") {
		t.Fatalf("run=%s expected victim set {a,b} (eff 1 and 2), got %v", runUUID, victimIDs(victims))
	}
	if containsID(victims, "c") {
		t.Fatalf("run=%s 'c' (eff=3) must be spared once need is met after 2 evictions, got %v", runUUID, victimIDs(victims))
	}
}

func victimIDs(victims []Instance) []string {
	ids := make([]string, 0, len(victims))
	for _, v := range victims {
		ids = append(ids, v.ID)
	}
	return ids
}
