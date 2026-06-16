// Package health provides the health check aggregation service for Helix Cluster OS.
package health

import (
	"context"
	"fmt"
	"sync"
)

// ServiceStatus holds the result of a single service health check.
type ServiceStatus struct {
	Name   string
	Status Status
	Error  error
}

// Aggregator collects and aggregates health status from all nodes and services.
type Aggregator struct {
	mu      sync.RWMutex
	checks  map[string]HealthCheck
	results map[string]ServiceStatus
	// kinds records the liveness/readiness classification of each check so the
	// liveness and readiness rollups can select the relevant subset. Checks
	// registered via RegisterCheck default to KindReadiness (the zero value).
	kinds map[string]CheckKind
}

// NewAggregator creates a new Aggregator.
func NewAggregator() *Aggregator {
	return &Aggregator{
		checks:  make(map[string]HealthCheck),
		results: make(map[string]ServiceStatus),
		kinds:   make(map[string]CheckKind),
	}
}

// RegisterCheck registers a named health check. The check defaults to
// KindReadiness; use RegisterCheckKind to classify it for the liveness rollup.
func (a *Aggregator) RegisterCheck(name string, check HealthCheck) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.checks[name] = check
	a.kinds[name] = KindReadiness
}

// UnregisterCheck removes a named health check.
func (a *Aggregator) UnregisterCheck(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.checks, name)
	delete(a.results, name)
	delete(a.kinds, name)
}

// RunChecks runs all registered checks concurrently and stores the results.
func (a *Aggregator) RunChecks(ctx context.Context) {
	a.mu.RLock()
	checks := make(map[string]HealthCheck, len(a.checks))
	for n, c := range a.checks {
		checks[n] = c
	}
	a.mu.RUnlock()

	var wg sync.WaitGroup
	results := make(map[string]ServiceStatus, len(checks))
	var resMu sync.Mutex

	for name, check := range checks {
		wg.Add(1)
		go func(n string, hc HealthCheck) {
			defer wg.Done()
			status, err := hc.Check(ctx)
			resMu.Lock()
			results[n] = ServiceStatus{Name: n, Status: status, Error: err}
			resMu.Unlock()
		}(name, check)
	}

	wg.Wait()

	a.mu.Lock()
	a.results = results
	a.mu.Unlock()
}

// GetStatus returns the overall cluster health based on the latest check results.
// Priority: Critical > Degraded > Unknown > Healthy.
func (a *Aggregator) GetStatus() Status {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.getStatusLocked()
}

// GetServiceStatus returns the status for a specific service.
func (a *Aggregator) GetServiceStatus(name string) (ServiceStatus, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	s, ok := a.results[name]
	return s, ok
}

// GetAllStatuses returns a copy of all latest service statuses.
func (a *Aggregator) GetAllStatuses() map[string]ServiceStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()
	copy := make(map[string]ServiceStatus, len(a.results))
	for k, v := range a.results {
		copy[k] = v
	}
	return copy
}

// StatusWithDetails returns the overall status along with a details map.
func (a *Aggregator) StatusWithDetails() (Status, map[string]string) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	details := make(map[string]string, len(a.results))
	for name, r := range a.results {
		msg := r.Status.String()
		if r.Error != nil {
			msg = fmt.Sprintf("%s: %v", r.Status.String(), r.Error)
		}
		details[name] = msg
	}
	return a.getStatusLocked(), details
}

// getStatusLocked returns the overall status assuming the read lock is held.
func (a *Aggregator) getStatusLocked() Status {
	if len(a.results) == 0 {
		return Unknown
	}

	overall := Healthy
	for _, r := range a.results {
		switch r.Status {
		case Critical:
			return Critical
		case Degraded:
			overall = Degraded
		case Unknown:
			if overall == Healthy {
				overall = Unknown
			}
		case Healthy:
			// keep current overall (Healthy is the baseline)
		default:
			// An unrecognised/malformed status must NEVER fold to Healthy
			// (fail-open). Downgrade the baseline to Unknown.
			if overall == Healthy {
				overall = Unknown
			}
		}
	}
	return overall
}
