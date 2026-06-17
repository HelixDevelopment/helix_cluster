package cloudspot

// Adversarial probes — wave 2 for pkg/cloudspot.
//
// CONTEXT: pkg/cloudspot is a graceful-drain *preemption handler*, not a
// price/bid selection pool. Wave 1 (cloudspot_adversarial_test.go) already
// pinned: zero/far-future Deadline poison, Azure unparseable-NotBefore fallback,
// Azure multi-event first-in-array selection (deterministic), and AWS
// sink-fires-exactly-once-under-race. These wave-2 probes attack DIFFERENT,
// previously-unprobed sink-side seams:
//
//   (E) FIRE-ONCE-ON-FAILURE  — the `sinkFired` latch is set BEFORE fireSink
//       runs, so a sink that FAILS its first fire is never retried: subsequent
//       polls report success (err=nil) with the sink NOT re-driven. This is a
//       LATENT RISK (a transient drain failure permanently looks "done"); these
//       tests PIN the current once-only-even-on-failure behaviour so any future
//       change to it is a deliberate, reviewed decision. (AWS + GCP.)
//
//   (F) NO-DEADLINE-VALIDATION — a notice whose parsed Deadline is already in
//       the PAST is still detected and the sink still fired (the poller performs
//       no deadline sanity check). PINNED sink-side as a CONTRACT: detection is
//       independent of deadline validity; honouring the (possibly past) deadline
//       is the Handler's job, not the poller's.
//
//   (G) CHANNEL-VS-SINK ORDERING — fireOnce delivers the notice to the
//       PreemptionSource channel even when the subsequent sink fire returns an
//       error. PINNED: a Handler wired to the channel still receives the notice
//       (drain is not silently dropped just because the poller's own sink hook
//       erred).
//
// Every assertion is SINK-SIDE (which sink methods ran, in what order, how many
// times, what the channel received), never "no panic". Each test carries a
// timeout guard so it can never hang.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

// awsIMDSAlwaysInterrupting builds an httptest IMDSv2 server that mints a token
// and ALWAYS serves the given interruption body (the instance is permanently in
// its 2-minute window). It is local to wave 2 to avoid coupling to wave-1
// helpers' mutable `interrupted` flag.
func awsIMDSAlwaysInterrupting(t *testing.T, body string) *httptest.Server {
	t.Helper()
	const token = "AQAE-adv2-token=="
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == awsTokenPath:
			if r.Header.Get(awsTokenTTLHeader) == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(token))
		case r.Method == http.MethodGet && r.URL.Path == awsInstanceActionPath:
			if r.Header.Get(awsTokenHeader) != token {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// gcpIMDSPreempted builds an httptest GCE metadata server that requires the
// Metadata-Flavor header and always reports the preempted flag as "TRUE".
func gcpIMDSPreempted(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != gcpPreemptedPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get(gcpMetadataFlavorHeader) != gcpMetadataFlavorValue {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("TRUE"))
	}))
}

// ----------------------------------------------------------------------------
// (E) FIRE-ONCE-ON-FAILURE: a sink that errors on its first fire is NOT retried.
// ----------------------------------------------------------------------------

// TestAdv2_AWSSinkNotRetriedAfterFailedFire PINS the latent once-only-even-on-
// failure contract for AWS. The first Poll fires the sink and Upload FAILS;
// the second Poll detects the same interruption but, because sinkFired is
// already latched, does NOT re-drive the sink.
//
// SINK-SIDE EVIDENCE: across two polls the sink is driven for exactly ONE
// drain→checkpoint→upload sequence (3 calls), and the SECOND poll returns
// err==nil despite the drain never having actually succeeded.
//
// PIN RATIONALE: the latch is set before fireSink runs, so a transient sink
// failure permanently looks "drained". This is a LATENT RISK, not an always-on
// bug (it needs a failing sink, and re-running non-idempotent drain hooks could
// be worse). If the latch is ever moved to fire only on sink SUCCESS, the
// second poll would re-fire the sink (6 calls) and this assertion FAILS —
// forcing a deliberate review.
func TestAdv2_AWSSinkNotRetriedAfterFailedFire(t *testing.T) {
	srv := awsIMDSAlwaysInterrupting(t,
		`{"action":"terminate","time":"2026-06-03T10:02:00Z"}`)
	defer srv.Close()

	sink := &fakeSink{failOn: "upload"}
	p := NewAWSPoller(srv.URL, sink, WithHTTPClient(srv.Client()))

	done := make(chan struct{})
	var err1, err2 error
	var det1, det2 bool
	go func() {
		_, det1, err1 = p.Poll(context.Background())
		_, det2, err2 = p.Poll(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Poll sequence hung")
	}

	// First poll: detected, and the upload failure surfaced as an error.
	if !det1 || err1 == nil {
		t.Fatalf("poll1: detected=%v err=%v; want detected=true, non-nil upload err", det1, err1)
	}
	// Second poll: STILL detected, but reports success (err==nil) even though the
	// drain never actually completed — the latent-risk surface being pinned.
	if !det2 || err2 != nil {
		t.Fatalf("poll2: detected=%v err=%v; want detected=true, err=nil (once-only latch)", det2, err2)
	}

	// The sink ran for exactly ONE full attempt across both polls.
	gotOrder, _ := sink.snapshot()
	if !reflect.DeepEqual(gotOrder, wantSinkSequence) {
		t.Fatalf("sink order across two polls = %v, want exactly one %v "+
			"(once-only-even-on-failure). 6 calls would mean retry-on-failure was "+
			"introduced — review this pin deliberately.", gotOrder, wantSinkSequence)
	}
}

// TestAdv2_GCPSinkNotRetriedAfterFailedFire PINS the same once-only-on-failure
// contract for the GCP poller (independent code path / latch). Drain fails on
// the first fire; the second poll does not re-drive the sink.
func TestAdv2_GCPSinkNotRetriedAfterFailedFire(t *testing.T) {
	srv := gcpIMDSPreempted(t)
	defer srv.Close()

	sink := &fakeSink{failOn: "drain"}
	p := NewGCPPoller(srv.URL, sink, WithHTTPClient(srv.Client()))

	done := make(chan struct{})
	var err1, err2 error
	go func() {
		_, _, err1 = p.Poll(context.Background())
		_, _, err2 = p.Poll(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Poll sequence hung")
	}

	if err1 == nil {
		t.Fatal("poll1: want non-nil error from failing Drain sink")
	}
	if err2 != nil {
		t.Fatalf("poll2: err=%v; want nil (once-only latch suppresses re-fire)", err2)
	}

	// Drain failed first, so the sequence stopped at drain (fireSink stops at the
	// first error) and was never retried: exactly one "drain" recorded.
	gotOrder, _ := sink.snapshot()
	want := []string{"drain"}
	if !reflect.DeepEqual(gotOrder, want) {
		t.Fatalf("sink order across two polls = %v, want %v "+
			"(drain failed once, never retried)", gotOrder, want)
	}
}

// ----------------------------------------------------------------------------
// (F) NO-DEADLINE-VALIDATION: a past deadline is still detected + fired.
// ----------------------------------------------------------------------------

// TestAdv2_AWSPastDeadline_StillDetectedAndFired PINS the contract that the AWS
// poller performs NO deadline sanity check: an instance-action whose `time` is
// already in the PAST is still detected, and the sink is fired with that past
// deadline verbatim. Validating/honouring the deadline is the Handler's job
// (which force-stops on a past deadline — see wave-1 zero-deadline test), NOT
// the poller's; the poller's contract is "detect and report what the provider
// said".
//
// SINK-SIDE: detection true, full drain→checkpoint→upload fired, each handed the
// exact (past) parsed deadline.
func TestAdv2_AWSPastDeadline_StillDetectedAndFired(t *testing.T) {
	past := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	body := `{"action":"terminate","time":"` + past.Format(time.RFC3339) + `"}`
	srv := awsIMDSAlwaysInterrupting(t, body)
	defer srv.Close()

	sink := &fakeSink{}
	p := NewAWSPoller(srv.URL, sink, WithHTTPClient(srv.Client()))

	done := make(chan struct{})
	var notice PreemptionNotice
	var detected bool
	var err error
	go func() {
		notice, detected, err = p.Poll(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Poll hung on a past-deadline notice")
	}

	if err != nil || !detected {
		t.Fatalf("past-deadline poll: detected=%v err=%v; want detected=true err=nil", detected, err)
	}
	if !notice.Deadline.Equal(past) {
		t.Fatalf("notice deadline = %v, want the verbatim past deadline %v", notice.Deadline, past)
	}
	gotOrder, gotDeadlines := sink.snapshot()
	if !reflect.DeepEqual(gotOrder, wantSinkSequence) {
		t.Fatalf("sink order = %v, want %v even for a past deadline", gotOrder, wantSinkSequence)
	}
	for i, d := range gotDeadlines {
		if !d.Equal(past) {
			t.Fatalf("sink step %d deadline = %v, want the past deadline %v passed through unaltered", i, d, past)
		}
	}
}

// ----------------------------------------------------------------------------
// (G) CHANNEL-VS-SINK ORDERING: the channel receives the notice even if the
// sink fire errors, so a Handler wired to the source still drains.
// ----------------------------------------------------------------------------

// TestAdv2_GCPChannelReceivesNoticeDespiteSinkFailure PINS that fireOnce
// delivers the notice to the PreemptionSource channel BEFORE (and regardless of
// whether) the sink fire succeeds. A Handler consuming Notifications() therefore
// still sees the preemption even when the poller's own injected sink hook erred
// — the drain signal is not silently swallowed by a sink failure.
//
// SINK-SIDE: the channel yields a notice with the expected GCP reason, AND the
// failing sink recorded its (failed) drain attempt.
func TestAdv2_GCPChannelReceivesNoticeDespiteSinkFailure(t *testing.T) {
	srv := gcpIMDSPreempted(t)
	defer srv.Close()

	sink := &fakeSink{failOn: "drain"}
	fixedNow := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	p := NewGCPPoller(srv.URL, sink,
		WithHTTPClient(srv.Client()),
		WithClock(func() time.Time { return fixedNow }))

	done := make(chan struct{})
	var err error
	go func() {
		_, _, err = p.Poll(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Poll hung")
	}
	if err == nil {
		t.Fatal("want non-nil error from the failing Drain sink")
	}

	// The notice must already be on the channel despite the sink error.
	select {
	case notice := <-p.Notifications():
		if notice.Reason != "gcp-preempted" {
			t.Fatalf("channel notice reason = %q, want gcp-preempted", notice.Reason)
		}
		want := fixedNow.Add(gcpDefaultLead)
		if !notice.Deadline.Equal(want) {
			t.Fatalf("channel notice deadline = %v, want now+lead %v", notice.Deadline, want)
		}
	default:
		t.Fatal("notice was NOT delivered to the channel when the sink fire failed; " +
			"a Handler wired to this source would never learn of the preemption")
	}

	// And the sink did attempt (and fail) its drain — proving the failure path,
	// not a skipped sink.
	gotOrder, _ := sink.snapshot()
	if !reflect.DeepEqual(gotOrder, []string{"drain"}) {
		t.Fatalf("sink order = %v, want exactly [drain] (failed at first step)", gotOrder)
	}
}

// TestAdv2_GCPClockDeterministic_AcrossRepeatedPolls is a determinism cross-
// check: with an injected fixed clock, the GCP deadline (now+lead) is identical
// across independent pollers / repeated runs — never drifting. This guards the
// total-order/determinism category for the GCP deadline-derivation path (which
// has no provider timestamp, unlike Azure/AWS) sink-side via the channel notice.
func TestAdv2_GCPClockDeterministic_AcrossRepeatedPolls(t *testing.T) {
	srv := gcpIMDSPreempted(t)
	defer srv.Close()

	fixedNow := time.Date(2026, 6, 14, 9, 30, 0, 0, time.UTC)
	want := fixedNow.Add(gcpDefaultLead)

	for i := 0; i < 4; i++ {
		sink := &fakeSink{}
		p := NewGCPPoller(srv.URL, sink,
			WithHTTPClient(srv.Client()),
			WithClock(func() time.Time { return fixedNow }))

		done := make(chan struct{})
		var notice PreemptionNotice
		var detected bool
		var err error
		go func() {
			notice, detected, err = p.Poll(context.Background())
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("iter %d: Poll hung", i)
		}
		if err != nil || !detected {
			t.Fatalf("iter %d: detected=%v err=%v", i, detected, err)
		}
		if !notice.Deadline.Equal(want) {
			t.Fatalf("iter %d: deadline = %v, want a STABLE %v (now+lead)", i, notice.Deadline, want)
		}
	}
}
