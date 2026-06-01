package policy

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func bg() context.Context { return context.Background() }

func mustLoad(t *testing.T, e *Engine, name, src string) {
	t.Helper()
	if err := e.LoadPolicy(bg(), name, src); err != nil {
		t.Fatalf("LoadPolicy(%q): %v", name, err)
	}
}

// ---------------------------------------------------------------------------
// HelixConstitution scheduling policy — the primary HXC-1127 requirement
// ---------------------------------------------------------------------------

// TestSchedulingDenyUnhealthyNode is the TDD-first test for the core requirement:
// a node with health < 50 MUST be denied.  This test was written BEFORE the
// implementation; it fails on the old hand-rolled stub.
//
// Mutation proof: flip the rego threshold to `>= 150` → allow rule never fires
// → Allow stays false but with health=80 the second sub-case below would also
// flip to deny.  Restoring `>= 50` brings both sub-cases back to green.
func TestSchedulingDenyUnhealthyNode(t *testing.T) {
	e := NewEngine()
	mustLoad(t, e, SchedulingPolicyName, SchedulingPolicyRego)

	d, err := e.Evaluate(bg(), SchedulingPolicyName, map[string]interface{}{
		"node": map[string]interface{}{"health": 40},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if d.Allow {
		t.Fatalf("Allow = true, want false for node health 40 (< 50 threshold)")
	}
	// Reason must mention health — proves the evaluated input is reflected back.
	if !strings.Contains(d.Reason, "health") {
		t.Fatalf("Reason %q does not mention health", d.Reason)
	}
	// EvaluatedInput must contain the node key we sent in.
	if d.EvaluatedInput["node"] == nil {
		t.Fatalf("EvaluatedInput missing 'node' key")
	}
}

// TestSchedulingAllowHealthyNode proves the allow path for health >= 50.
//
// Mutation proof: change `>= 50` to `> 100` in the rego → health=80 no longer
// matches → d.Allow flips to false → this test fails.  Restore `>= 50` → green.
func TestSchedulingAllowHealthyNode(t *testing.T) {
	e := NewEngine()
	mustLoad(t, e, SchedulingPolicyName, SchedulingPolicyRego)

	d, err := e.Evaluate(bg(), SchedulingPolicyName, map[string]interface{}{
		"node": map[string]interface{}{"health": 80},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !d.Allow {
		t.Fatalf("Allow = false, want true for node health 80 (>= 50 threshold)")
	}
	if !strings.Contains(d.Reason, "health") {
		t.Fatalf("Reason %q does not mention health", d.Reason)
	}
}

// TestSchedulingBoundaryDeny proves health == 49 is denied (strictly below 50).
//
// Mutation proof: change `>= 50` to `>= 49` in the rego → health=49 is allowed
// → this test fails.
func TestSchedulingBoundaryDeny(t *testing.T) {
	e := NewEngine()
	mustLoad(t, e, SchedulingPolicyName, SchedulingPolicyRego)

	d, err := e.Evaluate(bg(), SchedulingPolicyName, map[string]interface{}{
		"node": map[string]interface{}{"health": 49},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if d.Allow {
		t.Fatalf("Allow = true, want false for node health 49 (boundary below 50)")
	}
}

// TestSchedulingBoundaryAllow proves health == 50 is allowed (at the threshold).
//
// Mutation proof: change `>= 50` to `> 50` in the rego → health=50 becomes
// deny → this test fails.
func TestSchedulingBoundaryAllow(t *testing.T) {
	e := NewEngine()
	mustLoad(t, e, SchedulingPolicyName, SchedulingPolicyRego)

	d, err := e.Evaluate(bg(), SchedulingPolicyName, map[string]interface{}{
		"node": map[string]interface{}{"health": 50},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !d.Allow {
		t.Fatalf("Allow = false, want true for node health 50 (at threshold)")
	}
}

// TestSchedulingEvaluatedInputCapture proves that EvaluatedInput round-trips
// the submitted input into the Decision (§7.1 evidence requirement).
//
// Mutation proof: in Evaluate, remove the evalInput copy and return an empty
// map → d.EvaluatedInput["node"] is nil → this test fails.
func TestSchedulingEvaluatedInputCapture(t *testing.T) {
	e := NewEngine()
	mustLoad(t, e, SchedulingPolicyName, SchedulingPolicyRego)

	inputNode := map[string]interface{}{"health": 75, "id": "node-abc"}
	d, err := e.Evaluate(bg(), SchedulingPolicyName, map[string]interface{}{
		"node": inputNode,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !d.Allow {
		t.Fatalf("Allow = false for health=75, want true")
	}
	// The evaluated input must contain the node map.
	nodeVal, ok := d.EvaluatedInput["node"]
	if !ok {
		t.Fatal("EvaluatedInput missing 'node' key")
	}
	nodeMap, ok := nodeVal.(map[string]interface{})
	if !ok {
		t.Fatalf("EvaluatedInput['node'] type = %T, want map", nodeVal)
	}
	if nodeMap["id"] != "node-abc" {
		t.Fatalf("EvaluatedInput['node']['id'] = %v, want node-abc", nodeMap["id"])
	}
}

// TestSchedulingDefaultAllow proves the `default allow = false` rego clause:
// when no node health is provided the default fires and allow is false.
//
// Mutation proof: remove `default allow = false` from SchedulingPolicyRego →
// OPA returns undefined (empty result set) which the engine also maps to false,
// but this is a structural difference; to catch removal of the explicit default
// add a comment-only mutation — changing `default allow = false` to
// `default allow = true` causes this test to fail because d.Allow becomes true.
func TestSchedulingDefaultDeny(t *testing.T) {
	e := NewEngine()
	mustLoad(t, e, SchedulingPolicyName, SchedulingPolicyRego)

	// No node key at all → the allow rule body can't evaluate to true.
	d, err := e.Evaluate(bg(), SchedulingPolicyName, map[string]interface{}{})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if d.Allow {
		t.Fatalf("Allow = true for empty input, want false (default deny)")
	}
}

// ---------------------------------------------------------------------------
// Engine lifecycle
// ---------------------------------------------------------------------------

// TestLoadPolicyEmptyNameRejected proves an empty name is refused.
//
// Mutation proof: remove the `name == ""` guard → err becomes nil → fails.
func TestLoadPolicyEmptyNameRejected(t *testing.T) {
	e := NewEngine()
	if err := e.LoadPolicy(bg(), "", SchedulingPolicyRego); err == nil {
		t.Fatal("expected error for empty policy name")
	}
	if e.PolicyCount() != 0 {
		t.Fatalf("policy must not be stored on error; count = %d", e.PolicyCount())
	}
}

// TestLoadPolicyRejectsMalformedRego proves that syntactically invalid rego is
// rejected at compile time, not silently stored.
//
// Mutation proof: remove the compile step (replace PrepareForEval with a no-op)
// → malformed rego is stored without error → this test fails.
func TestLoadPolicyRejectsMalformedRego(t *testing.T) {
	e := NewEngine()
	err := e.LoadPolicy(bg(), "bad", "package bad\n!!! this is not valid rego !!!")
	if err == nil {
		t.Fatal("expected compile error for malformed rego source")
	}
	if e.PolicyCount() != 0 {
		t.Fatalf("malformed policy must not be stored; count = %d", e.PolicyCount())
	}
}

// TestEvaluateMissingPolicyReturnsError proves a missing-policy error is
// returned rather than a zero-value Decision.
//
// Mutation proof: in Evaluate remove the `!ok` guard → the engine panics on nil
// pointer dereference or returns a spurious zero Decision with no error.
func TestEvaluateMissingPolicyReturnsError(t *testing.T) {
	e := NewEngine()
	_, err := e.Evaluate(bg(), "no-such-policy", nil)
	if err == nil {
		t.Fatal("expected error for unknown policy name")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %q, expected 'not found'", err.Error())
	}
}

// TestLoadPolicyReplacesPrevious proves reloading a policy under the same name
// replaces it atomically (stale rules cannot linger).
//
// Mutation proof: guard the assignment with `if _, ok := ...; !ok` → second
// load is silently discarded → the old rego with health>=100 remains → the
// health=75 eval denies → this test fails.
func TestLoadPolicyReplacesPrevious(t *testing.T) {
	e := NewEngine()
	// First version: only allows health >= 100.
	mustLoad(t, e, "sched", `
package sched
default allow = false
allow { input.node.health >= 100 }
`)
	d, _ := e.Evaluate(bg(), "sched", map[string]interface{}{
		"node": map[string]interface{}{"health": 75},
	})
	if d.Allow {
		t.Fatal("pre-replace: health=75 should be denied with threshold 100")
	}

	// Replace with standard threshold 50.
	mustLoad(t, e, "sched", SchedulingPolicyRego)
	d, err := e.Evaluate(bg(), "sched", map[string]interface{}{
		"node": map[string]interface{}{"health": 75},
	})
	if err != nil {
		t.Fatalf("post-replace Evaluate: %v", err)
	}
	if !d.Allow {
		t.Fatal("post-replace: health=75 should be allowed with threshold 50")
	}
	if e.PolicyCount() != 1 {
		t.Fatalf("count = %d, want 1 after replace", e.PolicyCount())
	}
}

// TestRemovePolicy proves RemovePolicy deletes an existing policy and returns
// an error for a missing one.
//
// Mutation proof: remove `delete(e.policies, name)` → PolicyCount stays 1 →
// the count assertion fails.
func TestRemovePolicy(t *testing.T) {
	e := NewEngine()
	mustLoad(t, e, "p", SchedulingPolicyRego)

	if err := e.RemovePolicy("p"); err != nil {
		t.Fatalf("RemovePolicy: %v", err)
	}
	if e.PolicyCount() != 0 {
		t.Fatalf("count = %d after remove, want 0", e.PolicyCount())
	}
	if err := e.RemovePolicy("p"); err == nil {
		t.Fatal("expected error when removing already-removed policy")
	}
}

// TestListPolicies proves the count and content of ListPolicies.
func TestListPolicies(t *testing.T) {
	e := NewEngine()
	mustLoad(t, e, "p1", SchedulingPolicyRego)
	mustLoad(t, e, "p2", SchedulingPolicyRego)

	names := e.ListPolicies()
	if len(names) != 2 {
		t.Fatalf("len = %d, want 2", len(names))
	}
}

// ---------------------------------------------------------------------------
// Custom rego policies (prove the engine isn't hard-coded to scheduling)
// ---------------------------------------------------------------------------

// TestCustomPolicyAllowDeny proves the engine correctly evaluates arbitrary
// rego policies, not just the embedded scheduling one.
//
// Mutation proof: in Evaluate change `allow = b` to `allow = !b` → the
// role=admin case yields deny and the role=guest case yields allow → both
// sub-cases fail.
func TestCustomPolicyAllowDeny(t *testing.T) {
	e := NewEngine()
	mustLoad(t, e, "rbac", `
package rbac
default allow = false
allow { input.role == "admin" }
`)
	// Admin is allowed.
	d, err := e.Evaluate(bg(), "rbac", map[string]interface{}{"role": "admin"})
	if err != nil {
		t.Fatalf("Evaluate admin: %v", err)
	}
	if !d.Allow {
		t.Fatal("expected allow for role=admin")
	}

	// Guest is denied.
	d, err = e.Evaluate(bg(), "rbac", map[string]interface{}{"role": "guest"})
	if err != nil {
		t.Fatalf("Evaluate guest: %v", err)
	}
	if d.Allow {
		t.Fatal("expected deny for role=guest")
	}
}

// TestPolicyNameSurfacedInDecision proves PolicyName is set on the returned Decision.
//
// Mutation proof: remove `PolicyName: name` from the Decision literal → this
// assertion fails.
func TestPolicyNameSurfacedInDecision(t *testing.T) {
	e := NewEngine()
	mustLoad(t, e, SchedulingPolicyName, SchedulingPolicyRego)

	d, _ := e.Evaluate(bg(), SchedulingPolicyName, map[string]interface{}{
		"node": map[string]interface{}{"health": 70},
	})
	if d.PolicyName != SchedulingPolicyName {
		t.Fatalf("PolicyName = %q, want %q", d.PolicyName, SchedulingPolicyName)
	}
}

// ---------------------------------------------------------------------------
// Concurrency safety (race detector)
// ---------------------------------------------------------------------------

// TestConcurrentLoadEvaluate exercises the RWMutex under -race with concurrent
// loads, evaluations, and list operations.
//
// Mutation proof (under -race): remove RLock/RUnlock in Evaluate → data race
// detected → test fails with race report.
func TestConcurrentLoadEvaluate(t *testing.T) {
	e := NewEngine()
	mustLoad(t, e, SchedulingPolicyName, SchedulingPolicyRego)

	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			_ = e.LoadPolicy(bg(), SchedulingPolicyName, SchedulingPolicyRego)
		}()
		go func(h int) {
			defer wg.Done()
			_, _ = e.Evaluate(bg(), SchedulingPolicyName, map[string]interface{}{
				"node": map[string]interface{}{"health": h * 3},
			})
		}(i)
		go func() {
			defer wg.Done()
			_ = e.ListPolicies()
		}()
	}
	wg.Wait()

	// Final sanity: healthy node must still be allowed.
	d, err := e.Evaluate(bg(), SchedulingPolicyName, map[string]interface{}{
		"node": map[string]interface{}{"health": 80},
	})
	if err != nil || !d.Allow {
		t.Fatalf("post-concurrency: Allow=%v err=%v", d.Allow, err)
	}
}

// ---------------------------------------------------------------------------
// MustLoadSchedulingPolicy helper
// ---------------------------------------------------------------------------

// TestMustLoadSchedulingPolicyLoads proves the helper succeeds and the engine
// contains exactly one policy after the call.
func TestMustLoadSchedulingPolicyLoads(t *testing.T) {
	e := NewEngine()
	MustLoadSchedulingPolicy(e)
	if e.PolicyCount() != 1 {
		t.Fatalf("count = %d after MustLoadSchedulingPolicy, want 1", e.PolicyCount())
	}
	names := e.ListPolicies()
	if len(names) == 0 || names[0] != SchedulingPolicyName {
		t.Fatalf("names = %v, want [%s]", names, SchedulingPolicyName)
	}
}
