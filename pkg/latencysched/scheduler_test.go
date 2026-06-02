package latencysched

import "testing"

const runUUID = "latencysched-run-7f3a2c1e-9b04-4d2a-8a11-c0ffee001500"

// minRemoteLatency derives the expected min remote latency from the inputs so
// the assertion is not a hardcoded magic number that could mask a max-bug.
func minRemoteLatency(ps []Provider) int {
	min := -1
	for _, p := range ps {
		if p.Locality != Remote || !p.Available {
			continue
		}
		if min < 0 || p.LatencyMs < min {
			min = p.LatencyMs
		}
	}
	return min
}

// TestSelectLowestRemote covers closure clause 1: with two remotes and no
// local, the lower-latency remote is chosen and the recorded latency equals the
// minimum remote latency derived from the inputs.
//
// Mutation: replacing min with max in minByLatencyThenID makes the chosen ID
// "remote-fast"/latency 12 flip to "remote-slow"/latency 90 -> this test FAILS.
func TestSelectLowestRemote(t *testing.T) {
	s := New()
	providers := []Provider{
		{ID: "remote-slow", Locality: Remote, LatencyMs: 90, Available: true},
		{ID: "remote-fast", Locality: Remote, LatencyMs: 12, Available: true},
	}
	wantLatency := minRemoteLatency(providers) // derived, = 12

	d, err := s.Select(providers)
	if err != nil {
		t.Fatalf("[%s] unexpected error: %v", runUUID, err)
	}
	t.Logf("[%s] before: candidates remote-slow=90ms remote-fast=12ms (no local); min-remote=%dms",
		runUUID, wantLatency)
	t.Logf("[%s] after: chosen=%s locality=%s recordedLatency=%dms",
		runUUID, d.ChosenID, d.Locality, d.LatencyMs)

	if d.ChosenID != "remote-fast" {
		t.Fatalf("[%s] expected lowest-latency remote 'remote-fast', got %q", runUUID, d.ChosenID)
	}
	if d.LatencyMs != wantLatency {
		t.Fatalf("[%s] recorded latency %d != min remote latency %d", runUUID, d.LatencyMs, wantLatency)
	}
	if d.Locality != Remote {
		t.Fatalf("[%s] expected Remote locality, got %s", runUUID, d.Locality)
	}
}

// TestLocalPreference covers closure clause 2: a LOCAL provider is chosen over
// strictly lower-latency remotes once it is added. before -> only remotes (a
// remote is chosen); after -> add local (local is chosen despite higher ms).
//
// Mutation: dropping the local-preference branch (so the function falls through
// to pure min-latency over all providers) makes "remote-fast" (5ms) win over
// "local-a" (40ms) -> this test FAILS on the "after" assertion.
func TestLocalPreference(t *testing.T) {
	s := New()

	// before: only remotes available.
	remotesOnly := []Provider{
		{ID: "remote-fast", Locality: Remote, LatencyMs: 5, Available: true},
		{ID: "remote-mid", Locality: Remote, LatencyMs: 20, Available: true},
	}
	dBefore, err := s.Select(remotesOnly)
	if err != nil {
		t.Fatalf("[%s] unexpected error (before): %v", runUUID, err)
	}
	t.Logf("[%s] before: only remotes -> chosen=%s locality=%s latency=%dms",
		runUUID, dBefore.ChosenID, dBefore.Locality, dBefore.LatencyMs)
	if dBefore.Locality != Remote || dBefore.ChosenID != "remote-fast" {
		t.Fatalf("[%s] before: expected remote-fast, got %s/%s",
			runUUID, dBefore.ChosenID, dBefore.Locality)
	}

	// after: add a higher-latency local; it must win regardless.
	withLocal := append([]Provider{
		{ID: "local-a", Locality: Local, LatencyMs: 40, Available: true},
	}, remotesOnly...)
	dAfter, err := s.Select(withLocal)
	if err != nil {
		t.Fatalf("[%s] unexpected error (after): %v", runUUID, err)
	}
	t.Logf("[%s] after: add local-a=40ms -> chosen=%s locality=%s latency=%dms",
		runUUID, dAfter.ChosenID, dAfter.Locality, dAfter.LatencyMs)

	if dAfter.Locality != Local {
		t.Fatalf("[%s] after: expected Local to be preferred, got locality %s (id=%s)",
			runUUID, dAfter.Locality, dAfter.ChosenID)
	}
	if dAfter.ChosenID != "local-a" {
		t.Fatalf("[%s] after: expected 'local-a', got %q", runUUID, dAfter.ChosenID)
	}
	if dAfter.LatencyMs != 40 {
		t.Fatalf("[%s] after: expected recorded local latency 40, got %d", runUUID, dAfter.LatencyMs)
	}
	// The decision must have actually changed from before -> after, proving the
	// local-preference branch overrode the lower-latency remote.
	if dAfter.ChosenID == dBefore.ChosenID {
		t.Fatalf("[%s] expected local to override remote choice; both chose %q",
			runUUID, dAfter.ChosenID)
	}
}

// TestDeterministicTieBreak covers closure clause 3: two remotes with EQUAL
// latency resolve to the smaller-ID provider, stably and independent of input
// order.
//
// Mutation: removing the "ps[i].ID < ps[j].ID" tie-break (so order depends on
// input/sort instability) makes the order-flipped run return "remote-z" -> the
// cross-order equality assertion FAILS.
func TestDeterministicTieBreak(t *testing.T) {
	s := New()
	order1 := []Provider{
		{ID: "remote-z", Locality: Remote, LatencyMs: 30, Available: true},
		{ID: "remote-a", Locality: Remote, LatencyMs: 30, Available: true},
	}
	order2 := []Provider{
		{ID: "remote-a", Locality: Remote, LatencyMs: 30, Available: true},
		{ID: "remote-z", Locality: Remote, LatencyMs: 30, Available: true},
	}

	d1, err := s.Select(order1)
	if err != nil {
		t.Fatalf("[%s] unexpected error (order1): %v", runUUID, err)
	}
	d2, err := s.Select(order2)
	if err != nil {
		t.Fatalf("[%s] unexpected error (order2): %v", runUUID, err)
	}
	t.Logf("[%s] before: equal-latency remotes {remote-z,remote-a}@30ms, two input orderings",
		runUUID)
	t.Logf("[%s] after: order1 chose=%s ; order2 chose=%s", runUUID, d1.ChosenID, d2.ChosenID)

	if d1.ChosenID != "remote-a" {
		t.Fatalf("[%s] expected stable tie-break to 'remote-a', got %q", runUUID, d1.ChosenID)
	}
	if d1.ChosenID != d2.ChosenID {
		t.Fatalf("[%s] tie-break not order-independent: %q vs %q",
			runUUID, d1.ChosenID, d2.ChosenID)
	}
}

// TestNoProvider proves the empty/unavailable case returns ErrNoProvider rather
// than a bogus zero decision.
//
// Mutation: returning a zero Decision with nil error instead of ErrNoProvider
// makes this FAIL.
func TestNoProvider(t *testing.T) {
	s := New()
	_, err := s.Select([]Provider{
		{ID: "down", Locality: Remote, LatencyMs: 1, Available: false},
	})
	t.Logf("[%s] after: all-unavailable -> err=%v", runUUID, err)
	if err == nil {
		t.Fatalf("[%s] expected ErrNoProvider for all-unavailable input", runUUID)
	}
}
