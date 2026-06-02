// Package modelretry provides status-code-driven retry-with-fallback across an
// ordered list of models.
//
// This package is intentionally distinct from pkg/fallbackchain: fallbackchain
// is empty-result driven (it falls through when a step yields no usable result),
// whereas modelretry is HTTP-status driven. The model call is an injected
// callback returning an HTTP-like status code plus a body. Run tries the primary
// model first; on a RETRYABLE status (429 rate-limited, or any 5xx) it falls
// through to the NEXT model in order; on a 200 it stops and returns that result
// as the winner; on a non-retryable 4xx (e.g. 400) it ABORTS immediately without
// trying any remaining model and surfaces that status.
//
// The package is pure, deterministic, and free of any I/O: callers inject the
// attempt callback. It is self-contained and re-wraps nothing.
package modelretry

import (
	"errors"
	"fmt"
)

// Attempt invokes one model and reports the resulting status code and body.
//
// status uses HTTP-style semantics:
//   - 200          -> success; the model served the request (the winner).
//   - 429          -> retryable (rate limited); fall through to the next model.
//   - 500..599     -> retryable (server error); fall through to the next model.
//   - any other 4xx (e.g. 400) -> non-retryable; abort without trying further.
type Attempt func(model string) (status int, body string)

// AttemptRecord is one entry in a Run trace: the model that was tried, the
// status it returned, and whether that status was classified as retryable.
type AttemptRecord struct {
	Model     string
	Status    int
	Retryable bool
}

// Result is the outcome of a successful Run.
type Result struct {
	// Model is the model that served the request (returned 200), or, when Run
	// returns an error, the last model attempted.
	Model string
	// Body is the body returned by the serving model.
	Body string
	// Status is the status returned by the serving (or aborting) model.
	Status int
	// Trace records every attempt in the order they were made.
	Trace []AttemptRecord
}

// ErrAllFailed is returned (wrapped) when every model in the ordered list
// returned a retryable status and none served the request.
var ErrAllFailed = errors.New("modelretry: all models returned a retryable failure")

// ErrNoModels is returned when Run is called with an empty model list.
var ErrNoModels = errors.New("modelretry: no models supplied")

// ErrNonRetryable is returned (wrapped) when a model returns a non-retryable
// status (a 4xx other than 429), which aborts the run immediately.
var ErrNonRetryable = errors.New("modelretry: non-retryable status aborted run")

// IsRetryable reports whether a status code should trigger fallthrough to the
// next model. The retryable set is exactly {429} ∪ {500..599}.
//
// Note that 200 is a success (not retryable), and any other 4xx (e.g. 400, 401,
// 403, 404) is a non-retryable abort.
func IsRetryable(status int) bool {
	if status == 429 {
		return true
	}
	return status >= 500 && status <= 599
}

// Run tries each model in order using attemptFn.
//
//   - On a 200 it returns immediately with that model as the winner (nil error).
//   - On a retryable status (429 or 5xx) it records the attempt and falls
//     through to the next model.
//   - On a non-retryable status (a 4xx other than 429) it aborts immediately,
//     records the attempt, and returns a Result plus an error wrapping
//     ErrNonRetryable. Remaining models are NOT tried.
//   - If every model returns a retryable status, it returns the full trace plus
//     an error wrapping ErrAllFailed.
//
// The returned Result always carries the full Trace gathered so far, even on
// error, so callers can inspect the attempt history.
func Run(models []string, attemptFn Attempt) (Result, error) {
	if len(models) == 0 {
		return Result{}, ErrNoModels
	}
	if attemptFn == nil {
		return Result{}, errors.New("modelretry: nil attemptFn")
	}

	trace := make([]AttemptRecord, 0, len(models))
	var lastStatus int
	var lastModel string
	var lastBody string

	for _, model := range models {
		status, body := attemptFn(model)
		retryable := IsRetryable(status)
		trace = append(trace, AttemptRecord{Model: model, Status: status, Retryable: retryable})
		lastStatus, lastModel, lastBody = status, model, body

		if status == 200 {
			return Result{Model: model, Body: body, Status: status, Trace: trace}, nil
		}
		if !retryable {
			// Non-retryable 4xx (e.g. 400): abort without trying further models.
			return Result{Model: model, Body: body, Status: status, Trace: trace},
				fmt.Errorf("%w: model %q returned status %d", ErrNonRetryable, model, status)
		}
		// Retryable: fall through to the next model.
	}

	// Every model returned a retryable status.
	return Result{Model: lastModel, Body: lastBody, Status: lastStatus, Trace: trace},
		fmt.Errorf("%w: %d model(s) tried", ErrAllFailed, len(trace))
}
