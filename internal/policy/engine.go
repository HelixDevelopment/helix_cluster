// Package policy provides a real OPA/rego-backed policy engine for Helix
// Cluster OS.  Each named policy is compiled once at load time into a
// PreparedEvalQuery; Evaluate runs the compiled query against the caller's
// input and surfaces the allow/deny decision together with the evaluated input
// captured in the Decision.
package policy

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/open-policy-agent/opa/rego"
)

// timeNow is the clock function used when stamping DecisionLogEntry.At.
// It is a package-level variable so tests can override it if needed.
var timeNow = time.Now

// Decision is the result of evaluating a policy against an input document.
type Decision struct {
	// Allow is true when the policy permits the request.
	Allow bool
	// Reason is a human-readable description of why the decision was reached.
	// For scheduling decisions this includes the health threshold rationale.
	Reason string
	// EvaluatedInput is a copy of the input document that was evaluated,
	// surfaced so callers can prove the correct input round-tripped into the
	// engine (required by §7.1 evidence and integration tests).
	EvaluatedInput map[string]interface{}
	// PolicyName is the name under which the policy was loaded.
	PolicyName string
}

// compiledPolicy holds the OPA artefacts for a single named policy.
type compiledPolicy struct {
	name  string
	query rego.PreparedEvalQuery
}

// Engine compiles and evaluates OPA rego policies.  Zero value is invalid; use
// NewEngine or NewEngineWithOptions.
type Engine struct {
	mu       sync.RWMutex
	policies map[string]*compiledPolicy

	// Decision-log fields (HXC-1220).  Populated only when the engine is
	// constructed with WithDecisionLog() or WithDecisionLogSink().
	logOpts     *engineOptions
	logMu       sync.RWMutex
	decisionLog []DecisionLogEntry
}

// NewEngine returns a ready-to-use Engine with no policies loaded.
func NewEngine() *Engine {
	return &Engine{policies: make(map[string]*compiledPolicy)}
}

// LoadPolicy compiles the supplied Rego source (which MUST declare a boolean
// rule named `allow` under any package) and stores it under name.  A second
// call with the same name replaces the previous policy atomically.  The
// compilation happens under the caller's context so a deadline can bound it.
func (e *Engine) LoadPolicy(ctx context.Context, name, regoSrc string) error {
	if name == "" {
		return fmt.Errorf("policy name must not be empty")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Extract the package path declared in the rego source so the query
	// targets the correct data namespace (e.g. "data.scheduling.allow").
	// This handles policy names that differ from the rego package (hyphens,
	// aliases, etc.) and avoids hard-coding the package in every caller.
	pkg, err := regoPackage(regoSrc)
	if err != nil {
		return fmt.Errorf("policy %q: cannot determine rego package: %w", name, err)
	}

	// Compile the module once; the prepared query is reused across Evaluate calls.
	pq, err := rego.New(
		rego.Query("data."+pkg+".allow"),
		rego.Module(name+".rego", regoSrc),
	).PrepareForEval(ctx)
	if err != nil {
		return fmt.Errorf("policy %q: compile: %w", name, err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.policies[name] = &compiledPolicy{name: name, query: pq}
	return nil
}

// regoPackage parses the `package <path>` declaration from a rego source
// string and returns the dot-separated path (e.g. "scheduling",
// "helix.rbac").  It returns an error when no package declaration is found
// because OPA requires one and a missing package would produce a misleading
// "undefined" result rather than a clear compile error.
func regoPackage(src string) (string, error) {
	for _, raw := range strings.Split(src, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "package ") {
			continue
		}
		pkg := strings.TrimSpace(strings.TrimPrefix(line, "package "))
		// Strip an inline comment.
		if idx := strings.Index(pkg, "#"); idx >= 0 {
			pkg = strings.TrimSpace(pkg[:idx])
		}
		if pkg != "" {
			return pkg, nil
		}
	}
	return "", fmt.Errorf("no 'package' declaration found in rego source")
}

// Evaluate runs the named policy against input and returns a Decision that
// contains the allow/deny verdict, a human-readable reason, and the evaluated
// input document.  The decision is always deterministic for a given
// (policy, input) pair because OPA rego evaluation is deterministic.
//
// Semantics:
//   - If the rego `allow` rule evaluates to true  → Decision.Allow = true.
//   - If the result set is empty or allow is false → Decision.Allow = false.
//   - If the policy is not loaded               → error.
func (e *Engine) Evaluate(ctx context.Context, name string, input map[string]interface{}) (Decision, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	e.mu.RLock()
	cp, ok := e.policies[name]
	e.mu.RUnlock()
	if !ok {
		return Decision{}, fmt.Errorf("policy not found: %q", name)
	}

	rs, err := cp.query.Eval(ctx, rego.EvalInput(input))
	if err != nil {
		return Decision{}, fmt.Errorf("policy %q: eval: %w", name, err)
	}

	// Copy input for the decision record (§7.1 evidence requirement).
	evalInput := make(map[string]interface{}, len(input))
	for k, v := range input {
		evalInput[k] = v
	}

	allow := false
	if len(rs) > 0 && len(rs[0].Expressions) > 0 {
		if b, ok := rs[0].Expressions[0].Value.(bool); ok {
			allow = b
		}
	}

	reason := buildReason(name, allow, input)
	dec := Decision{
		Allow:          allow,
		Reason:         reason,
		EvaluatedInput: evalInput,
		PolicyName:     name,
	}

	// HXC-1220: record an audit entry when the decision-log sink is active.
	e.appendDecisionLog(DecisionLogEntry{
		RunUUID:    extractRunUUID(input),
		PolicyName: name,
		Allow:      allow,
		Reason:     reason,
		At:         timeNow(),
	})

	return dec, nil
}

// buildReason constructs a human-readable explanation.  For the scheduling
// policy the node.health field drives the message; for other policies a
// generic allow/deny string is returned.
//
// The scheduling-specific "health >= 50 / < 50" rationale is emitted ONLY for
// the actual scheduling policy. Emitting it for any policy that merely happens
// to carry a numeric input.node.health produced a fabricated audit reason: the
// hard-coded "50" threshold and the "scheduling" attribution belong to the
// scheduling policy alone, so for an unrelated policy the message could assert a
// mathematically false comparison (e.g. "health 10 >= 50, scheduling allowed")
// and misattribute the verdict to a health threshold that never applied. Since
// Decision.Reason is forwarded verbatim into the DecisionLogEntry audit sink
// (§7.1 evidence integrity), that false rationale is a sink-side defect.
func buildReason(policyName string, allow bool, input map[string]interface{}) string {
	// Attempt to extract node.health for a scheduling-specific message — but
	// only for the scheduling policy, whose rule is the source of the 50
	// threshold this message describes.
	if policyName == SchedulingPolicyName {
		if node, ok := input["node"]; ok {
			if nodeMap, ok := node.(map[string]interface{}); ok {
				if health, ok := toFloat(nodeMap["health"]); ok {
					if allow {
						return fmt.Sprintf("policy %q: node health %.0f >= 50, scheduling allowed", policyName, health)
					}
					return fmt.Sprintf("policy %q: node health %.0f < 50, scheduling denied (health threshold)", policyName, health)
				}
			}
		}
	}
	if allow {
		return fmt.Sprintf("policy %q: allow", policyName)
	}
	return fmt.Sprintf("policy %q: deny", policyName)
}

// toFloat coerces an interface{} that may hold int, int64, float64, or float32
// into a float64, returning false if no conversion is possible.
func toFloat(v interface{}) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case int32:
		return float64(t), true
	default:
		return 0, false
	}
}

// RemovePolicy deletes the named policy.  It returns an error when the policy
// is not found so callers can distinguish a real deletion from a no-op.
func (e *Engine) RemovePolicy(name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.policies[name]; !ok {
		return fmt.Errorf("policy not found: %q", name)
	}
	delete(e.policies, name)
	return nil
}

// PolicyCount returns the number of loaded policies.
func (e *Engine) PolicyCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.policies)
}

// ListPolicies returns all loaded policy names.
func (e *Engine) ListPolicies() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	names := make([]string, 0, len(e.policies))
	for n := range e.policies {
		names = append(names, n)
	}
	return names
}
