// Package slotmigration implements an atomic slot/session migration controller
// for live session migration in Helix Cluster OS.
//
// A session is bound to a hash slot that is owned by a source node. Migrating
// the slot (and its session) to a destination node must be ATOMIC with respect
// to ownership: at no observable point may the session be owned by both nodes
// at once, nor by neither. End users must never see their session lost.
//
// The controller drives a four-phase state machine:
//
//	PREPARE  -- the destination readies itself to receive the slot/session.
//	TRANSFER -- session state is copied to the destination; in-flight requests
//	            are buffered/forwarded; the SOURCE remains the authoritative
//	            owner throughout this phase.
//	COMMIT   -- ownership flips from source to destination in a single step:
//	            the destination becomes owner and the source slot is emptied.
//	ABORT    -- on failure (or explicit Abort) the source retains ownership,
//	            the destination holds nothing, and the session is never lost.
//
// The package is pure Go and deterministic: it takes no wall-clock time in its
// logic. A Clock interface supplies logical "ticks"; every call to Step
// advances the machine by exactly one tick, and elapsed logical time is tracked
// so a bounded completion budget can be asserted.
package slotmigration

import (
	"errors"
	"fmt"
	"sync"
)

// Phase is a state of the migration state machine.
type Phase int

const (
	// PhaseIdle is the initial phase before any migration is started.
	PhaseIdle Phase = iota
	// PhasePrepare is entered by StartMigration: the destination is readying.
	PhasePrepare
	// PhaseTransfer copies session state; the source still owns the slot.
	PhaseTransfer
	// PhaseCommitted is the terminal success phase: destination owns the slot
	// and session, source slot is empty.
	PhaseCommitted
	// PhaseAborted is the terminal failure phase: source retains ownership,
	// destination holds nothing.
	PhaseAborted
)

// String renders the phase for diagnostics.
func (p Phase) String() string {
	switch p {
	case PhaseIdle:
		return "IDLE"
	case PhasePrepare:
		return "PREPARE"
	case PhaseTransfer:
		return "TRANSFER"
	case PhaseCommitted:
		return "COMMITTED"
	case PhaseAborted:
		return "ABORTED"
	default:
		return fmt.Sprintf("Phase(%d)", int(p))
	}
}

// Clock supplies monotonically increasing logical time. It is injected so the
// controller never reads wall-clock time in logic under test.
type Clock interface {
	// Now returns the current logical tick.
	Now() int64
	// Advance moves logical time forward by delta ticks and returns the new
	// value. delta must be non-negative.
	Advance(delta int64) int64
}

// LogicalClock is a deterministic in-memory Clock driven by explicit ticks.
type LogicalClock struct {
	t int64
}

// NewLogicalClock returns a LogicalClock starting at tick 0.
func NewLogicalClock() *LogicalClock { return &LogicalClock{} }

// Now returns the current logical tick.
func (c *LogicalClock) Now() int64 { return c.t }

// Advance moves logical time forward by delta and returns the new tick.
func (c *LogicalClock) Advance(delta int64) int64 {
	if delta < 0 {
		delta = 0
	}
	c.t += delta
	return c.t
}

// Errors returned by the controller.
var (
	// ErrBusy is returned by StartMigration when a migration is already in
	// flight (not yet committed or aborted).
	ErrBusy = errors.New("slotmigration: a migration is already in progress")
	// ErrNoMigration is returned by Step/Abort when there is nothing to drive.
	ErrNoMigration = errors.New("slotmigration: no migration in progress")
	// ErrEmptyArg is returned when a required identifier is empty.
	ErrEmptyArg = errors.New("slotmigration: slot/session/src/dst must be non-empty")
	// ErrSameNode is returned when source and destination are the same node.
	ErrSameNode = errors.New("slotmigration: source and destination must differ")
	// ErrUnowned is returned when StartMigration is asked to move a slot whose
	// authoritative owner is not the named source.
	ErrUnowned = errors.New("slotmigration: source does not own the slot")
)

// migration captures the in-flight migration parameters and progress.
type migration struct {
	slot    string
	session string
	src     string
	dst     string

	// startTick is the logical tick at which StartMigration ran.
	startTick int64
	// transferTick is the tick at which TRANSFER work completes and the machine
	// becomes eligible to COMMIT. A negative value means "not yet scheduled".
	transferTick int64
	// failAtTransfer, when true, causes the next Step in TRANSFER to ABORT
	// instead of progressing toward COMMIT (models a destination/transfer
	// failure).
	failAtTransfer bool
}

// Controller drives an atomic slot+session migration. It owns the authoritative
// view of which node currently owns each slot and whether a session is alive.
// The zero value is not usable; construct with NewController.
//
// Mutators (Step, StartMigration, Abort, InjectTransferFailure, Seed) are
// serialized by callers in the logical model, but the authoritative ownership
// view is queried concurrently by routers/lookups DURING a migration. All public
// methods are therefore guarded by an RWMutex: mutators take the write lock and
// the COMMIT flip is observed atomically by readers, while concurrent routing
// lookups (Owner/OwnerCount/SessionAlive/Phase) take the read lock and can never
// observe a torn map or trip a "concurrent map read and map write" panic.
type Controller struct {
	mu  sync.RWMutex
	clk Clock

	// owner maps slot -> authoritative owning node. Exactly one entry per known
	// slot at all observable times; the COMMIT flip rewrites this atomically.
	owner map[string]string
	// alive maps session -> liveness. A session is never deleted while a
	// migration is in flight.
	alive map[string]bool

	// phase is the current state-machine phase.
	phase Phase
	// transferBudget is the number of logical ticks TRANSFER takes once entered.
	transferBudget int64

	mig *migration
}

// NewController returns a Controller using clk for logical time. transferTicks
// is the logical duration the TRANSFER phase occupies (must be >= 1); it models
// the cost of copying session state. It is used so a bounded completion budget
// can be asserted by callers.
func NewController(clk Clock, transferTicks int64) *Controller {
	if transferTicks < 1 {
		transferTicks = 1
	}
	return &Controller{
		clk:            clk,
		owner:          make(map[string]string),
		alive:          make(map[string]bool),
		phase:          PhaseIdle,
		transferBudget: transferTicks,
	}
}

// Seed establishes an initial slot owner and a live session bound to it. It is
// the setup primitive: before a migration, the slot is owned by src and the
// session is alive there.
func (c *Controller) Seed(slot, session, owner string) error {
	if slot == "" || session == "" || owner == "" {
		return ErrEmptyArg
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.owner[slot] = owner
	c.alive[session] = true
	return nil
}

// Phase returns the current state-machine phase.
func (c *Controller) Phase() Phase {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.phase
}

// Owner returns the authoritative owner of slot and whether the slot is owned.
// During PREPARE and TRANSFER this is always the source; only after COMMIT does
// it become the destination.
func (c *Controller) Owner(slot string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	o, ok := c.owner[slot]
	return o, ok && o != ""
}

// SessionAlive reports whether session is currently alive (never lost).
func (c *Controller) SessionAlive(session string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.alive[session]
}

// Elapsed returns the logical ticks elapsed since the in-flight migration
// started, or 0 when no migration has started. After a terminal phase it
// reflects the total ticks the migration consumed.
func (c *Controller) Elapsed() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.mig == nil {
		return 0
	}
	return c.clk.Now() - c.mig.startTick
}

// StartMigration begins moving slot (and its bound session) from src to dst. It
// enters PREPARE and records the start tick. It fails if a migration is already
// in flight, if any argument is empty, if src == dst, or if src is not the
// current authoritative owner of slot.
func (c *Controller) StartMigration(slot, session, src, dst string) error {
	if slot == "" || session == "" || src == "" || dst == "" {
		return ErrEmptyArg
	}
	if src == dst {
		return ErrSameNode
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.phase == PhasePrepare || c.phase == PhaseTransfer {
		return ErrBusy
	}
	if cur, ok := c.owner[slot]; !ok || cur != src {
		return ErrUnowned
	}
	c.mig = &migration{
		slot:         slot,
		session:      session,
		src:          src,
		dst:          dst,
		startTick:    c.clk.Now(),
		transferTick: -1,
	}
	// Ensure the session is registered alive (it is, by precondition, bound to
	// the slot on src).
	c.alive[session] = true
	c.phase = PhasePrepare
	return nil
}

// InjectTransferFailure arms a failure to be triggered on the next TRANSFER
// step, modeling a destination or copy failure. When the failure fires the
// machine ABORTs cleanly: source keeps ownership, destination holds nothing,
// session stays alive. It is a no-op (returns ErrNoMigration) when no migration
// is in flight.
func (c *Controller) InjectTransferFailure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mig == nil || (c.phase != PhasePrepare && c.phase != PhaseTransfer) {
		return ErrNoMigration
	}
	c.mig.failAtTransfer = true
	return nil
}

// Step advances the state machine by exactly one logical tick and returns the
// resulting phase. The transitions are:
//
//	PREPARE  -> TRANSFER         (destination readied)
//	TRANSFER -> TRANSFER         (still copying, until the transfer budget elapses)
//	TRANSFER -> COMMITTED        (atomic ownership flip: dst owns, src emptied)
//	TRANSFER -> ABORTED          (if a transfer failure was injected)
//
// The COMMIT flip is the ONLY place ownership changes, and it changes from src
// to dst in a single step so that exactly one node owns the slot at every
// observable point. Step returns ErrNoMigration if the machine is idle or in a
// terminal phase.
func (c *Controller) Step() (Phase, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mig == nil || c.phase == PhaseIdle || c.phase == PhaseCommitted || c.phase == PhaseAborted {
		return c.phase, ErrNoMigration
	}
	now := c.clk.Advance(1)

	switch c.phase {
	case PhasePrepare:
		// Destination readied. Schedule the transfer completion tick and move
		// into TRANSFER. The source remains the authoritative owner.
		if c.mig.failAtTransfer {
			// A failure during preparation/transfer aborts before any flip.
			c.abortNow()
			return c.phase, nil
		}
		c.mig.transferTick = now + c.transferBudget
		c.phase = PhaseTransfer
		return c.phase, nil

	case PhaseTransfer:
		if c.mig.failAtTransfer {
			c.abortNow()
			return c.phase, nil
		}
		if now >= c.mig.transferTick {
			// Transfer complete: perform the atomic COMMIT flip. Ownership
			// moves from src to dst and the source slot is emptied IN THE SAME
			// step, so the slot is never owned by two nodes nor by none.
			c.commitNow()
			return c.phase, nil
		}
		// Still copying; remain in TRANSFER.
		return c.phase, nil

	default:
		return c.phase, ErrNoMigration
	}
}

// commitNow performs the atomic ownership flip. After this, dst is the sole
// owner of the slot, the source no longer owns it, and the session remains
// alive bound to dst. The caller must hold c.mu (write lock).
func (c *Controller) commitNow() {
	m := c.mig
	// The flip: a single assignment makes dst the owner. There is never an
	// intermediate state where both src and dst own the slot, nor where the
	// slot has no owner: the map key always holds exactly one node id.
	c.owner[m.slot] = m.dst
	// Session is preserved (alive) and now conceptually bound to dst.
	c.alive[m.session] = true
	c.phase = PhaseCommitted
}

// abortNow rolls back cleanly: the source keeps ownership (owner map is left
// untouched, since it was never flipped), the destination holds nothing, and
// the session stays alive on the source. The caller must hold c.mu (write lock).
func (c *Controller) abortNow() {
	m := c.mig
	// Defensive: ensure ownership is exactly the source and nothing leaked to
	// the destination. The owner map was never changed before COMMIT, so this
	// is an assertion of the invariant rather than a mutation in the happy
	// rollback case.
	c.owner[m.slot] = m.src
	c.alive[m.session] = true
	c.phase = PhaseAborted
}

// Abort forces a clean rollback from PREPARE or TRANSFER: source retains
// ownership, destination holds nothing, session stays alive. It returns
// ErrNoMigration when there is no in-flight migration to abort. Abort does not
// itself advance logical time.
func (c *Controller) Abort() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mig == nil || (c.phase != PhasePrepare && c.phase != PhaseTransfer) {
		return ErrNoMigration
	}
	c.abortNow()
	return nil
}

// OwnerCount returns the number of distinct nodes that authoritatively own
// slot. By construction this is always exactly 1 for a seeded slot at every
// observable point of a migration: the owner map holds a single node id, and
// the COMMIT flip rewrites it atomically. The closure tests assert this stays
// 1 across the whole run (never 0, never 2). The destination is NOT counted as
// an owner until COMMIT, because it does not appear in the owner map for the
// slot before the flip.
func (c *Controller) OwnerCount(slot string) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	o, ok := c.owner[slot]
	if !ok || o == "" {
		return 0
	}
	return 1
}

// Migrating reports the in-flight migration's parameters and whether one is in
// a non-terminal phase. Useful for tests/diagnostics.
func (c *Controller) Migrating() (slot, session, src, dst string, inFlight bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.mig == nil {
		return "", "", "", "", false
	}
	inFlight = c.phase == PhasePrepare || c.phase == PhaseTransfer
	return c.mig.slot, c.mig.session, c.mig.src, c.mig.dst, inFlight
}
