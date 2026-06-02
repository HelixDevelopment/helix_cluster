package budgetcap

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

const runUUID = "budgetcap-run-7f3a9c21-HXC1497"

// TestCapRejectsBreachAndPreservesCommitted covers CLOSURE clause 1:
// cap=10, allocate 4+3+2=9 (ok), then +2 would be 11 -> rejected with
// ErrWouldExceedBudget and committed stays exactly 9.
//
// Mutation: if the admit check `committed+cost <= cap` is changed to always
// admit (ignoring committed), the breaching Allocate would return nil and
// committed would become 11 -> both assertions below fail.
func TestCapRejectsBreachAndPreservesCommitted(t *testing.T) {
	bc := NewBudgetCap(10.0)

	mustAllocate := func(id string, cost float64) {
		t.Helper()
		if err := bc.Allocate(id, cost); err != nil {
			t.Fatalf("[%s] Allocate(%s,%v) unexpected error: %v", runUUID, id, cost, err)
		}
	}
	mustAllocate("a", 4.0)
	mustAllocate("b", 3.0)
	mustAllocate("c", 2.0)

	before := bc.Committed()
	t.Logf("[%s] before breaching allocate: committed=%v remaining=%v", runUUID, before, bc.Remaining())
	if before != 9.0 {
		t.Fatalf("[%s] expected committed 9.0 after 4+3+2, got %v", runUUID, before)
	}

	// This allocation (+2 -> 11) breaches the cap of 10 and must be rejected.
	err := bc.Allocate("d", 2.0)
	after := bc.Committed()
	t.Logf("[%s] after breaching allocate attempt: err=%v committed=%v", runUUID, err, after)

	if !errors.Is(err, ErrWouldExceedBudget) {
		t.Fatalf("[%s] breaching Allocate: want ErrWouldExceedBudget, got %v", runUUID, err)
	}
	if after != 9.0 {
		t.Fatalf("[%s] committed must stay 9.0 after rejection, got %v (reserve violated)", runUUID, after)
	}
	// The rejected allocation must not be recorded.
	if _, present := bc.allocations["d"]; present {
		t.Fatalf("[%s] rejected allocation 'd' must not be recorded", runUUID)
	}
}

// TestReleaseFreesHeadroomEnablesRetry covers CLOSURE clause 2:
// after a rejection, releasing an existing allocation frees headroom so the
// previously-rejected allocation now succeeds.
//
// Mutation: if Release does NOT free the cost (committed -= cost removed),
// committed stays at 9 after Release and the retry of "d" (+2 -> 11) would
// still be rejected -> the final success assertion fails.
func TestReleaseFreesHeadroomEnablesRetry(t *testing.T) {
	bc := NewBudgetCap(10.0)
	_ = bc.Allocate("a", 4.0)
	_ = bc.Allocate("b", 3.0)
	_ = bc.Allocate("c", 2.0) // committed = 9

	// "d" (+2 -> 11) is rejected at committed 9.
	if err := bc.Allocate("d", 2.0); !errors.Is(err, ErrWouldExceedBudget) {
		t.Fatalf("[%s] precondition: expected rejection of 'd', got %v", runUUID, err)
	}
	committedBeforeRelease := bc.Committed()
	t.Logf("[%s] committed before release=%v (d rejected)", runUUID, committedBeforeRelease)

	// Release "b" (3.0) -> committed should drop to 6, freeing headroom.
	bc.Release("b")
	committedAfterRelease := bc.Committed()
	t.Logf("[%s] committed after Release(b)=%v remaining=%v", runUUID, committedAfterRelease, bc.Remaining())
	if committedAfterRelease != 6.0 {
		t.Fatalf("[%s] Release(b) must free 3.0: want committed 6.0, got %v", runUUID, committedAfterRelease)
	}

	// Now "d" (+2 -> 8) must succeed.
	if err := bc.Allocate("d", 2.0); err != nil {
		t.Fatalf("[%s] after Release, retry Allocate(d,2.0) must succeed, got %v", runUUID, err)
	}
	final := bc.Committed()
	t.Logf("[%s] committed after successful retry of d=%v", runUUID, final)
	if final != 8.0 {
		t.Fatalf("[%s] want committed 8.0 after retry, got %v", runUUID, final)
	}
}

// TestConcurrentAllocateNeverExceedsCap covers CLOSURE clause 3:
// many goroutines Allocate concurrently against a tight cap; committed must
// never exceed the cap and the number admitted must equal cap/unitCost exactly.
//
// Mutation: if the admit check ignores committed (always admit), more than
// `capacity` allocations get admitted and committed overshoots the cap ->
// both the admitted-count and committed assertions fail. Run with -race to
// also catch loss of mutex guarding.
func TestConcurrentAllocateNeverExceedsCap(t *testing.T) {
	const cap = 20.0
	const unitCost = 1.0
	const goroutines = 200 // far more than capacity (20)
	expectedAdmits := int(cap / unitCost)

	bc := NewBudgetCap(cap)

	var admitted int64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer wg.Done()
			id := "alloc-" + itoa(n)
			if err := bc.Allocate(id, unitCost); err == nil {
				atomic.AddInt64(&admitted, 1)
			}
		}(i)
	}
	wg.Wait()

	got := atomic.LoadInt64(&admitted)
	finalCommitted := bc.Committed()
	t.Logf("[%s] concurrent: goroutines=%d cap=%v unitCost=%v admitted=%d committed=%v",
		runUUID, goroutines, cap, unitCost, got, finalCommitted)

	if finalCommitted > cap {
		t.Fatalf("[%s] committed %v exceeded cap %v (reserve violated)", runUUID, finalCommitted, cap)
	}
	if int(got) != expectedAdmits {
		t.Fatalf("[%s] want exactly %d admitted, got %d", runUUID, expectedAdmits, got)
	}
	if finalCommitted != float64(expectedAdmits)*unitCost {
		t.Fatalf("[%s] committed %v != admitted*unitCost %v", runUUID, finalCommitted, float64(expectedAdmits)*unitCost)
	}
}

// TestRemainingAndDuplicateAndInvalid exercises remaining headroom accounting,
// duplicate-id rejection, and negative-cost rejection (end-user-visible guards).
//
// Mutation: removing the duplicate guard would let "x" be allocated twice and
// double-count committed (2.0 instead of 1.0); removing the negative guard
// would let committed go negative.
func TestRemainingAndDuplicateAndInvalid(t *testing.T) {
	bc := NewBudgetCap(5.0)
	if r := bc.Remaining(); r != 5.0 {
		t.Fatalf("[%s] initial Remaining want 5.0, got %v", runUUID, r)
	}
	if err := bc.Allocate("x", 1.0); err != nil {
		t.Fatalf("[%s] Allocate(x,1) want nil, got %v", runUUID, err)
	}
	if r := bc.Remaining(); r != 4.0 {
		t.Fatalf("[%s] Remaining after 1.0 want 4.0, got %v", runUUID, r)
	}
	if err := bc.Allocate("x", 1.0); !errors.Is(err, ErrDuplicateAllocation) {
		t.Fatalf("[%s] duplicate id must be rejected, got %v", runUUID, err)
	}
	if c := bc.Committed(); c != 1.0 {
		t.Fatalf("[%s] duplicate must not double-count, committed want 1.0 got %v", runUUID, c)
	}
	if err := bc.Allocate("neg", -2.0); !errors.Is(err, ErrInvalidCost) {
		t.Fatalf("[%s] negative cost must be rejected, got %v", runUUID, err)
	}
	if c := bc.Committed(); c != 1.0 {
		t.Fatalf("[%s] negative-cost rejection must not change committed, got %v", runUUID, c)
	}
	t.Logf("[%s] guards ok: committed=%v remaining=%v", runUUID, bc.Committed(), bc.Remaining())
}

// itoa is a tiny base-10 int formatter to avoid importing strconv for ids.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
