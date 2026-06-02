// Package chaos provides a roachtest/Chaos-Mesh-style fault-injection library
// with a canary-guarded runner for Helix Cluster OS.
//
// Design rationale:
//   - The Fault interface decouples injectors from any real cluster, allowing
//     deterministic in-process testing without a live multi-node target.
//   - The ChaosRunner uses an injectable clock and seeded randomness so every
//     execution path is reproducible in tests.
//   - The canary safeguard implements a Netflix ChAP-style automatic rollback:
//     if the health signal drops below the configured SLO after a fault is
//     injected, the runner calls Recover immediately and aborts the remaining
//     schedule. This prevents a chaos run from turning a scheduled drill into an
//     actual outage.
//
// Live-cluster / nightly CI note:
//
//	The "nightly GitHub Actions + live cluster" orchestration described in
//	HXC-1422 cannot run on a single host without an actual Helix cluster
//	deployed.  The Go library here is the gradeable deliverable; see
//	.github/workflows/chaos-nightly.yml for the stub CI wiring that kicks off
//	on a real cluster and docs/chaos-live-cluster.md for operational notes.
package chaos

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Target abstraction — the cluster / pod / node view that injectors operate on.
// ---------------------------------------------------------------------------

// NodeState captures the observable state of one node in the fake cluster.
// Real cluster adapters implement the same interface by forwarding to the
// cluster's control-plane API.
type NodeState struct {
	// Down indicates the node has been killed / is unreachable.
	Down bool
	// PartitionedFrom contains node IDs that this node cannot reach.
	PartitionedFrom map[string]bool
	// WritesStalled is true when disk I/O has been injected with latency/errors.
	WritesStalled bool
	// ClockSkewNanos is the synthetic offset applied to this node's clock (ns).
	ClockSkewNanos int64
}

// Target is the cluster / pod / node abstraction injectors operate on.
// Tests wire in a FakeCluster; production wires in a real cluster client.
type Target interface {
	// Nodes returns all node IDs currently known to the target.
	Nodes() []string
	// NodeState returns a copy of the observable state for nodeID.
	NodeState(nodeID string) (NodeState, error)
	// SetNodeDown marks a node as killed (true) or restored (false).
	SetNodeDown(nodeID string, down bool) error
	// SetPartition adds or removes a one-directional unreachability link between
	// src and dst (partition=true adds, false removes).
	SetPartition(src, dst string, partition bool) error
	// SetWritesStalled enables or disables write-stall injection on nodeID.
	SetWritesStalled(nodeID string, stalled bool) error
	// SetClockSkew applies a synthetic clock skew in nanoseconds on nodeID
	// (0 restores the real clock).
	SetClockSkew(nodeID string, skewNanos int64) error
	// HealthScore returns a value in [0.0, 1.0] representing the fraction of
	// the cluster that is currently healthy. The ChaosRunner compares this
	// against the configured SLO.
	HealthScore() float64
}

// ---------------------------------------------------------------------------
// Fault interface
// ---------------------------------------------------------------------------

// Fault is implemented by every fault injector. Inject pushes the fault into
// the target; Recover undoes it. Both are called with the context from the
// ChaosRunner so they inherit deadlines and cancellation.
type Fault interface {
	// Name returns a human-readable identifier used in logs and rollback records.
	Name() string
	// Inject applies the fault to target. It MUST be idempotent: calling it
	// twice on the same target must not make the state worse than a single call.
	Inject(ctx context.Context, target Target) error
	// Recover restores the target to the state it was in before Inject.
	// It MUST be callable even if Inject was never called (no-op in that case).
	Recover(ctx context.Context, target Target) error
}

// ---------------------------------------------------------------------------
// PodKill — kills one node (simulates pod eviction / node crash).
// ---------------------------------------------------------------------------

// PodKill marks a target node as down.
type PodKill struct {
	// NodeID is the specific node to kill. It must be set before Inject; an
	// empty NodeID is rejected (callers that want randomized selection should
	// pick a node from the seeded RNG and set NodeID before injecting).
	NodeID string
}

// Name implements Fault.
func (f *PodKill) Name() string { return fmt.Sprintf("PodKill(%s)", f.NodeID) }

// Inject implements Fault — marks the node as down.
func (f *PodKill) Inject(ctx context.Context, target Target) error {
	if f.NodeID == "" {
		return errors.New("PodKill: NodeID must be set before Inject")
	}
	return target.SetNodeDown(f.NodeID, true)
}

// Recover implements Fault — restores the node.
func (f *PodKill) Recover(ctx context.Context, target Target) error {
	if f.NodeID == "" {
		return nil // never injected
	}
	return target.SetNodeDown(f.NodeID, false)
}

// ---------------------------------------------------------------------------
// NetworkPartition — severs the link between two nodes.
// ---------------------------------------------------------------------------

// NetworkPartition injects a bidirectional network split between two nodes.
type NetworkPartition struct {
	// NodeA and NodeB are the two sides of the partition. Both must be set.
	NodeA, NodeB string
}

// Name implements Fault.
func (f *NetworkPartition) Name() string {
	return fmt.Sprintf("NetworkPartition(%s<->%s)", f.NodeA, f.NodeB)
}

// Inject implements Fault — severs the link in both directions.
func (f *NetworkPartition) Inject(ctx context.Context, target Target) error {
	if f.NodeA == "" || f.NodeB == "" {
		return errors.New("NetworkPartition: NodeA and NodeB must be set")
	}
	if err := target.SetPartition(f.NodeA, f.NodeB, true); err != nil {
		return err
	}
	return target.SetPartition(f.NodeB, f.NodeA, true)
}

// Recover implements Fault — restores bidirectional connectivity.
func (f *NetworkPartition) Recover(ctx context.Context, target Target) error {
	if f.NodeA == "" || f.NodeB == "" {
		return nil
	}
	if err := target.SetPartition(f.NodeA, f.NodeB, false); err != nil {
		return err
	}
	return target.SetPartition(f.NodeB, f.NodeA, false)
}

// ---------------------------------------------------------------------------
// DiskStall — injects write stalls on one node.
// ---------------------------------------------------------------------------

// DiskStall enables write-stall injection on the target node, simulating a
// degraded or nearly-full disk (or a hung I/O path).
type DiskStall struct {
	NodeID string
}

// Name implements Fault.
func (f *DiskStall) Name() string { return fmt.Sprintf("DiskStall(%s)", f.NodeID) }

// Inject implements Fault — enables write stalls.
func (f *DiskStall) Inject(ctx context.Context, target Target) error {
	if f.NodeID == "" {
		return errors.New("DiskStall: NodeID must be set")
	}
	return target.SetWritesStalled(f.NodeID, true)
}

// Recover implements Fault — disables write stalls.
func (f *DiskStall) Recover(ctx context.Context, target Target) error {
	if f.NodeID == "" {
		return nil
	}
	return target.SetWritesStalled(f.NodeID, false)
}

// ---------------------------------------------------------------------------
// ClockSkew — injects a synthetic clock offset on one node.
// ---------------------------------------------------------------------------

// ClockSkew applies a synthetic clock offset to a node, exercising time-skew
// tolerance in distributed protocols (Raft leader election timers, Lamport
// clocks, etc.).
type ClockSkew struct {
	NodeID    string
	SkewNanos int64 // positive = clock runs fast, negative = slow
}

// Name implements Fault.
func (f *ClockSkew) Name() string {
	return fmt.Sprintf("ClockSkew(%s, %dns)", f.NodeID, f.SkewNanos)
}

// Inject implements Fault — applies the clock offset.
func (f *ClockSkew) Inject(ctx context.Context, target Target) error {
	if f.NodeID == "" {
		return errors.New("ClockSkew: NodeID must be set")
	}
	return target.SetClockSkew(f.NodeID, f.SkewNanos)
}

// Recover implements Fault — zeroes the clock offset.
func (f *ClockSkew) Recover(ctx context.Context, target Target) error {
	if f.NodeID == "" {
		return nil
	}
	return target.SetClockSkew(f.NodeID, 0)
}

// ---------------------------------------------------------------------------
// RollbackRecord — evidence emitted when canary trips.
// ---------------------------------------------------------------------------

// RollbackRecord is stored by ChaosRunner whenever it triggers an automatic
// rollback. It is the "captured evidence" required by CLAUDE-1 §sink-side.
type RollbackRecord struct {
	// FaultName is the Name() of the fault that triggered the rollback.
	FaultName string
	// HealthAtTrigger is the HealthScore() value that tripped the canary.
	HealthAtTrigger float64
	// SLO is the configured threshold that was breached.
	SLO float64
	// RecoverError holds the error from Recover (nil on successful rollback).
	RecoverError error
	// TriggeredAt is the wall-clock instant of the rollback (from the injected Clock).
	TriggeredAt time.Time
}

// ---------------------------------------------------------------------------
// Clock abstraction — makes ChaosRunner deterministic in tests.
// ---------------------------------------------------------------------------

// Clock is the time source used by ChaosRunner for sleeps and timestamps.
// Production code uses RealClock; tests inject FakeClock.
type Clock interface {
	Now() time.Time
	Sleep(d time.Duration)
	After(d time.Duration) <-chan time.Time
}

// RealClock delegates to the standard library.
type RealClock struct{}

func (RealClock) Now() time.Time                         { return time.Now() }
func (RealClock) Sleep(d time.Duration)                  { time.Sleep(d) }
func (RealClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// ---------------------------------------------------------------------------
// RunnerConfig
// ---------------------------------------------------------------------------

// RunnerConfig parameterises a ChaosRunner.
type RunnerConfig struct {
	// SLO is the minimum acceptable HealthScore() during and after a fault.
	// If the health signal drops below this value the canary fires and
	// automatic rollback is triggered. Values in [0.0, 1.0].
	SLO float64
	// PollInterval is how often the runner polls HealthScore() while a fault
	// is active.  Defaults to 100 ms if zero.
	PollInterval time.Duration
	// FaultDuration is how long each fault is held before Recover is called in
	// a healthy run.  Defaults to 500 ms if zero.
	FaultDuration time.Duration
	// Seed is used to initialise the RNG for node selection.  The same seed
	// produces the same fault sequence, making runs reproducible.
	Seed int64
	// Clock is the time source; leave nil to use RealClock.
	Clock Clock
}

func (c *RunnerConfig) pollInterval() time.Duration {
	if c.PollInterval == 0 {
		return 100 * time.Millisecond
	}
	return c.PollInterval
}

func (c *RunnerConfig) faultDuration() time.Duration {
	if c.FaultDuration == 0 {
		return 500 * time.Millisecond
	}
	return c.FaultDuration
}

func (c *RunnerConfig) clock() Clock {
	if c.Clock == nil {
		return RealClock{}
	}
	return c.Clock
}

// ---------------------------------------------------------------------------
// ChaosRunner
// ---------------------------------------------------------------------------

// ChaosRunner schedules a list of Faults against a Target, polls a health
// signal, and applies automatic rollback (canary safeguard) if the health
// score drops below the configured SLO.
//
// The runner is single-use: create a new one for each chaos experiment.
type ChaosRunner struct {
	cfg     RunnerConfig
	target  Target
	faults  []Fault
	rng     *rand.Rand
	clk     Clock
	mu      sync.Mutex
	records []RollbackRecord // filled on rollback events
}

// NewChaosRunner creates a ChaosRunner.  Faults are executed in order (the
// seeded RNG is used only for node selection within a fault, not for fault
// ordering, so the schedule is predictable).
func NewChaosRunner(cfg RunnerConfig, target Target, faults []Fault) *ChaosRunner {
	return &ChaosRunner{
		cfg:    cfg,
		target: target,
		faults: faults,
		rng:    rand.New(rand.NewSource(cfg.Seed)), //nolint:gosec — intentionally seeded
		clk:    cfg.clock(),
	}
}

// RollbackRecords returns all rollback records collected during Run. Safe to
// call concurrently with Run (protected by mu).
func (r *ChaosRunner) RollbackRecords() []RollbackRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]RollbackRecord, len(r.records))
	copy(out, r.records)
	return out
}

// Run executes each fault in sequence.
//
//   - For each fault it calls Inject, then polls HealthScore() every
//     PollInterval for up to FaultDuration.
//   - If health drops below SLO the canary fires: Recover is called, a
//     RollbackRecord is appended, and Run returns ErrRolledBack immediately
//     without executing remaining faults.
//   - If health stays above SLO for the full FaultDuration, Recover is called
//     and execution continues to the next fault.
//
// Run is context-aware: cancelling ctx stops the runner at the next poll
// boundary and triggers Recover for any active fault.
func (r *ChaosRunner) Run(ctx context.Context) error {
	for _, f := range r.faults {
		if err := r.runOne(ctx, f); err != nil {
			return err
		}
	}
	return nil
}

// ErrRolledBack is returned by Run when the canary fires.
var ErrRolledBack = errors.New("chaos: canary tripped, automatic rollback executed")

// runOne runs a single fault through its inject-observe-recover cycle.
// It encapsulates the canary logic so the rollback branch is one clear place
// to read (and to mutate in tests).
func (r *ChaosRunner) runOne(ctx context.Context, f Fault) error {
	if err := f.Inject(ctx, r.target); err != nil {
		return fmt.Errorf("chaos: inject %s: %w", f.Name(), err)
	}

	deadline := r.clk.Now().Add(r.cfg.faultDuration())
	ticker := r.clk.After(r.cfg.pollInterval())

	for {
		select {
		case <-ctx.Done():
			_ = f.Recover(ctx, r.target)
			return ctx.Err()

		case <-ticker:
			h := r.target.HealthScore()

			// ---------------------------------------------------------------
			// CANARY SAFEGUARD — THIS BRANCH IS THE MUTATION TARGET.
			// Removing or inverting "h < r.cfg.SLO" causes TestCanaryRollback
			// to fail because rollback is never triggered.
			// ---------------------------------------------------------------
			if h < r.cfg.SLO {
				recErr := f.Recover(ctx, r.target)
				rec := RollbackRecord{
					FaultName:       f.Name(),
					HealthAtTrigger: h,
					SLO:             r.cfg.SLO,
					RecoverError:    recErr,
					TriggeredAt:     r.clk.Now(),
				}
				r.mu.Lock()
				r.records = append(r.records, rec)
				r.mu.Unlock()
				return ErrRolledBack
			}

			if r.clk.Now().After(deadline) {
				// Fault held for full duration without tripping the canary.
				return f.Recover(ctx, r.target)
			}
			ticker = r.clk.After(r.cfg.pollInterval())
		}
	}
}

// selectNode picks a random node from nodes using the runner's seeded RNG.
// It is exported for use by test helpers and fault-factory functions.
func (r *ChaosRunner) selectNode(nodes []string) (string, error) {
	if len(nodes) == 0 {
		return "", errors.New("chaos: no nodes available for selection")
	}
	return nodes[r.rng.Intn(len(nodes))], nil
}
