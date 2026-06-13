package thermalwarm

import (
	"sync"
	"testing"
	"time"
)

// raceClock is a trivially concurrency-safe Clock for the adversarial tests.
// It is NOT a *ManualClock, so Controller.Tick is a no-op against it — this
// keeps the clock out of the race picture and isolates the contention onto the
// shared c.backends map and the per-backend *backend state, which is exactly
// the state the Controller is responsible for protecting.
type raceClock struct {
	mu sync.Mutex
	t  time.Time
}

func newRaceClock(start time.Time) *raceClock { return &raceClock{t: start} }

func (c *raceClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *raceClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// TestConcurrentDispatchPreWarm_NoRace is the adversarial -race reproducer for
// the concurrent-by-role contract. The Controller owns a shared map
// (c.backends) and mutable *backend state. In a scale-from-zero serverless
// dispatcher, request goroutines call Dispatch/State while a warmer goroutine
// calls PreWarm/Register — all hitting the SAME controller concurrently. The
// package documentation is SILENT on thread-safety (no "not safe for concurrent
// use" / "synchronize externally" disclaimer), so by role it MUST be safe for
// concurrent use.
//
// WRITE paths exercised concurrently:
//   - ensure(): map write  c.backends[id] = b  (via Register/PreWarm/State/Dispatch)
//   - PreWarm():           b.state = Warming; b.warmStartedAt = now
//   - State():             b.state = Hot       (sticky-completion write, line ~134)
//   - Dispatch():          b.state = Hot       (line ~209/215/225)
//
// READ paths exercised concurrently:
//   - State()/Dispatch(): read c.backends[id]; read b.state / b.warmStartedAt
//
// Without internal synchronization this is a data race (concurrent map
// read/write + unsynchronized field read/write) and `go test -race` flags it.
// With the RWMutex fix it passes cleanly. Revert the fix -> the race returns,
// proving the lock is load-bearing.
func TestConcurrentDispatchPreWarm_NoRace(t *testing.T) {
	const coldStart = 5 * time.Millisecond
	clk := newRaceClock(anchor)
	c := NewController(clk, coldStart)

	const (
		backends   = 8
		goroutines = 64
		iters      = 200
	)
	ids := make([]string, backends)
	for i := range ids {
		ids[i] = runUUID(t)
		c.Register(ids[i])
	}

	var wg sync.WaitGroup
	start := make(chan struct{})

	// Writer/warmer goroutines: PreWarm + Register (map + state writes).
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			<-start
			for i := 0; i < iters; i++ {
				id := ids[(g+i)%backends]
				c.PreWarm(id)
				// Register a fresh id occasionally to force concurrent map writes
				// (new key insertion) racing with reads.
				if i%37 == 0 {
					c.Register(id + "-new")
				}
			}
		}(g)
	}

	// Reader/dispatcher goroutines: Dispatch + State (map reads + state R/W).
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			<-start
			for i := 0; i < iters; i++ {
				id := ids[(g*7+i)%backends]
				_ = c.State(id)
				_ = c.Dispatch(id)
			}
		}(g)
	}

	close(start)
	wg.Wait()

	// Sink-side sanity: every registered backend ended Hot (every backend was
	// dispatched at least once across the storm, and Dispatch always lands Hot).
	for _, id := range ids {
		if s := c.State(id); s != Hot {
			t.Fatalf("after concurrent storm: backend %s state=%s want Hot", id, s)
		}
	}
}

// TestConcurrentState_StickyWriteRace narrows the reproducer to the subtle
// READ-path write: State() itself mutates b.state = Hot when a warm-up has
// completed (the "sticky" persist on line ~134). Many goroutines calling the
// read-looking State() concurrently therefore WRITE the same backend, which is
// a data race on its own even with no Dispatch/PreWarm in flight.
//
// This guards against a too-narrow fix that locks only Dispatch/PreWarm but
// leaves State() taking a read-lock while it performs a write.
func TestConcurrentState_StickyWriteRace(t *testing.T) {
	const coldStart = 1 * time.Millisecond
	clk := newRaceClock(anchor)
	c := NewController(clk, coldStart)

	id := runUUID(t)
	c.Register(id)
	// Put it into Warming, then move the clock PAST the warm-up boundary so the
	// very first State() call from each goroutine wants to flip it to Hot (the
	// sticky b.state = Hot write on the read-looking path). Many goroutines
	// racing that write is itself a data race absent the lock.
	c.PreWarm(id)
	clk.advance(coldStart * 2)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for g := 0; g < 64; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 500; i++ {
				_ = c.State(id)
			}
		}()
	}
	close(start)
	wg.Wait()

	if s := c.State(id); s != Hot {
		t.Fatalf("warm-up elapsed: state=%s want Hot", s)
	}
}
