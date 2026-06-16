package policy

// policy_adversarial_test.go — adversarial, SINK-SIDE contract-pinning tests
// for the OPA/rego-backed policy Engine.
//
// The highest-severity class this file guards is FAIL-OPEN: a decision that
// returns Allow=true on a missing / malformed / unknown / empty input when it
// must deny. Every assertion below probes the *actual* permit/deny verdict
// (Decision.Allow), never merely "no panic".
//
// CONCLUSION (see the package-level report): the Engine is fail-CLOSED on every
// hostile input path. `Evaluate` initialises `allow := false` and only promotes
// it to true when OPA returns a genuine boolean `true` for `data.<pkg>.allow`.
// Undefined results, empty result sets, nil/empty input, and non-boolean
// `allow` values all fall through to deny. These tests PIN that contract so a
// future refactor that introduces a fail-open default is caught immediately.
//
// ANTI-HANG: concurrency is capped at 8 goroutines; the suite completes well
// under the 90s timeout.

import (
	"context"
	"sync"
	"testing"
)

func advCtx() context.Context { return context.Background() }

func advLoad(t *testing.T, e *Engine, name, src string) {
	t.Helper()
	if err := e.LoadPolicy(advCtx(), name, src); err != nil {
		t.Fatalf("LoadPolicy(%q): %v", name, err)
	}
}

// evalAllow runs Evaluate and returns the sink-side verdict, failing the test
// on any evaluation error (an error path must never be silently treated as a
// permit by a caller, so we surface it loudly here).
func evalAllow(t *testing.T, e *Engine, name string, input map[string]interface{}) bool {
	t.Helper()
	d, err := e.Evaluate(advCtx(), name, input)
	if err != nil {
		t.Fatalf("Evaluate(%q, %v): unexpected error %v", name, input, err)
	}
	return d.Allow
}

// ---------------------------------------------------------------------------
// (a) FAIL-OPEN — a should-deny request must NOT be permitted.
// ---------------------------------------------------------------------------

// TestAdversarialFailClosed_DenyByDefaultMatrix is the headline fail-open
// guard. For a policy that permits ONLY input.role=="admin", every other
// shape of input — nil, empty, unknown subject, wrong type — MUST deny.
func TestAdversarialFailClosed_DenyByDefaultMatrix(t *testing.T) {
	e := NewEngine()
	advLoad(t, e, "rbac", `package rbac
default allow = false
allow { input.role == "admin" }
`)

	// SINK-SIDE positive control: the one permitted shape really does allow,
	// so a "deny everything" mutation could not vacuously pass this file.
	if !evalAllow(t, e, "rbac", map[string]interface{}{"role": "admin"}) {
		t.Fatalf("positive control: role=admin must be ALLOWED")
	}

	denyCases := []struct {
		desc  string
		input map[string]interface{}
	}{
		{"nil input map", nil},
		{"empty input map", map[string]interface{}{}},
		{"unknown subject", map[string]interface{}{"role": "guest"}},
		{"zero-value role", map[string]interface{}{"role": ""}},
		{"wrong-type role (int)", map[string]interface{}{"role": 1}},
		{"wrong-type role (bool)", map[string]interface{}{"role": true}},
		{"unrelated key only", map[string]interface{}{"banana": "admin"}},
		{"nested unexpected", map[string]interface{}{"role": map[string]interface{}{"x": "admin"}}},
	}
	for _, tc := range denyCases {
		t.Run(tc.desc, func(t *testing.T) {
			if evalAllow(t, e, "rbac", tc.input) {
				t.Fatalf("FAIL-OPEN: input %q was PERMITTED but must be DENIED", tc.desc)
			}
		})
	}
}

// TestAdversarialFailClosed_UndefinedAllowRule pins that a policy whose `allow`
// rule is entirely undefined (no default, no satisfiable body) evaluates to an
// empty result set and is treated as DENY — not as an allow and not as an
// error swallowed into a permit.
func TestAdversarialFailClosed_UndefinedAllowRule(t *testing.T) {
	e := NewEngine()
	// No `allow` rule defined at all; only an unrelated rule exists.
	advLoad(t, e, "empty", `package empty
foo = 1
`)
	if evalAllow(t, e, "empty", map[string]interface{}{"anything": "goes"}) {
		t.Fatalf("FAIL-OPEN: undefined allow rule must DENY (empty result set)")
	}
}

// TestAdversarialFailClosed_NonBooleanAllow pins the type guard in Evaluate:
// when a (mis)written policy yields a non-boolean `allow` value (string,
// number, object), the `.(bool)` assertion must fail closed → DENY. A naive
// "truthiness" interpretation here would be a fail-open.
func TestAdversarialFailClosed_NonBooleanAllow(t *testing.T) {
	cases := []struct {
		desc string
		src  string
	}{
		{"allow=string", `package p
allow = "granted"
`},
		{"allow=number", `package p
allow = 1
`},
		{"allow=object", `package p
allow = {"ok": true}
`},
		{"allow=array", `package p
allow = ["yes"]
`},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			e := NewEngine()
			advLoad(t, e, "p", tc.src)
			if evalAllow(t, e, "p", map[string]interface{}{}) {
				t.Fatalf("FAIL-OPEN: non-boolean allow (%s) must DENY", tc.desc)
			}
		})
	}
}

// TestAdversarialFailClosed_UnloadedPolicyErrors pins that evaluating a policy
// that was never loaded returns an error AND a zero Decision (Allow=false) —
// it must never silently permit an unknown policy name.
func TestAdversarialFailClosed_UnloadedPolicyErrors(t *testing.T) {
	e := NewEngine()
	d, err := e.Evaluate(advCtx(), "does-not-exist", map[string]interface{}{"role": "admin"})
	if err == nil {
		t.Fatalf("expected error for unloaded policy, got nil")
	}
	if d.Allow {
		t.Fatalf("FAIL-OPEN: unloaded policy returned Allow=true")
	}
}

// TestAdversarialFailClosed_SchedulingThreshold pins the embedded production
// scheduling policy at the NUMERIC boundary: numeric health < 50, plus a
// missing node / missing health / nil input, MUST deny; numeric >= 50 allows.
// These cases ALL hold on current prod (the policy correctly fails closed for
// numeric and structurally-absent inputs). The separate non-numeric fail-open
// is reproduced by TestAdversarialFailOpen_SchedulingNonNumericHealth_LIVE_BUG.
func TestAdversarialFailClosed_SchedulingThreshold(t *testing.T) {
	e := NewEngine()
	MustLoadSchedulingPolicy(e)

	deny := []map[string]interface{}{
		{"node": map[string]interface{}{"health": 49}},
		{"node": map[string]interface{}{"health": 0}},
		{"node": map[string]interface{}{"health": -1000}},
		{"node": map[string]interface{}{}}, // missing health
		{},                                 // missing node
		nil,                                // nil input
	}
	for i, in := range deny {
		if evalAllow(t, e, SchedulingPolicyName, in) {
			t.Fatalf("FAIL-OPEN: scheduling case %d %v was PERMITTED, must DENY", i, in)
		}
	}
	// Positive controls at and above the threshold.
	for _, h := range []int{50, 51, 100} {
		if !evalAllow(t, e, SchedulingPolicyName, map[string]interface{}{
			"node": map[string]interface{}{"health": h},
		}) {
			t.Fatalf("scheduling health=%d must be ALLOWED", h)
		}
	}
}

// TestAdversarialFailOpen_SchedulingNonNumericHealth_LIVE_BUG is a LIVE-BUG
// REPRODUCER. It is NOT env-gated and is EXPECTED TO FAIL on unfixed prod.
//
// FINDING (REAL BUG, high severity — CLAUDE-1 / §7.1 fail-open): the embedded
// production scheduling policy (scheduling_policy.go SchedulingPolicyRego)
// PERMITS a node whose `health` is a non-numeric value — any string
// ("fine", "high", "", "0") and even an array — instead of denying it.
//
// ROOT CAUSE: Rego's `>=` uses a TOTAL type ordering in which strings/arrays
// sort ABOVE numbers, so the rule body `input.node.health >= 50` evaluates to
// TRUE for a string health. The `default allow = false` never engages because
// the rule body succeeds. A degraded node reporting a corrupted, missing-as-""
// or attacker-influenced health field is therefore scheduled work as if it were
// healthy.
//
// MINIMAL FIX (one line, NOT applied — see report): add a type guard to the
// rule body so it only fires on a real number:
//
//	allow {
//		is_number(input.node.health)   // <-- add this line
//		input.node.health >= 50
//	}
//
// With that guard present this test PASSES; with prod as-is it FAILS, naming
// the exact permitted-but-should-deny input. Do NOT skip/gate it: it is the
// sink-side proof of the defect.
func TestAdversarialFailOpen_SchedulingNonNumericHealth_LIVE_BUG(t *testing.T) {
	e := NewEngine()
	MustLoadSchedulingPolicy(e)

	nonNumeric := []interface{}{
		"fine", // garbage string
		"high",
		"",                     // empty string (e.g. unset/serialised-empty health)
		"0",                    // numeric-looking string that is NOT a number
		[]interface{}{1, 2, 3}, // array
	}
	for _, h := range nonNumeric {
		in := map[string]interface{}{"node": map[string]interface{}{"health": h}}
		if evalAllow(t, e, SchedulingPolicyName, in) {
			t.Fatalf("LIVE FAIL-OPEN: node health=%#v (non-numeric) was PERMITTED; "+
				"a node with non-numeric health MUST be DENIED. "+
				"Fix: add `is_number(input.node.health)` to the scheduling rego rule.", h)
		}
	}
}

// ---------------------------------------------------------------------------
// (b) PRECEDENCE — a deny condition expressed in rego must override allow.
// ---------------------------------------------------------------------------

// TestAdversarialPrecedence_DenyOverridesAllow pins the canonical
// deny-overrides-allow pattern: when an explicit deny condition is present, a
// subject that would otherwise be allowed must be DENIED. The Engine reads a
// single boolean `allow`; precedence is the policy author's expression, so we
// pin that the author's most-restrictive intent is faithfully surfaced and the
// engine never "most-permissive wins" on its own.
func TestAdversarialPrecedence_DenyOverridesAllow(t *testing.T) {
	e := NewEngine()
	// allow only when authenticated AND not on the blocklist.
	advLoad(t, e, "guard", `package guard
default allow = false
allow {
	input.authenticated == true
	not blocked
}
blocked { input.subject == input.blocklist[_] }
`)

	base := func(extra map[string]interface{}) map[string]interface{} {
		m := map[string]interface{}{
			"authenticated": true,
			"blocklist":     []interface{}{"mallory", "eve"},
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	// Allowed subject (not blocked) — positive control.
	if !evalAllow(t, e, "guard", base(map[string]interface{}{"subject": "alice"})) {
		t.Fatalf("authenticated, non-blocked subject must be ALLOWED")
	}
	// Blocked subject must lose the allow even though authenticated==true.
	if evalAllow(t, e, "guard", base(map[string]interface{}{"subject": "mallory"})) {
		t.Fatalf("PRECEDENCE LOST: blocked subject 'mallory' was PERMITTED; deny must override allow")
	}
	if evalAllow(t, e, "guard", base(map[string]interface{}{"subject": "eve"})) {
		t.Fatalf("PRECEDENCE LOST: blocked subject 'eve' was PERMITTED; deny must override allow")
	}
	// Unauthenticated also denied.
	if evalAllow(t, e, "guard", map[string]interface{}{"authenticated": false, "subject": "alice"}) {
		t.Fatalf("unauthenticated subject must be DENIED")
	}
}

// ---------------------------------------------------------------------------
// (c) DETERMINISM / TOTAL-ORDER — verdict must be stable across runs.
// ---------------------------------------------------------------------------

// TestAdversarialDeterminism_StableVerdict pins that a policy with multiple
// equal-priority `allow` definitions yields a byte-stable verdict across many
// repeated evaluations — i.e. no map-iteration nondeterminism leaks into the
// permit/deny decision. Both a should-deny and a should-allow input are
// hammered; any flip across iterations fails the test.
func TestAdversarialDeterminism_StableVerdict(t *testing.T) {
	e := NewEngine()
	advLoad(t, e, "multi", `package multi
default allow = false
allow { input.role == "admin" }
allow { input.tier == "gold" }
allow { input.scopes[_] == "write" }
`)

	const iters = 300
	denyInput := map[string]interface{}{
		"role":   "guest",
		"tier":   "bronze",
		"scopes": []interface{}{"read"},
	}
	allowInput := map[string]interface{}{
		"role":   "guest",
		"tier":   "gold", // satisfies exactly one of the equal-priority rules
		"scopes": []interface{}{"read"},
	}
	for i := 0; i < iters; i++ {
		if evalAllow(t, e, "multi", denyInput) {
			t.Fatalf("NONDETERMINISTIC: deny-input flipped to ALLOW on iter %d", i)
		}
		if !evalAllow(t, e, "multi", allowInput) {
			t.Fatalf("NONDETERMINISTIC: allow-input flipped to DENY on iter %d", i)
		}
	}
}

// ---------------------------------------------------------------------------
// (d) UNGUARDED SHARED STATE — concurrent load/eval/update must be race-free
//     AND must never let a concurrent reload leak a permit on a deny-input.
// ---------------------------------------------------------------------------

// TestAdversarialConcurrent_NoRaceNoFailOpen runs Evaluate, LoadPolicy,
// RemovePolicy/Reload, ListPolicies, and DecisionLog concurrently under -race
// with a capped pool of 8 goroutines. It asserts the sink-side invariant that a
// known should-DENY input is never permitted mid-reload, in addition to relying
// on the race detector for the shared-state guarantee.
func TestAdversarialConcurrent_NoRaceNoFailOpen(t *testing.T) {
	ctx := advCtx()
	e := NewEngineWithOptions(WithDecisionLog())
	const name = "conc"
	src := `package conc
default allow = false
allow { input.role == "admin" }
`
	if err := e.LoadPolicy(ctx, name, src); err != nil {
		t.Fatalf("initial load: %v", err)
	}

	const workers = 8 // capped per ANTI-HANG
	const ops = 80
	var wg sync.WaitGroup
	var failOpen int32 // atomic-ish flag via mutex below
	var mu sync.Mutex

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < ops; j++ {
				switch id % 4 {
				case 0:
					// Hammer a should-DENY input; it must never be permitted,
					// even while another goroutine reloads the policy.
					d, err := e.Evaluate(ctx, name, map[string]interface{}{"role": "guest"})
					if err == nil && d.Allow {
						mu.Lock()
						failOpen++
						mu.Unlock()
					}
				case 1:
					_ = e.LoadPolicy(ctx, name, src) // atomic replace
				case 2:
					_ = e.ListPolicies()
					_ = e.PolicyCount()
				case 3:
					_ = e.DecisionLog()
				}
			}
		}(w)
	}
	wg.Wait()

	if failOpen != 0 {
		t.Fatalf("FAIL-OPEN under concurrency: deny-input permitted %d time(s) during reload", failOpen)
	}
}
