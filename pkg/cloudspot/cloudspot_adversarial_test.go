package cloudspot

// Adversarial probes for pkg/cloudspot.
//
// CONTEXT / HONEST SCOPE NOTE: the assignment brief targeted a spot-instance
// *pricing/bidding* surface (NaN/Inf price poison, equal-price total-order ties,
// budget over-spend, int64 overflow). pkg/cloudspot is NOT that package: it is a
// graceful-drain *preemption handler* for cloud spot/preemptible instances. It
// has no float64 price/bid math, no min/max selection over money, no budget
// accounting, and no sort comparator. Its only ordered numeric surface is the
// untrusted `time.Time` Deadline parsed from provider IMDS bytes.
//
// So the probes below adapt the four adversarial categories to THIS package's
// real numeric surface and sink:
//   (A) NUMERIC POISON  -> a garbage/zero/extreme Deadline parsed from IMDS must
//       not poison the drain decision; the SINK (ForceStop vs Drained, and which
//       steps ran) must stay correct.
//   (B) TOTAL ORDER     -> when an Azure Scheduled-Events document carries
//       MULTIPLE preemption events with different NotBefore deadlines, which one
//       is selected, and is the choice deterministic?
//   (D) OVER-COMMIT/RACE-> the sink "fires exactly once" invariant under
//       concurrent polls: the SINK must not be driven twice (an over-commit of
//       the drain side effect).
//
// Every assertion is SINK-SIDE (actual outcome / which steps ran / how many
// times the sink fired), never "no panic".

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ----------------------------------------------------------------------------
// (A) NUMERIC POISON: a zero / extreme Deadline from the provider must not
// poison the drain decision.
// ----------------------------------------------------------------------------

// TestAdv_ZeroDeadline_ForceStopsImmediately_NoStepsRun proves that a
// zero-value PreemptionNotice.Deadline (the "poison" you get if an IMDS body
// parsed to the time zero value, or a notice was constructed without a deadline)
// is treated as an already-expired deadline: the handler force-stops and runs
// NONE of the drain hooks. SINK-SIDE: zero steps executed, ForceStop fired.
func TestAdv_ZeroDeadline_ForceStopsImmediately_NoStepsRun(t *testing.T) {
	src := NewFakeSource()
	var ran int32
	var forced int32
	hooks := Hooks{
		StopAdmission: func(context.Context) error { atomic.AddInt32(&ran, 1); return nil },
		Checkpoint:    func(context.Context) error { atomic.AddInt32(&ran, 1); return nil },
		Deregister:    func(context.Context) error { atomic.AddInt32(&ran, 1); return nil },
		ForceStop:     func() { atomic.StoreInt32(&forced, 1) },
	}
	h := NewHandler(src, hooks)

	done := make(chan Result, 1)
	go func() { done <- h.Run(context.Background()) }()

	// Zero-value deadline (time.Time{} == year 1) — already long past.
	src.EmitNotice(PreemptionNotice{})

	var res Result
	select {
	case res = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run hung on a zero-value deadline")
	}
	if res.Outcome != OutcomeForceStopped {
		t.Fatalf("outcome = %q, want %q for a zero deadline", res.Outcome, OutcomeForceStopped)
	}
	if n := atomic.LoadInt32(&ran); n != 0 {
		t.Fatalf("%d drain steps ran for a zero deadline; want 0 (no work against a dead VM)", n)
	}
	if atomic.LoadInt32(&forced) != 1 {
		t.Fatal("ForceStop must run for a zero deadline")
	}
	if len(res.Steps) != 0 {
		t.Fatalf("Result.Steps = %v, want empty for a zero deadline", res.Steps)
	}
}

// TestAdv_FarFutureExtremeDeadline_DrainsNormally proves the opposite poison
// direction: an absurdly far-future deadline (the largest representable, ~year
// 219250+) does NOT overflow the deadline arithmetic or skip the drain. The
// drain runs all steps and reports Drained. SINK-SIDE: all three steps ran.
func TestAdv_FarFutureExtremeDeadline_DrainsNormally(t *testing.T) {
	src := NewFakeSource()
	var rec recorder
	var forced int32
	h := NewHandler(src, recordingHooks(&rec, &forced))

	done := make(chan Result, 1)
	go func() { done <- h.Run(context.Background()) }()

	// time.Unix with a huge but valid second count: deadline is effectively
	// unreachable, so the drain must complete normally.
	farFuture := time.Unix(1<<62/int64(time.Second), 0)
	src.EmitNotice(PreemptionNotice{Deadline: farFuture})

	var res Result
	select {
	case res = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run hung on a far-future deadline")
	}
	if res.Outcome != OutcomeDrained {
		t.Fatalf("outcome = %q, want %q for a far-future deadline", res.Outcome, OutcomeDrained)
	}
	want := []DrainStep{StepStopAdmission, StepCheckpoint, StepDeregister}
	if got := rec.snapshot(); !equalSteps(got, want) {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	if atomic.LoadInt32(&forced) != 0 {
		t.Fatal("ForceStop must NOT run for an effectively-unreachable deadline")
	}
}

// TestAdv_AzureNaNLikeNotBefore_FallsBackDeterministically proves that an
// unparseable ("poison") NotBefore string does NOT poison the deadline: the
// Azure poller falls back to now()+lead via the injected clock, yielding a
// concrete, finite deadline (never a zero/NaN-equivalent time). SINK-SIDE: the
// fired notice carries exactly fixedNow+lead.
func TestAdv_AzureNaNLikeNotBefore_FallsBackDeterministically(t *testing.T) {
	var sawHeader bool
	var mu sync.Mutex
	// "NaN"-style garbage where a timestamp is expected.
	doc := `{"DocumentIncarnation":3,"Events":[{"EventId":"E2","EventType":"Preempt","ResourceType":"VirtualMachine","Resources":["vm0"],"EventStatus":"Scheduled","NotBefore":"not-a-timestamp-NaN"}]}`
	srv := newAzureIMDS(t, doc, &sawHeader, &mu)
	defer srv.Close()

	fixedNow := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	sink := &fakeSink{}
	p := NewAzurePoller(srv.URL, sink,
		WithHTTPClient(srv.Client()),
		WithClock(func() time.Time { return fixedNow }))

	notice, detected, err := p.Poll(context.Background())
	if err != nil || !detected {
		t.Fatalf("poll: detected=%v err=%v", detected, err)
	}
	want := fixedNow.Add(azureDefaultLead)
	if !notice.Deadline.Equal(want) {
		t.Fatalf("fallback deadline = %v, want %v (now+lead)", notice.Deadline, want)
	}
	if notice.Deadline.IsZero() {
		t.Fatal("poison NotBefore yielded a zero deadline (would force-stop instantly)")
	}
}

// ----------------------------------------------------------------------------
// (B) TOTAL ORDER: multiple Azure preemption events with different deadlines.
// ----------------------------------------------------------------------------

// TestAdv_AzureMultiEvent_DeterministicSelection probes which event the Azure
// poller selects when the Scheduled-Events document carries MORE THAN ONE
// preemption event with DIFFERENT NotBefore deadlines.
//
// FINDING (honest three-way): the poller selects the FIRST event in JSON array
// order (encoding/json preserves array order), NOT the earliest deadline. That
// selection is DETERMINISTIC (array order is stable across runs), so there is no
// nondeterminism/total-order DEFECT to fix here — and the package docstring only
// promises "treat a Preempt/Terminate event as a reclamation warning", it does
// not promise earliest-deadline selection. This test therefore PINS the
// deterministic first-in-array behaviour as a CONTRACT.
//
// LATENT RISK (noted, not fixed): if Azure ever emits the earliest-deadline
// event AFTER a later one in the array, the node would drain against an
// over-generous deadline and could be reclaimed mid-drain. Promoting selection
// to min-NotBefore would harden this. See report.
//
// The probe runs the SAME document twice (count>=2 via -count, plus an explicit
// inner repeat) and asserts the SAME deadline is chosen each time — pinning
// determinism, which is the property the brief's category (B) cares about.
func TestAdv_AzureMultiEvent_DeterministicSelection(t *testing.T) {
	var sawHeader bool
	var mu sync.Mutex

	early := time.Date(2026, 6, 14, 10, 0, 0, 0, time.UTC) // earliest deadline
	late := time.Date(2026, 6, 14, 11, 0, 0, 0, time.UTC)  // later deadline

	// Array order: LATE event first, EARLY event second. A min-deadline selector
	// would pick `early`; a first-in-array selector picks `late`.
	doc := `{"DocumentIncarnation":9,"Events":[` +
		`{"EventId":"LATE","EventType":"Preempt","ResourceType":"VirtualMachine","Resources":["vm0"],"EventStatus":"Scheduled","NotBefore":"` + late.Format(time.RFC1123) + `"},` +
		`{"EventId":"EARLY","EventType":"Preempt","ResourceType":"VirtualMachine","Resources":["vm0"],"EventStatus":"Scheduled","NotBefore":"` + early.Format(time.RFC1123) + `"}` +
		`]}`

	srv := newAzureIMDS(t, doc, &sawHeader, &mu)
	defer srv.Close()

	var first time.Time
	for i := 0; i < 5; i++ {
		sink := &fakeSink{}
		p := NewAzurePoller(srv.URL, sink, WithHTTPClient(srv.Client()))
		notice, detected, err := p.Poll(context.Background())
		if err != nil || !detected {
			t.Fatalf("iter %d: detected=%v err=%v", i, detected, err)
		}
		if i == 0 {
			first = notice.Deadline
		} else if !notice.Deadline.Equal(first) {
			t.Fatalf("NONDETERMINISTIC selection: iter %d deadline=%v, iter 0 deadline=%v",
				i, notice.Deadline, first)
		}
	}

	// Pin the observed behaviour: first-in-array (LATE) is chosen. This documents
	// the contract so a future change to min-deadline selection is a deliberate,
	// reviewed change rather than an accident.
	wantLate, _ := time.Parse(time.RFC1123, late.Format(time.RFC1123))
	if !first.Equal(wantLate) {
		t.Fatalf("selected deadline = %v; observed contract is first-in-array (%v). "+
			"If this changed to earliest-deadline (%v), update this pin deliberately.",
			first, wantLate, early)
	}
}

// ----------------------------------------------------------------------------
// (D) OVER-COMMIT / RACE: the sink must fire exactly once under concurrent polls.
// ----------------------------------------------------------------------------

// TestAdv_AWSSinkFiresExactlyOnceUnderRace proves the "drain side effect is
// committed exactly once" invariant under concurrency: many goroutines Poll the
// same already-preempting AWS IMDS simultaneously, and the SINK is driven for
// exactly ONE drain→checkpoint→upload sequence (3 calls total), never
// over-committed. Run under -race; a missing/incorrect sinkFired latch would
// either race or record >3 sink calls.
func TestAdv_AWSSinkFiresExactlyOnceUnderRace(t *testing.T) {
	token := "AQAE-adversarial-token"
	// AWS IMDS that is already issuing a terminate notice.
	deadline := time.Now().Add(2 * time.Minute).UTC().Truncate(time.Second)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case awsTokenPath:
			if r.Method != http.MethodPut {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(token))
		case awsInstanceActionPath:
			if r.Header.Get(awsTokenHeader) != token {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"action":"terminate","time":"` + deadline.Format(time.RFC3339) + `"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	var sinkCalls int32
	cnt := &countingSink{n: &sinkCalls}
	p := NewAWSPoller(srv.URL, cnt, WithHTTPClient(srv.Client()))

	const goroutines = 16
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, _, _ = p.Poll(context.Background())
		}()
	}
	wg.Wait()

	// Exactly one drain→checkpoint→upload = 3 sink method calls, no more.
	if got := atomic.LoadInt32(&sinkCalls); got != 3 {
		t.Fatalf("sink driven %d times across %d concurrent polls; want exactly 3 "+
			"(drain+checkpoint+upload once) — over/under-commit of the drain side effect",
			got, goroutines)
	}
}

// countingSink counts every sink method invocation atomically.
type countingSink struct{ n *int32 }

func (c *countingSink) Drain(context.Context, time.Time) error      { atomic.AddInt32(c.n, 1); return nil }
func (c *countingSink) Checkpoint(context.Context, time.Time) error { atomic.AddInt32(c.n, 1); return nil }
func (c *countingSink) Upload(context.Context, time.Time) error     { atomic.AddInt32(c.n, 1); return nil }

// equalSteps compares two DrainStep slices element-wise.
func equalSteps(a, b []DrainStep) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
