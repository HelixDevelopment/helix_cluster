package backfill

import "testing"

// TestTimelineEarliestStartFindsGapAfterBoundary pins the core occupancy math.
// On a capacity-4 timeline we reserve 3 units over [0,10). A width-2 request
// then cannot fit during [0,10) (3+2 > 4) and must start at the boundary 10.
//
// MUTATION: in earliestStart, seed candidates with only the boundaries and
// drop `notBefore` itself (`candidates := []int{}`). With a different reserve
// pattern that leaves t=0 free this would regress; here specifically, change
// fits to ignore interior boundaries and it returns 0 -> assertion FAILS.
func TestTimelineEarliestStartFindsGapAfterBoundary(t *testing.T) {
	tl := newTimeline(4)
	tl.reserve(0, 10, 3)

	if got := tl.earliestStart(0, 5, 2); got != 10 {
		t.Fatalf("width-2 request must wait until 10, got %d", got)
	}
	// A width-1 request DOES fit immediately in the remaining unit.
	if got := tl.earliestStart(0, 5, 1); got != 0 {
		t.Fatalf("width-1 request must fit at 0, got %d", got)
	}
}

// TestTimelineFitsRejectsInteriorSpike verifies fits() inspects boundaries
// strictly inside the window, not just the start instant.
//
// Reserve 2 units over [5,8) on a capacity-3 timeline. A width-2 request for
// [0,10) starts in free space (used=0 at t=0) but collides at t=5 (2+2>3).
// fits MUST return false.
//
// MUTATION: in fits, change the interior loop bound from `b < end` to `b <= 0`
// (skip interior checks). fits would then accept [0,10) and FAIL this test.
func TestTimelineFitsRejectsInteriorSpike(t *testing.T) {
	tl := newTimeline(3)
	tl.reserve(5, 3, 2)

	if tl.fits(0, 10, 2) {
		t.Fatalf("fits must reject a window that collides with an interior spike at t=5")
	}
	// The same request fits if it ends before the spike.
	if !tl.fits(0, 5, 2) {
		t.Fatalf("window [0,5) must fit (ends exactly at the spike start)")
	}
	// And fits after the spike clears.
	if !tl.fits(8, 5, 3) {
		t.Fatalf("window [8,13) must fit at full capacity after spike clears")
	}
}

// TestTimelineReservePanicsOnOvercommit asserts the hard invariant guard.
//
// MUTATION: delete the fits() guard at the top of reserve(). The overcommit
// below would silently succeed and no panic occurs -> this test FAILS.
func TestTimelineReservePanicsOnOvercommit(t *testing.T) {
	tl := newTimeline(2)
	tl.reserve(0, 5, 2) // fully booked over [0,5)

	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("reserve must panic when committing past capacity")
		}
	}()
	tl.reserve(0, 5, 1) // 2+1 > 2 -> must panic
}
