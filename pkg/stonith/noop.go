package stonith

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// PowerController is a real (in-process) power state machine. It is the
// abstraction every "power off the box" driver acts upon, and it is also used
// directly by NoOpAgent so tests can assert genuine state transitions rather
// than mock call-counts. Implementations MUST be safe for concurrent use.
//
// This is intentionally not a mock: PowerState transitions are real, observable
// state. A test that drives a fence and then reads State()==PoweredOff is
// proving sink-side behaviour per CLAUDE-1.
type PowerController interface {
	// PowerOff transitions target to PoweredOff. It is idempotent.
	PowerOff(ctx context.Context, target string) error
	// State returns the current PowerState of target. Unknown targets default
	// to PoweredOn (a node we have never fenced is assumed alive).
	State(ctx context.Context, target string) (PowerState, error)
}

// PowerState is the on/off state of a node as tracked by a PowerController.
type PowerState int

const (
	// PoweredOn means the node is alive and reachable.
	PoweredOn PowerState = iota
	// PoweredOff means the node has been fenced (powered off / isolated).
	PoweredOff
)

func (s PowerState) String() string {
	switch s {
	case PoweredOn:
		return "powered-on"
	case PoweredOff:
		return "powered-off"
	default:
		return fmt.Sprintf("PowerState(%d)", int(s))
	}
}

// InMemoryPower is a real, concurrency-safe PowerController backed by a map.
// It models an actual power fabric: targets start PoweredOn and a PowerOff
// genuinely flips their recorded state. It is suitable for integration-style
// tests (the state machine is real) and for the NoOpAgent.
type InMemoryPower struct {
	mu     sync.Mutex
	states map[string]PowerState
}

// NewInMemoryPower constructs an empty power fabric where every node is
// implicitly PoweredOn until fenced.
func NewInMemoryPower() *InMemoryPower {
	return &InMemoryPower{states: make(map[string]PowerState)}
}

// PowerOff records target as PoweredOff.
func (p *InMemoryPower) PowerOff(_ context.Context, target string) error {
	if target == "" {
		return ErrEmptyTarget
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.states[target] = PoweredOff
	return nil
}

// State returns the recorded state of target, defaulting to PoweredOn.
func (p *InMemoryPower) State(_ context.Context, target string) (PowerState, error) {
	if target == "" {
		return PoweredOn, ErrEmptyTarget
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if s, ok := p.states[target]; ok {
		return s, nil
	}
	return PoweredOn, nil
}

// NoOpAgent is a FencingAgent driven by a PowerController. It is "no-op" only in
// the sense that it performs no out-of-band/cloud/disk I/O — it still drives a
// real power state machine and confirms the transition, so it is safe to use in
// tests as a genuinely-succeeding (or, when wired to a failing controller,
// genuinely-failing) level.
type NoOpAgent struct {
	name string
	pc   PowerController
}

// NewNoOpAgent builds a NoOpAgent with the given name over a PowerController.
func NewNoOpAgent(name string, pc PowerController) *NoOpAgent {
	return &NoOpAgent{name: name, pc: pc}
}

// Name returns the agent's name.
func (a *NoOpAgent) Name() string { return a.name }

// Fence powers off target via the controller and confirms it reached
// PoweredOff. If the post-condition is not met it returns ErrNotConfirmed.
func (a *NoOpAgent) Fence(ctx context.Context, target string) (*Confirmation, error) {
	if target == "" {
		return nil, ErrEmptyTarget
	}
	if err := a.pc.PowerOff(ctx, target); err != nil {
		return nil, fmt.Errorf("noop power off %q: %w", target, err)
	}
	st, err := a.pc.State(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("noop confirm %q: %w", target, err)
	}
	if st != PoweredOff {
		return nil, fmt.Errorf("noop %q still %s: %w", target, st, ErrNotConfirmed)
	}
	return &Confirmation{
		Target: target,
		Agent:  a.name,
		Method: "noop power off",
		At:     time.Now(),
	}, nil
}

// IsFenced reports whether the controller shows target as PoweredOff.
func (a *NoOpAgent) IsFenced(ctx context.Context, target string) (bool, error) {
	st, err := a.pc.State(ctx, target)
	if err != nil {
		return false, err
	}
	return st == PoweredOff, nil
}

// FailingPowerController is a PowerController whose PowerOff always fails. It
// models a power fabric that genuinely cannot reach the target (the realistic
// trigger for STONITH fallback) and is used to drive the "primary level fails"
// branch in tests. State() still reports honestly so that a failed PowerOff
// leaves the node PoweredOn — proving the level did NOT fence the target.
type FailingPowerController struct {
	Err error
}

// PowerOff always returns the configured error (or a default if nil).
func (f *FailingPowerController) PowerOff(_ context.Context, _ string) error {
	if f.Err != nil {
		return f.Err
	}
	return fmt.Errorf("power fabric unreachable: %w", ErrNotConfirmed)
}

// State always reports PoweredOn: a node this controller failed to fence is, by
// definition, still alive.
func (f *FailingPowerController) State(_ context.Context, _ string) (PowerState, error) {
	return PoweredOn, nil
}
