package main

import (
	"math"
	"strings"
	"testing"
	"time"
)

// burst_adversarial_test.go — adversarial probes against the burst-controller
// command's testable core (Run) and its sample parsers.
//
// Focus (class-d numeric): non-finite samples (NaN/±Inf) must NOT silently
// drive — or silently bypass — a real burst-allocation control decision; the
// SpillHigh boundary must be exact; out-of-range and empty input must be
// handled deterministically; the controller must not get stuck.
//
// runWithTimeout guards every Run call so a stuck/looping controller surfaces
// as a test failure rather than a hung suite.
func runWithTimeout(t *testing.T, runID string, samples []float64) (string, error) {
	t.Helper()
	type res struct {
		out string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		var buf strings.Builder
		err := Run(runID, samples, &buf)
		ch <- res{buf.String(), err}
	}()
	select {
	case r := <-ch:
		return r.out, r.err
	case <-time.After(5 * time.Second):
		t.Fatalf("Run did not return within timeout for samples=%v (possible stuck state)", samples)
		return "", nil
	}
}

// ---------------------------------------------------------------------------
// CLASS-D NUMERIC: non-finite samples must not drive a bogus control decision.
// ---------------------------------------------------------------------------

// TestRunRejectsNaNSample probes the headline class-d hazard: a NaN sample.
//
// A NaN bypasses every numeric comparison (NaN >= 0.90 is false, NaN <= 0.70
// is false), so when fed to the raw controller it is SILENTLY SWALLOWED while
// in MONITOR and, worse, can WEDGE the controller in SPILL (NaN can never
// satisfy the RecoverLow guard). The command documents its input as
// "utilization samples in [0,1]"; a NaN is never a valid utilization. Run MUST
// reject it with an error rather than feed it into a production burst decision.
//
// Sink-side: on rejection, NO transition line may be written.
func TestRunRejectsNaNSample(t *testing.T) {
	out, err := runWithTimeout(t, "nan-run", []float64{0.50, math.NaN(), 0.95})
	if err == nil {
		t.Errorf("Run accepted a NaN sample (silent mis-decision); want a rejection error.\n  output=%q", out)
	}
	if out != "" {
		t.Errorf("Run wrote output for a stream containing NaN; want none on rejection.\n  output=%q", out)
	}
}

// TestRunRejectsPosInfSample probes +Inf: it satisfies +Inf >= SpillHigh and
// therefore fires a REAL "burst allocation request" carrying a garbage
// utilization value. A +Inf utilization is never valid; Run must reject it
// instead of emitting a bogus allocation decision.
func TestRunRejectsPosInfSample(t *testing.T) {
	out, err := runWithTimeout(t, "posinf-run", []float64{math.Inf(1)})
	if err == nil {
		t.Errorf("Run accepted +Inf and (per controller) emits a burst request on garbage input; want rejection.\n  output=%q", out)
	}
	if strings.Contains(out, "burst allocation request") {
		t.Errorf("Run emitted a burst allocation request for a +Inf sample:\n  output=%q", out)
	}
}

// TestRunRejectsNegInfSample probes -Inf: it satisfies -Inf <= RecoverLow and
// can fire a RECOVER on garbage. Must be rejected.
func TestRunRejectsNegInfSample(t *testing.T) {
	out, err := runWithTimeout(t, "neginf-run", []float64{0.95, math.Inf(-1)})
	if err == nil {
		t.Errorf("Run accepted -Inf; want rejection.\n  output=%q", out)
	}
}

// TestRunFiniteStreamUnaffected pins that the rejection does NOT regress the
// normal finite path: a clean SPILL→RECOVER cycle still produces exactly two
// lines with the expected substrings.
func TestRunFiniteStreamUnaffected(t *testing.T) {
	out, err := runWithTimeout(t, "finite-run", []float64{0.50, 0.95, 0.80, 0.62})
	if err != nil {
		t.Fatalf("Run on a clean finite stream returned error: %v", err)
	}
	lines := nonEmptyLines(out)
	if len(lines) != 2 {
		t.Fatalf("clean finite stream: want 2 lines, got %d:\n%s", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], "SPILL") || !strings.Contains(lines[0], "burst allocation request") {
		t.Errorf("first line not a proper SPILL: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "RECOVER") {
		t.Errorf("second line not a RECOVER: %q", lines[1])
	}
}

// ---------------------------------------------------------------------------
// THRESHOLD BOUNDARY: the SpillHigh fence (>= 0.90) must be exact.
// ---------------------------------------------------------------------------

// TestRunSpillBoundaryExact pins the >= boundary: exactly 0.90 SPILLs, the
// nearest representable value below does not. Guards an off-by-one (>= vs >)
// regression in the underlying controller as observed through the command.
func TestRunSpillBoundaryExact(t *testing.T) {
	// Exactly at the fence: must SPILL.
	out, err := runWithTimeout(t, "boundary-at", []float64{0.90})
	if err != nil {
		t.Fatalf("Run(0.90) error: %v", err)
	}
	if !strings.Contains(out, "SPILL") {
		t.Errorf("sample exactly 0.90 must SPILL (>= boundary), got no SPILL:\n  output=%q", out)
	}

	// Just below the fence: must NOT SPILL.
	below := math.Nextafter(0.90, 0.0)
	out2, err := runWithTimeout(t, "boundary-below", []float64{below})
	if err != nil {
		t.Fatalf("Run(%v) error: %v", below, err)
	}
	if strings.Contains(out2, "SPILL") {
		t.Errorf("sample %v (just below 0.90) must NOT SPILL, got:\n  output=%q", below, out2)
	}
}

// ---------------------------------------------------------------------------
// EMPTY / DETERMINISM.
// ---------------------------------------------------------------------------

// TestRunEmptyDeterministic pins the empty-input contract (ErrNoSamples, no
// output) — an empty stream must be an explicit error, never a silent success.
func TestRunEmptyDeterministic(t *testing.T) {
	out, err := runWithTimeout(t, "empty-run", []float64{})
	if err != ErrNoSamples {
		t.Errorf("empty samples: want ErrNoSamples, got %v", err)
	}
	if out != "" {
		t.Errorf("empty samples wrote output: %q", out)
	}
}

// ---------------------------------------------------------------------------
// PARSER LAYER: the flag/stdin parsers feed Run, so they are the real ingress
// for hostile tokens. Pin that they parse the literal "NaN"/"Inf" tokens (Go's
// ParseFloat accepts them) so the Run-layer guard is the thing that must stop
// them — documenting where the defense lives.
// ---------------------------------------------------------------------------

// TestReadSamplesParsesNonFiniteTokens proves the parser does NOT itself reject
// "NaN"/"Inf" (strconv.ParseFloat accepts them), so the guard MUST live in Run.
// This pins the threat model: non-finite values genuinely reach Run.
func TestReadSamplesParsesNonFiniteTokens(t *testing.T) {
	got, err := readSamplesFromReader(strings.NewReader("0.5 NaN Inf -Inf"))
	if err != nil {
		t.Fatalf("readSamplesFromReader unexpectedly errored on non-finite tokens: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("want 4 parsed tokens, got %d: %v", len(got), got)
	}
	if !math.IsNaN(got[1]) || !math.IsInf(got[2], 1) || !math.IsInf(got[3], -1) {
		t.Errorf("parser did not yield the non-finite values it accepts: %v", got)
	}
}
