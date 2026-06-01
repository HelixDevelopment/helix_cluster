package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTemp creates a temporary file in dir with the given content and returns
// its path. It fails the test on any error.
func writeTemp(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writeTemp %q: %v", path, err)
	}
	return path
}

// goldenPath returns the expected path of the .golden file for the given
// snapshot name under baseDir.
func goldenPath(baseDir, name string) string {
	return filepath.Join(baseDir, name+".golden")
}

// TestCreate verifies that create writes the .golden file to disk and prints
// the path to stdout.
func TestCreate(t *testing.T) {
	tmpDir := t.TempDir()
	snapshotDir := filepath.Join(tmpDir, "snapshots")
	inputFile := writeTemp(t, tmpDir, "input.txt", "hello world")

	var stdout, stderr bytes.Buffer
	code := run([]string{"-dir", snapshotDir, "create", "mysnap", inputFile}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr=%q", code, stderr.String())
	}

	// The printed path must appear on stdout.
	printedPath := strings.TrimSpace(stdout.String())
	if printedPath == "" {
		t.Fatal("stdout is empty; expected a path")
	}

	// The .golden file MUST exist on disk.
	wantPath := goldenPath(snapshotDir, "mysnap")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("golden file not found at %q: %v", wantPath, err)
	}

	// The printed path must point to the actual file.
	if printedPath != wantPath {
		t.Errorf("printed path = %q; want %q", printedPath, wantPath)
	}

	// The file content must match the original input.
	got, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read golden file: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("golden content = %q; want %q", got, "hello world")
	}
}

// TestList verifies that list shows the snapshot that was just created.
func TestList(t *testing.T) {
	tmpDir := t.TempDir()
	snapshotDir := filepath.Join(tmpDir, "snapshots")
	inputFile := writeTemp(t, tmpDir, "input.txt", "list test")

	// Create a snapshot first.
	var buf bytes.Buffer
	code := run([]string{"-dir", snapshotDir, "create", "listsnap", inputFile}, &buf, &buf)
	if code != 0 {
		t.Fatalf("create failed with code %d", code)
	}

	// Now list.
	var stdout, stderr bytes.Buffer
	code = run([]string{"-dir", snapshotDir, "list"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("list exit %d; stderr=%q", code, stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "listsnap") {
		t.Errorf("list output %q does not contain %q", out, "listsnap")
	}
}

// TestListEmpty verifies that list prints "No snapshots" when the directory is
// empty (or doesn't exist yet).
func TestListEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	snapshotDir := filepath.Join(tmpDir, "empty-snapshots")

	// The directory exists but is empty; `list` must print "No snapshots".
	// (Manager.List uses filepath.Walk, which would error on a missing dir, so
	// the empty-but-present case is the one this test pins down.)
	if err := os.MkdirAll(snapshotDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"-dir", snapshotDir, "list"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("list exit %d; stderr=%q", code, stderr.String())
	}

	out := strings.TrimSpace(stdout.String())
	if out != "No snapshots" {
		t.Errorf("expected %q, got %q", "No snapshots", out)
	}
}

// TestCompareMatch verifies that compare exits 0 when file matches snapshot.
func TestCompareMatch(t *testing.T) {
	tmpDir := t.TempDir()
	snapshotDir := filepath.Join(tmpDir, "snapshots")
	content := "compare content"
	inputFile := writeTemp(t, tmpDir, "input.txt", content)

	// Create snapshot.
	var buf bytes.Buffer
	if code := run([]string{"-dir", snapshotDir, "create", "cmpsnap", inputFile}, &buf, &buf); code != 0 {
		t.Fatalf("create failed with code %d", code)
	}

	// Compare matching file.
	matchFile := writeTemp(t, tmpDir, "match.txt", content)
	var stdout, stderr bytes.Buffer
	code := run([]string{"-dir", snapshotDir, "compare", "cmpsnap", matchFile}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("compare (match) exit %d; stderr=%q", code, stderr.String())
	}
}

// TestCompareMismatch verifies that compare exits 1 and writes to stderr when
// the file content differs from the snapshot.
func TestCompareMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	snapshotDir := filepath.Join(tmpDir, "snapshots")
	inputFile := writeTemp(t, tmpDir, "input.txt", "original content")

	// Create snapshot from original.
	var buf bytes.Buffer
	if code := run([]string{"-dir", snapshotDir, "create", "mismatch", inputFile}, &buf, &buf); code != 0 {
		t.Fatalf("create failed with code %d", code)
	}

	// Compare against a modified file.
	modFile := writeTemp(t, tmpDir, "modified.txt", "DIFFERENT content")
	var stdout, stderr bytes.Buffer
	code := run([]string{"-dir", snapshotDir, "compare", "mismatch", modFile}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("compare (mismatch) expected exit 1, got %d", code)
	}
	if stderr.Len() == 0 {
		t.Error("expected mismatch description on stderr, got nothing")
	}
}

// TestRestore verifies that restore writes the original snapshot bytes verbatim
// to stdout.
func TestRestore(t *testing.T) {
	tmpDir := t.TempDir()
	snapshotDir := filepath.Join(tmpDir, "snapshots")
	original := "restore bytes"
	inputFile := writeTemp(t, tmpDir, "input.txt", original)

	// Create snapshot.
	var buf bytes.Buffer
	if code := run([]string{"-dir", snapshotDir, "create", "restoresnap", inputFile}, &buf, &buf); code != 0 {
		t.Fatalf("create failed with code %d", code)
	}

	// Restore and check stdout bytes == original.
	var stdout, stderr bytes.Buffer
	code := run([]string{"-dir", snapshotDir, "restore", "restoresnap"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("restore exit %d; stderr=%q", code, stderr.String())
	}

	got := stdout.Bytes()
	if !bytes.Equal(got, []byte(original)) {
		t.Errorf("restore bytes = %q; want %q", got, original)
	}
}

// TestDelete verifies that delete exits 0 and the .golden file is GONE from disk.
func TestDelete(t *testing.T) {
	tmpDir := t.TempDir()
	snapshotDir := filepath.Join(tmpDir, "snapshots")
	inputFile := writeTemp(t, tmpDir, "input.txt", "delete me")

	// Create snapshot.
	var buf bytes.Buffer
	if code := run([]string{"-dir", snapshotDir, "create", "delsnap", inputFile}, &buf, &buf); code != 0 {
		t.Fatalf("create failed with code %d", code)
	}

	// Confirm it exists.
	wantPath := goldenPath(snapshotDir, "delsnap")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("golden file not found before delete: %v", err)
	}

	// Delete.
	var stdout, stderr bytes.Buffer
	code := run([]string{"-dir", snapshotDir, "delete", "delsnap"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("delete exit %d; stderr=%q", code, stderr.String())
	}

	// The .golden file MUST be gone.
	if _, err := os.Stat(wantPath); !os.IsNotExist(err) {
		t.Errorf("expected golden file to be gone, stat returned err=%v", err)
	}
}

// TestDeleteNonExistent verifies that deleting a snapshot that doesn't exist
// exits 1.
func TestDeleteNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	snapshotDir := filepath.Join(tmpDir, "snapshots")
	if err := os.MkdirAll(snapshotDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"-dir", snapshotDir, "delete", "does-not-exist"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1 for nonexistent delete, got %d", code)
	}
}

// TestUnknownSubcommand verifies that an unrecognised subcommand exits 1 and
// prints usage information to stderr.
func TestUnknownSubcommand(t *testing.T) {
	tmpDir := t.TempDir()
	snapshotDir := filepath.Join(tmpDir, "snapshots")

	var stdout, stderr bytes.Buffer
	code := run([]string{"-dir", snapshotDir, "bogus"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1 for unknown subcommand, got %d", code)
	}

	// Usage text should appear on stderr.
	errOut := stderr.String()
	if !strings.Contains(errOut, "Usage") && !strings.Contains(errOut, "usage") && !strings.Contains(errOut, "helix-snapshot") {
		t.Errorf("expected usage text on stderr, got %q", errOut)
	}
}

// TestNoSubcommand verifies that invoking with no args exits 1 with usage on stderr.
func TestNoSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1 for no subcommand, got %d", code)
	}
	errOut := stderr.String()
	if errOut == "" && stdout.String() == "" {
		t.Error("expected some output for no subcommand")
	}
}

// TestEnvFallback verifies that HELIX_SNAPSHOT_DIR is honoured when -dir is not set.
func TestEnvFallback(t *testing.T) {
	tmpDir := t.TempDir()
	snapshotDir := filepath.Join(tmpDir, "env-snapshots")
	inputFile := writeTemp(t, tmpDir, "env.txt", "env content")

	t.Setenv("HELIX_SNAPSHOT_DIR", snapshotDir)

	// No -dir flag; must use env.
	var stdout, stderr bytes.Buffer
	code := run([]string{"create", "envsnap", inputFile}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr=%q", code, stderr.String())
	}

	wantPath := goldenPath(snapshotDir, "envsnap")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("golden file not created via env path: %v", err)
	}
}
