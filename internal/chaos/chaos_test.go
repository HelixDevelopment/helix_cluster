// Tests for package chaos — closure criteria from HXC-1422.
//
// CLAUDE-1 compliance:
//   - Every test asserts SINK-SIDE STATE (NodeState fields), not merely that
//     Inject/Recover returned nil.
//   - The fake cluster is a real in-process state machine (see
//     fake_cluster_test.go), not a no-op mock.
//
// Mutation guard:
//   - TestCanaryRollback_TripsOnLowHealth is the load-bearing test. Removing
//     or inverting the "h < r.cfg.SLO" branch in runOne() causes this test to
//     fail because ErrRolledBack is never returned and no RollbackRecord is
//     recorded.
package chaos

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newDeterministicRunner builds a ChaosRunner wired to a FakeCluster and
// FakeClock. FaultDuration is exactly 3×PollInterval so the runner exits
// after 3 ticks without needing real-time sleeps. The FakeClock auto-advances
// by PollInterval on every After() call, so the deadline is reached quickly.
func newDeterministicRunner(cluster *FakeCluster, faults []Fault, slo float64) (*ChaosRunner, *FakeClock) {
	clk := NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	poll := 10 * time.Millisecond
	cfg := RunnerConfig{
		SLO:           slo,
		PollInterval:  poll,
		FaultDuration: 3 * poll, // exits after 3 ticks (clock advances poll per tick)
		Seed:          42,
		Clock:         clk,
	}
	return NewChaosRunner(cfg, cluster, faults), clk
}

// afterThenDone returns a FakeClock After function that fires immediately once
// and then returns a never-closing channel (so the loop only polls once per
// call sequence, keeping tests deterministic).
func immediateAfter(clk *FakeClock) func(d time.Duration) <-chan time.Time {
	return func(d time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- clk.Now()
		return ch
	}
}

// ---------------------------------------------------------------------------
// Closure criterion 1 — each fault class changes observable state on Inject
// and restores it on Recover.
// ---------------------------------------------------------------------------

func TestPodKill_InjectAndRecover(t *testing.T) {
	cluster := NewFakeCluster("node-1", "node-2")

	// Pre-condition: node is up.
	before, err := cluster.NodeState("node-1")
	require.NoError(t, err)
	assert.False(t, before.Down, "node should be up before inject")

	fault := &PodKill{NodeID: "node-1"}
	ctx := context.Background()

	// Inject — SINK-SIDE: node must be marked down.
	require.NoError(t, fault.Inject(ctx, cluster))
	after, err := cluster.NodeState("node-1")
	require.NoError(t, err)
	assert.True(t, after.Down, "node must be down after PodKill.Inject")

	// Recover — SINK-SIDE: node must be up again.
	require.NoError(t, fault.Recover(ctx, cluster))
	restored, err := cluster.NodeState("node-1")
	require.NoError(t, err)
	assert.False(t, restored.Down, "node must be up after PodKill.Recover")
}

func TestPodKill_Idempotent(t *testing.T) {
	cluster := NewFakeCluster("node-1")
	fault := &PodKill{NodeID: "node-1"}
	ctx := context.Background()

	require.NoError(t, fault.Inject(ctx, cluster))
	require.NoError(t, fault.Inject(ctx, cluster)) // second call must not error
	st, _ := cluster.NodeState("node-1")
	assert.True(t, st.Down)
}

func TestPodKill_RecoverWithoutInject(t *testing.T) {
	cluster := NewFakeCluster("node-1")
	fault := &PodKill{NodeID: "node-1"}
	// Recover before Inject must be a no-op (idempotent).
	require.NoError(t, fault.Recover(context.Background(), cluster))
	st, _ := cluster.NodeState("node-1")
	assert.False(t, st.Down)
}

func TestPodKill_EmptyNodeID_ReturnsError(t *testing.T) {
	cluster := NewFakeCluster("node-1")
	fault := &PodKill{} // no NodeID
	err := fault.Inject(context.Background(), cluster)
	assert.Error(t, err, "Inject with empty NodeID should return error")
}

func TestNetworkPartition_InjectAndRecover(t *testing.T) {
	cluster := NewFakeCluster("A", "B")
	ctx := context.Background()

	fault := &NetworkPartition{NodeA: "A", NodeB: "B"}

	// Pre-condition: no partition.
	before, _ := cluster.NodeState("A")
	assert.Empty(t, before.PartitionedFrom, "A should have no partitions before inject")

	// Inject — SINK-SIDE: both nodes must be partitioned from each other.
	require.NoError(t, fault.Inject(ctx, cluster))

	afterA, _ := cluster.NodeState("A")
	afterB, _ := cluster.NodeState("B")
	assert.True(t, afterA.PartitionedFrom["B"], "A must be partitioned from B after inject")
	assert.True(t, afterB.PartitionedFrom["A"], "B must be partitioned from A after inject")

	// Recover — SINK-SIDE: partition must be gone.
	require.NoError(t, fault.Recover(ctx, cluster))

	restoredA, _ := cluster.NodeState("A")
	restoredB, _ := cluster.NodeState("B")
	assert.False(t, restoredA.PartitionedFrom["B"], "A must not be partitioned from B after recover")
	assert.False(t, restoredB.PartitionedFrom["A"], "B must not be partitioned from A after recover")
}

func TestNetworkPartition_EmptyNodes_ReturnsError(t *testing.T) {
	cluster := NewFakeCluster("A", "B")
	fault := &NetworkPartition{} // empty NodeA and NodeB
	err := fault.Inject(context.Background(), cluster)
	assert.Error(t, err)
}

func TestDiskStall_InjectAndRecover(t *testing.T) {
	cluster := NewFakeCluster("node-1")
	ctx := context.Background()
	fault := &DiskStall{NodeID: "node-1"}

	// Pre-condition: writes not stalled.
	before, _ := cluster.NodeState("node-1")
	assert.False(t, before.WritesStalled, "writes should not be stalled before inject")

	// Inject — SINK-SIDE: writes must be stalled.
	require.NoError(t, fault.Inject(ctx, cluster))
	after, _ := cluster.NodeState("node-1")
	assert.True(t, after.WritesStalled, "writes must be stalled after DiskStall.Inject")

	// Recover — SINK-SIDE: writes must be unstalled.
	require.NoError(t, fault.Recover(ctx, cluster))
	restored, _ := cluster.NodeState("node-1")
	assert.False(t, restored.WritesStalled, "writes must not be stalled after DiskStall.Recover")
}

func TestDiskStall_EmptyNodeID_ReturnsError(t *testing.T) {
	cluster := NewFakeCluster("node-1")
	err := (&DiskStall{}).Inject(context.Background(), cluster)
	assert.Error(t, err)
}

func TestClockSkew_InjectAndRecover(t *testing.T) {
	cluster := NewFakeCluster("node-1")
	ctx := context.Background()
	const skew = int64(5_000_000_000) // 5 s expressed in ns
	fault := &ClockSkew{NodeID: "node-1", SkewNanos: skew}

	// Pre-condition: no skew.
	before, _ := cluster.NodeState("node-1")
	assert.Equal(t, int64(0), before.ClockSkewNanos, "clock skew should be 0 before inject")

	// Inject — SINK-SIDE: skew must be applied.
	require.NoError(t, fault.Inject(ctx, cluster))
	after, _ := cluster.NodeState("node-1")
	assert.Equal(t, skew, after.ClockSkewNanos, "clock skew must be set after ClockSkew.Inject")

	// Recover — SINK-SIDE: skew must be zeroed.
	require.NoError(t, fault.Recover(ctx, cluster))
	restored, _ := cluster.NodeState("node-1")
	assert.Equal(t, int64(0), restored.ClockSkewNanos, "clock skew must be 0 after ClockSkew.Recover")
}

func TestClockSkew_EmptyNodeID_ReturnsError(t *testing.T) {
	cluster := NewFakeCluster("node-1")
	err := (&ClockSkew{SkewNanos: 1000}).Inject(context.Background(), cluster)
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// Fault Name() — sanity checks that names contain identifying information.
// ---------------------------------------------------------------------------

func TestFaultNames(t *testing.T) {
	assert.Contains(t, (&PodKill{NodeID: "n1"}).Name(), "n1")
	assert.Contains(t, (&NetworkPartition{NodeA: "A", NodeB: "B"}).Name(), "A")
	assert.Contains(t, (&NetworkPartition{NodeA: "A", NodeB: "B"}).Name(), "B")
	assert.Contains(t, (&DiskStall{NodeID: "n2"}).Name(), "n2")
	assert.Contains(t, (&ClockSkew{NodeID: "n3", SkewNanos: 999}).Name(), "n3")
}

// ---------------------------------------------------------------------------
// Closure criterion 2 — Canary safeguard triggers automatic rollback.
//
// MUTATION GUARD: the "h < r.cfg.SLO" branch in runOne() is the only place
// where ErrRolledBack is produced and RollbackRecords are appended. Deleting
// or inverting that condition prevents both assertions below from passing.
// ---------------------------------------------------------------------------

func TestCanaryRollback_TripsOnLowHealth(t *testing.T) {
	// Cluster starts healthy. After Inject we drive health to 0.0 via the
	// override, which is below the SLO of 0.8.
	cluster := NewFakeCluster("node-1")
	clk := NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	// healthCallCount tracks how many times HealthScore is polled so we can
	// drive the value down on the second call (after inject).
	callCount := 0
	cluster.SetHealthOverride(func() float64 {
		callCount++
		if callCount >= 2 {
			return 0.0 // below SLO — triggers rollback
		}
		return 1.0
	})

	fault := &PodKill{NodeID: "node-1"}
	cfg := RunnerConfig{
		SLO:           0.8,
		PollInterval:  1 * time.Millisecond,
		FaultDuration: 10 * time.Second,
		Seed:          1,
		Clock:         clk,
	}
	runner := NewChaosRunner(cfg, cluster, []Fault{fault})

	err := runner.Run(context.Background())

	// SINK-SIDE assertion 1: Run must return ErrRolledBack.
	require.ErrorIs(t, err, ErrRolledBack,
		"canary must trigger ErrRolledBack when health < SLO")

	// SINK-SIDE assertion 2: a RollbackRecord must be captured.
	records := runner.RollbackRecords()
	require.Len(t, records, 1, "exactly one rollback record must be captured")
	rec := records[0]
	assert.Equal(t, fault.Name(), rec.FaultName, "rollback record must name the triggering fault")
	assert.Less(t, rec.HealthAtTrigger, cfg.SLO, "health at trigger must be below SLO")
	assert.Equal(t, cfg.SLO, rec.SLO, "rollback record must capture the configured SLO")
	assert.NoError(t, rec.RecoverError, "Recover must succeed (no error in rollback record)")

	// SINK-SIDE assertion 3: node must be UP again (Recover was called).
	st, err := cluster.NodeState("node-1")
	require.NoError(t, err)
	assert.False(t, st.Down, "Recover must have been called — node must be up after rollback")
}

// TestCanaryRollback_AbortsRemainingFaults verifies that after a rollback the
// runner does NOT execute subsequent faults.
func TestCanaryRollback_AbortsRemainingFaults(t *testing.T) {
	cluster := NewFakeCluster("node-1", "node-2")
	clk := NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	// First fault drives health below SLO after inject.
	callCount := 0
	cluster.SetHealthOverride(func() float64 {
		callCount++
		if callCount >= 2 {
			return 0.0
		}
		return 1.0
	})

	faults := []Fault{
		&PodKill{NodeID: "node-1"},
		&PodKill{NodeID: "node-2"}, // must NOT be executed after rollback
	}
	cfg := RunnerConfig{
		SLO:           0.8,
		PollInterval:  1 * time.Millisecond,
		FaultDuration: 10 * time.Second,
		Seed:          1,
		Clock:         clk,
	}
	runner := NewChaosRunner(cfg, cluster, faults)
	err := runner.Run(context.Background())

	require.ErrorIs(t, err, ErrRolledBack)

	// node-2 must still be up (second fault never injected).
	st, _ := cluster.NodeState("node-2")
	assert.False(t, st.Down, "second fault must not have been injected after rollback")
}

// ---------------------------------------------------------------------------
// Closure criterion 3 — healthy run completes all faults within SLO.
// ---------------------------------------------------------------------------

func TestHealthyRun_AllFaultsComplete(t *testing.T) {
	cluster := NewFakeCluster("n1", "n2", "n3")
	// Health stays at 1.0 throughout — cluster is resilient.
	// (FakeCluster default: all nodes up = 1.0)

	faults := []Fault{
		&PodKill{NodeID: "n1"},
		&NetworkPartition{NodeA: "n2", NodeB: "n3"},
		&DiskStall{NodeID: "n1"},
		&ClockSkew{NodeID: "n2", SkewNanos: 2_000_000_000},
	}

	runner, _ := newDeterministicRunner(cluster, faults, 0.0) // SLO=0 never trips
	err := runner.Run(context.Background())
	require.NoError(t, err, "healthy run must complete without error")

	// No rollbacks.
	assert.Empty(t, runner.RollbackRecords(), "no rollback records expected in a healthy run")

	// SINK-SIDE: all faults must have been recovered (cluster fully restored).
	n1, _ := cluster.NodeState("n1")
	assert.False(t, n1.Down, "n1 must be up after healthy run")
	assert.False(t, n1.WritesStalled, "n1 writes must be unstalled after healthy run")

	n2, _ := cluster.NodeState("n2")
	assert.False(t, n2.PartitionedFrom["n3"], "n2 must not be partitioned from n3 after healthy run")
	assert.Equal(t, int64(0), n2.ClockSkewNanos, "n2 clock skew must be zeroed after healthy run")

	n3, _ := cluster.NodeState("n3")
	assert.False(t, n3.PartitionedFrom["n2"], "n3 must not be partitioned from n2 after healthy run")
}

func TestHealthyRun_SLONearBoundary(t *testing.T) {
	// Health is exactly at SLO; condition is strictly <, so no rollback.
	cluster := NewFakeCluster("n1")
	cluster.SetHealthOverride(func() float64 { return 0.8 }) // exactly at SLO=0.8

	faults := []Fault{&DiskStall{NodeID: "n1"}}
	runner, _ := newDeterministicRunner(cluster, faults, 0.8)
	err := runner.Run(context.Background())
	require.NoError(t, err, "health == SLO must NOT trigger rollback (strictly <)")
}

// ---------------------------------------------------------------------------
// Context cancellation — runner must not block after ctx is cancelled.
// ---------------------------------------------------------------------------

func TestRunnerContextCancellation(t *testing.T) {
	cluster := NewFakeCluster("n1")
	// Health override returns 1.0 so canary never trips; the only way out is
	// context cancellation.
	cluster.SetHealthOverride(func() float64 { return 1.0 })

	clk := NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	// After() blocks until the context is cancelled (via a blocking channel).
	blockCh := make(chan time.Time) // never fires
	clk.SetAfterFunc(func(d time.Duration) <-chan time.Time { return blockCh })

	cfg := RunnerConfig{
		SLO:           0.8,
		PollInterval:  1 * time.Second,
		FaultDuration: 10 * time.Second,
		Seed:          0,
		Clock:         clk,
	}
	runner := NewChaosRunner(cfg, cluster, []Fault{&PodKill{NodeID: "n1"}})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()

	cancel() // trigger cancellation
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s after context cancel")
	}

	// Recover must have been called on cancellation → node back up.
	st, _ := cluster.NodeState("n1")
	assert.False(t, st.Down, "Recover must be called on context cancellation")
}

// ---------------------------------------------------------------------------
// ChaosRunner determinism — same seed = same node selection.
// ---------------------------------------------------------------------------

func TestRunner_DeterministicNodeSelection(t *testing.T) {
	cluster := NewFakeCluster("A", "B", "C")
	cfg := RunnerConfig{Seed: 7, Clock: NewFakeClock(time.Now())}
	r1 := NewChaosRunner(cfg, cluster, nil)
	r2 := NewChaosRunner(cfg, cluster, nil)

	nodes := cluster.Nodes()
	n1, _ := r1.selectNode(nodes)
	n2, _ := r2.selectNode(nodes)
	assert.Equal(t, n1, n2, "same seed must produce same node selection")
}

func TestRunner_SelectNode_EmptyList(t *testing.T) {
	cfg := RunnerConfig{Seed: 0, Clock: NewFakeClock(time.Now())}
	r := NewChaosRunner(cfg, NewFakeCluster(), nil)
	_, err := r.selectNode(nil)
	assert.Error(t, err, "selectNode on empty list must return error")
}

// ---------------------------------------------------------------------------
// RollbackRecord concurrency safety.
// ---------------------------------------------------------------------------

func TestRollbackRecords_ConcurrentRead(t *testing.T) {
	cluster := NewFakeCluster("n1")
	callCount := 0
	cluster.SetHealthOverride(func() float64 {
		callCount++
		if callCount >= 2 {
			return 0.0
		}
		return 1.0
	})

	runner, _ := newDeterministicRunner(cluster, []Fault{&PodKill{NodeID: "n1"}}, 0.8)

	// Read concurrently while Run is in progress (race detector coverage).
	done := make(chan struct{})
	go func() {
		_ = runner.Run(context.Background())
		close(done)
	}()
	for {
		select {
		case <-done:
			return
		default:
			_ = runner.RollbackRecords()
		}
	}
}

// ---------------------------------------------------------------------------
// FakeCluster self-tests — validate the in-process state machine.
// ---------------------------------------------------------------------------

func TestFakeCluster_NodeNotFound(t *testing.T) {
	cluster := NewFakeCluster("n1")
	_, err := cluster.NodeState("missing")
	var nfe *nodeNotFoundError
	assert.True(t, errors.As(err, &nfe), "must return nodeNotFoundError for unknown node")
}

func TestFakeCluster_HealthScore_DefaultFraction(t *testing.T) {
	cluster := NewFakeCluster("n1", "n2", "n3")
	assert.InEpsilon(t, 1.0, cluster.HealthScore(), 1e-9, "all up = 1.0")

	_ = cluster.SetNodeDown("n1", true)
	got := cluster.HealthScore()
	assert.InEpsilon(t, 2.0/3.0, got, 1e-9, "2/3 nodes up = ~0.667")
}

func TestFakeCluster_HealthScore_Override(t *testing.T) {
	cluster := NewFakeCluster("n1")
	cluster.SetHealthOverride(func() float64 { return 0.42 })
	assert.InEpsilon(t, 0.42, cluster.HealthScore(), 1e-9)
}

// ---------------------------------------------------------------------------
// Table-driven tests — all four fault classes through a single healthy runner.
// ---------------------------------------------------------------------------

func TestAllFaultClasses_TableDriven(t *testing.T) {
	type faultSpec struct {
		name   string
		fault  Fault
		verify func(t *testing.T, before, after, restored NodeState)
	}

	tests := []faultSpec{
		{
			name:  "PodKill",
			fault: &PodKill{NodeID: "node-0"},
			verify: func(t *testing.T, before, after, restored NodeState) {
				assert.False(t, before.Down, "node must be up before inject")
				assert.True(t, after.Down, "node must be down after inject")
				assert.False(t, restored.Down, "node must be up after recover")
			},
		},
		{
			name:  "DiskStall",
			fault: &DiskStall{NodeID: "node-0"},
			verify: func(t *testing.T, before, after, restored NodeState) {
				assert.False(t, before.WritesStalled)
				assert.True(t, after.WritesStalled)
				assert.False(t, restored.WritesStalled)
			},
		},
		{
			name:  "ClockSkew",
			fault: &ClockSkew{NodeID: "node-0", SkewNanos: 1_000_000},
			verify: func(t *testing.T, before, after, restored NodeState) {
				assert.Equal(t, int64(0), before.ClockSkewNanos)
				assert.Equal(t, int64(1_000_000), after.ClockSkewNanos)
				assert.Equal(t, int64(0), restored.ClockSkewNanos)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := NewFakeCluster("node-0")
			ctx := context.Background()

			before, _ := cluster.NodeState("node-0")
			require.NoError(t, tt.fault.Inject(ctx, cluster))
			after, _ := cluster.NodeState("node-0")
			require.NoError(t, tt.fault.Recover(ctx, cluster))
			restored, _ := cluster.NodeState("node-0")

			tt.verify(t, before, after, restored)
		})
	}
}

// NetworkPartition needs two nodes so it gets its own sub-test.
func TestNetworkPartition_TableDriven(t *testing.T) {
	cluster := NewFakeCluster("A", "B")
	ctx := context.Background()
	fault := &NetworkPartition{NodeA: "A", NodeB: "B"}

	// Before
	beforeA, _ := cluster.NodeState("A")
	beforeB, _ := cluster.NodeState("B")
	assert.Empty(t, beforeA.PartitionedFrom)
	assert.Empty(t, beforeB.PartitionedFrom)

	// Inject
	require.NoError(t, fault.Inject(ctx, cluster))
	afterA, _ := cluster.NodeState("A")
	afterB, _ := cluster.NodeState("B")
	assert.True(t, afterA.PartitionedFrom["B"])
	assert.True(t, afterB.PartitionedFrom["A"])

	// Recover
	require.NoError(t, fault.Recover(ctx, cluster))
	restoredA, _ := cluster.NodeState("A")
	restoredB, _ := cluster.NodeState("B")
	assert.False(t, restoredA.PartitionedFrom["B"])
	assert.False(t, restoredB.PartitionedFrom["A"])
}
