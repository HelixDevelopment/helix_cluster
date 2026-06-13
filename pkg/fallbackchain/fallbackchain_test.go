package fallbackchain

import (
	"errors"
	"reflect"
	"testing"
)

const runUUID = "fbc-7f3a91d2-5c4e-4b8a-9e21-0d6f4a2c1b88"

// recordingAttempt builds an Attempt that returns outputs from a per-provider
// map and records the order in which providers were invoked. A missing entry
// (or empty-string entry) is treated as an empty completion.
func recordingAttempt(outputs map[string]string, calls *[]string) Attempt {
	return func(providerID string) (string, error) {
		*calls = append(*calls, providerID)
		return outputs[providerID], nil
	}
}

// Mutation: returning the FIRST (empty) result instead of continuing to the
// fallback breaks this test — Output/Provider would be the primary's empty
// value rather than the fallback's non-empty value.
func TestPrimaryEmptyFallbackWins(t *testing.T) {
	var calls []string
	outputs := map[string]string{
		"primary":  "",              // primary returns an empty completion
		"backup-a": "",              // first fallback also empty
		"backup-b": "ANSWER-FROM-B", // second fallback has the real answer
	}
	chain := Chain{Providers: []string{"primary", "backup-a", "backup-b"}}

	t.Logf("run=%s before: primary completion=%q (empty=%v)", runUUID, outputs["primary"], outputs["primary"] == "")

	res, err := Run(chain, recordingAttempt(outputs, &calls))
	if err != nil {
		t.Fatalf("run=%s unexpected error: %v", runUUID, err)
	}

	t.Logf("run=%s after: winner=%q output=%q trace=%v calls=%v", runUUID, res.Provider, res.Output, res.Trace, calls)

	if res.Provider != "backup-b" {
		t.Fatalf("run=%s expected winning provider backup-b, got %q", runUUID, res.Provider)
	}
	if res.Output != "ANSWER-FROM-B" {
		t.Fatalf("run=%s expected output ANSWER-FROM-B, got %q", runUUID, res.Output)
	}
	// Trace must show primary first, then the fallbacks, in order, ending at
	// the winner.
	wantTrace := []string{"primary", "backup-a", "backup-b"}
	if !reflect.DeepEqual(res.Trace, wantTrace) {
		t.Fatalf("run=%s expected trace %v, got %v", runUUID, wantTrace, res.Trace)
	}
	// The primary must have been attempted BEFORE the winner (order proof).
	if calls[0] != "primary" || calls[len(calls)-1] != "backup-b" {
		t.Fatalf("run=%s expected attempts to start at primary and end at backup-b, got %v", runUUID, calls)
	}
}

// Mutation: removing dedup makes the duplicated provider get attempted twice,
// so the trace and call list would each contain "primary" twice and this test
// would fail.
func TestDedupAttemptsProviderOnce(t *testing.T) {
	var calls []string
	outputs := map[string]string{
		"primary": "", // always empty so we exhaust the chain
		"backup":  "FROM-BACKUP",
	}
	// "primary" listed twice; must be tried only once.
	chain := Chain{Providers: []string{"primary", "primary", "backup"}}

	t.Logf("run=%s before: chain providers=%v", runUUID, chain.Providers)

	res, err := Run(chain, recordingAttempt(outputs, &calls))
	if err != nil {
		t.Fatalf("run=%s unexpected error: %v", runUUID, err)
	}

	t.Logf("run=%s after: winner=%q trace=%v calls=%v", runUUID, res.Provider, res.Trace, calls)

	// primary must appear exactly once across both the trace and the actual
	// invocations.
	if got := countOccurrences(res.Trace, "primary"); got != 1 {
		t.Fatalf("run=%s expected primary once in trace, got %d (trace=%v)", runUUID, got, res.Trace)
	}
	if got := countOccurrences(calls, "primary"); got != 1 {
		t.Fatalf("run=%s expected primary attempted once, got %d (calls=%v)", runUUID, got, calls)
	}
	wantTrace := []string{"primary", "backup"}
	if !reflect.DeepEqual(res.Trace, wantTrace) {
		t.Fatalf("run=%s expected deduped trace %v, got %v", runUUID, wantTrace, res.Trace)
	}
	if res.Provider != "backup" {
		t.Fatalf("run=%s expected winner backup, got %q", runUUID, res.Provider)
	}
}

// Mutation: ignoring the cap lets more than Cap providers be attempted, so the
// call count / trace length would exceed Cap and this test would fail. It also
// guards against an infinite-loop / beyond-cap regression.
func TestCapBoundsAttemptsAndAllEmpty(t *testing.T) {
	var calls []string
	// Every provider returns empty -> chain is exhausted.
	outputs := map[string]string{} // all lookups yield "" (empty)
	chain := Chain{
		Providers: []string{"p1", "p2", "p3", "p4", "p5"},
		Cap:       2,
	}

	t.Logf("run=%s before: cap=%d providers=%v", runUUID, chain.Cap, chain.Providers)

	res, err := Run(chain, recordingAttempt(outputs, &calls))

	t.Logf("run=%s after: err=%v trace=%v calls=%v", runUUID, err, res.Trace, calls)

	if !errors.Is(err, ErrAllEmpty) {
		t.Fatalf("run=%s expected ErrAllEmpty, got %v", runUUID, err)
	}
	// At most Cap attempts: exactly 2 here, and never beyond cap.
	if len(calls) != chain.Cap {
		t.Fatalf("run=%s expected exactly %d attempts, got %d (calls=%v)", runUUID, chain.Cap, len(calls), calls)
	}
	if len(res.Trace) != chain.Cap {
		t.Fatalf("run=%s expected trace length %d, got %d (trace=%v)", runUUID, chain.Cap, len(res.Trace), res.Trace)
	}
	// Providers beyond the cap must never have been touched.
	for _, beyond := range []string{"p3", "p4", "p5"} {
		if countOccurrences(calls, beyond) != 0 {
			t.Fatalf("run=%s provider %q was attempted beyond the cap (calls=%v)", runUUID, beyond, calls)
		}
	}
	wantTrace := []string{"p1", "p2"}
	if !reflect.DeepEqual(res.Trace, wantTrace) {
		t.Fatalf("run=%s expected capped trace %v, got %v", runUUID, wantTrace, res.Trace)
	}
	if res.Output != "" || res.Provider != "" {
		t.Fatalf("run=%s expected empty winner on all-empty, got provider=%q output=%q", runUUID, res.Provider, res.Output)
	}
}

// Supporting-behavior coverage: an errored attempt is treated as empty and
// falls through to the next provider.
//
// Mutation: treating a non-nil error as a win (returning its output) would make
// the winner "errprov" and fail this test.
func TestErroredAttemptFallsThrough(t *testing.T) {
	var calls []string
	attempt := func(providerID string) (string, error) {
		calls = append(calls, providerID)
		switch providerID {
		case "errprov":
			// Non-empty output but with an error: must NOT be treated as a win.
			return "SHOULD-BE-IGNORED", errors.New("provider failed")
		case "good":
			return "REAL", nil
		}
		return "", nil
	}
	chain := Chain{Providers: []string{"errprov", "good"}}

	t.Logf("run=%s before: errored provider returns output+error", runUUID)
	res, err := Run(chain, attempt)
	t.Logf("run=%s after: winner=%q output=%q trace=%v", runUUID, res.Provider, res.Output, res.Trace)

	if err != nil {
		t.Fatalf("run=%s unexpected error: %v", runUUID, err)
	}
	if res.Provider != "good" || res.Output != "REAL" {
		t.Fatalf("run=%s expected good/REAL to win, got %q/%q", runUUID, res.Provider, res.Output)
	}
	if !reflect.DeepEqual(res.Trace, []string{"errprov", "good"}) {
		t.Fatalf("run=%s expected trace [errprov good], got %v", runUUID, res.Trace)
	}
}

// Supporting-behavior coverage: an empty/deduped-to-nothing chain reports
// ErrEmptyChain rather than ErrAllEmpty (nothing was ever attempted).
func TestEmptyChain(t *testing.T) {
	var calls []string
	chain := Chain{Providers: nil}
	t.Logf("run=%s before: empty provider list", runUUID)
	res, err := Run(chain, recordingAttempt(map[string]string{}, &calls))
	t.Logf("run=%s after: err=%v calls=%v", runUUID, err, calls)

	if !errors.Is(err, ErrEmptyChain) {
		t.Fatalf("run=%s expected ErrEmptyChain, got %v", runUUID, err)
	}
	if len(calls) != 0 {
		t.Fatalf("run=%s expected zero attempts on empty chain, got %v", runUUID, calls)
	}
	if len(res.Trace) != 0 {
		t.Fatalf("run=%s expected empty trace, got %v", runUUID, res.Trace)
	}
}

func countOccurrences(xs []string, target string) int {
	n := 0
	for _, x := range xs {
		if x == target {
			n++
		}
	}
	return n
}
