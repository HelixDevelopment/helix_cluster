package llm

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ErrVerificationFailed is returned by Verifier.Verify when an advisory violates
// a constitutional constraint — most importantly when it references a target the
// cluster does not actually have (a hallucinated action). It is typed so the
// caller can distinguish an unsafe/hallucinated proposal from a transient error,
// and it carries the machine-readable Reason for audit.
type ErrVerificationFailed struct {
	ID     string
	Target string
	Reason string
}

func (e *ErrVerificationFailed) Error() string {
	return fmt.Sprintf("llm: advisory %s rejected by verifier: %s (target=%q)", e.ID, e.Reason, e.Target)
}

// IsVerificationFailed reports whether err is (or wraps) ErrVerificationFailed.
func IsVerificationFailed(err error) bool {
	var t *ErrVerificationFailed
	return errors.As(err, &t)
}

// Verifier is the mandatory "LLMs verifier" gate. Every advisory the Store
// generates is run through Verify before it can be persisted as PENDING. The
// verifier validates an advisory against constitutional constraints and REJECTS
// hallucinated or unsafe actions — for example an action that references a
// target which does not exist in the cluster, or one that proposes a forbidden
// action on a protected target. This is the safety net that stops an LLM from
// acting on a confidently-stated fiction.
type Verifier struct {
	mu sync.RWMutex
	// knownTargets is the set of targets that actually exist in the cluster. An
	// advisory whose Target is absent here is a hallucination and is rejected.
	knownTargets map[string]struct{}
	// constraints are additional named predicates an advisory must satisfy. A
	// constraint returns a non-empty reason string to REJECT the advisory.
	constraints map[string]Constraint
}

// Constraint is a named constitutional predicate evaluated against an advisory.
// It returns "" to accept, or a non-empty human-readable reason to REJECT.
type Constraint func(a *Advisory) (reason string)

// NewVerifier builds a verifier whose known-target oracle is the supplied set.
// Targets not in this set are treated as hallucinated and rejected.
func NewVerifier(knownTargets ...string) *Verifier {
	v := &Verifier{
		knownTargets: make(map[string]struct{}, len(knownTargets)),
		constraints:  make(map[string]Constraint),
	}
	for _, t := range knownTargets {
		v.knownTargets[t] = struct{}{}
	}
	return v
}

// AddTarget registers a real cluster target so advisories referencing it pass
// the existence check.
func (v *Verifier) AddTarget(target string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.knownTargets[target] = struct{}{}
}

// AddConstraint registers a named constitutional constraint. Re-adding a name
// replaces the prior predicate.
func (v *Verifier) AddConstraint(name string, c Constraint) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.constraints[name] = c
}

// Verify validates an advisory against the verifier's constraints. It returns a
// typed *ErrVerificationFailed (REJECT) when:
//   - the advisory names no target, or
//   - the advisory's Target is not a known cluster target (hallucination), or
//   - the advisory's Confidence is outside [0,1] (a malformed proposal), or
//   - any registered named Constraint returns a rejection reason.
//
// It returns nil when the advisory is constitutionally admissible. Verify NEVER
// mutates the advisory.
func (v *Verifier) Verify(a *Advisory) error {
	if a == nil {
		return &ErrVerificationFailed{Reason: "nil advisory"}
	}
	if strings.TrimSpace(a.Target) == "" {
		return &ErrVerificationFailed{ID: a.ID, Target: a.Target, Reason: "advisory names no target"}
	}
	if a.Confidence < 0 || a.Confidence > 1 {
		return &ErrVerificationFailed{ID: a.ID, Target: a.Target, Reason: fmt.Sprintf("confidence %.3f outside [0,1]", a.Confidence)}
	}

	v.mu.RLock()
	defer v.mu.RUnlock()

	if _, ok := v.knownTargets[a.Target]; !ok {
		// Hallucinated action: the LLM referenced a target the cluster does not
		// have. This is the canonical unsafe-action rejection.
		return &ErrVerificationFailed{ID: a.ID, Target: a.Target, Reason: "references non-existent target (hallucinated)"}
	}

	// Evaluate named constraints in deterministic order for reproducible reasons.
	names := make([]string, 0, len(v.constraints))
	for name := range v.constraints {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if reason := v.constraints[name](a); reason != "" {
			return &ErrVerificationFailed{ID: a.ID, Target: a.Target, Reason: fmt.Sprintf("constraint %q: %s", name, reason)}
		}
	}
	return nil
}
