package events

// events_adversarial_test.go — adversarial / sink-side probes for the in-memory
// Bus (pkg/events/package.go), hunting the helixnet teardown-race bug class:
// an unguarded subscribers map and a deregister-closes-channel-while-deliver-
// sends-to-it race (close-of-closed / send-on-closed → panic).
//
// FINDING (see final report): NO BUG in Bus for the teardown-race class. Bus is
// handler-func based (no per-subscriber channel, no Unsubscribe), so there is no
// channel to close-of-closed or send-on-closed. The subscribers map is guarded
// by sync.RWMutex on BOTH the write path (Subscribe → Lock) and the read path
// (Publish → RLock), and Subscribe always appends (copy-on-grow), so the slice
// captured under RLock in Publish is never mutated underneath a delivery
// goroutine. These tests PIN that contract: they assert -race-clean concurrency
// and correct sink-side delivery. They PASS on unmodified prod and are safe to
// keep in the default suite (no gating needed — no panic path exists).

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// drainWait waits until want deliveries are observed via counter, or the
// deadline elapses. It avoids brittle fixed sleeps for async (go h(e)) delivery.
func drainWait(counter *int64, want int64, deadline time.Duration) bool {
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if atomic.LoadInt64(counter) >= want {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return atomic.LoadInt64(counter) >= want
}

// TestBus_ConcurrentSubscribePublish_NoRace is probe (a): UNGUARDED SHARED
// MUTABLE STATE. Many goroutines Subscribe and Publish on the SAME event type
// concurrently, forcing simultaneous map-write (Subscribe → Lock) and map-read
// + slice-range (Publish → RLock). Run under -race: a missing/incorrect lock on
// either path surfaces as a DATA RACE on b.handlers. No panic path exists, so
// this is safe by default.
func TestBus_ConcurrentSubscribePublish_NoRace(t *testing.T) {
	b := NewBus()
	const writers = 32
	const publishers = 32
	const iters = 200

	var delivered int64
	var wg sync.WaitGroup

	// Subscribers churning the map under Lock.
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				b.Subscribe("hot", func(e Event) {
					atomic.AddInt64(&delivered, 1)
				})
			}
		}()
	}

	// Publishers reading the map + ranging the slice under RLock, concurrent
	// with the writers above. This is the exact read-by-many/written-by-few
	// contention the helixnet bug lived in.
	for i := 0; i < publishers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				b.Publish(Event{ID: "x", Type: "hot"})
			}
		}()
	}

	wg.Wait()

	// Sink-side: at least one delivery must have occurred (we registered many
	// handlers and published many times). The strong assertion here is the
	// -race verdict; this guards against a no-op Publish regression too.
	if !drainWait(&delivered, 1, 2*time.Second) {
		t.Fatal("expected at least one delivery from concurrent Subscribe/Publish; got none")
	}
}

// TestBus_TeardownRace_NoPanic is probe (b): the helixnet TEARDOWN RACE,
// adapted. Bus has no Unsubscribe / no channel close, so the literal
// close-of-closed / send-on-closed cannot occur. We still drive the nearest
// equivalent — continuous Subscribe (the only mutation of subscriber state)
// fully concurrent with continuous Publish (delivery) on the same type — and
// assert NO panic and that delivery still happens. If a future change adds a
// channel-closing Unsubscribe without holding the lock across send-or-close,
// this harness is where the panic would surface.
func TestBus_TeardownRace_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("teardown race caused panic (helixnet bug class): %v", r)
		}
	}()

	b := NewBus()
	const goroutines = 24
	const iters = 300
	var delivered int64
	var wg sync.WaitGroup

	stop := make(chan struct{})

	// Continuous subscriber churn.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				select {
				case <-stop:
					return
				default:
				}
				b.Subscribe("teardown", func(e Event) {
					atomic.AddInt64(&delivered, 1)
				})
			}
		}()
	}

	// Continuous delivery concurrent with the churn.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				b.Publish(Event{ID: "t", Type: "teardown"})
			}
		}()
	}

	wg.Wait()
	close(stop)

	if !drainWait(&delivered, 1, 2*time.Second) {
		t.Fatal("expected deliveries during concurrent subscribe/publish teardown drive")
	}
}

// TestBus_DeliveryCorrectness_NoLostEvents is probe (c): DELIVERY CORRECTNESS.
// Register M subscribers for one type, publish N events, and assert the SINK
// side observes exactly N*M deliveries — no lost, no duplicated events under
// the goroutine-per-handler fan-out. This is the anti-bluff delivery assertion
// (not "compiles"): it counts actual handler invocations.
func TestBus_DeliveryCorrectness_NoLostEvents(t *testing.T) {
	b := NewBus()
	const m = 8 // subscribers
	const n = 50 // events

	var delivered int64
	for i := 0; i < m; i++ {
		b.Subscribe("count", func(e Event) {
			atomic.AddInt64(&delivered, 1)
		})
	}

	for i := 0; i < n; i++ {
		b.Publish(Event{ID: "e", Type: "count"})
	}

	want := int64(n * m)
	if !drainWait(&delivered, want, 3*time.Second) {
		t.Fatalf("lost-event defect: expected %d deliveries (N=%d * M=%d), got %d",
			want, n, m, atomic.LoadInt64(&delivered))
	}
	// Guard against over-delivery (duplicate fan-out): give late goroutines a
	// beat, then assert the count did not exceed the exact expected total.
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt64(&delivered); got != want {
		t.Fatalf("duplicate/over-delivery defect: expected exactly %d, got %d", want, got)
	}
}

// TestBus_TypeIsolation_NoCrossDelivery is probe (d): IDENTITY / routing. Bus
// promises type-keyed routing (handlers map[string]). Assert an event of type
// A is never delivered to a type-B subscriber, even with both registered and
// published concurrently. This pins the routing contract the existing
// TestBus_WrongTypeNotDelivered_Mutation checks, but under concurrency.
func TestBus_TypeIsolation_NoCrossDelivery(t *testing.T) {
	b := NewBus()
	var aHits, bHits int64

	b.Subscribe("A", func(e Event) { atomic.AddInt64(&aHits, 1) })
	b.Subscribe("B", func(e Event) { atomic.AddInt64(&bHits, 1) })

	const n = 100
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			b.Publish(Event{ID: "a", Type: "A"})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			b.Publish(Event{ID: "b", Type: "B"})
		}
	}()
	wg.Wait()

	if !drainWait(&aHits, n, 3*time.Second) {
		t.Fatalf("A subscriber missed events: got %d want %d", atomic.LoadInt64(&aHits), n)
	}
	if !drainWait(&bHits, n, 3*time.Second) {
		t.Fatalf("B subscriber missed events: got %d want %d", atomic.LoadInt64(&bHits), n)
	}
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt64(&aHits); got != n {
		t.Fatalf("cross-delivery into A: expected exactly %d, got %d", n, got)
	}
	if got := atomic.LoadInt64(&bHits); got != n {
		t.Fatalf("cross-delivery into B: expected exactly %d, got %d", n, got)
	}
}
