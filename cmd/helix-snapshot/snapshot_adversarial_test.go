package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withTimeout fails the test if fn does not complete promptly, guarding against
// any accidental hang in the filesystem-backed CLI operations.
func withTimeout(t *testing.T, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("CLI operation did not complete within timeout")
	}
}

// runCLI is a small helper that invokes run() with captured stdout/stderr.
func runCLI(args ...string) (code int, stdout, stderr []byte) {
	var out, errb bytes.Buffer
	code = run(args, &out, &errb)
	return code, out.Bytes(), errb.Bytes()
}

// TestAdv_CLIRoundTripExact proves the create->restore CLI path is byte-lossless
// for hostile payloads. cmdCreate reads a file then stores it; cmdRestore writes
// the stored bytes to stdout. The bytes printed by restore MUST equal the original
// file content exactly — including empty files, NUL bytes, CR/LF, and large binary.
func TestAdv_CLIRoundTripExact(t *testing.T) {
	cases := map[string][]byte{
		"empty":      {},
		"nul_bytes":  {0x00, 0x01, 0x00, 0xFF, 0x00},
		"crlf":       []byte("line1\r\nline2\nline3\r"),
		"no_newline": []byte("no trailing newline"),
		"trailing_n": []byte("ends with newline\n"),
		"utf8":       []byte("ünïçödé — 日本語 — 😀"),
		"large_bin":  bytes.Repeat([]byte{0xDE, 0xAD, 0xBE, 0xEF}, 8192),
	}
	for tag, data := range cases {
		tag, data := tag, data
		t.Run(tag, func(t *testing.T) {
			withTimeout(t, func() {
				root := t.TempDir()
				snapDir := filepath.Join(root, "snaps")
				inFile := filepath.Join(root, "in.bin")
				if err := os.WriteFile(inFile, data, 0o644); err != nil {
					t.Fatalf("write input: %v", err)
				}

				if code, _, errb := runCLI("-dir", snapDir, "create", tag, inFile); code != 0 {
					t.Fatalf("create exit %d; stderr=%q", code, errb)
				}

				code, out, errb := runCLI("-dir", snapDir, "restore", tag)
				if code != 0 {
					t.Fatalf("restore exit %d; stderr=%q", code, errb)
				}
				if !bytes.Equal(out, data) {
					t.Fatalf("round-trip mismatch for %s: in=%v out=%v", tag, data, out)
				}

				// compare of the identical file must succeed (exit 0).
				if code, _, errb := runCLI("-dir", snapDir, "compare", tag, inFile); code != 0 {
					t.Fatalf("compare (identical) exit %d; stderr=%q", code, errb)
				}
			})
		})
	}
}

// TestAdv_CLICompareDetectsTamper proves cmdCompare detects every flavor of
// tampering at the sink (exit 1 + stderr): a single-byte flip of equal length,
// truncation, and an appended byte. A compare that returns 0 on a tampered file
// is a checksum-bluff (a "PASS on broken").
func TestAdv_CLICompareDetectsTamper(t *testing.T) {
	root := t.TempDir()
	snapDir := filepath.Join(root, "snaps")
	original := []byte("the quick brown fox jumps over the lazy dog")
	inFile := filepath.Join(root, "orig.bin")
	if err := os.WriteFile(inFile, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errb := runCLI("-dir", snapDir, "create", "tamper", inFile); code != 0 {
		t.Fatalf("create exit %d; stderr=%q", code, errb)
	}

	mutate := func(name string, b []byte) {
		t.Helper()
		f := filepath.Join(root, name)
		if err := os.WriteFile(f, b, 0o644); err != nil {
			t.Fatal(err)
		}
		code, _, errb := runCLI("-dir", snapDir, "compare", "tamper", f)
		if code != 1 {
			t.Fatalf("compare(%s) expected exit 1, got %d", name, code)
		}
		if len(errb) == 0 {
			t.Fatalf("compare(%s) expected mismatch text on stderr, got none", name)
		}
	}

	// Equal-length single-byte flip (the hardest case: same byte count).
	flip := append([]byte(nil), original...)
	flip[10] ^= 0xFF
	mutate("flip.bin", flip)

	// Truncated by one byte.
	mutate("trunc.bin", original[:len(original)-1])

	// One byte appended.
	mutate("extra.bin", append(append([]byte(nil), original...), 'X'))

	// Empty file vs non-empty snapshot.
	mutate("empty.bin", []byte{})
}

// TestAdv_CLINameEscapeRejected proves a snapshot name that resolves OUTSIDE the
// snapshot dir is rejected by the CLI (nonzero exit) and never writes, reads, or
// deletes an unrelated file. A path-escape here would let `create`/`delete` clobber
// arbitrary files on the host.
func TestAdv_CLINameEscapeRejected(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	// Plant a victim as a sibling of base, named so an escaping snapshot name
	// "../victim" would resolve onto it.
	victim := filepath.Join(root, "victim.golden")
	if err := os.WriteFile(victim, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(root, "payload.txt")
	if err := os.WriteFile(payload, []byte("OVERWRITTEN"), 0o644); err != nil {
		t.Fatal(err)
	}

	// create with escaping name must NOT exit 0 and must NOT clobber the victim.
	if code, _, _ := runCLI("-dir", base, "create", "../victim", payload); code == 0 {
		t.Fatal("create with escaping name unexpectedly succeeded (exit 0)")
	}
	if got, _ := os.ReadFile(victim); !bytes.Equal(got, []byte("ORIGINAL")) {
		t.Fatalf("escaping create clobbered an external file: %q", got)
	}

	// restore/compare with escaping name must not read the external file.
	if code, out, _ := runCLI("-dir", base, "restore", "../victim"); code == 0 {
		t.Fatalf("restore with escaping name unexpectedly succeeded; leaked=%q", out)
	}
	if code, _, _ := runCLI("-dir", base, "compare", "../victim", victim); code == 0 {
		t.Fatal("compare with escaping name unexpectedly succeeded (exit 0)")
	}

	// delete with escaping name must not remove the external file.
	if code, _, _ := runCLI("-dir", base, "delete", "../victim"); code == 0 {
		t.Fatal("delete with escaping name unexpectedly succeeded (exit 0)")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("escaping delete removed an external file: %v", err)
	}
}

// TestAdv_CLIListThenDeleteRemovesOnlyTarget proves that delete removes EXACTLY
// the named snapshot and leaves siblings intact (a retention/prune bug would
// delete the wrong file). It then proves list reflects the change.
func TestAdv_CLIListThenDeleteRemovesOnlyTarget(t *testing.T) {
	root := t.TempDir()
	snapDir := filepath.Join(root, "snaps")
	inFile := filepath.Join(root, "in.txt")
	if err := os.WriteFile(inFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	names := []string{"alpha", "beta", "nested/gamma", "alpha-2"}
	for _, n := range names {
		if code, _, errb := runCLI("-dir", snapDir, "create", n, inFile); code != 0 {
			t.Fatalf("create(%q) exit %d; stderr=%q", n, code, errb)
		}
	}

	// list must contain every created name.
	code, out, errb := runCLI("-dir", snapDir, "list")
	if code != 0 {
		t.Fatalf("list exit %d; stderr=%q", code, errb)
	}
	listed := map[string]bool{}
	for _, line := range bytes.Split(bytes.TrimSpace(out), []byte("\n")) {
		listed[string(line)] = true
	}
	for _, n := range names {
		if !listed[n] {
			t.Fatalf("created snapshot %q missing from list output %q", n, out)
		}
	}

	// Delete "alpha" only. "alpha-2" (a prefix-sharing sibling) must survive.
	if code, _, errb := runCLI("-dir", snapDir, "delete", "alpha"); code != 0 {
		t.Fatalf("delete(alpha) exit %d; stderr=%q", code, errb)
	}

	code, out, errb = runCLI("-dir", snapDir, "list")
	if code != 0 {
		t.Fatalf("list exit %d; stderr=%q", code, errb)
	}
	after := map[string]bool{}
	for _, line := range bytes.Split(bytes.TrimSpace(out), []byte("\n")) {
		after[string(line)] = true
	}
	if after["alpha"] {
		t.Fatalf("delete(alpha) did not remove it; list=%q", out)
	}
	for _, n := range []string{"beta", "nested/gamma", "alpha-2"} {
		if !after[n] {
			t.Fatalf("delete(alpha) wrongly removed sibling %q; list=%q", n, out)
		}
	}

	// The deleted snapshot must no longer be restorable.
	if code, _, _ := runCLI("-dir", snapDir, "restore", "alpha"); code == 0 {
		t.Fatal("restore of deleted snapshot unexpectedly succeeded")
	}
}

// TestAdv_CLIRestoreMissingNoPanicNonzero proves restore/compare/delete of a
// nonexistent snapshot return a nonzero exit and never panic (DoS safety).
func TestAdv_CLIRestoreMissingNoPanicNonzero(t *testing.T) {
	root := t.TempDir()
	snapDir := filepath.Join(root, "snaps")
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		t.Fatal(err)
	}
	missingCmp := filepath.Join(root, "cmp.txt")
	if err := os.WriteFile(missingCmp, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	withTimeout(t, func() {
		if code, _, _ := runCLI("-dir", snapDir, "restore", "ghost"); code == 0 {
			t.Error("restore of missing snapshot should be nonzero")
		}
		if code, _, _ := runCLI("-dir", snapDir, "compare", "ghost", missingCmp); code == 0 {
			t.Error("compare of missing snapshot should be nonzero")
		}
		if code, _, _ := runCLI("-dir", snapDir, "delete", "ghost"); code == 0 {
			t.Error("delete of missing snapshot should be nonzero")
		}
	})
}

// TestAdv_CLICreateUpdatesOverwrite proves cmdCreate uses update=true semantics:
// re-creating an existing snapshot with new content overwrites it (does not error),
// and a subsequent restore returns the NEW bytes, not stale ones.
func TestAdv_CLICreateUpdatesOverwrite(t *testing.T) {
	root := t.TempDir()
	snapDir := filepath.Join(root, "snaps")

	v1 := filepath.Join(root, "v1.txt")
	v2 := filepath.Join(root, "v2.txt")
	if err := os.WriteFile(v1, []byte("VERSION-1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(v2, []byte("VERSION-2-longer"), 0o644); err != nil {
		t.Fatal(err)
	}

	if code, _, errb := runCLI("-dir", snapDir, "create", "ver", v1); code != 0 {
		t.Fatalf("create v1 exit %d; stderr=%q", code, errb)
	}
	if code, _, errb := runCLI("-dir", snapDir, "create", "ver", v2); code != 0 {
		t.Fatalf("re-create (overwrite) exit %d; stderr=%q", code, errb)
	}
	code, out, errb := runCLI("-dir", snapDir, "restore", "ver")
	if code != 0 {
		t.Fatalf("restore exit %d; stderr=%q", code, errb)
	}
	if !bytes.Equal(out, []byte("VERSION-2-longer")) {
		t.Fatalf("overwrite did not take effect: restore returned %q", out)
	}
}
