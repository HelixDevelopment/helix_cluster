// Package federation provides the multi-cell (multi-cluster) federation core
// for Helix Cluster OS. A "cell" is one autonomous cluster; federation lets a
// control plane treat many cells as one logical fabric.
//
// This package owns three concerns:
//
//   - Cell — an immutable-by-convention descriptor of a federated cluster
//     (id, region, endpoint, health, member-count) plus an explicit lifecycle
//     FSM (Joining -> Active -> Degraded -> Draining -> Left).
//   - CellRegistry — a concurrency-safe directory that registers, deregisters,
//     looks up and advances the lifecycle of cells, emitting transition events.
//   - Aggregator — an "--all-cells" query fan-out helper that asks every
//     registered cell (via the injectable CellClient seam) and merges the
//     per-cell answers into one aggregated view, tolerating partial failure.
//
// The cross-cluster network transport is intentionally OUT OF SCOPE: it sits
// behind the CellClient interface so this core is fully unit-testable with a
// software fake and carries zero third-party dependencies (stdlib only).
package federation

import (
	"errors"
	"fmt"
	"time"
)

// CellPhase is the lifecycle state of a federated cell. The legal transition
// graph is a linear pipeline with a few explicit shortcuts; see legalNext.
type CellPhase int

const (
	// PhaseJoining is the initial phase: the cell has been registered but has
	// not yet completed its handshake / first health report.
	PhaseJoining CellPhase = iota
	// PhaseActive means the cell is healthy and serving federated queries.
	PhaseActive
	// PhaseDegraded means the cell is reachable but unhealthy (partial members,
	// high error rate); it may recover back to Active.
	PhaseDegraded
	// PhaseDraining means the cell is being gracefully removed; it stops taking
	// new work and bleeds off existing work.
	PhaseDraining
	// PhaseLeft is the terminal phase: the cell has departed the federation.
	PhaseLeft
)

// String renders the phase for logs and events.
func (p CellPhase) String() string {
	switch p {
	case PhaseJoining:
		return "Joining"
	case PhaseActive:
		return "Active"
	case PhaseDegraded:
		return "Degraded"
	case PhaseDraining:
		return "Draining"
	case PhaseLeft:
		return "Left"
	default:
		return fmt.Sprintf("CellPhase(%d)", int(p))
	}
}

// Sentinel errors returned by the federation core.
var (
	// ErrIllegalTransition is returned when an FSM transition is not permitted
	// by the lifecycle graph (e.g. Joining -> Draining directly).
	ErrIllegalTransition = errors.New("federation: illegal cell phase transition")
	// ErrCellNotFound is returned by registry lookups for an unknown cell id.
	ErrCellNotFound = errors.New("federation: cell not found")
	// ErrCellExists is returned when registering a cell id that already exists.
	ErrCellExists = errors.New("federation: cell already registered")
	// ErrEmptyCellID is returned when a cell is created/registered with no id.
	ErrEmptyCellID = errors.New("federation: empty cell id")
)

// legalTransitions encodes the lifecycle graph as an adjacency set. A transition
// from a to b is legal iff b is in legalTransitions[a]. Self-transitions are NOT
// listed and are therefore illegal (advancing to the current phase is a no-op
// the caller must avoid); PhaseLeft is terminal (no outgoing edges).
//
// Graph:
//
//	Joining  -> Active, Left
//	Active   -> Degraded, Draining, Left
//	Degraded -> Active, Draining, Left
//	Draining -> Left
//	Left     -> (terminal)
var legalTransitions = map[CellPhase]map[CellPhase]bool{
	PhaseJoining:  {PhaseActive: true, PhaseLeft: true},
	PhaseActive:   {PhaseDegraded: true, PhaseDraining: true, PhaseLeft: true},
	PhaseDegraded: {PhaseActive: true, PhaseDraining: true, PhaseLeft: true},
	PhaseDraining: {PhaseLeft: true},
	PhaseLeft:     {},
}

// LegalTransition reports whether advancing from `from` to `to` is permitted by
// the lifecycle graph. It is exported so callers (and the registry) can validate
// without mutating, and so tests can assert the graph directly.
func LegalTransition(from, to CellPhase) bool {
	return legalTransitions[from][to]
}

// Health is a coarse health classification for a cell, independent of the
// lifecycle phase (a Draining cell can still be Healthy).
type Health int

const (
	// HealthUnknown is the zero value: health has not been reported yet.
	HealthUnknown Health = iota
	// HealthHealthy means all expected members are up and serving.
	HealthHealthy
	// HealthDegraded means some members are down but the cell still serves.
	HealthDegraded
	// HealthUnhealthy means the cell is not serving.
	HealthUnhealthy
)

// String renders the health classification.
func (h Health) String() string {
	switch h {
	case HealthHealthy:
		return "Healthy"
	case HealthDegraded:
		return "Degraded"
	case HealthUnhealthy:
		return "Unhealthy"
	default:
		return "Unknown"
	}
}

// Cell is the descriptor of a single federated cluster. Construct it with
// NewCell; mutate its lifecycle only through Advance so the FSM invariants hold.
//
// Cell is NOT safe for concurrent use on its own; the CellRegistry owns the
// locking discipline for the cells it stores. Code that holds a Cell outside a
// registry must provide its own synchronization.
type Cell struct {
	ID          string
	Region      string
	Endpoint    string
	Health      Health
	MemberCount int

	phase     CellPhase
	updatedAt time.Time
}

// CellOption configures a Cell at construction time.
type CellOption func(*Cell)

// WithRegion sets the cell's region.
func WithRegion(region string) CellOption { return func(c *Cell) { c.Region = region } }

// WithEndpoint sets the cell's transport endpoint.
func WithEndpoint(endpoint string) CellOption { return func(c *Cell) { c.Endpoint = endpoint } }

// WithHealth sets the cell's initial health classification.
func WithHealth(h Health) CellOption { return func(c *Cell) { c.Health = h } }

// WithMemberCount sets the cell's initial member count.
func WithMemberCount(n int) CellOption { return func(c *Cell) { c.MemberCount = n } }

// NewCell builds a Cell in the initial PhaseJoining phase. It returns
// ErrEmptyCellID if id is empty. The returned Cell's updatedAt is set to now.
func NewCell(id string, opts ...CellOption) (*Cell, error) {
	if id == "" {
		return nil, ErrEmptyCellID
	}
	c := &Cell{
		ID:        id,
		Health:    HealthUnknown,
		phase:     PhaseJoining,
		updatedAt: time.Now(),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// Phase returns the cell's current lifecycle phase.
func (c *Cell) Phase() CellPhase { return c.phase }

// UpdatedAt returns the time of the cell's last phase transition (or creation).
func (c *Cell) UpdatedAt() time.Time { return c.updatedAt }

// advance moves the cell to `to`, enforcing the FSM. It returns
// ErrIllegalTransition (without mutating) if the edge is not in the graph. On
// success it records the new phase and bumps updatedAt to `at`.
func (c *Cell) advance(to CellPhase, at time.Time) error {
	if !LegalTransition(c.phase, to) {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, c.phase, to)
	}
	c.phase = to
	c.updatedAt = at
	return nil
}

// clone returns a value copy of the cell so callers can read a snapshot without
// racing on the registry's stored pointer.
func (c *Cell) clone() Cell { return *c }

// Snapshot returns a value copy of the cell, including its current phase, safe
// to read after the registry releases its lock.
func (c *Cell) Snapshot() Cell { return *c }
