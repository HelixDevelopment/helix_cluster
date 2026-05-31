package tracing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
)

// capturedTraces is the minimal subset of the OTLP JSON body the tests assert
// on. It decodes whatever the collector actually received over real HTTP.
type capturedTraces struct {
	ResourceSpans []struct {
		Resource struct {
			Attributes []struct {
				Key   string `json:"key"`
				Value struct {
					StringValue string `json:"stringValue"`
				} `json:"value"`
			} `json:"attributes"`
		} `json:"resource"`
		ScopeSpans []struct {
			Scope struct {
				Name string `json:"name"`
			} `json:"scope"`
			Spans []struct {
				TraceID           string `json:"traceId"`
				SpanID            string `json:"spanId"`
				ParentSpanID      string `json:"parentSpanId"`
				Name              string `json:"name"`
				StartTimeUnixNano string `json:"startTimeUnixNano"`
				EndTimeUnixNano   string `json:"endTimeUnixNano"`
				Status            struct {
					Code int `json:"code"`
				} `json:"status"`
				Attributes []struct {
					Key   string `json:"key"`
					Value struct {
						StringValue string `json:"stringValue"`
					} `json:"value"`
				} `json:"attributes"`
			} `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"resourceSpans"`
}

func (c capturedTraces) flatSpans() map[string]struct {
	TraceID, ParentSpanID, Name string
	StatusCode                  int
	Attrs                       map[string]string
} {
	out := map[string]struct {
		TraceID, ParentSpanID, Name string
		StatusCode                  int
		Attrs                       map[string]string
	}{}
	for _, rs := range c.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			for _, sp := range ss.Spans {
				attrs := map[string]string{}
				for _, a := range sp.Attributes {
					attrs[a.Key] = a.Value.StringValue
				}
				out[sp.SpanID] = struct {
					TraceID, ParentSpanID, Name string
					StatusCode                  int
					Attrs                       map[string]string
				}{sp.TraceID, sp.ParentSpanID, sp.Name, sp.Status.Code, attrs}
			}
		}
	}
	return out
}

// fakeCollector is an httptest.Server that records the last OTLP body and the
// path it was POSTed to. statusCode controls the response.
type fakeCollector struct {
	srv        *httptest.Server
	mu         sync.Mutex
	body       []byte
	path       string
	method     string
	contentTyp string
	hits       int
}

func newFakeCollector(t *testing.T, statusCode int) *fakeCollector {
	t.Helper()
	fc := &fakeCollector{}
	fc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		full := readAll(r)
		fc.mu.Lock()
		fc.body = full
		fc.path = r.URL.Path
		fc.method = r.Method
		fc.contentTyp = r.Header.Get("Content-Type")
		fc.hits++
		fc.mu.Unlock()
		w.WriteHeader(statusCode)
	}))
	t.Cleanup(fc.srv.Close)
	return fc
}

func readAll(r *http.Request) []byte {
	var buf []byte
	tmp := make([]byte, 4096)
	for {
		n, err := r.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			break
		}
	}
	return buf
}

func (fc *fakeCollector) decoded(t *testing.T) capturedTraces {
	t.Helper()
	fc.mu.Lock()
	defer fc.mu.Unlock()
	var ct capturedTraces
	if err := json.Unmarshal(fc.body, &ct); err != nil {
		t.Fatalf("collector body is not valid JSON: %v\nbody=%q", err, string(fc.body))
	}
	return ct
}

// TestOTLPExportTwoSpansParentChild creates and finishes two spans (a parent
// and its child), flushes through the batch processor, and asserts the
// collector received a well-formed OTLP body containing BOTH spans with their
// exact (normalized) trace/span ids, names, and the parent-child link.
//
// MUTATION: in OTLPHTTPExporter.encode, change `for _, s := range spans` to
// `for _, s := range spans[:1]` (drop the second span) OR have ExportSpans POST
// an empty body — either makes the "child present with exact id" / "both spans
// received" assertions below fail.
func TestOTLPExportTwoSpansParentChild(t *testing.T) {
	fc := newFakeCollector(t, http.StatusOK)
	exp := NewOTLPHTTPExporter(fc.srv.URL, WithHTTPClient(fc.srv.Client()), WithServiceName("svc-under-test"))
	proc := NewBatchSpanProcessor(exp)

	parent, ctx := StartSpan(context.Background(), "parent-op")
	child, _ := StartSpan(ctx, "child-op")
	child.SetTag("error", "true") // exercise status mapping
	parent.Finish()
	child.Finish()
	proc.OnEnd(parent)
	proc.OnEnd(child)

	if err := proc.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// Real HTTP sink-side checks.
	if fc.method != http.MethodPost {
		t.Errorf("expected POST, got %s", fc.method)
	}
	if fc.path != "/v1/traces" {
		t.Errorf("expected default path /v1/traces, got %s", fc.path)
	}
	if fc.contentTyp != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", fc.contentTyp)
	}

	ct := fc.decoded(t)
	// service.name resource attribute present.
	foundSvc := false
	for _, rs := range ct.ResourceSpans {
		for _, a := range rs.Resource.Attributes {
			if a.Key == "service.name" && a.Value.StringValue == "svc-under-test" {
				foundSvc = true
			}
		}
	}
	if !foundSvc {
		t.Error("expected service.name=svc-under-test resource attribute")
	}

	spans := ct.flatSpans()
	if len(spans) != 2 {
		t.Fatalf("expected collector to receive 2 spans, got %d", len(spans))
	}

	wantParentSpanID := OTLPSpanID(parent.SpanID)
	wantChildSpanID := OTLPSpanID(child.SpanID)
	wantTraceID := OTLPTraceID(parent.TraceID)

	gotParent, ok := spans[wantParentSpanID]
	if !ok {
		t.Fatalf("parent span id %s not found in collected spans %v", wantParentSpanID, keys(spans))
	}
	gotChild, ok := spans[wantChildSpanID]
	if !ok {
		t.Fatalf("child span id %s not found in collected spans %v", wantChildSpanID, keys(spans))
	}

	// Exact names.
	if gotParent.Name != "parent-op" {
		t.Errorf("parent name: want parent-op got %s", gotParent.Name)
	}
	if gotChild.Name != "child-op" {
		t.Errorf("child name: want child-op got %s", gotChild.Name)
	}

	// Exact trace id shared and matches the source span.
	if gotParent.TraceID != wantTraceID || gotChild.TraceID != wantTraceID {
		t.Errorf("trace id mismatch: want %s, parent=%s child=%s", wantTraceID, gotParent.TraceID, gotChild.TraceID)
	}
	if len(wantTraceID) != 32 {
		t.Errorf("trace id must be 32 hex chars, got %d (%s)", len(wantTraceID), wantTraceID)
	}
	if len(wantChildSpanID) != 16 {
		t.Errorf("span id must be 16 hex chars, got %d (%s)", len(wantChildSpanID), wantChildSpanID)
	}

	// Parent-child link: child.parentSpanId == parent.spanId; parent has none.
	if gotChild.ParentSpanID != wantParentSpanID {
		t.Errorf("child parentSpanId: want %s got %s", wantParentSpanID, gotChild.ParentSpanID)
	}
	if gotParent.ParentSpanID != "" {
		t.Errorf("root parent should have no parentSpanId, got %s", gotParent.ParentSpanID)
	}

	// Status mapping: child had error=true -> code 2; parent -> 0.
	if gotChild.StatusCode != 2 {
		t.Errorf("child status: want 2 (error), got %d", gotChild.StatusCode)
	}
	if gotParent.StatusCode != 0 {
		t.Errorf("parent status: want 0 (unset), got %d", gotParent.StatusCode)
	}

	// Queue drained after a successful flush.
	if proc.Queued() != 0 {
		t.Errorf("queue should be empty after flush, got %d", proc.Queued())
	}
}

// TestOTLPExportTimesAreSpanTimes asserts the exported end nano equals the
// span's recorded finish time (proving Finish() records end time and the
// exporter uses it), not merely a non-zero placeholder.
//
// MUTATION: drop the `s.endTime = time.Now()` line in Span.Finish (or make
// endUnixNano always return time.Now()) and the end-time equality fails because
// the encoded value will differ from the span's recorded endTime.
func TestOTLPExportTimesAreSpanTimes(t *testing.T) {
	fc := newFakeCollector(t, http.StatusOK)
	exp := NewOTLPHTTPExporter(fc.srv.URL, WithHTTPClient(fc.srv.Client()))
	proc := NewBatchSpanProcessor(exp)

	s, _ := StartSpan(context.Background(), "timed")
	s.Finish()
	wantEnd := s.endUnixNano()
	wantStart := s.StartTime.UnixNano()
	proc.OnEnd(s)
	if err := proc.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	ct := fc.decoded(t)
	var got struct{ start, end string }
	for _, rs := range ct.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			for _, sp := range ss.Spans {
				got.start = sp.StartTimeUnixNano
				got.end = sp.EndTimeUnixNano
			}
		}
	}
	if got.start != strconv.FormatInt(wantStart, 10) {
		t.Errorf("start nano: want %d got %s", wantStart, got.start)
	}
	if got.end != strconv.FormatInt(wantEnd, 10) {
		t.Errorf("end nano: want %d got %s", wantEnd, got.end)
	}
	endN, _ := strconv.ParseInt(got.end, 10, 64)
	if endN < wantStart {
		t.Errorf("end %d must be >= start %d", endN, wantStart)
	}
}

// TestOTLPExportErroringCollectorSurfacesError asserts a non-2xx collector
// response is returned as an error and the spans are re-queued (not lost).
//
// MUTATION: make ExportSpans ignore resp.StatusCode (always return nil) and
// this test fails because err would be nil despite the 500 response.
func TestOTLPExportErroringCollectorSurfacesError(t *testing.T) {
	fc := newFakeCollector(t, http.StatusInternalServerError)
	exp := NewOTLPHTTPExporter(fc.srv.URL, WithHTTPClient(fc.srv.Client()))
	proc := NewBatchSpanProcessor(exp)

	s, _ := StartSpan(context.Background(), "will-fail")
	s.Finish()
	proc.OnEnd(s)

	err := proc.Flush(context.Background())
	if err == nil {
		t.Fatal("expected error from erroring collector, got nil")
	}
	if fc.hits == 0 {
		t.Error("expected collector to have been hit over HTTP")
	}
	// Spans re-queued for retry, not dropped.
	if proc.Queued() != 1 {
		t.Errorf("expected 1 span re-queued after failed flush, got %d", proc.Queued())
	}
}

// TestBatchFlushesAllQueuedSpans queues three spans and asserts a single flush
// delivers all three in one POST.
//
// MUTATION: in Flush, change `batch := p.queue` to `batch := p.queue[:1]` and
// this fails because only one span would reach the collector.
func TestBatchFlushesAllQueuedSpans(t *testing.T) {
	fc := newFakeCollector(t, http.StatusOK)
	exp := NewOTLPHTTPExporter(fc.srv.URL, WithHTTPClient(fc.srv.Client()))
	proc := NewBatchSpanProcessor(exp)

	want := map[string]bool{}
	for _, name := range []string{"a", "b", "c"} {
		s, _ := StartSpan(context.Background(), name)
		s.Finish()
		proc.OnEnd(s)
		want[OTLPSpanID(s.SpanID)] = true
	}
	if proc.Queued() != 3 {
		t.Fatalf("expected 3 queued, got %d", proc.Queued())
	}
	if err := proc.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if fc.hits != 1 {
		t.Errorf("expected exactly 1 POST for the batch, got %d", fc.hits)
	}
	got := fc.decoded(t).flatSpans()
	if len(got) != 3 {
		t.Fatalf("expected 3 spans delivered, got %d", len(got))
	}
	for id := range want {
		if _, ok := got[id]; !ok {
			t.Errorf("span id %s missing from delivered batch", id)
		}
	}
}

// TestOTLPExportEmptyBatchNoPost asserts a zero-span flush makes no HTTP call.
//
// MUTATION: remove the `if len(spans) == 0 { return nil }` guard in ExportSpans
// (or the len(batch)==0 guard in Flush) and this fails because the collector
// would be hit with an empty/garbage body.
func TestOTLPExportEmptyBatchNoPost(t *testing.T) {
	fc := newFakeCollector(t, http.StatusOK)
	exp := NewOTLPHTTPExporter(fc.srv.URL, WithHTTPClient(fc.srv.Client()))
	proc := NewBatchSpanProcessor(exp)

	if err := proc.Flush(context.Background()); err != nil {
		t.Fatalf("empty flush should succeed: %v", err)
	}
	if fc.hits != 0 {
		t.Errorf("expected no POST for empty batch, got %d hits", fc.hits)
	}
}

// TestNoOpExporterDropsSpans asserts the default exporter ships nothing,
// preserving historical behaviour.
//
// MUTATION: make NoOpExporter.ExportSpans actually deliver, and this would not
// fail directly, but TestOTLPExportTwoSpansParentChild's wiring proves real
// delivery; here we assert the no-op path never errors and reports drop.
func TestNoOpExporterDropsSpans(t *testing.T) {
	proc := NewBatchSpanProcessor(NewNoOpExporter())
	s, _ := StartSpan(context.Background(), "x")
	s.Finish()
	proc.OnEnd(s)
	if err := proc.Flush(context.Background()); err != nil {
		t.Fatalf("noop flush: %v", err)
	}
	if proc.Queued() != 0 {
		t.Errorf("noop flush should drain queue, got %d", proc.Queued())
	}
}

// TestOnEndIgnoresUnfinishedSpans asserts only finished spans are queued.
//
// MUTATION: remove the `!s.IsFinished()` check in OnEnd and this fails because
// the unfinished span would be queued.
func TestOnEndIgnoresUnfinishedSpans(t *testing.T) {
	proc := NewBatchSpanProcessor(NewNoOpExporter())
	s, _ := StartSpan(context.Background(), "open")
	proc.OnEnd(s) // not finished
	if proc.Queued() != 0 {
		t.Errorf("unfinished span should not be queued, got %d", proc.Queued())
	}
	s.Finish()
	proc.OnEnd(s)
	if proc.Queued() != 1 {
		t.Errorf("finished span should be queued, got %d", proc.Queued())
	}
}

// TestNormalizeTracesEndpoint asserts base URLs get /v1/traces and explicit
// paths are preserved.
//
// MUTATION: make normalizeTracesEndpoint always append defaultTracesPath and
// the explicit-path case fails (it would become /custom/v1/traces).
func TestNormalizeTracesEndpoint(t *testing.T) {
	cases := map[string]string{
		"http://collector:4318":           "http://collector:4318/v1/traces",
		"http://collector:4318/":          "http://collector:4318/v1/traces",
		"http://collector:4318/custom":    "http://collector:4318/custom",
		"http://collector:4318/v1/traces": "http://collector:4318/v1/traces",
	}
	for in, want := range cases {
		if got := normalizeTracesEndpoint(in); got != want {
			t.Errorf("normalize(%q): want %q got %q", in, want, got)
		}
	}
}

// TestOTLPIDNormalization asserts UUID trace ids map to 32 hex (dashes removed)
// and 8-byte hex span ids pass through unchanged, with non-hex ids hashed to
// the right width.
//
// MUTATION: change OTLPTraceID to return id unchanged and the UUID/length
// assertions fail.
func TestOTLPIDNormalization(t *testing.T) {
	uuid := "12345678-90ab-cdef-1234-567890abcdef"
	if got := OTLPTraceID(uuid); got != "1234567890abcdef1234567890abcdef" {
		t.Errorf("uuid trace id: got %s", got)
	}
	span16 := "0011223344556677"
	if got := OTLPSpanID(span16); got != span16 {
		t.Errorf("hex span id should pass through: got %s", got)
	}
	// Non-hex normalized deterministically to required width.
	if got := OTLPSpanID("noop"); len(got) != 16 || !isLowerHex(got) {
		t.Errorf("non-hex span id should hash to 16 hex chars, got %q", got)
	}
	if a, b := OTLPSpanID("noop"), OTLPSpanID("noop"); a != b {
		t.Errorf("normalization must be deterministic: %s != %s", a, b)
	}
	if a, b := OTLPSpanID("noop"), OTLPSpanID("noop2"); a == b {
		t.Errorf("distinct ids should not collide: both %s", a)
	}
	if got := OTLPTraceID("noop"); len(got) != 32 || !isLowerHex(got) {
		t.Errorf("non-hex trace id should hash to 32 hex chars, got %q", got)
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
