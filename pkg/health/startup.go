package health

import (
	"context"
	"net/http"
	"sync"
)

// Startup is the third Kubernetes-style probe tier, added additively alongside
// Liveness and Readiness. A Startup probe answers "has this component finished
// initializing?" While a slow-starting component's Startup probe is still
// failing, the component is in its startup grace window: it must NOT be reported
// Unhealthy for being un-ready, and its liveness/readiness checks must NOT trip
// the rollup against it. Once the Startup probe succeeds the component latches
// "startup complete" and from then on normal liveness/readiness semantics apply
// (a later failure CAN make the rollup Unhealthy).
//
// Startup is a distinct CheckKind so a component can register a Startup probe in
// addition to (not instead of) its liveness/readiness dependencies.
const Startup CheckKind = "startup"

// Starting is the overall status reported while one or more components are still
// inside their startup grace window (Startup probe failing, latch not yet set)
// and nothing else has driven the rollup Unhealthy. It is distinct from
// Degraded: Starting means "not done initializing yet", not "running
// degraded". Like Degraded/Healthy it is a serving-tolerant status for liveness
// purposes — an orchestrator should keep waiting rather than restart.
const Starting Status = "starting"

// StartupGate tracks, per component name, whether that component's Startup probe
// has ever succeeded. The success is latched: once a component reports startup
// complete it stays complete, so a transient post-startup probe blip cannot drag
// the component back into the startup grace window and mask a real failure.
//
// A StartupGate is safe for concurrent use.
type StartupGate struct {
	mu        sync.RWMutex
	completed map[string]bool
}

// NewStartupGate creates an empty StartupGate with no components latched.
func NewStartupGate() *StartupGate {
	return &StartupGate{completed: make(map[string]bool)}
}

// MarkComplete latches the named component as having finished startup. Idempotent.
func (g *StartupGate) MarkComplete(name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.completed[name] = true
}

// IsComplete reports whether the named component has latched startup-complete.
func (g *StartupGate) IsComplete(name string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.completed[name]
}

// evaluateStartup runs a component's Startup probe (if any) and updates the
// latch. It returns whether the component is still starting up — i.e. it has a
// Startup probe, has not previously latched complete, and the probe is currently
// failing. A successful probe latches the component complete and returns false.
//
// The returned DependencyResult is the probe outcome (zero value Name when there
// is no startup probe for the component); callers use the boolean, not the
// result's Status, to decide gating.
func (r *DependencyRollup) evaluateStartup(ctx context.Context, name string) (starting bool, res DependencyResult, hasProbe bool) {
	if r.gate.IsComplete(name) {
		return false, DependencyResult{}, false
	}

	r.mu.RLock()
	probe, ok := r.deps[startupKey(name)]
	r.mu.RUnlock()
	if !ok {
		// No startup probe registered for this component: it is not gated,
		// preserving the existing 2-tier behavior.
		return false, DependencyResult{}, false
	}

	res = r.runOne(ctx, probe)
	if res.Status == Healthy {
		r.gate.MarkComplete(name)
		return false, res, true
	}
	return true, res, true
}

// startupKey namespaces a component's startup probe inside the shared deps map so
// it never collides with that component's liveness/readiness dependency of the
// same Name. Startup probes are addressed by component name via RegisterStartup
// and are not returned by CheckLiveness/CheckReadiness/CheckAll selection.
func startupKey(name string) string { return "\x00startup\x00" + name }

// RegisterStartup registers (or replaces) the Startup probe for the component
// identified by name. The probe gates that component: while it fails and the
// component has not latched complete, the component reports Starting and does not
// trip liveness/readiness. Registering a Startup probe is purely additive — a
// component with no Startup probe keeps the original 2-tier rollup behavior.
//
// The supplied Dependency's Kind is forced to Startup and its Name to the
// component name regardless of the caller-provided values, so it is keyed and
// reported consistently.
func (r *DependencyRollup) RegisterStartup(name string, d Dependency) {
	d.Name = name
	d.Kind = Startup
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deps[startupKey(name)] = d
}

// DeregisterStartup removes the Startup probe for a component. It does NOT clear
// an already-latched completion; use the StartupGate directly if a reset is
// required.
func (r *DependencyRollup) DeregisterStartup(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.deps, startupKey(name))
}

// StartupComplete reports whether the named component has latched startup.
func (r *DependencyRollup) StartupComplete(name string) bool {
	return r.gate.IsComplete(name)
}

// CheckStartup evaluates every registered Startup probe and reports an aggregate.
// A component still inside its grace window yields a Starting overall status; a
// component whose probe has succeeded latches complete and reports Healthy. This
// is intended for a /startupz endpoint so an orchestrator can hold traffic until
// startup finishes without restarting a slow starter.
func (r *DependencyRollup) CheckStartup(ctx context.Context) Rollup {
	r.mu.RLock()
	names := make([]string, 0, len(r.deps))
	for k := range r.deps {
		if name, ok := startupName(k); ok {
			names = append(names, name)
		}
	}
	r.mu.RUnlock()

	results := make([]DependencyResult, len(names))
	var wg sync.WaitGroup
	wg.Add(len(names))
	for i, name := range names {
		go func(i int, name string) {
			defer wg.Done()
			starting, res, _ := r.evaluateStartup(ctx, name)
			if res.Name == "" {
				// Already latched complete before this call: synthesize a
				// Healthy result so the endpoint still lists the component.
				res = DependencyResult{Name: name, Kind: Startup, Status: Healthy}
			} else if starting {
				res.Status = Starting
			}
			results[i] = res
		}(i, name)
	}
	wg.Wait()

	sortResults(results)

	overall := Healthy
	for _, res := range results {
		if res.Status == Starting && overall == Healthy {
			overall = Starting
		}
	}
	return Rollup{Status: overall, Checks: results}
}

// startupName recovers the component name from a startup map key, or reports
// false if the key is not a startup key.
func startupName(key string) (string, bool) {
	const prefix = "\x00startup\x00"
	if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
		return key[len(prefix):], true
	}
	return "", false
}

// StartupHandler returns an http.Handler that evaluates only Startup probes and
// reports the rollup. A Starting overall status serves 200 (orchestrator should
// keep waiting, not restart); intended for a /startupz endpoint.
func StartupHandler(r *DependencyRollup) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		writeRollup(w, r.CheckStartup(req.Context()))
	})
}
