//go:build integration

package infra

// infra_container_integration_test.go — §7.1 REAL integration tests for the
// containerOrchestrator (HXC-1010 through HXC-1015).
//
// Build tag: //go:build integration
// Run command: go test -tags=integration -v ./pkg/infra/...
//
// Prerequisites (orchestrator gates via the brokertest / runtime seams):
//   - podman (rootless) installed and running (runtime.AutoDetect succeeds)
//   - Internet access or cached images for docker.io/library/redis:7
//
// The tests SKIP gracefully (never FAIL) when the container runtime is absent.
// They exercise the REAL path per CLAUDE-1: Boot a real redis-master, verify
// via TCP dial and runtime.Exec(PING), Scale to 3 and verify via runtime.List,
// Logs tail finds an emitted marker, and VMSSH over a real SSH-in-container.

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"digital.vasic.containers/pkg/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// integrationRuntime returns the auto-detected real runtime or skips the test.
func integrationRuntime(t *testing.T) runtime.ContainerRuntime {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rt, err := runtime.AutoDetect(ctx)
	if err != nil {
		t.Skipf("SKIP-OK: no container runtime available: %v", err)
	}
	return rt
}

// realOrchestrator builds a containerOrchestrator wired to the system runtime.
func realOrchestrator(t *testing.T, rt runtime.ContainerRuntime) *containerOrchestrator {
	t.Helper()
	cfg := DefaultContainerOrchestratorConfig(DefaultConfig(), rt)
	cfg.HealthProbeTimeout = 3 * time.Second
	cfg.BootReadinessTimeout = 60 * time.Second
	// Register the redis port for readiness probing and publish it from the
	// real container so the host-side TCP probe can reach it.
	cfg.ServicePorts["redis-master"] = "127.0.0.1:16379"
	cfg.ContainerImages["redis-master"] = "docker.io/library/redis:7"
	cfg.ContainerPorts["redis-master"] = "16379:6379"
	o, err := NewContainerOrchestrator(cfg)
	require.NoError(t, err)
	return o
}

// cleanupContainer forcibly removes a named container after the test.
func cleanupContainer(ctx context.Context, rt runtime.ContainerRuntime, name string) {
	_ = rt.Stop(ctx, name)
	_ = rt.Remove(ctx, name, runtime.WithForceRemove(true), runtime.WithRemoveVolumes(true))
}

// ─── HXC-1010 integration: Boot real ─────────────────────────────────────────

// TestRealOrchestrator_Boot_StartsRedisAndProbesPing boots a real redis-master
// container and proves it is serving by issuing a PING command via
// runtime.Exec, which returns "+PONG". This is sink-side evidence per §7.1.
func TestRealOrchestrator_Boot_StartsRedisAndProbesPing(t *testing.T) {
	rt := integrationRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	defer cleanupContainer(context.Background(), rt, "redis-master")

	o := realOrchestrator(t, rt)

	// Boot MUST actually start a real redis container and report it Healthy
	// only after the TCP readiness probe on the published port succeeds. With
	// podman present this is a HARD assertion — no skip-on-failure escape, per
	// CLAUDE-1 (a skip on a broken feature is a PASS-bluff).
	results, err := o.Boot(ctx, []string{"redis-master"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Truef(t, results[0].Healthy,
		"Boot must report redis-master Healthy after a real readiness probe; status=%s msg=%s",
		results[0].Status, results[0].Message)

	// Sink-side proof: redis-server answers PING with PONG over runtime.Exec.
	result, execErr := rt.Exec(ctx, "redis-master", []string{"redis-cli", "PING"})
	require.NoError(t, execErr, "redis EXEC must succeed against the booted container")
	assert.Contains(t, strings.TrimSpace(result.Stdout), "PONG",
		"redis PING via runtime.Exec must return PONG — sink-side proof the service is serving")
}

// ─── HXC-1011 integration: Health real ───────────────────────────────────────

// TestRealOrchestrator_Health_TCPDialToRunningService confirms that a live
// Health probe to a booted service measures a real (non-constant) latency.
func TestRealOrchestrator_Health_TCPDialToRunningService(t *testing.T) {
	// Open an in-process listener to represent a "service" — no real container
	// needed for this proof because Health only needs a TCP target.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	addr := ln.Addr().String()

	rt := integrationRuntime(t)
	o := realOrchestrator(t, rt)
	o.cfg.ServicePorts["probe-target"] = addr
	o.cfg.HealthProbeTimeout = 2 * time.Second

	ctx := context.Background()

	// HEALTHY path.
	results, err := o.Health(ctx, []string{"probe-target"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.True(t, results[0].Healthy,
		"Health must report Healthy==true when the service is listening")
	assert.Greater(t, results[0].Latency, time.Duration(0),
		"latency must be measured and positive — not a constant")
	t.Logf("REAL health probe: latency=%v (measured, not canned)", results[0].Latency)

	// UNHEALTHY path: close the listener.
	ln.Close()
	o.cfg.HealthProbeTimeout = 300 * time.Millisecond
	results2, err := o.Health(ctx, []string{"probe-target"})
	require.NoError(t, err)
	require.Len(t, results2, 1)
	assert.False(t, results2[0].Healthy,
		"Health must report Healthy==false after the listener is closed")
}

// ─── HXC-1012 integration: Logs real ─────────────────────────────────────────

// TestRealOrchestrator_Logs_TailFindMarkerInRealContainer tails a real
// container log and asserts an emitted marker appears.
func TestRealOrchestrator_Logs_TailFindMarkerInRealContainer(t *testing.T) {
	rt := integrationRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	const svc = "helix-log-test"
	defer cleanupContainer(context.Background(), rt, svc)

	const marker = "HELIX-INTEG-LOG-MARKER-9c3f"

	// Boot a real busybox container that emits the marker to stdout and stays
	// alive so its logs can be tailed. This exercises the REAL run path.
	o := realOrchestrator(t, rt)
	o.cfg.ContainerImages[svc] = "docker.io/library/busybox:latest"
	o.cfg.ContainerCommand[svc] = []string{"sh", "-c", fmt.Sprintf("echo %s; sleep 30", marker)}

	_, err := o.Boot(ctx, []string{svc})
	require.NoError(t, err, "Boot of log-emitting container must succeed")

	// Give the container a moment to emit the marker line.
	time.Sleep(1 * time.Second)

	// Tail the last 20 lines via the real runtime's Logs stream — hard failure
	// (no skip) when podman is present.
	rc, logsErr := rt.Logs(ctx, svc, runtime.WithTail("20"))
	require.NoError(t, logsErr, "real container logs must be retrievable")
	defer rc.Close()

	var buf strings.Builder
	tmp := make([]byte, 4096)
	for {
		n, rerr := rc.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
		}
		if rerr != nil {
			break
		}
	}

	assert.Contains(t, buf.String(), marker,
		"real log tail must contain the emitted marker — sink-side proof of real log streaming")
}

// ─── HXC-1013 integration: Scale real ────────────────────────────────────────

// TestRealOrchestrator_Scale_ThreeReplicasVerifiedByList scales a service to 3
// and asserts runtime.List reports exactly 3 running containers.
func TestRealOrchestrator_Scale_ThreeReplicasVerifiedByList(t *testing.T) {
	rt := integrationRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	o := realOrchestrator(t, rt)
	const svc = "scale-test"
	// Real, lightweight, long-lived replicas via busybox sleep.
	o.cfg.ContainerImages[svc] = "docker.io/library/busybox:latest"
	o.cfg.ContainerCommand[svc] = []string{"sleep", "90"}
	for i := 1; i <= 3; i++ {
		defer cleanupContainer(context.Background(), rt, fmt.Sprintf("%s-%d", svc, i))
	}

	// Scale to 3 — HARD assertion (no skip) when podman is present.
	status, err := o.Scale(ctx, svc, 3)
	require.NoError(t, err, "Scale must create real replicas")

	// Verify via runtime.List — the ground-truth running replica count.
	list, listErr := rt.List(ctx, runtime.ListFilter{
		All:    false,
		Names:  []string{svc},
		Status: []runtime.ContainerState{runtime.StateRunning},
	})
	require.NoError(t, listErr, "runtime.List must succeed")

	assert.Equal(t, 3, len(list),
		"runtime.List must report exactly 3 running replicas — real replica proof")
	assert.Equal(t, len(list), status.Replicas,
		"Scale.Replicas must match the count from runtime.List")
	t.Logf("REAL scale proof: runtime.List reports %d running replicas", len(list))
}

// ─── HXC-1014 integration: VMSpawn/VMSSH real session ─────────────────────────

// TestRealVMSSH_EchoOkOverRealSession spawns ONE real local-container VM node
// via real podman, establishes a REAL command session into it via VMSSH, and
// asserts stdout=="ok". This is the sink-side proof for HXC-1014: VMSpawn/VMSSH
// establish a real session end-to-end and run 'echo ok' over it.
//
// It HARD-FAILS (no skip) when a container runtime is available — a skip on a
// present runtime would be a PASS-bluff per CLAUDE-1. It only skips when
// runtime.AutoDetect fails (no runtime at all).
func TestRealVMSSH_EchoOkOverRealSession(t *testing.T) {
	rt := integrationRuntime(t) // SKIPs only when AutoDetect fails
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	o := realOrchestrator(t, rt)
	// No RemoteExecutor is wired, so VMSSH uses the real container-session path
	// (runtime.Exec into the node container). The default VMNodeImage
	// (busybox) + VMNodeCommand (sleep 3600) keep the node alive.

	// VMSpawn names the first node deterministically ("vm-1"); pre-clean any
	// leftover of that name from a crashed prior run so the create cannot
	// collide, and defer cleanup of both candidate node names.
	cleanupContainer(context.Background(), rt, "vm-1")
	defer cleanupContainer(context.Background(), rt, "vm-1")

	nodes, err := o.VMSpawn(ctx, 1)
	require.NoError(t, err, "VMSpawn must create a real local-container node")
	require.Len(t, nodes, 1)
	nodeID := nodes[0].ID
	defer cleanupContainer(context.Background(), rt, nodeID)

	stdout, err := o.VMSSH(ctx, nodeID)
	require.NoError(t, err, "VMSSH must establish a real session into the node and run echo ok")
	require.Equal(t, "ok", stdout,
		"VMSSH must return 'ok' from the real command session into the node — sink-side proof")
	t.Logf("REAL VMSSH proof: ran 'echo ok' over a real session into node %s, stdout=%q", nodeID, stdout)
}

// ─── HXC-1015 integration: Partition observable ───────────────────────────────

// TestRealOrchestrator_Partition_ObservableEffect verifies the partition/recover
// cycle against a real in-process listener (no container needed): during
// partition the real listener is there but HealthVM still reports false,
// and after recovery it reports true.
func TestRealOrchestrator_Partition_ObservableEffect(t *testing.T) {
	rt := integrationRuntime(t)
	o := realOrchestrator(t, rt)
	o.cfg.HealthProbeTimeout = 300 * time.Millisecond
	ctx := context.Background()

	// VMSpawn now creates a REAL node container ("vm-1"); pre-clean any leftover
	// and clean up after so this test does not leak a container that would
	// collide with other VMSpawn-using tests.
	cleanupContainer(context.Background(), rt, "vm-1")
	nodes, err := o.VMSpawn(ctx, 1)
	require.NoError(t, err)
	nodeID := nodes[0].ID
	defer cleanupContainer(context.Background(), rt, nodeID)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	addr := ln.Addr().String()

	// Pre-partition: healthy.
	h, lat := o.HealthVM(ctx, nodeID, addr)
	require.True(t, h, "pre-partition probe must succeed")
	assert.Greater(t, lat, time.Duration(0))
	t.Logf("REAL pre-partition probe: latency=%v", lat)

	// Partition for 300ms.
	require.NoError(t, o.VMSimulatePartition(ctx, nodeID, 300*time.Millisecond))

	// During partition: unhealthy despite live listener.
	h2, _ := o.HealthVM(ctx, nodeID, addr)
	assert.False(t, h2, "during partition probe must fail (observable network effect)")

	// After recovery.
	require.Eventually(t, func() bool {
		h3, _ := o.HealthVM(ctx, nodeID, addr)
		return h3
	}, 2*time.Second, 20*time.Millisecond,
		"probe must recover after partition duration elapses")
	t.Logf("REAL partition effect: probe flipped false during partition and recovered")
}
