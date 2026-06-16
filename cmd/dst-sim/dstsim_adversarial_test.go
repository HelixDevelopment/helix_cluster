package main

// Adversarial probes for the dst-sim CI GATE (HXC bug-hunt).
//
// dst-sim is the deterministic-simulation CI gate: exit 0 is a HARD claim that
// "every requested seed was run and found linearizable". The single most
// dangerous failure mode for such a gate is a VACUOUS PASS (CLAUDE-1): it exits
// 0 (green CI) without actually having run any simulation. These tests probe the
// sink-side verdict directly: a non-positive seed count must NOT be reported as
// a passing run, the all-pass report must reflect a real (non-zero) workload,
// and the verdict/report must be self-consistent (the named range must match the
// number of seeds the gate claims to have run).

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// TestGate_NonPositiveSeeds_NotAVacuousPass is the load-bearing CLAUDE-1 probe.
//
// A CI gate that runs ZERO simulations has proven NOTHING. If `dst-sim -seeds 0`
// (or a negative count from a flag typo / arithmetic mistake) exits 0 while
// printing "OK: all N seeds linearizable", that is a PASS-bluff: a green gate on
// an empty run. The gate MUST refuse to claim success for a workload it never
// executed — it must exit non-zero and MUST NOT print the all-pass "OK:" line.
//
// Load-bearing: on the unfixed Gate (which loops `for i:=0; i<seeds`), seeds<=0
// skips the loop entirely and falls through to the "OK: ... exit 0" report, so
// this test FAILS (code==0, "OK:" present). With the guard it PASSES.
func TestGate_NonPositiveSeeds_NotAVacuousPass(t *testing.T) {
	for _, seeds := range []int{0, -1, -5} {
		var buf bytes.Buffer
		code := Gate(&buf, 0, seeds, defaultCfg(), false)
		out := buf.String()

		if code == 0 {
			t.Fatalf("seeds=%d: gate exited 0 (green CI) without running any simulation — vacuous PASS-bluff; output:\n%s", seeds, out)
		}
		if strings.Contains(out, "OK: all") && strings.Contains(out, "linearizable") {
			t.Fatalf("seeds=%d: gate printed an all-pass success line for a run it never executed; output:\n%s", seeds, out)
		}
	}
}

// TestGate_AllPassReport_ReflectsRealWorkload pins that a genuine exit-0 only
// happens for a positive workload AND that the success line names that same
// positive count. This is the positive counterpart: the fix must not break the
// real all-pass path, and the report must be self-consistent.
//
// Load-bearing: if a fix made Gate reject ALL inputs (over-broad guard) this
// would FAIL on the valid seeds=8 run; if the report wording drifted from the
// actual count, the substring assertion would FAIL.
func TestGate_AllPassReport_ReflectsRealWorkload(t *testing.T) {
	const n = 8
	var buf bytes.Buffer
	code := Gate(&buf, 0, n, defaultCfg(), false)
	out := buf.String()
	if code != 0 {
		t.Fatalf("expected exit 0 for %d correct seeds, got %d; output:\n%s", n, code, out)
	}
	want := fmt.Sprintf("OK: all %d seeds linearizable", n)
	if !strings.Contains(out, want) {
		t.Fatalf("all-pass report did not name the real workload (%q); output:\n%s", want, out)
	}
}

// TestGate_NonPositiveSeeds_NoCrash_NoVacuousRangeHeader hardens against the
// nonsensical header arithmetic that accompanies the vacuous path: with the bug,
// the header printed "seeds 0..-1" (start+seeds-1 underflows the intent). After
// the guard the gate must not emit a header advertising a negative-width seed
// range as if it were doing work. It must also never panic on these inputs.
func TestGate_NonPositiveSeeds_NoCrash_NoVacuousRangeHeader(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Gate panicked on non-positive seeds: %v", r)
		}
	}()
	var buf bytes.Buffer
	code := Gate(&buf, 0, 0, defaultCfg(), false)
	out := buf.String()
	if code == 0 {
		t.Fatalf("seeds=0 must not exit 0; output:\n%s", out)
	}
	// The misleading "running 0 seeded simulations (seeds 0..-1)" header is the
	// hallmark of the vacuous path; the guarded gate must reject before printing
	// a success verdict for it.
	if strings.Contains(out, "OK:") {
		t.Fatalf("seeds=0 produced a success verdict; output:\n%s", out)
	}
}
