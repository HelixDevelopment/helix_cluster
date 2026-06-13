// Package healthprobe implements a Kubernetes-style three-tier health probe
// (startup / readiness / liveness) with a startup GRACE PERIOD, in pure,
// deterministic Go.
//
// # Model
//
// A component is composed of named dependencies, each of which is
// health-checkable via a CheckFn. There are three probe tiers:
//
//   - CheckStartup   — "did the component finish booting?" During a configured
//     startup grace window, a still-failing dependency is TOLERATED (reported
//     Healthy) so the orchestrator does not kill a slow-starting component
//     (e.g. a GPU warming up). Once the grace window ELAPSES, a still-failing
//     dependency flips CheckStartup to Unhealthy => the orchestrator restarts.
//   - CheckReadiness — "should this component receive traffic?" A failing
//     dependency is NEVER tolerated here, in or out of grace: traffic must not
//     be routed to a component that cannot serve it.
//   - CheckLiveness  — "is the component currently alive?" Reflects current
//     health of dependencies directly, independent of the startup grace.
//
// # Determinism
//
// All time-dependent logic flows through an injected Clock. Logic under test
// never calls time.Now(); advance a ManualClock to model the passage of the
// grace window.
package healthprobe

import (
	"sort"
	"time"
)

// Status is the binary health verdict reported by a probe tier, either for a
// single dependency or for the aggregate.
type Status int

const (
	// Unhealthy indicates the checked subject is failing.
	Unhealthy Status = iota
	// Healthy indicates the checked subject is passing (or tolerated in-grace
	// for the startup tier).
	Healthy
)

// String renders the Status for logs and human-readable test output.
func (s Status) String() string {
	switch s {
	case Healthy:
		return "Healthy"
	case Unhealthy:
		return "Unhealthy"
	default:
		return "Unknown"
	}
}

// Clock is the injectable time source. Logic under test reads Now() from here
// instead of the wall clock so the grace window can be advanced deterministically.
type Clock interface {
	Now() time.Time
}

// ManualClock is a deterministic Clock driven by explicit advancement. It is
// the intended Clock for tests and simulations.
type ManualClock struct {
	t time.Time
}

// NewManualClock returns a ManualClock initialized to the given instant.
func NewManualClock(start time.Time) *ManualClock {
	return &ManualClock{t: start}
}

// Now returns the clock's current logical instant.
func (c *ManualClock) Now() time.Time { return c.t }

// Advance moves the logical clock forward by d.
func (c *ManualClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

// Set forces the logical clock to an absolute instant.
func (c *ManualClock) Set(t time.Time) { c.t = t }

// CheckFn reports whether a single dependency is currently healthy. It must be
// pure and side-effect free for the probe results to be deterministic.
type CheckFn func() bool

type dependency struct {
	name  string
	check CheckFn
}

// DependencyResult is the per-dependency detail attached to a ProbeResult.
type DependencyResult struct {
	// Name is the dependency identifier supplied to RegisterDependency.
	Name string
	// Raw is the unmodified result of the dependency's CheckFn — the
	// underlying health, before any grace tolerance is applied.
	Raw Status
	// Reported is the Status the probe tier attributes to this dependency
	// after applying tier-specific policy (e.g. startup in-grace tolerance).
	Reported Status
	// Tolerated is true only when Raw is Unhealthy but Reported is Healthy
	// because the startup grace window masked the failure.
	Tolerated bool
}

// ProbeResult is the aggregate verdict of a probe tier plus per-dependency detail.
type ProbeResult struct {
	// Aggregate is Healthy only if every dependency's Reported status is Healthy.
	Aggregate Status
	// Dependencies holds per-dependency detail, sorted by Name for determinism.
	Dependencies []DependencyResult
}

// Prober evaluates the three probe tiers over a set of registered dependencies,
// applying a startup grace window measured against an injected Clock.
type Prober struct {
	clock       Clock
	gracePeriod time.Duration
	startedAt   time.Time
	deps        []dependency
}

// NewProber constructs a Prober. The grace window begins at clock.Now() at
// construction time; gracePeriod is the duration a failing dependency is
// tolerated by CheckStartup before startup is declared failed.
func NewProber(clock Clock, gracePeriod time.Duration) *Prober {
	return &Prober{
		clock:       clock,
		gracePeriod: gracePeriod,
		startedAt:   clock.Now(),
	}
}

// RegisterDependency adds a named, health-checkable dependency. Re-registering
// an existing name replaces its check function.
func (p *Prober) RegisterDependency(name string, check CheckFn) {
	for i := range p.deps {
		if p.deps[i].name == name {
			p.deps[i].check = check
			return
		}
	}
	p.deps = append(p.deps, dependency{name: name, check: check})
}

// GracePeriod returns the configured startup grace window.
func (p *Prober) GracePeriod() time.Duration { return p.gracePeriod }

// inGrace reports whether the current clock instant is still within the startup
// grace window. The window is [startedAt, startedAt+gracePeriod); it is over
// once the elapsed time reaches (is no longer strictly less than) gracePeriod.
func (p *Prober) inGrace() bool {
	elapsed := p.clock.Now().Sub(p.startedAt)
	return elapsed < p.gracePeriod
}

// CheckStartup reports startup health. A failing dependency is tolerated
// (reported Healthy) WHILE the grace window is open; once the window elapses, a
// still-failing dependency is reported Unhealthy so the orchestrator restarts.
func (p *Prober) CheckStartup() ProbeResult {
	inGrace := p.inGrace()
	return p.evaluate(func(raw Status) (Status, bool) {
		if raw == Unhealthy && inGrace {
			return Healthy, true // tolerated during grace
		}
		return raw, false
	})
}

// CheckReadiness reports readiness to receive traffic. A failing dependency is
// never tolerated — readiness is Unhealthy regardless of the grace window.
func (p *Prober) CheckReadiness() ProbeResult {
	return p.evaluate(func(raw Status) (Status, bool) {
		return raw, false
	})
}

// CheckLiveness reports current liveness, reflecting raw dependency health
// directly and independent of the startup grace window.
func (p *Prober) CheckLiveness() ProbeResult {
	return p.evaluate(func(raw Status) (Status, bool) {
		return raw, false
	})
}

// evaluate runs every dependency's check, applies the tier policy to each, and
// aggregates. policy maps a raw status to (reported status, tolerated).
func (p *Prober) evaluate(policy func(raw Status) (Status, bool)) ProbeResult {
	results := make([]DependencyResult, 0, len(p.deps))
	aggregate := Healthy
	for _, d := range p.deps {
		raw := Unhealthy
		if d.check() {
			raw = Healthy
		}
		reported, tolerated := policy(raw)
		if reported == Unhealthy {
			aggregate = Unhealthy
		}
		results = append(results, DependencyResult{
			Name:      d.name,
			Raw:       raw,
			Reported:  reported,
			Tolerated: tolerated,
		})
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Name < results[j].Name
	})
	return ProbeResult{Aggregate: aggregate, Dependencies: results}
}

// Find returns the DependencyResult for name, and whether it was present.
func (r ProbeResult) Find(name string) (DependencyResult, bool) {
	for _, d := range r.Dependencies {
		if d.Name == name {
			return d, true
		}
	}
	return DependencyResult{}, false
}
