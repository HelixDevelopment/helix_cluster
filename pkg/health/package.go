// Package health provides health checking for Helix Cluster OS.
package health

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

// Status represents the health status.
type Status string

const (
	Healthy   Status = "healthy"
	Unhealthy Status = "unhealthy"
	Degraded  Status = "degraded"
)

// Checker performs health checks.
type Checker struct {
	mu     sync.RWMutex
	status Status
}

// NewChecker creates a new Checker.
func NewChecker() *Checker {
	return &Checker{status: Healthy}
}

// SetStatus updates the health status.
func (c *Checker) SetStatus(s Status) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = s
}

// GetStatus returns the current health status.
func (c *Checker) GetStatus() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status
}

// CheckFunc is a function that performs a health check and returns a status.
type CheckFunc func() Status

// CheckResult holds the result of a single named check.
type CheckResult struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
}

// CompositeChecker aggregates multiple health checks.
type CompositeChecker struct {
	mu     sync.RWMutex
	checks map[string]CheckFunc
}

// NewCompositeChecker creates a new CompositeChecker.
func NewCompositeChecker() *CompositeChecker {
	return &CompositeChecker{checks: make(map[string]CheckFunc)}
}

// AddCheck registers a named check function.
func (cc *CompositeChecker) AddCheck(name string, fn CheckFunc) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	cc.checks[name] = fn
}

// RemoveCheck removes a named check function.
func (cc *CompositeChecker) RemoveCheck(name string) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	delete(cc.checks, name)
}

// Check runs all registered checks and returns the aggregated status along with
// individual results. The overall status is:
//   - Unhealthy if any check is Unhealthy
//   - Degraded if any check is Degraded and none are Unhealthy
//   - Healthy otherwise
func (cc *CompositeChecker) Check() (Status, []CheckResult) {
	cc.mu.RLock()
	defer cc.mu.RUnlock()

	results := make([]CheckResult, 0, len(cc.checks))
	overall := Healthy

	for name, fn := range cc.checks {
		status := fn()
		results = append(results, CheckResult{Name: name, Status: status})
		if status == Unhealthy {
			overall = Unhealthy
		} else if status == Degraded && overall != Unhealthy {
			overall = Degraded
		}
	}
	return overall, results
}

// response is the JSON shape returned by the HTTP handler.
type response struct {
	Status  Status        `json:"status"`
	Checks  []CheckResult `json:"checks,omitempty"`
	Message string        `json:"message,omitempty"`
}

// Handler returns an http.Handler that reports the status of a Checker.
func Handler(c *Checker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := c.GetStatus()
		code := http.StatusOK
		if status == Unhealthy {
			code = http.StatusServiceUnavailable
		} else if status == Degraded {
			code = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		resp := response{Status: status}
		if status != Healthy {
			resp.Message = fmt.Sprintf("service is %s", status)
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
}

// CompositeHandler returns an http.Handler that reports the status of a CompositeChecker.
func CompositeHandler(cc *CompositeChecker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status, checks := cc.Check()
		code := http.StatusOK
		if status == Unhealthy {
			code = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		resp := response{Status: status, Checks: checks}
		if status != Healthy {
			resp.Message = fmt.Sprintf("service is %s", status)
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
}
