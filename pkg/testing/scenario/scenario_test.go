package scenario

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Test 1: Phase ordering + fault recording
// ---------------------------------------------------------------------------

func TestPhaseOrderingAndFaultRecording(t *testing.T) {
	yamlDoc := `
name: ordering-test
max_affected: 100
phases:
  - name: alpha
    faults: ["net-partition", "packet-loss"]
    duration_ms: 100
    max_faults: 10
  - name: beta
    faults: ["node-crash"]
    duration_ms: 200
    max_faults: 10
  - name: gamma
    faults: ["clock-skew", "disk-full"]
    duration_ms: 50
    max_faults: 10
`
	s, err := ParseScenarioYAML([]byte(yamlDoc))
	if err != nil {
		t.Fatalf("ParseScenarioYAML: %v", err)
	}

	log := s.Run()

	if log.Aborted {
		t.Errorf("expected not aborted, got Aborted=true, reason=%q", log.AbortReason)
	}
	if len(log.Phases) != 3 {
		t.Fatalf("expected 3 phases, got %d", len(log.Phases))
	}

	// Phase names must appear in declaration order.
	expectedNames := []string{"alpha", "beta", "gamma"}
	for i, want := range expectedNames {
		if log.Phases[i].Name != want {
			t.Errorf("phase[%d] name: got %q, want %q", i, log.Phases[i].Name, want)
		}
	}

	// Fault names must match exactly what the scenario specifies.
	assertFaults(t, "alpha", log.Phases[0].FaultsInjected, "net-partition", "packet-loss")
	assertFaults(t, "beta", log.Phases[1].FaultsInjected, "node-crash")
	assertFaults(t, "gamma", log.Phases[2].FaultsInjected, "clock-skew", "disk-full")

	// Total fault count must reflect cumulative injections.
	if log.TotalFaults != 5 {
		t.Errorf("TotalFaults: got %d, want 5", log.TotalFaults)
	}
}

// ---------------------------------------------------------------------------
// Test 2: Blast-radius clamping — per-phase and global
// ---------------------------------------------------------------------------

func TestBlastRadiusPerPhase(t *testing.T) {
	// Phase lists 4 faults but MaxFaults=2 — only 2 should be injected and
	// BlastRadiusExceeded must be true.  Removing the MaxFaults check in
	// engine.go would inject all 4 and BlastRadiusExceeded would stay false,
	// making this assertion fail.
	yamlDoc := `
name: blast-radius-phase
max_affected: 100
phases:
  - name: restricted
    faults: ["a", "b", "c", "d"]
    duration_ms: 100
    max_faults: 2
`
	s, err := ParseScenarioYAML([]byte(yamlDoc))
	if err != nil {
		t.Fatalf("ParseScenarioYAML: %v", err)
	}

	log := s.Run()

	if len(log.Phases) != 1 {
		t.Fatalf("expected 1 phase, got %d", len(log.Phases))
	}
	pr := log.Phases[0]

	// Anti-bluff bite: if the blast-radius check is removed, all 4 faults are
	// injected and BlastRadiusExceeded stays false — this assertion fails.
	if !pr.BlastRadiusExceeded {
		t.Errorf("BlastRadiusExceeded: got false, want true")
	}
	if len(pr.FaultsInjected) != 2 {
		t.Errorf("FaultsInjected count: got %d, want 2", len(pr.FaultsInjected))
	}
	assertFaults(t, "restricted", pr.FaultsInjected, "a", "b")
}

func TestBlastRadiusGlobal(t *testing.T) {
	// Global MaxAffected=3; two phases each listing 3 faults.
	// Phase 1 injects 3 (exhausts budget); phase 2 gets 0 and sets flag.
	// Removing the global budget check would inject 3 in phase 2 as well
	// and BlastRadiusExceeded would be false for phase 2 — bite fails.
	yamlDoc := `
name: blast-radius-global
max_affected: 3
phases:
  - name: phase-one
    faults: ["f1", "f2", "f3"]
    duration_ms: 50
    max_faults: 5
  - name: phase-two
    faults: ["g1", "g2", "g3"]
    duration_ms: 50
    max_faults: 5
`
	s, err := ParseScenarioYAML([]byte(yamlDoc))
	if err != nil {
		t.Fatalf("ParseScenarioYAML: %v", err)
	}

	log := s.Run()

	if len(log.Phases) != 2 {
		t.Fatalf("expected 2 phases, got %d", len(log.Phases))
	}

	p1 := log.Phases[0]
	p2 := log.Phases[1]

	if p1.BlastRadiusExceeded {
		t.Errorf("phase-one should not exceed blast radius")
	}
	if len(p1.FaultsInjected) != 3 {
		t.Errorf("phase-one FaultsInjected: got %d, want 3", len(p1.FaultsInjected))
	}

	// Anti-bluff bite: removing global budget clamping makes p2 inject 3 faults
	// and BlastRadiusExceeded stays false — this fails.
	if !p2.BlastRadiusExceeded {
		t.Errorf("phase-two BlastRadiusExceeded: got false, want true (global budget exhausted)")
	}
	if len(p2.FaultsInjected) != 0 {
		t.Errorf("phase-two FaultsInjected: got %d, want 0 (global budget exhausted)", len(p2.FaultsInjected))
	}

	if log.TotalFaults != 3 {
		t.Errorf("TotalFaults: got %d, want 3", log.TotalFaults)
	}
}

// ---------------------------------------------------------------------------
// Test 3: Abort-on condition — early stop
// ---------------------------------------------------------------------------

func TestAbortOnEarlyStop(t *testing.T) {
	// Three phases; AbortOn fires when faults_injected > 2.
	// Phase 1 injects 2 faults -> no abort.
	// Phase 2 injects 1 fault (total = 3) -> abort fires.
	// Phase 3 must NOT appear in the log.
	//
	// Anti-bluff bite: removing the abort evaluation makes all phases run,
	// len(log.Phases) == 3, and log.Aborted stays false — both assertions fail.
	yamlDoc := `
name: abort-test
max_affected: 100
abort_on:
  field: faults_injected
  op: ">"
  value: 2
phases:
  - name: phase-one
    faults: ["net-partition", "packet-loss"]
    duration_ms: 100
    max_faults: 10
  - name: phase-two
    faults: ["node-crash"]
    duration_ms: 100
    max_faults: 10
  - name: phase-three
    faults: ["clock-skew"]
    duration_ms: 100
    max_faults: 10
`
	s, err := ParseScenarioYAML([]byte(yamlDoc))
	if err != nil {
		t.Fatalf("ParseScenarioYAML: %v", err)
	}

	log := s.Run()

	// Anti-bluff: if abort evaluation is removed all 3 phases run.
	if !log.Aborted {
		t.Errorf("expected Aborted=true, got false")
	}
	if log.AbortReason == "" {
		t.Errorf("AbortReason must not be empty when aborted")
	}
	if !strings.Contains(log.AbortReason, "faults_injected") {
		t.Errorf("AbortReason should mention faults_injected, got %q", log.AbortReason)
	}

	// Phase-three must NOT be in the execution log — abort happened in phase-two.
	if len(log.Phases) != 2 {
		t.Errorf("expected 2 phases in log (phase-three must not run), got %d", len(log.Phases))
	}

	// The phase that triggered the abort should have Aborted=true.
	triggerPhase := log.Phases[1]
	if !triggerPhase.Aborted {
		t.Errorf("phase-two.Aborted: got false, want true")
	}

	if log.TotalFaults != 3 {
		t.Errorf("TotalFaults: got %d, want 3", log.TotalFaults)
	}
}

// ---------------------------------------------------------------------------
// Test 4: ParseScenarioYAML validation rejects invalid input
// ---------------------------------------------------------------------------

func TestParseScenarioYAMLValidation(t *testing.T) {
	cases := []struct {
		desc    string
		yaml    string
		wantErr string
	}{
		{
			desc: "empty name",
			yaml: `
name: ""
max_affected: 5
phases:
  - name: p1
    faults: ["f"]
    duration_ms: 10
    max_faults: 1
`,
			wantErr: "name",
		},
		{
			desc: "zero phases",
			yaml: `
name: my-scenario
max_affected: 5
phases: []
`,
			wantErr: "phase",
		},
		{
			desc: "zero max_affected",
			yaml: `
name: my-scenario
max_affected: 0
phases:
  - name: p1
    faults: ["f"]
    duration_ms: 10
    max_faults: 1
`,
			wantErr: "max_affected",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.desc, func(t *testing.T) {
			_, err := ParseScenarioYAML([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q should contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

func assertFaults(t *testing.T, phase string, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("phase %q: FaultsInjected len=%d, want %d (%v vs %v)", phase, len(got), len(want), got, want)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("phase %q: fault[%d] = %q, want %q", phase, i, got[i], want[i])
		}
	}
}
