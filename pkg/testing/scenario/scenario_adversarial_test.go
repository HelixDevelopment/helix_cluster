package scenario

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/testing/chaos"
)

// ===========================================================================
// Adversarial probes for pkg/testing/scenario.
//
// PRIMARY HYPOTHESIS — DETERMINISM BLUFF: the engine docstring claims
// "Run executes the scenario deterministically (no wall-clock or sleeps)".
// A sibling package (pkg/testing/dst) made the same claim but leaked
// nondeterminism by ranging over a Go map when building an ordered result.
// These probes assert that the SAME scenario produces a BYTE-IDENTICAL
// ordered ExecutionLog across many runs, that no wall-clock is consulted,
// and that concurrent runs are race-free. Additional probes pin numeric
// edges, empty/nil inputs, and error propagation.
//
// All probes MUST pass on the production code as it stands; the engine is
// genuinely slice-ordered (no map range / rand / time / goroutines), so no
// production fix is expected. Each probe documents the contract it pins.
// ===========================================================================

// serializeLog renders an ExecutionLog to a stable byte string so that two
// runs can be compared for FULL structural + ordering equality, not just a
// hand-picked field. Any reordering of phases or injected faults, any flag
// flip, or any counter drift changes these bytes.
func serializeLog(log ExecutionLog) string {
	var b []byte
	b = append(b, fmt.Sprintf("scenario=%q aborted=%v reason=%q total=%d\n",
		log.ScenarioName, log.Aborted, log.AbortReason, log.TotalFaults)...)
	for i, p := range log.Phases {
		b = append(b, fmt.Sprintf("  phase[%d] name=%q blast=%v aborted=%v faults=%v\n",
			i, p.Name, p.BlastRadiusExceeded, p.Aborted, p.FaultsInjected)...)
	}
	return string(b)
}

// richScenarioYAML exercises every ordered-output path: multiple phases, a
// per-phase clamp, a global-budget clamp, and an abort condition. If ANY of
// these were built from a randomized source the serialized bytes would vary
// run to run.
const richScenarioYAML = `
name: adversarial-determinism
max_affected: 5
abort_on:
  field: faults_injected
  op: ">"
  value: 4
phases:
  - name: zeta
    faults: ["z1", "z2", "z3"]
    duration_ms: 100
    max_faults: 2
  - name: yankee
    faults: ["y1", "y2", "y3", "y4"]
    duration_ms: 200
    max_faults: 10
  - name: xray
    faults: ["x1"]
    duration_ms: 50
    max_faults: 10
`

// ---------------------------------------------------------------------------
// PROBE 1 (DETERMINISM): same scenario, 40 runs, BYTE-IDENTICAL output.
// CLASSIFY: DOCUMENTED CONTRACT — engine.go:33 claims deterministic Run.
// A FAIL here would be a REAL determinism bug (PASS-bluff).
// ---------------------------------------------------------------------------

func TestAdversarial_RunIsByteIdenticalAcrossManyRuns(t *testing.T) {
	s, err := ParseScenarioYAML([]byte(richScenarioYAML))
	if err != nil {
		t.Fatalf("ParseScenarioYAML: %v", err)
	}

	const iterations = 40
	first := serializeLog(s.Run())
	for i := 0; i < iterations; i++ {
		got := serializeLog(s.Run())
		if got != first {
			t.Fatalf("non-deterministic ExecutionLog on iteration %d:\n--- first ---\n%s\n--- got ---\n%s",
				i, first, got)
		}
	}

	// Parsing fresh each time must also yield the identical ordered result —
	// guards against any hidden parse-order state.
	for i := 0; i < iterations; i++ {
		s2, perr := ParseScenarioYAML([]byte(richScenarioYAML))
		if perr != nil {
			t.Fatalf("ParseScenarioYAML(reparse %d): %v", i, perr)
		}
		if got := serializeLog(s2.Run()); got != first {
			t.Fatalf("reparse %d produced different log:\n--- first ---\n%s\n--- got ---\n%s",
				i, first, got)
		}
	}
}

// ---------------------------------------------------------------------------
// PROBE 2 (DETERMINISM): the byte-identical result is also the CORRECT one.
// Pins the exact ordered semantics so a future map-range regression that
// happened to be stable-on-this-machine still fails.
// ---------------------------------------------------------------------------

func TestAdversarial_RunOrderedResultIsExact(t *testing.T) {
	s, err := ParseScenarioYAML([]byte(richScenarioYAML))
	if err != nil {
		t.Fatalf("ParseScenarioYAML: %v", err)
	}
	got := serializeLog(s.Run())

	// Walk-through of the contract:
	//   zeta:   3 faults, max_faults=2 -> inject z1,z2 (clamped) blast=true; total=2
	//   yankee: 4 faults, global budget left = 5-2 = 3 -> inject y1,y2,y3
	//           blast=true (1 dropped); total=5; abort ">4" fires (5>4) -> aborted
	//   xray:   never runs.
	want := "" +
		"scenario=\"adversarial-determinism\" aborted=true reason=\"abort: faults_injected > 4 (current=5)\" total=5\n" +
		"  phase[0] name=\"zeta\" blast=true aborted=false faults=[z1 z2]\n" +
		"  phase[1] name=\"yankee\" blast=true aborted=true faults=[y1 y2 y3]\n"

	if got != want {
		t.Fatalf("ordered result mismatch:\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

// ---------------------------------------------------------------------------
// PROBE 3 (DETERMINISM): different inputs produce different output — proves
// the equality in PROBE 1 is meaningful (not a constant), and that the
// engine actually reads the scenario data.
// ---------------------------------------------------------------------------

func TestAdversarial_DifferentScenariosDiffer(t *testing.T) {
	a, err := ParseScenarioYAML([]byte(richScenarioYAML))
	if err != nil {
		t.Fatalf("parse a: %v", err)
	}
	// Same shape but reordered faults in phase zeta.
	altYAML := `
name: adversarial-determinism
max_affected: 5
abort_on:
  field: faults_injected
  op: ">"
  value: 4
phases:
  - name: zeta
    faults: ["z3", "z2", "z1"]
    duration_ms: 100
    max_faults: 2
  - name: yankee
    faults: ["y1", "y2", "y3", "y4"]
    duration_ms: 200
    max_faults: 10
  - name: xray
    faults: ["x1"]
    duration_ms: 50
    max_faults: 10
`
	b, err := ParseScenarioYAML([]byte(altYAML))
	if err != nil {
		t.Fatalf("parse b: %v", err)
	}
	if serializeLog(a.Run()) == serializeLog(b.Run()) {
		t.Fatal("reordered fault list produced identical output — engine ignores fault order")
	}
}

// ---------------------------------------------------------------------------
// PROBE 4 (DETERMINISM): Run does not consult wall-clock. We run, sleep a
// real interval, run again, and require identical output. A wall-clock leak
// (e.g. timestamps in the log) would change the bytes.
// Guarded against hangs by construction (sleep is bounded, no blocking ops).
// ---------------------------------------------------------------------------

func TestAdversarial_RunIgnoresWallClock(t *testing.T) {
	s, err := ParseScenarioYAML([]byte(richScenarioYAML))
	if err != nil {
		t.Fatalf("ParseScenarioYAML: %v", err)
	}
	before := serializeLog(s.Run())
	time.Sleep(15 * time.Millisecond)
	after := serializeLog(s.Run())
	if before != after {
		t.Fatalf("output changed across a wall-clock interval (wall-clock leak):\n--- before ---\n%s\n--- after ---\n%s",
			before, after)
	}
}

// ---------------------------------------------------------------------------
// PROBE 5 (RACE/DETERMINISM): concurrent Run calls on the same *Scenario are
// race-free and each returns the same ordered result. Run on a single package
// with -race in CI. Bounded goroutine count; no blocking — cannot hang.
// ---------------------------------------------------------------------------

func TestAdversarial_ConcurrentRunsRaceFree(t *testing.T) {
	s, err := ParseScenarioYAML([]byte(richScenarioYAML))
	if err != nil {
		t.Fatalf("ParseScenarioYAML: %v", err)
	}
	want := serializeLog(s.Run())

	const goroutines = 16
	var wg sync.WaitGroup
	results := make([]string, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = serializeLog(s.Run())
		}(i)
	}
	wg.Wait()

	for i, got := range results {
		if got != want {
			t.Fatalf("goroutine %d produced divergent log:\n--- want ---\n%s\n--- got ---\n%s",
				i, want, got)
		}
	}
}

// ---------------------------------------------------------------------------
// PROBE 6 (CORRECTNESS): global budget exhaustion mid-phase. A phase that
// partially fits the remaining budget must inject exactly the remaining
// count and set BlastRadiusExceeded. Pins the integer clamp math.
// ---------------------------------------------------------------------------

func TestAdversarial_GlobalBudgetPartialClamp(t *testing.T) {
	y := `
name: partial-budget
max_affected: 2
phases:
  - name: only
    faults: ["a", "b", "c", "d"]
    duration_ms: 10
    max_faults: 10
`
	s, err := ParseScenarioYAML([]byte(y))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	log := s.Run()
	if len(log.Phases) != 1 {
		t.Fatalf("phases: got %d want 1", len(log.Phases))
	}
	p := log.Phases[0]
	if len(p.FaultsInjected) != 2 {
		t.Errorf("FaultsInjected: got %d want 2 (budget=2)", len(p.FaultsInjected))
	}
	if !p.BlastRadiusExceeded {
		t.Errorf("BlastRadiusExceeded: got false want true")
	}
	if log.TotalFaults != 2 {
		t.Errorf("TotalFaults: got %d want 2", log.TotalFaults)
	}
}

// ---------------------------------------------------------------------------
// PROBE 7 (CORRECTNESS): empty fault list in a phase. No faults injected,
// blast radius NOT exceeded (0 listed == 0 injected). Edge: empty slice.
// ---------------------------------------------------------------------------

func TestAdversarial_EmptyFaultListPhase(t *testing.T) {
	y := `
name: empty-faults
max_affected: 5
phases:
  - name: noop
    faults: []
    duration_ms: 10
    max_faults: 3
`
	s, err := ParseScenarioYAML([]byte(y))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	log := s.Run()
	p := log.Phases[0]
	if len(p.FaultsInjected) != 0 {
		t.Errorf("FaultsInjected: got %d want 0", len(p.FaultsInjected))
	}
	if p.BlastRadiusExceeded {
		t.Errorf("BlastRadiusExceeded: got true want false (0 listed, 0 injected)")
	}
	// FaultsInjected must be non-nil ([]string{}) per the engine contract.
	if p.FaultsInjected == nil {
		t.Errorf("FaultsInjected is nil; engine documents an empty (non-nil) slice")
	}
}

// ---------------------------------------------------------------------------
// PROBE 8 (CORRECTNESS): AbortCondition with an UNKNOWN field never fires.
// CLASSIFY: DOCUMENTED CONTRACT — evaluate() returns (false,"") for unknown
// fields "to avoid false positives".
// ---------------------------------------------------------------------------

func TestAdversarial_UnknownAbortFieldNeverFires(t *testing.T) {
	y := `
name: unknown-field
max_affected: 100
abort_on:
  field: not_a_real_field
  op: ">"
  value: 0
phases:
  - name: p
    faults: ["a", "b", "c"]
    duration_ms: 10
    max_faults: 10
`
	s, err := ParseScenarioYAML([]byte(y))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	log := s.Run()
	if log.Aborted {
		t.Errorf("Aborted: got true want false (unknown abort field must never fire)")
	}
	if log.TotalFaults != 3 {
		t.Errorf("TotalFaults: got %d want 3", log.TotalFaults)
	}
}

// ---------------------------------------------------------------------------
// PROBE 9 (CORRECTNESS): AbortCondition with an UNKNOWN op never fires.
// CLASSIFY: DOCUMENTED CONTRACT — only "==", ">", "<" are recognised; any
// other op leaves fires=false.
// ---------------------------------------------------------------------------

func TestAdversarial_UnknownAbortOpNeverFires(t *testing.T) {
	y := `
name: unknown-op
max_affected: 100
abort_on:
  field: faults_injected
  op: "!="
  value: 0
phases:
  - name: p
    faults: ["a", "b"]
    duration_ms: 10
    max_faults: 10
`
	s, err := ParseScenarioYAML([]byte(y))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	log := s.Run()
	if log.Aborted {
		t.Errorf("Aborted: got true want false (unknown op %q must not fire)", "!=")
	}
}

// ---------------------------------------------------------------------------
// PROBE 10 (CORRECTNESS): error propagation — malformed YAML and nil/empty
// inputs surface typed errors rather than panicking.
// ---------------------------------------------------------------------------

func TestAdversarial_ParseErrorPropagation(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
	}{
		{"garbage", []byte("\t\t::: not yaml :::")},
		{"nil", nil},
		{"empty", []byte("")},
		{"list-not-map", []byte("- a\n- b\n")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Must not panic; must return an error (never a usable Scenario).
			s, err := ParseScenarioYAML(tc.in)
			if err == nil && s != nil {
				// A genuinely valid empty doc would fail validation (no name),
				// so any non-error path here is a contract break.
				t.Errorf("expected error for %q input, got scenario=%+v", tc.name, s)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// PROBE 11 (CORRECTNESS): EmergencyStop ordered RecoveredNames is
// registration-ordered and deterministic across many runs. Pins the
// stop-path ordering (it iterates ChaosRunner.faults, a slice).
// ---------------------------------------------------------------------------

func TestAdversarial_EmergencyStopNamesDeterministic(t *testing.T) {
	build := func() *StopController {
		runner := chaos.NewChaosRunner()
		// Distinct node IDs so any reordering is visible in the names.
		for _, id := range []string{"n0", "n1", "n2", "n3"} {
			runner.AddFault(&chaos.NodeCrash{NodeID: id})
		}
		if err := runner.ApplyAll(); err != nil {
			t.Fatalf("ApplyAll: %v", err)
		}
		return NewStopController(runner, 10_000)
	}

	first := build().EmergencyStop()
	for i := 0; i < 30; i++ {
		got := build().EmergencyStop()
		if len(got.RecoveredNames) != len(first.RecoveredNames) {
			t.Fatalf("iter %d: RecoveredNames len drift: got %d want %d",
				i, len(got.RecoveredNames), len(first.RecoveredNames))
		}
		for j := range got.RecoveredNames {
			if got.RecoveredNames[j] != first.RecoveredNames[j] {
				t.Fatalf("iter %d: RecoveredNames[%d] drift: got %q want %q",
					i, j, got.RecoveredNames[j], first.RecoveredNames[j])
			}
		}
		if got.FaultsRecovered != 4 || got.HaltLatencyMS != 40 {
			t.Fatalf("iter %d: stop math drift: recovered=%d latency=%d",
				i, got.FaultsRecovered, got.HaltLatencyMS)
		}
	}
}
