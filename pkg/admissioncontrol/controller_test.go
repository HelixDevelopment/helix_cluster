package admissioncontrol

import (
	"sync"
	"testing"
)

// mustController builds a controller or fails the test.
func mustController(t *testing.T, k int, nodes ...Node) *Controller {
	t.Helper()
	c, err := NewController(k, nodes...)
	if err != nil {
		t.Fatalf("NewController(k=%d): unexpected error: %v", k, err)
	}
	return c
}

// TestReservedHeadroom_IsKLargestNodes pins the reserve to the K LARGEST nodes'
// capacity using a concrete vector. Capacities 10,40,20,30 with K=2 -> the two
// largest are 40 and 30 -> reserve = 70 (NOT 10+20=30, NOT 0).
//
// Mutation: sorting ascending (reserving the K SMALLEST) yields 10+20=30 and
// fails this exact-equality assertion. Likewise a no-op reserve yields 0.
func TestReservedHeadroom_IsKLargestNodes(t *testing.T) {
	c := mustController(t, 2,
		Node{ID: "a", Capacity: 10},
		Node{ID: "b", Capacity: 40},
		Node{ID: "c", Capacity: 20},
		Node{ID: "d", Capacity: 30},
	)
	if got, want := c.ReservedHeadroom(), int64(70); got != want {
		t.Fatalf("ReservedHeadroom() = %d, want %d (40+30, the two LARGEST nodes)", got, want)
	}
}

// TestAdmit_RejectsWhenHeadroomWouldBreak_K1 builds a cluster where free capacity
// is only slightly above the largest node, so admitting a job that eats into the
// reserve must be REJECTED.
//
// Nodes: largest=50 (cap), plus capacity totals giving free=60. Reserve (K=1) =
// 50. AvailableForAdmission = 60-50 = 10. A request of 11 leaves free=49 < 50 =>
// REJECT. A request of 10 leaves free=50 >= 50 => ADMIT (boundary).
//
// Mutation: reserving the K SMALLEST node (smallest cap = 20 here) sets reserve=20,
// AvailableForAdmission=40, and the size-11 job is (wrongly) ADMITTED — failing
// the rejection assertion below. Reserve=0 admits it too.
func TestAdmit_RejectsWhenHeadroomWouldBreak_K1(t *testing.T) {
	// node big: cap 50, alloc 30 -> free 20
	// node mid: cap 40, alloc 20 -> free 20
	// node sml: cap 20, alloc 0  -> free 20
	// total free = 60; largest capacity = 50 => reserve(K=1)=50; available=10.
	c := mustController(t, 1,
		Node{ID: "big", Capacity: 50, Allocated: 30},
		Node{ID: "mid", Capacity: 40, Allocated: 20},
		Node{ID: "sml", Capacity: 20, Allocated: 0},
	)

	if got, want := c.ReservedHeadroom(), int64(50); got != want {
		t.Fatalf("reserve = %d, want %d (largest node cap)", got, want)
	}
	if got, want := c.AvailableForAdmission(), int64(10); got != want {
		t.Fatalf("available = %d, want %d (free 60 - reserve 50)", got, want)
	}

	// Over-the-line request: must be rejected.
	d := c.Admit(11)
	if d.Admitted {
		t.Fatalf("Admit(11) admitted; want REJECT. decision=%+v", d)
	}
	if d.Reason != ReasonInsufficientHeadroom {
		t.Fatalf("Admit(11) reason = %q, want %q", d.Reason, ReasonInsufficientHeadroom)
	}
	if d.ReservedHeadroom != 50 || d.AvailableForAdmission != 10 {
		t.Fatalf("decision reserve/available = %d/%d, want 50/10", d.ReservedHeadroom, d.AvailableForAdmission)
	}
}

// TestAdmit_AdmitsWithinHeadroom verifies a job that fits within
// AvailableForAdmission is ADMITTED, and the exact boundary job (== available)
// is also admitted while one unit beyond is rejected.
//
// Mutation: a strict `>` instead of `>=` in the invariant check rejects the exact
// boundary job (size 10) and fails the boundary-admit assertion.
func TestAdmit_AdmitsWithinHeadroom(t *testing.T) {
	c := mustController(t, 1,
		Node{ID: "big", Capacity: 50, Allocated: 30}, // free 20
		Node{ID: "mid", Capacity: 40, Allocated: 20}, // free 20
		Node{ID: "sml", Capacity: 20, Allocated: 0},  // free 20
	) // free=60, reserve=50, available=10

	if d := c.Admit(5); !d.Admitted {
		t.Fatalf("Admit(5) rejected; want ADMIT. decision=%+v", d)
	}
	// Exact boundary: request == available (10) leaves free exactly == reserve.
	if d := c.Admit(10); !d.Admitted {
		t.Fatalf("Admit(10) rejected; boundary job (==available) must be admitted. decision=%+v", d)
	}
	// One beyond boundary: rejected.
	if d := c.Admit(11); d.Admitted {
		t.Fatalf("Admit(11) admitted; one unit beyond available must be rejected. decision=%+v", d)
	}
}

// TestMarkFailed_RecomputesAndRejects shows that failing a node recomputes the
// reserve and can turn a previously-fine job into a rejection.
//
// Before failure: nodes cap 100(alloc 90),100(alloc 90),100(alloc 0). free=120,
// reserve(K=1)=100, available=20. Admit(20) ADMITS (boundary).
// After failing the empty 100-cap node: free=20, live nodes both cap 100,
// reserve=100, available=0 (floored). Admit(20) now REJECTS.
//
// Mutation: if MarkFailed did not exclude the node from free/reserve (i.e. the
// recompute ignored Failed), the post-failure Admit(20) would still be admitted,
// failing the post-failure rejection assertion.
func TestMarkFailed_RecomputesAndRejects(t *testing.T) {
	c := mustController(t, 1,
		Node{ID: "x", Capacity: 100, Allocated: 90}, // free 10
		Node{ID: "y", Capacity: 100, Allocated: 90}, // free 10
		Node{ID: "z", Capacity: 100, Allocated: 0},  // free 100 (the spare)
	) // free=120, reserve=100, available=20

	if d := c.Admit(20); !d.Admitted {
		t.Fatalf("pre-failure Admit(20) rejected; want ADMIT. decision=%+v", d)
	}

	if !c.MarkFailed("z") {
		t.Fatal("MarkFailed(z) returned false for a known node")
	}

	// Recompute: free now 20, reserve still 100 (two live 100-cap nodes),
	// available floored to 0.
	if got, want := c.AvailableForAdmission(), int64(0); got != want {
		t.Fatalf("post-failure available = %d, want %d", got, want)
	}
	d := c.Admit(20)
	if d.Admitted {
		t.Fatalf("post-failure Admit(20) admitted; want REJECT after losing the spare node. decision=%+v", d)
	}
	if d.Reason != ReasonInsufficientHeadroom {
		t.Fatalf("post-failure reason = %q, want %q", d.Reason, ReasonInsufficientHeadroom)
	}
}

// TestAdmit_K0_NoReserve verifies K=0 disables the reserve: the cluster admits up
// to raw free capacity and rejects only beyond it.
//
// Mutation: if reservedHeadroomLocked summed the largest node even for k=0 (e.g.
// off-by-one `i <= k`), reserve would be the largest node's cap, available would
// shrink, and Admit(free) would be rejected — failing the full-capacity admit.
func TestAdmit_K0_NoReserve(t *testing.T) {
	c := mustController(t, 0,
		Node{ID: "a", Capacity: 30, Allocated: 10}, // free 20
		Node{ID: "b", Capacity: 30, Allocated: 5},  // free 25
	) // free=45, reserve=0, available=45

	if got, want := c.ReservedHeadroom(), int64(0); got != want {
		t.Fatalf("K=0 reserve = %d, want 0", got)
	}
	if got, want := c.AvailableForAdmission(), int64(45); got != want {
		t.Fatalf("K=0 available = %d, want raw free 45", got)
	}
	if d := c.Admit(45); !d.Admitted {
		t.Fatalf("K=0 Admit(45) (==raw free) rejected; want ADMIT. decision=%+v", d)
	}
	if d := c.Admit(46); d.Admitted {
		t.Fatalf("K=0 Admit(46) admitted; beyond raw free must reject. decision=%+v", d)
	}
}

// TestAdmit_K2_ReservesTwoLargest verifies the multi-K case reserves the SUM of
// the two largest nodes, not just one and not the smallest two.
//
// Nodes caps 100,80,60,40 all empty => free=280. Reserve(K=2)=100+80=180.
// available=100. Admit(100) ADMITS; Admit(101) REJECTS.
//
// Mutation: reserving the two SMALLEST (40+60=100) makes available=180 and
// Admit(101) wrongly ADMITS — failing the rejection assertion.
func TestAdmit_K2_ReservesTwoLargest(t *testing.T) {
	c := mustController(t, 2,
		Node{ID: "a", Capacity: 100},
		Node{ID: "b", Capacity: 80},
		Node{ID: "c", Capacity: 60},
		Node{ID: "d", Capacity: 40},
	)
	if got, want := c.ReservedHeadroom(), int64(180); got != want {
		t.Fatalf("reserve = %d, want 180 (100+80)", got)
	}
	if got, want := c.AvailableForAdmission(), int64(100); got != want {
		t.Fatalf("available = %d, want 100 (free 280 - reserve 180)", got)
	}
	if d := c.Admit(100); !d.Admitted {
		t.Fatalf("Admit(100) rejected; want ADMIT at boundary. decision=%+v", d)
	}
	if d := c.Admit(101); d.Admitted {
		t.Fatalf("Admit(101) admitted; want REJECT beyond two-largest reserve. decision=%+v", d)
	}
}

// TestAdmit_NegativeRequest_Rejected verifies a negative ask is rejected with the
// dedicated reason and never treated as freeing capacity.
//
// Mutation: dropping the `requestedCapacity < 0` guard makes `free - (-5) >=
// reserve` trivially true, (wrongly) ADMITTING the negative request.
func TestAdmit_NegativeRequest_Rejected(t *testing.T) {
	c := mustController(t, 1, Node{ID: "a", Capacity: 10})
	d := c.Admit(-5)
	if d.Admitted {
		t.Fatalf("Admit(-5) admitted; negative requests must reject. decision=%+v", d)
	}
	if d.Reason != ReasonNegativeRequest {
		t.Fatalf("Admit(-5) reason = %q, want %q", d.Reason, ReasonNegativeRequest)
	}
}

// TestUpsert_OverAllocationClamped verifies an over-allocated node contributes
// zero free capacity (not negative), so free is computed honestly.
//
// Nodes: a cap 10 alloc 25 (clamped alloc->10, free 0); b cap 100 alloc 0
// (free 100). free=100. reserve(K=1)=100 (largest cap). available=0.
//
// Mutation: if free() returned Capacity-Allocated without the negative floor,
// node a would contribute -15, free would be 85, reserve 100, available floored
// 0 — but Admit's `free-req >= reserve` uses the wrong free (85), so Admit(0)
// computes 85>=100 == false and a zero job would be REJECTED, failing the
// zero-job admit below.
func TestUpsert_OverAllocationClamped(t *testing.T) {
	c := mustController(t, 1,
		Node{ID: "a", Capacity: 10, Allocated: 25}, // clamps to free 0
		Node{ID: "b", Capacity: 100, Allocated: 0}, // free 100
	)
	if got, want := c.AvailableForAdmission(), int64(0); got != want {
		t.Fatalf("available = %d, want 0", got)
	}
	// A zero-capacity job consumes nothing; free(100) - 0 >= reserve(100) -> admit.
	if d := c.Admit(0); !d.Admitted {
		t.Fatalf("Admit(0) rejected; a no-cost job must always be admitted. decision=%+v", d)
	}
}

// TestNewController_NegativeK_Error verifies construction rejects negative K.
//
// Mutation: removing the `k < 0` guard returns a non-nil controller and nil error,
// failing this assertion.
func TestNewController_NegativeK_Error(t *testing.T) {
	if c, err := NewController(-1); err == nil || c != nil {
		t.Fatalf("NewController(-1) = (%v, %v), want (nil, error)", c, err)
	}
}

// TestConcurrentAdmit_NoRace exercises the RWMutex under -race with concurrent
// readers and writers. The assertion is the data-integrity invariant: after the
// storm, AvailableForAdmission equals the deterministic recomputed value.
//
// Mutation: dropping the mutex (or using a plain map without locking) triggers the
// race detector, failing the test under `go test -race`.
func TestConcurrentAdmit_NoRace(t *testing.T) {
	c := mustController(t, 1,
		Node{ID: "a", Capacity: 100, Allocated: 0},
		Node{ID: "b", Capacity: 100, Allocated: 0},
	) // free=200, reserve=100, available=100

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = c.Admit(int64(i % 50))
			_ = c.ReservedHeadroom()
			_ = c.AvailableForAdmission()
			c.UpsertNode(Node{ID: "a", Capacity: 100, Allocated: 0})
		}(i)
	}
	wg.Wait()

	if got, want := c.AvailableForAdmission(), int64(100); got != want {
		t.Fatalf("post-storm available = %d, want %d", got, want)
	}
}
