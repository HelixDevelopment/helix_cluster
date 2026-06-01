package metrics

import (
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestCounter(t *testing.T) {
	c := &Counter{}
	c.Inc()
	c.Inc()
	c.Add(3)
	if c.Value() != 5 {
		t.Errorf("expected 5, got %d", c.Value())
	}
}

func TestGauge(t *testing.T) {
	g := &Gauge{}
	g.Set(10)
	if g.Value() != 10 {
		t.Errorf("expected 10, got %d", g.Value())
	}
	g.Inc()
	if g.Value() != 11 {
		t.Errorf("expected 11, got %d", g.Value())
	}
	g.Dec()
	if g.Value() != 10 {
		t.Errorf("expected 10, got %d", g.Value())
	}
	g.Add(5)
	if g.Value() != 15 {
		t.Errorf("expected 15, got %d", g.Value())
	}
	g.Add(-3)
	if g.Value() != 12 {
		t.Errorf("expected 12, got %d", g.Value())
	}
}

func TestHistogram(t *testing.T) {
	h := NewHistogram([]float64{1, 5, 10})
	h.Observe(0.5)
	h.Observe(3)
	h.Observe(7)
	h.Observe(15)

	buckets, counts, sum, count := h.Snapshot()
	if len(buckets) != 3 {
		t.Fatalf("expected 3 buckets, got %d", len(buckets))
	}
	if count != 4 {
		t.Errorf("expected count 4, got %d", count)
	}
	if sum != 25.5 {
		t.Errorf("expected sum 25.5, got %g", sum)
	}
	if counts[0] != 1 {
		t.Errorf("expected bucket[1] count 1, got %d", counts[0])
	}
	if counts[1] != 2 {
		t.Errorf("expected bucket[5] count 2, got %d", counts[1])
	}
	if counts[2] != 3 {
		t.Errorf("expected bucket[10] count 3, got %d", counts[2])
	}
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	c := r.RegisterCounter("requests_total", "Total requests")
	g := r.RegisterGauge("active_connections", "Active connections")
	h := r.RegisterHistogram("request_duration_seconds", "Request duration", []float64{0.1, 0.5, 1})

	c.Inc()
	g.Set(5)
	h.Observe(0.2)

	if v, ok := r.Get("requests_total"); !ok {
		t.Fatal("expected requests_total to exist")
	} else if v.(*Counter).Value() != 1 {
		t.Errorf("expected counter value 1, got %d", v.(*Counter).Value())
	}

	if v, ok := r.Get("active_connections"); !ok {
		t.Fatal("expected active_connections to exist")
	} else if v.(*Gauge).Value() != 5 {
		t.Errorf("expected gauge value 5, got %d", v.(*Gauge).Value())
	}
}

func TestPrometheusHandler(t *testing.T) {
	r := NewRegistry()
	c := r.RegisterCounter("requests_total", "Total requests")
	g := r.RegisterGauge("active_connections", "Active connections")
	h := r.RegisterHistogram("request_duration_seconds", "Request duration", []float64{0.1, 0.5, 1})

	c.Add(42)
	g.Set(7)
	h.Observe(0.05)
	h.Observe(0.3)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	r.PrometheusHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "requests_total 42") {
		t.Errorf("expected requests_total 42 in output, got:\n%s", body)
	}
	if !strings.Contains(body, "active_connections 7") {
		t.Errorf("expected active_connections 7 in output, got:\n%s", body)
	}
	if !strings.Contains(body, "request_duration_seconds_bucket{le=\"0.1\"} 1") {
		t.Errorf("expected histogram bucket in output, got:\n%s", body)
	}
	if !strings.Contains(body, "request_duration_seconds_count 2") {
		t.Errorf("expected histogram count in output, got:\n%s", body)
	}
	if !strings.Contains(body, "# HELP requests_total Total requests") {
		t.Errorf("expected HELP line in output, got:\n%s", body)
	}
	if !strings.Contains(body, "# TYPE requests_total counter") {
		t.Errorf("expected TYPE line in output, got:\n%s", body)
	}
}

func TestDefaultBuckets(t *testing.T) {
	b := DefaultBuckets()
	if len(b) != 11 {
		t.Errorf("expected 11 default buckets, got %d", len(b))
	}
	for i := 1; i < len(b); i++ {
		if b[i] <= b[i-1] {
			t.Error("default buckets must be strictly increasing")
		}
	}
}

// --- Mutation tests ---

func TestCounter_ConcurrentInc_Mutation(t *testing.T) {
	c := &Counter{}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Inc()
		}()
	}
	wg.Wait()
	if c.Value() != 100 {
		t.Errorf("expected 100 after 100 concurrent increments, got %d", c.Value())
	}
}

func TestCounter_AddNegative_Mutation(t *testing.T) {
	c := &Counter{}
	c.Add(10)
	c.Add(-3)
	if c.Value() != 7 {
		t.Errorf("expected 7 after Add(10) then Add(-3), got %d", c.Value())
	}
}

func TestCounter_ValueIsolation_Mutation(t *testing.T) {
	c1 := &Counter{}
	c2 := &Counter{}
	c1.Inc()
	if c2.Value() != 0 {
		t.Error("separate Counter instances must not share state")
	}
}

func TestGauge_ConcurrentMutation_Mutation(t *testing.T) {
	g := &Gauge{}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.Inc()
		}()
	}
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.Dec()
		}()
	}
	wg.Wait()
	if g.Value() != 0 {
		t.Errorf("expected 0 after equal inc/dec, got %d", g.Value())
	}
}

func TestHistogram_ConcurrentObserve_Mutation(t *testing.T) {
	h := NewHistogram([]float64{1, 10, 100})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(v float64) {
			defer wg.Done()
			h.Observe(v)
		}(float64(i))
	}
	wg.Wait()
	_, _, _, count := h.Snapshot()
	if count != 100 {
		t.Errorf("expected count 100 after concurrent observes, got %d", count)
	}
}

func TestRegistry_GetMissing(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Get("nonexistent"); ok {
		t.Error("expected Get to return false for missing metric")
	}
}

// --- Prometheus exposition golden / explicit assertions ---

// TestPrometheusHandler_GoldenExposition pins the exact, fully-ordered
// Prometheus text exposition body for a known multi-type metric set. This
// proves end-user-visible scrape output including the +Inf bucket line, the
// _sum line, the _count line, %g float formatting, deterministic name
// ordering, and HELP/TYPE headers. A regression dropping +Inf/_sum, changing
// ordering, or mangling formatting FAILS this test.
func TestPrometheusHandler_GoldenExposition(t *testing.T) {
	r := NewRegistry()
	c := r.RegisterCounter("requests_total", "Total requests")
	g := r.RegisterGauge("active_connections", "Active connections")
	h := r.RegisterHistogram("request_duration_seconds", "Request duration", []float64{0.1, 0.5, 1})

	c.Add(42)
	g.Set(7)
	h.Observe(0.05) // <= 0.1
	h.Observe(0.3)  // <= 0.5, <= 1

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	r.PrometheusHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("unexpected Content-Type: %q", ct)
	}

	// Names sort lexicographically: active_connections, request_duration_seconds, requests_total.
	want := strings.Join([]string{
		"# HELP active_connections Active connections",
		"# TYPE active_connections gauge",
		"active_connections 7",
		"# HELP request_duration_seconds Request duration",
		"# TYPE request_duration_seconds histogram",
		`request_duration_seconds_bucket{le="0.1"} 1`,
		`request_duration_seconds_bucket{le="0.5"} 2`,
		`request_duration_seconds_bucket{le="1"} 2`,
		`request_duration_seconds_bucket{le="+Inf"} 2`,
		"request_duration_seconds_sum 0.35",
		"request_duration_seconds_count 2",
		"# HELP requests_total Total requests",
		"# TYPE requests_total counter",
		"requests_total 42",
		"",
	}, "\n")

	if got := rec.Body.String(); got != want {
		t.Errorf("exposition body mismatch.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestPrometheusHandler_InfAndSumLines explicitly asserts the +Inf bucket and
// _sum lines that the original suite never checked (PASS-bluff #2).
func TestPrometheusHandler_InfAndSumLines(t *testing.T) {
	r := NewRegistry()
	h := r.RegisterHistogram("lat_seconds", "Latency", []float64{0.1, 1})
	h.Observe(0.05)
	h.Observe(0.5)
	h.Observe(5) // exceeds all finite buckets -> only in +Inf

	body := r.serialize()

	mustContain := []string{
		`lat_seconds_bucket{le="0.1"} 1`,
		`lat_seconds_bucket{le="1"} 2`,
		`lat_seconds_bucket{le="+Inf"} 3`,
		"lat_seconds_sum 5.55",
		"lat_seconds_count 3",
	}
	for _, line := range mustContain {
		if !strings.Contains(body, line) {
			t.Errorf("missing required exposition line %q in:\n%s", line, body)
		}
	}
}

// TestRegistry_Serialize_Ordering proves serialize() emits metric blocks in
// stable, sorted-by-name order regardless of registration order.
func TestRegistry_Serialize_Ordering(t *testing.T) {
	r := NewRegistry()
	// Register out of lexical order.
	r.RegisterGauge("zzz_gauge", "z")
	r.RegisterCounter("mmm_counter", "m")
	r.RegisterCounter("aaa_counter", "a")

	body := r.serialize()
	iA := strings.Index(body, "# TYPE aaa_counter")
	iM := strings.Index(body, "# TYPE mmm_counter")
	iZ := strings.Index(body, "# TYPE zzz_gauge")
	if iA < 0 || iM < 0 || iZ < 0 {
		t.Fatalf("missing TYPE lines in output:\n%s", body)
	}
	if !(iA < iM && iM < iZ) {
		t.Errorf("metrics not in sorted order: aaa@%d mmm@%d zzz@%d\n%s", iA, iM, iZ, body)
	}
}

// --- Duplicate registration semantics ---

// TestRegistry_DuplicateName pins the DEFINED behavior: registering the same
// name twice overwrites the prior entry and returns a fresh, independent
// metric instance (last-writer-wins). The old handle keeps its own state but
// is no longer the one exposed/retrieved.
func TestRegistry_DuplicateName(t *testing.T) {
	r := NewRegistry()
	c1 := r.RegisterCounter("dup", "first")
	c1.Add(5)

	c2 := r.RegisterCounter("dup", "second")
	if c1 == c2 {
		t.Fatal("re-registration must return a new instance")
	}
	if c2.Value() != 0 {
		t.Errorf("re-registered counter must start at 0, got %d", c2.Value())
	}

	got, ok := r.Get("dup")
	if !ok {
		t.Fatal("expected dup to exist")
	}
	if got != c2 {
		t.Error("Get must return the most recently registered instance")
	}

	body := r.serialize()
	if !strings.Contains(body, "# HELP dup second") {
		t.Errorf("expected overwritten help text 'second', got:\n%s", body)
	}
	if !strings.Contains(body, "dup 0\n") {
		t.Errorf("expected exposed value to be the new instance (0), got:\n%s", body)
	}
}

// TestRegistry_DuplicateName_TypeChange proves overwrite works across metric
// types and Get reflects the new type.
func TestRegistry_DuplicateName_TypeChange(t *testing.T) {
	r := NewRegistry()
	r.RegisterCounter("x", "as counter")
	g := r.RegisterGauge("x", "as gauge")

	got, ok := r.Get("x")
	if !ok {
		t.Fatal("expected x to exist")
	}
	if _, isGauge := got.(*Gauge); !isGauge {
		t.Errorf("expected *Gauge after type-changing re-registration, got %T", got)
	}
	if got != g {
		t.Error("Get must return the re-registered gauge instance")
	}
	if !strings.Contains(r.serialize(), "# TYPE x gauge") {
		t.Error("exposition must reflect the new type")
	}
}

// --- Histogram edge cases ---

// TestHistogram_EmptyBuckets: a histogram with no finite buckets still tracks
// sum, count and emits only the +Inf bucket.
func TestHistogram_EmptyBuckets(t *testing.T) {
	r := NewRegistry()
	h := r.RegisterHistogram("e", "empty", []float64{})
	h.Observe(1)
	h.Observe(2)

	buckets, counts, sum, count := h.Snapshot()
	if len(buckets) != 0 || len(counts) != 0 {
		t.Errorf("expected no finite buckets, got %d/%d", len(buckets), len(counts))
	}
	if count != 2 || sum != 3 {
		t.Errorf("expected count 2 sum 3, got %d %g", count, sum)
	}
	body := r.serialize()
	if !strings.Contains(body, `e_bucket{le="+Inf"} 2`) {
		t.Errorf("expected +Inf bucket for empty-bucket histogram, got:\n%s", body)
	}
	if strings.Contains(body, `le="`) && strings.Count(body, "e_bucket") != 1 {
		t.Errorf("empty-bucket histogram must emit exactly one (+Inf) bucket line, got:\n%s", body)
	}
}

// TestHistogram_BoundaryEquality proves the cumulative <= semantics: a value
// exactly equal to a bucket bound counts into that bucket.
func TestHistogram_BoundaryEquality(t *testing.T) {
	h := NewHistogram([]float64{1, 2, 3})
	h.Observe(1) // == bound 1 -> in le=1, le=2, le=3
	h.Observe(2) // == bound 2 -> in le=2, le=3
	h.Observe(3) // == bound 3 -> in le=3

	_, counts, _, count := h.Snapshot()
	if count != 3 {
		t.Fatalf("expected count 3, got %d", count)
	}
	want := []uint64{1, 2, 3}
	for i, w := range want {
		if counts[i] != w {
			t.Errorf("boundary equality: bucket[%d] expected %d, got %d", i, w, counts[i])
		}
	}
}

// TestHistogram_UnsortedBuckets documents behavior when buckets are NOT sorted.
// The implementation evaluates v<=b per-bucket independently (no early exit),
// so cumulative counts are computed correctly per bound even if the slice is
// not monotonic; only the exposition ordering follows the input slice order.
func TestHistogram_UnsortedBuckets(t *testing.T) {
	h := NewHistogram([]float64{10, 1, 5})
	h.Observe(3) // <=10 true, <=1 false, <=5 true

	_, counts, _, count := h.Snapshot()
	if count != 1 {
		t.Fatalf("expected count 1, got %d", count)
	}
	if counts[0] != 1 || counts[1] != 0 || counts[2] != 1 {
		t.Errorf("unsorted-bucket per-bound counts wrong: got %v, want [1 0 1]", counts)
	}
}

// TestHistogram_InfObservation: +Inf is <= no finite bound but is counted and
// added to sum (making sum +Inf).
func TestHistogram_InfObservation(t *testing.T) {
	h := NewHistogram([]float64{1, 2})
	h.Observe(math.Inf(1))

	_, counts, sum, count := h.Snapshot()
	if count != 1 {
		t.Errorf("expected count 1, got %d", count)
	}
	if counts[0] != 0 || counts[1] != 0 {
		t.Errorf("expected +Inf observation in no finite bucket, got %v", counts)
	}
	if !math.IsInf(sum, 1) {
		t.Errorf("expected sum +Inf, got %g", sum)
	}
}

// TestHistogram_NaNObservation: NaN compares false against every bound, lands
// in no finite bucket, increments count, and poisons sum to NaN. This pins the
// defined (if degenerate) behavior so a silent change is caught.
func TestHistogram_NaNObservation(t *testing.T) {
	h := NewHistogram([]float64{1, 2})
	h.Observe(math.NaN())

	_, counts, sum, count := h.Snapshot()
	if count != 1 {
		t.Errorf("expected count 1, got %d", count)
	}
	if counts[0] != 0 || counts[1] != 0 {
		t.Errorf("expected NaN observation in no finite bucket, got %v", counts)
	}
	if !math.IsNaN(sum) {
		t.Errorf("expected sum NaN, got %g", sum)
	}
}

// --- Concurrency tests meaningful under -race ---
//
// These tests assert EXACT totals AND interleave concurrent reads with writes
// so the -race detector observes unsynchronized access if the atomic/lock were
// removed from the implementation. Run with `go test -race`.

// TestCounter_ConcurrentAdd_Race fires many goroutines doing Add while other
// goroutines concurrently read Value(). Removing atomic.AddInt64 makes this a
// data race (FAIL under -race) and also corrupts the exact total.
func TestCounter_ConcurrentAdd_Race(t *testing.T) {
	c := &Counter{}
	const writers = 200
	const perWriter = 50
	var wg sync.WaitGroup

	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				c.Inc()
			}
		}()
	}
	// Concurrent readers to expose unsynchronized reads under -race.
	stop := make(chan struct{})
	var rwg sync.WaitGroup
	for i := 0; i < 8; i++ {
		rwg.Add(1)
		go func() {
			defer rwg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = c.Value()
				}
			}
		}()
	}

	wg.Wait()
	close(stop)
	rwg.Wait()

	if got := c.Value(); got != writers*perWriter {
		t.Errorf("expected exact total %d, got %d", writers*perWriter, got)
	}
}

// TestGauge_ConcurrentIncDec_Race interleaves equal Inc/Dec across goroutines
// with concurrent reads. Exact final value must be 0; removing atomics races.
func TestGauge_ConcurrentIncDec_Race(t *testing.T) {
	g := &Gauge{}
	const n = 500
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); g.Inc() }()
		go func() { defer wg.Done(); g.Dec() }()
	}

	stop := make(chan struct{})
	var rwg sync.WaitGroup
	for i := 0; i < 4; i++ {
		rwg.Add(1)
		go func() {
			defer rwg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = g.Value()
				}
			}
		}()
	}

	wg.Wait()
	close(stop)
	rwg.Wait()

	if got := g.Value(); got != 0 {
		t.Errorf("expected exact 0 after %d Inc/Dec pairs, got %d", n, got)
	}
}

// TestHistogram_ConcurrentObserve_Race runs many concurrent Observe calls with
// concurrent Snapshot reads, asserting EXACT count, sum, and per-bucket totals.
// Removing the mutex makes this race under -race AND corrupts the totals.
func TestHistogram_ConcurrentObserve_Race(t *testing.T) {
	h := NewHistogram([]float64{1, 10, 100})
	const writers = 200
	var wg sync.WaitGroup

	// Each writer observes the value 5 once: 5 <= 10 and <= 100, not <= 1.
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.Observe(5)
		}()
	}

	stop := make(chan struct{})
	var rwg sync.WaitGroup
	for i := 0; i < 4; i++ {
		rwg.Add(1)
		go func() {
			defer rwg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					h.Snapshot()
				}
			}
		}()
	}

	wg.Wait()
	close(stop)
	rwg.Wait()

	_, counts, sum, count := h.Snapshot()
	if count != writers {
		t.Errorf("expected exact count %d, got %d", writers, count)
	}
	if sum != float64(writers)*5 {
		t.Errorf("expected exact sum %g, got %g", float64(writers)*5, sum)
	}
	want := []uint64{0, uint64(writers), uint64(writers)} // not<=1, <=10, <=100
	for i, w := range want {
		if counts[i] != w {
			t.Errorf("bucket[%d] expected exact %d, got %d", i, w, counts[i])
		}
	}
}

// --- HXC-1049: real HTTP scrape via httptest.NewServer ---

// TestPrometheusHandler_HTTPServer mounts PrometheusHandler on a real
// httptest.Server, issues an actual HTTP GET (not just an in-process recorder
// call), and validates the parsed body. This proves end-user /metrics scrape
// behavior: the handler is reachable over a real TCP connection, returns
// Content-Type text/plain, and contains all expected metric lines including the
// +Inf bucket and _sum line. A no-op handler, wrong content-type, or missing
// lines fail this test.
func TestPrometheusHandler_HTTPServer(t *testing.T) {
	r := NewRegistry()
	c := r.RegisterCounter("http_requests_total", "Total HTTP requests")
	g := r.RegisterGauge("open_conns", "Open connections")
	h := r.RegisterHistogram("rpc_duration_seconds", "RPC duration", []float64{0.01, 0.1, 1})

	c.Add(7)
	g.Set(3)
	h.Observe(0.005) // <= 0.01, 0.1, 1
	h.Observe(0.05)  // <= 0.1, 1 (not <= 0.01)
	h.Observe(2)     // exceeds all finite buckets

	ts := httptest.NewServer(r.PrometheusHandler())
	defer ts.Close()

	resp, err := http.Get(ts.URL) //nolint:noctx
	if err != nil {
		t.Fatalf("HTTP GET failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "text/plain; charset=utf-8" {
		t.Errorf("unexpected Content-Type: %q", ct)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	body := string(bodyBytes)

	// Validate all required lines are present — any omission fails.
	required := []string{
		"http_requests_total 7",
		"open_conns 3",
		`rpc_duration_seconds_bucket{le="0.01"} 1`,
		`rpc_duration_seconds_bucket{le="0.1"} 2`,
		`rpc_duration_seconds_bucket{le="1"} 2`,
		`rpc_duration_seconds_bucket{le="+Inf"} 3`,
		"rpc_duration_seconds_sum 2.055",
		"rpc_duration_seconds_count 3",
	}
	for _, line := range required {
		if !strings.Contains(body, line) {
			t.Errorf("HTTP /metrics body missing required line %q\nfull body:\n%s", line, body)
		}
	}
}
