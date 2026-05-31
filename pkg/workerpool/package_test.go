package workerpool

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPool(t *testing.T) {
	p := New(2)
	var counter int32
	done := make(chan struct{})
	p.Submit(func() {
		atomic.AddInt32(&counter, 1)
		close(done)
	})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for worker")
	}
	p.Stop()
	if atomic.LoadInt32(&counter) != 1 {
		t.Errorf("expected counter 1, got %d", counter)
	}
}

// --- Mutation tests ---

func TestPool_MultipleWorkers_Mutation(t *testing.T) {
	// Mutation: New creates only 1 worker regardless of n → tasks serialize instead of parallelize
	p := New(3)
	var active int32
	var maxActive int32
	var mu sync.Mutex
	barrier := make(chan struct{})

	for i := 0; i < 3; i++ {
		p.Submit(func() {
			n := atomic.AddInt32(&active, 1)
			mu.Lock()
			if n > maxActive {
				maxActive = n
			}
			mu.Unlock()
			<-barrier
			atomic.AddInt32(&active, -1)
		})
	}

	time.Sleep(100 * time.Millisecond) // let workers pick up tasks
	mu.Lock()
	if maxActive < 3 {
		t.Errorf("expected 3 concurrent workers, got %d", maxActive)
	}
	mu.Unlock()
	close(barrier)
	p.Stop()
}

func TestPool_StopWaitsForWorkers_Mutation(t *testing.T) {
	// Mutation: Stop does not wait → work may not complete
	p := New(1)
	var completed int32
	start := time.Now()
	p.Submit(func() {
		time.Sleep(50 * time.Millisecond)
		atomic.StoreInt32(&completed, 1)
	})
	p.Stop()
	if atomic.LoadInt32(&completed) != 1 {
		t.Error("Stop should wait for submitted work to finish")
	}
	// Prove the wait was real, not coincidental scheduling: Stop() must have
	// blocked at least as long as the in-flight task's sleep.
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Errorf("Stop returned after %v; expected >= 50ms (it must wait for in-flight work)", elapsed)
	}
}

// TestPool_NilWorkSafe_Mutation proves the worker SURVIVES a nil task and goes
// on to process the NEXT task. Sink-side proof: a nil submission followed by a
// real signalling task whose effect we observe. If the nil guard were removed,
// the worker goroutine would panic and the process would crash (the subsequent
// task would never run); this is a deterministic, asserted check rather than a
// "no panic" hope.
func TestPool_NilWorkSafe_Mutation(t *testing.T) {
	p := New(1)
	defer p.Stop()

	// Submit nil work first — must be skipped, not panic the worker.
	p.Submit(nil)

	// Then submit a real task on the SAME single worker. If the worker died
	// processing nil, this signal never fires and we time out.
	survived := make(chan struct{})
	p.Submit(func() {
		close(survived)
	})

	select {
	case <-survived:
		// Worker survived the nil task and processed the next one. PASS.
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not process the task after a nil submission; nil work likely killed the worker")
	}
}

// TestPool_FanIn_AllTasksRun proves the core pool guarantee: every submitted
// task is eventually executed exactly once and none are dropped. Run under
// -race to also prove there are no data races in the dispatch path.
func TestPool_FanIn_AllTasksRun(t *testing.T) {
	const n = 1000
	p := New(8)

	var counter int64
	var done sync.WaitGroup
	done.Add(n)

	// Submit concurrently from multiple producer goroutines to exercise the
	// fan-in / dispatch path under contention.
	const producers = 4
	var prodWG sync.WaitGroup
	prodWG.Add(producers)
	for g := 0; g < producers; g++ {
		go func(start int) {
			defer prodWG.Done()
			for i := start; i < n; i += producers {
				p.Submit(func() {
					atomic.AddInt64(&counter, 1)
					done.Done()
				})
			}
		}(g)
	}
	prodWG.Wait()

	// Wait for all tasks to actually run (sink-side), with a guard timeout.
	finished := make(chan struct{})
	go func() {
		done.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout: only %d of %d tasks ran", atomic.LoadInt64(&counter), n)
	}

	p.Stop()
	if got := atomic.LoadInt64(&counter); got != n {
		t.Errorf("expected all %d tasks to run, got %d", n, got)
	}
}

// TestPool_PanickingTaskDoesNotKillPool proves resilience: a task that panics
// must NOT take down the worker/process. The pool must keep processing
// subsequent tasks. Sink-side proof: a later task's effect is observed.
func TestPool_PanickingTaskDoesNotKillPool(t *testing.T) {
	p := New(1) // single worker: the SAME goroutine must survive the panic
	defer p.Stop()

	panicked := make(chan struct{})
	p.Submit(func() {
		close(panicked)
		panic("boom")
	})
	<-panicked // ensure the panicking task actually ran

	// A subsequent task on the same single worker must still execute.
	recovered := make(chan struct{})
	p.Submit(func() {
		close(recovered)
	})
	select {
	case <-recovered:
		// Worker survived the panic and processed the next task. PASS.
	case <-time.After(2 * time.Second):
		t.Fatal("pool stopped processing after a panicking task; panic killed the worker")
	}
}

// TestPool_SubmitAfterStop proves a documented post-Stop contract: Submit must
// NOT hang the caller and must NOT panic the process. Here the task is dropped
// (a no-op), which we verify by ensuring Submit returns promptly and the task
// never runs.
func TestPool_SubmitAfterStop(t *testing.T) {
	p := New(2)
	p.Stop()

	ran := make(chan struct{})
	returned := make(chan struct{})
	go func() {
		p.Submit(func() { close(ran) }) // must not block forever, must not panic
		close(returned)
	}()

	select {
	case <-returned:
		// Submit returned without hanging. Good.
	case <-time.After(2 * time.Second):
		t.Fatal("Submit hung after Stop(); caller goroutine leaked")
	}

	// The dropped task must not run (pool is stopped).
	select {
	case <-ran:
		t.Error("task submitted after Stop() unexpectedly ran")
	case <-time.After(100 * time.Millisecond):
		// Expected: task was dropped.
	}
}

// TestPool_DoubleStopIdempotent proves Stop() is safe to call more than once
// (no panic from closing an already-closed channel).
func TestPool_DoubleStopIdempotent(t *testing.T) {
	p := New(2)
	p.Stop()
	// Second Stop must be a no-op, not a panic.
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Stop()
		p.Stop()
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("repeated Stop() hung")
	}
}
