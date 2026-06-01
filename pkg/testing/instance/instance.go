package instance

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/testing/device"
)

// ─── State enum ──────────────────────────────────────────────────────────────

// State is a node in the instance lifecycle FSM.
//
// Legal transitions:
//
//	provisioned -> booting
//	booting     -> ready     (only after boot-time has elapsed on the virtual clock)
//	ready       -> stopped
//
// stopped is terminal.
type State int

const (
	// StateProvisioned is the initial state after Provision() succeeds.
	StateProvisioned State = iota
	// StateBooting is entered when Boot() is called on a provisioned instance.
	// The instance transitions to StateReady only after bootTime elapses.
	StateBooting
	// StateReady means the instance has fully booted and is available.
	StateReady
	// StateStopped is the terminal state after Stop() is called on a ready instance.
	StateStopped
)

// String renders State for logs and assertions.
func (s State) String() string {
	switch s {
	case StateProvisioned:
		return "provisioned"
	case StateBooting:
		return "booting"
	case StateReady:
		return "ready"
	case StateStopped:
		return "stopped"
	default:
		return fmt.Sprintf("State(%d)", int(s))
	}
}

// ─── Errors ───────────────────────────────────────────────────────────────────

// ErrIllegalTransition is returned when a caller attempts a state transition
// that is not present in the legal-transitions map.
var ErrIllegalTransition = errors.New("instance: illegal state transition")

// ─── Legal transition map ─────────────────────────────────────────────────────

// legalTransitions encodes valid FSM edges. Transitions absent from this map
// are rejected by Instance.fsm().
var legalTransitions = map[State]map[State]bool{
	StateProvisioned: {StateBooting: true},
	StateBooting:     {StateReady: true},
	StateReady:       {StateStopped: true},
	StateStopped:     {},
}

// ─── TransitionEvent ──────────────────────────────────────────────────────────

// TransitionEvent is a single observed FSM step captured in the ordered trace.
type TransitionEvent struct {
	From  State
	To    State
	At    time.Time
	RunID string
}

// ─── Instance ─────────────────────────────────────────────────────────────────

// Instance is a handle to a provisioned virtual instance. It is safe for
// concurrent Status / Boot / Stop calls.
type Instance struct {
	id       string
	profile  *device.Profile
	clk      *VirtualClock
	bootTime time.Duration // tier-specific required boot duration

	mu         sync.RWMutex
	state      State
	trace      []State        // ordered sequence of states entered (sink-side)
	readyAt    time.Time      // set when booting->ready transition fires
	bootAt     time.Time      // virtual time at which Boot() was called
	rejectSink func(from, to State) // called on every rejected transition (set by provisioner)
}

// newInstance creates an Instance already in StateProvisioned.
func newInstance(id string, p *device.Profile, clk *VirtualClock, bootTime time.Duration) *Instance {
	inst := &Instance{
		id:       id,
		profile:  p,
		clk:      clk,
		bootTime: bootTime,
		state:    StateProvisioned,
		trace:    []State{StateProvisioned},
	}
	return inst
}

// Status returns the current FSM state. If the instance is booting it checks
// whether boot-time has elapsed on the virtual clock and promotes to ready.
func (inst *Instance) Status() State {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	if inst.state == StateBooting {
		now := inst.clk.Now()
		if !now.Before(inst.bootAt.Add(inst.bootTime)) {
			// Boot-time has elapsed — promote to ready.
			inst.readyAt = inst.bootAt.Add(inst.bootTime)
			inst.state = StateReady
			inst.trace = append(inst.trace, StateReady)
		}
	}
	return inst.state
}

// Boot transitions the instance from provisioned to booting.
// It records the virtual-clock time at which booting began so that Status()
// can gate the booting→ready promotion correctly.
// Returns ErrIllegalTransition (wrapped) if called from any other state.
func (inst *Instance) Boot() error {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	err := inst.fsm(StateBooting)
	if err != nil && inst.rejectSink != nil {
		inst.rejectSink(inst.state, StateBooting)
	}
	return err
}

// Stop transitions the instance from ready to stopped (terminal).
// Returns ErrIllegalTransition (wrapped) if called from any other state.
func (inst *Instance) Stop() error {
	// Trigger any pending booting→ready promotion first.
	_ = inst.Status() // promotes under its own lock; we re-lock below
	inst.mu.Lock()
	defer inst.mu.Unlock()
	err := inst.fsm(StateStopped)
	if err != nil && inst.rejectSink != nil {
		inst.rejectSink(inst.state, StateStopped)
	}
	return err
}

// Trace returns an ordered copy of every State that has been entered, starting
// with provisioned.
func (inst *Instance) Trace() []State {
	inst.mu.RLock()
	defer inst.mu.RUnlock()
	out := make([]State, len(inst.trace))
	copy(out, inst.trace)
	return out
}

// ReadyAt returns the virtual timestamp at which the instance became ready.
// The zero value is returned if the instance has not yet reached StateReady.
func (inst *Instance) ReadyAt() time.Time {
	inst.mu.RLock()
	defer inst.mu.RUnlock()
	return inst.readyAt
}

// fsm performs a single state transition. Must be called with inst.mu held.
// It appends to the trace only for legal edges; illegal edges leave the state
// unchanged.
func (inst *Instance) fsm(to State) error {
	from := inst.state
	if !legalTransitions[from][to] {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, from, to)
	}
	if to == StateBooting {
		inst.bootAt = inst.clk.Now()
	}
	inst.state = to
	inst.trace = append(inst.trace, to)
	return nil
}

// ─── Provisioner interface ────────────────────────────────────────────────────

// Provisioner is the lifecycle contract for spinning up a virtual instance.
type Provisioner interface {
	Provision(p *device.Profile) (*Instance, error)
}

// ─── RejectionRecord ─────────────────────────────────────────────────────────

// RejectionRecord captures a refused FSM transition with the run-ID that
// provoked it so tests can assert sink-side that the rejection was observed.
type RejectionRecord struct {
	From  State
	To    State
	At    time.Time
	RunID string
}

// ─── FakeProvisioner ─────────────────────────────────────────────────────────

// bootTimes maps tier → virtual boot duration. These are deterministic values
// defined entirely inside this package so that device.Profile need not be
// modified (avoiding shared-struct pollution as specified in the design).
var bootTimes = map[string]time.Duration{
	"T1": 50 * time.Millisecond,
	"T2": 75 * time.Millisecond,
	"T3": 100 * time.Millisecond,
	"T4": 150 * time.Millisecond,
	"T5": 200 * time.Millisecond,
	"T6": 300 * time.Millisecond,
	"T7": 500 * time.Millisecond,
	"T8": 800 * time.Millisecond,
}

// defaultBootTime is used for any tier not listed above.
const defaultBootTime = 100 * time.Millisecond

// FakeProvisioner is a deterministic, in-process Provisioner suitable for unit
// and integration tests. It uses a VirtualClock so boot-time gating is fully
// under test control. It also records every rejected illegal transition so
// tests can assert the rejection log.
type FakeProvisioner struct {
	clk     *VirtualClock
	mu      sync.Mutex
	seq     int
	runID   string
	rejected []RejectionRecord
}

// NewFakeProvisioner creates a FakeProvisioner backed by clk.
func NewFakeProvisioner(clk *VirtualClock) *FakeProvisioner {
	return &FakeProvisioner{
		clk:   clk,
		runID: "fp-run-HXC1231",
	}
}

// SetRunID overrides the default run-ID embedded in rejection records and
// transition events. Call before Provision if you need a custom UUID.
func (fp *FakeProvisioner) SetRunID(id string) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.runID = id
}

// BootTimeFor returns the virtual boot duration for the given tier.
func (fp *FakeProvisioner) BootTimeFor(tier string) time.Duration {
	if d, ok := bootTimes[tier]; ok {
		return d
	}
	return defaultBootTime
}

// Provision creates an Instance for the given Profile, calls Boot() to move it
// from provisioned to booting, and returns it. The instance transitions to
// ready only once the virtual clock has advanced past bootTime (checked lazily
// in Status()).
func (fp *FakeProvisioner) Provision(p *device.Profile) (*Instance, error) {
	if p == nil {
		return nil, fmt.Errorf("instance: Provision: nil profile")
	}
	fp.mu.Lock()
	fp.seq++
	id := fmt.Sprintf("inst-%04d-%s", fp.seq, p.Tier)
	runID := fp.runID
	fp.mu.Unlock()

	bt := fp.BootTimeFor(p.Tier)
	inst := newInstance(id, p, fp.clk, bt)

	// Boot() transitions provisioned→booting. This cannot fail on a fresh
	// instance but we handle the error defensively.
	if err := inst.Boot(); err != nil {
		return nil, fmt.Errorf("instance: Provision: Boot: %w", err)
	}

	// Wire rejection recording: wrap the instance's Boot/Stop to intercept
	// any future illegal transitions and log them to fp.rejected.
	// We accomplish this by giving the instance a reference back to fp so
	// it can call recordRejection. We do this via a thin wrapper below.
	inst.rejectSink = fp.recordRejectionFor(inst, runID)

	return inst, nil
}

// recordRejectionFor returns a closure that logs a RejectionRecord when called.
func (fp *FakeProvisioner) recordRejectionFor(inst *Instance, runID string) func(from, to State) {
	return func(from, to State) {
		fp.mu.Lock()
		defer fp.mu.Unlock()
		fp.rejected = append(fp.rejected, RejectionRecord{
			From:  from,
			To:    to,
			At:    fp.clk.Now(),
			RunID: runID,
		})
	}
}

// RejectedTransitions returns a copy of all recorded rejection events.
func (fp *FakeProvisioner) RejectedTransitions() []RejectionRecord {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	out := make([]RejectionRecord, len(fp.rejected))
	copy(out, fp.rejected)
	return out
}

// Compile-time proof FakeProvisioner satisfies the interface.
var _ Provisioner = (*FakeProvisioner)(nil)
