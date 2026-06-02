// Package fallbackchain implements per-task ordered fallback chains with
// deduplication, an attempt cap, and empty-result handling.
//
// A task carries an ordered list of provider/model IDs to try. Run executes
// them in order: for each provider it invokes the injected attempt function;
// if that attempt yields an EMPTY result (or an error), it falls through to the
// NEXT provider; the FIRST non-empty result wins. Duplicate providers in the
// chain are deduplicated (each is tried at most once). An attempt cap bounds
// the total number of tries. If every attempt is empty (or the chain is
// exhausted), a typed ErrAllEmpty is returned together with the attempt trace.
//
// This package is pure, deterministic, and self-contained. The actual model or
// provider call is injected as a callback; this package never performs network,
// host, or LLM calls of its own.
package fallbackchain

import "errors"

// ErrAllEmpty is returned by Run when every attempted provider produced an
// empty result (and no provider returned a non-empty winning output).
var ErrAllEmpty = errors.New("fallbackchain: all attempts returned empty")

// ErrEmptyChain is returned by Run when the chain has no providers to try
// (either it was declared empty, or its cap reduces the effective attempt
// budget to zero).
var ErrEmptyChain = errors.New("fallbackchain: chain has no providers to attempt")

// Attempt is the injected per-provider callback. Given a providerID it returns
// the produced result and an optional error. An empty result string OR a
// non-nil error both cause Run to fall through to the next provider.
type Attempt func(providerID string) (result string, err error)

// Chain describes a task's ordered fallback preference.
//
//   - Providers is the ordered list of provider/model IDs to try.
//   - Cap bounds the total number of attempts actually made. A Cap <= 0 means
//     "no cap" (try every distinct provider).
type Chain struct {
	Providers []string
	Cap       int
}

// Result is the outcome of a Run.
//
//   - Provider is the ID of the winning provider (the first to return a
//     non-empty result). It is empty when no provider won.
//   - Output is the winning non-empty result. It is empty when no provider won.
//   - Trace lists the providers actually attempted, in order, after dedup and
//     after the cap was applied. Trace ends at the winning provider (inclusive)
//     when there is a winner.
type Result struct {
	Provider string
	Output   string
	Trace    []string
}

// Run executes the fallback chain against attemptFn.
//
// Semantics:
//   - Providers are tried in the chain's declared order.
//   - Duplicate provider IDs are deduplicated; each distinct provider is tried
//     at most once (the first occurrence position is honored).
//   - At most Cap attempts are made (when Cap > 0). The cap counts attempts, so
//     no provider beyond the cap is invoked.
//   - The first provider whose attempt yields a non-empty result and a nil
//     error wins; Run returns immediately with that Result and a nil error.
//   - An empty result or a non-nil error causes fall-through to the next
//     provider.
//   - If the effective (deduped, capped) provider list is empty, ErrEmptyChain
//     is returned.
//   - If providers were attempted but all were empty/errored, ErrAllEmpty is
//     returned together with the full Trace of what was attempted.
func Run(chain Chain, attemptFn Attempt) (Result, error) {
	if attemptFn == nil {
		return Result{}, errors.New("fallbackchain: nil attempt function")
	}

	// Deduplicate while preserving first-seen order.
	seen := make(map[string]struct{}, len(chain.Providers))
	ordered := make([]string, 0, len(chain.Providers))
	for _, p := range chain.Providers {
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		ordered = append(ordered, p)
	}

	if len(ordered) == 0 {
		return Result{Trace: []string{}}, ErrEmptyChain
	}

	trace := make([]string, 0, len(ordered))
	for _, provider := range ordered {
		// Enforce the attempt cap BEFORE making the attempt so that no
		// provider beyond the cap is ever invoked.
		if chain.Cap > 0 && len(trace) >= chain.Cap {
			break
		}

		trace = append(trace, provider)
		out, err := attemptFn(provider)
		if err == nil && out != "" {
			return Result{Provider: provider, Output: out, Trace: trace}, nil
		}
		// Empty or errored: fall through to the next provider.
	}

	if len(trace) == 0 {
		// Cap reduced the effective budget to zero.
		return Result{Trace: trace}, ErrEmptyChain
	}
	return Result{Trace: trace}, ErrAllEmpty
}
