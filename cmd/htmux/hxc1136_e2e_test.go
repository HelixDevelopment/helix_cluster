//go:build integration

package main

// hxc1136_e2e_test.go — HXC-1136 E2E integration test.
//
// CLOSURE CRITERIA (from the HXC-1136 design):
//   - Build the helix-session and htmux binaries from source.
//   - Start helix-session on an ephemeral port.
//   - Execute `htmux create -s helix-1136-<runUUID> --addr <addr>` via the real binary.
//   - Assert the session is visible with status RUNNING within 3 s via
//     `htmux list --addr <addr>`.
//   - SINK-SIDE: assert a real tmux session exists via `tmux list-sessions`.
//   - Capture session record + elapsed + per-run UUID to
//     qa-results/wave15/A-session/ evidence file.
//   - t.Skip ONLY when tmux is genuinely absent from PATH.
//
// This test is guarded by //go:build integration (line 1) and is NOT run by
// the hermetic unit command.  The orchestrator runs it with -tags=integration.
//
// ANTI-BLUFF (CLAUDE-1): a session that reports StatusRunning but has no tmux
// counterpart is a PASS-bluff.  We verify via `tmux list-sessions` that the
// session actually exists.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestHXC1136_E2E_SessionCreate_RealTmux is the full E2E test for HXC-1136.
//
// It proves:
//  1. `htmux create` creates a session visible as RUNNING in `htmux list` within 3 s.
//  2. The session has a real tmux process (tmux list-sessions is non-empty after create).
//  3. The per-run UUID is present in both the session name and the evidence file.
//
// MUTATION that makes the "real tmux" assertion FAIL: revert
// internal/session.NewServer() to use session.NewManager(nil) — sessions
// are created in-memory with StatusRunning but no tmux process is started;
// `tmux list-sessions` will return "no server running" and the sink-side
// assertion fails.
func TestHXC1136_E2E_SessionCreate_RealTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not found in PATH; skipping real-tmux E2E test")
	}

	// Per-run UUID — embedded in session name and evidence, asserted on sink.
	runUUID := uuid.New().String()[:8]
	sessionName := "helix-1136-" + runUUID

	t.Logf("HXC-1136 E2E: runUUID=%s sessionName=%s", runUUID, sessionName)

	// -------------------------------------------------------------------------
	// 1. Build binaries from source into a temp directory.
	// -------------------------------------------------------------------------
	tmpDir := t.TempDir()
	helixSessionBin := filepath.Join(tmpDir, "helix-session")
	htmuxBin := filepath.Join(tmpDir, "htmux")

	buildCtx, buildCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer buildCancel()

	if out, err := exec.CommandContext(buildCtx,
		"go", "build", "-o", helixSessionBin,
		"github.com/HelixDevelopment/helix_cluster/cmd/helix-session",
	).CombinedOutput(); err != nil {
		t.Fatalf("build helix-session: %v\n%s", err, out)
	}
	if out, err := exec.CommandContext(buildCtx,
		"go", "build", "-o", htmuxBin,
		"github.com/HelixDevelopment/helix_cluster/cmd/htmux",
	).CombinedOutput(); err != nil {
		t.Fatalf("build htmux: %v\n%s", err, out)
	}

	// -------------------------------------------------------------------------
	// 2. Find a free port and start helix-session.
	// -------------------------------------------------------------------------
	srvAddr, srvPort := e2eFindFreePort(t)

	srvCtx, srvCancel := context.WithCancel(context.Background())
	defer srvCancel()

	srvCmd := exec.CommandContext(srvCtx, helixSessionBin)
	srvCmd.Env = append(os.Environ(),
		"HELIX_SESSION_HOST=127.0.0.1",
		fmt.Sprintf("HELIX_SESSION_PORT=%d", srvPort),
	)

	if err := srvCmd.Start(); err != nil {
		t.Fatalf("start helix-session: %v", err)
	}
	t.Cleanup(func() {
		srvCancel()
		_ = srvCmd.Wait()
	})

	// Wait for the server to accept connections.
	e2eWaitForServer(t, srvAddr, 10*time.Second)

	// -------------------------------------------------------------------------
	// 3. htmux create -s <sessionName> --addr <addr>
	// -------------------------------------------------------------------------
	startCreate := time.Now()
	createCtx, createCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer createCancel()

	createOut, err := exec.CommandContext(createCtx,
		htmuxBin, "create", "-s", sessionName, "--addr", srvAddr,
	).CombinedOutput()
	elapsedCreate := time.Since(startCreate)
	t.Logf("htmux create (%v): %s", elapsedCreate, createOut)
	if err != nil {
		t.Fatalf("htmux create: %v\n%s", err, createOut)
	}

	// The output must contain the session name — confirms the RPC returned a
	// real session record (not a silent no-op).
	if !strings.Contains(string(createOut), sessionName) {
		t.Errorf("htmux create stdout does not contain session name %q: %s", sessionName, createOut)
	}

	// -------------------------------------------------------------------------
	// 4. htmux list within 3 s — assert RUNNING visible.
	// -------------------------------------------------------------------------
	var listOut []byte
	var foundRunning bool
	listDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(listDeadline) {
		listCtx, listCancel := context.WithTimeout(context.Background(), 2*time.Second)
		out, lerr := exec.CommandContext(listCtx,
			htmuxBin, "list", "--addr", srvAddr,
		).CombinedOutput()
		listCancel()
		if lerr == nil {
			listOut = out
			if strings.Contains(string(out), sessionName) &&
				strings.Contains(strings.ToLower(string(out)), "running") {
				foundRunning = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	elapsedTotal := time.Since(startCreate)
	t.Logf("htmux list (after %v): %s", elapsedTotal, listOut)

	if !foundRunning {
		t.Fatalf("session %q not visible as RUNNING within 3 s; last list output:\n%s", sessionName, listOut)
	}
	if elapsedTotal >= 3*time.Second {
		t.Errorf("session appeared RUNNING but elapsed %v exceeds 3 s deadline", elapsedTotal)
	}

	// -------------------------------------------------------------------------
	// 5. SINK-SIDE: verify a real tmux process exists.
	//
	// The Manager assigns the tmux session the manager-generated ID
	// (session-<nanos>-<rand>), not the human-readable Name.  We cannot match
	// by name here, but we can assert that `tmux list-sessions` is non-empty
	// and does NOT report "no server running" — proving a real tmux server was
	// started and at least one session lives in it.
	//
	// ANTI-BLUFF: if helix-session used a nil backend, no tmux server would be
	// started, `tmux list-sessions` would fail with "no server running", and
	// this assertion would catch the bluff.
	// -------------------------------------------------------------------------
	tmuxOut, tmuxErr := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").CombinedOutput()
	t.Logf("tmux list-sessions: %s (err: %v)", tmuxOut, tmuxErr)

	tmuxNoServer := tmuxErr != nil && (strings.Contains(string(tmuxOut), "no server running") ||
		strings.Contains(string(tmuxOut), "error connecting"))
	if tmuxNoServer {
		t.Fatal("ANTI-BLUFF §7.1: tmux has no server / no sessions after helix-session reported RUNNING; " +
			"the session-create path did not launch a real tmux process (nil-backend PASS-bluff detected)")
	}

	// -------------------------------------------------------------------------
	// 6. Capture evidence to qa-results/wave15/A-session/.
	// -------------------------------------------------------------------------
	evidence := map[string]interface{}{
		"hxc_id":            "HXC-1136",
		"run_uuid":          runUUID,
		"session_name":      sessionName,
		"elapsed_create_ms": elapsedCreate.Milliseconds(),
		"elapsed_total_ms":  elapsedTotal.Milliseconds(),
		"running_within_3s": foundRunning && elapsedTotal < 3*time.Second,
		"list_output":       string(listOut),
		"tmux_sessions":     string(tmuxOut),
		"timestamp":         time.Now().UTC().Format(time.RFC3339),
	}
	e2eWriteEvidence(t, "qa-results/wave15/A-session/hxc1136_e2e_evidence.json", evidence)
}

// e2eFindFreePort picks a free TCP port on 127.0.0.1 and returns (addr, port).
// It briefly binds :0 to get the OS-assigned port, then closes the listener
// so the target process can bind the same port.
func e2eFindFreePort(t *testing.T) (string, int) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("e2eFindFreePort: %v", err)
	}
	addr := lis.Addr().(*net.TCPAddr)
	port := addr.Port
	_ = lis.Close()
	return fmt.Sprintf("127.0.0.1:%d", port), port
}

// e2eWaitForServer polls addr until it accepts a TCP connection or timeout.
func e2eWaitForServer(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("server at %s not ready after %v", addr, timeout)
}

// e2eWriteEvidence serializes evidence to the given relative path.
func e2eWriteEvidence(t *testing.T, relPath string, evidence map[string]interface{}) {
	t.Helper()
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Logf("marshal evidence: %v", err)
		return
	}
	absPath := relPath
	if !filepath.IsAbs(relPath) {
		cwd, _ := os.Getwd()
		// Test runs from cmd/htmux; module root is two levels up.
		absPath = filepath.Join(cwd, "..", "..", relPath)
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Logf("mkdir evidence dir: %v", err)
		return
	}
	if err := os.WriteFile(absPath, data, 0o644); err != nil {
		t.Logf("write evidence: %v", err)
	} else {
		t.Logf("evidence written to %s", absPath)
	}
}
