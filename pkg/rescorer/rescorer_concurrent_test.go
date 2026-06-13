package rescorer

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// runUUIDConc anchors this concurrent test binary's observable log lines so a
// given run is traceable in CI output.
func runUUIDConc(t *testing.T) string {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b[:])
}

// TestConcurrentRescoreRole_RaceUnderLoad exercises the EXACT routing-scorer
// (load-balancer) concurrency profile the package's own doc describes: provider
// prices/scores are UPDATED from many request goroutines via SetPrice (which
// writes r.providers[id]=... — a map write — and, on the boundary, Tick ->
// rerank iterates the r.providers map and rewrites r.ranking) while OTHER
// request goroutines READ the published ranking to pick a provider via
// Ranking()/Top() (reads of r.ranking).
//
// The package documents NO single-threaded / "synchronize externally" contract
// (contrast pkg/ewmarank, whose doc explicitly says "not safe for concurrent
// use; callers that need concurrency must synchronize externally"). rescorer's
// doc instead describes the balancer role ("lets traffic stay pinned to a
// chosen provider", "moving traffic off it") and only claims determinism / no
// host access — i.e. it implies safety for that concurrent role. A missing lock
// is therefore a real bug, not an honest disclaimer.
//
// ANTI-BLUFF — why -race bites here: the writers call SetPrice with FRESH ids,
// which executes `r.providers[id] = p` (a map write) and `r.order = append(...)`
// concurrently with Tick/rerank ranging over r.providers (a map read) and with
// Ranking()/Top() reading r.ranking while rerank writes it. Without a mutex the
// Go race detector reports a data race (and the runtime can fatal with
// "concurrent map read and map write"). With the RWMutex fix this test is clean
// under -race. If a future change drops the lock, this test fails again — so it
// is load-bearing on the synchronization, not a no-op.
func TestConcurrentRescoreRole_RaceUnderLoad(t *testing.T) {
	id := runUUIDConc(t)
	t.Logf("run=%s phase=before launching concurrent rescore workload", id)

	r := New(4 /* short cycle so rerank fires often */, 0, 2.0)
	// Seed a couple of stable providers so Ranking()/Top() return non-empty
	// data that readers can pick from immediately.
	r.SetPrice("seedA", 10)
	r.SetPrice("seedB", 20)
	r.Tick(0)

	const (
		writers = 8
		readers = 8
		iters   = 400
	)

	var wg sync.WaitGroup
	var tick int64 // monotonically advanced logical clock shared by tickers

	// Writers: update existing and ADD NEW providers (forcing map writes) and
	// advance the clock so rerank ranges over the providers map concurrently.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				// New unique id per (w,i): guaranteed map insert -> map write.
				pid := fmt.Sprintf("p-%d-%d", w, i)
				r.SetPrice(pid, float64((i%50)+1))
				// Also mutate a shared hot provider to drive the spike guard.
				r.SetPrice("seedA", float64((i%30)+1))
				// Drive the boundary/rerank path (map read + ranking write).
				r.Tick(atomic.AddInt64(&tick, 1))
			}
		}(w)
	}

	// Readers: select a provider from the published ranking, as a router would.
	for rdr := 0; rdr < readers; rdr++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				_ = r.Top()
				rk := r.Ranking()
				if len(rk) > 0 {
					_ = rk[0]
				}
			}
		}()
	}

	wg.Wait()
	final := r.Ranking()
	t.Logf("run=%s phase=after race-free concurrent workload, len(ranking)=%d top=%s",
		id, len(final), r.Top())
}
