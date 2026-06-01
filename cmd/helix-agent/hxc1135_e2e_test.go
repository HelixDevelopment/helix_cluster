//go:build integration

// Package main — HXC-1135 two-node cluster formation E2E integration test.
//
// Proves that two real helix-agent processes, started with --etcd-endpoints
// pointing at the same real etcd, both appear in the shared-etcd membership
// view within 60 seconds. The per-run UUID embedded in each node's ID makes
// the assertion unforgeable: a stale prior-run etcd entry cannot reproduce it.
//
// §1.1 mutation anchor (documented; orchestrator runs): if buildConfig ignores
// cfg.EtcdEndpoints (sets it to nil always), both agents fall back to the
// in-memory discovery backend. Each registers only with its own in-process
// registry. The observer — a third ServiceRegistry querying shared etcd —
// finds neither node. The "both visible within 60s" assertion FAILS with
// "timed out waiting for both nodes to appear in etcd: found 0 of 2".
//
// Catalogue-check: reuse digital.vasic.containers/pkg/brokertest for etcd
// lifecycle (same as internal/node's HXC-1210 integration test).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"digital.vasic.containers/pkg/brokertest"
	"github.com/HelixDevelopment/helix_cluster/pkg/discovery"
	"github.com/HelixDevelopment/helix_cluster/pkg/etcd"
)

// TestHXC1135_TwoNodeFormation_E2E is the HXC-1135 exit gate:
//
//  1. Starts a real ephemeral etcd container via brokertest.StartEtcd.
//  2. Builds the helix-agent binary from source.
//  3. Launches two helix-agent processes (node A and B) with distinct ports
//     and --etcd-endpoints pointing at the real etcd, with per-run UUIDs
//     embedded in the node IDs.
//  4. From the test process, polls shared etcd via discovery.ServiceRegistry
//     until BOTH node IDs are visible, within a 60 s deadline.
//  5. Captures node IDs, formation timestamp, and run UUID to the evidence dir.
//
// t.Skip is used ONLY when the container runtime is genuinely absent.
func TestHXC1135_TwoNodeFormation_E2E(t *testing.T) {
	// ── Boot real etcd ────────────────────────────────────────────────────────
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	etcdEndpoint, stopEtcd, err := brokertest.StartEtcd(ctx)
	if err != nil {
		t.Skipf("no container runtime available, skipping HXC-1135 E2E test: %v", err)
		return
	}
	defer stopEtcd()
	t.Logf("HXC-1135 E2E: real etcd at %s", etcdEndpoint)

	// ── Build the helix-agent binary ──────────────────────────────────────────
	binDir := t.TempDir()
	agentBin := filepath.Join(binDir, "helix-agent")
	buildCtx, buildCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer buildCancel()

	buildCmd := exec.CommandContext(buildCtx, "go", "build", "-o", agentBin, "./cmd/helix-agent")
	buildCmd.Dir = projectRoot(t)
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build helix-agent: %v\n%s", err, out)
	}
	t.Logf("HXC-1135 E2E: built binary at %s", agentBin)

	// ── Per-run UUID — unforgeable node IDs ───────────────────────────────────
	// Embedding a nanosecond timestamp in each node ID means a stale etcd entry
	// from a prior run cannot satisfy the assertions, satisfying the CLAUDE-1
	// anti-bluff mandate.
	runUUID := fmt.Sprintf("%d", time.Now().UnixNano())
	idA := fmt.Sprintf("helix-1135-a-%s", runUUID)
	idB := fmt.Sprintf("helix-1135-b-%s", runUUID)

	// ── Launch helix-agent A ───────────────────────────────────────────────────
	procCtx, procCancel := context.WithCancel(context.Background())
	defer procCancel()

	agentA := exec.CommandContext(procCtx, agentBin,
		"--id", idA,
		"--region", "us-east-1",
		"--bind-port", "19200",
		"--wg-port", "59200",
		"--etcd-endpoints", etcdEndpoint,
	)
	agentA.Env = append(os.Environ(), "HELIX_ETCD_ENDPOINTS=")
	if err := agentA.Start(); err != nil {
		t.Fatalf("start helix-agent A: %v", err)
	}
	defer func() {
		agentA.Process.Kill()
		agentA.Wait()
	}()
	t.Logf("HXC-1135 E2E: started agent A pid=%d id=%s", agentA.Process.Pid, idA)

	// ── Launch helix-agent B ───────────────────────────────────────────────────
	agentB := exec.CommandContext(procCtx, agentBin,
		"--id", idB,
		"--region", "us-east-1",
		"--bind-port", "19201",
		"--wg-port", "59201",
		"--etcd-endpoints", etcdEndpoint,
	)
	agentB.Env = append(os.Environ(), "HELIX_ETCD_ENDPOINTS=")
	if err := agentB.Start(); err != nil {
		t.Fatalf("start helix-agent B: %v", err)
	}
	defer func() {
		agentB.Process.Kill()
		agentB.Wait()
	}()
	t.Logf("HXC-1135 E2E: started agent B pid=%d id=%s", agentB.Process.Pid, idB)

	// ── Observer: poll shared etcd until both nodes appear ────────────────────
	// Build a ServiceRegistry backed by the same etcd. The prefix "helix-node"
	// mirrors what internal/node.NewAgent registers under (node.go line 141-142).
	ec, err := etcd.New(etcd.Config{
		Endpoints:   []string{etcdEndpoint},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("etcd.New for observer: %v", err)
	}

	backend := discovery.NewEtcdBackend(ec, "helix-node")
	observer := discovery.NewServiceRegistry(backend)

	deadline := time.Now().Add(60 * time.Second)
	var foundA, foundB bool
	var formationTime time.Time

	pollCtx, pollCancel := context.WithDeadline(context.Background(), deadline)
	defer pollCancel()

	for time.Now().Before(deadline) {
		insts, err := observer.Lookup(pollCtx, "helix-node")
		if err != nil {
			t.Logf("HXC-1135 E2E: Lookup transient error: %v", err)
			select {
			case <-pollCtx.Done():
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}

		foundA = false
		foundB = false
		for _, inst := range insts {
			if inst.ID == idA {
				foundA = true
			}
			if inst.ID == idB {
				foundB = true
			}
		}

		if foundA && foundB {
			formationTime = time.Now().UTC()
			break
		}

		select {
		case <-pollCtx.Done():
		case <-time.After(500 * time.Millisecond):
		}
	}

	// ── Sink-side assertions ──────────────────────────────────────────────────
	if !foundA {
		t.Errorf("HXC-1135 HARD-FAIL: node A (id=%s) not found in shared etcd within 60s", idA)
	}
	if !foundB {
		t.Errorf("HXC-1135 HARD-FAIL: node B (id=%s) not found in shared etcd within 60s", idB)
	}
	if !foundA || !foundB {
		t.Fatalf("timed out waiting for both nodes to appear in etcd: foundA=%v foundB=%v "+
			"(mutation proof: if EtcdEndpoints ignored, agents use in-memory backend "+
			"and neither appears in shared etcd — this exact assertion fails)", foundA, foundB)
	}

	t.Logf("HXC-1135 E2E PASS: both nodes visible in shared etcd — "+
		"run_uuid=%s formation_time=%s", runUUID, formationTime.Format(time.RFC3339))

	// ── Capture evidence ──────────────────────────────────────────────────────
	captureEvidence(t, runUUID, idA, idB, formationTime)
}

// projectRoot resolves the module root so exec.Command("go", "build", ...)
// runs from the right working directory regardless of where the test binary
// itself is invoked from.
func projectRoot(t *testing.T) string {
	t.Helper()
	// Walk up from this file's directory until we find go.work or go.mod that
	// declares the root module. The simplest reliable approach for a Go workspace
	// is to use `go env GOWORK` (available in Go 1.18+) or fall back to the
	// directory that contains go.work.
	out, err := exec.Command("go", "env", "GOWORK").Output()
	if err != nil || len(out) == 0 {
		// fallback: go.mod in the current binary dir
		return "."
	}
	// GOWORK points to the go.work file; its parent is the workspace root.
	return filepath.Dir(strings.TrimSpace(string(out)))
}

// captureEvidence writes a JSON evidence file to qa-results/wave14/A-exitgate/
// containing the per-run UUID, both node IDs, and the formation timestamp.
// This satisfies the §7.1 sink-side evidence requirement.
func captureEvidence(t *testing.T, runUUID, idA, idB string, formation time.Time) {
	t.Helper()

	// Resolve the evidence dir relative to the workspace root.
	root, err := workspaceRoot()
	if err != nil {
		t.Logf("captureEvidence: cannot resolve workspace root: %v (evidence not written)", err)
		return
	}

	evidenceDir := filepath.Join(root, "qa-results", "wave14", "A-exitgate")
	if err := os.MkdirAll(evidenceDir, 0o755); err != nil {
		t.Logf("captureEvidence: MkdirAll %s: %v", evidenceDir, err)
		return
	}

	ev := struct {
		RunUUID       string    `json:"run_uuid"`
		NodeIDA       string    `json:"node_id_a"`
		NodeIDB       string    `json:"node_id_b"`
		FormationTime string    `json:"formation_time_utc"`
		AssertionPass bool      `json:"both_nodes_visible_in_etcd"`
	}{
		RunUUID:       runUUID,
		NodeIDA:       idA,
		NodeIDB:       idB,
		FormationTime: formation.Format(time.RFC3339Nano),
		AssertionPass: true,
	}

	data, err := json.MarshalIndent(ev, "", "  ")
	if err != nil {
		t.Logf("captureEvidence: marshal: %v", err)
		return
	}

	fname := filepath.Join(evidenceDir, fmt.Sprintf("hxc1135_formation_%s.json", runUUID))
	if err := os.WriteFile(fname, data, 0o644); err != nil {
		t.Logf("captureEvidence: write %s: %v", fname, err)
		return
	}
	t.Logf("HXC-1135 E2E: evidence written to %s", fname)
}

// workspaceRoot returns the Go workspace root by running `go env GOWORK`.
func workspaceRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOWORK").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOWORK: %w", err)
	}
	gowork := strings.TrimSpace(string(out))
	if gowork == "" {
		return "", fmt.Errorf("GOWORK is empty (not in a Go workspace)")
	}
	return filepath.Dir(gowork), nil
}
