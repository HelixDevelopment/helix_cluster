//go:build e2e

// This file is the REAL spawn-the-binary end-to-end proof for helix-advisory.
// It is gated behind the `e2e` build tag because it go-builds and spawns a real
// OS process, binds a real TCP port, and drives it over a real gRPC connection;
// run it explicitly with:
//
//	go test -tags e2e -run TestE2E ./cmd/helix-advisory/ -v
//
// It is NOT a mock and NOT skipped. It go-builds the binary, reserves a free
// loopback port (net.Listen :0 -> read addr -> close), launches the process with
// that port via the binary's real HELIX_ADVISORY_ADDR env var, dials it with a
// real grpc.ClientConn, polls the standard gRPC Health service until it reports
// SERVING, and then exercises the genuine end-user RPC surface:
//
//   - Health: grpc_health_v1.Check reports SERVING (the binary calls
//     hc.SetStatus(Healthy)) — readiness an end user / load balancer relies on.
//   - TryLock acquires an advisory lock (Acquired=true, non-empty LockId).
//   - A second TryLock by a DIFFERENT holder on the same resource is rejected
//     (Acquired=false) — real mutual-exclusion semantics.
//   - Unlock by the holder releases it (Released=true), after which a third
//     TryLock by the contending holder now succeeds — proving real state, not a
//     constant.
//   - Invalid args (empty resource) are rejected with codes.InvalidArgument
//     through the real RPC path.
//
// The process is killed cleanly at the end; evidence is written to qa-results/.
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

const (
	e2eAdvReadyTimeout = 25 * time.Second
	e2eAdvPollEvery    = 50 * time.Millisecond
	e2eAdvRPCTimeout   = 5 * time.Second
)

// e2eAdvFreePort reserves a free loopback TCP port via bind-:0/read-back/close
// and returns "127.0.0.1:<port>".
func e2eAdvFreePort(t *testing.T) string {
	t.Helper()
	l, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("reserve free port: %v", err)
	}
	addr := l.Addr().(*net.TCPAddr)
	_ = l.Close()
	return fmt.Sprintf("127.0.0.1:%d", addr.Port)
}

// e2eAdvBuild go-builds the helix-advisory binary to a temp path.
func e2eAdvBuild(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "helix-advisory-e2e")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build helix-advisory: %v\n%s", err, out)
	}
	return bin
}

// e2eAdvDial opens a real client connection to the spawned process.
func e2eAdvDial(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc dial %s: %v", addr, err)
	}
	return conn
}

// e2eAdvWaitServing polls the standard gRPC Health service until it answers
// SERVING or the deadline passes.
func e2eAdvWaitServing(t *testing.T, conn *grpc.ClientConn) {
	t.Helper()
	hc := grpc_health_v1.NewHealthClient(conn)
	deadline := time.Now().Add(e2eAdvReadyTimeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), e2eAdvRPCTimeout)
		resp, err := hc.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
		cancel()
		if err == nil && resp.GetStatus() == grpc_health_v1.HealthCheckResponse_SERVING {
			return
		}
		time.Sleep(e2eAdvPollEvery)
	}
	t.Fatalf("helix-advisory never reported gRPC health SERVING within %s", e2eAdvReadyTimeout)
}

// TestE2EAdvisorySpawnLockLifecycle spawns the REAL binary and proves the
// advisory-lock RPC surface works for an end user over a real gRPC connection.
func TestE2EAdvisorySpawnLockLifecycle(t *testing.T) {
	evDir := filepath.Join("..", "..", "qa-results", "live-20260622", "cmd_e2e4", "helix-advisory")
	if err := os.MkdirAll(evDir, 0o750); err != nil {
		t.Fatalf("mkdir evidence dir: %v", err)
	}
	var ev strings.Builder
	logf := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		t.Log(line)
		ev.WriteString(time.Now().UTC().Format(time.RFC3339Nano) + "  " + line + "\n")
	}
	defer func() { _ = os.WriteFile(filepath.Join(evDir, "e2e.log"), []byte(ev.String()), 0o640) }()

	bin := e2eAdvBuild(t)
	logf("BUILT binary: %s", bin)

	addr := e2eAdvFreePort(t)
	logf("RESERVED free port, launching with HELIX_ADVISORY_ADDR=%s", addr)

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "HELIX_ADVISORY_ADDR="+addr)
	var procOut strings.Builder
	cmd.Stdout = &procOut
	cmd.Stderr = &procOut
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn helix-advisory: %v", err)
	}
	pid := cmd.Process.Pid
	logf("SPAWNED process pid=%d addr=%s", pid, addr)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})

	conn := e2eAdvDial(t, addr)
	defer func() { _ = conn.Close() }()

	e2eAdvWaitServing(t, conn)
	logf("READY: gRPC Health reports SERVING (pid=%d)", pid)

	client := helixv1.NewAdvisoryServiceClient(conn)
	const resource = "e2e/advisory/resource-A"
	const holderA = "holder-A"
	const holderB = "holder-B"

	// ---- TryLock by holderA: acquires. ----
	ctx, cancel := context.WithTimeout(context.Background(), e2eAdvRPCTimeout)
	r1, err := client.TryLock(ctx, &helixv1.TryLockRequest{Resource: resource, Holder: holderA, TtlSeconds: 60})
	cancel()
	if err != nil {
		t.Fatalf("TryLock(holderA): %v", err)
	}
	if !r1.GetAcquired() || r1.GetLockId() == "" {
		t.Fatalf("TryLock(holderA) acquired=%v lockId=%q, want acquired=true with non-empty lockId", r1.GetAcquired(), r1.GetLockId())
	}
	logf("TryLock(holderA) ACQUIRED lockId=%s", r1.GetLockId())

	// ---- TryLock by holderB on the SAME resource: rejected (mutual exclusion). ----
	ctx, cancel = context.WithTimeout(context.Background(), e2eAdvRPCTimeout)
	r2, err := client.TryLock(ctx, &helixv1.TryLockRequest{Resource: resource, Holder: holderB, TtlSeconds: 60})
	cancel()
	if err != nil {
		t.Fatalf("TryLock(holderB): %v", err)
	}
	if r2.GetAcquired() {
		t.Fatalf("TryLock(holderB) acquired=true on a held resource — mutual exclusion broken")
	}
	logf("TryLock(holderB) correctly REJECTED while holderA holds the lock (mutual exclusion)")

	// ---- Unlock by holderA: releases. ----
	ctx, cancel = context.WithTimeout(context.Background(), e2eAdvRPCTimeout)
	rel, err := client.Unlock(ctx, &helixv1.UnlockRequest{Resource: resource, Holder: holderA})
	cancel()
	if err != nil {
		t.Fatalf("Unlock(holderA): %v", err)
	}
	if !rel.GetReleased() {
		t.Fatalf("Unlock(holderA) released=false, want true")
	}
	logf("Unlock(holderA) RELEASED the lock")

	// ---- TryLock by holderB now succeeds — proves real released state. ----
	ctx, cancel = context.WithTimeout(context.Background(), e2eAdvRPCTimeout)
	r3, err := client.TryLock(ctx, &helixv1.TryLockRequest{Resource: resource, Holder: holderB, TtlSeconds: 60})
	cancel()
	if err != nil {
		t.Fatalf("TryLock(holderB, after release): %v", err)
	}
	if !r3.GetAcquired() || r3.GetLockId() == "" {
		t.Fatalf("TryLock(holderB, after release) acquired=%v lockId=%q, want acquired=true", r3.GetAcquired(), r3.GetLockId())
	}
	logf("TryLock(holderB) ACQUIRED after release lockId=%s — state is real, not constant", r3.GetLockId())

	// ---- Invalid args rejected with InvalidArgument through the real RPC. ----
	ctx, cancel = context.WithTimeout(context.Background(), e2eAdvRPCTimeout)
	_, err = client.TryLock(ctx, &helixv1.TryLockRequest{Resource: "", Holder: holderA})
	cancel()
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("TryLock(empty resource) code=%v, want InvalidArgument", status.Code(err))
	}
	logf("TryLock(empty resource) correctly rejected with InvalidArgument")

	// Clean kill.
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	logf("KILLED process pid=%d cleanly. PASS. Evidence in %s", pid, evDir)

	readme := fmt.Sprintf(`# helix-advisory spawn-the-binary E2E evidence

Run: %s (UTC)
Binary: %s
Listen addr via HELIX_ADVISORY_ADDR (reserved free loopback port): %s

## Proven (real spawned process, real gRPC over TCP)
- Standard gRPC Health service reports SERVING.
- TryLock(holderA) acquires (non-empty LockId).
- TryLock(holderB) on the same resource is rejected while held (mutual exclusion).
- Unlock(holderA) releases (Released=true).
- TryLock(holderB) then succeeds — proving real released state, not a constant.
- TryLock with empty resource is rejected with codes.InvalidArgument.

See e2e.log for the timestamped trace.
`, time.Now().UTC().Format(time.RFC3339), bin, addr)
	_ = os.WriteFile(filepath.Join(evDir, "README.md"), []byte(readme), 0o640)
}
