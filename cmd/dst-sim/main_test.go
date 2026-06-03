package main

import (
	"bytes"
	"strings"
	"testing"
)

// defaultCfg mirrors the production gate parameters (no Buggy injection).
func defaultCfg() ScenarioConfig { return ScenarioConfig{MaxSteps: 4000} }

// TestGate_AllPass_Exit0_Over1000Seeds is the primary CLOSURE proof: the runner
// executes >= 1000 seeded simulations of the replicated-register scenario, runs
// the porcupine linearizability check on each, and returns exit code 0 because a
// CORRECT primary keeps every seed linearizable. This is the sink-side evidence
// that the gate passes a working system (not a vacuous all-skip).
//
// Load-bearing: if the scenario or checker regressed so that a correct primary
// produced a non-linearizable history, this asserts code != 0 and FAILS.
func TestGate_AllPass_Exit0_Over1000Seeds(t *testing.T) {
	var buf bytes.Buffer
	code := Gate(&buf, 0, 1000, defaultCfg(), false)
	if code != 0 {
		t.Fatalf("expected exit 0 over 1000 correct seeds, got %d; output:\n%s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "OK: all 1000 seeds linearizable") {
		t.Fatalf("missing all-pass report line; output:\n%s", buf.String())
	}
}

// TestGate_LoadBearing_BuggySeedExitsNonZeroNamingSeed proves the OTHER half of
// CLOSURE: the gate is load-bearing. We run the SAME runner over the SAME seed
// range with the linearizability-violating primary injected. The gate MUST exit
// non-zero AND name the FIRST offending seed (seeds are processed ascending, so
// the named seed is deterministic). seed 2 is the first violator under the
// injected stale-read bug (established empirically and asserted here).
//
// Load-bearing: if the porcupine check were inverted/removed (always OK), the
// buggy run would exit 0 and this test FAILS. If the runner did not report the
// offending seed, the "FAIL: seed 2" assertion FAILS.
func TestGate_LoadBearing_BuggySeedExitsNonZeroNamingSeed(t *testing.T) {
	cfg := ScenarioConfig{MaxSteps: 4000, Buggy: true}
	var buf bytes.Buffer
	code := Gate(&buf, 0, 1000, cfg, false)
	if code == 0 {
		t.Fatalf("buggy primary must surface a violation and exit non-zero, got exit 0; output:\n%s", buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "FAIL: seed 2 surfaced a linearizability violation") {
		t.Fatalf("expected the gate to name the first offending seed (2); output:\n%s", out)
	}
	if !strings.Contains(out, "witness:") {
		t.Fatalf("expected a witness operation in the failure report; output:\n%s", out)
	}
}

// TestRunSeed_BuggyVsCorrect_AtSeed2 isolates the per-seed primitive: at seed 2
// the correct primary is linearizable and the buggy primary is NOT. This pins
// the exact discrimination the gate relies on, independent of the loop in Gate.
//
// Load-bearing: inverting the buggy stale-read (serving `cur` instead of `prev`)
// makes the buggy case linearizable and this test FAILS on the second assertion.
func TestRunSeed_BuggyVsCorrect_AtSeed2(t *testing.T) {
	if r := RunSeed(2, ScenarioConfig{MaxSteps: 4000}); !r.OK {
		t.Fatalf("correct primary must be linearizable at seed 2, but it was not: %s", r.Reason)
	}
	r := RunSeed(2, ScenarioConfig{MaxSteps: 4000, Buggy: true})
	if r.OK {
		t.Fatal("buggy primary must be NON-linearizable at seed 2, but the checker passed it (PASS-bluff)")
	}
	if r.NumOps == 0 {
		t.Fatal("expected a non-empty client history to check (vacuous history would be a bluff)")
	}
}

// TestGate_Deterministic_SameSeedSet_SameOutcome proves the runner is
// deterministic: the SAME seed set reproduces the SAME pass/fail outcome AND the
// SAME byte-for-byte report. This is the determinism CLOSURE — a flaky gate is
// useless. Run twice and compare exit codes and full output.
//
// Load-bearing: if any nondeterminism (wall-clock, unseeded RNG, map iteration)
// leaked into the scenario, the two outputs would differ and this FAILS.
func TestGate_Deterministic_SameSeedSet_SameOutcome(t *testing.T) {
	run := func(buggy bool) (int, string) {
		var buf bytes.Buffer
		cfg := ScenarioConfig{MaxSteps: 4000, Buggy: buggy}
		code := Gate(&buf, 0, 300, cfg, true)
		return code, buf.String()
	}

	c1, o1 := run(false)
	c2, o2 := run(false)
	if c1 != c2 || o1 != o2 {
		t.Fatalf("non-deterministic correct run: codes %d vs %d; outputs differ=%v", c1, c2, o1 != o2)
	}
	if c1 != 0 {
		t.Fatalf("correct run expected exit 0, got %d", c1)
	}

	b1, bo1 := run(true)
	b2, bo2 := run(true)
	if b1 != b2 || bo1 != bo2 {
		t.Fatalf("non-deterministic buggy run: codes %d vs %d; outputs differ=%v", b1, b2, bo1 != bo2)
	}
	if b1 == 0 {
		t.Fatalf("buggy run expected non-zero exit, got %d", b1)
	}
}

// TestRunSeed_Deterministic_PerSeed pins determinism at the per-seed primitive:
// RunSeed(seed) returns the identical verdict, op count, and (on failure) the
// identical offending operation across repeated calls.
func TestRunSeed_Deterministic_PerSeed(t *testing.T) {
	for _, seed := range []int64{0, 2, 7, 42, 123} {
		cfg := ScenarioConfig{MaxSteps: 4000, Buggy: true}
		a := RunSeed(seed, cfg)
		b := RunSeed(seed, cfg)
		if a.OK != b.OK || a.NumOps != b.NumOps {
			t.Fatalf("seed %d non-deterministic: (OK %v/%v, ops %d/%d)", seed, a.OK, b.OK, a.NumOps, b.NumOps)
		}
		if !a.OK {
			if a.Offending.Call != b.Offending.Call || a.Offending.Return != b.Offending.Return {
				t.Fatalf("seed %d non-deterministic offending op across runs", seed)
			}
		}
	}
}
