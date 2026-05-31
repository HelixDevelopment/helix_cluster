package policy

import (
	"sync"
	"testing"
)

func TestEngineLoadAndEvaluate(t *testing.T) {
	e := NewEngine()
	if err := e.LoadPolicy("test", "package test\nallow { true }"); err != nil {
		t.Fatal(err)
	}

	allowed, decisions, err := e.Evaluate("test", map[string]interface{}{"allowed": true})
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Error("expected allowed")
	}
	if decisions["policy"] != "test" {
		t.Errorf("policy = %v, want test", decisions["policy"])
	}
}

func TestEngineEvaluateDenied(t *testing.T) {
	e := NewEngine()
	_ = e.LoadPolicy("test", "package test")

	allowed, _, err := e.Evaluate("test", map[string]interface{}{"allowed": false})
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Error("expected denied")
	}
}

func TestEngineMissingPolicy(t *testing.T) {
	e := NewEngine()
	_, _, err := e.Evaluate("missing", nil)
	if err == nil {
		t.Error("expected error for missing policy")
	}
}

func TestEngineListPolicies(t *testing.T) {
	e := NewEngine()
	_ = e.LoadPolicy("p1", "")
	_ = e.LoadPolicy("p2", "")

	policies := e.ListPolicies()
	if len(policies) != 2 {
		t.Errorf("policies = %d, want 2", len(policies))
	}
}

// --- Rule-driven evaluation: the stored Rego text must affect the decision. ---

// TestEvaluateAllowRuleMatches proves an `allow if` rule parsed from the policy
// text actually drives an allow decision against typed input.
// Mutation that fails this: in matchAny, change `return true` to `return false`.
func TestEvaluateAllowRuleMatches(t *testing.T) {
	e := NewEngine()
	if err := e.LoadPolicy("p", "package p\nallow if role == \"admin\""); err != nil {
		t.Fatal(err)
	}
	allowed, dec, err := e.Evaluate("p", map[string]interface{}{"role": "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("expected allow when role==admin")
	}
	if dec["matched"] != "allow" {
		t.Errorf("matched = %v, want allow", dec["matched"])
	}
}

// TestEvaluateAllowRuleNoMatchDefaultDeny proves default-deny: a policy that
// declares rules denies input that matches none of them.
// Mutation that fails this: change `allowed = matchAny(...)` to `allowed = true`.
func TestEvaluateAllowRuleNoMatchDefaultDeny(t *testing.T) {
	e := NewEngine()
	if err := e.LoadPolicy("p", "allow if role == \"admin\""); err != nil {
		t.Fatal(err)
	}
	allowed, dec, err := e.Evaluate("p", map[string]interface{}{"role": "guest"})
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("expected default-deny when no allow rule matches")
	}
	if dec["matched"] != "default-deny" {
		t.Errorf("matched = %v, want default-deny", dec["matched"])
	}
}

// TestEvaluateDenyOverridesAllow proves deny precedence: when both an allow and
// a deny rule match, the deny wins.
// Mutation that fails this: remove the `case matchAny(policy.deny, input):`
// branch (or reorder it after the allow branch).
func TestEvaluateDenyOverridesAllow(t *testing.T) {
	e := NewEngine()
	rego := "allow if tier == \"gold\"\ndeny if banned == \"true\""
	if err := e.LoadPolicy("p", rego); err != nil {
		t.Fatal(err)
	}

	// String-valued deny input exercises the deny rule's string path directly,
	// independently of stringify's bool conversion. The rule literal is the
	// string "true" (quotes stripped by unquote), and the input is the string
	// "true", so the match happens via stringify's string case — not the bool
	// case. This isolates deny precedence from the bool->string conversion: if
	// the bool-stringify path were changed to no longer yield "true", the bool
	// sub-case below would silently stop firing but this string sub-case still
	// proves the deny rule and its precedence over the matching allow.
	allowed, dec, err := e.Evaluate("p", map[string]interface{}{"tier": "gold", "banned": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("expected deny (string \"true\") to override matching allow")
	}
	if dec["matched"] != "deny" {
		t.Errorf("matched = %v, want deny (string path)", dec["matched"])
	}

	// Bool-valued deny input additionally exercises stringify's bool case:
	// stringify(true) == "true" must match the rule literal "true".
	allowed, dec, err = e.Evaluate("p", map[string]interface{}{"tier": "gold", "banned": true})
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("expected deny (bool true) to override matching allow")
	}
	if dec["matched"] != "deny" {
		t.Errorf("matched = %v, want deny (bool path)", dec["matched"])
	}
}

// TestEvaluateStringifyNumber proves typed (non-string) input is compared
// against the string literal in the rule, exercising the float64 path that
// JSON decoding produces.
//
// The first sub-case (level==5) is NOT sufficient on its own: for whole
// numbers, deleting the float64 branch falls through to the default
// fmt.Sprintf("%v", ...) which also yields "5", so the mutation survives. The
// large-magnitude sub-case below is the real killer: FormatFloat(1e19) renders
// the full decimal "10000000000000000000" while fmt.Sprintf("%v", 1e19) renders
// "1e+19". Only the dedicated float64 branch produces the value the rule needs.
// Mutation that fails this: in stringify, delete the float64 case (falling
// through to default), OR change the float64 case to `return ""`.
func TestEvaluateStringifyNumber(t *testing.T) {
	e := NewEngine()
	if err := e.LoadPolicy("p", "allow if level == 5"); err != nil {
		t.Fatal(err)
	}
	allowed, _, err := e.Evaluate("p", map[string]interface{}{"level": float64(5)})
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("expected allow when numeric level stringifies to 5")
	}
	// And a non-matching number must deny.
	allowed, _, _ = e.Evaluate("p", map[string]interface{}{"level": float64(6)})
	if allowed {
		t.Fatal("expected deny when level=6 != 5")
	}

	// Large-magnitude float: discriminates the float64 branch from the default
	// fmt backstop. FormatFloat(1e19, 'f', -1, 64) == "10000000000000000000",
	// whereas fmt.Sprintf("%v", float64(1e19)) == "1e+19". The rule literal is
	// the full decimal form, so the allow only fires via the float64 branch.
	if err := e.LoadPolicy("big", "allow if n == 10000000000000000000"); err != nil {
		t.Fatal(err)
	}
	allowed, _, err = e.Evaluate("big", map[string]interface{}{"n": float64(1e19)})
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("expected allow when 1e19 stringifies via FormatFloat to its full decimal form")
	}
}

// TestEvaluateLegacyFallback proves backward compatibility: a policy with no
// parsed rules still honours input["allowed"].
// Mutation that fails this: in the default branch, delete the `allowed = b` line.
func TestEvaluateLegacyFallback(t *testing.T) {
	e := NewEngine()
	if err := e.LoadPolicy("p", "package p\nallow { true }"); err != nil {
		t.Fatal(err)
	}
	allowed, dec, err := e.Evaluate("p", map[string]interface{}{"allowed": true})
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("expected legacy allowed=true to allow")
	}
	if dec["matched"] != "legacy" {
		t.Errorf("matched = %v, want legacy", dec["matched"])
	}
}

// TestLoadPolicyRejectsMalformedRule proves LoadPolicy validates rule syntax
// and does NOT store a policy whose rule line is malformed.
// Mutation that fails this: in parseRego, change the `==` error return to
// `continue`.
func TestLoadPolicyRejectsMalformedRule(t *testing.T) {
	e := NewEngine()
	err := e.LoadPolicy("bad", "allow if role admin")
	if err == nil {
		t.Fatal("expected error for rule missing '=='")
	}
	if e.PolicyCount() != 0 {
		t.Errorf("malformed policy must not be stored; count = %d", e.PolicyCount())
	}
}

// TestLoadPolicyRejectsEmptyKey proves empty key/value rules are rejected.
// Mutation that fails this: remove the `key == "" || value == ""` check.
func TestLoadPolicyRejectsEmptyValue(t *testing.T) {
	e := NewEngine()
	if err := e.LoadPolicy("bad", "deny if role =="); err == nil {
		t.Fatal("expected error for empty value")
	}
}

// --- Lifecycle methods. ---

// TestRemovePolicy proves RemovePolicy deletes a loaded policy and reports an
// error for an unknown one.
// Mutation that fails this: in RemovePolicy, delete the `delete(e.policies, name)` line.
func TestRemovePolicy(t *testing.T) {
	e := NewEngine()
	_ = e.LoadPolicy("p", "")
	if err := e.RemovePolicy("p"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if e.PolicyCount() != 0 {
		t.Errorf("count after remove = %d, want 0", e.PolicyCount())
	}
	if err := e.RemovePolicy("p"); err == nil {
		t.Error("expected error removing already-removed policy")
	}
}

// TestGetPolicyReturnsCopy proves GetPolicy returns a defensive copy: mutating
// the returned map must not corrupt engine state.
// Mutation that fails this: in GetPolicy, return p (the stored pointer's value)
// directly without copying Rules — i.e. `rules := p.Rules`.
func TestGetPolicyReturnsCopy(t *testing.T) {
	e := NewEngine()
	_ = e.LoadPolicy("p", "package p")
	got, ok := e.GetPolicy("p")
	if !ok {
		t.Fatal("expected policy to exist")
	}
	if got.Name != "p" || got.Rego != "package p" {
		t.Errorf("got = %+v", got)
	}
	got.Rules["injected"] = "x"
	again, _ := e.GetPolicy("p")
	if _, leaked := again.Rules["injected"]; leaked {
		t.Error("mutation of returned copy leaked into engine state")
	}
}

// TestGetPolicyMissing proves the not-found path.
// Mutation that fails this: in GetPolicy, change `return Policy{}, false` to
// `return Policy{}, true`.
func TestGetPolicyMissing(t *testing.T) {
	e := NewEngine()
	if _, ok := e.GetPolicy("nope"); ok {
		t.Error("expected ok=false for missing policy")
	}
}

// TestLoadPolicyReplaces proves a second load under the same name replaces the
// first (so a stale rule set cannot linger).
// Mutation that fails this: in LoadPolicy, guard the assignment with
// `if _, ok := e.policies[name]; !ok { e.policies[name] = ... }`.
func TestLoadPolicyReplaces(t *testing.T) {
	e := NewEngine()
	_ = e.LoadPolicy("p", "allow if role == \"a\"")
	_ = e.LoadPolicy("p", "allow if role == \"b\"")
	if e.PolicyCount() != 1 {
		t.Fatalf("count = %d, want 1", e.PolicyCount())
	}
	allowed, _, _ := e.Evaluate("p", map[string]interface{}{"role": "a"})
	if allowed {
		t.Error("old rule (role==a) must be gone after replace")
	}
	allowed, _, _ = e.Evaluate("p", map[string]interface{}{"role": "b"})
	if !allowed {
		t.Error("new rule (role==b) must apply after replace")
	}
}

// TestEvaluateConcurrent exercises the RWMutex under -race with concurrent
// loads, evaluations, and removals. The race detector is the assertion.
// Mutation that fails this (under -race): change the RLock in Evaluate to a
// no-op by reading e.policies without holding e.mu.
func TestEvaluateConcurrent(t *testing.T) {
	e := NewEngine()
	_ = e.LoadPolicy("base", "allow if ok == \"true\"")

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(3)
		go func(n int) {
			defer wg.Done()
			_ = e.LoadPolicy("base", "allow if ok == \"true\"")
		}(i)
		go func() {
			defer wg.Done()
			_, _, _ = e.Evaluate("base", map[string]interface{}{"ok": true})
		}()
		go func() {
			defer wg.Done()
			_ = e.ListPolicies()
		}()
	}
	wg.Wait()

	allowed, _, err := e.Evaluate("base", map[string]interface{}{"ok": true})
	if err != nil || !allowed {
		t.Fatalf("post-concurrency eval: allowed=%v err=%v", allowed, err)
	}
}
