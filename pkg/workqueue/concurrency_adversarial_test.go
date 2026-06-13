package workqueue

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// advRunUUID is a fresh forensic anchor for this adversarial suite, distinct
// from package_test.go's runUUID so its log lines are unambiguously attributable.
const advRunUUID = "wq-adv-3d9b71e4-2a08-4c6e-bf15-7c0e4a91d2f0"

func advAnchor() time.Time { return time.Unix(1_700_000_000, 0).UTC() }

// drainWorker is a worker loop: Get -> (callback) -> Done, until shutdown.
// It is shared by the adversarial tests. The callback runs while the item is
// "processing" (between Get and Done) and is exactly the window in which the
// safety property (no concurrent double-processing) must hold.
func drainWorker(q *Queue, onProcess func(item interface{})) {
	for {
		item, shutdown := q.Get()
		if shutdown {
			return
		}
		if onProcess != nil {
			onProcess(item)
		}
		q.Done(item)
	}
}

// TestAdversarialNoConcurrentDoubleProcessing is the headline -race safety test.
//
// Many producer goroutines hammer Add on the SAME single item while a pool of
// worker goroutines concurrently Get/Done it. The k8s three-set contract
// guarantees: between a worker's Get and its matching Done, the item is in the
// "processing" set and MUST NOT be handed to any other worker. We prove this
// with a per-item in-flight gauge: on entry to the processing callback we
// increment it and assert it is exactly 1; on exit we decrement. If two workers
// ever held the same item simultaneously (a broken dirty/processing split, a
// lost delete(q.dirty,...) on Get, or a duplicate FIFO append), the gauge would
// read >= 2 and the test fails. -race independently catches any unsynchronized
// access to the three sets.
//
// ANTI-BLUFF: a queue that always returned the item (ignoring processing) or
// that appended a fresh FIFO copy on every Add would let two workers run the
// callback at once -> inFlight==2 -> FAIL. A queue that simply never delivered
// the item would never increment processedTotal and the post-loop
// "processedTotal>0" / progress assertions would FAIL. So both
// double-processing AND silent-loss are bitten.
func TestAdversarialNoConcurrentDoubleProcessing(t *testing.T) {
	clk := NewManualClock(advAnchor())
	q := New(clk, DefaultRateLimit())

	const item = "hot-singleton-key"
	const producers = 32
	const addsEach = 5000
	const workers = 16

	var inFlight int32       // current concurrent holders of `item`; must stay <= 1
	var maxInFlight int32    // high-water mark observed
	var processedTotal int64 // total times the item was processed
	var violations int32     // count of observed inFlight > 1

	worker := func() {
		drainWorker(q, func(got interface{}) {
			if got != item {
				atomic.AddInt32(&violations, 1)
				t.Errorf("worker got unexpected item %v", got)
				return
			}
			cur := atomic.AddInt32(&inFlight, 1)
			if cur > 1 {
				atomic.AddInt32(&violations, 1)
			}
			// Track high-water mark without a lock.
			for {
				old := atomic.LoadInt32(&maxInFlight)
				if cur <= old || atomic.CompareAndSwapInt32(&maxInFlight, old, cur) {
					break
				}
			}
			// Hold the item briefly to widen the race window in which a buggy
			// queue could hand the same item to a second worker.
			time.Sleep(time.Microsecond)
			atomic.AddInt64(&processedTotal, 1)
			atomic.AddInt32(&inFlight, -1)
		})
	}

	var workerWG sync.WaitGroup
	workerWG.Add(workers)
	for w := 0; w < workers; w++ {
		go func() { defer workerWG.Done(); worker() }()
	}

	var prodWG sync.WaitGroup
	prodWG.Add(producers)
	for p := 0; p < producers; p++ {
		go func() {
			defer prodWG.Done()
			for i := 0; i < addsEach; i++ {
				q.Add(item)
			}
		}()
	}
	prodWG.Wait()

	// Producers are done. Drain any final re-deliveries, then shut down so the
	// workers exit. We poll Len()==0 a few times (with no new Adds, the queue
	// must reach 0 and stay there) before shutting down.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if q.Len() == 0 {
			// Confirm it stays drained briefly (no in-flight processing that
			// would re-enqueue, since no Adds happen now).
			time.Sleep(2 * time.Millisecond)
			if q.Len() == 0 && atomic.LoadInt32(&inFlight) == 0 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue did not drain: Len()=%d inFlight=%d", q.Len(), atomic.LoadInt32(&inFlight))
		}
		time.Sleep(time.Millisecond)
	}

	q.ShutDown()
	workerWG.Wait()

	t.Logf("run=%s adv=double-proc producers=%d addsEach=%d workers=%d processedTotal=%d maxInFlight=%d violations=%d",
		advRunUUID, producers, addsEach, workers, atomic.LoadInt64(&processedTotal),
		atomic.LoadInt32(&maxInFlight), atomic.LoadInt32(&violations))

	if v := atomic.LoadInt32(&violations); v != 0 {
		t.Fatalf("SAFETY VIOLATION: item processed by >1 worker simultaneously %d time(s)", v)
	}
	if mx := atomic.LoadInt32(&maxInFlight); mx != 1 {
		t.Fatalf("max concurrent in-flight for single item = %d, want exactly 1", mx)
	}
	if pt := atomic.LoadInt64(&processedTotal); pt < 1 {
		t.Fatalf("item was never processed (silent loss): processedTotal=%d", pt)
	}
	// Dedup sanity: with coalescing, the item is delivered far fewer times than
	// it was Added. This is not a hard contract bound (timing-dependent) but a
	// gross violation (>= total Adds) would mean no dedup happened at all.
	if pt := atomic.LoadInt64(&processedTotal); pt >= int64(producers)*int64(addsEach) {
		t.Fatalf("no dedup observed: processedTotal=%d >= totalAdds=%d", pt, int64(producers)*int64(addsEach))
	}
}

// TestAdversarialConservationManyItems proves the conservation property under
// true concurrency: N distinct items are each Added exactly once by concurrent
// producers, N concurrent workers drain, and every item is processed EXACTLY
// once — none lost, none phantom, none double-processed. Because each item is
// Added once and never re-Added during processing, Done never re-enqueues, so
// the expected processed-count per item is exactly 1.
//
// ANTI-BLUFF: a per-item processed counter must be exactly 1 for every item.
// A lost item -> count 0 -> FAIL (missing-item assertion). A phantom/duplicate
// delivery (e.g. an item handed out twice) -> count 2 -> FAIL (the >1 / total
// assertions). -race catches set corruption from concurrent Get/Done/Add.
func TestAdversarialConservationManyItems(t *testing.T) {
	clk := NewManualClock(advAnchor())
	q := New(clk, DefaultRateLimit())

	const numItems = 20000
	const producers = 24
	const workers = 24

	var mu sync.Mutex
	counts := make(map[int]int, numItems)
	var processedTotal int64
	var doubleProcessed int32

	worker := func() {
		drainWorker(q, func(got interface{}) {
			n, ok := got.(int)
			if !ok {
				t.Errorf("non-int item %v", got)
				return
			}
			mu.Lock()
			counts[n]++
			c := counts[n]
			mu.Unlock()
			if c > 1 {
				atomic.AddInt32(&doubleProcessed, 1)
			}
			atomic.AddInt64(&processedTotal, 1)
		})
	}

	var workerWG sync.WaitGroup
	workerWG.Add(workers)
	for w := 0; w < workers; w++ {
		go func() { defer workerWG.Done(); worker() }()
	}

	// Concurrent producers partition the item space so each distinct item is
	// Added exactly once across all producers (no intentional re-Add).
	var prodWG sync.WaitGroup
	prodWG.Add(producers)
	for p := 0; p < producers; p++ {
		go func(start int) {
			defer prodWG.Done()
			for n := start; n < numItems; n += producers {
				q.Add(n)
			}
		}(p)
	}
	prodWG.Wait()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if q.Len() == 0 {
			time.Sleep(2 * time.Millisecond)
			if q.Len() == 0 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("conservation drain timeout: Len()=%d processed=%d", q.Len(), atomic.LoadInt64(&processedTotal))
		}
		time.Sleep(time.Millisecond)
	}

	q.ShutDown()
	workerWG.Wait()

	mu.Lock()
	distinct := len(counts)
	var missing, dupes int
	for n := 0; n < numItems; n++ {
		c := counts[n]
		switch {
		case c == 0:
			missing++
		case c > 1:
			dupes++
		}
	}
	mu.Unlock()

	t.Logf("run=%s adv=conservation numItems=%d producers=%d workers=%d distinctProcessed=%d processedTotal=%d missing=%d dupes=%d doubleProcessed=%d",
		advRunUUID, numItems, producers, workers, distinct, atomic.LoadInt64(&processedTotal),
		missing, dupes, atomic.LoadInt32(&doubleProcessed))

	if missing != 0 {
		t.Fatalf("CONSERVATION VIOLATION: %d item(s) lost (never processed)", missing)
	}
	if dupes != 0 || atomic.LoadInt32(&doubleProcessed) != 0 {
		t.Fatalf("CONSERVATION VIOLATION: %d item(s) processed more than once", dupes)
	}
	if distinct != numItems {
		t.Fatalf("distinct processed=%d, want %d", distinct, numItems)
	}
	if pt := atomic.LoadInt64(&processedTotal); pt != int64(numItems) {
		t.Fatalf("processedTotal=%d, want exactly %d (one per item)", pt, numItems)
	}
}

// TestAdversarialAddDuringProcessingRequeuesExactlyOnce stresses the precise
// k8s re-add semantics concurrently: a worker holds item X; while it is held,
// a burst of concurrent Add(X) happens; on Done, X must be re-delivered EXACTLY
// once regardless of how many Adds landed during the processing window. We loop
// this many times and count deliveries.
//
// ANTI-BLUFF: per round, the first Get is the initial delivery; the concurrent
// re-Adds during processing must collapse to exactly ONE re-delivery after Done
// (not zero -> lost re-add, not many -> duplicate). We assert the second Get
// succeeds (re-add not lost) and that after the second Done with no pending
// adds the queue is empty (re-add not duplicated). A broken dirty-on-Done path
// would either hang the second Get (lost) or leave Len>0 (duplicated).
func TestAdversarialAddDuringProcessingRequeuesExactlyOnce(t *testing.T) {
	clk := NewManualClock(advAnchor())
	q := New(clk, DefaultRateLimit())

	const rounds = 300
	const reAddBurst = 64

	for r := 0; r < rounds; r++ {
		item := fmt.Sprintf("round-%d-key", r)

		// Seed and check out the item.
		q.Add(item)
		got, shut := q.Get()
		if shut || got != item {
			t.Fatalf("round %d: initial Get got=%v shutdown=%v want %v", r, got, shut, item)
		}

		// While the item is "processing", fire many concurrent re-Adds.
		var wg sync.WaitGroup
		wg.Add(reAddBurst)
		for i := 0; i < reAddBurst; i++ {
			go func() { defer wg.Done(); q.Add(item) }()
		}
		wg.Wait()

		// None of those re-Adds may have leaked into the FIFO: the item is still
		// processing, so it must stay dirty-but-not-queued.
		if l := q.Len(); l != 0 {
			t.Fatalf("round %d: re-Add during processing leaked: Len()=%d want 0", r, l)
		}

		// Done re-enqueues exactly once.
		q.Done(item)
		if l := q.Len(); l != 1 {
			t.Fatalf("round %d: after Done Len()=%d want exactly 1 (re-add lost or duplicated)", r, l)
		}

		// Consume the single re-delivery; with no further Adds the queue empties.
		got2, shut2 := q.Get()
		if shut2 || got2 != item {
			t.Fatalf("round %d: re-delivery Get got=%v shutdown=%v want %v", r, got2, shut2, item)
		}
		q.Done(got2)
		if l := q.Len(); l != 0 {
			t.Fatalf("round %d: after final Done Len()=%d want 0 (re-add duplicated)", r, l)
		}
	}

	t.Logf("run=%s adv=requeue-once rounds=%d reAddBurst=%d -> every round had exactly one re-delivery",
		advRunUUID, rounds, reAddBurst)
}

// TestAdversarialShutdownWithInFlightDone proves that ShutDown unblocks all
// blocked Get callers AND that a Done for an item still checked out at shutdown
// is honored without panic/deadlock. We start workers, hand out some items,
// shut down mid-flight, and confirm the in-flight workers can still call Done
// and then observe shutdown.
//
// ANTI-BLUFF: this test is designed so that at ShutDown time at least one
// worker is genuinely parked in cond.Wait (the queue is drained to empty
// before ShutDown) AND at least one worker is in-flight holding an item
// (blocked on a gate between Get and Done). If ShutDown failed to Broadcast,
// the parked worker(s) would hang -> WaitGroup never completes -> timeout FAIL.
// If Done after ShutDown panicked or deadlocked (touching a torn-down
// structure), the in-flight worker's deferred Done would crash -> FAIL.
func TestAdversarialShutdownWithInFlightDone(t *testing.T) {
	clk := NewManualClock(advAnchor())
	q := New(clk, DefaultRateLimit())

	const workers = 12
	const gatedHolders = 3 // workers that grab an item and block in-flight

	var processed int64
	gate := make(chan struct{}) // released after ShutDown; in-flight Done must still work
	var holders int32           // how many workers are currently gated in-flight
	heldEnough := make(chan struct{}, 1)

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			drainWorker(q, func(item interface{}) {
				// The first `gatedHolders` items handed out park in-flight on the
				// gate; everything else processes immediately. This guarantees a
				// mix of in-flight (gated) and, once the queue drains, parked
				// (cond.Wait) workers at ShutDown time.
				if atomic.AddInt32(&holders, 1) <= gatedHolders {
					select {
					case heldEnough <- struct{}{}:
					default:
					}
					<-gate
				} else {
					atomic.AddInt32(&holders, -1)
				}
				atomic.AddInt64(&processed, 1)
			})
		}()
	}

	// Seed exactly gatedHolders items so they are all checked out and held
	// in-flight; the remaining workers find the queue empty and park in cond.Wait.
	for i := 0; i < gatedHolders; i++ {
		q.Add(fmt.Sprintf("sd-held-%d", i))
	}
	// Wait until at least one holder is parked in-flight on the gate.
	select {
	case <-heldEnough:
	case <-time.After(2 * time.Second):
		t.Fatalf("no worker reached in-flight gate; holders=%d", atomic.LoadInt32(&holders))
	}
	// Give the surplus workers a beat to reach cond.Wait on the now-empty queue.
	time.Sleep(20 * time.Millisecond)
	if l := q.Len(); l != 0 {
		t.Fatalf("precondition: queue not drained before ShutDown, Len()=%d", l)
	}

	// Shut down: parked (cond.Wait) workers MUST be Broadcast-woken to exit.
	q.ShutDown()
	close(gate) // release the in-flight holders; their Done must still work

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("ShutDown did not unblock workers (deadlock); processed=%d", atomic.LoadInt64(&processed))
	}

	// Post-shutdown Get is immediate shutdown, and an extra Done does not panic.
	if _, shut := q.Get(); !shut {
		t.Fatalf("Get after ShutDown returned shutdown=false")
	}
	q.Done("never-issued-after-shutdown") // must be a harmless no-op, no panic

	t.Logf("run=%s adv=shutdown-inflight workers=%d gatedHolders=%d processedBeforeStop=%d (parked workers woken, in-flight Done honored)",
		advRunUUID, workers, gatedHolders, atomic.LoadInt64(&processed))
}
