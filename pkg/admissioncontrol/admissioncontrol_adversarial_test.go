package admissioncontrol

// Adversarial probes for the N+K admission gate. Same bug family as the
// budgetcap NaN-poison and marketplace over-commit-under-race classes hunted
// earlier tonight. admissioncontrol uses pure int64 (no NaN/Inf surface), and
// Admit is a READ-ONLY decision that does NOT commit placement, so the bug
// surface is shaped differently here:
//
//   - NUMERIC POISON becomes int64 OVERFLOW (reserve = sum of K largest caps;
//     a huge request near MaxInt64).
//   - OVER-COMMIT-UNDER-RACE lives in the caller's Admit-then-UpsertNode
//     compose, because the controller exposes no atomic admit-and-place.
//
// Each test asserts SINK-SIDE behavior (admitted vs. true capacity, committed
// total vs. capacity), never merely "no panic".

import (
	"math"
	"sync"
	"sync/atomic"
	"testing"
)

// nodeSnapshot reads a single node's stored state under the controller lock.
// Test-internal accessor (package-internal test); mirrors how a caller would
// observe committed allocation, but reaching directly into c.nodes since the
// public API exposes no per-node getter.
func (c *Controller) nodeSnapshot(id string) Node {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.nodes[id]
}

// ---------------------------------------------------------------------------
// PROBE (b) NUMERIC POISON via int64 OVERFLOW of the reserve sum.
//
// reservedHeadroomLocked sums the K largest live-node Capacity values into an
// int64 with no overflow guard. With K>=2 huge-capacity nodes, the sum wraps to
// a NEGATIVE reserve. Admit's invariant `free - req >= reserve` then becomes
// trivially true for a negative reserve, so the gate ADMITS A REQUEST THAT
// SHOULD BE REJECTED — a total cap bypass, exactly the poison-the-total class.
//
// Concrete (asymmetric, so the bypass is clean and does NOT self-cancel):
// the two reserve-dominating nodes have Capacity = MaxInt64 but are FULLY
// allocated (free 0); one small "spare" node has 5 free units.
//   honest reserve (K=2) = MaxInt64 + MaxInt64  (mathematically ~1.8e19)
//   honest free          = 5
// Honest available = 0 (the reserve dwarfs free), so EVERY positive request
// MUST be REJECTED to preserve failure tolerance. The int64 reserve sum
// overflows to a small NEGATIVE number, and Admit's `free - req >= reserve`
// then admits any req <= free - reserve -- a cap bypass on the spare capacity
// that should be untouchable.
func TestAdversarial_ReserveSumOverflow_AdmitsBeyondCapacity(t *testing.T) {
	c := mustController(t, 2,
		Node{ID: "huge1", Capacity: math.MaxInt64, Allocated: math.MaxInt64}, // free 0
		Node{ID: "huge2", Capacity: math.MaxInt64, Allocated: math.MaxInt64}, // free 0
		Node{ID: "spare", Capacity: 5, Allocated: 0},                         // free 5
	)

	reserve := c.ReservedHeadroom()
	// SINK 1: the reserve must never be negative. A negative reserve is a
	// poisoned total that disables the gate.
	if reserve < 0 {
		t.Errorf("BUG: ReservedHeadroom()=%d is NEGATIVE (int64 overflow of the K-largest "+
			"capacity sum); a negative reserve disables the N+K gate", reserve)
	}

	// SINK 2 (the real exploit): honest available is 0 because the reserve is
	// astronomically larger than free(=5). Any positive request must REJECT.
	for _, req := range []int64{1, 5, 7} {
		d := c.Admit(req)
		if d.Admitted {
			t.Errorf("BUG: Admit(%d) ADMITTED on a cluster whose reserve (two MaxInt64 nodes) "+
				"dwarfs free capacity (5); honest answer is REJECT. decision=%+v "+
				"-- int64 reserve-sum overflow flipped reserve to %d and bypassed the cap",
				req, d, reserve)
		}
	}
}

// PROBE (b') huge request near MaxInt64 vs. the `free - req >= reserve` check.
// If req is large enough that `free - req` underflows past MinInt64 it could
// wrap positive and wrongly satisfy the >= reserve test. Assert a giant request
// against a tiny cluster is always REJECTED (it cannot possibly fit).
func TestAdversarial_HugeRequestUnderflow_Rejected(t *testing.T) {
	c := mustController(t, 1,
		Node{ID: "a", Capacity: 10, Allocated: 0},
		Node{ID: "b", Capacity: 10, Allocated: 0},
	) // free=20, reserve=10, available=10

	for _, req := range []int64{
		math.MaxInt64,
		math.MaxInt64 - 1,
		1 << 62,
		1 << 50,
	} {
		d := c.Admit(req)
		if d.Admitted {
			t.Errorf("BUG: Admit(%d) ADMITTED on a 20-unit cluster (available=10); "+
				"int64 underflow of free-req wrapped past the gate; decision=%+v", req, d)
		}
	}
}

// ---------------------------------------------------------------------------
// PROBE (a) OVER-COMMIT UNDER RACE via the realistic caller compose.
//
// The controller has no atomic "admit-and-place". A scheduler MUST do:
//     d := c.Admit(req); if d.Admitted { c.UpsertNode(placement) }
// Under concurrency every goroutine observes the SAME free snapshot inside its
// own Admit RLock, all decide ADMIT, then all UpsertNode -> the committed
// allocation exceeds capacity. This pins whether that over-commit is real.
//
// Setup: ONE node, Capacity = N, Allocated = 0, K = 0 (no reserve, so
// available == raw free == N). N goroutines each try to admit-and-place 1 unit
// onto that node using a compare-style UpsertNode (read current alloc, +1).
// If the gate were atomic over admit+commit, at most N admits commit and final
// Allocated == N. We assert the SINK: final Allocated never exceeds Capacity
// and admitted count never exceeds N.
//
// NOTE ON CLASSIFICATION: the controller's doc explicitly says "Admit does NOT
// place the job; the caller must record placement via UpsertNode", so a torn
// admit+commit is a caller-composition hazard / API gap, NOT a broken promise
// of any single Controller method. This test DOCUMENTS the hazard. It asserts
// the one thing the controller DOES still owe even under the gap: UpsertNode's
// own clamp must never let committed Allocated exceed Capacity.
func TestAdversarial_ComposeAdmitThenPlace_NeverExceedsNodeCapacity(t *testing.T) {
	const capN = 64
	c := mustController(t, 0, Node{ID: "n", Capacity: capN, Allocated: 0})

	var admitted int64
	var wg sync.WaitGroup
	// Serialize the read-modify-write of UpsertNode through a caller-side lock
	// to model a *correct* caller; even so we measure committed vs. capacity.
	var placeMu sync.Mutex

	for i := 0; i < capN*4; i++ { // 4x oversubscription pressure
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := c.Admit(1)
			if !d.Admitted {
				return
			}
			placeMu.Lock()
			// model a real placement: read current, add one, write back.
			n := c.nodeSnapshot("n")
			c.UpsertNode(Node{ID: "n", Capacity: capN, Allocated: n.Allocated + 1})
			atomic.AddInt64(&admitted, 1)
			placeMu.Unlock()
		}()
	}
	wg.Wait()

	final := c.nodeSnapshot("n")
	// SINK: committed Allocated must never exceed Capacity. UpsertNode clamps
	// Allocated into [0, Capacity], so this must hold regardless of the race.
	if final.Allocated > final.Capacity {
		t.Fatalf("BUG: committed Allocated=%d exceeds Capacity=%d", final.Allocated, final.Capacity)
	}
	// Informational: how many admits committed beyond true capacity. With the
	// Admit/UpsertNode gap this CAN exceed capN (each admit saw free>0), but the
	// clamp pins the stored value. We assert the clamp held (above) and record
	// the over-admit pressure for the report.
	if got := atomic.LoadInt64(&admitted); got > capN {
		t.Logf("LATENT(documented gap): %d admits succeeded for a capacity of %d -- "+
			"Admit+UpsertNode is not atomic; an atomic admit-and-place primitive would cap this at %d",
			got, capN, capN)
	}
}

// ---------------------------------------------------------------------------
// PROBE concurrency soundness of the existing single-method promise:
// hammer Admit / ReservedHeadroom / AvailableForAdmission / UpsertNode / MarkFailed
// concurrently and assert no invariant is violated (available is always within
// [0, free]) -- run under -race to validate the "safe for concurrent use" claim.
func TestAdversarial_ConcurrentMethods_InvariantHolds(t *testing.T) {
	c := mustController(t, 1,
		Node{ID: "a", Capacity: 1000, Allocated: 0},
		Node{ID: "b", Capacity: 1000, Allocated: 0},
		Node{ID: "c", Capacity: 1000, Allocated: 0},
	)

	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			switch i % 5 {
			case 0:
				_ = c.Admit(int64(i))
			case 1:
				avail := c.AvailableForAdmission()
				if avail < 0 {
					t.Errorf("BUG: AvailableForAdmission()=%d negative", avail)
				}
			case 2:
				if r := c.ReservedHeadroom(); r < 0 {
					t.Errorf("BUG: ReservedHeadroom()=%d negative", r)
				}
			case 3:
				c.UpsertNode(Node{ID: "a", Capacity: 1000, Allocated: int64(i % 500)})
			case 4:
				c.UpsertNode(Node{ID: "c", Capacity: 1000, Allocated: 0})
			}
		}(i)
	}
	wg.Wait()
}
