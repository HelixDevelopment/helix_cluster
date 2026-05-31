package swim

import (
	"sync"
	"time"
)

// MemberState represents the state of a cluster member.
type MemberState int

const (
	StateAlive MemberState = iota
	StateSuspect
	StateDead
	StateLeft
)

// Member represents a node in the cluster.
type Member struct {
	ID          string
	Address     string
	State       MemberState
	Incarnation uint32 // Lamport clock for conflict resolution
	Metadata    map[string]string

	mu       sync.RWMutex
	lastSeen time.Time
}

// UpdateState transitions member state with incarnation check.
func (m *Member) UpdateState(state MemberState, incarnation uint32) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if incarnation < m.Incarnation {
		return false
	}
	if incarnation == m.Incarnation && state < m.State {
		return false
	}

	m.State = state
	m.Incarnation = incarnation
	m.lastSeen = time.Now()
	return true
}

// ClearSuspect atomically transitions the member from Suspect back to Alive
// (e.g. upon receiving an ack) under the member's own lock, and reports whether
// a transition occurred. Using this instead of a bare State write keeps all
// State mutations serialized on m.mu, avoiding races with UpdateState.
func (m *Member) ClearSuspect() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.State == StateSuspect {
		m.State = StateAlive
		m.lastSeen = time.Now()
		return true
	}
	return false
}

// IsHealthy returns true if member is alive.
func (m *Member) IsHealthy() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.State == StateAlive
}

// TimeSinceLastSeen returns duration since last heartbeat.
func (m *Member) TimeSinceLastSeen() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return time.Since(m.lastSeen)
}

// LastSeen returns the timestamp of the most recent observation of this member.
func (m *Member) LastSeen() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastSeen
}

// Touch updates the last seen timestamp.
func (m *Member) Touch() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastSeen = time.Now()
}
