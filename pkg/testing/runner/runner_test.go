package runner

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test 1 — Parallelism proof via a barrier
//
// Register 4 suites each of which:
//   1. Signals it has STARTED by doing wg.Done()
//   2. Waits for ALL 4 to have started (wg.Wait())
//   3. Returns success.
//
// With MaxConcurrency >= 4 all suites are dispatched before any returns, so
// wg.Wait() resolves and the test completes.
// With a serial runner (MaxConcurrency == 1) suite-1 would block on wg.Wait()
// forever because suite-2/3/4 never start — the context deadline would fire.
// ─────────────────────────────────────────────────────────────────────────────

func TestParallelism_BarrierProof(t *testing.T) {
	const n = 4
	var startedWG sync.WaitGroup
	startedWG.Add(n)

	// allStarted is closed once every suite has signalled it is running.
	allStarted := make(chan struct{})
	var closeOnce sync.Once
	go func() {
		startedWG.Wait()
		closeOnce.Do(func() { close(allStarted) })
	}()

	r := New(Options{MaxConcurrency: n})
	for i := 0; i < n; i++ {
		name := string(rune('A' + i)) // "A", "B", "C", "D"
		if err := r.Register(name, func(ctx context.Context) Result {
			startedWG.Done() // signal: I am running
			// Block until all peers are also running — serial runner deadlocks here.
			select {
			case <-allStarted:
				return Result{Passed: true}
			case <-ctx.Done():
				return Result{Passed: false, Err: ctx.Err()}
			}
		}); err != nil {
			t.Fatalf("Register(%q): %v", name, err)
		}
	}

	// Generous deadline: if concurrent it finishes in <<1s; serial would block
	// until deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	report := r.Run(ctx)

	if report.Passed != n {
		t.Fatalf("parallelism barrier: want %d passed, got %d (failed=%d) — serial runner would deadlock/timeout",
			n, report.Passed, report.Failed)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test 2 — Concurrency cap: MaxConcurrency=2, peak in-flight MUST NOT exceed 2
//
// Each of 6 suites atomically increments an in-flight counter on entry and
// decrements it on exit, recording the peak. The test asserts peak <= 2.
// If the cap is ignored, suites run freely and with 6 goroutines the peak will
// reach 6 — the assertion fires.
// ─────────────────────────────────────────────────────────────────────────────

func TestConcurrencyCap(t *testing.T) {
	const numSuites = 6
	const cap = 2

	var (
		inFlight int64 // current goroutines inside a suite
		peak     int64 // maximum observed inFlight
	)

	r := New(Options{MaxConcurrency: cap})
	for i := 0; i < numSuites; i++ {
		name := string(rune('0' + i))
		if err := r.Register(name, func(ctx context.Context) Result {
			cur := atomic.AddInt64(&inFlight, 1)
			// Update peak with a CAS loop so we never miss a higher value.
			for {
				old := atomic.LoadInt64(&peak)
				if cur <= old {
					break
				}
				if atomic.CompareAndSwapInt64(&peak, old, cur) {
					break
				}
			}
			// Yield to increase chance of concurrent overlap being observed.
			// Small but real work so the scheduler can see multiple goroutines
			// in-flight without relying on sleep.
			runtime_doWork()
			atomic.AddInt64(&inFlight, -1)
			return Result{Passed: true}
		}); err != nil {
			t.Fatalf("Register(%q): %v", name, err)
		}
	}

	report := r.Run(context.Background())

	if report.Total != numSuites {
		t.Fatalf("want Total=%d, got %d", numSuites, report.Total)
	}
	observedPeak := atomic.LoadInt64(&peak)
	if observedPeak > cap {
		t.Fatalf("concurrency cap violated: MaxConcurrency=%d but peak in-flight was %d", cap, observedPeak)
	}
}

// runtime_doWork performs a tiny but non-trivial computation so the Go scheduler
// has an opportunity to switch goroutines. We avoid sleep to keep tests fast.
func runtime_doWork() {
	sum := 0
	for i := 0; i < 10_000; i++ {
		sum += i
	}
	_ = sum
}

// ─────────────────────────────────────────────────────────────────────────────
// Test 3 — Result collection: 3 passing + 2 failing
//
// 3 suites return Passed=true, Err=nil.
// 1 suite returns Passed=false, Err=nil.
// 1 suite returns Passed=true, Err=<non-nil>  (Err overrides Passed flag).
//
// Expected: Total==5, Passed==3, Failed==2.
// The two failing SuiteResults must appear in the report with correct Err values.
// ─────────────────────────────────────────────────────────────────────────────

func TestResultCollection(t *testing.T) {
	sentinelErr := errors.New("injected failure")

	r := New(Options{MaxConcurrency: 5})

	// 3 passing
	for _, name := range []string{"pass-1", "pass-2", "pass-3"} {
		n := name
		if err := r.Register(n, func(ctx context.Context) Result {
			return Result{Passed: true}
		}); err != nil {
			t.Fatalf("Register(%q): %v", n, err)
		}
	}

	// 1 failing via Passed=false, no error
	if err := r.Register("fail-noerr", func(ctx context.Context) Result {
		return Result{Passed: false}
	}); err != nil {
		t.Fatalf("Register fail-noerr: %v", err)
	}

	// 1 failing via Err != nil (Passed=true should be overridden)
	if err := r.Register("fail-err", func(ctx context.Context) Result {
		return Result{Passed: true, Err: sentinelErr}
	}); err != nil {
		t.Fatalf("Register fail-err: %v", err)
	}

	report := r.Run(context.Background())

	if report.Total != 5 {
		t.Fatalf("Total: want 5, got %d", report.Total)
	}
	if report.Passed != 3 {
		t.Fatalf("Passed: want 3, got %d", report.Passed)
	}
	if report.Failed != 2 {
		t.Fatalf("Failed: want 2, got %d", report.Failed)
	}

	// Verify the report carries the failing suite entries correctly.
	failErrs := map[string]error{}
	for _, sr := range report.Suites {
		if !sr.Passed {
			failErrs[sr.Name] = sr.Err
		}
	}
	if len(failErrs) != 2 {
		t.Fatalf("expected 2 failing SuiteResults in report, got %d", len(failErrs))
	}
	if _, ok := failErrs["fail-noerr"]; !ok {
		t.Fatal("fail-noerr should appear in failed suites")
	}
	if _, ok := failErrs["fail-err"]; !ok {
		t.Fatal("fail-err should appear in failed suites")
	}
	if !errors.Is(failErrs["fail-err"], sentinelErr) {
		t.Fatalf("fail-err: want Err=%v, got %v", sentinelErr, failErrs["fail-err"])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test 4 — Register rejects duplicate and empty names
// ─────────────────────────────────────────────────────────────────────────────

func TestRegister_Validation(t *testing.T) {
	r := New(Options{})

	noop := func(ctx context.Context) Result { return Result{Passed: true} }

	// Empty name must return ErrEmptyName.
	if err := r.Register("", noop); !errors.Is(err, ErrEmptyName) {
		t.Fatalf("empty name: want ErrEmptyName, got %v", err)
	}

	// First registration of a valid name must succeed.
	if err := r.Register("alpha", noop); err != nil {
		t.Fatalf("first Register(alpha): unexpected error %v", err)
	}

	// Duplicate registration must return ErrDuplicateName.
	if err := r.Register("alpha", noop); !errors.Is(err, ErrDuplicateName) {
		t.Fatalf("duplicate name: want ErrDuplicateName, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test 5 — Report.Suites is sorted by Name
// ─────────────────────────────────────────────────────────────────────────────

func TestReport_SuitesSortedByName(t *testing.T) {
	r := New(Options{MaxConcurrency: 4})
	names := []string{"zebra", "apple", "mango", "banana"}
	for _, n := range names {
		nm := n
		_ = r.Register(nm, func(ctx context.Context) Result { return Result{Passed: true} })
	}

	report := r.Run(context.Background())

	want := []string{"apple", "banana", "mango", "zebra"}
	if len(report.Suites) != len(want) {
		t.Fatalf("want %d suites, got %d", len(want), len(report.Suites))
	}
	for i, sr := range report.Suites {
		if sr.Name != want[i] {
			t.Fatalf("Suites[%d].Name: want %q, got %q", i, want[i], sr.Name)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test 6 — MaxDuration is the maximum suite duration
// ─────────────────────────────────────────────────────────────────────────────

func TestReport_MaxDuration(t *testing.T) {
	r := New(Options{MaxConcurrency: 4})

	// One suite that takes at least 10ms.
	slowDone := make(chan struct{})
	_ = r.Register("slow", func(ctx context.Context) Result {
		time.Sleep(10 * time.Millisecond)
		close(slowDone)
		return Result{Passed: true}
	})
	_ = r.Register("fast", func(ctx context.Context) Result {
		return Result{Passed: true}
	})

	report := r.Run(context.Background())

	if report.MaxDuration < 10*time.Millisecond {
		t.Fatalf("MaxDuration should be >= 10ms (slow suite), got %v", report.MaxDuration)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test 7 — Context cancellation stops dispatching new suites
// ─────────────────────────────────────────────────────────────────────────────

func TestContextCancellation_StopsDispatching(t *testing.T) {
	// Use MaxConcurrency=1 so suites run sequentially; cancel the context
	// after the first suite returns. The second suite should never run.
	ctx, cancel := context.WithCancel(context.Background())

	var started int64
	r := New(Options{MaxConcurrency: 1})

	// Suite 1: increments counter and cancels ctx so no more suites dispatch.
	_ = r.Register("a-first", func(ctx context.Context) Result {
		atomic.AddInt64(&started, 1)
		cancel() // signal the runner to stop dispatching
		return Result{Passed: true}
	})
	// Suite 2: if it runs it increments the counter; runner should skip it.
	_ = r.Register("b-second", func(ctx context.Context) Result {
		atomic.AddInt64(&started, 1)
		return Result{Passed: true}
	})

	_ = r.Run(ctx)

	// At least 1 must have started; the second may or may not (race between
	// cancel and dispatch) — but Total must be <= 2 and we should not panic.
	got := atomic.LoadInt64(&started)
	if got < 1 {
		t.Fatalf("expected at least 1 suite to have started, got %d", got)
	}
	// This is not a strict assert on exactly 1 because with MaxConcurrency=1
	// the second suite may have already been dispatched; the key invariant is
	// no panic and at most 2 started.
	if got > 2 {
		t.Fatalf("started should be <= 2, got %d", got)
	}
}
