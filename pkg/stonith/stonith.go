// Package stonith implements STONITH (Shoot-The-Other-Node-In-The-Head)
// node fencing for Helix Cluster OS.
//
// Fencing is the act of forcibly isolating a node that is suspected to be
// faulty or unreachable, so the rest of the cluster can safely take over its
// resources without risk of split-brain (two nodes both believing they own the
// same resource). Unlike a graceful shutdown, STONITH does not cooperate with
// the target: it powers the node off (IPMI/cloud) or writes a poison message
// to a shared device (SBD) that the target's watchdog will act on.
//
// The package is structured around the FencingAgent interface so that the
// fencing mechanism (out-of-band IPMI, cloud power API, shared-disk SBD) is
// pluggable. MultiLevelFencer composes several agents into an ordered fallback
// chain: only when fencing is *confirmed* by an agent does the chain declare
// success. This "confirm, don't assume" rule is the whole point of STONITH —
// assuming a node is dead when it is not is exactly the split-brain we are
// fencing to prevent.
//
// External systems (cloud APIs, the ipmitool binary, the SBD block device) are
// kept behind small interfaces so the logic is testable without real
// credentials or hardware. Each driver returns a typed error on inability to
// act (e.g. ErrIPMIToolAbsent) rather than silently reporting a fake fence.
package stonith

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Common typed errors. Callers use errors.Is to distinguish "could not even
// attempt to fence" (a hard infrastructure problem) from "attempted but the
// target did not transition" (a fencing failure that should trigger fallback).
var (
	// ErrNotConfirmed means a fence command was issued but the target was not
	// observed to transition to the fenced state. This is a fencing FAILURE,
	// never a success, and MUST trigger fallback in a MultiLevelFencer.
	ErrNotConfirmed = errors.New("stonith: fence not confirmed")

	// ErrIPMIToolAbsent means the ipmitool binary is not installed/locatable.
	// The IPMI driver returns this instead of fabricating a successful fence.
	ErrIPMIToolAbsent = errors.New("stonith: ipmitool binary not found")

	// ErrNoAgents means a MultiLevelFencer was constructed with no agents and
	// therefore can never fence anything.
	ErrNoAgents = errors.New("stonith: no fencing agents configured")

	// ErrEmptyTarget means a fence was requested without a target identifier.
	ErrEmptyTarget = errors.New("stonith: empty target")

	// ErrTargetMismatch means the target passed to an agent does not match the
	// node the agent was configured for. Acting on a mismatched target is a
	// wrong-node-fence hazard (fencing the wrong machine while emitting a
	// Confirmation claiming the right one was fenced), so we surface it as an
	// explicit typed error that callers — and MultiLevelFencer — can detect and
	// reject without falling through to a worse outcome.
	ErrTargetMismatch = errors.New("stonith: target does not match agent configuration")
)

// Confirmation is the sink-side evidence that a fence actually took effect.
// Producing this record (rather than merely returning a nil error) is what lets
// a caller — and our CLAUDE-1 tests — prove the feature really worked: the
// target was confirmed unreachable/powered-off by a named agent at a known
// time.
type Confirmation struct {
	// Target is the node that was fenced.
	Target string
	// Agent is the Name() of the FencingAgent that confirmed the fence.
	Agent string
	// Method describes how the fence was achieved (e.g. "ipmi power off",
	// "ec2 stop", "sbd poison").
	Method string
	// At is when confirmation was observed.
	At time.Time
}

// FencingAgent is a single mechanism that can fence a node and confirm the
// resulting state.
type FencingAgent interface {
	// Name returns a stable identifier for this agent (used in confirmations
	// and aggregated errors).
	Name() string

	// Fence forcibly isolates target and returns a Confirmation only once the
	// fenced state has been verified. If the command was issued but the
	// transition could not be confirmed, Fence returns an error wrapping
	// ErrNotConfirmed (never a nil error with no confirmation).
	Fence(ctx context.Context, target string) (*Confirmation, error)

	// IsFenced reports whether target is currently in the fenced (isolated /
	// powered-off) state according to this agent's view of the world.
	IsFenced(ctx context.Context, target string) (bool, error)
}

// MultiLevelFencer tries a chain of FencingAgents in order. The first agent to
// return a confirmed fence wins; if an agent fails, the next is tried. If every
// agent fails, the aggregated error of all attempts is returned and the target
// is NOT considered fenced.
//
// Ordering matters: place the most authoritative / fastest mechanism first
// (typically out-of-band IPMI), with cloud power-off and SBD as fallbacks.
type MultiLevelFencer struct {
	agents []FencingAgent
}

// NewMultiLevelFencer builds a fencer from an ordered list of agents. At least
// one agent is required; otherwise construction returns ErrNoAgents so the
// misconfiguration is caught at wiring time rather than during an incident.
func NewMultiLevelFencer(agents ...FencingAgent) (*MultiLevelFencer, error) {
	if len(agents) == 0 {
		return nil, ErrNoAgents
	}
	// Defensively copy so a caller mutating their slice later cannot reorder or
	// truncate our fallback chain.
	cp := make([]FencingAgent, len(agents))
	copy(cp, agents)
	return &MultiLevelFencer{agents: cp}, nil
}

// Agents returns the configured agents in order. The returned slice is a copy.
func (m *MultiLevelFencer) Agents() []FencingAgent {
	cp := make([]FencingAgent, len(m.agents))
	copy(cp, m.agents)
	return cp
}

// Fence attempts to fence target using each agent in order, returning the first
// confirmation. If all agents fail it returns a joined error describing every
// attempt. The context is honoured between attempts so a cancelled fence stops
// promptly instead of grinding through the whole chain.
func (m *MultiLevelFencer) Fence(ctx context.Context, target string) (*Confirmation, error) {
	if target == "" {
		return nil, ErrEmptyTarget
	}

	var attemptErrs []error
	for _, agent := range m.agents {
		// Honour cancellation before each level so a deadline-exceeded fence
		// does not keep escalating.
		if err := ctx.Err(); err != nil {
			attemptErrs = append(attemptErrs, fmt.Errorf("aborting fence chain: %w", err))
			break
		}

		conf, err := agent.Fence(ctx, target)
		if err != nil {
			// This level failed — record why and fall through to the next.
			// Removing this "continue to next agent" behaviour (i.e. returning
			// here) would break multi-level fallback; that is the mutation our
			// fallback test guards against.
			attemptErrs = append(attemptErrs, fmt.Errorf("agent %q: %w", agent.Name(), err))
			continue
		}
		if conf == nil {
			// Defensive: a well-behaved agent never returns (nil, nil), but if
			// one does we treat it as not-confirmed rather than a success.
			attemptErrs = append(attemptErrs, fmt.Errorf("agent %q: %w", agent.Name(), ErrNotConfirmed))
			continue
		}
		return conf, nil
	}

	return nil, fmt.Errorf("all %d fencing level(s) failed: %w", len(m.agents), errors.Join(attemptErrs...))
}

// IsFenced reports true if ANY configured agent reports the target as fenced.
// A node confirmed dead by one mechanism is dead for cluster purposes.
func (m *MultiLevelFencer) IsFenced(ctx context.Context, target string) (bool, error) {
	var errs []error
	for _, agent := range m.agents {
		fenced, err := agent.IsFenced(ctx, target)
		if err != nil {
			errs = append(errs, fmt.Errorf("agent %q: %w", agent.Name(), err))
			continue
		}
		if fenced {
			return true, nil
		}
	}
	if len(errs) > 0 {
		return false, errors.Join(errs...)
	}
	return false, nil
}
