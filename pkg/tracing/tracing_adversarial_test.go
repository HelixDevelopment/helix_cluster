package tracing

// Adversarial, sink-side test suite for pkg/tracing.
//
// Probes (per the protocol):
//   (a) UNGUARDED SHARED STATE — concurrent SetTag/GetTag/Log + BatchSpanProcessor
//       OnEnd/Flush under -race, with conservation (no lost span/tag/log).
//   (b) SPAN LIFECYCLE — Finish twice, mutate-after-Finish, and the frozen-duration
//       property of a finished span (Duration must equal endTime-StartTime).
//   (c) ID UNIQUENESS — many concurrent trace/span IDs are unique and never all-zero.
//   (d) PROPAGATION/SAMPLING — Inject->Extract round-trip preserves trace/parent/sampled
//       bits; malformed/empty/oversized traceparent is rejected (no valid-looking ctx);
//       sampling numeric edges (flags 0x00/0x01/0xff-rejected).
//
// Assertions are SINK-SIDE: conservation counts, equality of preserved fields,
// rejection of invalid input — never merely "did not panic".

import (
	"context"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// (a) UNGUARDED SHARED STATE — concurrent span mutation + batch processor.
// ---------------------------------------------------------------------------

// TestAdversarial_ConcurrentSpanMutation_Conservation hammers a single span's
// tag/log maps from many goroutines and asserts conservation: every distinct
// tag written is readable, and the total log count equals the number written.
// Run under -race this also surfaces any data race on the tags/logs maps.
func TestAdversarial_ConcurrentSpanMutation_Conservation(t *testing.T) {
	t.Parallel()
	_, ctx := StartSpan(context.Background(), "root")
	s := SpanFromContext(ctx)
	if s == nil {
		t.Fatal("SpanFromContext returned nil for a started span")
	}

	const goroutines = 64
	const perG = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				key := "k-" + advItoa(g) + "-" + advItoa(i)
				s.SetTag(key, "v")
				_ = s.GetTag(key)
				_ = s.Tags()
				s.Log("msg")
				_ = s.Logs()
			}
		}(g)
	}
	wg.Wait()

	// Conservation: every tag we wrote must be present.
	tags := s.Tags()
	wantTags := goroutines * perG
	if len(tags) != wantTags {
		t.Errorf("lost tags: got %d distinct tags, want %d", len(tags), wantTags)
	}
	// Conservation: every log we wrote must be present (no lost append).
	logs := s.Logs()
	wantLogs := goroutines * perG
	if len(logs) != wantLogs {
		t.Errorf("lost logs: got %d, want %d", len(logs), wantLogs)
	}
}

// TestAdversarial_BatchProcessor_Conservation enqueues finished spans from many
// goroutines and flushes concurrently, asserting that every finished span is
// exported exactly once (no lost or duplicated span across the queue boundary).
func TestAdversarial_BatchProcessor_Conservation(t *testing.T) {
	t.Parallel()
	exp := &countingExporter{seen: map[string]int{}}
	p := NewBatchSpanProcessor(exp)

	const goroutines = 32
	const perG = 25
	total := goroutines * perG

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				_, ctx := StartSpan(context.Background(), "s")
				s := SpanFromContext(ctx)
				s.Finish()
				p.OnEnd(s)
				if i%5 == 0 {
					_ = p.Flush(context.Background())
				}
			}
		}(g)
	}
	wg.Wait()
	if err := p.Flush(context.Background()); err != nil {
		t.Fatalf("final flush: %v", err)
	}

	exp.mu.Lock()
	defer exp.mu.Unlock()
	if exp.total != total {
		t.Errorf("span conservation violated: exported %d spans, enqueued %d", exp.total, total)
	}
	for id, n := range exp.seen {
		if n != 1 {
			t.Errorf("span %s exported %d times (want exactly 1)", id, n)
		}
	}
}

type countingExporter struct {
	mu    sync.Mutex
	seen  map[string]int
	total int
}

func (c *countingExporter) ExportSpans(_ context.Context, spans []*Span) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range spans {
		c.seen[s.SpanID]++
		c.total++
	}
	return nil
}

// ---------------------------------------------------------------------------
// (b) SPAN LIFECYCLE — end-twice, mutate-after-end, frozen duration.
// ---------------------------------------------------------------------------

// TestAdversarial_FinishTwice_EndTimeStable asserts that finishing a span twice
// does not move its recorded endTime (the first finish wins) and does not panic.
func TestAdversarial_FinishTwice_EndTimeStable(t *testing.T) {
	t.Parallel()
	_, ctx := StartSpan(context.Background(), "s")
	s := SpanFromContext(ctx)
	s.Finish()
	first := s.endUnixNano()
	time.Sleep(2 * time.Millisecond)
	s.Finish() // second finish must be a no-op on endTime
	second := s.endUnixNano()
	if first != second {
		t.Errorf("end-twice moved endTime: first=%d second=%d (want stable)", first, second)
	}
	if !s.IsFinished() {
		t.Error("span not marked finished after Finish")
	}
}

// TestAdversarial_FinishedSpan_DurationFrozen is the sink-side correctness probe
// for the span lifecycle: once a span is finished, its Duration MUST equal
// endTime-StartTime and MUST NOT keep growing with wall-clock time.
//
// !!! REAL BUG REPRODUCER — FAILS ON UNFIXED PROD. !!!
// Span.Finish() records s.endTime, but Span.Duration() returns
// time.Since(s.StartTime) and never consults endTime, so a finished span's
// reported duration drifts upward with wall-clock time instead of being frozen.
// This test is intentionally LEFT FAILING against current prod (not env-gated;
// the defect is non-fatal). Minimal fix: in Duration(), if the span is finished
// and endTime is set, return end.Sub(s.StartTime). See report.
func TestAdversarial_FinishedSpan_DurationFrozen(t *testing.T) {
	t.Parallel()
	_, ctx := StartSpan(context.Background(), "s")
	s := SpanFromContext(ctx)

	time.Sleep(5 * time.Millisecond)
	s.Finish()

	d1 := s.Duration()
	// Sink-side: the finished span's duration is whatever it was at Finish.
	time.Sleep(20 * time.Millisecond)
	d2 := s.Duration()

	// Sink-side correctness: the duration of a finished span is FROZEN. It must
	// not change as wall-clock time advances after Finish. This is the property
	// that bites prod (Duration() == time.Since(StartTime), which keeps growing).
	if d2 != d1 {
		t.Errorf("finished span duration drifted: d1=%v d2=%v (a finished span's duration must be frozen at endTime-StartTime)", d1, d2)
	}

	// Sanity bound: the frozen duration must be at least the time we slept before
	// Finish and not absurdly large (it should be ~the pre-Finish elapsed time,
	// definitely far below the post-Finish idle window we added).
	if d1 < 5*time.Millisecond {
		t.Errorf("finished span duration %v is below the 5ms elapsed before Finish", d1)
	}
	if d1 >= 20*time.Millisecond {
		t.Errorf("finished span duration %v includes post-Finish idle time (should be frozen near 5ms)", d1)
	}
}

// TestAdversarial_DurationNeverNegative guards against a negative duration from a
// finished span (clock/lifecycle inversion). endTime is set in Finish from
// time.Now() after StartTime, so the delta must be >= 0.
func TestAdversarial_DurationNeverNegative(t *testing.T) {
	t.Parallel()
	_, ctx := StartSpan(context.Background(), "s")
	s := SpanFromContext(ctx)
	s.Finish()
	if d := s.Duration(); d < 0 {
		t.Errorf("finished span reported negative duration: %v", d)
	}
}

// ---------------------------------------------------------------------------
// (c) ID UNIQUENESS — concurrent trace/span IDs unique and never all-zero.
// ---------------------------------------------------------------------------

// TestAdversarial_IDUniqueness_Concurrent starts many spans concurrently and
// asserts every TraceID and SpanID is unique and non-zero (W3C: ids are never
// all-zero). A collision or all-zero id is a hard failure.
func TestAdversarial_IDUniqueness_Concurrent(t *testing.T) {
	t.Parallel()
	const goroutines = 64
	const perG = 200
	type pair struct{ trace, span string }
	results := make([][]pair, goroutines)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			local := make([]pair, perG)
			for i := 0; i < perG; i++ {
				_, ctx := StartSpan(context.Background(), "s")
				s := SpanFromContext(ctx)
				local[i] = pair{s.TraceID, s.SpanID}
			}
			results[g] = local
		}(g)
	}
	wg.Wait()

	traceSeen := make(map[string]struct{})
	spanSeen := make(map[string]struct{})
	for _, batch := range results {
		for _, p := range batch {
			if isAllZeroOrEmpty(p.trace) {
				t.Fatalf("all-zero/empty TraceID generated: %q", p.trace)
			}
			if isAllZeroOrEmpty(p.span) {
				t.Fatalf("all-zero/empty SpanID generated: %q", p.span)
			}
			if _, dup := spanSeen[p.span]; dup {
				t.Fatalf("SpanID collision: %q", p.span)
			}
			spanSeen[p.span] = struct{}{}
			traceSeen[p.trace] = struct{}{}
		}
	}
	wantSpans := goroutines * perG
	if len(spanSeen) != wantSpans {
		t.Errorf("span-id uniqueness: got %d distinct, want %d", len(spanSeen), wantSpans)
	}
	// Trace IDs should also be globally unique (each StartSpan with a fresh root ctx).
	if len(traceSeen) != wantSpans {
		t.Errorf("trace-id uniqueness: got %d distinct, want %d", len(traceSeen), wantSpans)
	}
}

func isAllZeroOrEmpty(s string) bool {
	if s == "" {
		return true
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '0' && c != '-' {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// (d) PROPAGATION + SAMPLING — round-trip preservation and malformed rejection.
// ---------------------------------------------------------------------------

// TestAdversarial_InjectExtract_RoundTrip asserts the W3C carrier round-trip
// preserves trace-id, parent-id and the sampled bit exactly.
func TestAdversarial_InjectExtract_RoundTrip(t *testing.T) {
	t.Parallel()
	for _, sampled := range []bool{true, false} {
		tp := NewTraceParent(sampled)
		ts, _ := ParseTraceState("vendor=val")
		carrier := map[string]string{}
		Inject(carrier, tp, ts)

		got, gotTS, err := Extract(carrier)
		if err != nil {
			t.Fatalf("Extract after Inject failed: %v", err)
		}
		if got.TraceID != tp.TraceID {
			t.Errorf("trace-id not preserved: got %q want %q", got.TraceID, tp.TraceID)
		}
		if got.ParentID != tp.ParentID {
			t.Errorf("parent-id not preserved: got %q want %q", got.ParentID, tp.ParentID)
		}
		if got.Sampled() != sampled {
			t.Errorf("sampled bit not preserved: got %v want %v", got.Sampled(), sampled)
		}
		if gotTS.Get("vendor") != "val" {
			t.Errorf("tracestate not preserved: got %q", gotTS.Get("vendor"))
		}
	}
}

// TestAdversarial_MalformedTraceParent_Rejected asserts that malformed, empty,
// all-zero, and oversized traceparent inputs are REJECTED — never silently
// yielding a valid-looking TraceParent.
func TestAdversarial_MalformedTraceParent_Rejected(t *testing.T) {
	t.Parallel()
	bad := []string{
		"",
		"00",
		"00-00000000000000000000000000000000-0000000000000000-01", // all-zero trace & parent
		"00-" + repeat("0", 32) + "-" + repeat("0", 16) + "-01",   // all-zero (explicit)
		"00-" + repeat("a", 32) + "-" + repeat("0", 16) + "-01",   // all-zero parent
		"ff-" + repeat("a", 32) + "-" + repeat("b", 16) + "-01",   // forbidden version 0xff
		"00-" + repeat("a", 31) + "-" + repeat("b", 16) + "-01",   // short trace-id
		"00-" + repeat("A", 32) + "-" + repeat("b", 16) + "-01",   // uppercase (must be lowercase)
		"00-" + repeat("a", 32) + "-" + repeat("b", 16) + "-01-x", // trailing data on version 0
		"00-" + repeat("a", 32) + "-" + repeat("b", 16),           // truncated (no flags)
		repeat("a", 5000),                                         // oversized garbage
	}
	for _, raw := range bad {
		if _, err := ParseTraceParent(raw); err == nil {
			t.Errorf("malformed traceparent accepted (want error): %q", raw)
		}
		// And via the carrier: a bad traceparent must fail Extract, not produce a ctx.
		_, _, err := Extract(map[string]string{"traceparent": raw})
		if raw != "" && err == nil {
			t.Errorf("Extract accepted malformed traceparent: %q", raw)
		}
	}
}

// TestAdversarial_SamplingFlagEdges checks the sampling numeric edges on the
// trace-flags byte: only the low bit is the sampled decision; 0xff version is
// rejected at parse but 0xff *flags* (with a valid version) is accepted and
// reports sampled=true.
func TestAdversarial_SamplingFlagEdges(t *testing.T) {
	t.Parallel()
	cases := []struct {
		flags   byte
		sampled bool
	}{
		{0x00, false},
		{0x01, true},
		{0xfe, false}, // low bit clear
		{0xff, true},  // low bit set
	}
	for _, c := range cases {
		if got := SampledFromFlags(c.flags); got != c.sampled {
			t.Errorf("SampledFromFlags(0x%02x)=%v want %v", c.flags, got, c.sampled)
		}
		tp := TraceParent{Flags: c.flags}
		if got := tp.Sampled(); got != c.sampled {
			t.Errorf("TraceParent{Flags:0x%02x}.Sampled()=%v want %v", c.flags, got, c.sampled)
		}
	}
}

// --- tiny local helpers (avoid pulling strconv/strings into assertions) ---

func advItoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
