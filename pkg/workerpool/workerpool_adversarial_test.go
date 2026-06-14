package workerpool

// Adversarial sink-side probes for the bounded worker pool.
//
// Contract under test (from package.go doc comments):
//   - New(n) starts n workers; at most n tasks run concurrently.
//   - Every accepted Submit eventually runs its fn exactly once.
//   - Submit after Stop drops the task: no block, no panic (documented).
//   - runTask recovers panics: a panicking task must not kill a worker.
//   - Stop is idempotent and waits for in-flight workers.
//
// These tests assert SINK-SIDE invariants (peak concurrency <= n, exactly
// M tasks ran, no panic under close/submit race), never "no panic" alone.

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// (a) CONCURRENCY BOUND — peak live workers must NEVER exceed n.
//
// Instrument a live counter INSIDE the task. Each task increments on entry,
// records the running peak, holds briefly (so overlap is real and observable),
// then decrements. With many more tasks than workers, if New created more than
// n workers (or dispatch let extra tasks run), peak would exceed n and we catch
// it. Run with -race.
func TestAdversarial_ConcurrencyNeverExceedsN(t *testing.T) {
	const n = 4
	const tasks = 400

	p := New(n)
	defer p.Stop()

	var live int32
	var peak int32

	var ran sync.WaitGroup
	ran.Add(tasks)

	for i := 0; i < tasks; i++ {
		p.Submit(func() {
			cur := atomic.AddInt32(&live, 1)
			// Track the maximum observed concurrency (CAS loop, race-safe).
			for {
				old := atomic.LoadInt32(&peak)
				if cur <= old || atomic.CompareAndSwapInt32(&peak, old, cur) {
					break
				}
			}
			// Hold so overlapping tasks genuinely coexist; this widens the
			// window in which a bound violation would manifest.
			time.Sleep(200 * time.Microsecond)
			atomic.AddInt32(&live, -1)
			ran.Done()
		})
	}

	finished := make(chan struct{})
	go func() { ran.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatalf("timeout: peak=%d live=%d", atomic.LoadInt32(&peak), atomic.LoadInt32(&live))
	}

	if got := atomic.LoadInt32(&peak); got > int32(n) {
		t.Fatalf("CONCURRENCY BOUND VIOLATED: peak concurrent workers = %d, bound n = %d", got, n)
	}
	if got := atomic.LoadInt32(&peak); got < 2 {
		// Sanity: with 400 tasks and 4 workers we must have seen real overlap,
		// otherwise the test is not actually exercising concurrency.
		t.Fatalf("expected real overlap (peak >= 2) with n=%d; got peak=%d — test not exercising concurrency", n, got)
	}
}

// (b) TASK CONSERVATION — exactly M tasks run: none lost, none double-run.
//
// Each task bumps a global counter AND a per-index counter. We assert the
// global == M and every per-index slot == 1 (no double execution, no drops).
func TestAdversarial_ExactlyMTasksRunOnceEach(t *testing.T) {
	const m = 5000
	p := New(8)
	defer p.Stop()

	var total int64
	perIdx := make([]int32, m)

	var ran sync.WaitGroup
	ran.Add(m)

	// Concurrent producers to stress the dispatch path.
	const producers = 6
	var prod sync.WaitGroup
	prod.Add(producers)
	for g := 0; g < producers; g++ {
		go func(start int) {
			defer prod.Done()
			for i := start; i < m; i += producers {
				idx := i
				p.Submit(func() {
					atomic.AddInt64(&total, 1)
					atomic.AddInt32(&perIdx[idx], 1)
					ran.Done()
				})
			}
		}(g)
	}
	prod.Wait()

	finished := make(chan struct{})
	go func() { ran.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(15 * time.Second):
		t.Fatalf("timeout: only %d of %d tasks ran (task loss)", atomic.LoadInt64(&total), m)
	}

	if got := atomic.LoadInt64(&total); got != m {
		t.Fatalf("TASK CONSERVATION VIOLATED: expected exactly %d runs, got %d", m, got)
	}
	for i := 0; i < m; i++ {
		if c := atomic.LoadInt32(&perIdx[i]); c != 1 {
			t.Fatalf("task %d ran %d times (want exactly 1) — lost or double-run", i, c)
		}
	}
}

// (c) CLOSE/SUBMIT RACE — concurrent Submit while Stop closes the pool.
//
// Drive Submit and Stop concurrently from many goroutines. Sink-side asserts:
//   - NO panic (no "send on closed channel"; the helixnet teardown class).
//   - NO accepted-then-lost task: every task that we observe Submit *return for*
//     while the pool was still alive and that the pool *accepted* must run. The
//     pool documents that a Submit racing with Stop MAY drop the task, so we do
//     not demand all submits run — but we DO demand that any task which actually
//     started running completes (no half-run corruption) and the process never
//     panics. We additionally assert that before Stop, accepted tasks ran.
//
// The strongest portable assertion here is: no panic under the race, and the
// number of tasks that ran is consistent (<= submitted, and every started task
// finished). A panic or a negative/over-count would fail.
func TestAdversarial_CloseSubmitRace_NoPanicNoCorruption(t *testing.T) {
	const rounds = 200
	for r := 0; r < rounds; r++ {
		p := New(4)

		var started int64
		var finished int64

		var submitters sync.WaitGroup
		const subN = 8
		submitters.Add(subN)
		for s := 0; s < subN; s++ {
			go func() {
				defer submitters.Done()
				for i := 0; i < 50; i++ {
					// A panic inside Submit (send on closed channel) would
					// propagate and crash the test binary — that is the bug
					// class we are hunting. recover() here lets us convert it
					// into a test failure instead of a process abort.
					func() {
						defer func() {
							if rec := recover(); rec != nil {
								t.Errorf("PANIC in Submit during close/submit race: %v", rec)
							}
						}()
						p.Submit(func() {
							atomic.AddInt64(&started, 1)
							atomic.AddInt64(&finished, 1)
						})
					}()
				}
			}()
		}

		// Race Stop against the in-flight submitters.
		go func() {
			// Tiny stagger so some submits land before and some after close.
			runtime.Gosched()
			p.Stop()
		}()

		submitters.Wait()
		p.Stop() // ensure fully drained/idempotent

		st := atomic.LoadInt64(&started)
		fi := atomic.LoadInt64(&finished)
		if st != fi {
			t.Fatalf("round %d: %d tasks started but %d finished — a task was interrupted mid-flight (corruption)", r, st, fi)
		}
		if st > subN*50 {
			t.Fatalf("round %d: %d tasks ran but only %d submitted — double execution", r, st, subN*50)
		}
	}
}

// (c') CLOSE/SUBMIT RACE — accepted-before-Stop tasks are NOT lost.
//
// Stronger conservation variant: we Submit a batch, then Stop. Because Submit
// on an unbuffered channel only returns once a worker has RECEIVED the task
// (rendezvous), every Submit that returned BEFORE we called Stop must have been
// handed to a worker, and Stop waits for in-flight workers (wg.Wait). So all
// pre-Stop accepted tasks must run. This pins the "accepted task never lost"
// guarantee for the non-racing path.
func TestAdversarial_AcceptedBeforeStopAllRun(t *testing.T) {
	const m = 200
	p := New(4)

	var ran int64
	var submitted int64

	// Submit synchronously (single goroutine) so each Submit returns => accepted.
	for i := 0; i < m; i++ {
		p.Submit(func() {
			// Slow enough that several are genuinely in flight when Stop lands.
			time.Sleep(time.Millisecond)
			atomic.AddInt64(&ran, 1)
		})
		atomic.AddInt64(&submitted, 1)
	}

	// All m were accepted (each Submit returned). Stop must drain them.
	p.Stop()

	if got := atomic.LoadInt64(&ran); got != m {
		t.Fatalf("ACCEPTED-TASK LOSS: %d tasks accepted via Submit (returned) but only %d ran after Stop", atomic.LoadInt64(&submitted), got)
	}
}

// (d) PANIC IN TASK — a panicking task must not permanently kill a worker.
//
// Single worker: the SAME goroutine must survive each panic. We interleave many
// panicking tasks with real signalling tasks and assert every real task ran.
func TestAdversarial_PanicDoesNotDegradePool(t *testing.T) {
	p := New(1) // one worker: degradation = total loss, easy to detect
	defer p.Stop()

	const cycles = 50
	var good int64
	var ran sync.WaitGroup
	ran.Add(cycles)

	for c := 0; c < cycles; c++ {
		// Panic task.
		p.Submit(func() { panic("boom") })
		// Real task that must still run on the same (only) worker.
		p.Submit(func() {
			atomic.AddInt64(&good, 1)
			ran.Done()
		})
	}

	finished := make(chan struct{})
	go func() { ran.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatalf("POOL DEGRADED after panic: only %d of %d post-panic tasks ran", atomic.LoadInt64(&good), cycles)
	}

	if got := atomic.LoadInt64(&good); got != cycles {
		t.Fatalf("expected %d post-panic tasks to run, got %d", cycles, got)
	}
}
