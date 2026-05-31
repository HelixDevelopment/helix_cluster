package device

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"
)

// State is a node in the device provisioning lifecycle FSM.
//
// Legal transitions:
//
//	Requested   -> Provisioning | Failed
//	Provisioning-> Ready        | Failed
//	Ready       -> InUse        | Releasing
//	InUse       -> Releasing
//	Releasing   -> Released     | Failed
//
// Released and Failed are terminal.
type State int

const (
	// StateRequested is the initial state of a device the moment a spec is accepted.
	StateRequested State = iota
	// StateProvisioning is the state while the backend brings the device up.
	StateProvisioning
	// StateReady means the device is provisioned and idle, available for use.
	StateReady
	// StateInUse means the device has been claimed by a caller.
	StateInUse
	// StateReleasing is the state while the backend tears the device down.
	StateReleasing
	// StateReleased is the terminal state of a successfully released device.
	StateReleased
	// StateFailed is the terminal state of a device whose lifecycle errored.
	StateFailed
)

// realClock is the wall-clock used by non-deterministic (hardware) backends.
func realClock() time.Time { return time.Now() }

// String renders the State for logs and assertions.
func (s State) String() string {
	switch s {
	case StateRequested:
		return "Requested"
	case StateProvisioning:
		return "Provisioning"
	case StateReady:
		return "Ready"
	case StateInUse:
		return "InUse"
	case StateReleasing:
		return "Releasing"
	case StateReleased:
		return "Released"
	case StateFailed:
		return "Failed"
	default:
		return fmt.Sprintf("State(%d)", int(s))
	}
}

// legalTransitions encodes the FSM edges. A transition not present here is
// rejected by transition(), which is what keeps the lifecycle honest.
var legalTransitions = map[State]map[State]bool{
	StateRequested:    {StateProvisioning: true, StateFailed: true},
	StateProvisioning: {StateReady: true, StateFailed: true},
	StateReady:        {StateInUse: true, StateReleasing: true},
	StateInUse:        {StateReleasing: true},
	StateReleasing:    {StateReleased: true, StateFailed: true},
	StateReleased:     {},
	StateFailed:       {},
}

// DeviceSpec describes the device a caller wants provisioned. It is intentionally
// independent of the static Profile registry: a spec is a request, a Profile is a
// catalog entry. Tier may reference a Profile tier (e.g. "T3") for matching.
type DeviceSpec struct {
	Tier     string // e.g. "T3"; may map to a Profile in a Registry
	Arch     string // e.g. "arm64", "x86_64"
	MemoryGB int    // requested RAM
	GPU      bool   // whether a GPU is required
	Labels   map[string]string
}

// Event is an observable lifecycle transition, captured sink-side so tests can
// assert the exact ordered path a device walked through.
type Event struct {
	DeviceID string
	From     State
	To       State
	At       time.Time
}

// Device is a handle to a provisioned (or in-flight) device. It is safe for
// concurrent Status reads; mutation goes through the owning Provisioner.
type Device struct {
	ID   string
	Spec DeviceSpec

	mu     sync.RWMutex
	state  State
	events []Event
	clock  func() time.Time
}

// State returns the device's current FSM state.
func (d *Device) State() State {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.state
}

// Events returns a copy of the ordered transition log for this device.
func (d *Device) Events() []Event {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]Event, len(d.events))
	copy(out, d.events)
	return out
}

// ErrIllegalTransition is returned when an FSM edge is not permitted.
var ErrIllegalTransition = errors.New("device: illegal state transition")

// transition advances the device along a legal FSM edge, recording an Event.
// It returns ErrIllegalTransition (wrapped) if the edge is not in the FSM.
func (d *Device) transition(to State) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	from := d.state
	if !legalTransitions[from][to] {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, from, to)
	}
	d.state = to
	d.events = append(d.events, Event{
		DeviceID: d.ID,
		From:     from,
		To:       to,
		At:       d.clock(),
	})
	return nil
}

// Provisioner is the lifecycle contract for a device backend.
type Provisioner interface {
	// Provision brings up a device matching spec, returning a Ready handle.
	Provision(ctx context.Context, spec DeviceSpec) (*Device, error)
	// Release tears the device down, driving it to Released.
	Release(ctx context.Context, d *Device) error
	// Status reports the current lifecycle state of a device.
	Status(d *Device) State
}

// SoftwareProvisioner is a deterministic, no-hardware reference Provisioner. It
// walks the full FSM (Requested->Provisioning->Ready on Provision,
// Ready/InUse->Releasing->Released on Release) and emits sink-side Events. A
// fixed seed makes the simulated provisioning order/jitter reproducible.
type SoftwareProvisioner struct {
	mu    sync.Mutex
	rng   *rand.Rand
	now   time.Time
	seq   int
	delay time.Duration // virtual per-step delay folded into the deterministic clock
}

// NewSoftwareProvisioner builds a deterministic provisioner. Same seed =>
// identical device IDs and identical event timestamps across runs.
func NewSoftwareProvisioner(seed int64) *SoftwareProvisioner {
	return &SoftwareProvisioner{
		rng:   rand.New(rand.NewSource(seed)),
		now:   time.Unix(0, 0).UTC(),
		delay: time.Millisecond,
	}
}

// virtualClock returns a monotonic deterministic clock closure. Each call
// advances virtual time by a seeded jitter so timestamps are reproducible but
// strictly increasing.
func (p *SoftwareProvisioner) tick() time.Time {
	p.now = p.now.Add(p.delay + time.Duration(p.rng.Intn(1000))*time.Microsecond)
	return p.now
}

// Provision implements Provisioner. It is deterministic given the seed and the
// arrival order of calls.
func (p *SoftwareProvisioner) Provision(ctx context.Context, spec DeviceSpec) (*Device, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("device: provision cancelled: %w", err)
	}
	p.mu.Lock()
	p.seq++
	id := fmt.Sprintf("dev-%04d", p.seq)
	clock := func() time.Time {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.tick()
	}
	p.mu.Unlock()

	d := &Device{
		ID:    id,
		Spec:  spec,
		state: StateRequested,
		clock: clock,
	}
	// Record the Requested entry so the event log starts at the true origin.
	d.recordOrigin()

	if err := d.transition(StateProvisioning); err != nil {
		return nil, err
	}
	// Honest simulated work: respect cancellation between steps.
	if err := ctx.Err(); err != nil {
		_ = d.transition(StateFailed)
		return nil, fmt.Errorf("device: provision cancelled: %w", err)
	}
	if err := d.transition(StateReady); err != nil {
		return nil, err
	}
	return d, nil
}

// recordOrigin seeds the event log with the Requested origin (From==To==Requested).
func (d *Device) recordOrigin() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.events = append(d.events, Event{
		DeviceID: d.ID,
		From:     StateRequested,
		To:       StateRequested,
		At:       d.clock(),
	})
}

// Claim moves a Ready device to InUse. It is exported so a Pool (or caller) can
// mark usage; it enforces the FSM.
func (p *SoftwareProvisioner) Claim(d *Device) error {
	return d.transition(StateInUse)
}

// Release implements Provisioner. A device already Released double-releases into
// an error (illegal transition from terminal Released).
func (p *SoftwareProvisioner) Release(ctx context.Context, d *Device) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("device: release cancelled: %w", err)
	}
	if err := d.transition(StateReleasing); err != nil {
		return fmt.Errorf("device %s: %w", d.ID, err)
	}
	if err := d.transition(StateReleased); err != nil {
		return fmt.Errorf("device %s: %w", d.ID, err)
	}
	return nil
}

// Status implements Provisioner.
func (p *SoftwareProvisioner) Status(d *Device) State {
	return d.State()
}

// Compile-time proof SoftwareProvisioner satisfies the interface.
var _ Provisioner = (*SoftwareProvisioner)(nil)
