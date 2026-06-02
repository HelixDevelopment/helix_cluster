package modelrouter

import (
	"encoding/json"
	"fmt"
	"testing"
)

// ----------------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------------

// decodeBody unmarshals raw JSON produced by BuildRequestBody into a
// RequestBody so tests can inspect real sink-side values.
func decodeBody(t *testing.T, raw []byte) RequestBody {
	t.Helper()
	var rb RequestBody
	if err := json.Unmarshal(raw, &rb); err != nil {
		t.Fatalf("decodeBody: json.Unmarshal failed: %v (raw=%s)", err, raw)
	}
	return rb
}

// ----------------------------------------------------------------------------
// TestBuildRequestBody_AllStrategies
//
// MUTATION GUARD: swapping any two entries in the defaultModels table will
// cause this test to fail because the decoded "model" field will no longer
// equal the expected constant for that strategy.
// ----------------------------------------------------------------------------

func TestBuildRequestBody_AllStrategies(t *testing.T) {
	router := New()

	// expectedConcretes must stay in sync with defaultModels.
	// Swapping e.g. latency↔cost in defaultModels makes the latency case (and
	// the cost case) fail: decoded.Model == "helix-nano-v1" ≠ "helix-fast-v2".
	expectedConcretes := map[Strategy]string{
		StrategyLatency:    "helix-fast-v2",
		StrategyThroughput: "helix-bulk-v3",
		StrategyQuality:    "helix-precise-v4",
		StrategyCost:       "helix-nano-v1",
	}

	strategies := []Strategy{
		StrategyLatency,
		StrategyThroughput,
		StrategyQuality,
		StrategyCost,
	}

	for _, strat := range strategies {
		strat := strat // capture
		t.Run(string(strat), func(t *testing.T) {
			runID := fmt.Sprintf("run-%s-001", strat)
			want := expectedConcretes[strat]

			// --- sink-side: call BuildRequestBody with sentinel "default" ---
			raw, err := router.BuildRequestBody(strat, SentinelDefault, runID)
			if err != nil {
				t.Fatalf("[%s] BuildRequestBody returned unexpected error: %v", strat, err)
			}
			decoded := decodeBody(t, raw)

			// Before: the input was the sentinel literal.
			before := SentinelDefault

			// After: the JSON body must contain the concrete strategy model id.
			after := decoded.Model

			// (1) model field must equal the expected concrete id
			if after != want {
				t.Errorf("[%s] model field: got %q, want %q (before=%q)",
					strat, after, want, before)
			}

			// (2) model field must differ from the sentinel literal
			if after == SentinelDefault {
				t.Errorf("[%s] model field must differ from sentinel %q but got %q",
					strat, SentinelDefault, after)
			}

			// (3) runID must round-trip verbatim (sink-side correlation evidence)
			if decoded.RunID != runID {
				t.Errorf("[%s] run_id field: got %q, want %q", strat, decoded.RunID, runID)
			}

			t.Logf("[%s] before=%q after=%q run_id=%q (raw=%s)",
				strat, before, after, decoded.RunID, raw)
		})
	}
}

// TestBuildRequestBody_PassThrough verifies that a non-sentinel model name is
// returned unchanged in the JSON body.
func TestBuildRequestBody_PassThrough(t *testing.T) {
	router := New()
	const explicitModel = "gpt-explicit-custom"
	const runID = "run-passthru-007"

	raw, err := router.BuildRequestBody(StrategyLatency, explicitModel, runID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	decoded := decodeBody(t, raw)

	if decoded.Model != explicitModel {
		t.Errorf("model: got %q, want %q", decoded.Model, explicitModel)
	}
	if decoded.RunID != runID {
		t.Errorf("run_id: got %q, want %q", decoded.RunID, runID)
	}
	t.Logf("pass-through model=%q run_id=%q (raw=%s)", decoded.Model, decoded.RunID, raw)
}

// TestResolve_UnknownStrategy ensures Resolve returns a typed ErrUnknownStrategy
// for a strategy not in the table, and not a nil error or a generic error.
func TestResolve_UnknownStrategy(t *testing.T) {
	router := New()

	_, err := router.Resolve("nonexistent", SentinelDefault)
	if err == nil {
		t.Fatal("expected an error for unknown strategy but got nil")
	}
	if !IsUnknownStrategy(err) {
		t.Errorf("error is not ErrUnknownStrategy: %v (%T)", err, err)
	}
	t.Logf("got expected error: %v", err)
}

// TestBuildRequestBody_UnknownStrategy ensures the error propagates through
// BuildRequestBody as well.
func TestBuildRequestBody_UnknownStrategy(t *testing.T) {
	router := New()

	_, err := router.BuildRequestBody("ultrafast", SentinelDefault, "run-x")
	if err == nil {
		t.Fatal("expected an error for unknown strategy but got nil")
	}
	if !IsUnknownStrategy(err) {
		t.Errorf("error type wrong: %v (%T)", err, err)
	}
}

// TestDefaultModelsDistinct asserts the four concrete ids are all different.
// This pins the requirement that a swapped mapping is detectable.
func TestDefaultModelsDistinct(t *testing.T) {
	seen := make(map[string]Strategy)
	for strat, id := range defaultModels {
		if prev, dup := seen[id]; dup {
			t.Errorf("strategies %q and %q share the same model id %q — ids must be distinct",
				prev, strat, id)
		}
		seen[id] = strat
	}
	if len(defaultModels) != 4 {
		t.Errorf("expected 4 strategies, got %d", len(defaultModels))
	}
}

// TestStrategyDefault_KnownStrategies exercises the StrategyDefault accessor
// for all four strategies and verifies results match BuildRequestBody output.
func TestStrategyDefault_KnownStrategies(t *testing.T) {
	router := New()
	strategies := []Strategy{
		StrategyLatency,
		StrategyThroughput,
		StrategyQuality,
		StrategyCost,
	}
	for _, strat := range strategies {
		strat := strat
		t.Run(string(strat), func(t *testing.T) {
			runID := fmt.Sprintf("run-accessor-%s", strat)
			direct, err := router.StrategyDefault(strat)
			if err != nil {
				t.Fatalf("StrategyDefault(%q) error: %v", strat, err)
			}
			// Must not be empty and must not be the sentinel.
			if direct == "" {
				t.Errorf("StrategyDefault(%q) returned empty string", strat)
			}
			if direct == SentinelDefault {
				t.Errorf("StrategyDefault(%q) returned sentinel %q", strat, SentinelDefault)
			}

			// Cross-check against BuildRequestBody.
			raw, err := router.BuildRequestBody(strat, SentinelDefault, runID)
			if err != nil {
				t.Fatalf("BuildRequestBody: %v", err)
			}
			decoded := decodeBody(t, raw)
			if decoded.Model != direct {
				t.Errorf("mismatch: StrategyDefault=%q BuildRequestBody.model=%q",
					direct, decoded.Model)
			}
			t.Logf("[%s] concrete=%q run_id=%q", strat, direct, decoded.RunID)
		})
	}
}

// TestStrategyDefault_Unknown exercises the ErrUnknownStrategy path via the
// accessor.
func TestStrategyDefault_Unknown(t *testing.T) {
	router := New()
	_, err := router.StrategyDefault("bogus")
	if err == nil {
		t.Fatal("expected error for unknown strategy")
	}
	if !IsUnknownStrategy(err) {
		t.Errorf("wrong error type: %T %v", err, err)
	}
}

// TestResolve_SentinelVsPassThrough checks Resolve for both branches:
// sentinel → concrete, non-sentinel → unchanged.
func TestResolve_SentinelVsPassThrough(t *testing.T) {
	router := New()

	concrete, err := router.Resolve(StrategyQuality, SentinelDefault)
	if err != nil {
		t.Fatalf("Resolve quality/default: %v", err)
	}
	if concrete == SentinelDefault {
		t.Errorf("sentinel not resolved: still %q", concrete)
	}
	t.Logf("quality sentinel→%q", concrete)

	const custom = "my-finetuned-model"
	passthru, err := router.Resolve(StrategyQuality, custom)
	if err != nil {
		t.Fatalf("Resolve quality/custom: %v", err)
	}
	if passthru != custom {
		t.Errorf("pass-through: got %q, want %q", passthru, custom)
	}
	t.Logf("quality pass-through→%q", passthru)
}
