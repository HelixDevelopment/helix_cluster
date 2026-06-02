// Package healthmonitor provides a deterministic, pure-Go HealthMonitor that
// periodically checks a set of providers, detects healthy<->unhealthy
// transitions on an edge-triggered basis, fires auto-failover / recovery
// callbacks, and records a transition-event log.
//
// All evaluation is driven by explicit Check(now) calls so behavior is fully
// deterministic in tests: no real timers, no time.Now() in logic, no network,
// no host/GPU access. A DefaultInterval constant is exposed for callers that
// want to drive Check on a 30s cadence, but the Monitor itself never sleeps.
package healthmonitor

import (
	"sync"
	"time"
)

// DefaultInterval is the recommended cadence at which a caller should drive
// Check. The Monitor does NOT use this internally — tests and callers drive
// Check(now) explicitly so behavior stays deterministic.
const DefaultInterval = 30 * time.Second

// HealthCheckable is anything the Monitor can probe. A nil CheckHealth() error
// means the provider is healthy; a non-nil error means it is unhealthy.
type HealthCheckable interface {
	// ID returns a stable identifier for the provider (e.g. a GPU device id).
	ID() string
	// CheckHealth returns nil when healthy, or the failure reason otherwise.
	CheckHealth() error
}

// EventKind classifies a recorded transition event.
type EventKind int

const (
	// EventUnhealthy is recorded on a healthy->unhealthy transition.
	EventUnhealthy EventKind = iota
	// EventRecovered is recorded on an unhealthy->healthy transition.
	EventRecovered
)

func (k EventKind) String() string {
	switch k {
	case EventUnhealthy:
		return "unhealthy"
	case EventRecovered:
		return "recovered"
	default:
		return "unknown"
	}
}

// Event is a single recorded health-state transition for one provider.
type Event struct {
	Kind       EventKind
	ProviderID string
	// Message is a human-readable description of the transition.
	Message string
	// Err is the CheckHealth error that caused an EventUnhealthy transition.
	// It is nil for EventRecovered.
	Err error
	// At is the tick (now) value passed to Check when the transition fired.
	At time.Time
}

// Monitor watches a set of HealthCheckable providers and edge-triggers
// failover / recovery callbacks on health-state transitions.
//
// A Monitor is safe for concurrent use.
type Monitor struct {
	mu sync.Mutex

	providers []HealthCheckable
	// healthy tracks the last-known health state per provider id. A provider
	// is seeded as healthy on first registration so the very first failing
	// Check produces exactly one healthy->unhealthy transition.
	healthy map[string]bool

	events []Event

	onUnhealthy func(Event)
	onRecovered func(Event)
}

// New creates a Monitor over the given providers. Every provider is seeded as
// healthy so the first failing Check edge-triggers exactly one unhealthy
// transition (and not a spurious recovery).
func New(providers ...HealthCheckable) *Monitor {
	m := &Monitor{
		healthy: make(map[string]bool, len(providers)),
	}
	for _, p := range providers {
		m.providers = append(m.providers, p)
		m.healthy[p.ID()] = true
	}
	return m
}

// OnUnhealthy registers the auto-failover callback fired exactly once per
// healthy->unhealthy transition.
func (m *Monitor) OnUnhealthy(fn func(Event)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onUnhealthy = fn
}

// OnRecovered registers the callback fired exactly once per unhealthy->healthy
// transition.
func (m *Monitor) OnRecovered(fn func(Event)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onRecovered = fn
}

// IsHealthy reports the last-known health state of the provider id as of the
// most recent Check. Unknown ids report false.
func (m *Monitor) IsHealthy(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.healthy[id]
}

// Events returns a copy of the recorded transition-event log in order.
func (m *Monitor) Events() []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Event, len(m.events))
	copy(out, m.events)
	return out
}

// Check evaluates every provider exactly once at tick `now`. It is
// edge-triggered: a callback/event is produced ONLY when a provider's health
// state changes relative to the previous Check. A provider that stays in the
// same state across many Checks produces zero events and zero callbacks.
//
// It returns the events produced by this Check (in provider order), which is
// empty when nothing transitioned.
func (m *Monitor) Check(now time.Time) []Event {
	// Collect work under lock, but invoke user callbacks AFTER releasing the
	// lock so a callback that re-enters the Monitor cannot deadlock.
	m.mu.Lock()

	var fired []Event
	var dispatch []struct {
		ev EventKind
		e  Event
	}

	for _, p := range m.providers {
		id := p.ID()
		err := p.CheckHealth()
		nowHealthy := err == nil
		prevHealthy := m.healthy[id]

		if nowHealthy == prevHealthy {
			continue // no edge -> no event (this is what makes it edge-triggered)
		}

		// State changed. Record the transition and queue the callback.
		m.healthy[id] = nowHealthy

		var e Event
		if nowHealthy {
			e = Event{
				Kind:       EventRecovered,
				ProviderID: id,
				Message:    "GPU device recovered",
				Err:        nil,
				At:         now,
			}
		} else {
			e = Event{
				Kind:       EventUnhealthy,
				ProviderID: id,
				Message:    "GPU device went unhealthy",
				Err:        err,
				At:         now,
			}
		}
		m.events = append(m.events, e)
		fired = append(fired, e)
		dispatch = append(dispatch, struct {
			ev EventKind
			e  Event
		}{e.Kind, e})
	}

	onUnhealthy := m.onUnhealthy
	onRecovered := m.onRecovered
	m.mu.Unlock()

	for _, d := range dispatch {
		switch d.ev {
		case EventUnhealthy:
			if onUnhealthy != nil {
				onUnhealthy(d.e)
			}
		case EventRecovered:
			if onRecovered != nil {
				onRecovered(d.e)
			}
		}
	}

	return fired
}
