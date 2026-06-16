package policy

// policy_adversarial2_test.go — DEEPER (second-pass) adversarial probes for the
// OPA/rego-backed policy Engine. The first pass (policy_adversarial_test.go)
// already pinned fail-closed on hostile inputs and reproduced the non-numeric
// scheduling fail-open (now fixed with `is_number` in scheduling_policy.go).
//
// This pass targets the SINK-SIDE AUDIT RECORD rather than the permit/deny
// verdict: Decision.Reason is forwarded verbatim into the DecisionLogEntry audit
// sink, so a fabricated reason is a §7.1 / CLAUDE-1 evidence-integrity defect of
// the same class as a passing test on a broken feature.
//
// REAL BUG found & fixed here: buildReason emitted the scheduling-specific
// "node health N >= 50, scheduling allowed" / "N < 50, scheduling denied"
// rationale for ANY policy carrying a numeric input.node.health — not just the
// scheduling policy. For an unrelated policy this asserted a mathematically
// FALSE comparison (e.g. allow=true with health=10 → "health 10 >= 50") and
// misattributed the verdict to a health threshold that never ran. The fix gates
// that message on policyName == SchedulingPolicyName.
//
// ANTI-HANG: all evaluations use context.Background() and are O(1); no loops
// over unbounded input, no goroutines, no sleeps.

import (
	"context"
	"strings"
	"testing"
)

func adv2Ctx() context.Context { return context.Background() }

func adv2Load(t *testing.T, e *Engine, name, src string) {
	t.Helper()
	if err := e.LoadPolicy(adv2Ctx(), name, src); err != nil {
		t.Fatalf("LoadPolicy(%q): %v", name, err)
	}
}

func adv2Eval(t *testing.T, e *Engine, name string, in map[string]interface{}) Decision {
	t.Helper()
	d, err := e.Evaluate(adv2Ctx(), name, in)
	if err != nil {
		t.Fatalf("Evaluate(%q): unexpected error %v", name, err)
	}
	return d
}

// ---------------------------------------------------------------------------
// (a) REASON-BLUFF — the audit reason must not fabricate a health rationale
//     for a non-scheduling policy. MUTATION-PROOF of the buildReason fix.
// ---------------------------------------------------------------------------

// TestAdversarial2_ReasonNoFalseHealthRationale_NonSchedulingAllow pins that a
// NON-scheduling policy which ALLOWS, while the input merely happens to carry a
// numeric node.health BELOW the scheduling threshold, does NOT get an audit
// reason that (1) claims the verdict was "scheduling allowed" or (2) asserts a
// mathematically false ">= 50" comparison.
//
// MUTATION PROOF: revert the buildReason fix (drop the
// `policyName == SchedulingPolicyName` guard) and this test FAILS — the reason
// becomes `policy "authz": node health 10 >= 50, scheduling allowed`, a false
// rationale attached to an admin-RBAC allow. With the guard present it PASSES.
func TestAdversarial2_ReasonNoFalseHealthRationale_NonSchedulingAllow(t *testing.T) {
	e := NewEngine()
	adv2Load(t, e, "authz", `package authz
default allow = false
allow { input.role == "admin" }
`)
	d := adv2Eval(t, e, "authz", map[string]interface{}{
		"role": "admin",
		"node": map[string]interface{}{"health": 10}, // numeric, but irrelevant to this policy
	})
	if !d.Allow {
		t.Fatalf("positive control: role=admin must be ALLOWED")
	}
	// The reason must not claim this allow came from a scheduling health check.
	if strings.Contains(d.Reason, "scheduling") {
		t.Fatalf("REASON BLUFF: non-scheduling allow got scheduling rationale: %q", d.Reason)
	}
	// The reason must not assert the false comparison "10 >= 50".
	if strings.Contains(d.Reason, ">= 50") {
		t.Fatalf("REASON BLUFF: false comparison in audit reason: %q (health was 10)", d.Reason)
	}
}

// TestAdversarial2_ReasonNoFalseHealthRationale_NonSchedulingDeny is the deny
// twin: a non-scheduling policy that DENIES (admin guard fails) while the input
// carries node.health = 99 must not record "health 99 < 50, scheduling denied
// (health threshold)" — a doubly-false claim (99 is not < 50, and the threshold
// never ran).
//
// MUTATION PROOF: without the buildReason guard this FAILS (reason asserts
// "99 < 50" and attributes the deny to the health threshold); with it, PASSES.
func TestAdversarial2_ReasonNoFalseHealthRationale_NonSchedulingDeny(t *testing.T) {
	e := NewEngine()
	adv2Load(t, e, "authz", `package authz
default allow = false
allow { input.role == "admin" }
`)
	d := adv2Eval(t, e, "authz", map[string]interface{}{
		"role": "guest",
		"node": map[string]interface{}{"health": 99},
	})
	if d.Allow {
		t.Fatalf("positive control: role=guest must be DENIED")
	}
	if strings.Contains(d.Reason, "scheduling") || strings.Contains(d.Reason, "health threshold") {
		t.Fatalf("REASON BLUFF: non-scheduling deny misattributed to health threshold: %q", d.Reason)
	}
	if strings.Contains(d.Reason, "< 50") {
		t.Fatalf("REASON BLUFF: false comparison in audit reason: %q (health was 99)", d.Reason)
	}
}

// TestAdversarial2_SchedulingReasonStaysAccurate pins the OTHER side of the fix:
// the genuine scheduling policy MUST still produce its health rationale, and
// that rationale must be ARITHMETICALLY TRUE for the verdict — allow ⟹ the
// reported health really is >= 50; deny ⟹ it really is < 50. This guarantees
// the fix did not strip the legitimate scheduling message and that the
// scheduling message is never itself a bluff.
func TestAdversarial2_SchedulingReasonStaysAccurate(t *testing.T) {
	e := NewEngine()
	MustLoadSchedulingPolicy(e)

	// Allow path: health 80 -> allowed, reason must mention health and the
	// ">= 50, scheduling allowed" claim is true.
	dAllow := adv2Eval(t, e, SchedulingPolicyName, map[string]interface{}{
		"node": map[string]interface{}{"health": 80},
	})
	if !dAllow.Allow {
		t.Fatalf("scheduling health=80 must be ALLOWED")
	}
	if !strings.Contains(dAllow.Reason, "health") {
		t.Fatalf("scheduling allow reason must mention health, got %q", dAllow.Reason)
	}
	if !strings.Contains(dAllow.Reason, ">= 50") {
		t.Fatalf("scheduling allow reason lost its (true) threshold rationale: %q", dAllow.Reason)
	}

	// Deny path: health 40 -> denied, reason must say "< 50".
	dDeny := adv2Eval(t, e, SchedulingPolicyName, map[string]interface{}{
		"node": map[string]interface{}{"health": 40},
	})
	if dDeny.Allow {
		t.Fatalf("scheduling health=40 must be DENIED")
	}
	if !strings.Contains(dDeny.Reason, "< 50") {
		t.Fatalf("scheduling deny reason lost its (true) threshold rationale: %q", dDeny.Reason)
	}
}

// ---------------------------------------------------------------------------
// (b) DECISION-LOG SINK carries the (now honest) reason — sink-side, not just
//     the returned Decision. Pins that the audit record a downstream auditor
//     reads is the corrected reason, closing the loop end-to-end.
// ---------------------------------------------------------------------------

func TestAdversarial2_DecisionLogReasonHonest(t *testing.T) {
	var sunk []DecisionLogEntry
	e := NewEngineWithOptions(WithDecisionLog(), WithDecisionLogSink(func(en DecisionLogEntry) {
		sunk = append(sunk, en)
	}))
	adv2Load(t, e, "authz", `package authz
default allow = false
allow { input.role == "admin" }
`)
	_ = adv2Eval(t, e, "authz", map[string]interface{}{
		"role": "admin",
		"node": map[string]interface{}{"health": 3},
	})
	if len(sunk) != 1 {
		t.Fatalf("expected exactly 1 sunk decision-log entry, got %d", len(sunk))
	}
	if strings.Contains(sunk[0].Reason, "scheduling") || strings.Contains(sunk[0].Reason, ">= 50") {
		t.Fatalf("AUDIT SINK BLUFF: persisted entry carries fabricated health rationale: %q", sunk[0].Reason)
	}
	// In-memory ring must agree with the external sink (no divergence).
	mem := e.DecisionLog()
	if len(mem) != 1 || mem[0].Reason != sunk[0].Reason {
		t.Fatalf("in-memory log and external sink disagree: mem=%+v sink=%+v", mem, sunk)
	}
}

// ---------------------------------------------------------------------------
// (c) DEEPER FAIL-CLOSED edges the first pass did not cover. Each pins
//     deny-by-default on a structurally unusual but well-formed policy.
// ---------------------------------------------------------------------------

// TestAdversarial2_PartialSetAllowFailsClosed pins that a policy whose `allow`
// is a PARTIAL SET rule (`allow[x] { ... }`) — i.e. allow is a *set*, not a
// boolean — is treated as DENY. The Engine reads a single boolean expression
// value; a set value fails the `.(bool)` assertion and must fall through to
// deny rather than being coerced to "non-empty set == allow".
func TestAdversarial2_PartialSetAllowFailsClosed(t *testing.T) {
	e := NewEngine()
	adv2Load(t, e, "ps", `package ps
allow[x] { x := input.roles[_] }
`)
	// roles is NON-empty: a truthiness bug would permit here.
	d := adv2Eval(t, e, "ps", map[string]interface{}{"roles": []interface{}{"admin", "root"}})
	if d.Allow {
		t.Fatalf("FAIL-OPEN: partial-set allow with non-empty set was PERMITTED; must DENY")
	}
}

// TestAdversarial2_PackageParseEdgesNeverFailOpen pins that quirky-but-valid
// `package` declarations (indented, trailing inline comment, preceded by a
// commented-out fake package line) are parsed to the CORRECT namespace so the
// query targets the real allow rule — and in every case a should-DENY input
// stays denied (no namespace mismatch silently turning into a permit, and no
// commented `# package` line hijacking the parse).
func TestAdversarial2_PackageParseEdgesNeverFailOpen(t *testing.T) {
	cases := []struct {
		desc string
		src  string
	}{
		{"indented package", "   package edge1\ndefault allow = false\nallow { input.ok == true }\n"},
		{"trailing inline comment", "package edge2  # ns with comment\ndefault allow = false\nallow { input.ok == true }\n"},
		{"commented fake package first", "# package decoy\npackage edge3\ndefault allow = false\nallow { input.ok == true }\n"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			e := NewEngine()
			adv2Load(t, e, "x", tc.src)
			// Positive control: the real rule fires for ok==true.
			if !adv2Eval(t, e, "x", map[string]interface{}{"ok": true}).Allow {
				t.Fatalf("namespace parse wrong: ok==true should ALLOW for %s", tc.desc)
			}
			// Should-deny input must deny (not silently permit via wrong ns).
			if adv2Eval(t, e, "x", map[string]interface{}{"ok": false}).Allow {
				t.Fatalf("FAIL-OPEN: ok==false was PERMITTED for %s", tc.desc)
			}
			if adv2Eval(t, e, "x", map[string]interface{}{}).Allow {
				t.Fatalf("FAIL-OPEN: empty input was PERMITTED for %s", tc.desc)
			}
		})
	}
}
