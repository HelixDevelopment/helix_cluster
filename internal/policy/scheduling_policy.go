package policy

import "context"

// SchedulingPolicyName is the canonical name for the HelixConstitution
// scheduling policy that must be loaded into the engine before the scheduler
// calls Evaluate.
const SchedulingPolicyName = "scheduling"

// SchedulingPolicyRego is the embedded OPA rego source for the HelixConstitution
// scheduling rule.  The rule enforces:
//
//   - DENY  when input.node.health < 50  (node is too degraded to accept work)
//   - ALLOW when input.node.health >= 50 (node is healthy enough)
//
// The policy is compiled once at engine initialisation; all subsequent
// Evaluate calls use the PreparedEvalQuery for deterministic, low-latency
// evaluation without re-parsing or re-compiling.
//
// OPA rego semantics: the `allow` rule is undefined (empty result set) when no
// rule body evaluates to true.  The engine treats undefined as deny.
const SchedulingPolicyRego = `
package scheduling

# allow is true only when the target node health is at or above the threshold.
# Any value below 50 leaves allow undefined (OPA default-deny).
default allow = false

allow {
	is_number(input.node.health)
	input.node.health >= 50
}
`

// MustLoadSchedulingPolicy loads the HelixConstitution scheduling policy into
// engine e.  It panics on compile error because a misconfigured scheduling
// policy is a fatal startup condition — the cluster cannot safely schedule work
// without it.  Call this once during process initialisation.
func MustLoadSchedulingPolicy(e *Engine) {
	ctx := context.Background()
	if err := e.LoadPolicy(ctx, SchedulingPolicyName, SchedulingPolicyRego); err != nil {
		panic("helix-policy: failed to compile scheduling policy: " + err.Error())
	}
}
