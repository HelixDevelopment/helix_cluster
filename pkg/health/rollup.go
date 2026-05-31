package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"
)

// CheckKind distinguishes liveness checks from readiness checks.
//
//   - Liveness answers "is the process alive and not deadlocked?" A failing
//     liveness check typically means the instance should be restarted.
//   - Readiness answers "can the process serve traffic right now?" A failing
//     readiness check typically means the instance should be removed from the
//     load-balancer rotation until its dependencies recover.
type CheckKind string

const (
	Liveness  CheckKind = "liveness"
	Readiness CheckKind = "readiness"
)

// defaultCheckTimeout is applied to a Dependency that does not set Timeout.
// It guarantees a hanging check can never block the aggregator forever.
const defaultCheckTimeout = 5 * time.Second

// DependencyCheck performs a single dependency health probe. It MUST honor the
// provided context: when the context is cancelled (e.g. its deadline is
// exceeded) the check should return promptly. A nil return means healthy; a
// non-nil error means the dependency is failing.
type DependencyCheck func(ctx context.Context) error

// Dependency describes one named health check and how it contributes to the
// overall rollup.
type Dependency struct {
	// Name uniquely identifies the dependency within a Rollup.
	Name string
	// Kind selects whether this check participates in liveness, readiness, or
	// both (via CheckAll). Defaults to Readiness when empty.
	Kind CheckKind
	// Critical marks a dependency whose failure makes the overall status
	// Unhealthy. A non-critical failure only Degrades the overall status.
	Critical bool
	// Timeout bounds a single execution of Check. If zero, defaultCheckTimeout
	// is used. A check that does not return before the deadline is reported as
	// Unhealthy with a context-deadline error.
	Timeout time.Duration
	// Check is the probe function. Required.
	Check DependencyCheck
}

// DependencyResult is the outcome of evaluating a single Dependency.
type DependencyResult struct {
	Name     string        `json:"name"`
	Kind     CheckKind     `json:"kind"`
	Critical bool          `json:"critical"`
	Status   Status        `json:"status"`
	Error    string        `json:"error,omitempty"`
	Duration time.Duration `json:"duration_ns"`
}

// Rollup is the aggregated result of evaluating a set of dependencies.
type Rollup struct {
	Status Status             `json:"status"`
	Checks []DependencyResult `json:"checks"`
}

// clock abstracts time so per-check timeouts are deterministic in tests.
type clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time                         { return time.Now() }
func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// DependencyRollup aggregates multiple named dependency checks, distinguishing
// liveness from readiness and critical from non-critical, with a per-check
// timeout so a single hanging check can never block the whole rollup.
type DependencyRollup struct {
	mu    sync.RWMutex
	deps  map[string]Dependency
	clock clock
	// gate latches per-component startup completion. See startup.go. It is
	// always non-nil after construction so the 2-tier path needs no nil checks.
	gate *StartupGate
}

// NewRollup creates a DependencyRollup backed by the real system clock.
func NewRollup() *DependencyRollup {
	return NewRollupWithClock(systemClock{})
}

// NewRollupWithClock creates a DependencyRollup with an injectable clock. The
// clock drives per-check timeouts, making timeout behavior deterministic in
// tests. A nil clock falls back to the system clock.
func NewRollupWithClock(c clock) *DependencyRollup {
	if c == nil {
		c = systemClock{}
	}
	return &DependencyRollup{
		deps:  make(map[string]Dependency),
		clock: c,
		gate:  NewStartupGate(),
	}
}

// Register adds or replaces a dependency by name. A Dependency with an empty
// Kind defaults to Readiness.
func (r *DependencyRollup) Register(d Dependency) {
	if d.Kind == "" {
		d.Kind = Readiness
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deps[d.Name] = d
}

// Deregister removes a dependency by name.
func (r *DependencyRollup) Deregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.deps, name)
}

// CheckLiveness evaluates only liveness dependencies.
func (r *DependencyRollup) CheckLiveness(ctx context.Context) Rollup {
	return r.check(ctx, func(d Dependency) bool { return d.Kind == Liveness })
}

// CheckReadiness evaluates only readiness dependencies.
func (r *DependencyRollup) CheckReadiness(ctx context.Context) Rollup {
	return r.check(ctx, func(d Dependency) bool { return d.Kind == Readiness })
}

// CheckAll evaluates every registered dependency regardless of kind.
func (r *DependencyRollup) CheckAll(ctx context.Context) Rollup {
	return r.check(ctx, func(Dependency) bool { return true })
}

// check runs every dependency matching the predicate concurrently, each under
// its own deadline, then rolls the per-check results up into an overall status.
func (r *DependencyRollup) check(ctx context.Context, include func(Dependency) bool) Rollup {
	r.mu.RLock()
	selected := make([]Dependency, 0, len(r.deps))
	for key, d := range r.deps {
		// Startup probes live in the same map under a namespaced key; they are
		// addressed only via the startup tier and must never be selected by
		// liveness/readiness/all.
		if _, isStartup := startupName(key); isStartup {
			continue
		}
		if include(d) {
			selected = append(selected, d)
		}
	}
	r.mu.RUnlock()

	results := make([]DependencyResult, len(selected))
	var wg sync.WaitGroup
	wg.Add(len(selected))
	for i, d := range selected {
		go func(i int, d Dependency) {
			defer wg.Done()
			// A component still inside its startup grace window (Startup probe
			// failing, not yet latched) is NOT evaluated for liveness/readiness
			// failure: its result is reported Starting so a not-yet-ready
			// component is never marked Unhealthy for being un-ready.
			isStarting, _, _ := r.evaluateStartup(ctx, d.Name)
			if isStarting {
				results[i] = DependencyResult{
					Name:     d.Name,
					Kind:     d.Kind,
					Critical: d.Critical,
					Status:   Starting,
				}
				return
			}
			results[i] = r.runOne(ctx, d)
		}(i, d)
	}
	wg.Wait()

	// Deterministic ordering for stable output/JSON regardless of map iteration.
	sortResults(results)

	overall := Healthy
	for _, res := range results {
		if res.Status == Healthy {
			continue
		}
		// A component still starting up cannot make the rollup Unhealthy or
		// Degraded — it only raises Healthy to Starting. This is the gate: a
		// slow starter gets its grace window instead of a restart/eviction.
		if res.Status == Starting {
			if overall == Healthy {
				overall = Starting
			}
			continue
		}
		// A failing critical dependency makes the whole rollup Unhealthy.
		// A failing non-critical dependency only Degrades it.
		if res.Critical {
			overall = Unhealthy
		} else if overall != Unhealthy {
			overall = Degraded
		}
	}

	return Rollup{Status: overall, Checks: results}
}

// runOne evaluates a single dependency under a per-check deadline driven by the
// injectable clock. A check that does not return before its timeout is reported
// Unhealthy, and runOne returns promptly without waiting for the check to
// finish — so one hanging check cannot stall the aggregator.
func (r *DependencyRollup) runOne(ctx context.Context, d Dependency) DependencyResult {
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = defaultCheckTimeout
	}

	res := DependencyResult{
		Name:     d.Name,
		Kind:     d.Kind,
		Critical: d.Critical,
	}

	if d.Check == nil {
		res.Status = Unhealthy
		res.Error = "no check function registered"
		return res
	}

	// Per-check context derived from the caller's context, cancelled when this
	// check completes or its deadline fires.
	checkCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	start := r.clock.Now()
	type outcome struct{ err error }
	resultCh := make(chan outcome, 1)
	go func() {
		// Recover so a panicking check is reported as unhealthy rather than
		// crashing the aggregator.
		defer func() {
			if rec := recover(); rec != nil {
				resultCh <- outcome{err: panicError(rec)}
			}
		}()
		resultCh <- outcome{err: d.Check(checkCtx)}
	}()

	timer := r.clock.After(timeout)
	select {
	case out := <-resultCh:
		res.Duration = r.clock.Now().Sub(start)
		if out.err != nil {
			res.Status = Unhealthy
			res.Error = out.err.Error()
		} else {
			res.Status = Healthy
		}
	case <-timer:
		// Deadline exceeded: cancel the check's context (so a well-behaved
		// check unwinds) and report Unhealthy without blocking on it.
		cancel()
		res.Duration = r.clock.Now().Sub(start)
		res.Status = Unhealthy
		res.Error = context.DeadlineExceeded.Error()
	case <-ctx.Done():
		cancel()
		res.Duration = r.clock.Now().Sub(start)
		res.Status = Unhealthy
		res.Error = ctx.Err().Error()
	}
	return res
}

// sortResults orders dependency results by name for stable output/JSON
// regardless of map iteration order.
func sortResults(results []DependencyResult) {
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })
}

// statusCode maps an overall Status to an HTTP status code. Healthy and
// Degraded are both serving (200) — a degraded service still accepts traffic —
// while Unhealthy returns 503 so orchestrators take the instance out of
// rotation.
func statusCode(s Status) int {
	if s == Unhealthy {
		return http.StatusServiceUnavailable
	}
	return http.StatusOK
}

func writeRollup(w http.ResponseWriter, rollup Rollup) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode(rollup.Status))
	_ = json.NewEncoder(w).Encode(rollup)
}

// LivenessHandler returns an http.Handler that evaluates only liveness
// dependencies and reports the rollup. Intended for a /livez endpoint.
func LivenessHandler(r *DependencyRollup) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		writeRollup(w, r.CheckLiveness(req.Context()))
	})
}

// ReadinessHandler returns an http.Handler that evaluates only readiness
// dependencies and reports the rollup. Intended for a /readyz endpoint.
func ReadinessHandler(r *DependencyRollup) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		writeRollup(w, r.CheckReadiness(req.Context()))
	})
}

// panicError wraps a recovered panic value into an error.
func panicError(v any) error {
	return &recoveredPanic{v: v}
}

type recoveredPanic struct{ v any }

func (e *recoveredPanic) Error() string {
	return "health check panicked: " + sprint(e.v)
}

// sprint renders a recovered panic value without pulling in fmt at call sites.
func sprint(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case error:
		return t.Error()
	default:
		return "unknown panic"
	}
}
