package snapshot

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withTimeout fails the test if fn does not complete promptly, guarding against
// any accidental hang in the filesystem-backed snapshot operations.
func withTimeout(t *testing.T, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("operation did not complete within timeout")
	}
}

// TestAdversarial_ByteRoundTrip proves Restore(Create(data)) == data byte-for-byte
// across hostile payloads (nil, empty, NUL bytes, CR/LF, large binary). This is the
// core "lossless" claim of the golden snapshot manager.
func TestAdversarial_ByteRoundTrip(t *testing.T) {
	cases := map[string][]byte{
		"nil":        nil,
		"empty":      {},
		"nul_bytes":  {0x00, 0x01, 0x00, 0xFF, 0x00},
		"crlf":       []byte("line1\r\nline2\nline3\r"),
		"utf8":       []byte("ünïçödé — 日本語 — 😀"),
		"large_bin":  bytes.Repeat([]byte{0xDE, 0xAD, 0xBE, 0xEF}, 4096),
		"trailing_n": []byte("ends with newline\n"),
	}
	for tag, data := range cases {
		tag, data := tag, data
		t.Run(tag, func(t *testing.T) {
			withTimeout(t, func() {
				m := NewManager(t.TempDir())
				if _, err := m.Create(tag, data, false); err != nil {
					t.Fatalf("Create(%s): %v", tag, err)
				}
				got, err := m.Restore(tag)
				if err != nil {
					t.Fatalf("Restore(%s): %v", tag, err)
				}
				// nil and empty are equivalent on disk; bytes.Equal treats them equal.
				if !bytes.Equal(got, data) {
					t.Fatalf("round-trip mismatch for %s: in=%v out=%v", tag, data, got)
				}
				if err := m.Compare(tag, data); err != nil {
					t.Fatalf("Compare(%s) after round-trip: %v", tag, err)
				}
			})
		})
	}
}

// TestAdversarial_OriginalMutationDoesNotAliasSnapshot proves the snapshot does
// not alias the caller's input slice: mutating the original byte slice after
// Create must not change what Restore returns (no shared mutable backing array).
func TestAdversarial_OriginalMutationDoesNotAliasSnapshot(t *testing.T) {
	m := NewManager(t.TempDir())
	original := []byte("snapshot-state-v1")
	want := append([]byte(nil), original...) // independent copy of the expected value

	if _, err := m.Create("alias", original, false); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Mutate the original AFTER snapshotting.
	for i := range original {
		original[i] = 'X'
	}
	got, err := m.Restore("alias")
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("snapshot aliased caller buffer: mutating original corrupted the snapshot; got=%q want=%q", got, want)
	}
}

// TestAdversarial_CreateListRestoreInverse proves every name that Create accepts
// is faithfully reported by List and that each listed name is restorable. List
// must be a complete, restorable inventory of what was created.
func TestAdversarial_CreateListRestoreInverse(t *testing.T) {
	m := NewManager(t.TempDir())
	// NOTE: only names that round-trip byte-identically through the filesystem are
	// used here. Non-ASCII names are intentionally excluded: normalizing
	// filesystems (e.g. APFS NFD) return a different byte sequence from List than
	// the NFC bytes passed to Create, which is an OS property, not a package bug.
	names := []string{
		"plain",
		"a/foo",
		"b/c/deep",
		"with space",
		"with.golden", // name containing the suffix substring
		"",            // empty name -> .golden at root
	}
	created := map[string][]byte{}
	for i, n := range names {
		data := []byte{byte(i), byte(i + 1)}
		if _, err := m.Create(n, data, false); err != nil {
			t.Fatalf("Create(%q): %v", n, err)
		}
		created[n] = data
	}

	listed, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Every listed name must be restorable, and the bytes must match the source.
	listedSet := map[string]bool{}
	for _, l := range listed {
		listedSet[l] = true
		got, rerr := m.Restore(l)
		if rerr != nil {
			t.Fatalf("listed name %q is not restorable: %v", l, rerr)
		}
		if want, ok := created[l]; ok && !bytes.Equal(got, want) {
			t.Fatalf("listed %q restored wrong bytes: got=%v want=%v", l, got, want)
		}
	}
	// Every created name must appear in List (no created snapshot is invisible).
	for n := range created {
		if !listedSet[n] {
			t.Fatalf("created snapshot %q is missing from List() output %v", n, listed)
		}
	}
}

// TestAdversarial_NameEscapeRejected proves a name that would resolve OUTSIDE
// baseDir is rejected by every method, rather than silently writing/reading/
// deleting unrelated files. Before the fix, Create("../victim") wrote a file
// outside baseDir that List() could never enumerate, and Delete("../x") removed
// arbitrary files.
func TestAdversarial_NameEscapeRejected(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	// Plant a victim file as a sibling of base.
	victim := filepath.Join(root, "victim.golden")
	if err := os.WriteFile(victim, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewManager(base)

	// Create must refuse to escape and must NOT touch the victim.
	if _, err := m.Create("../victim", []byte("OVERWRITTEN"), true); err == nil {
		t.Fatal("Create with escaping name should fail")
	}
	if got, _ := os.ReadFile(victim); !bytes.Equal(got, []byte("ORIGINAL")) {
		t.Fatalf("escaping Create clobbered an external file: %q", got)
	}

	// Restore/Compare must refuse to read outside base.
	if _, err := m.Restore("../victim"); err == nil {
		t.Fatal("Restore with escaping name should fail")
	}
	if err := m.Compare("../victim", []byte("ORIGINAL")); err == nil {
		t.Fatal("Compare with escaping name should fail (must not read external file)")
	}

	// Delete must refuse to remove outside base.
	if err := m.Delete("../victim"); err == nil {
		t.Fatal("Delete with escaping name should fail")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("escaping Delete removed an external file: %v", err)
	}

	// An absolute path as the name is contained by filepath.Join (which treats
	// the second arg as relative), so it must land INSIDE base, not escape.
	absName := filepath.Join(root, "abs")
	p, err := m.Create(absName, []byte("x"), true)
	if err != nil {
		t.Fatalf("Create with absolute-looking name should be contained, got error: %v", err)
	}
	cleanBase := filepath.Clean(base)
	if !bytes.HasPrefix([]byte(filepath.Clean(p)), []byte(cleanBase+string(filepath.Separator))) {
		t.Fatalf("absolute-looking name escaped base: path=%q base=%q", p, base)
	}
}

// TestAdversarial_RestoreMissingNoPanic proves Restore/Compare/Delete of a
// nonexistent snapshot return an error and never panic (DoS safety).
func TestAdversarial_RestoreMissingNoPanic(t *testing.T) {
	m := NewManager(t.TempDir())
	withTimeout(t, func() {
		if _, err := m.Restore("nope"); err == nil {
			t.Error("Restore of missing snapshot should error")
		}
		if err := m.Compare("nope", []byte("x")); err == nil {
			t.Error("Compare of missing snapshot should error")
		}
		if err := m.Delete("nope"); err == nil {
			t.Error("Delete of missing snapshot should error")
		}
	})
}

// TestAdversarial_CreateIsDeterministic proves Create writes byte-identical files
// for identical input (serialization determinism), so golden comparisons are stable.
func TestAdversarial_CreateIsDeterministic(t *testing.T) {
	data := bytes.Repeat([]byte("deterministic-"), 100)

	m1 := NewManager(t.TempDir())
	p1, err := m1.Create("det", data, false)
	if err != nil {
		t.Fatal(err)
	}
	m2 := NewManager(t.TempDir())
	p2, err := m2.Create("det", data, false)
	if err != nil {
		t.Fatal(err)
	}
	b1, _ := os.ReadFile(p1)
	b2, _ := os.ReadFile(p2)
	if !bytes.Equal(b1, b2) {
		t.Fatalf("non-deterministic snapshot bytes: %q vs %q", b1, b2)
	}
}
