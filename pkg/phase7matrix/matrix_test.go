package phase7matrix

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const runUUID = "phase7matrix-run-7f3c1a2e-0b9d-4c5a-9e21-aa11bb22cc33"

// writeFile creates a file at rootDir/rel with the given number of lines,
// creating parent directories as needed.
func writeFile(t *testing.T, rootDir, rel string, lines int) {
	t.Helper()
	full := filepath.Join(rootDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	var b strings.Builder
	for i := 0; i < lines; i++ {
		b.WriteString("evidence line ")
		b.WriteByte(byte('0' + (i % 10)))
		b.WriteByte('\n')
	}
	if err := os.WriteFile(full, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// buildMatrix returns a matrix whose declared statuses all MATCH a fixture
// tree it writes under rootDir. Its row count is taken from the production
// Phase7Matrix() (so the clean-reconcile and drift fixtures track the shipped
// deliverable count, not an independent test constant). Rows are a mix of DONE
// (line in range), PARTIAL (line past EOF), and MISSING (no file).
func buildMatrix(t *testing.T, rootDir string) Matrix {
	t.Helper()
	rows := len(Phase7Matrix())
	var m Matrix
	for i := 0; i < rows; i++ {
		gapID := "P7-" + string(rune('A'+i/10)) + string(rune('0'+i%10))
		switch i % 3 {
		case 0: // DONE: file exists with enough lines, cited line in range.
			rel := filepath.ToSlash(filepath.Join("docs", gapID+".md"))
			writeFile(t, rootDir, rel, 12)
			m = append(m, GapRow{
				GapID: gapID, Title: "done gap " + gapID, Status: StatusDone,
				EvidencePath: rel, EvidenceLine: 8,
			})
		case 1: // PARTIAL: file exists but cited line is beyond EOF.
			rel := filepath.ToSlash(filepath.Join("docs", gapID+".md"))
			writeFile(t, rootDir, rel, 4)
			m = append(m, GapRow{
				GapID: gapID, Title: "partial gap " + gapID, Status: StatusPartial,
				EvidencePath: rel, EvidenceLine: 99,
			})
		case 2: // MISSING: no file on disk.
			rel := filepath.ToSlash(filepath.Join("docs", gapID+".md"))
			m = append(m, GapRow{
				GapID: gapID, Title: "missing gap " + gapID, Status: StatusMissing,
				EvidencePath: rel, EvidenceLine: 3,
			})
		}
	}
	return m
}

// TestVerifyCleanMatrixZeroMismatches proves CLOSURE CLAUSE 1: a matrix whose
// declared statuses all match the on-disk evidence reconciles cleanly.
//
// CLOSURE NOTE (non-discriminating on its own): this test is NOT independent
// evidence that recompute() consults disk. Under an "echo declared status"
// mutation (recompute returns row.Status) every declaration would trivially
// match and this test would STILL PASS. It must NOT be cited as proof that
// Verify reads the filesystem. The echo mutation is killed elsewhere — by the
// drift tests (clause 2) and by TestVerifyCoversEveryProductionRow, which
// omits one shipped evidence file and asserts a real disk-derived MISSING.
//
// Mutation (what this test alone catches): if recompute() mis-derived
// DONE/PARTIAL/MISSING from real files (e.g. off-by-one on the line check),
// the matching declarations here would be flagged and this test would FAIL.
func TestVerifyCleanMatrixZeroMismatches(t *testing.T) {
	root := t.TempDir()
	m := buildMatrix(t, root)

	rep, err := Verify(root, m)
	if err != nil {
		t.Fatalf("[%s] Verify error: %v", runUUID, err)
	}
	mism := rep.Mismatches()
	t.Logf("[%s] before: declared rows=%d ; after: mismatches=%d clean=%v",
		runUUID, len(m), len(mism), rep.Clean())

	if len(mism) != 0 {
		for _, rr := range mism {
			t.Errorf("[%s] unexpected mismatch %s: declared=%s recomputed=%s",
				runUUID, rr.GapID, rr.Declared, rr.Recomputed)
		}
	}
	if !rep.Clean() {
		t.Fatalf("[%s] expected clean report, got %d mismatches", runUUID, len(mism))
	}
	for _, rr := range rep.Rows {
		if !rr.OK {
			t.Fatalf("[%s] row %s not OK", runUUID, rr.GapID)
		}
	}
}

// TestVerifyDetectsMissingPathDrift proves CLOSURE CLAUSE 2 (missing-path
// drift): a row declared DONE whose EvidencePath does not exist on disk must
// recompute to MISSING and be flagged.
//
// Mutation: if Verify ignored the on-disk check and echoed the declared
// status, recomputed would read DONE, OK would be true, and this test would
// FAIL — directly killing the "always echo declared" mutation.
func TestVerifyDetectsMissingPathDrift(t *testing.T) {
	root := t.TempDir()
	m := Matrix{
		{GapID: "P7-X1", Title: "declared done but file absent", Status: StatusDone,
			EvidencePath: "docs/never-created.md", EvidenceLine: 5},
	}

	rep, err := Verify(root, m)
	if err != nil {
		t.Fatalf("[%s] Verify error: %v", runUUID, err)
	}
	got := rep.Rows[0]
	t.Logf("[%s] before: declared=%s ; after: recomputed=%s OK=%v",
		runUUID, got.Declared, got.Recomputed, got.OK)

	if got.Recomputed != StatusMissing {
		t.Fatalf("[%s] expected recomputed MISSING, got %s", runUUID, got.Recomputed)
	}
	if got.OK {
		t.Fatalf("[%s] expected mismatch flagged (OK=false) for DONE-vs-MISSING", runUUID)
	}
	if len(rep.Mismatches()) != 1 {
		t.Fatalf("[%s] expected exactly 1 mismatch, got %d", runUUID, len(rep.Mismatches()))
	}
}

// TestVerifyDetectsLineOutOfRangeDrift proves CLOSURE CLAUSE 2 (line-drift): a
// row declared DONE whose EvidenceLine exceeds the file length must recompute
// to PARTIAL and be flagged. The companion in-range case must NOT be flagged,
// proving the line check is a real range comparison, not a constant.
//
// Mutation: if recompute ignored EvidenceLine (or treated every present file
// as DONE), the out-of-range row would read DONE and stay OK — this test would
// FAIL. If it always returned PARTIAL for present files, the in-range row
// would be flagged — this test would also FAIL.
func TestVerifyDetectsLineOutOfRangeDrift(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "docs/short.md", 10) // file has exactly 10 lines

	m := Matrix{
		// Declared DONE, but cites line 50 in a 10-line file -> drift to PARTIAL.
		{GapID: "P7-Y1", Title: "line past EOF", Status: StatusDone,
			EvidencePath: "docs/short.md", EvidenceLine: 50},
		// Declared DONE, cites the exact last line -> in range, stays DONE.
		{GapID: "P7-Y2", Title: "line in range", Status: StatusDone,
			EvidencePath: "docs/short.md", EvidenceLine: 10},
	}

	rep, err := Verify(root, m)
	if err != nil {
		t.Fatalf("[%s] Verify error: %v", runUUID, err)
	}
	outOfRange, inRange := rep.Rows[0], rep.Rows[1]
	t.Logf("[%s] before: both declared=DONE ; after: outOfRange recomputed=%s OK=%v ; inRange recomputed=%s OK=%v",
		runUUID, outOfRange.Recomputed, outOfRange.OK, inRange.Recomputed, inRange.OK)

	if outOfRange.Recomputed != StatusPartial {
		t.Fatalf("[%s] line-past-EOF: expected PARTIAL, got %s", runUUID, outOfRange.Recomputed)
	}
	if outOfRange.OK {
		t.Fatalf("[%s] line-past-EOF: expected mismatch flagged (OK=false)", runUUID)
	}
	if inRange.Recomputed != StatusDone {
		t.Fatalf("[%s] in-range: expected DONE, got %s", runUUID, inRange.Recomputed)
	}
	if !inRange.OK {
		t.Fatalf("[%s] in-range: expected OK=true (no drift)", runUUID)
	}
}

// TestPhase7MatrixIsShipped23RowProductionLedger proves CLOSURE CLAUSE 3
// (completeness) against PRODUCTION data, not a test fixture. It asserts that
// the shipped Phase7Matrix() carries exactly Phase7GapRowCount (23) rows, that
// the literal 23 is a real production constant, that every shipped row is
// structurally well-formed and GapIDs are unique. The 23 is NOT derived from
// any test-side loop — it lives in matrix.go and is read back here.
//
// Mutation: if Phase7Matrix() dropped or duplicated a row, or gained/lost one,
// len(Phase7Matrix()) would diverge from Phase7GapRowCount and this test would
// FAIL. If a shipped row had an empty GapID/EvidencePath or invalid Status,
// the structural checks would FAIL. If Phase7GapRowCount were changed away from
// 23, the literal-anchor assertion would FAIL.
func TestPhase7MatrixIsShipped23RowProductionLedger(t *testing.T) {
	prod := Phase7Matrix()
	t.Logf("[%s] before: Phase7GapRowCount=%d ; after: len(Phase7Matrix())=%d",
		runUUID, Phase7GapRowCount, len(prod))

	// The deliverable claim ("23-row Phase-7 gap matrix") anchored to a literal
	// in production code, not a test loop bound.
	if Phase7GapRowCount != 23 {
		t.Fatalf("[%s] Phase7GapRowCount must be 23, got %d", runUUID, Phase7GapRowCount)
	}
	if len(prod) != Phase7GapRowCount {
		t.Fatalf("[%s] shipped matrix has %d rows, want Phase7GapRowCount=%d",
			runUUID, len(prod), Phase7GapRowCount)
	}

	seen := make(map[string]bool, len(prod))
	for i, row := range prod {
		if row.GapID == "" {
			t.Fatalf("[%s] shipped row %d has empty GapID", runUUID, i)
		}
		if seen[row.GapID] {
			t.Fatalf("[%s] shipped matrix has duplicate GapID %s", runUUID, row.GapID)
		}
		seen[row.GapID] = true
		if row.EvidencePath == "" {
			t.Fatalf("[%s] shipped row %s has empty EvidencePath", runUUID, row.GapID)
		}
		if !row.Status.Valid() {
			t.Fatalf("[%s] shipped row %s has invalid Status %q", runUUID, row.GapID, row.Status)
		}
	}
}

// TestVerifyCoversEveryProductionRow proves CLOSURE CLAUSE 3 (full coverage):
// Verify emits exactly one RowReport per row of the PRODUCTION Phase7Matrix(),
// in order, covering every shipped GapID — it does not skip or short-circuit.
// Evidence files are materialized so the shipped DONE declarations reconcile
// cleanly, proving the report is end-user-trustworthy.
//
// Mutation: if Verify dropped MISSING rows or short-circuited on first drift,
// len(rep.Rows) != len(prod) and the per-GapID coverage map would miss an
// entry — this test would FAIL. If recompute() echoed declared status instead
// of reading disk, the deliberately-removed P7-C2 evidence file below would
// still report DONE/clean — instead it must surface as a MISSING mismatch,
// which this test asserts, killing the echo mutation independently of clause 2.
func TestVerifyCoversEveryProductionRow(t *testing.T) {
	root := t.TempDir()
	prod := Phase7Matrix()

	// Materialize on-disk evidence for every shipped row EXCEPT the last one,
	// so the report is mostly clean but carries exactly one real disk-derived
	// mismatch (declared DONE, file absent -> recomputed MISSING).
	var omitted string
	for i, row := range prod {
		if i == len(prod)-1 {
			omitted = row.GapID
			continue
		}
		// EvidenceLine 1 is in range for any non-empty file.
		writeFile(t, root, row.EvidencePath, 3)
	}

	rep, err := Verify(root, prod)
	if err != nil {
		t.Fatalf("[%s] Verify error: %v", runUUID, err)
	}
	mism := rep.Mismatches()
	t.Logf("[%s] before: prod rows=%d omitted=%s ; after: report rows=%d mismatches=%d",
		runUUID, len(prod), omitted, len(rep.Rows), len(mism))

	if len(rep.Rows) != len(prod) {
		t.Fatalf("[%s] report length %d != production matrix length %d", runUUID, len(rep.Rows), len(prod))
	}
	covered := make(map[string]bool, len(prod))
	for _, rr := range rep.Rows {
		if covered[rr.GapID] {
			t.Fatalf("[%s] duplicate coverage for %s", runUUID, rr.GapID)
		}
		covered[rr.GapID] = true
	}
	for _, row := range prod {
		if !covered[row.GapID] {
			t.Fatalf("[%s] shipped row %s not covered by report", runUUID, row.GapID)
		}
	}

	// Exactly the omitted row must drift to MISSING; the rest stay DONE/clean.
	if len(mism) != 1 {
		t.Fatalf("[%s] expected exactly 1 disk-derived mismatch (the omitted row), got %d", runUUID, len(mism))
	}
	if mism[0].GapID != omitted {
		t.Fatalf("[%s] expected mismatch on omitted row %s, got %s", runUUID, omitted, mism[0].GapID)
	}
	if mism[0].Recomputed != StatusMissing {
		t.Fatalf("[%s] omitted row %s: expected recomputed MISSING (disk-derived), got %s",
			runUUID, omitted, mism[0].Recomputed)
	}
}

// TestEncodeParseRoundTrip proves the optional Parse/Encode pair preserves
// every field, so an encoded ledger reconstructs to an identical matrix that
// Verify treats the same way.
//
// Mutation: if Encode dropped EvidenceLine or Parse mis-mapped a column, the
// reconstructed row would differ and this test would FAIL.
func TestEncodeParseRoundTrip(t *testing.T) {
	orig := Matrix{
		{GapID: "P7-A0", Title: "alpha", Status: StatusDone, EvidencePath: "docs/a.md", EvidenceLine: 7},
		{GapID: "P7-A1", Title: "beta gap", Status: StatusMissing, EvidencePath: "docs/b.md", EvidenceLine: 0},
	}
	encoded := orig.Encode()
	got, err := Parse(encoded)
	if err != nil {
		t.Fatalf("[%s] Parse error: %v", runUUID, err)
	}
	t.Logf("[%s] before: rows=%d ; after: encoded=%q parsed rows=%d",
		runUUID, len(orig), encoded, len(got))

	if len(got) != len(orig) {
		t.Fatalf("[%s] round-trip length %d != %d", runUUID, len(got), len(orig))
	}
	for i := range orig {
		if got[i] != orig[i] {
			t.Fatalf("[%s] round-trip row %d mismatch: got %+v want %+v", runUUID, i, got[i], orig[i])
		}
	}
}

// TestVerifyEmptyGapIDError proves Verify refuses a structurally invalid
// matrix rather than silently producing a bogus clean report.
//
// Mutation: if the GapID guard were removed, Verify would return a nil error
// and this test would FAIL.
func TestVerifyEmptyGapIDError(t *testing.T) {
	root := t.TempDir()
	_, err := Verify(root, Matrix{{GapID: "", Status: StatusMissing, EvidencePath: "x.md"}})
	t.Logf("[%s] empty-GapID Verify err=%v", runUUID, err)
	if err == nil {
		t.Fatalf("[%s] expected error for empty GapID", runUUID)
	}
}
