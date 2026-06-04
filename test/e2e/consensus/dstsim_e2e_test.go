//go:build e2e

// Package consensus_e2e drives a REAL wired Helix binary (cmd/dst-sim) end-to-end
// as an end user would: it `go build`s the binary, runs it as a subprocess, and
// asserts the observable contract (stdout report + process exit code). This is a
// genuine end-to-end exercise of the deterministic-simulation consensus gate
// (HXC-915) — not an in-process function call.
//
// WHY dst-sim is the e2e target for HXC-937: of the packages in this task's scope
// (pkg/federation, pkg/multiraft, pkg/marketplace, internal/federation), NONE is
// yet wired into a runnable cmd/* binary — they are libraries consumed only by
// tests today. Rather than fake an end-user surface for them (which would be a
// CLAUDE-1 PASS-bluff), this e2e drives the CLOSEST wired binary that exercises
// the SAME problem domain: cmd/dst-sim runs a replicated linearizable register
// under adversarial scheduling (dropped/reordered/delayed messages, disk faults)
// and emits a deterministic PASS/FAIL — exactly the consensus-safety guarantee
// the multiraft chaos suite asserts in-process. See the package doc and the
// report concern: when pkg/multiraft or pkg/marketplace gains a real cmd/* entry
// point, a dedicated e2e for that binary should be added here in the same form.
//
// Run with: go test -tags e2e ./test/e2e/consensus/...
package consensus_e2e

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRoot walks up from this test file to the module root (the dir holding go.mod).
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve caller path")
	}
	// thisFile = <root>/test/e2e/consensus/dstsim_e2e_test.go -> up 3 dirs.
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
}

// buildDSTSim builds the cmd/dst-sim binary into a temp dir and returns its path.
// Building (not `go run`) proves the binary genuinely compiles and links as a
// shippable artifact, then we execute that artifact directly.
func buildDSTSim(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	bin := filepath.Join(t.TempDir(), "dst-sim")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/dst-sim")
	cmd.Dir = root
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build ./cmd/dst-sim failed: %v\n%s", err, out.String())
	}
	return bin
}

// TestDSTSimBinaryGatePasses runs the built dst-sim binary over a small seed
// range and asserts the END-USER-OBSERVABLE contract: exit code 0 AND a stdout
// line reporting that every seed was linearizable. This proves the wired gate
// binary actually runs the consensus simulation to a PASS, not merely that the
// package compiles.
func TestDSTSimBinaryGatePasses(t *testing.T) {
	bin := buildDSTSim(t)

	cmd := exec.Command(bin, "-seeds", "25", "-steps", "800")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	out := stdout.String()
	if err != nil {
		t.Fatalf("dst-sim exited non-zero on a passing seed range: %v\nstdout:\n%s\nstderr:\n%s",
			err, out, stderr.String())
	}
	// SINK-SIDE: observable stdout proves the simulation ran and every seed was
	// checked for linearizability (not a silent no-op exit).
	if !strings.Contains(out, "running 25 seeded simulations") {
		t.Fatalf("dst-sim did not report running the simulations; stdout:\n%s", out)
	}
	if !strings.Contains(out, "all 25 seeds linearizable") {
		t.Fatalf("dst-sim did not report the all-pass linearizable result; stdout:\n%s", out)
	}
}

// TestDSTSimBinaryDeterministic proves the e2e contract that makes the gate
// trustworthy: running the SAME seed range twice produces BYTE-IDENTICAL output
// and the same (zero) exit code. A non-deterministic gate could hide a real
// violation behind a lucky run (CLAUDE-1). This asserts determinism end-to-end
// through the actual binary.
func TestDSTSimBinaryDeterministic(t *testing.T) {
	bin := buildDSTSim(t)

	run := func() (string, bool) {
		cmd := exec.Command(bin, "-start", "100", "-seeds", "15", "-steps", "600")
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		ok := cmd.Run() == nil
		return stdout.String(), ok
	}

	out1, ok1 := run()
	out2, ok2 := run()

	if !ok1 || !ok2 {
		t.Fatalf("dst-sim should exit zero on both runs of a passing range (ok1=%v ok2=%v)\nout1:\n%s", ok1, ok2, out1)
	}
	if out1 != out2 {
		t.Fatalf("dst-sim is NOT deterministic across runs of the same seed range:\n--- run1 ---\n%s\n--- run2 ---\n%s", out1, out2)
	}
	if !strings.Contains(out1, "linearizable") {
		t.Fatalf("dst-sim output missing linearizability report:\n%s", out1)
	}
}
