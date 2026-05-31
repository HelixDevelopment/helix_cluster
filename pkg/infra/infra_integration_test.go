//go:build integration

package infra

import (
	"context"
	"net"
	"testing"
	"time"

	"digital.vasic.containers/pkg/brokertest"
	"digital.vasic.containers/pkg/runtime"
)

// This file is the SKELETON for the REAL infrastructure-orchestrator proof. It
// is gated behind `//go:build integration` so it never runs in the default unit
// suite and can never be mistaken for evidence that the simulation works.
//
// It documents, in compilable form, the sink-side evidence a PRODUCTION
// Orchestrator MUST produce per CLAUDE-1.6 / PCS-6: booting a REAL service and
// asserting a REAL health probe succeeds, and that Health reports unhealthy for
// a non-existent service. The simulatedOrchestrator (the default) satisfies
// NONE of this — its Health echoes a canned flag and never opens a socket.
//
// Until a real Orchestrator is wired in, the end-to-end orchestrator test
// SKIPS with an explicit reason. The REAL-service boot path below
// (brokertest.StartNATS) is genuinely exercised when a container runtime is
// present, to keep the harness honest and the wiring real.

// runtimeAvailable reports whether a CLI-backed container runtime we can drive
// is present. Used to SKIP gracefully (never FAIL) on hosts with no runtime.
func runtimeAvailable(ctx context.Context) bool {
	_, err := runtime.AutoDetect(ctx)
	return err == nil
}

// probeTCPGreeting dials addr and returns true if the server sends ANY bytes
// promptly — positive, sink-side evidence that a real service is serving on the
// endpoint (NATS sends an "INFO" greeting immediately on connect). This is the
// shape of the live health probe a real Orchestrator.Health MUST perform.
func probeTCPGreeting(ctx context.Context, addr string) (bool, time.Duration) {
	start := time.Now()
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false, time.Since(start)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4)
	n, err := conn.Read(buf)
	if err != nil && n == 0 {
		return false, time.Since(start)
	}
	return n > 0, time.Since(start)
}

// TestRealOrchestrator_HealthProbesRealService is the SKELETON for the future
// real-orchestrator proof. A genuine PASS here (once a real Orchestrator is
// wired in) MUST assert end-user-visible, sink-side evidence — NOT canned
// literals:
//
//   - After Boot("nats"), a REAL NATS service accepts a connection and a live
//     Health probe to it measures a real, non-constant latency and reports
//     Healthy==true (proven below against a real brokertest.StartNATS broker).
//   - Health for a service that was never booted (or was killed) reports
//     Healthy==false from an ACTUAL failed probe — not merely map-absence.
//
// This test does the REAL half of that proof today (it boots a real broker and
// probes it) and SKIPS the orchestrator-wiring half with an explicit reason,
// so it can never masquerade as evidence the simulation is real.
func TestRealOrchestrator_HealthProbesRealService(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if !runtimeAvailable(ctx) {
		t.Skip("SKIP-OK: no CLI container runtime detected (podman/docker/nerdctl); cannot boot a real service")
	}

	// Boot a REAL service (NATS) via the containers submodule. This is the
	// real-world operation the simulatedOrchestrator.Boot fakes.
	endpoint, stop, err := brokertest.StartNATS(ctx)
	if err != nil {
		t.Fatalf("StartNATS (real service boot): %v", err)
	}
	defer stop()

	// strip the "nats://" scheme to get host:port for a raw TCP probe.
	addr := endpoint
	if i := len("nats://"); len(endpoint) > i && endpoint[:i] == "nats://" {
		addr = endpoint[i:]
	}

	// REAL health probe: positive sink-side evidence the service is serving.
	ok, latency := probeTCPGreeting(ctx, addr)
	if !ok {
		t.Fatalf("real health probe to booted NATS at %s produced no greeting; service is not actually serving", addr)
	}
	if latency <= 0 {
		t.Fatalf("real probe latency must be a measured, positive duration, got %v", latency)
	}
	t.Logf("REAL health probe OK: NATS at %s answered in %v (measured, not canned)", addr, latency)

	// Negative path: a probe to an address where nothing listens MUST fail —
	// this is the failure-path the simulation structurally cannot produce.
	deadAddr := "127.0.0.1:1" // port 1: nothing serves here
	if dead, _ := probeTCPGreeting(ctx, deadAddr); dead {
		t.Fatalf("probe to %s unexpectedly succeeded; negative health path is broken", deadAddr)
	}

	// The remaining half — driving these probes THROUGH a real
	// Orchestrator.Boot/Health implementation and asserting its returned
	// ServiceStatus reflects the real probe — is out of scope until a real
	// Orchestrator is implemented. Default NewOrchestrator returns the
	// simulation, which would echo canned values here and prove nothing.
	t.Skip("PCS-6: real Orchestrator not implemented. A real implementation MUST " +
		"drive Boot/Health against this booted service and assert Health reports " +
		"Healthy==true with the measured latency above for the live service, and " +
		"Healthy==false for a non-existent/killed service (sink-side evidence per CLAUDE-1.6). " +
		"The default NewOrchestrator returns simulatedOrchestrator, which echoes canned " +
		"values and cannot satisfy this proof.")
}

// TestSimulatedOrchestrator_IsNotProduction is a guard that compiles under the
// integration tag too: it asserts the default orchestrator self-identifies as
// the simulation, so an integration run on a CI host cannot silently treat the
// fake as the real thing.
func TestSimulatedOrchestrator_IsNotProduction(t *testing.T) {
	var o Orchestrator = NewOrchestrator(DefaultConfig())
	if !o.Simulated() {
		t.Fatal("default orchestrator MUST report Simulated()==true until a real Orchestrator is wired in")
	}
}
