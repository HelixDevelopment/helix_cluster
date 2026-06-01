package scenario

import (
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/testing/chaos"
)

// nodeCrashFaults returns n *chaos.NodeCrash faults with distinct IDs.
// NodeCrash is the fault type that chaos.ChaosRunner.ActiveFaults() actually
// tracks (via IsCrashed). Using these faults ensures sink-side validation is
// real, not a bluff against a type that ActiveFaults ignores.
func nodeCrashFaults(n int) []*chaos.NodeCrash {
	faults := make([]*chaos.NodeCrash, n)
	for i := range faults {
		faults[i] = &chaos.NodeCrash{NodeID: "node"}
	}
	return faults
}

// ---------------------------------------------------------------------------
// Test 1: EmergencyStop restores all active faults (sink-side: ActiveFaults
//         empty after stop, names listed in result).
// ---------------------------------------------------------------------------

func TestEmergencyStopRestoresAllFaults(t *testing.T) {
	const N = 3

	runner := chaos.NewChaosRunner()
	faults := nodeCrashFaults(N)
	for _, f := range faults {
		runner.AddFault(f)
	}

	// Apply all faults so they appear in ActiveFaults().
	if err := runner.ApplyAll(); err != nil {
		t.Fatalf("ApplyAll: %v", err)
	}

	// Precondition (bite setup): all N faults must be active before the stop.
	// If ActiveFaults returns 0 here the test setup is broken, not the controller.
	preFaults := runner.ActiveFaults()
	if len(preFaults) != N {
		t.Fatalf("pre-stop ActiveFaults: got %d, want %d", len(preFaults), N)
	}

	sc := NewStopController(runner, 2000) // generous budget
	result := sc.EmergencyStop()

	// ---- Primary assertions -----------------------------------------------

	// FaultsRecovered must equal N.
	// Anti-bluff bite: if EmergencyStop does not call RestoreAll, the faults
	// remain applied; IsCrashed returns true for each NodeCrash so ActiveFaults
	// still returns N entries, but FaultsRecovered would be wrong if the
	// snapshot is taken post-restore (it snapshots BEFORE restore, so it is N).
	if result.FaultsRecovered != N {
		t.Errorf("FaultsRecovered: got %d, want %d", result.FaultsRecovered, N)
	}

	// RecoveredNames must list all fault names.
	if len(result.RecoveredNames) != N {
		t.Errorf("RecoveredNames len: got %d, want %d", len(result.RecoveredNames), N)
	}
	for i, name := range result.RecoveredNames {
		if name != "node-crash" {
			t.Errorf("RecoveredNames[%d]: got %q, want %q", i, name, "node-crash")
		}
	}

	// ---- Sink-side anti-bluff bite ----------------------------------------
	// A stop that does NOT call RestoreAll leaves ActiveFaults > 0.
	// If RestoreAll is missing or no-op, this assertion fails.
	postFaults := runner.ActiveFaults()
	if len(postFaults) != 0 {
		t.Errorf("post-stop ActiveFaults: got %d, want 0 — faults were NOT restored", len(postFaults))
	}
}

// ---------------------------------------------------------------------------
// Test 2: Halt budget math is real, not hardcoded.
//           large budget  -> WithinBudget true
//           tiny budget   -> WithinBudget false when faults > 0
// ---------------------------------------------------------------------------

func TestHaltBudgetMath(t *testing.T) {
	const N = 5 // 5 × 10 ms = 50 ms simulated latency

	// ---- Sub-test A: large budget (e.g. 2000 ms) -> WithinBudget true -----
	t.Run("large_budget_within", func(t *testing.T) {
		runner := chaos.NewChaosRunner()
		for _, f := range nodeCrashFaults(N) {
			runner.AddFault(f)
		}
		if err := runner.ApplyAll(); err != nil {
			t.Fatalf("ApplyAll: %v", err)
		}

		sc := NewStopController(runner, 2000)
		result := sc.EmergencyStop()

		expectedLatency := int64(N) * perFaultCostMS // 50 ms
		if result.HaltLatencyMS != expectedLatency {
			t.Errorf("HaltLatencyMS: got %d, want %d", result.HaltLatencyMS, expectedLatency)
		}
		if !result.WithinBudget {
			t.Errorf("WithinBudget: got false, want true (latency=%d <= budget=%d)",
				result.HaltLatencyMS, 2000)
		}
	})

	// ---- Sub-test B: tiny budget (1 ms) with N faults -> WithinBudget false
	// Anti-bluff bite: if WithinBudget is hardcoded true, this fails.
	t.Run("tiny_budget_exceeds", func(t *testing.T) {
		runner := chaos.NewChaosRunner()
		for _, f := range nodeCrashFaults(N) {
			runner.AddFault(f)
		}
		if err := runner.ApplyAll(); err != nil {
			t.Fatalf("ApplyAll: %v", err)
		}

		sc := NewStopController(runner, 1) // budget = 1 ms; 5 faults = 50 ms
		result := sc.EmergencyStop()

		expectedLatency := int64(N) * perFaultCostMS // 50 ms
		if result.HaltLatencyMS != expectedLatency {
			t.Errorf("HaltLatencyMS: got %d, want %d", result.HaltLatencyMS, expectedLatency)
		}
		// WithinBudget must be false: 50 > 1.
		if result.WithinBudget {
			t.Errorf("WithinBudget: got true, want false (latency=%d > budget=%d)",
				result.HaltLatencyMS, 1)
		}
	})

	// ---- Sub-test C: zero active faults -> latency 0, always within budget.
	t.Run("zero_faults_zero_latency", func(t *testing.T) {
		runner := chaos.NewChaosRunner()
		// Add faults but do NOT apply them — ActiveFaults() returns 0.
		for _, f := range nodeCrashFaults(N) {
			runner.AddFault(f)
		}

		sc := NewStopController(runner, 0) // even budget=0 must be within
		result := sc.EmergencyStop()

		if result.FaultsRecovered != 0 {
			t.Errorf("FaultsRecovered: got %d, want 0 (no faults applied)", result.FaultsRecovered)
		}
		if result.HaltLatencyMS != 0 {
			t.Errorf("HaltLatencyMS: got %d, want 0 (no faults)", result.HaltLatencyMS)
		}
		if !result.WithinBudget {
			t.Errorf("WithinBudget: got false, want true (zero latency, zero budget)")
		}
	})
}

// ---------------------------------------------------------------------------
// Test 3: AutoRecover returns nil after EmergencyStop; returns error when
//         faults remain active (no stop was called).
// ---------------------------------------------------------------------------

func TestAutoRecover(t *testing.T) {
	// ---- Sub-test A: AutoRecover after EmergencyStop returns nil -----------
	t.Run("nil_after_stop", func(t *testing.T) {
		runner := chaos.NewChaosRunner()
		for _, f := range nodeCrashFaults(3) {
			runner.AddFault(f)
		}
		if err := runner.ApplyAll(); err != nil {
			t.Fatalf("ApplyAll: %v", err)
		}

		sc := NewStopController(runner, 2000)
		sc.EmergencyStop() // restores all

		// Sink-side: faults truly gone.
		if got := runner.ActiveFaults(); len(got) != 0 {
			t.Fatalf("post-stop ActiveFaults: got %d, want 0", len(got))
		}

		if err := sc.AutoRecover(); err != nil {
			t.Errorf("AutoRecover after EmergencyStop: got %v, want nil", err)
		}
	})

	// ---- Sub-test B: AutoRecover returns error when no stop was called -----
	// Anti-bluff bite: if AutoRecover always returns nil, this assertion fails.
	t.Run("error_when_faults_remain", func(t *testing.T) {
		runner := chaos.NewChaosRunner()
		for _, f := range nodeCrashFaults(2) {
			runner.AddFault(f)
		}
		if err := runner.ApplyAll(); err != nil {
			t.Fatalf("ApplyAll: %v", err)
		}

		// Faults are applied but EmergencyStop was NOT called.
		sc := NewStopController(runner, 2000)

		err := sc.AutoRecover()
		if err == nil {
			t.Errorf("AutoRecover with active faults: got nil, want non-nil error")
		}
	})
}

// ---------------------------------------------------------------------------
// Test 4: RunWithStop — scenario runs and stop fires, faults cleaned up.
// ---------------------------------------------------------------------------

func TestRunWithStop(t *testing.T) {
	yaml := `
name: stop-scenario
max_affected: 10
phases:
  - name: phase-one
    faults: ["f1", "f2"]
    duration_ms: 100
    max_faults: 5
  - name: phase-two
    faults: ["f3"]
    duration_ms: 50
    max_faults: 5
`
	s, err := ParseScenarioYAML([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseScenarioYAML: %v", err)
	}

	runner := chaos.NewChaosRunner()
	// Add and apply some node-crash faults so there is something to stop.
	for _, f := range nodeCrashFaults(2) {
		runner.AddFault(f)
	}
	if err := runner.ApplyAll(); err != nil {
		t.Fatalf("ApplyAll: %v", err)
	}

	sc := NewStopController(runner, 2000)
	log, result, runErr := sc.RunWithStop(s, 1)

	if runErr != nil {
		t.Fatalf("RunWithStop: %v", runErr)
	}

	// The scenario should complete (2 phases, no abort).
	if log.Aborted {
		t.Errorf("scenario aborted unexpectedly: %s", log.AbortReason)
	}
	if len(log.Phases) != 2 {
		t.Errorf("log.Phases len: got %d, want 2", len(log.Phases))
	}

	// EmergencyStop fired: 2 NodeCrash faults were active.
	if result.FaultsRecovered != 2 {
		t.Errorf("FaultsRecovered: got %d, want 2", result.FaultsRecovered)
	}

	// Sink-side: faults truly removed after RunWithStop.
	if got := runner.ActiveFaults(); len(got) != 0 {
		t.Errorf("post-RunWithStop ActiveFaults: got %d, want 0", len(got))
	}
}

// ---------------------------------------------------------------------------
// Test 5: RunWithStop rejects nil scenario.
// ---------------------------------------------------------------------------

func TestRunWithStopNilScenario(t *testing.T) {
	runner := chaos.NewChaosRunner()
	sc := NewStopController(runner, 100)
	_, _, err := sc.RunWithStop(nil, 0)
	if err == nil {
		t.Error("RunWithStop(nil): got nil error, want non-nil")
	}
}
