package main

import (
	"strings"
	"testing"
)

// TestRunEmitsBurstRequestOnSpill is the REQUIRED named test that pins the
// mutation guard in Run.
//
// It feeds the sequence 0.50, 0.95, 0.80, 0.62 through a controller built
// from bursthysteresis.DefaultConfig (SpillHigh=0.90, RecoverLow=0.70) and
// asserts:
//   - exactly one SPILL line containing "burst allocation request" and the runID
//   - exactly one RECOVER line containing the runID
//   - no other transition lines are emitted
//
// Mutation guard: if the Fprintf call in Run that emits "burst allocation
// request" is removed or blanked, the SPILL line written to out will no longer
// contain that substring, and the assertion below will fail.
func TestRunEmitsBurstRequestOnSpill(t *testing.T) {
	const runID = "test-burst-run-001"

	samples := []float64{0.50, 0.95, 0.80, 0.62}

	var buf strings.Builder
	err := Run(runID, samples, &buf)
	if err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}

	output := buf.String()
	lines := nonEmptyLines(output)

	// Assert exactly two transition lines were emitted.
	if len(lines) != 2 {
		t.Fatalf("Run emitted %d non-empty lines, want 2:\n%s", len(lines), output)
	}

	spillLine := lines[0]
	recoverLine := lines[1]

	// MUTATION GUARD assertion — pinned by this test:
	// The SPILL line MUST contain the substring "burst allocation request".
	// Removing the Fprintf call in Run that emits this substring makes this
	// assertion fail.
	if !strings.Contains(spillLine, "burst allocation request") {
		t.Errorf("SPILL line missing \"burst allocation request\":\n  got: %q", spillLine)
	}

	// The SPILL line must contain the runID.
	if !strings.Contains(spillLine, runID) {
		t.Errorf("SPILL line missing runID %q:\n  got: %q", runID, spillLine)
	}

	// The SPILL line must start with "SPILL".
	if !strings.HasPrefix(spillLine, "SPILL") {
		t.Errorf("expected SPILL line to start with \"SPILL\", got: %q", spillLine)
	}

	// The RECOVER line must start with "RECOVER".
	if !strings.HasPrefix(recoverLine, "RECOVER") {
		t.Errorf("expected RECOVER line to start with \"RECOVER\", got: %q", recoverLine)
	}

	// The RECOVER line must contain the runID.
	if !strings.Contains(recoverLine, runID) {
		t.Errorf("RECOVER line missing runID %q:\n  got: %q", runID, recoverLine)
	}

	// The RECOVER line must NOT contain "burst allocation request" —
	// that substring is exclusive to SPILL.
	if strings.Contains(recoverLine, "burst allocation request") {
		t.Errorf("RECOVER line unexpectedly contains \"burst allocation request\":\n  got: %q", recoverLine)
	}
}

// TestRunNoEventBelowThreshold verifies that samples all below SpillHigh
// produce no output lines (no spurious transitions).
func TestRunNoEventBelowThreshold(t *testing.T) {
	const runID = "test-below-thresh-run"

	// All samples are below DefaultConfig SpillHigh (0.90).
	samples := []float64{0.10, 0.40, 0.70, 0.89}

	var buf strings.Builder
	err := Run(runID, samples, &buf)
	if err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}

	lines := nonEmptyLines(buf.String())
	if len(lines) != 0 {
		t.Errorf("expected no output lines for sub-threshold samples, got %d:\n%s", len(lines), buf.String())
	}
}

// TestRunMultipleCycles verifies that Run correctly tracks multiple SPILL/RECOVER
// cycles and that every SPILL line carries "burst allocation request".
func TestRunMultipleCycles(t *testing.T) {
	const runID = "test-multi-cycle-run"

	// Three full cycles with dead-band samples in between.
	samples := []float64{
		0.95, 0.80, 0.60, // cycle 1: SPILL, dead-band, RECOVER
		0.92, 0.75, 0.65, // cycle 2: SPILL, dead-band, RECOVER
		0.91, 0.85, 0.68, // cycle 3: SPILL, dead-band, RECOVER
	}

	var buf strings.Builder
	err := Run(runID, samples, &buf)
	if err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}

	lines := nonEmptyLines(buf.String())

	// Expect 6 lines: 3 SPILL + 3 RECOVER.
	if len(lines) != 6 {
		t.Fatalf("expected 6 output lines for 3 cycles, got %d:\n%s", len(lines), buf.String())
	}

	for i, line := range lines {
		if i%2 == 0 {
			// Even indices: SPILL lines.
			if !strings.HasPrefix(line, "SPILL") {
				t.Errorf("line[%d]: expected SPILL prefix, got: %q", i, line)
			}
			// Every SPILL line must contain "burst allocation request" and the runID.
			if !strings.Contains(line, "burst allocation request") {
				t.Errorf("line[%d]: SPILL line missing \"burst allocation request\": %q", i, line)
			}
			if !strings.Contains(line, runID) {
				t.Errorf("line[%d]: SPILL line missing runID %q: %q", i, runID, line)
			}
		} else {
			// Odd indices: RECOVER lines.
			if !strings.HasPrefix(line, "RECOVER") {
				t.Errorf("line[%d]: expected RECOVER prefix, got: %q", i, line)
			}
			if !strings.Contains(line, runID) {
				t.Errorf("line[%d]: RECOVER line missing runID %q: %q", i, runID, line)
			}
		}
	}
}

// TestRunEmptySliceReturnsError verifies that Run returns ErrNoSamples when
// given an empty sample slice rather than silently succeeding with no output.
func TestRunEmptySliceReturnsError(t *testing.T) {
	var buf strings.Builder
	err := Run("empty-run", []float64{}, &buf)
	if err == nil {
		t.Fatal("Run with empty samples: expected error, got nil")
	}
	if err != ErrNoSamples {
		t.Errorf("Run with empty samples: want ErrNoSamples, got: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("Run with empty samples wrote unexpected output: %q", buf.String())
	}
}

// TestRunRunIDEmbeddedInEveryLine feeds a single SPILL+RECOVER cycle and
// asserts the caller-supplied runID appears on both output lines, proving
// sink-side correlation across runs is reliable.
func TestRunRunIDEmbeddedInEveryLine(t *testing.T) {
	const runID = "sink-side-runid-check-99"

	samples := []float64{0.95, 0.60}

	var buf strings.Builder
	if err := Run(runID, samples, &buf); err != nil {
		t.Fatalf("Run error: %v", err)
	}

	lines := nonEmptyLines(buf.String())
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d:\n%s", len(lines), buf.String())
	}

	for i, line := range lines {
		if !strings.Contains(line, runID) {
			t.Errorf("line[%d] missing runID %q: %q", i, runID, line)
		}
	}
}

// TestReadSamplesFromReader verifies the stdin-parsing helper correctly
// tokenises space- and newline-separated floats.
func TestReadSamplesFromReader(t *testing.T) {
	input := "0.10 0.50\n0.90\n0.65\n"
	r := strings.NewReader(input)

	got, err := readSamplesFromReader(r)
	if err != nil {
		t.Fatalf("readSamplesFromReader error: %v", err)
	}

	want := []float64{0.10, 0.50, 0.90, 0.65}
	if len(got) != len(want) {
		t.Fatalf("len=%d, want %d; got=%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %v, want %v", i, got[i], want[i])
		}
	}
}

// TestReadSamplesFromReaderInvalidToken checks that a non-numeric token causes
// an error instead of a silent zero.
func TestReadSamplesFromReaderInvalidToken(t *testing.T) {
	r := strings.NewReader("0.50 notanumber 0.90")
	_, err := readSamplesFromReader(r)
	if err == nil {
		t.Fatal("expected error for non-numeric token, got nil")
	}
}

// nonEmptyLines splits s into lines and returns only those that are non-empty
// after trimming whitespace.
func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
