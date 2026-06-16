package admissioncontrol

// Second adversarial pass over the N+K admission gate. The FIRST pass already
// pinned the int64 reserve-sum overflow (now saturated) and the huge-request
// underflow. This pass targets OTHER load-bearing guards the existing suite
// misses, with the same discipline: every probe asserts SINK-SIDE behavior
// (admitted vs. true capacity / documented contract), is mutation-proven
// load-bearing, and never merely checks "no panic".
//
// Findings classified inline:
//   - REAL BUG (FIXED + left): zero-capacity request wrongly REJECTED when the
//     cluster is over-reserved (reserve > free). Violates the verbatim contract
//     "A zero-capacity request is always admitted (it consumes nothing)".
//   - LATENT (documented): totalFreeLocked has no overflow guard, but for
//     non-negative frees the wrapped sum is always <= the true sum, so the
//     overflow is strictly fail-CLOSED (under-counts free, never over-admits).
//     Pinned below so a future change that makes it fail-OPEN trips a test.

import (
	"math"
	"sync"
	"sync/atomic"
	"testing"
)

// ---------------------------------------------------------------------------
// REAL BUG (fixed): zero-capacity request must ALWAYS be admitted, even when the
// cluster is over-reserved so that reserve > free.
//
// The doc on Admit promises verbatim: "A zero-capacity request is always
// admitted (it consumes nothing)." A no-cost job changes nothing about the
// cluster's failure tolerance, so it can never erode the reserve.
//
// Pre-fix code ran the headroom check `free - requestedCapacity >= reserve`
// unconditionally. For req=0 that is `free >= reserve`, which is FALSE whenever
// reserve > free -- a regime that is REACHABLE in normal operation by failing a
// spare node (free drops, reserve stays). The no-cost job was then spuriously
// REJECTED with ReasonInsufficientHeadroom.
//
// MUTATION PROOF: deleting the `if requestedCapacity == 0 { admit }` short
// circuit in Admit makes this test FAIL (Admit(0) rejected). With the fix it
// passes. This is the load-bearing guard.
//
// Severity note: rejecting a no-cost job is fail-CLOSED (conservative), not a
// cap bypass -- but it is a real contract violation that silently denies
// metadata-only / re-validation / observability jobs exactly when the cluster is
// already under failure pressure, which is when those jobs matter most.
func TestAdversarial2_ZeroRequest_AdmittedEvenWhenOverReserved(t *testing.T) {
	// x,y are loaded (free 10 each); z is the empty spare (free 100). K=1.
	// Before failing z: free=120, reserve=100 (largest live cap), avail=20.
	c := mustController(t, 1,
		Node{ID: "x", Capacity: 100, Allocated: 90}, // free 10
		Node{ID: "y", Capacity: 100, Allocated: 90}, // free 10
		Node{ID: "z", Capacity: 100, Allocated: 0},  // free 100 (spare)
	)

	// Sanity: zero is admitted before any failure too.
	if d := c.Admit(0); !d.Admitted {
		t.Fatalf("pre-failure Admit(0) rejected; zero-cost job must always admit. decision=%+v", d)
	}

	// Fail the spare: now live={x,y}, free=20, reserve=100 -> reserve > free.
	if !c.MarkFailed("z") {
		t.Fatal("MarkFailed(z) returned false for a known node")
	}

	// Establish the over-reserved regime that triggers the bug.
	if got := c.ReservedHeadroom(); got <= c.totalFreeForTest() {
		t.Fatalf("test precondition not met: reserve=%d is not > free=%d", got, c.totalFreeForTest())
	}

	// SINK: the zero-cost request MUST be admitted. Pre-fix this was rejected
	// with ReasonInsufficientHeadroom because `free(20) >= reserve(100)` is false.
	d := c.Admit(0)
	if !d.Admitted {
		t.Fatalf("BUG: Admit(0) REJECTED when reserve(%d) > free(%d); the contract guarantees "+
			"a zero-capacity request is ALWAYS admitted (it consumes nothing). decision=%+v",
			d.ReservedHeadroom, c.totalFreeForTest(), d)
	}
	if d.Reason != "" {
		t.Fatalf("Admit(0) admitted but carried a rejection reason %q; want empty", d.Reason)
	}
}

// totalFreeForTest exposes totalFreeLocked for assertions. Package-internal test
// accessor; mirrors how a caller observes aggregate free.
func (c *Controller) totalFreeForTest() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.totalFreeLocked()
}

// ---------------------------------------------------------------------------
// Zero-request admitted under EVERY reserve/free relation, including the
// astronomically-over-reserved case where reserve saturates to MaxInt64. This
// guards the fix against a narrower "only when reserve>free by a little"
// regression and proves the short-circuit is unconditional for req==0.
func TestAdversarial2_ZeroRequest_AdmittedUnderSaturatedReserve(t *testing.T) {
	// Two MaxInt64 reserve-dominating nodes (fully allocated -> free 0) plus a
	// tiny spare. reserve sum overflows and saturates to MaxInt64; free=5.
	c := mustController(t, 2,
		Node{ID: "huge1", Capacity: math.MaxInt64, Allocated: math.MaxInt64},
		Node{ID: "huge2", Capacity: math.MaxInt64, Allocated: math.MaxInt64},
		Node{ID: "spare", Capacity: 5, Allocated: 0},
	)
	if got := c.ReservedHeadroom(); got != math.MaxInt64 {
		t.Fatalf("precondition: reserve=%d, want saturated MaxInt64", got)
	}
	d := c.Admit(0)
	if !d.Admitted {
		t.Fatalf("BUG: Admit(0) rejected under a MaxInt64-saturated reserve; a zero-cost job "+
			"must still admit. decision=%+v", d)
	}
	// And a positive request in this regime MUST still reject (cap stays closed).
	if dd := c.Admit(1); dd.Admitted {
		t.Fatalf("Admit(1) admitted under saturated reserve; the cap must stay closed. decision=%+v", dd)
	}
}

// ---------------------------------------------------------------------------
// LATENT (documented, fail-CLOSED): totalFreeLocked sums free() with no overflow
// guard. We PIN that the overflow is fail-closed: a positive request that does
// NOT genuinely fit must never be admitted via a free-sum wrap. Construct three
// MaxInt64-free nodes whose honest free is astronomical; admitting large
// requests is CORRECT there, so we instead assert the gate never admits a
// request that exceeds the TRUE aggregate free in a regime where the wrap is
// negative (two MaxInt64 nodes -> sum wraps to -2).
//
// With free wrapped to -2 and K=0 (reserve 0), Admit(req) = `-2 - req >= 0`,
// true only for req <= -2 (already rejected as negative). So no positive request
// is admitted -- the wrap under-counts and stays fail-closed. If a future change
// made totalFreeLocked saturate to MaxInt64 (fail-open analog), Admit(1) would
// (wrongly) admit and this test would catch the regression direction.
func TestAdversarial2_FreeSumOverflow_IsFailClosed(t *testing.T) {
	c := mustController(t, 0,
		Node{ID: "a", Capacity: math.MaxInt64, Allocated: 0}, // free MaxInt64
		Node{ID: "b", Capacity: math.MaxInt64, Allocated: 0}, // free MaxInt64
	) // honest free ~1.8e19; int64 sum wraps to -2.

	// AvailableForAdmission must never report a bogus value that exceeds what the
	// gate will actually honor; floored, it reports 0 here (under-count).
	if avail := c.AvailableForAdmission(); avail < 0 {
		t.Fatalf("AvailableForAdmission()=%d negative; floor broken", avail)
	}
	// SINK: positive requests are NOT admitted via the negative wrap (fail-closed).
	for _, req := range []int64{1, 5, 1 << 40} {
		if d := c.Admit(req); d.Admitted {
			t.Fatalf("Admit(%d) admitted via a free-sum overflow wrap; the gate must not "+
				"admit on a wrapped (negative) free total. decision=%+v", req, d)
		}
	}
	// Zero still admits (consumes nothing) -- the contract holds regardless of wrap.
	if d := c.Admit(0); !d.Admitted {
		t.Fatalf("Admit(0) rejected even though it consumes nothing. decision=%+v", d)
	}
}

// ---------------------------------------------------------------------------
// MarkFailed must RE-SELECT the K largest LIVE nodes. Failing the single largest
// node changes which capacities form the reserve; if the reserve were computed
// over a stale or all-nodes set, the post-failure gate would mis-size and could
// admit beyond the true reserve. Concrete, exact-equality sink.
//
// Nodes caps 100,80,60 empty, K=1. reserve=100 (the 100-node). free=240, avail=140.
// Fail the 100-node: live={80,60}, free=140, reserve=80 (new largest), avail=60.
// Admit(60) at the new boundary ADMITS; Admit(61) REJECTS.
//
// Mutation: if reservedHeadroomLocked summed over ALL nodes (ignoring Failed),
// reserve would stay 100, avail=40, and Admit(60) would be (wrongly) REJECTED --
// failing the boundary-admit. If it summed the SMALLEST live, reserve=60, avail=80,
// and Admit(61) would (wrongly) ADMIT -- failing the rejection.
func TestAdversarial2_MarkFailed_ReselectsKLargestLive(t *testing.T) {
	c := mustController(t, 1,
		Node{ID: "big", Capacity: 100, Allocated: 0},
		Node{ID: "mid", Capacity: 80, Allocated: 0},
		Node{ID: "low", Capacity: 60, Allocated: 0},
	)
	if got, want := c.ReservedHeadroom(), int64(100); got != want {
		t.Fatalf("pre-failure reserve=%d, want %d", got, want)
	}
	if !c.MarkFailed("big") {
		t.Fatal("MarkFailed(big) returned false")
	}
	if got, want := c.ReservedHeadroom(), int64(80); got != want {
		t.Fatalf("post-failure reserve=%d, want %d (new largest live node)", got, want)
	}
	if got, want := c.AvailableForAdmission(), int64(60); got != want {
		t.Fatalf("post-failure available=%d, want %d (free 140 - reserve 80)", got, want)
	}
	if d := c.Admit(60); !d.Admitted {
		t.Fatalf("Admit(60) rejected at the recomputed boundary; want ADMIT. decision=%+v", d)
	}
	if d := c.Admit(61); d.Admitted {
		t.Fatalf("Admit(61) admitted beyond the recomputed reserve; want REJECT. decision=%+v", d)
	}
}

// ---------------------------------------------------------------------------
// CONSERVATION UNDER RACE (-race). The only safety promise a single Controller
// method owns is that committed Allocated never exceeds Capacity, and that
// concurrent admit-then-place through a correct (serialized) caller never
// over-commits a node beyond its capacity NOR loses (leaks) committed units.
//
// This differs from the existing compose test: it uses K>0 (a live reserve), it
// places onto a DEDICATED placement node while a reserve node holds the
// headroom, and it asserts CONSERVATION sink-side -- the number of admitted
// 1-unit jobs that actually committed equals the node's allocation delta (no
// double-count, no lost reservation) -- run under the race detector.
func TestAdversarial2_ConcurrentAdmitPlace_ConservesAllocation(t *testing.T) {
	const placeCap = 128
	// reserve node holds K=1 headroom = its own 1000 capacity; the place node has
	// placeCap free. Available for admission = (1000 free on reserve node + placeCap
	// free on place node) - reserve(1000) = placeCap. So at most placeCap unit-jobs
	// should ever be admissible if the caller composed atomically; without atomicity
	// admits over-fire, but UpsertNode's clamp pins committed alloc to <= Capacity.
	c := mustController(t, 1,
		Node{ID: "reserve", Capacity: 1000, Allocated: 0},
		Node{ID: "place", Capacity: placeCap, Allocated: 0},
	)

	var committed int64
	var placeMu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < placeCap*4; i++ { // 4x oversubscription pressure
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := c.Admit(1)
			if !d.Admitted {
				return
			}
			// Correct caller: serialize the read-modify-write placement.
			placeMu.Lock()
			defer placeMu.Unlock()
			n := c.nodeSnapshot("place")
			if n.Allocated >= n.Capacity {
				// Node already full; a correct caller drops this admit rather than
				// over-placing. (Models the atomic re-check a real scheduler does.)
				return
			}
			c.UpsertNode(Node{ID: "place", Capacity: placeCap, Allocated: n.Allocated + 1})
			atomic.AddInt64(&committed, 1)
		}()
	}
	wg.Wait()

	final := c.nodeSnapshot("place")
	// SINK 1: committed Allocated never exceeds Capacity (clamp + caller re-check).
	if final.Allocated > final.Capacity {
		t.Fatalf("BUG: committed Allocated=%d exceeds Capacity=%d (over-commit under race)",
			final.Allocated, final.Capacity)
	}
	// SINK 2: CONSERVATION -- every counted commit is reflected in the stored
	// allocation; no unit is double-counted or leaked. With the caller re-check
	// above, committed == final.Allocated exactly.
	if got := atomic.LoadInt64(&committed); got != final.Allocated {
		t.Fatalf("BUG: conservation violated: counted %d committed units but node Allocated=%d "+
			"(reservation double-counted or leaked)", got, final.Allocated)
	}
	// SINK 3: never admit-and-commit beyond the node's true capacity.
	if final.Allocated > placeCap {
		t.Fatalf("BUG: %d units committed onto a %d-capacity node", final.Allocated, placeCap)
	}
}
