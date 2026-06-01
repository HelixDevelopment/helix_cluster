package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ─── Fake MeshJoiner ─────────────────────────────────────────────────────────

// recordingJoiner is a test double for MeshJoiner. It records the NodeConfig
// passed to Join so tests can assert the orchestrator actually called Join with
// the right data (sink-side evidence). It is NOT a product stub — the
// orchestrator always calls it; if Join is never called the test assertion on
// receivedCfg.NodeID fails.
type recordingJoiner struct {
	called      bool
	receivedCfg NodeConfig
	errToReturn error
}

func (r *recordingJoiner) Join(cfg NodeConfig) error {
	r.called = true
	r.receivedCfg = cfg
	return r.errToReturn
}

// ─── DetectHardware ──────────────────────────────────────────────────────────

// TestDetectHardwareRealValues proves DetectHardware returns CPUCores > 0 and
// MemoryMB > 0 on the actual host (M3 mac, or any GOOS). If DetectHardware
// fabricates values these assertions trivially pass — but the next two
// sub-assertions also check that Tier is one of the four known values, which
// would fail if deriveTier received 0 inputs (it would return "T1" from the
// default branch, but MemoryMB==0 is still caught by the MemoryMB>0 check).
//
// Bite: a broken platformMemoryMB that returns 0 causes MemoryMB>0 to fail.
// Bite: a broken runtime.NumCPU wrapper that returns 0 causes CPUCores>0 to fail.
func TestDetectHardwareRealValues(t *testing.T) {
	hw := DetectHardware()

	if hw.CPUCores <= 0 {
		t.Errorf("CPUCores = %d, want > 0", hw.CPUCores)
	}
	if hw.MemoryMB <= 0 {
		t.Errorf("MemoryMB = %d, want > 0", hw.MemoryMB)
	}
	validTiers := map[string]bool{"T1": true, "T2": true, "T3": true, "T4": true}
	if !validTiers[hw.Tier] {
		t.Errorf("Tier = %q, want one of T1-T4", hw.Tier)
	}
}

// ─── GenerateConfig + ToYAML round-trip ──────────────────────────────────────

// TestGenerateConfigYAMLRoundTrip proves GenerateConfig populates all required
// fields AND ToYAML serialises them so a YAML decoder can recover them. A
// broken generator that drops e.g. cluster_name causes the round-trip parse to
// produce an empty string and the comparison to fail.
//
// Bite: drop ClusterName in GenerateConfig → decoded ClusterName == "" != "my-cluster".
// Bite: drop NodeID in GenerateConfig → decoded NodeID == "" != "node-42".
// Bite: drop Tier in GenerateConfig → decoded Tier == "" != hw.Tier.
func TestGenerateConfigYAMLRoundTrip(t *testing.T) {
	hw := HardwareSummary{CPUCores: 8, MemoryMB: 16384, Tier: "T2"}
	cfg := GenerateConfig(hw, "my-cluster", "node-42")

	raw, err := cfg.ToYAML()
	if err != nil {
		t.Fatalf("ToYAML: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("ToYAML returned empty bytes")
	}

	// Parse back via yaml.v3 — same package used by ToYAML — to prove the bytes
	// are well-formed YAML that carries the expected field values.
	var decoded NodeConfig
	if err := yaml.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("yaml.Unmarshal: %v\nraw:\n%s", err, raw)
	}

	if decoded.NodeID != "node-42" {
		t.Errorf("decoded NodeID = %q, want %q; YAML:\n%s", decoded.NodeID, "node-42", raw)
	}
	if decoded.ClusterName != "my-cluster" {
		t.Errorf("decoded ClusterName = %q, want %q; YAML:\n%s", decoded.ClusterName, "my-cluster", raw)
	}
	if decoded.Tier != "T2" {
		t.Errorf("decoded Tier = %q, want %q; YAML:\n%s", decoded.Tier, "T2", raw)
	}
	// The raw bytes must also contain the literal node ID so a human reading the
	// file can identify the node — this is end-user-visible evidence.
	if !strings.Contains(string(raw), "node-42") {
		t.Errorf("YAML output does not contain nodeID string:\n%s", raw)
	}
}

// ─── Orchestrator.Run with fake joiner ───────────────────────────────────────

// TestOrchestratorRunSinkSide is the primary end-to-end test for Run(). It
// verifies three sink-side conditions without any network, root, or real
// hardware dependency:
//
//  1. The fake joiner was actually called (Join was invoked — not skipped).
//  2. The joiner received a NodeConfig with the expected NodeID and ClusterName.
//  3. The config file was written to disk (stat + content round-trip).
//  4. The lifecycle advanced to Ready.
//
// Bite: skip the joiner.Join call in Run → joiner.called==false, test fails.
// Bite: pass wrong NodeID to Join → receivedCfg.NodeID mismatch, test fails.
// Bite: skip os.WriteFile in Run → config file absent, Stat returns error.
// Bite: stop lifecycle at Joined → lifecycle.Current()=="Joined" != "Ready".
func TestOrchestratorRunSinkSide(t *testing.T) {
	dataDir := t.TempDir()
	joiner := &recordingJoiner{}

	orch := NewOrchestrator("test-cluster", "node-99", dataDir, joiner)

	if err := orch.Run(); err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	// 1. Joiner must have been called.
	if !joiner.called {
		t.Fatal("MeshJoiner.Join was never called — orchestrator did not reach mesh-join step")
	}

	// 2. Joiner received correct NodeConfig identity.
	if joiner.receivedCfg.NodeID != "node-99" {
		t.Errorf("joiner received NodeID %q, want %q", joiner.receivedCfg.NodeID, "node-99")
	}
	if joiner.receivedCfg.ClusterName != "test-cluster" {
		t.Errorf("joiner received ClusterName %q, want %q", joiner.receivedCfg.ClusterName, "test-cluster")
	}
	// Tier must be a known value — proves GenerateConfig ran correctly.
	validTiers := map[string]bool{"T1": true, "T2": true, "T3": true, "T4": true}
	if !validTiers[joiner.receivedCfg.Tier] {
		t.Errorf("joiner received Tier %q, not one of T1-T4", joiner.receivedCfg.Tier)
	}

	// 3. Config file was written and contains the correct node ID.
	cfgPath := orch.ConfigPath()
	info, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("config file not found at %s: %v", cfgPath, err)
	}
	if info.Size() == 0 {
		t.Fatal("config file is empty")
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("reading config file: %v", err)
	}
	var decoded NodeConfig
	if err := yaml.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("config file is not valid YAML: %v\ncontent:\n%s", err, raw)
	}
	if decoded.NodeID != "node-99" {
		t.Errorf("config file NodeID = %q, want %q", decoded.NodeID, "node-99")
	}
	if decoded.ClusterName != "test-cluster" {
		t.Errorf("config file ClusterName = %q, want %q", decoded.ClusterName, "test-cluster")
	}

	// 4. Lifecycle reached Ready.
	if got := orch.Lifecycle.Current(); got != StateReady {
		t.Errorf("lifecycle state = %q after Run, want %q", got, StateReady)
	}
}

// TestOrchestratorRunNilJoiner proves Run returns an error immediately when no
// joiner is configured (defensive check before any side effects).
func TestOrchestratorRunNilJoiner(t *testing.T) {
	orch := NewOrchestrator("c", "n", t.TempDir(), nil)
	orch.Joiner = nil // explicit nil — NewOrchestrator accepts nil gracefully
	if err := orch.Run(); err == nil {
		t.Fatal("Run() with nil joiner should return an error, got nil")
	}
}

// TestOrchestratorConfigPath proves ConfigPath derives from DataDir correctly
// so tests can find the file without hard-coding the filename.
func TestOrchestratorConfigPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	orch := &Orchestrator{DataDir: dir}
	got := orch.ConfigPath()
	want := filepath.Join(dir, "node-config.yaml")
	if got != want {
		t.Errorf("ConfigPath = %q, want %q", got, want)
	}
}

// ─── Lifecycle State Machine ─────────────────────────────────────────────────

// TestLifecycleLegalPath proves the full Idle→Provisioning→…→Ready legal
// sequence succeeds. Each Advance must return nil and Current must update.
//
// Bite: remove any intermediate state from allowedTransitions → Advance errors.
func TestLifecycleLegalPath(t *testing.T) {
	lc := NewLifecycle()
	if lc.Current() != StateIdle {
		t.Fatalf("initial state = %q, want Idle", lc.Current())
	}

	path := []State{
		StateProvisioning,
		StateConfigured,
		StateJoined,
		StateReady,
		StateDraining,
		StateStopped,
	}
	for _, s := range path {
		if err := lc.Advance(s); err != nil {
			t.Fatalf("Advance(%s) returned error: %v", s, err)
		}
		if lc.Current() != s {
			t.Fatalf("after Advance(%s): current = %q", s, lc.Current())
		}
	}

	// History must have one entry per transition.
	if len(lc.History) != len(path) {
		t.Errorf("History has %d entries, want %d", len(lc.History), len(path))
	}
}

// TestLifecycleIllegalTransitionRejected proves that an illegal transition
// (e.g. Provisioning → Stopped, skipping all intermediate states) returns a
// non-nil error AND leaves the current state unchanged.
//
// This is the primary anti-bluff bite for the lifecycle:
//   - If Advance always returns nil, the error assertion fails.
//   - If Advance returns an error but silently advances the state, the
//     Current()==StateProvisioning assertion fails.
func TestLifecycleIllegalTransitionRejected(t *testing.T) {
	lc := NewLifecycle()

	// Move to Provisioning (legal).
	if err := lc.Advance(StateProvisioning); err != nil {
		t.Fatalf("Advance(Provisioning): %v", err)
	}

	// Attempt to jump directly to Stopped (illegal from Provisioning).
	err := lc.Advance(StateStopped)
	if err == nil {
		t.Fatal("Advance(Provisioning → Stopped) should return an error, got nil")
	}

	// State must remain Provisioning — the failed Advance must not mutate state.
	if got := lc.Current(); got != StateProvisioning {
		t.Errorf("after illegal transition: current = %q, want Provisioning", got)
	}

	// History must have only the one legal transition.
	if len(lc.History) != 1 {
		t.Errorf("History = %v, want exactly 1 entry (the legal transition)", lc.History)
	}
}

// TestLifecycleIllegalFromReady proves another illegal jump (Ready → Provisioning)
// is also rejected. Ensures the guard is not special-cased for Stopped only.
func TestLifecycleIllegalFromReady(t *testing.T) {
	lc := NewLifecycle()
	legal := []State{StateProvisioning, StateConfigured, StateJoined, StateReady}
	for _, s := range legal {
		if err := lc.Advance(s); err != nil {
			t.Fatalf("setup Advance(%s): %v", s, err)
		}
	}

	// Attempting to go backwards is illegal.
	if err := lc.Advance(StateProvisioning); err == nil {
		t.Fatal("Advance(Ready → Provisioning) should return an error, got nil")
	}
	if lc.Current() != StateReady {
		t.Errorf("current = %q after illegal transition, want Ready", lc.Current())
	}
}

// TestLifecycleTerminalStateStopped proves that once the lifecycle reaches
// Stopped (the terminal state), no further transitions are possible.
func TestLifecycleTerminalStateStopped(t *testing.T) {
	lc := NewLifecycle()
	path := []State{StateProvisioning, StateConfigured, StateJoined, StateReady, StateDraining, StateStopped}
	for _, s := range path {
		if err := lc.Advance(s); err != nil {
			t.Fatalf("Advance(%s): %v", s, err)
		}
	}

	// Any further advance from Stopped must error.
	if err := lc.Advance(StateIdle); err == nil {
		t.Fatal("Advance from Stopped should error, got nil")
	}
	if lc.Current() != StateStopped {
		t.Errorf("current = %q after rejected transition, want Stopped", lc.Current())
	}
}

// ─── deriveTier ───────────────────────────────────────────────────────────────

// TestDeriveTierThresholds verifies that all four tier boundaries work.
// Bite: change a threshold constant in deriveTier → a case below fails.
func TestDeriveTierThresholds(t *testing.T) {
	cases := []struct {
		cores, memMB int
		want         string
	}{
		// T1 edge: just below T2
		{cores: 4, memMB: 8 * 1024, want: "T1"},
		// T2 edge: exactly 8 cores
		{cores: 8, memMB: 4 * 1024, want: "T2"},
		// T2 via memory: exactly 16 GiB
		{cores: 2, memMB: 16 * 1024, want: "T2"},
		// T3 edge: exactly 16 cores
		{cores: 16, memMB: 4 * 1024, want: "T3"},
		// T3 via memory: exactly 32 GiB
		{cores: 2, memMB: 32 * 1024, want: "T3"},
		// T4 edge: exactly 32 cores
		{cores: 32, memMB: 4 * 1024, want: "T4"},
		// T4 via memory: exactly 64 GiB
		{cores: 2, memMB: 64 * 1024, want: "T4"},
	}
	for _, tc := range cases {
		got := deriveTier(tc.cores, tc.memMB)
		if got != tc.want {
			t.Errorf("deriveTier(%d cores, %d MB) = %q, want %q", tc.cores, tc.memMB, got, tc.want)
		}
	}
}
