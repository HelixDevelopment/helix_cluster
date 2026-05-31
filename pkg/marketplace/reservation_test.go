package marketplace

import (
	"errors"
	"sync"
	"testing"
)

// TestReservation_DeniesCrossClassOveruse is the anti-bluff cross-class denial
// test. Two GPU classes with separate pools: A100 has 4 units, H100 has 2. We
// fully book H100 (2/2), leaving A100 with 4 free. A further H100 request MUST be
// denied even though the cluster as a whole has 4 idle A100 units — capacity does
// NOT cross class boundaries.
//
// We assert concretely: the denied error is ErrInsufficientCapacity AND names the
// H100 class; AND the A100 pool was untouched (still 4 free) so no silent
// borrowing happened.
//
// Mutation that this kills: in Reserve, check capacity against the GLOBAL total
// instead of the per-class total (e.g. sum all capacities / all used). Then the
// third H100 request would be ALLOWED because cluster-wide 6 units exist, and the
// "expected ErrInsufficientCapacity" assertion FAILS. Equally, a mutation that
// drew the overflow from A100 would change A100's free count and fail the
// "A100 untouched" assertion.
func TestReservation_DeniesCrossClassOveruse(t *testing.T) {
	tbl := NewGPUReservationTable(map[string]int{"A100": 4, "H100": 2})

	r1, err := tbl.Reserve("H100", 2) // fully books H100
	if err != nil {
		t.Fatalf("reserve H100 2: %v", err)
	}
	if free, _ := tbl.Available("H100"); free != 0 {
		t.Fatalf("H100 free after full booking = %d, want 0", free)
	}

	// A100 still has 4 idle units cluster-wide — but they must NOT satisfy H100.
	if free, _ := tbl.Available("A100"); free != 4 {
		t.Fatalf("A100 free = %d, want 4", free)
	}

	_, err = tbl.Reserve("H100", 1)
	if !errors.Is(err, ErrInsufficientCapacity) {
		t.Fatalf("over-booking H100 err = %v, want ErrInsufficientCapacity", err)
	}

	// The denied request must not have borrowed from (or otherwise disturbed) A100.
	if free, _ := tbl.Available("A100"); free != 4 {
		t.Fatalf("A100 free after denied H100 request = %d, want 4 (no cross-class borrow)", free)
	}

	// Releasing the H100 reservation frees its units back into H100 only.
	r1.Release()
	if free, _ := tbl.Available("H100"); free != 2 {
		t.Fatalf("H100 free after release = %d, want 2", free)
	}
}

// TestReservation_UnknownClassDenied proves a job for an unprovisioned class is
// denied, not served from some other class.
//
// Mutation that this kills: in Reserve, default an unknown class to some other
// class's pool (or to unlimited). Then ErrUnknownClass would not be returned and
// this assertion FAILS.
func TestReservation_UnknownClassDenied(t *testing.T) {
	tbl := NewGPUReservationTable(map[string]int{"A100": 4})
	_, err := tbl.Reserve("TPUv5", 1)
	if !errors.Is(err, ErrUnknownClass) {
		t.Fatalf("unknown-class err = %v, want ErrUnknownClass", err)
	}
}

// TestReservation_ExactCapacityBoundary proves the boundary is inclusive: a
// reservation that exactly fills a class succeeds, and one more unit is denied.
//
// Mutation that this kills: change the guard from `used+count > total` to
// `used+count >= total`. Then reserving exactly `total` (4 of 4) would be denied
// and the "exact fill succeeds" assertion FAILS.
func TestReservation_ExactCapacityBoundary(t *testing.T) {
	tbl := NewGPUReservationTable(map[string]int{"A100": 4})
	if _, err := tbl.Reserve("A100", 4); err != nil {
		t.Fatalf("exact-fill reserve = %v, want success", err)
	}
	if _, err := tbl.Reserve("A100", 1); !errors.Is(err, ErrInsufficientCapacity) {
		t.Fatalf("over-fill err = %v, want ErrInsufficientCapacity", err)
	}
}

// TestReservation_InvalidCount proves a non-positive count is rejected and does
// not mutate usage.
//
// Mutation that this kills: drop the `count <= 0` guard. A Reserve(class, 0)
// would then "succeed" with a zero-unit reservation and this assertion FAILS;
// a negative count would corrupt the used counter.
func TestReservation_InvalidCount(t *testing.T) {
	tbl := NewGPUReservationTable(map[string]int{"A100": 4})
	if _, err := tbl.Reserve("A100", 0); !errors.Is(err, ErrInvalidReservation) {
		t.Fatalf("zero-count err = %v, want ErrInvalidReservation", err)
	}
	if _, err := tbl.Reserve("A100", -3); !errors.Is(err, ErrInvalidReservation) {
		t.Fatalf("negative-count err = %v, want ErrInvalidReservation", err)
	}
	if free, _ := tbl.Available("A100"); free != 4 {
		t.Fatalf("A100 free after invalid reserves = %d, want 4 (unchanged)", free)
	}
}

// TestReservation_ReleaseIdempotent proves Release returns units exactly once.
//
// Mutation that this kills: drop the `r.released` guard in Release. Then a double
// Release would subtract the units twice; the clamp-to-zero would mask the worst
// of it, but a partially-used pool would end up with MORE free than capacity. We
// pin that double-release leaves exactly capacity free, not more.
func TestReservation_ReleaseIdempotent(t *testing.T) {
	tbl := NewGPUReservationTable(map[string]int{"A100": 4})
	a, err := tbl.Reserve("A100", 1)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	b, err := tbl.Reserve("A100", 1)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if free, _ := tbl.Available("A100"); free != 2 {
		t.Fatalf("free after 2 reservations = %d, want 2", free)
	}
	a.Release()
	a.Release() // idempotent: must not double-free
	if free, _ := tbl.Available("A100"); free != 3 {
		t.Fatalf("free after double-release of one unit = %d, want 3", free)
	}
	b.Release()
	if free, _ := tbl.Available("A100"); free != 4 {
		t.Fatalf("free after releasing all = %d, want 4", free)
	}
}

// TestReservation_ConcurrentNoOverbook proves the table is race-free AND never
// over-books under concurrency: 50 goroutines each try to reserve 1 unit of a
// 10-unit class; exactly 10 succeed and used never exceeds capacity. Run under
// -race this also catches unsynchronised map access.
//
// Mutation that this kills: remove the mutex (or the capacity check). Under -race
// the data race is reported; functionally, more than 10 reservations would
// succeed and the "exactly 10 granted" assertion FAILS.
func TestReservation_ConcurrentNoOverbook(t *testing.T) {
	const total = 10
	const workers = 50
	tbl := NewGPUReservationTable(map[string]int{"A100": total})

	var wg sync.WaitGroup
	var mu sync.Mutex
	granted := 0
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			if _, err := tbl.Reserve("A100", 1); err == nil {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if granted != total {
		t.Fatalf("granted = %d, want exactly %d", granted, total)
	}
	if free, _ := tbl.Available("A100"); free != 0 {
		t.Fatalf("free after full contention = %d, want 0", free)
	}
}
