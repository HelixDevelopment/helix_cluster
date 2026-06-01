//go:build integration

package serde

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestIntegration_CapnpRegenerateAndRoundTrip does two things:
//  1. Regenerates node.capnp via the real 'capnp compile' toolchain into a
//     temp dir and asserts the output matches the committed node.capnp.go —
//     proving the schema + toolchain are real and in sync.
//  2. Embeds a per-run UUID in a Node.id, round-trips it through the generated
//     code, and asserts the exact UUID survives (sink-side assertion).
//
// Hard-FAIL on mismatch; t.Skip only when 'capnp' is genuinely absent.
func TestIntegration_CapnpRegenerateAndRoundTrip(t *testing.T) {
	// ── 0. Check toolchain availability ──────────────────────────────────────
	capnpBin, err := exec.LookPath("capnp")
	if err != nil {
		t.Skip("capnp binary not on PATH — skipping integration test")
	}
	capnpcGo := filepath.Join(os.Getenv("HOME"), "go", "bin", "capnpc-go")
	if _, err := os.Stat(capnpcGo); err != nil {
		t.Skipf("capnpc-go not found at %s — skipping integration test", capnpcGo)
	}

	// ── 1. Locate the committed schema and generated file ────────────────────
	// Resolve path relative to this test file's package directory.
	// os.Getwd() returns the package dir when running 'go test'.
	pkgDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	schemaPath := filepath.Join(pkgDir, "node.capnp")
	committedGenPath := filepath.Join(pkgDir, "node.capnp.go")

	committedGen, err := os.ReadFile(committedGenPath)
	if err != nil {
		t.Fatalf("read committed generated file: %v", err)
	}

	// ── 2. Regenerate into a temp dir ────────────────────────────────────────
	tmpDir, err := os.MkdirTemp("", "helix-serde-capnp-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// The capnp compiler needs the go.capnp std annotation reachable via -I.
	stdDir := filepath.Join(os.Getenv("HOME"),
		"go", "pkg", "mod",
		"capnproto.org", "go", "capnp", "v3@v3.1.0-alpha.2", "std")

	outputFlag := fmt.Sprintf("%s:%s", capnpcGo, tmpDir)
	cmd := exec.Command(capnpBin,
		"compile",
		"-I", stdDir,
		"-o", outputFlag,
		schemaPath,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("capnp compile failed: %v\nstderr: %s", err, stderr.String())
	}

	// ── 3. Read freshly-generated file ───────────────────────────────────────
	freshGen, err := os.ReadFile(filepath.Join(tmpDir, "node.capnp.go"))
	if err != nil {
		t.Fatalf("read freshly-generated file: %v", err)
	}

	// ── 4. Assert generated file matches committed file ───────────────────────
	if !bytes.Equal(committedGen, freshGen) {
		// Show a diff hint (first differing line).
		committedLines := strings.Split(string(committedGen), "\n")
		freshLines := strings.Split(string(freshGen), "\n")
		for i := 0; i < len(committedLines) && i < len(freshLines); i++ {
			if committedLines[i] != freshLines[i] {
				t.Errorf("generated file mismatch at line %d:\n  committed: %q\n  fresh:     %q",
					i+1, committedLines[i], freshLines[i])
				break
			}
		}
		if len(committedLines) != len(freshLines) {
			t.Errorf("generated file line count mismatch: committed=%d fresh=%d",
				len(committedLines), len(freshLines))
		}
		t.Fatalf("committed node.capnp.go is out of sync with node.capnp — regenerate and commit")
	}

	// ── 5. Per-run UUID round-trip ────────────────────────────────────────────
	// Build a UUID from timestamp + pid to guarantee uniqueness across runs
	// (no external UUID dep required; the point is a unique per-run string).
	perRunUUID := fmt.Sprintf("integ-%d-%d-%s",
		time.Now().UnixNano(), os.Getpid(),
		"b3d4e5f6a7c8d9e0") // embed schema file ID for traceability

	n := NodeRecord{
		ID:       perRunUUID,
		Hostname: "integration-host.helix.internal",
		Cores:    48,
		MemBytes: 274877906944, // 256 GiB
		Labels:   []string{"env=integration", "wave=11", "stream=C-capnp"},
	}

	data, err := MarshalNode(n)
	if err != nil {
		t.Fatalf("MarshalNode: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("MarshalNode returned empty bytes")
	}

	view, err := DeserializeNode(data)
	if err != nil {
		t.Fatalf("DeserializeNode: %v", err)
	}

	// Assert exact per-run UUID survived the round-trip (sink-side).
	gotID, err := view.Id()
	if err != nil {
		t.Fatalf("view.Id(): %v", err)
	}
	if gotID != perRunUUID {
		t.Errorf("per-run UUID mismatch:\n  want: %q\n  got:  %q", perRunUUID, gotID)
	}

	// Assert all other fields.
	gotHost, err := view.Hostname()
	if err != nil {
		t.Fatalf("view.Hostname(): %v", err)
	}
	if gotHost != n.Hostname {
		t.Errorf("hostname mismatch: want %q, got %q", n.Hostname, gotHost)
	}
	if view.Cores() != n.Cores {
		t.Errorf("cores mismatch: want %d, got %d", n.Cores, view.Cores())
	}
	if view.MemBytes() != n.MemBytes {
		t.Errorf("memBytes mismatch: want %d, got %d", n.MemBytes, view.MemBytes())
	}

	lbls, err := view.Labels()
	if err != nil {
		t.Fatalf("view.Labels(): %v", err)
	}
	if lbls.Len() != len(n.Labels) {
		t.Fatalf("labels len mismatch: want %d, got %d", len(n.Labels), lbls.Len())
	}
	for i, want := range n.Labels {
		got, err := lbls.At(i)
		if err != nil {
			t.Fatalf("labels.At(%d): %v", i, err)
		}
		if got != want {
			t.Errorf("label[%d] mismatch: want %q, got %q", i, want, got)
		}
	}

	t.Logf("Integration round-trip OK: per-run UUID=%s, bytes=%d", perRunUUID, len(data))
}
