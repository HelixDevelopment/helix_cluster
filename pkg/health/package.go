// Package health provides health checking for Helix Cluster OS.
package health

import "sync"

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

// Status returns the current health status.
func (c *Checker) GetStatus() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status
}
