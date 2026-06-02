package priorityqueue

import (
	"sort"
	"testing"
)

const runUUID = "HXC-1393-priorityqueue-2f9c4a7e-3b1d-4e62-9a0c-7d5e1f08b3aa"

// idsOf extracts the dequeue order as a slice of IDs for compact assertions.
func idsOf(jobs []Job) []string {
	out := make([]string, len(jobs))
	for i, j := range jobs {
		out[i] = j.ID
	}
	return out
}

// drainIDs repeatedly Pops the queue at a fixed now and records the order.
func drainIDs(q *Queue, now int64) []string {
	var out []string
	for {
		j, ok := q.Pop(now)
		if !ok {
			break
		}
		out = append(out, j.ID)
	}
	return out
}

func eq(a, b []string) bool {
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

// q2order returns the non-mutating dequeue order at now.
func q2order(q *Queue, now int64) []string {
	return idsOf(q.Order(now))
}

// expectedOrder hand-derives the dequeue order directly from the documented
// formula (NOT hardcoded): it computes Priority for each job at now and sorts
// by the same primary key + tie-breaks the queue documents. Tests assert the
// queue's actual behavior equals this independently-computed order.
func expectedOrder(w Weights, jobs []Job, now int64) []string {
	cp := make([]Job, len(jobs))
	copy(cp, jobs)
	sort.SliceStable(cp, func(i, k int) bool {
		pi := Priority(w, cp[i], now)
		pk := Priority(w, cp[k], now)
		if pi != pk {
			return pi > pk
		}
		if cp[i].SubmitTick != cp[k].SubmitTick {
			return cp[i].SubmitTick < cp[k].SubmitTick
		}
		return cp[i].ID < cp[k].ID
	})
	return idsOf(cp)
}

// Clause 1: full dequeue sequence at a fixed now matches the composite formula.
//
// Mutation: if Pop ignored fairshare/size/qos (e.g. only used age, or froze a
// constant priority), the hand-computed expectedOrder would diverge from the
// drained order and this fails.
func TestCompositeOrderingFullSequence(t *testing.T) {
	w := DefaultWeights() // Age:2 FairShare:5 Size:1
	now := int64(100)

	// Distinct factor mixes so no single factor trivially dominates.
	// p = qos + 2*age + 5*fair - 1*size
	jobs := []Job{
		{ID: "guaranteed-small", SubmitTick: 95, FairShare: 1, Size: 2, QoS: QoSGuaranteed}, // 300 + 2*5 + 5 - 2 = 313
		{ID: "burst-fair", SubmitTick: 90, FairShare: 20, Size: 5, QoS: QoSBurstable},       // 150 + 2*10 + 100 - 5 = 265
		{ID: "besteffort-old", SubmitTick: 10, FairShare: 4, Size: 1, QoS: QoSBestEffort},   // 0 + 2*90 + 20 - 1 = 199
		{ID: "besteffort-big", SubmitTick: 98, FairShare: 30, Size: 80, QoS: QoSBestEffort}, // 0 + 2*2 + 150 - 80 = 74
		{ID: "burst-young", SubmitTick: 99, FairShare: 2, Size: 1, QoS: QoSBurstable},       // 150 + 2*1 + 10 - 1 = 161
	}
	// Hand-derived: guaranteed-small(313) > burst-fair(265) > besteffort-old(199)
	//             > burst-young(161) > besteffort-big(74)

	q := New(w)
	for _, j := range jobs {
		q.Push(j)
	}

	want := expectedOrder(w, jobs, now)
	for _, j := range jobs {
		t.Logf("run=%s BEFORE priority(%s, now=%d)=%d", runUUID, j.ID, now, Priority(w, j, now))
	}

	got := drainIDs(q, now)
	t.Logf("run=%s AFTER  dequeue order at now=%d: got=%v want(formula)=%v", runUUID, now, got, want)

	if !eq(got, want) {
		t.Fatalf("dequeue order mismatch: got=%v want=%v", got, want)
	}
	// Independent cross-check against the explicit hand-derived sequence so the
	// test is not merely re-deriving the implementation's own ordering.
	handDerived := []string{"guaranteed-small", "burst-fair", "besteffort-old", "burst-young", "besteffort-big"}
	if !eq(got, handDerived) {
		t.Fatalf("dequeue order does not match hand-derived sequence: got=%v want=%v", got, handDerived)
	}
	if q.Len() != 0 {
		t.Fatalf("queue not drained: len=%d", q.Len())
	}
}

// Clause 2: aging overtake. A low-priority job submitted EARLY overtakes a
// higher-priority reference once enough logical time has passed, because
// priority is recomputed against now (aging), not frozen at insert.
//
// Construction (honest derivation): with a single GLOBAL linear age weight, two
// jobs that BOTH keep aging have parallel priority-vs-now lines, so a constant
// head start could never be closed. A genuine now-DEPENDENT crossover therefore
// compares the aging older job against a reference whose age does NOT grow with
// now: a freshly-arriving higher-QoS job whose SubmitTick == now (age always 0).
// This is the canonical scheduler "starving job vs. fresh arrival" scenario --
// exactly what aging exists to fix. The older job's accrued age term 2*(now-0)
// eventually exceeds the fresh job's constant QoS head start, so the order
// FLIPS as now advances. Both jobs go through a real Queue and Pop(now).
//
// Mutation: drop the Age term (age contributes 0) => older's priority is a flat
// -90 forever, the fresh burstable (=150) always wins, the order never flips ->
// the nowLate and FLIP assertions fail. Freeze priority at insert => order at
// nowLate equals order at nowEarly -> the FLIP assertion fails.
func TestAgingOvertake(t *testing.T) {
	w := Weights{Age: 2, FairShare: 5, Size: 1}

	// older: best-effort, submitted at tick 0, fixed size penalty (-90).
	//   p(older, now) = 0 + 2*(now-0) + 0 - 90 = 2*now - 90
	older := Job{ID: "older", SubmitTick: 0, FairShare: 0, Size: 90, QoS: QoSBestEffort}

	// freshAt models a brand-new higher-QoS arrival at evaluation time `now`
	// (SubmitTick == now => age 0). Its priority is CONSTANT in now: 150.
	//   p(fresh, now) = 150 + 2*0 + 0 - 0 = 150
	freshAt := func(now int64) Job {
		return Job{ID: "fresh", SubmitTick: now, FairShare: 0, Size: 0, QoS: QoSBurstable}
	}

	// BEFORE: small now. older has barely aged; fresh burstable wins.
	nowEarly := int64(10)
	qEarly := New(w)
	qEarly.Push(older)
	qEarly.Push(freshAt(nowEarly))

	pOldEarly := Priority(w, older, nowEarly) // 2*10 - 90 = -70
	pFreshEarly := Priority(w, freshAt(nowEarly), nowEarly)
	orderEarly := drainIDs(qEarly, nowEarly)
	t.Logf("run=%s BEFORE now=%d p(older)=%d p(fresh)=%d dequeue=%v",
		runUUID, nowEarly, pOldEarly, pFreshEarly, orderEarly)
	if pFreshEarly <= pOldEarly {
		t.Fatalf("precondition: fresh must outrank older at now=%d (fresh=%d older=%d)",
			nowEarly, pFreshEarly, pOldEarly)
	}
	if orderEarly[0] != "fresh" {
		t.Fatalf("at now=%d expected fresh first, got %v", nowEarly, orderEarly)
	}

	// AFTER: advance now substantially. The aged older job overtakes.
	nowLate := int64(200)
	qLate := New(w)
	qLate.Push(older)
	qLate.Push(freshAt(nowLate))

	pOldLate := Priority(w, older, nowLate) // 2*200 - 90 = 310
	pFreshLate := Priority(w, freshAt(nowLate), nowLate)
	orderLate := drainIDs(qLate, nowLate)
	t.Logf("run=%s AFTER  now=%d p(older)=%d p(fresh)=%d dequeue=%v",
		runUUID, nowLate, pOldLate, pFreshLate, orderLate)
	if pOldLate <= pFreshLate {
		t.Fatalf("aging overtake failed: at now=%d aged older (=%d) must outrank fresh (=%d)",
			nowLate, pOldLate, pFreshLate)
	}
	if orderLate[0] != "older" {
		t.Fatalf("at now=%d expected aged older first, got %v", nowLate, orderLate)
	}

	// The ordering MUST have flipped purely because now advanced. If priority
	// were frozen at insert, or the age term dropped, these would be equal.
	if eq(orderEarly, orderLate) {
		t.Fatalf("ordering did not flip with now: early=%v late=%v (priority frozen / age ignored)",
			orderEarly, orderLate)
	}
}

// Clause 3: deterministic tie-break by SubmitTick then ID.
//
// Mutation: if the tie-break dropped the SubmitTick key, tieA and tieB (equal
// composite priority, different SubmitTick) would order by ID only and the
// SubmitTick assertion fails. If it dropped the ID key, c and d (equal priority
// AND equal SubmitTick) would have nondeterministic order and the
// stable-repeat assertion fails.
func TestTieBreakDeterministic(t *testing.T) {
	w := Weights{Age: 1, FairShare: 1, Size: 0}
	now := int64(100)

	// tieA, tieB: equal composite priority via fairshare canceling the age gap,
	// but DIFFERENT SubmitTick. p = 0 + 1*(now-submit) + 1*fair.
	// tieA: submit=90, fair=10 -> (100-90) + 10 = 20
	// tieB: submit=95, fair=15 -> (100-95) + 15 = 20
	// IDs chosen so the earlier-submit job has the LEXICALLY LARGER id, proving
	// SubmitTick outranks ID.
	tieA := Job{ID: "B-id", SubmitTick: 90, FairShare: 10, QoS: QoSBestEffort}
	tieB := Job{ID: "A-id", SubmitTick: 95, FairShare: 15, QoS: QoSBestEffort}
	if Priority(w, tieA, now) != Priority(w, tieB, now) {
		t.Fatalf("test setup: tieA and tieB must have equal priority, got %d vs %d",
			Priority(w, tieA, now), Priority(w, tieB, now))
	}
	q := New(w)
	q.Push(tieB) // insert the lexically-smaller-id job first to rule out insertion order
	q.Push(tieA)
	order := q2order(q, now)
	t.Logf("run=%s TIE submit-key: p(tieA)=%d p(tieB)=%d order=%v (expect earlier-submit B-id first)",
		runUUID, Priority(w, tieA, now), Priority(w, tieB, now), order)
	if order[0] != "B-id" {
		t.Fatalf("submit-tiebreak failed: expected earlier-submit 'B-id' first, got %v", order)
	}

	// c, d: equal priority AND equal SubmitTick -> tie-break falls to ID.
	c := Job{ID: "z-job", SubmitTick: 50, FairShare: 7, QoS: QoSBestEffort}
	d := Job{ID: "a-job", SubmitTick: 50, FairShare: 7, QoS: QoSBestEffort}
	if Priority(w, c, now) != Priority(w, d, now) {
		t.Fatalf("test setup: c and d must have equal priority")
	}
	// Run with both insertion orders; result must be stable and ID-ordered.
	for trial, pair := range [][]Job{{c, d}, {d, c}} {
		qq := New(w)
		qq.Push(pair[0])
		qq.Push(pair[1])
		got := drainIDs(qq, now)
		t.Logf("run=%s TIE id-key trial=%d insertion=%v drain=%v", runUUID, trial, idsOf(pair), got)
		want := []string{"a-job", "z-job"}
		if !eq(got, want) {
			t.Fatalf("id-tiebreak not deterministic: trial=%d got=%v want=%v", trial, got, want)
		}
	}
}

// Clause 4: the size penalty is load-bearing for ordering. Two jobs are
// identical in every factor EXCEPT Size; the larger job must dequeue LAST
// purely because of the size penalty, and a precondition asserts the penalty
// is the sole differentiator.
//
// Mutation: zero the size term (w.Size*j.Size -> 0) => big and small have equal
// priority, equal SubmitTick, and order falls to ID; "small" (id) would still
// not be forced first by size. We force the issue by giving the LARGE job the
// lexically smaller id, so without the size penalty the large job would win the
// ID tie-break and dequeue FIRST -> the assertion "small first" fails. Flip the
// size SIGN (penalty->bonus) => large wins on priority -> also fails.
func TestSizePenaltyOrders(t *testing.T) {
	w := DefaultWeights() // Size weight = 1
	now := int64(100)

	// Identical except Size. Give the BIG job the lexically-smaller id ("a-big")
	// so that if the size term vanished, the priorities+SubmitTick would tie and
	// the ID tie-break would put "a-big" first. The size penalty must override.
	small := Job{ID: "z-small", SubmitTick: 50, FairShare: 3, Size: 1, QoS: QoSBurstable}
	big := Job{ID: "a-big", SubmitTick: 50, FairShare: 3, Size: 40, QoS: QoSBurstable}

	pSmall := Priority(w, small, now)
	pBig := Priority(w, big, now)
	t.Logf("run=%s SIZE p(small,size=1)=%d p(big,size=40)=%d", runUUID, pSmall, pBig)
	// Precondition: the ONLY thing separating them is the size penalty, so the
	// priority gap must equal exactly w.Size*(big.Size-small.Size).
	if pSmall-pBig != w.Size*(big.Size-small.Size) {
		t.Fatalf("size precondition: priority gap=%d, expected size-only gap=%d",
			pSmall-pBig, w.Size*(big.Size-small.Size))
	}
	if pSmall <= pBig {
		t.Fatalf("size penalty must make small outrank big: small=%d big=%d", pSmall, pBig)
	}

	// Run under BOTH insertion orders to prove it is the penalty, not luck.
	for trial, pair := range [][]Job{{small, big}, {big, small}} {
		q := New(w)
		q.Push(pair[0])
		q.Push(pair[1])
		got := drainIDs(q, now)
		t.Logf("run=%s SIZE trial=%d insertion=%v drain=%v", runUUID, trial, idsOf(pair), got)
		want := []string{"z-small", "a-big"}
		if !eq(got, want) {
			t.Fatalf("size-penalty ordering failed: trial=%d got=%v want=%v", trial, got, want)
		}
	}
}

// TestPopEmpty documents the empty-queue contract.
//
// Mutation: if Pop returned ok==true on empty, this fails.
func TestPopEmpty(t *testing.T) {
	q := New(DefaultWeights())
	j, ok := q.Pop(0)
	t.Logf("run=%s empty pop -> job=%v ok=%v", runUUID, j, ok)
	if ok {
		t.Fatalf("Pop on empty queue returned ok=true (job=%v)", j)
	}
	if q.Len() != 0 {
		t.Fatalf("empty queue Len=%d", q.Len())
	}
}
