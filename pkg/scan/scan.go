// Package scan implements an Oracle-SCAN-style stable virtual client endpoint
// that load-balances connections across a dynamic set of backend nodes.
//
// The virtual endpoint name is fixed at construction time and never changes
// even as nodes are added or removed (EndpointName invariant). Route selects
// the least-loaded HEALTHY node, breaking ties deterministically by node id.
package scan

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// ErrNoHealthyBackend is returned by Route when no healthy backend node exists.
var ErrNoHealthyBackend = errors.New("scan: no healthy backend available")

// RoutingRecord captures a single routing decision.
type RoutingRecord struct {
	RunID     string
	NodeID    string
	Timestamp time.Time
}

// Backend represents a single backend node.
type Backend struct {
	ID      string
	Load    int
	Healthy bool
}

// Registry is an Oracle-SCAN-style stable virtual endpoint.
// The endpoint name is set once at construction and is immutable.
type Registry struct {
	mu           sync.RWMutex
	endpointName string
	backends     map[string]Backend
	history      []RoutingRecord
	now          func() time.Time // injected clock; never uses real wall clock
}

// New creates a new Registry with the given stable endpoint name and clock function.
// The now function is injected so that tests can control time without touching
// wall-clock state; callers that want real time pass time.Now.
func New(endpointName string, now func() time.Time) *Registry {
	return &Registry{
		endpointName: endpointName,
		backends:     make(map[string]Backend),
		now:          now,
	}
}

// EndpointName returns the stable virtual name.
// It NEVER changes regardless of backend membership mutations.
func (r *Registry) EndpointName() string {
	// No lock needed: endpointName is written once at construction.
	return r.endpointName
}

// AddBackend inserts or replaces a backend node.
func (r *Registry) AddBackend(b Backend) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.backends[b.ID] = b
}

// RemoveBackend removes the backend with the given id.
// Removing a non-existent id is a no-op.
func (r *Registry) RemoveBackend(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.backends, id)
}

// Route returns the node id of the least-loaded HEALTHY backend.
// Ties in Load are broken deterministically by ascending node id.
// The routing decision is recorded with the provided runID.
//
// MUTATION GUARD — this comparison is pinned by TestRoutesToLeastLoaded:
// inverting the comparison to select the MOST loaded node makes that test fail.
func (r *Registry) Route(runID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Collect all healthy backends into a deterministically ordered slice.
	var healthy []Backend
	for _, b := range r.backends {
		if b.Healthy {
			healthy = append(healthy, b)
		}
	}
	if len(healthy) == 0 {
		return "", ErrNoHealthyBackend
	}

	// Sort: primary key = Load ascending, secondary key = ID ascending.
	// MUTATION GUARD (TestRoutesToLeastLoaded): the first comparison below
	// selects the LEAST loaded node. Inverting to `>` selects the most loaded.
	sort.Slice(healthy, func(i, j int) bool {
		if healthy[i].Load != healthy[j].Load {
			return healthy[i].Load < healthy[j].Load // ← MUTATION GUARD
		}
		return healthy[i].ID < healthy[j].ID
	})

	chosen := healthy[0].ID
	r.history = append(r.history, RoutingRecord{
		RunID:     runID,
		NodeID:    chosen,
		Timestamp: r.now(),
	})
	return chosen, nil
}

// History returns a snapshot of all routing records in insertion order.
func (r *Registry) History() []RoutingRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]RoutingRecord, len(r.history))
	copy(out, r.history)
	return out
}

// SetBackendLoad updates the Load field for an existing backend.
// It is a convenience method for tests and callers that want to change load
// without rebuilding the entire Backend struct.
func (r *Registry) SetBackendLoad(id string, load int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.backends[id]
	if !ok {
		return fmt.Errorf("scan: backend %q not found", id)
	}
	b.Load = load
	r.backends[id] = b
	return nil
}

// SetBackendHealth updates the Healthy field for an existing backend.
func (r *Registry) SetBackendHealth(id string, healthy bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.backends[id]
	if !ok {
		return fmt.Errorf("scan: backend %q not found", id)
	}
	b.Healthy = healthy
	r.backends[id] = b
	return nil
}
