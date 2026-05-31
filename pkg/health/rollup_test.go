package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// --- Clock injection ---

func TestSystemClock_NowAndAfter(t *testing.T) {
	clk := systemClock{}
	start := clk.Now()
	ch := clk.After(10 * time.Millisecond)
	select {
	case fired := <-ch:
		if fired.Before(start) {
			t.Errorf("fired time %v should not be before start %v", fired, start)
		}
	case <-time.After(time.Second):
		t.Fatal("systemClock.After did not fire within 1s")
	}
}

// fakeClock is a deterministic, manually-advanced clock for tests.
type fakeClock struct {
	mu      chanMutex
	now     time.Time
	waiters []fakeWaiter
}

type fakeWaiter struct {
	deadline time.Time
	ch       chan time.Time
}

// chanMutex is a tiny mutex built on a buffered channel to avoid importing sync
// in the test file solely for locking; keeps the fake clock race-safe.
type chanMutex chan struct{}

func newChanMutex() chanMutex {
	m := make(chanMutex, 1)
	m <- struct{}{}
	return m
}
func (m chanMutex) lock()   { <-m }
func (m chanMutex) unlock() { m <- struct{}{} }

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{mu: newChanMutex(), now: start}
}

func (f *fakeClock) Now() time.Time {
	f.mu.lock()
	defer f.mu.unlock()
	return f.now
}

func (f *fakeClock) After(d time.Duration) <-chan time.Time {
	f.mu.lock()
	defer f.mu.unlock()
	ch := make(chan time.Time, 1)
	deadline := f.now.Add(d)
	if d <= 0 {
		ch <- f.now
		return ch
	}
	f.waiters = append(f.waiters, fakeWaiter{deadline: deadline, ch: ch})
	return ch
}

// advance moves the clock forward and fires any waiters whose deadline passed.
func (f *fakeClock) advance(d time.Duration) {
	f.mu.lock()
	defer f.mu.unlock()
	f.now = f.now.Add(d)
	remaining := f.waiters[:0]
	for _, w := range f.waiters {
		if !w.deadline.After(f.now) {
			w.ch <- f.now
		} else {
			remaining = append(remaining, w)
		}
	}
	f.waiters = remaining
}

// --- Liveness vs Readiness distinction ---

func TestRollup_LivenessReadinessSeparation(t *testing.T) {
	r := NewRollup()
	r.Register(Dependency{
		Name: "process",
		Kind: Liveness,
		Check: func(ctx context.Context) error { return nil },
	})
	r.Register(Dependency{
		Name:     "db",
		Kind:     Readiness,
		Critical: true,
		Check:    func(ctx context.Context) error { return errors.New("db not ready") },
	})

	// Liveness only: should be healthy (process is alive).
	live := r.CheckLiveness(context.Background())
	if live.Status != Healthy {
		t.Errorf("liveness: expected Healthy, got %s (%+v)", live.Status, live.Checks)
	}
	if len(live.Checks) != 1 || live.Checks[0].Name != "process" {
		t.Errorf("liveness should only include liveness checks, got %+v", live.Checks)
	}

	// Readiness only: db is critical and failing -> Unhealthy.
	ready := r.CheckReadiness(context.Background())
	if ready.Status != Unhealthy {
		t.Errorf("readiness: expected Unhealthy, got %s (%+v)", ready.Status, ready.Checks)
	}
	if len(ready.Checks) != 1 || ready.Checks[0].Name != "db" {
		t.Errorf("readiness should only include readiness checks, got %+v", ready.Checks)
	}
}

// --- Critical vs non-critical status transitions ---

func TestRollup_CriticalFailureUnhealthy(t *testing.T) {
	r := NewRollup()
	r.Register(Dependency{Name: "ok", Kind: Readiness, Check: okCheck})
	r.Register(Dependency{Name: "db", Kind: Readiness, Critical: true, Check: failCheck})

	res := r.CheckReadiness(context.Background())
	if res.Status != Unhealthy {
		t.Fatalf("one critical failure must flip overall to Unhealthy, got %s", res.Status)
	}
}

// Mutation pairing: same topology but the failing dep is NON-critical -> Degraded, NOT Unhealthy.
func TestRollup_NonCriticalFailureDegraded(t *testing.T) {
	r := NewRollup()
	r.Register(Dependency{Name: "ok", Kind: Readiness, Check: okCheck})
	r.Register(Dependency{Name: "cache", Kind: Readiness, Critical: false, Check: failCheck})

	res := r.CheckReadiness(context.Background())
	if res.Status != Degraded {
		t.Fatalf("one non-critical failure must yield Degraded (not Unhealthy/Healthy), got %s", res.Status)
	}
	if res.Status == Unhealthy {
		t.Fatal("non-critical failure must NOT be Unhealthy")
	}
}

func TestRollup_AllHealthy(t *testing.T) {
	r := NewRollup()
	r.Register(Dependency{Name: "db", Kind: Readiness, Critical: true, Check: okCheck})
	r.Register(Dependency{Name: "cache", Kind: Readiness, Critical: false, Check: okCheck})

	res := r.CheckReadiness(context.Background())
	if res.Status != Healthy {
		t.Fatalf("all passing must be Healthy, got %s", res.Status)
	}
}

// Critical failure dominates a concurrent non-critical failure -> Unhealthy.
func TestRollup_CriticalDominatesNonCritical(t *testing.T) {
	r := NewRollup()
	r.Register(Dependency{Name: "cache", Kind: Readiness, Critical: false, Check: failCheck})
	r.Register(Dependency{Name: "db", Kind: Readiness, Critical: true, Check: failCheck})

	res := r.CheckReadiness(context.Background())
	if res.Status != Unhealthy {
		t.Fatalf("critical failure must dominate, got %s", res.Status)
	}
}

// --- Per-check timeout: a hanging check must be cut off, not block forever ---

func TestRollup_HangingCheckTimesOutUnhealthy(t *testing.T) {
	started := make(chan struct{})
	r := NewRollup()
	// Critical hanging check: blocks until ctx is cancelled.
	r.Register(Dependency{
		Name:     "hang",
		Kind:     Readiness,
		Critical: true,
		Timeout:  50 * time.Millisecond, // short, deterministic real deadline
		Check: func(ctx context.Context) error {
			close(started)
			<-ctx.Done() // would block forever without the per-check deadline
			return ctx.Err()
		},
	})

	done := make(chan Rollup)
	go func() { done <- r.CheckReadiness(context.Background()) }()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("check never started")
	}

	select {
	case res := <-done:
		if res.Status != Unhealthy {
			t.Fatalf("hanging critical check must report Unhealthy, got %s", res.Status)
		}
		if len(res.Checks) != 1 {
			t.Fatalf("expected 1 check result, got %d", len(res.Checks))
		}
		if res.Checks[0].Status != Unhealthy {
			t.Errorf("hanging check status must be Unhealthy, got %s", res.Checks[0].Status)
		}
		if res.Checks[0].Error == "" {
			t.Error("timed-out check must carry an error message (sink-side evidence)")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("aggregator hung on a blocking check — per-check timeout did not cut it off")
	}
}

// Mutation pairing for timeout: a NON-critical hanging check -> Degraded, not Unhealthy,
// and still must not block the aggregator.
func TestRollup_HangingNonCriticalCheckDegraded(t *testing.T) {
	r := NewRollup()
	r.Register(Dependency{
		Name:     "slow-cache",
		Kind:     Readiness,
		Critical: false,
		Timeout:  50 * time.Millisecond,
		Check: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	})

	done := make(chan Rollup)
	go func() { done <- r.CheckReadiness(context.Background()) }()

	select {
	case res := <-done:
		if res.Status != Degraded {
			t.Fatalf("non-critical hanging check must yield Degraded, got %s", res.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("aggregator hung on a non-critical blocking check")
	}
}

// Deterministic timeout proof using an injectable fake clock: the check blocks,
// we advance the fake clock past the deadline, and the result must be Unhealthy.
func TestRollup_TimeoutUsesInjectedClock(t *testing.T) {
	clk := newFakeClock(time.Unix(0, 0))
	r := NewRollupWithClock(clk)

	release := make(chan struct{})
	r.Register(Dependency{
		Name:     "db",
		Kind:     Readiness,
		Critical: true,
		Timeout:  30 * time.Second, // large; only the fake clock advances it
		Check: func(ctx context.Context) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
				return nil
			}
		},
	})

	done := make(chan Rollup)
	go func() { done <- r.CheckReadiness(context.Background()) }()

	// The check is blocked; advancing the injected clock past the timeout must
	// fire the deadline and cut the check off as Unhealthy — proving the
	// aggregator's timeout is driven by the injectable clock, not wall time.
	deadline := time.After(2 * time.Second)
	for {
		clk.advance(31 * time.Second)
		select {
		case res := <-done:
			if res.Status != Unhealthy {
				t.Fatalf("expected Unhealthy after clock-driven timeout, got %s", res.Status)
			}
			return
		case <-deadline:
			close(release)
			t.Fatal("injected clock advance did not trigger per-check timeout")
		default:
		}
	}
}

func TestRollup_EmptyIsHealthy(t *testing.T) {
	r := NewRollup()
	res := r.CheckReadiness(context.Background())
	if res.Status != Healthy {
		t.Errorf("rollup with no checks should be Healthy, got %s", res.Status)
	}
	live := r.CheckLiveness(context.Background())
	if live.Status != Healthy {
		t.Errorf("empty liveness should be Healthy, got %s", live.Status)
	}
}

// CheckAll includes both liveness and readiness deps.
func TestRollup_CheckAllCombinesKinds(t *testing.T) {
	r := NewRollup()
	r.Register(Dependency{Name: "process", Kind: Liveness, Check: okCheck})
	r.Register(Dependency{Name: "db", Kind: Readiness, Critical: true, Check: failCheck})

	res := r.CheckAll(context.Background())
	if res.Status != Unhealthy {
		t.Errorf("CheckAll: critical readiness failure should make overall Unhealthy, got %s", res.Status)
	}
	if len(res.Checks) != 2 {
		t.Errorf("CheckAll should include both kinds, got %d checks", len(res.Checks))
	}
}

func TestRollup_PerCheckResultsPopulated(t *testing.T) {
	r := NewRollup()
	r.Register(Dependency{Name: "db", Kind: Readiness, Critical: true, Check: failCheck})
	r.Register(Dependency{Name: "cache", Kind: Readiness, Critical: false, Check: okCheck})

	res := r.CheckReadiness(context.Background())
	byName := map[string]DependencyResult{}
	for _, c := range res.Checks {
		byName[c.Name] = c
	}
	if byName["db"].Status != Unhealthy {
		t.Errorf("db result should be Unhealthy, got %s", byName["db"].Status)
	}
	if !byName["db"].Critical {
		t.Error("db result should report Critical=true")
	}
	if byName["db"].Error == "" {
		t.Error("failing db result should carry an error string")
	}
	if byName["cache"].Status != Healthy {
		t.Errorf("cache result should be Healthy, got %s", byName["cache"].Status)
	}
}

// --- HTTP handlers: sink-side end-user evidence ---

func TestReadinessHandler_CriticalFailureReturns503(t *testing.T) {
	r := NewRollup()
	r.Register(Dependency{Name: "db", Kind: Readiness, Critical: true, Check: failCheck})

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	ReadinessHandler(r).ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("critical readiness failure must return 503, got %d", rec.Code)
	}
	var got Rollup
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != Unhealthy {
		t.Errorf("body status should be Unhealthy, got %s", got.Status)
	}
	if len(got.Checks) != 1 || got.Checks[0].Name != "db" {
		t.Errorf("body should carry per-check db result, got %+v", got.Checks)
	}
}

func TestReadinessHandler_NonCriticalFailureReturns200Degraded(t *testing.T) {
	r := NewRollup()
	r.Register(Dependency{Name: "cache", Kind: Readiness, Critical: false, Check: failCheck})

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	ReadinessHandler(r).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("non-critical degradation must still serve (200), got %d", rec.Code)
	}
	var got Rollup
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != Degraded {
		t.Errorf("body status should be Degraded, got %s", got.Status)
	}
}

func TestLivenessHandler_OnlyLivenessChecks(t *testing.T) {
	r := NewRollup()
	r.Register(Dependency{Name: "process", Kind: Liveness, Check: okCheck})
	r.Register(Dependency{Name: "db", Kind: Readiness, Critical: true, Check: failCheck})

	req := httptest.NewRequest(http.MethodGet, "/livez", nil)
	rec := httptest.NewRecorder()
	LivenessHandler(r).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("liveness must ignore failing readiness dep, got %d", rec.Code)
	}
	var got Rollup
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != Healthy {
		t.Errorf("liveness status should be Healthy, got %s", got.Status)
	}
	if len(got.Checks) != 1 || got.Checks[0].Name != "process" {
		t.Errorf("liveness handler should only run liveness checks, got %+v", got.Checks)
	}
}

// --- helpers ---

func okCheck(ctx context.Context) error   { return nil }
func failCheck(ctx context.Context) error { return errors.New("boom") }
