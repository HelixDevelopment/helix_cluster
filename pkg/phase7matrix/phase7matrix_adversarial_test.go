package phase7matrix

import (
	"os"
	"path/filepath"
	"testing"
)

// adversarialRunUUID tags every log line so sink-side evidence is greppable.
const adversarialRunUUID = "phase7matrix-adv-3b8e9d10-7a42-4f1c-bb6e-d4c5e6f70819"

// advWrite writes a file at rootDir/rel with exact byte content (so we control
// trailing-newline / empty-file edge cases the line counter must honor).
func advWrite(t *testing.T, rootDir, rel, content string) {
	t.Helper()
	full := filepath.Join(rootDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func advRecompute(t *testing.T, root string, row GapRow) Status {
	t.Helper()
	rep, err := Verify(root, Matrix{row})
	if err != nil {
		t.Fatalf("[%s] Verify(%+v): %v", adversarialRunUUID, row, err)
	}
	return rep.Rows[0].Recomputed
}

// TestAdversarialLineBoundaryExact pins the EvidenceLine boundary SINK-SIDE: the
// recompute rule is "line > n -> PARTIAL", i.e. the cited line is valid up to AND
// INCLUDING the last line. This is the off-by-one frontier. We assert the actual
// returned verdict at n-1, n (last valid), and n+1 (first invalid).
//
// Mutation A (>= instead of >): line==n would flip DONE->PARTIAL; the n case below
// FAILS.
// Mutation B (drop the line check / always DONE for present files): the n+1 case
// below would read DONE instead of PARTIAL; that case FAILS.
func TestAdversarialLineBoundaryExact(t *testing.T) {
	root := t.TempDir()
	// Exactly 5 lines, trailing newline.
	advWrite(t, root, "docs/five.md", "a\nb\nc\nd\ne\n")

	cases := []struct {
		line int
		want Status
	}{
		{4, StatusDone},    // one before last -> in range
		{5, StatusDone},    // EXACT last line -> still in range (boundary)
		{6, StatusPartial}, // one past last -> out of range
	}
	for _, c := range cases {
		got := advRecompute(t, root, GapRow{
			GapID: "P7-BND", Status: StatusDone, EvidencePath: "docs/five.md", EvidenceLine: c.line,
		})
		t.Logf("[%s] 5-line file, EvidenceLine=%d -> recomputed=%s (want %s)",
			adversarialRunUUID, c.line, got, c.want)
		if got != c.want {
			t.Fatalf("[%s] boundary off-by-one: line=%d got=%s want=%s",
				adversarialRunUUID, c.line, got, c.want)
		}
	}
}

// TestAdversarialCountLineTrailingNewlineContract pins the two documented
// countLines promises SINK-SIDE: (1) a trailing newline does NOT create a phantom
// final line, and (2) a file ending WITHOUT a newline still counts its last
// partial line. Both files have 3 logical lines; line 3 must be DONE and line 4
// PARTIAL for BOTH, proving the counts are identical regardless of the final
// newline.
//
// Mutation (phantom-line: count a trailing-newline file as 4): the "with newline,
// line 4" case below would read DONE instead of PARTIAL and FAIL.
func TestAdversarialCountLineTrailingNewlineContract(t *testing.T) {
	root := t.TempDir()
	advWrite(t, root, "docs/withnl.md", "x\ny\nz\n") // 3 lines, trailing \n
	advWrite(t, root, "docs/nonl.md", "x\ny\nz")     // 3 lines, no trailing \n

	for _, rel := range []string{"docs/withnl.md", "docs/nonl.md"} {
		atLast := advRecompute(t, root, GapRow{GapID: "P7-NL", Status: StatusDone, EvidencePath: rel, EvidenceLine: 3})
		past := advRecompute(t, root, GapRow{GapID: "P7-NL", Status: StatusDone, EvidencePath: rel, EvidenceLine: 4})
		t.Logf("[%s] %s: line3=%s line4=%s", adversarialRunUUID, rel, atLast, past)
		if atLast != StatusDone {
			t.Fatalf("[%s] %s line 3 should be in range (DONE), got %s", adversarialRunUUID, rel, atLast)
		}
		if past != StatusPartial {
			t.Fatalf("[%s] %s line 4 should be out of range (PARTIAL), got %s", adversarialRunUUID, rel, past)
		}
	}
}

// TestAdversarialEmptyFileLineOne pins the empty-file edge SINK-SIDE: an empty
// (0-line) but PRESENT file cited at line 1 recomputes to PARTIAL (line 1 > 0
// lines), NOT to a fail-open DONE; while the same empty file cited at line 0
// (path-only) recomputes to DONE. This proves "present" alone is not treated as
// DONE when a specific line is demanded.
//
// Mutation (treat any present file as DONE): the line-1 case would read DONE and
// FAIL.
func TestAdversarialEmptyFileLineOne(t *testing.T) {
	root := t.TempDir()
	advWrite(t, root, "docs/empty.md", "") // 0 lines

	line1 := advRecompute(t, root, GapRow{GapID: "P7-E", Status: StatusDone, EvidencePath: "docs/empty.md", EvidenceLine: 1})
	line0 := advRecompute(t, root, GapRow{GapID: "P7-E", Status: StatusDone, EvidencePath: "docs/empty.md", EvidenceLine: 0})
	t.Logf("[%s] empty file: line1=%s line0=%s", adversarialRunUUID, line1, line0)

	if line1 != StatusPartial {
		t.Fatalf("[%s] empty file cited line 1 should be PARTIAL (not fail-open DONE), got %s", adversarialRunUUID, line1)
	}
	if line0 != StatusDone {
		t.Fatalf("[%s] empty file cited line 0 (path-only) should be DONE, got %s", adversarialRunUUID, line0)
	}
}

// TestAdversarialDirectoryNeverDone pins the directory rule SINK-SIDE: a present
// directory at the evidence path always recomputes to PARTIAL — even with
// EvidenceLine 0 (which for a regular file would mean path-only DONE). A directory
// can never anchor a specific line, so it must NOT fail open to DONE. This also
// pins the ORDER of the checks (IsDir is tested before the EvidenceLine<=0
// shortcut).
//
// Mutation (move the EvidenceLine<=0 DONE shortcut before the IsDir check): a
// directory at line 0 would read DONE and this test would FAIL.
func TestAdversarialDirectoryNeverDone(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs", "subdir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, line := range []int{0, 1, 99} {
		got := advRecompute(t, root, GapRow{
			GapID: "P7-D", Status: StatusDone, EvidencePath: "docs/subdir", EvidenceLine: line,
		})
		t.Logf("[%s] directory EvidenceLine=%d -> recomputed=%s", adversarialRunUUID, line, got)
		if got != StatusPartial {
			t.Fatalf("[%s] directory at line=%d should be PARTIAL (never DONE), got %s",
				adversarialRunUUID, line, got)
		}
	}
}

// TestAdversarialMissingVsPartialVsDone pins the full unknown/present trichotomy
// SINK-SIDE in one shot: an ABSENT path -> MISSING (safe default, not a fail-open
// DONE); a present file with an in-range line -> DONE; a present file with an
// out-of-range line -> PARTIAL. The unknown/absent input is the fail-open probe:
// it must deny (MISSING), never silently pass as DONE.
func TestAdversarialMissingVsPartialVsDone(t *testing.T) {
	root := t.TempDir()
	advWrite(t, root, "docs/present.md", "l1\nl2\n")

	missing := advRecompute(t, root, GapRow{GapID: "P7-M", Status: StatusDone, EvidencePath: "docs/absent.md", EvidenceLine: 1})
	done := advRecompute(t, root, GapRow{GapID: "P7-M", Status: StatusDone, EvidencePath: "docs/present.md", EvidenceLine: 1})
	partial := advRecompute(t, root, GapRow{GapID: "P7-M", Status: StatusDone, EvidencePath: "docs/present.md", EvidenceLine: 7})
	t.Logf("[%s] absent=%s presentInRange=%s presentOutOfRange=%s",
		adversarialRunUUID, missing, done, partial)

	if missing != StatusMissing {
		t.Fatalf("[%s] absent path must recompute MISSING (no fail-open), got %s", adversarialRunUUID, missing)
	}
	if done != StatusDone {
		t.Fatalf("[%s] present in-range must be DONE, got %s", adversarialRunUUID, done)
	}
	if partial != StatusPartial {
		t.Fatalf("[%s] present out-of-range must be PARTIAL, got %s", adversarialRunUUID, partial)
	}
}

// TestAdversarialNegativeEvidenceLineCharacterization documents a LATENT RISK, not
// a contract violation. The package contract enumerates only EvidenceLine == 0
// ("path-only") and positive 1-based lines; a NEGATIVE line is out-of-contract /
// malformed input. recompute()'s `EvidenceLine <= 0` guard silently folds a
// negative line into the SAME most-permissive verdict as the legitimate 0 case:
// a present file recomputes to DONE.
//
// This is fail-open ON MALFORMED INPUT (a negative line arguably should not pass
// as fully-anchored DONE), but the documentation makes no promise for negatives,
// so per honest three-way discrimination we PIN current behavior rather than
// "fix" an undefined contract. If a future revision decides malformed lines must
// deny (PARTIAL), THIS test is the tripwire that will flag the behavior change.
func TestAdversarialNegativeEvidenceLineCharacterization(t *testing.T) {
	root := t.TempDir()
	advWrite(t, root, "docs/neg.md", "a\nb\nc\n") // 3 lines

	got := advRecompute(t, root, GapRow{
		GapID: "P7-NEG", Status: StatusDone, EvidencePath: "docs/neg.md", EvidenceLine: -5,
	})
	t.Logf("[%s] LATENT-RISK negative EvidenceLine=-5 on present file -> recomputed=%s "+
		"(folds into the EvidenceLine<=0 path-only DONE branch)", adversarialRunUUID, got)

	// Current, documented-as-undefined behavior: negative == 0 == path-only DONE.
	if got != StatusDone {
		t.Fatalf("[%s] characterization drift: negative EvidenceLine now yields %s (was DONE); "+
			"if intentional, update this pin and document the malformed-line rule",
			adversarialRunUUID, got)
	}
}

// TestAdversarialPathEscapesRootCharacterization documents a second LATENT RISK:
// recompute() joins EvidencePath under rootDir with no containment check, so an
// EvidencePath using ".." escapes rootDir and can resolve a file OUTSIDE the
// intended tree, recomputing to DONE. The package doc says paths are "resolved
// beneath" rootDir descriptively but promises no sandbox, and the matrix is
// trusted (declared) data, so this is a NOTE/pin rather than a fixed bug. If a
// future revision adds path containment (e.g. reject "../" or verify the cleaned
// path stays within rootDir), this test flags the behavior change.
func TestAdversarialPathEscapesRootCharacterization(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Dir(root)
	outside := filepath.Join(parent, "phase7matrix-adv-outside-evidence.md")
	if err := os.WriteFile(outside, []byte("x\ny\nz\n"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	defer os.Remove(outside)

	rel := "../" + filepath.Base(outside)
	got := advRecompute(t, root, GapRow{
		GapID: "P7-ESC", Status: StatusMissing, EvidencePath: rel, EvidenceLine: 2,
	})
	t.Logf("[%s] LATENT-RISK EvidencePath %q escapes rootDir -> recomputed=%s "+
		"(no path-containment enforced)", adversarialRunUUID, rel, got)

	if got != StatusDone {
		t.Fatalf("[%s] characterization drift: ../-escape now yields %s (was DONE); "+
			"if path containment was added, update this pin", adversarialRunUUID, got)
	}
}

// TestAdversarialParseRejectsWrongFieldCount and accepts trailing-garbage line
// numbers — pins Parse SINK-SIDE. A line with !=5 tab fields is rejected; a line
// whose EvidenceLine has trailing non-digits is silently truncated by Sscanf
// (latent), but a fully non-numeric / empty field errors. We pin the
// error-vs-silent boundary so a future stricter Parse is noticed.
func TestAdversarialParseFieldContract(t *testing.T) {
	// Wrong field count -> error.
	if _, err := Parse("only\ttwo\n"); err == nil {
		t.Fatalf("[%s] Parse should reject a 2-field line", adversarialRunUUID)
	}
	// Empty EvidenceLine field -> error (Sscanf EOF).
	if _, err := Parse("P7-Z\tDONE\t\tdocs/x.md\ttitle\n"); err == nil {
		t.Fatalf("[%s] Parse should reject an empty EvidenceLine field", adversarialRunUUID)
	}
	// Trailing garbage after the integer is SILENTLY accepted (Sscanf stops at
	// the first non-digit). This is a latent leniency; pin current behavior.
	m, err := Parse("P7-Z\tDONE\t12xy\tdocs/x.md\ttitle\n")
	if err != nil {
		t.Fatalf("[%s] Parse unexpectedly rejected trailing-garbage int: %v", adversarialRunUUID, err)
	}
	t.Logf("[%s] LATENT Parse leniency: '12xy' -> EvidenceLine=%d (trailing garbage dropped)",
		adversarialRunUUID, m[0].EvidenceLine)
	if m[0].EvidenceLine != 12 {
		t.Fatalf("[%s] characterization drift: '12xy' parsed to %d (was 12)",
			adversarialRunUUID, m[0].EvidenceLine)
	}
}
