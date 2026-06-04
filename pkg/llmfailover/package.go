// Package llmfailover provides an error-classified LLM failover taxonomy and a
// sandbox scheduling-hint contract for Helix Cluster OS.
//
// # Error classification
//
// Classify(err) maps an arbitrary error to one of the named ErrorClass values
// using caller-supplied predicate functions (Classifier.Predicates). This keeps
// the package free of any dependency on a concrete LLM provider while still
// providing deterministic, testable behaviour.
//
// FallbackPath(class) returns the DETERMINISTIC ordered slice of FallbackAction
// values that should be attempted for the given class.  The four primary classes
// (RateLimited, ServerError, Timeout, AuthError / ContentFiltered) map to
// DISTINCT paths so that swapping any two entries is immediately detectable by
// TestFallbackPathPerClass.
//
// # Scheduling hints
//
// SandboxJob carries scheduling hints (Priority, Preemptable, Flavor).
// Place(job, scheduler) delegates to an injected Scheduler seam and validates
// that the returned Placement carries the hints unchanged.
//
// # Observability
//
// Every top-level operation (Classify, FallbackPath, Place) accepts a RunID
// string that is embedded in the returned record so callers can correlate events.
package llmfailover

import (
	"errors"
	"fmt"
	"time"
)

// ---------------------------------------------------------------------------
// Error classification
// ---------------------------------------------------------------------------

// ErrorClass is the canonical taxonomy of LLM-provider errors.
type ErrorClass int

const (
	// Unknown is the default class when no predicate matches.
	Unknown ErrorClass = iota
	// RateLimited indicates the provider is throttling requests.
	RateLimited
	// ServerError indicates a transient 5xx-style failure on the provider side.
	ServerError
	// Timeout indicates the request exceeded the provider's response deadline.
	Timeout
	// AuthError indicates an authentication or authorisation failure (terminal).
	AuthError
	// ContentFiltered indicates the request was rejected by content policy (terminal).
	ContentFiltered
)

// String returns the human-readable name of the class.
func (c ErrorClass) String() string {
	switch c {
	case RateLimited:
		return "RateLimited"
	case ServerError:
		return "ServerError"
	case Timeout:
		return "Timeout"
	case AuthError:
		return "AuthError"
	case ContentFiltered:
		return "ContentFiltered"
	default:
		return "Unknown"
	}
}

// Sentinel errors that can be used in place of (or alongside) custom predicates.
var (
	ErrRateLimited     = errors.New("llmfailover: rate limited")
	ErrServerError     = errors.New("llmfailover: server error")
	ErrTimeout         = errors.New("llmfailover: timeout")
	ErrAuthError       = errors.New("llmfailover: auth error")
	ErrContentFiltered = errors.New("llmfailover: content filtered")

	// ErrNoCapability is returned by Place when the injected Scheduler is nil
	// or reports that it cannot honour the request.
	ErrNoCapability = errors.New("llmfailover: scheduler capability absent")
)

// ClassifyResult is the record emitted by a Classify call.
type ClassifyResult struct {
	RunID     string
	Class     ErrorClass
	Err       error
	Timestamp time.Time
}

// FallbackAction names a single step in a recovery sequence.
type FallbackAction string

const (
	ActionRetryAfterDelay  FallbackAction = "retry_after_delay"
	ActionSwitchEndpoint   FallbackAction = "switch_endpoint"
	ActionReduceTokenCount FallbackAction = "reduce_token_count" //gosec:disable G101 -- enum label for an LLM recovery step, not a credential ("token" = LLM context token)
	ActionQueueForLater    FallbackAction = "queue_for_later"
	ActionEscalateAlert    FallbackAction = "escalate_alert"
	ActionFallbackModel    FallbackAction = "fallback_model"
	ActionAbort            FallbackAction = "abort"
)

// FallbackPathResult is the record emitted by a FallbackPath call.
type FallbackPathResult struct {
	RunID   string
	Class   ErrorClass
	Actions []FallbackAction
}

// fallbackPaths maps each ErrorClass to its DETERMINISTIC ordered action slice.
//
// MUTATION GUARD — TestFallbackPathPerClass pins every entry below.
// Swapping any two class→path entries (e.g. RateLimited ↔ ServerError) causes
// at least one sub-case in TestFallbackPathPerClass to receive the wrong path
// and fail on the first ActionFallbackModel vs ActionEscalateAlert assertion.
var fallbackPaths = map[ErrorClass][]FallbackAction{
	// RateLimited: back off, queue, switch to a fallback model.
	// Guard: TestFallbackPathPerClass asserts [ActionRetryAfterDelay, ActionQueueForLater, ActionFallbackModel].
	RateLimited: {ActionRetryAfterDelay, ActionQueueForLater, ActionFallbackModel},

	// ServerError: try again, switch endpoint, escalate if still broken.
	// Guard: TestFallbackPathPerClass asserts [ActionRetryAfterDelay, ActionSwitchEndpoint, ActionEscalateAlert].
	ServerError: {ActionRetryAfterDelay, ActionSwitchEndpoint, ActionEscalateAlert},

	// Timeout: reduce the request size, retry, then switch endpoint.
	// Guard: TestFallbackPathPerClass asserts [ActionReduceTokenCount, ActionRetryAfterDelay, ActionSwitchEndpoint].
	Timeout: {ActionReduceTokenCount, ActionRetryAfterDelay, ActionSwitchEndpoint},

	// AuthError: terminal — no retry makes sense; abort immediately.
	// Guard: TestFallbackPathPerClass asserts [ActionAbort].
	AuthError: {ActionAbort},

	// ContentFiltered: terminal — abort immediately.
	// Guard: TestFallbackPathPerClass asserts [ActionAbort].
	ContentFiltered: {ActionAbort},

	// Unknown: conservative — queue and alert.
	Unknown: {ActionQueueForLater, ActionEscalateAlert},
}

// Predicate is a function that decides whether an error belongs to its
// associated ErrorClass.  Return true if the error matches.
type Predicate func(err error) bool

// Classifier holds the configuration for error classification.
// Predicates is evaluated in declaration order; the first match wins.
// If no predicate matches, the sentinel-error table is tried next.
// If that also yields no match, Unknown is returned.
type Classifier struct {
	// Predicates maps ErrorClass values to caller-supplied match functions.
	// Only classes present in the map are consulted.
	Predicates map[ErrorClass]Predicate
	// Now is an injected clock (seam for deterministic tests).
	// If nil, time.Now is used.
	Now func() time.Time
}

// now returns the current time using the injected clock or the wall clock.
func (c *Classifier) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Classify maps err to an ErrorClass and returns a ClassifyResult carrying runID.
// Evaluation order: injected predicates (in deterministic class order) → sentinel
// errors (errors.Is) → Unknown.
func (c *Classifier) Classify(runID string, err error) ClassifyResult {
	ts := c.now()

	// Evaluate injected predicates in a fixed, deterministic order so that the
	// result is not sensitive to map-iteration order.
	orderedClasses := []ErrorClass{RateLimited, ServerError, Timeout, AuthError, ContentFiltered}
	for _, class := range orderedClasses {
		if pred, ok := c.Predicates[class]; ok && pred(err) {
			return ClassifyResult{RunID: runID, Class: class, Err: err, Timestamp: ts}
		}
	}

	// Fall back to sentinel errors.
	switch {
	case errors.Is(err, ErrRateLimited):
		return ClassifyResult{RunID: runID, Class: RateLimited, Err: err, Timestamp: ts}
	case errors.Is(err, ErrServerError):
		return ClassifyResult{RunID: runID, Class: ServerError, Err: err, Timestamp: ts}
	case errors.Is(err, ErrTimeout):
		return ClassifyResult{RunID: runID, Class: Timeout, Err: err, Timestamp: ts}
	case errors.Is(err, ErrAuthError):
		return ClassifyResult{RunID: runID, Class: AuthError, Err: err, Timestamp: ts}
	case errors.Is(err, ErrContentFiltered):
		return ClassifyResult{RunID: runID, Class: ContentFiltered, Err: err, Timestamp: ts}
	}

	return ClassifyResult{RunID: runID, Class: Unknown, Err: err, Timestamp: ts}
}

// FallbackPath returns the deterministic ordered slice of FallbackActions for
// class and records the lookup under runID.  The returned slice is a copy so
// callers may safely mutate it.
func FallbackPath(runID string, class ErrorClass) FallbackPathResult {
	path, ok := fallbackPaths[class]
	if !ok {
		path = fallbackPaths[Unknown]
	}
	// Return a defensive copy.
	out := make([]FallbackAction, len(path))
	copy(out, path)
	return FallbackPathResult{RunID: runID, Class: class, Actions: out}
}

// ---------------------------------------------------------------------------
// Scheduling-hint contract
// ---------------------------------------------------------------------------

// SandboxJob carries scheduling hints for sandbox placement.
type SandboxJob struct {
	// ID is the caller-assigned job identifier.
	ID string
	// Priority is a higher-is-more-urgent integer hint.
	Priority int
	// Preemptable indicates the job may be evicted to make room for higher-priority work.
	Preemptable bool
	// Flavor is an opaque string hint (e.g. "gpu-large", "cpu-small").
	Flavor string
}

// Placement is the scheduler's response to a Place request.
type Placement struct {
	// RunID is the correlation identifier supplied by the caller.
	RunID string
	// JobID mirrors SandboxJob.ID.
	JobID string
	// NodeID is the scheduler-assigned destination node (opaque string).
	NodeID string
	// Priority is the priority reflected back from the job (must equal SandboxJob.Priority).
	Priority int
	// Preemptable is the preemptable flag reflected back from the job.
	Preemptable bool
	// Flavor is the flavor reflected back from the job.
	Flavor string
}

// Scheduler is the seam for the sandbox placement engine.
// Implementations MUST reflect the job hints (Priority, Preemptable, Flavor)
// unchanged in the returned Placement.  Return ErrNoCapability when the
// scheduler genuinely cannot service the request.
type Scheduler interface {
	Schedule(runID string, job SandboxJob) (Placement, error)
}

// Place delegates job placement to the injected scheduler seam and verifies
// that the returned Placement carries the job hints through unchanged.
// Returns ErrNoCapability when scheduler is nil.
func Place(runID string, job SandboxJob, scheduler Scheduler) (Placement, error) {
	if scheduler == nil {
		return Placement{}, fmt.Errorf("%w: no scheduler injected for job %s", ErrNoCapability, job.ID)
	}
	p, err := scheduler.Schedule(runID, job)
	if err != nil {
		return Placement{}, err
	}
	// Validate hint pass-through so callers can trust the contract.
	if p.Priority != job.Priority {
		return Placement{}, fmt.Errorf(
			"llmfailover: scheduler contract violation: priority not reflected (got %d, want %d)",
			p.Priority, job.Priority,
		)
	}
	if p.Preemptable != job.Preemptable {
		return Placement{}, fmt.Errorf(
			"llmfailover: scheduler contract violation: preemptable not reflected (got %v, want %v)",
			p.Preemptable, job.Preemptable,
		)
	}
	if p.Flavor != job.Flavor {
		return Placement{}, fmt.Errorf(
			"llmfailover: scheduler contract violation: flavor not reflected (got %q, want %q)",
			p.Flavor, job.Flavor,
		)
	}
	return p, nil
}
