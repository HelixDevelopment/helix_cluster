// Package runner provides a parallel test-suite runner with bounded concurrency
// and deterministic result collection.
package runner

import "time"

// Result is the outcome of a single Suite execution.
type Result struct {
	Passed   bool
	Duration time.Duration
	Err      error
}

// SuiteResult couples a Suite name with its execution Result.
type SuiteResult struct {
	Name     string
	Passed   bool
	Duration time.Duration
	Err      error
}

// Report is the aggregated output from a Run call.
// Suites is sorted by Name for determinism.
type Report struct {
	Total       int
	Passed      int
	Failed      int
	Suites      []SuiteResult
	MaxDuration time.Duration
}
