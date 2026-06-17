package phase7matrix

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// adversarial2RunUUID tags every log line so sink-side evidence is greppable
// and distinct from the first adversarial wave.
const adversarial2RunUUID = "phase7matrix-adv2-c1f0a934-6e57-4d28-9a3b-2f8e1c0d7b46"

// adv2Recompute runs a single-row Verify and returns the recomputed status,
// failing the test on any Verify error.
func adv2Recompute(t *testing.T, root string, row GapRow) Status {
	t.Helper()
	rep, err := Verify(root, Matrix{row})
	if err != nil {
		t.Fatalf("[%s] Verify(%+v): %v", adversarial2RunUUID, row, err)
	}
	return rep.Rows[0].Recomputed
}

// TestAdversarial2AbsoluteEvidencePathIsContained pins the SAFE side of the
// path-resolution contract, complementing TestAdversarialPathEscapesRootCharacterization
// (which pins the unsafe "../" escape). recompute() joins EvidencePath under
// rootDir via filepath.Join, which treats an ABSOLUTE-looking EvidencePath as a
// path component RELATIVE to rootDir (Join("/root/dir","/etc/passwd") ->
// "/root/dir/etc/passwd"). So an attacker who supplies an absolute path like
// "/etc/passwd" does NOT read the real /etc/passwd: it is re-rooted under
// rootDir and, absent there, recomputes to MISSING — it canNOT exfiltrate an
// absolute out-of-tree file's existence.
//
// This is the key reason the traversal surface is LATENT (only "../" sequences
// escape, not absolute paths) rather than an always-on read primitive. We prove
// it SINK-SIDE: an absolute EvidencePath naming a file that DOES exist at the
// system root resolves to a non-existent re-rooted path -> MISSING; and the
// same basename placed UNDER rootDir at the re-rooted location IS found -> DONE.
//
// Mutation (resolve EvidencePath without joining under rootDir / honor absolute
// paths verbatim): the absolute-path case would resolve the real out-of-tree
// file and read DONE instead of MISSING, and this test FAILS.
func TestAdversarial2AbsoluteEvidencePathIsContained(t *testing.T) {
	root := t.TempDir()

	// An absolute EvidencePath. Under filepath.Join semantics this is re-rooted
	// to root + "/abs/secret.md", which does not exist -> MISSING.
	absLike := "/abs/secret.md"
	gotAbsent := adv2Recompute(t, root, GapRow{
		GapID: "P7-ABS", Status: StatusDone, EvidencePath: absLike, EvidenceLine: 1,
	})
	t.Logf("[%s] absolute-looking EvidencePath %q (re-rooted under rootDir) -> recomputed=%s",
		adversarial2RunUUID, absLike, gotAbsent)
	if gotAbsent != StatusMissing {
		t.Fatalf("[%s] absolute EvidencePath must be re-rooted under rootDir and recompute MISSING "+
			"(no verbatim absolute read), got %s", adversarial2RunUUID, gotAbsent)
	}

	// Now materialize the re-rooted target so the SAME absolute EvidencePath
	// resolves to a real file BENEATH rootDir -> DONE. Proves the join target
	// is rootDir + path, not the verbatim absolute path.
	advWrite(t, root, "abs/secret.md", "line1\n")
	gotPresent := adv2Recompute(t, root, GapRow{
		GapID: "P7-ABS", Status: StatusDone, EvidencePath: absLike, EvidenceLine: 1,
	})
	t.Logf("[%s] absolute-looking EvidencePath %q with re-rooted file present -> recomputed=%s",
		adversarial2RunUUID, absLike, gotPresent)
	if gotPresent != StatusDone {
		t.Fatalf("[%s] absolute EvidencePath must resolve UNDER rootDir; re-rooted present file should be DONE, got %s",
			adversarial2RunUUID, gotPresent)
	}
}

// TestAdversarial2OverlongLineFailsClosed pins the countLines buffer contract
// SINK-SIDE: a file containing a single line longer than the 1 MiB scanner
// token cap surfaces a HARD ERROR from Verify rather than silently undercounting
// the file to 0 lines (which would fail-OPEN: a cited line could read DONE/PARTIAL
// off a miscount). Fail-closed (error) is the safe behavior for an unreadable
// evidence file.
//
// Mutation (swallow bufio.ErrTooLong / ignore sc.Err() and return the partial
// count): Verify would return nil error with a bogus line count and this test —
// which requires a non-nil error — FAILS. This is the tripwire against a naive
// "make the scanner not error" change that would silently corrupt counts.
func TestAdversarial2OverlongLineFailsClosed(t *testing.T) {
	root := t.TempDir()
	// One 2 MiB line, no newline -> exceeds the 1 MiB max-token buffer.
	big := strings.Repeat("x", 2*1024*1024)
	full := filepath.Join(root, "big.md")
	if err := os.WriteFile(full, []byte(big), 0o644); err != nil {
		t.Fatalf("[%s] write big file: %v", adversarial2RunUUID, err)
	}

	_, err := Verify(root, Matrix{{
		GapID: "P7-BIG", Status: StatusDone, EvidencePath: "big.md", EvidenceLine: 1,
	}})
	t.Logf("[%s] overlong-line file -> Verify err=%v (must be non-nil: fail-closed, no silent undercount)",
		adversarial2RunUUID, err)
	if err == nil {
		t.Fatalf("[%s] overlong line must surface an error (fail-closed), not silently undercount", adversarial2RunUUID)
	}
}

// TestAdversarial2EncodeParseTabAsymmetry documents a LATENT RISK, not a
// production defect: Encode joins fields with '\t' and Parse splits a row on
// '\t' demanding EXACTLY 5 fields. A field value (here Title) that itself
// contains a '\t' therefore makes Encode emit a line that Parse REJECTS (it
// sees 6 fields). The tab-delimited wire format is lossy for tab-bearing field
// values; the shipped Phase7Matrix() rows contain no tabs, so this never bites
// production data, and the format makes no promise about embedded delimiters.
// We PIN the current behavior so a future Encode-escaping / SplitN(...,5) fix
// (which would make the round-trip succeed) is noticed as an intentional change.
//
// Mutation tripwire: if Parse is changed to SplitN(line,"\t",5) so the last
// field absorbs extra tabs (a real robustness improvement), the round-trip
// below would SUCCEED and reconstruct the tab-bearing Title, flipping this
// assertion — flagging the contract change for review.
func TestAdversarial2EncodeParseTabAsymmetry(t *testing.T) {
	orig := Matrix{{
		GapID: "P7-TAB", Title: "alpha\tbeta", Status: StatusDone,
		EvidencePath: "docs/a.md", EvidenceLine: 7,
	}}
	enc := orig.Encode()
	_, err := Parse(enc)
	t.Logf("[%s] LATENT tab-asymmetry: encoded=%q -> Parse err=%v "+
		"(tab in field breaks the 5-field round-trip)", adversarial2RunUUID, enc, err)
	if err == nil {
		t.Fatalf("[%s] characterization drift: a tab-bearing field now round-trips through Parse; "+
			"if Encode-escaping or SplitN(...,5) was added, update this pin", adversarial2RunUUID)
	}
}

// TestAdversarial2NewlineInFieldSplitsRow documents a second Encode/Parse
// LATENT leniency: Encode terminates each row with '\n' and Parse splits the
// whole blob on '\n'. A field value containing a '\n' is therefore emitted as
// (and re-parsed as) TWO physical lines. With a newline embedded in Title, the
// single logical row encodes to two lines; the first has only 4 tab fields
// (Title's pre-newline half) and Parse rejects the blob on a field-count error.
// Like the tab case this is undefined-format input (no production row carries a
// newline) and is pinned, not fixed.
//
// Mutation tripwire: a future Encode that escapes newlines (or a Parse that
// quotes/joins continuation lines) would make this succeed; this pin flags it.
func TestAdversarial2NewlineInFieldSplitsRow(t *testing.T) {
	orig := Matrix{{
		GapID: "P7-NLF", Title: "line1\nline2", Status: StatusDone,
		EvidencePath: "docs/a.md", EvidenceLine: 1,
	}}
	enc := orig.Encode()
	_, err := Parse(enc)
	t.Logf("[%s] LATENT newline-in-field: encoded=%q -> Parse err=%v",
		adversarial2RunUUID, enc, err)
	if err == nil {
		t.Fatalf("[%s] characterization drift: a newline-bearing field now round-trips; "+
			"if newline escaping was added, update this pin", adversarial2RunUUID)
	}
}

// TestAdversarial2DotDotResolvingInsideRootStaysContained sharpens the traversal
// picture: NOT every ".." is an escape. A ".." that is cancelled out by a prior
// in-tree segment (e.g. "sub/../docs/x.md") resolves to a path STILL under
// rootDir. This proves the latent surface is specifically a NET upward escape,
// not the mere presence of "..". Sink-side: "sub/../real.md" resolves to
// rootDir/real.md and, when that file exists with an in-range line, recomputes
// to DONE — i.e. it is treated identically to "real.md".
//
// Mutation (reject any path containing ".." outright): the in-tree-cancelling
// case would no longer resolve to the real file and would read MISSING instead
// of DONE, and this test FAILS — so it also guards against an over-broad
// "ban all .." patch that would break legitimate paths.
func TestAdversarial2DotDotResolvingInsideRootStaysContained(t *testing.T) {
	root := t.TempDir()
	advWrite(t, root, "real.md", "a\nb\nc\n") // 3 lines, directly under root

	rel := "sub/../real.md" // net resolves to root/real.md (contained)
	got := adv2Recompute(t, root, GapRow{
		GapID: "P7-DOTIN", Status: StatusDone, EvidencePath: rel, EvidenceLine: 2,
	})
	t.Logf("[%s] in-tree-cancelling %q -> recomputed=%s (stays under rootDir)",
		adversarial2RunUUID, rel, got)
	if got != StatusDone {
		t.Fatalf("[%s] a '..' cancelled by a prior in-tree segment must stay contained and resolve "+
			"the real file (DONE), got %s", adversarial2RunUUID, got)
	}
}
