//go:build integration

package policy

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestIntegrationSchedulingPolicyFullRoundTrip exercises the real OPA rego
// engine end-to-end with a per-run UUID embedded in the input, verifying:
//
//  1. An unhealthy node (health=30) is DENIED and the UUID appears in the
//     evaluated input captured on the Decision.
//  2. A healthy node (health=70) is ALLOWED and the UUID similarly round-trips.
//
// This test MUST hard-FAIL if either decision is wrong — it is not a smoke
// test.  Because OPA rego evaluation is deterministic and in-process, there is
// no external service to skip against; the test always runs.
//
// §1.1 mutation scenario (documented, not executed here):
//   - Flip `>= 50` to `>= 150` in SchedulingPolicyRego → health=70 is denied
//     → the allow assertion fails.
//   - Flip `default allow = false` to `default allow = true` → health=30 is
//     allowed → the deny assertion fails.
func TestIntegrationSchedulingPolicyFullRoundTrip(t *testing.T) {
	runID := uuid.New().String()
	ctx := context.Background()

	e := NewEngine()
	MustLoadSchedulingPolicy(e)

	type tc struct {
		desc        string
		health      int
		wantAllow   bool
		wantHealthy string
	}
	cases := []tc{
		{"unhealthy node health=30", 30, false, "30"},
		{"healthy node health=70", 70, true, "70"},
	}

	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			input := map[string]interface{}{
				"run_id": runID,
				"node": map[string]interface{}{
					"health": c.health,
					"id":     fmt.Sprintf("node-%s", runID[:8]),
				},
			}
			d, err := e.Evaluate(ctx, SchedulingPolicyName, input)
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}

			// Hard-fail on wrong decision (no tolerance, no skip).
			if d.Allow != c.wantAllow {
				t.Fatalf("health=%d: Allow=%v want %v — rego threshold may be misconfigured",
					c.health, d.Allow, c.wantAllow)
			}

			// The run UUID must round-trip into EvaluatedInput.
			if got, ok := d.EvaluatedInput["run_id"]; !ok || got != runID {
				t.Fatalf("EvaluatedInput[run_id] = %v, want %s", got, runID)
			}

			// The node health must appear in EvaluatedInput.
			nodeMap, _ := d.EvaluatedInput["node"].(map[string]interface{})
			if nodeMap == nil {
				t.Fatal("EvaluatedInput missing 'node' map")
			}

			// Reason must mention health.
			if !strings.Contains(d.Reason, "health") {
				t.Fatalf("Reason %q does not mention health", d.Reason)
			}

			// PolicyName must be set.
			if d.PolicyName != SchedulingPolicyName {
				t.Fatalf("PolicyName = %q, want %q", d.PolicyName, SchedulingPolicyName)
			}

			t.Logf("run_id=%s health=%d Allow=%v Reason=%q", runID, c.health, d.Allow, d.Reason)
		})
	}
}

// TestIntegrationCustomPolicySameEngine proves that multiple distinct policies
// can coexist in the engine and are evaluated independently — i.e., the engine
// does not confuse compiled queries across policy names.
func TestIntegrationCustomPolicySameEngine(t *testing.T) {
	runID := uuid.New().String()
	ctx := context.Background()

	e := NewEngine()
	MustLoadSchedulingPolicy(e)

	// Load an additional RBAC policy alongside the scheduling one.
	rbacRego := `
package rbac
default allow = false
allow { input.role == "operator" }
`
	if err := e.LoadPolicy(ctx, "rbac", rbacRego); err != nil {
		t.Fatalf("LoadPolicy rbac: %v", err)
	}

	if e.PolicyCount() != 2 {
		t.Fatalf("PolicyCount = %d, want 2", e.PolicyCount())
	}

	// Scheduling: health=60 must allow.
	d, err := e.Evaluate(ctx, SchedulingPolicyName, map[string]interface{}{
		"run_id": runID,
		"node":   map[string]interface{}{"health": 60},
	})
	if err != nil || !d.Allow {
		t.Fatalf("scheduling health=60: Allow=%v err=%v", d.Allow, err)
	}

	// RBAC: operator must allow.
	d, err = e.Evaluate(ctx, "rbac", map[string]interface{}{
		"run_id": runID,
		"role":   "operator",
	})
	if err != nil || !d.Allow {
		t.Fatalf("rbac operator: Allow=%v err=%v", d.Allow, err)
	}

	// RBAC: viewer must deny.
	d, err = e.Evaluate(ctx, "rbac", map[string]interface{}{
		"run_id": runID,
		"role":   "viewer",
	})
	if err != nil || d.Allow {
		t.Fatalf("rbac viewer: Allow=%v err=%v (expected deny)", d.Allow, err)
	}

	t.Logf("integration run_id=%s: scheduling+rbac coexistence OK", runID)
}
