// Package federation provides cross-cell coordination primitives for the
// HelixCluster control plane, including suspicion propagation via an injected
// gateway relay.
package federation

import (
	"context"
	"fmt"
	"sync"

	"github.com/HelixDevelopment/helix_cluster/pkg/swim"
)

// MemberStatus is an alias for swim.MemberState so callers in the federation
// package can reference the SWIM state enum without importing pkg/swim directly.
// It does NOT redefine new constants — the canonical values (StateAlive,
// StateSuspect, StateDead, StateLeft) come from pkg/swim.
type MemberStatus = swim.MemberState

// SuspicionEvent carries the cross-cell suspicion signal for a single member.
// All fields are mandatory; the zero value is intentionally invalid (empty
// CellID and MemberID are rejected by CrossCellSuspicion.Observe).
type SuspicionEvent struct {
	// CellID is the cell that originated the observation.
	CellID string
	// MemberID is the member being observed.
	MemberID string
	// State is the observed SWIM state (Alive / Suspect / Dead / Left).
	State swim.MemberState
	// Incarnation is the SWIM Lamport clock value for conflict resolution.
	Incarnation uint32
}

// GatewayRelay is the injectable cross-cell transport seam.  The real
// implementation issues a signed RPC to a remote cell's gateway endpoint; the
// test double records every forwarded event in memory.
//
// HONEST EXTERNAL SEAM: the actual wire protocol (mTLS, retries, back-pressure)
// lives behind this interface and is deliberately NOT implemented in this
// package, so the federation core remains stdlib-only and deterministically
// testable.
//
// Forward must be called exactly once per accepted (changed) SuspicionEvent
// and MUST NOT be called for no-op observations.
type GatewayRelay interface {
	// Forward transmits ev to the remote cell gateway.  Implementations must
	// honour ctx cancellation.  A non-nil error means the event was NOT
	// delivered; the caller should surface this upstream for retry / alerting.
	Forward(ctx context.Context, ev SuspicionEvent) error
}

// memberKey is the composite key used to look up a member inside a cell.
type memberKey struct {
	cellID   string
	memberID string
}

// memberEntry holds the last-accepted state for one (cell, member) pair.
type memberEntry struct {
	state       swim.MemberState
	incarnation uint32
}

// CrossCellSuspicion tracks the SWIM health of members across cell boundaries
// and propagates accepted state changes via an injected GatewayRelay.
//
// Incarnation / state semantics are deliberately close to, but not identical
// to, pkg/swim.Member.UpdateState:
//
//   - Strictly higher incarnation always wins (a higher-incarnation Alive
//     refutes a prior Suspect or Dead).
//   - Equal incarnation: state may only strictly advance Alive→Suspect→Dead→Left,
//     never regress or stay the same.  Unlike swim.Member.UpdateState (which
//     accepts and overwrites on an equal-state same-incarnation re-observation),
//     CrossCellSuspicion treats a same-state same-incarnation re-observation as
//     a no-op and does NOT forward it to the relay.  This keeps relay.Forward
//     idempotent: each distinct (cell, member, incarnation, state) tuple is
//     forwarded at most once.
//   - Lower incarnation: ignored (no-op, changed=false).
//
// relay.Forward is called exactly once per accepted change and never on a
// no-op.
type CrossCellSuspicion struct {
	mu        sync.Mutex
	localCell string
	relay     GatewayRelay
	members   map[memberKey]memberEntry
}

// NewCrossCellSuspicion constructs a CrossCellSuspicion for localCell that
// propagates accepted events via relay.  Both arguments must be non-nil.
func NewCrossCellSuspicion(localCell string, relay GatewayRelay) *CrossCellSuspicion {
	if localCell == "" {
		panic("federation: NewCrossCellSuspicion: localCell must not be empty")
	}
	if relay == nil {
		panic("federation: NewCrossCellSuspicion: relay must not be nil")
	}
	return &CrossCellSuspicion{
		localCell: localCell,
		relay:     relay,
		members:   make(map[memberKey]memberEntry),
	}
}

// Observe applies ev to the local suspicion table using monotonic
// incarnation/state semantics and, on a genuine state change, forwards the
// event via the injected GatewayRelay exactly once.
//
// Returns (true, nil) when the event was accepted and forwarded.
// Returns (false, nil) when the event is a no-op (stale incarnation or
// equal-incarnation state regression).
// Returns (false, err) when the event was accepted but relay.Forward failed;
// the local state IS updated in this case — the error is transport-only.
//
// Mutation anchor (incarnation compare):
//
//	Removing "incarnation < entry.incarnation → return false" causes
//	TestStaleAliveDoesNotRefute to fail because the stale Alive would be
//	accepted and overwrite the newer Suspect.
func (c *CrossCellSuspicion) Observe(ctx context.Context, ev SuspicionEvent) (changed bool, err error) {
	if ev.CellID == "" || ev.MemberID == "" {
		return false, fmt.Errorf("federation: SuspicionEvent: CellID and MemberID must not be empty")
	}

	c.mu.Lock()
	key := memberKey{cellID: ev.CellID, memberID: ev.MemberID}
	entry, known := c.members[key]

	if known {
		// Lower incarnation: stale — ignore.
		if ev.Incarnation < entry.incarnation {
			c.mu.Unlock()
			return false, nil
		}
		// Equal incarnation: state may only advance (never regress).
		// swim states are ordered: StateAlive(0) < StateSuspect(1) < StateDead(2) < StateLeft(3)
		if ev.Incarnation == entry.incarnation && ev.State <= entry.state {
			c.mu.Unlock()
			return false, nil
		}
	}

	// Accept the update.
	c.members[key] = memberEntry{state: ev.State, incarnation: ev.Incarnation}
	c.mu.Unlock()

	// Forward exactly once per accepted change, outside the lock to avoid
	// holding the mutex across a network call.
	if fwdErr := c.relay.Forward(ctx, ev); fwdErr != nil {
		return true, fwdErr
	}
	return true, nil
}

// State returns the last-accepted (state, incarnation) for the given
// (cellID, memberID) pair.  known is false when the pair has never been
// observed.
func (c *CrossCellSuspicion) State(cellID, memberID string) (state swim.MemberState, incarnation uint32, known bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.members[memberKey{cellID: cellID, memberID: memberID}]
	if !ok {
		return 0, 0, false
	}
	return e.state, e.incarnation, true
}
