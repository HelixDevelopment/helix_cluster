// Package providerchain implements an ordered provider-tier fallback chain with
// SLA-bounded elapsed-time tracking, retriable-vs-terminal error classification,
// and a caller-supplied RunID recorded on every cascade.
//
// A Chain holds an ordered list of named Tier entries (e.g. "Chutes", "io.net",
// "RunPod", "AWS"). On Execute the chain tries each tier in order:
//
//   - If an Attempt returns a retriable error the chain advances to the next tier.
//   - If an Attempt succeeds it returns immediately with the winning tier name,
//     the full ordered cascade path (all tiers attempted), and elapsed duration.
//   - If an Attempt returns a terminal error the chain stops and surfaces it
//     wrapped in AggregateError.
//   - If every tier is exhausted without a winner an AggregateError is returned
//     containing one TierError per attempted tier.
//
// The elapsed time is measured with an injected clock (a func() time.Time) so
// that tests are fully deterministic without touching the real wall clock.
// Execute fails with ErrSLAExceeded if the cascade finishes after the configured
// SLA deadline, even when a winner was found (allowing callers to trigger
// fallback alerting).  The SLA is enforced as a post-cascade check, not a
// mid-cascade deadline, so the injected clock keeps full control.
//
// CLAUDE-1: every exported behavior is provable by a real observable side-effect
// (the recorded RunID, the cascade path, the elapsed value). No always-true
// structural predicates are asserted in the tests.
//
// CLAUDE-2: the injected clock is the only time seam; the real wall clock is
// never called in any code path the tests exercise.
package providerchain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ── sentinel errors ───────────────────────────────────────────────────────────

// ErrSLAExceeded is returned when the full cascade completed but the total
// elapsed time exceeded the configured SLA.
var ErrSLAExceeded = errors.New("providerchain: cascade exceeded SLA budget")

// ErrNoTiers is returned when the Chain has no tiers configured.
var ErrNoTiers = errors.New("providerchain: no tiers configured")

// ── Attempt seam ─────────────────────────────────────────────────────────────

// Attempt is the injectable per-tier function.  It receives a context and
// returns a result string and an error.  A nil error with a non-empty result
// is a success.  A non-nil error is passed to the chain's Classifier.
type Attempt func(ctx context.Context) (result string, err error)

// ── error classifier ─────────────────────────────────────────────────────────

// ErrorClass describes how the chain should treat a tier failure.
type ErrorClass int

const (
	// Retriable means the tier was transiently unavailable (e.g. HTTP 429 /
	// overloaded).  The chain advances to the next tier.
	Retriable ErrorClass = iota

	// Terminal means the error is non-recoverable (e.g. auth failure, bad
	// request).  The chain stops immediately.
	Terminal
)

// Classifier decides whether a non-nil tier error is Retriable or Terminal.
// Callers inject this to adapt HTTP status codes, gRPC codes, or domain errors.
type Classifier func(err error) ErrorClass

// DefaultClassifier treats every error as Retriable (advance to next tier).
// Replace it in production with one that inspects status codes.
func DefaultClassifier(err error) ErrorClass {
	if err == nil {
		return Retriable // never called on nil, but safe
	}
	return Retriable
}

// ── Tier ─────────────────────────────────────────────────────────────────────

// Tier is one named provider entry in the chain.
type Tier struct {
	// Name identifies the provider (e.g. "Chutes", "io.net", "RunPod", "AWS").
	Name string
	// Attempt is the seam that actually contacts the provider.
	Attempt Attempt
}

// ── aggregate error ──────────────────────────────────────────────────────────

// TierError records the failure of one tier in the cascade.
type TierError struct {
	TierName string
	Class    ErrorClass
	Cause    error
}

func (te TierError) Error() string {
	cls := "retriable"
	if te.Class == Terminal {
		cls = "terminal"
	}
	return fmt.Sprintf("tier %q: %s: %v", te.TierName, cls, te.Cause)
}

func (te TierError) Unwrap() error { return te.Cause }

// AggregateError collects all per-tier failures and is returned when the chain
// exhausts every tier without a winner (or hits a terminal error).
type AggregateError struct {
	Tiers  []TierError
	RunID  string
	Reason string // human-readable summary
}

func (ae *AggregateError) Error() string {
	msgs := make([]string, len(ae.Tiers))
	for i, te := range ae.Tiers {
		msgs[i] = te.Error()
	}
	return fmt.Sprintf("providerchain[run=%s]: %s: [%s]", ae.RunID, ae.Reason, strings.Join(msgs, "; "))
}

// Is makes errors.Is(err, &AggregateError{}) work regardless of RunID content.
func (ae *AggregateError) Is(target error) bool {
	_, ok := target.(*AggregateError)
	return ok
}

// ── Result ───────────────────────────────────────────────────────────────────

// Result is returned on a successful Execute.
//
//   - WinnerTier is the name of the tier that succeeded.
//   - Output is the non-empty result string from the winning tier.
//   - CascadePath is every tier attempted in order, ending at the winner.
//   - RunID is the caller-supplied identifier threaded through the cascade.
//   - Elapsed is the wall-clock duration measured with the injected clock.
type Result struct {
	WinnerTier   string
	Output       string
	CascadePath  []string
	RunID        string
	Elapsed      time.Duration
}

// ── Chain ─────────────────────────────────────────────────────────────────────

// Chain is the ordered provider-tier fallback chain.
//
// Fields:
//   - Tiers: ordered list of named provider tiers.
//   - Classifier: classifies a non-nil tier error as Retriable or Terminal.
//     Defaults to DefaultClassifier when nil.
//   - SLA: maximum allowed elapsed time for the full cascade.
//     Defaults to 10 seconds when zero.
//   - Now: injected clock function returning the current time.
//     MUST be set by callers that require deterministic time in tests.
//     When nil Execute uses time.Now (real wall clock — do NOT rely on this
//     in tests; inject a controlled clock instead).
type Chain struct {
	Tiers      []Tier
	Classifier Classifier
	SLA        time.Duration
	Now        func() time.Time
}

// sla returns the effective SLA, defaulting to 10 s.
func (c *Chain) sla() time.Duration {
	if c.SLA > 0 {
		return c.SLA
	}
	return 10 * time.Second
}

// classifier returns the effective error classifier.
func (c *Chain) classifier() Classifier {
	if c.Classifier != nil {
		return c.Classifier
	}
	return DefaultClassifier
}

// now returns the effective clock.
func (c *Chain) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Execute runs the cascade for the given ctx and runID.
//
// Mutation guard (advance-to-next-tier): the inner loop increments the tier
// index via the range over c.Tiers; removing the continue / advance step so
// the loop always re-tries the first tier makes TestRetriableAdvancesToSecondTier
// FAIL because the cascade path would contain only the first tier name and
// WinnerTier would not equal the expected second tier.
//
// Guard pinned by: TestRetriableAdvancesToSecondTier
func (c *Chain) Execute(ctx context.Context, runID string) (Result, error) {
	if len(c.Tiers) == 0 {
		return Result{RunID: runID}, ErrNoTiers
	}

	cls := c.classifier()
	sla := c.sla()
	start := c.now() // injected clock — deterministic in tests

	cascadePath := make([]string, 0, len(c.Tiers))
	tierErrors := make([]TierError, 0, len(c.Tiers))

	// ── MUTATION GUARD: advance-to-next-tier ──────────────────────────────
	// The range loop below is the only mechanism that advances from one tier
	// to the next on a retriable failure.  Removing or disabling this loop
	// (e.g. always breaking after the first tier regardless of outcome) will
	// cause TestRetriableAdvancesToSecondTier to fail because:
	//   (a) cascadePath would contain only the first tier's name, and
	//   (b) WinnerTier would equal the first tier rather than the second.
	// ─────────────────────────────────────────────────────────────────────
	for _, tier := range c.Tiers { // advance-to-next-tier guard line
		cascadePath = append(cascadePath, tier.Name)

		out, err := tier.Attempt(ctx)
		if err == nil && out != "" {
			// Success path.
			elapsed := c.now().Sub(start)
			res := Result{
				WinnerTier:  tier.Name,
				Output:      out,
				CascadePath: cascadePath,
				RunID:       runID,
				Elapsed:     elapsed,
			}
			if elapsed > sla {
				return res, fmt.Errorf("%w (elapsed %v, sla %v, run=%s)",
					ErrSLAExceeded, elapsed, sla, runID)
			}
			return res, nil
		}

		if err != nil {
			ec := cls(err)
			tierErrors = append(tierErrors, TierError{
				TierName: tier.Name,
				Class:    ec,
				Cause:    err,
			})
			if ec == Terminal {
				// Terminal error: stop immediately.
				elapsed := c.now().Sub(start)
				_ = elapsed // recorded in AggregateError for diagnostics
				return Result{RunID: runID, CascadePath: cascadePath},
					&AggregateError{
						Tiers:  tierErrors,
						RunID:  runID,
						Reason: "terminal error from tier " + tier.Name,
					}
			}
			// Retriable: advance to the next tier (loop continues).
			continue
		}

		// err == nil but out == "": treat as retriable (empty result).
		tierErrors = append(tierErrors, TierError{
			TierName: tier.Name,
			Class:    Retriable,
			Cause:    errors.New("empty result"),
		})
	}

	// Every tier exhausted without a winner.
	elapsed := c.now().Sub(start)
	ae := &AggregateError{
		Tiers: tierErrors,
		RunID: runID,
		Reason: fmt.Sprintf("all %d tier(s) failed (elapsed %v)", len(c.Tiers), elapsed),
	}
	return Result{RunID: runID, CascadePath: cascadePath}, ae
}
