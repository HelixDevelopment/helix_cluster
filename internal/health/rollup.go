// Package health provides the health check aggregation service for Helix Cluster OS.
package health

// CheckKind classifies a health check as contributing to liveness, readiness,
// or both. Liveness answers "is the process alive and should it keep running";
// readiness answers "can the process currently serve traffic". A check may be
// relevant to both rollups.
type CheckKind uint8

const (
	// KindReadiness marks a check that only affects readiness. This is the
	// default: a failing dependency (e.g. a database) should make the node
	// unready (drop out of load balancing) without forcing a restart.
	KindReadiness CheckKind = iota
	// KindLiveness marks a check that only affects liveness. A failing
	// liveness check signals the supervisor that the process is wedged and
	// should be restarted (e.g. deadlock or unrecoverable internal state).
	KindLiveness
	// KindBoth marks a check that affects both liveness and readiness.
	KindBoth
)

// affectsLiveness reports whether the kind contributes to the liveness rollup.
func (k CheckKind) affectsLiveness() bool {
	return k == KindLiveness || k == KindBoth
}

// affectsReadiness reports whether the kind contributes to the readiness rollup.
func (k CheckKind) affectsReadiness() bool {
	return k == KindReadiness || k == KindBoth
}

// RegisterCheckKind registers a named health check with an explicit kind so it
// can be included in the liveness and/or readiness rollups. Checks registered
// via RegisterCheck default to KindReadiness.
func (a *Aggregator) RegisterCheckKind(name string, check HealthCheck, kind CheckKind) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.checks[name] = check
	a.kinds[name] = kind
}

// Liveness returns the rolled-up liveness status across all checks that affect
// liveness, using the same Critical > Degraded > Unknown > Healthy precedence
// as the overall status. If no liveness checks are registered the node is
// considered Healthy (alive) — absence of a liveness probe must never wedge a
// process into a restart loop.
func (a *Aggregator) Liveness() Status {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.rollupLocked(CheckKind.affectsLiveness, Healthy)
}

// Ready reports whether the node should currently receive traffic. It is true
// only when the readiness rollup is Healthy. Degraded, Critical, and Unknown
// all map to not-ready, which is the conservative choice for load balancing.
func (a *Aggregator) Ready() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.rollupLocked(CheckKind.affectsReadiness, Unknown) == Healthy
}

// Readiness returns the rolled-up readiness status across all checks that
// affect readiness. If no readiness checks have produced results the status is
// Unknown, which Ready treats as not-ready.
func (a *Aggregator) Readiness() Status {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.rollupLocked(CheckKind.affectsReadiness, Unknown)
}

// rollupLocked aggregates the statuses of results whose kind satisfies the
// selector, returning empty when no result matches. The read lock must be held.
func (a *Aggregator) rollupLocked(selector func(CheckKind) bool, empty Status) Status {
	overall := Healthy
	matched := false
	for name, r := range a.results {
		if !selector(a.kinds[name]) {
			continue
		}
		matched = true
		switch r.Status {
		case Critical:
			return Critical
		case Degraded:
			overall = Degraded
		case Unknown:
			if overall == Healthy {
				overall = Unknown
			}
		}
	}
	if !matched {
		return empty
	}
	return overall
}
