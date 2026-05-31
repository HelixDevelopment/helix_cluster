package infra

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// NOTE (PCS-6 / CLAUDE-1): Every test in this file exercises the
// simulatedOrchestrator — a NON-PRODUCTION fake that boots NOTHING, probes
// NOTHING, streams NO logs, creates NO real replicas, and spawns NO real VMs.
// These tests therefore assert ONLY what the simulation genuinely guarantees:
// its in-memory bookkeeping state machine (map insert / lookup / mutate /
// delete) and the partition auto-heal timer. They do NOT — and MUST NOT be
// read to — prove that any service is actually running, healthy, reachable, or
// usable by an end user. The canned literals ("running", Healthy==true, IP
// 10.0.0.N, "ssh user@...") are values the fake fabricates unconditionally;
// asserting them proves only that the fake wrote what it was coded to write.
//
// Real end-user-visible proof (a booted NATS that answers a health probe, a
// non-existent service reporting unhealthy, real log output, real replicas,
// a reachable SSH session) lives in infra_integration_test.go behind the
// `integration` build tag.

// TestOrchestrator_IsSimulated is the load-bearing honesty guard: the default
// constructor returns the NON-PRODUCTION simulation. If a real orchestrator is
// ever wired in as the default, this assertion MUST be revisited deliberately.
func TestOrchestrator_IsSimulated(t *testing.T) {
	o := NewOrchestrator(DefaultConfig())
	require.NotNil(t, o)
	assert.True(t, o.Simulated(), "default orchestrator MUST self-identify as the NON-PRODUCTION simulation")

	// The interface seam exists so a real Orchestrator can be substituted.
	var _ Orchestrator = o
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	require.NotNil(t, cfg)
	assert.Equal(t, "helix-cluster", cfg.ProjectName)
	assert.Equal(t, 5*time.Minute, cfg.DefaultTimeout)
	assert.NotEmpty(t, cfg.Services)
}

func TestNewOrchestrator(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	require.NotNil(t, o)
	assert.NotNil(t, o.services)
	assert.NotNil(t, o.vmNodes)
}

// TestInfraOrchestrator_Boot_Simulated verifies the simulation's bookkeeping:
// Boot records an entry per requested service and reflects it back. NO real
// service is started; the returned "running"/Healthy values are canned and
// prove nothing about real boot.
func TestInfraOrchestrator_Boot_Simulated(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	ctx := context.Background()

	// Bookkeeping: one record per requested service name.
	results, err := o.Boot(ctx, []string{"postgres-primary", "redis-master-1"})
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, "postgres-primary", results[0].Name)
	assert.Equal(t, "redis-master-1", results[1].Name)

	// The recorded entries are retrievable via Status (same map). This is the
	// honest guarantee: insert-then-read consistency, NOT that postgres booted.
	st, err := o.Status(ctx, []string{"postgres-primary"})
	require.NoError(t, err)
	require.Len(t, st, 1)
	assert.Equal(t, "postgres-primary", st[0].Name)

	// Booting with no explicit list records every configured service.
	results2, err := o.Boot(ctx, nil)
	require.NoError(t, err)
	assert.Len(t, results2, len(cfg.Services))
}

// TestInfraOrchestrator_Stop_Simulated verifies the state transition the
// simulation actually performs: a booted entry flips to "stopped"/unhealthy.
func TestInfraOrchestrator_Stop_Simulated(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	ctx := context.Background()

	_, _ = o.Boot(ctx, []string{"postgres-primary"})
	results, err := o.Stop(ctx, []string{"postgres-primary"}, false)
	require.NoError(t, err)
	require.Len(t, results, 1)
	// Honest bookkeeping assertion: Stop mutated the stored record. No real
	// process was signalled.
	assert.Equal(t, "stopped", results[0].Status)
	assert.False(t, results[0].Healthy)

	// The mutation is observable on re-read (map consistency).
	st, _ := o.Status(ctx, []string{"postgres-primary"})
	require.Len(t, st, 1)
	assert.Equal(t, "stopped", st[0].Status)
}

func TestInfraOrchestrator_Stop_WithVolumes_Simulated(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	ctx := context.Background()

	_, _ = o.Boot(ctx, []string{"postgres-primary"})
	results, err := o.Stop(ctx, []string{"postgres-primary"}, true)
	require.NoError(t, err)
	// The removeVolumes flag only alters the canned message string. No volume
	// exists to remove.
	assert.Contains(t, results[0].Message, "volumes removed")
}

// TestInfraOrchestrator_Status_Simulated verifies lookup hit/miss bookkeeping.
func TestInfraOrchestrator_Status_Simulated(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	ctx := context.Background()

	_, _ = o.Boot(ctx, []string{"postgres-primary"})
	results, err := o.Status(ctx, []string{"postgres-primary"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "postgres-primary", results[0].Name)

	// Not-found path: a name never booted reports the not_found sentinel. This
	// is the only "negative" the fake can produce — absence from the map, NOT a
	// real failed probe.
	results2, err := o.Status(ctx, []string{"nonexistent"})
	require.NoError(t, err)
	require.Len(t, results2, 1)
	assert.Equal(t, "not_found", results2[0].Status)
	assert.False(t, results2[0].Healthy)
}

// TestInfraOrchestrator_Health_Simulated documents the simulation's central
// limitation: Health is CANNED. It echoes the Healthy flag Boot set and a
// constant 1ms latency; it never opens a socket. The honest assertions here are
// (a) a known service is echoed back, and (b) an UNKNOWN service is reported
// not_found/unhealthy. There is deliberately NO assertion that a "healthy"
// result means anything is reachable — because the fake cannot prove that.
func TestInfraOrchestrator_Health_Simulated(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	ctx := context.Background()

	_, _ = o.Boot(ctx, []string{"postgres-primary"})

	results, err := o.Health(ctx, []string{"postgres-primary"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "postgres-primary", results[0].Name)
	// The simulation hardcodes Latency to a constant — assert that constant
	// EXACTLY, to make it unmistakable in review that this is NOT a measured
	// round-trip. A real Health probe MUST replace this with a measured value.
	assert.Equal(t, 1*time.Millisecond, results[0].Latency,
		"simulated Health returns a constant latency; it does not probe anything")

	// The only unhealthy verdict the fake can emit is for a service absent from
	// the map. A real probe would also report unhealthy for a DOWN-but-known
	// service; the simulation structurally cannot, which is why the real
	// failure-path lives in infra_integration_test.go.
	unknown, err := o.Health(ctx, []string{"never-booted"})
	require.NoError(t, err)
	require.Len(t, unknown, 1)
	assert.Equal(t, "not_found", unknown[0].Status)
	assert.False(t, unknown[0].Healthy)
}

// TestInfraOrchestrator_Logs_Simulated verifies only the existence check the
// fake performs. The simulation produces NO log output and ignores
// follow/tail/since/timestamps; we assert solely the error/no-error branch.
func TestInfraOrchestrator_Logs_Simulated(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	ctx := context.Background()

	_, _ = o.Boot(ctx, []string{"postgres-primary"})
	// Known service: passes the map existence check. No output is produced or
	// asserted — the fake streams nothing.
	err := o.Logs(ctx, "postgres-primary", true, 100, "", true)
	assert.NoError(t, err)

	// Unknown service: the only real behavior here is the not-found error.
	err = o.Logs(ctx, "nonexistent", true, 100, "", true)
	assert.Error(t, err)
}

// TestInfraOrchestrator_Scale_Simulated verifies the field write only. No
// replica is created; Scale mutates Replicas and the message string.
func TestInfraOrchestrator_Scale_Simulated(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	ctx := context.Background()

	_, _ = o.Boot(ctx, []string{"postgres-primary"})
	status, err := o.Scale(ctx, "postgres-primary", 3)
	require.NoError(t, err)
	// Honest assertion: the field round-trips. This does NOT mean 3 replicas
	// exist anywhere — only that the struct field was set.
	assert.Equal(t, 3, status.Replicas)

	// The mutation is observable on re-read.
	st, _ := o.Status(ctx, []string{"postgres-primary"})
	require.Len(t, st, 1)
	assert.Equal(t, 3, st[0].Replicas)
}

// TestInfraOrchestrator_VMSpawn_Simulated verifies the map grows by `count` and
// IDs are unique. The CPU/Memory/IP values are fabricated constants and are
// NOT asserted as proof of a real VM — only that distinct records were created.
func TestInfraOrchestrator_VMSpawn_Simulated(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	ctx := context.Background()

	nodes, err := o.VMSpawn(ctx, 3)
	require.NoError(t, err)
	require.Len(t, nodes, 3)

	// Honest bookkeeping: three distinct records now exist and are listable.
	ids := map[string]struct{}{}
	for _, n := range nodes {
		ids[n.ID] = struct{}{}
	}
	assert.Len(t, ids, 3, "spawned nodes must have unique IDs")

	list, err := o.VMList(ctx)
	require.NoError(t, err)
	assert.Len(t, list, 3)
}

func TestInfraOrchestrator_VMDestroy_Simulated(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	ctx := context.Background()

	nodes, _ := o.VMSpawn(ctx, 1)
	err := o.VMDestroy(ctx, nodes[0].ID)
	assert.NoError(t, err)

	list, _ := o.VMList(ctx)
	assert.Len(t, list, 0)

	// Destroying a non-existent node is the honest not-found error.
	err = o.VMDestroy(ctx, "nonexistent")
	assert.Error(t, err)
}

func TestInfraOrchestrator_VMList_Simulated(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	ctx := context.Background()

	_, _ = o.VMSpawn(ctx, 2)
	list, err := o.VMList(ctx)
	require.NoError(t, err)
	assert.Len(t, list, 2)
}

func TestInfraOrchestrator_VMStatus_Simulated(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	ctx := context.Background()

	nodes, _ := o.VMSpawn(ctx, 1)
	status, err := o.VMStatus(ctx, nodes[0].ID)
	require.NoError(t, err)
	assert.Equal(t, nodes[0].ID, status.ID)

	_, err = o.VMStatus(ctx, "nonexistent")
	assert.Error(t, err)
}

// TestInfraOrchestrator_VMSSH_Simulated verifies the error branches only. The
// success branch returns a formatted "ssh user@<fabricated-ip>" string for a
// host that does NOT exist and is NOT reachable; we deliberately do NOT assert
// that string is a working SSH target. Real SSH reachability is proven only in
// infra_integration_test.go.
func TestInfraOrchestrator_VMSSH_Simulated(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	ctx := context.Background()

	nodes, _ := o.VMSpawn(ctx, 1)
	cmd, err := o.VMSSH(ctx, nodes[0].ID)
	require.NoError(t, err)
	// We assert non-empty only — NOT that this command connects anywhere.
	assert.NotEmpty(t, cmd)

	_, err = o.VMSSH(ctx, "nonexistent")
	assert.Error(t, err)
}

// TestInfraOrchestrator_VMSimulateFailure_Simulated verifies the in-map status
// flip. The method name is honest: it SIMULATES failure by mutating a string;
// it injects no real fault.
func TestInfraOrchestrator_VMSimulateFailure_Simulated(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	ctx := context.Background()

	nodes, _ := o.VMSpawn(ctx, 1)
	err := o.VMSimulateFailure(ctx, nodes[0].ID)
	require.NoError(t, err)

	status, _ := o.VMStatus(ctx, nodes[0].ID)
	assert.Equal(t, "failed", status.Status)

	err = o.VMSimulateFailure(ctx, "nonexistent")
	assert.Error(t, err)
}

// TestInfraOrchestrator_VMSimulatePartition_Simulated is the one genuinely
// non-trivial behavior in the simulation: a background timer flips the in-map
// status partitioned -> running after the duration elapses. This asserts real
// concurrent state-machine behavior (still purely in-memory; no real network is
// partitioned).
func TestInfraOrchestrator_VMSimulatePartition_Simulated(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	ctx := context.Background()

	nodes, _ := o.VMSpawn(ctx, 1)
	err := o.VMSimulatePartition(ctx, nodes[0].ID, 100*time.Millisecond)
	require.NoError(t, err)

	status, _ := o.VMStatus(ctx, nodes[0].ID)
	assert.Equal(t, "partitioned", status.Status)

	// Auto-heal: the timer goroutine restores "running".
	require.Eventually(t, func() bool {
		s, _ := o.VMStatus(ctx, nodes[0].ID)
		return s.Status == "running"
	}, 1*time.Second, 10*time.Millisecond, "partition must auto-heal to running")

	err = o.VMSimulatePartition(ctx, "nonexistent", time.Second)
	assert.Error(t, err)
}
