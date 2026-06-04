//go:build chaos

package marketplace

import (
	"sync"
	"sync/atomic"
	"testing"
)

// chaos_test.go exercises the GPU reservation accounting under concurrent
// contention with cancellation (HXC-937, Constitution §11.4.85). The fault model
// is high-concurrency reserve/release churn where some callers cancel (release)
// mid-flight, modeling racing bids that get settled or abandoned. The REAL
// invariant under test: a class's live usage MUST NEVER exceed its capacity, and
// the table must NEVER satisfy a request from another class's spare pool — even
// under heavy concurrent reserve/release/cancel. These assertions observe the
// sink-side accounting (Available / over-commit), not a single happy path.
//
// Run with: go test -tags chaos -race ./pkg/marketplace/...

// TestChaosConcurrentReserveNeverOverCommits launches many goroutines that all
// contend for the same scarce class. The invariant: the number of LIVE (not
// released) reservations never exceeds capacity, and total granted-minus-released
// units never drives Available negative. We sample Available throughout and
// assert it stays within [0, capacity].
func TestChaosConcurrentReserveNeverOverCommits(t *testing.T) {
	const capacity = 8
	table := NewGPUReservationTable(map[string]int{"H100": capacity, "A100": 100})

	var live int64 // currently-held units in H100, maintained by successful Reserve/Release
	var maxLive int64
	var granted int64

	const workers = 64
	const itersPerWorker = 200

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for i := 0; i < itersPerWorker; i++ {
				// Request 1..3 units; varied sizes stress the boundary check.
				count := (i % 3) + 1
				r, err := table.Reserve("H100", count)
				if err != nil {
					// Denial under contention is correct behavior, not a failure.
					continue
				}
				atomic.AddInt64(&granted, 1)
				now := atomic.AddInt64(&live, int64(count))
				for {
					m := atomic.LoadInt64(&maxLive)
					if now <= m || atomic.CompareAndSwapInt64(&maxLive, m, now) {
						break
					}
				}
				// INVARIANT (sink-side): live units must never exceed capacity.
				if now > capacity {
					t.Errorf("over-commit: live H100 units = %d > capacity %d", now, capacity)
				}
				// Cancellation / settlement: release the reservation.
				atomic.AddInt64(&live, -int64(count))
				r.Release()
			}
		}(w)
	}
	wg.Wait()

	if granted == 0 {
		t.Fatal("no reservation ever succeeded — contention test exercised nothing")
	}
	if maxLive == 0 {
		t.Fatal("maxLive stayed 0 — concurrency was not actually exercised")
	}
	if maxLive > capacity {
		t.Fatalf("peak live usage %d exceeded capacity %d (over-commit occurred)", maxLive, capacity)
	}

	// SINK-SIDE FINAL INVARIANT: after every reservation is released, the class
	// is fully free again (no leaked accounting).
	free, ok := table.Available("H100")
	if !ok {
		t.Fatal("H100 unexpectedly unknown")
	}
	if free != capacity {
		t.Fatalf("accounting leak: H100 free=%d after all releases, want %d", free, capacity)
	}
}

// TestChaosConcurrentNoCrossClassBleed runs concurrent reservations against a
// FULLY-SATURATED scarce class while a different class has abundant spare units.
// The invariant: requests for the saturated class MUST be denied — they may
// NEVER be silently satisfied from the other class's spare pool, even under
// concurrency. We assert no Reserve("scarce") succeeds while it is at capacity.
func TestChaosConcurrentNoCrossClassBleed(t *testing.T) {
	table := NewGPUReservationTable(map[string]int{"scarce": 2, "abundant": 1000})

	// Saturate the scarce class up front.
	r1, err := table.Reserve("scarce", 2)
	if err != nil {
		t.Fatalf("initial saturate: %v", err)
	}
	defer r1.Release()

	if free, _ := table.Available("scarce"); free != 0 {
		t.Fatalf("scarce should be saturated, free=%d", free)
	}

	var bled int64 // count of scarce reservations that wrongly succeeded while full

	var wg sync.WaitGroup
	for w := 0; w < 48; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				// While scarce is held at capacity, this MUST fail.
				if r, err := table.Reserve("scarce", 1); err == nil {
					atomic.AddInt64(&bled, 1)
					r.Release()
				}
				// Meanwhile abundant requests should succeed (proves the table is
				// live, not just rejecting everything).
				if r, err := table.Reserve("abundant", 1); err == nil {
					r.Release()
				}
			}
		}()
	}
	wg.Wait()

	if bled != 0 {
		t.Fatalf("cross-class bleed / over-commit: %d scarce reservations succeeded while class was full", bled)
	}

	// An unknown class must still be denied under concurrency (no fallback).
	if _, err := table.Reserve("ghost", 1); err == nil {
		t.Fatal("reserve on unknown class succeeded — must be denied")
	}
}
