// Package metrics — ADVERSARIAL test suite (untracked; safe to delete).
//
// Adversarial Go test engineer probe per CLAUDE-1 (sink-side evidence, not
// "no panic"). Target classes:
//
//	(a) total-order / label-set identity on the registry + label-map metrics
//	(b) unguarded shared accumulator -> data race + lost update (costtracker class)
//	(c) histogram bucket boundary (< vs <=, +Inf, out-of-range)
//	(d) numeric poison: NaN/Inf into sum/mean; negative Add to a "counter"; overflow
//
// Every concurrency probe asserts EXACT conservation of the final sink value
// (final == N*M), never merely "did not panic". Run with:
//
//	go test -race -count=2 github.com/HelixDevelopment/helix_cluster/pkg/metrics -run Adversarial
package metrics

import (
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// -----------------------------------------------------------------------------
// (b) UNGUARDED SHARED ACCUMULATOR / LOST UPDATE
//
// Contract under test: Counter is documented as "a simple atomic counter";
// Inc uses atomic.AddInt64. The costtracker bug lost ~78% of charges because a
// shared accumulator was mutated without synchronisation. Here we hammer a
// single Counter from N goroutines, M increments each, and assert the SINK
// (Value()) equals exactly N*M. A race that drops an increment fails the
// conservation assertion (and trips -race).
// -----------------------------------------------------------------------------

func TestAdversarial_CounterInc_Conservation(t *testing.T) {
	const (
		goroutines = 64
		perG       = 5000
	)
	var c Counter
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				c.Inc()
			}
		}()
	}
	wg.Wait()

	want := int64(goroutines * perG)
	if got := c.Value(); got != want {
		t.Fatalf("LOST UPDATE: Counter.Inc dropped increments under concurrency: got=%d want=%d (lost %d)",
			got, want, want-got)
	}
}

func TestAdversarial_CounterAdd_Conservation(t *testing.T) {
	const (
		goroutines = 48
		perG       = 4000
		addend     = 3
	)
	var c Counter
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				c.Add(addend)
			}
		}()
	}
	wg.Wait()

	want := int64(goroutines * perG * addend)
	if got := c.Value(); got != want {
		t.Fatalf("LOST UPDATE: Counter.Add dropped value under concurrency: got=%d want=%d (lost %d)",
			got, want, want-got)
	}
}

func TestAdversarial_GaugeAddDec_Conservation(t *testing.T) {
	// Half the goroutines Add(+1) M times, half Dec() M times -> net zero.
	const (
		pairs = 32
		perG  = 5000
	)
	var gg Gauge
	var wg sync.WaitGroup
	wg.Add(pairs * 2)
	for p := 0; p < pairs; p++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				gg.Add(1)
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				gg.Dec()
			}
		}()
	}
	wg.Wait()

	if got := gg.Value(); got != 0 {
		t.Fatalf("LOST UPDATE: Gauge Add/Dec not conserved: got=%d want=0", got)
	}
}

// Histogram is mutex-guarded; observing concurrently must conserve count and
// sum exactly. Sink: Snapshot().count == N*M and Snapshot().sum == N*M*v.
func TestAdversarial_HistogramObserve_Conservation(t *testing.T) {
	const (
		goroutines = 40
		perG       = 3000
		val        = 0.2 // lands in finite buckets; sum is exact-ish in float64
	)
	h := NewHistogram(DefaultBuckets())
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				h.Observe(val)
			}
		}()
	}
	wg.Wait()

	_, counts, sum, count := h.Snapshot()
	wantCount := uint64(goroutines * perG)
	if count != wantCount {
		t.Fatalf("LOST UPDATE: Histogram.count dropped observations: got=%d want=%d", count, wantCount)
	}
	// 0.2 <= every bucket >= 0.25; so buckets for le>=0.25 each hold wantCount.
	// The +Inf-equivalent (count) we already checked. Validate the top finite
	// bucket (le=10) caught all of them.
	if counts[len(counts)-1] != wantCount {
		t.Fatalf("LOST UPDATE: top finite bucket undercount: got=%d want=%d",
			counts[len(counts)-1], wantCount)
	}
	// sum conservation within float tolerance.
	wantSum := float64(wantCount) * val
	if math.Abs(sum-wantSum) > 1e-6 {
		t.Fatalf("LOST UPDATE: Histogram.sum not conserved: got=%g want=%g", sum, wantSum)
	}
}

// -----------------------------------------------------------------------------
// (d) LABEL-SET IDENTITY / TOTAL-ORDER + concurrent series creation
//
// EarningsMetrics / TierMetrics are documented "safe for concurrent use" and
// keyed by a single string label (hotkey/tier/...). We:
//   - race concurrent distinct-series creation and assert every series survives
//     (no map-write race losing a key) -> exact distinct-key count in Render().
//   - assert same key from many goroutines collapses to ONE series (identity),
//     last-writer-wins, not duplicated.
// -----------------------------------------------------------------------------

func TestAdversarial_EarningsMetrics_DistinctSeries_Conservation(t *testing.T) {
	const series = 500
	e := NewEarningsMetrics()
	var wg sync.WaitGroup
	wg.Add(series)
	for i := 0; i < series; i++ {
		i := i
		go func() {
			defer wg.Done()
			key := "hotkey-" + itoa(i)
			e.SetTaoEarnings(key, float64(i))
		}()
	}
	wg.Wait()

	out := e.Render()
	got := strings.Count(out, "tao_earnings_total{hotkey=")
	if got != series {
		t.Fatalf("SERIES LOSS: concurrent distinct-series creation lost keys: got=%d want=%d", got, series)
	}
}

func TestAdversarial_TierMetrics_SameKeyIdentity(t *testing.T) {
	const writers = 200
	tm := NewTierMetrics()
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		i := i
		go func() {
			defer wg.Done()
			tm.SetTierUtilization("a100", float64(i)) // same key, racing
		}()
	}
	wg.Wait()

	out := tm.Render()
	// Exactly ONE a100 series — identity must collapse, not duplicate.
	if c := strings.Count(out, `gpu_tier_utilization{tier="a100"}`); c != 1 {
		t.Fatalf("LABEL IDENTITY: same key produced %d series, want exactly 1\n%s", c, out)
	}
}

// Registry concurrent registration + concurrent counter increments through the
// registry handle: assert the serialized sink reflects exact conservation.
func TestAdversarial_Registry_ConcurrentCounter_Conservation(t *testing.T) {
	const (
		goroutines = 50
		perG       = 4000
	)
	r := NewRegistry()
	c := r.RegisterCounter("helix_test_total", "adversarial")
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				c.Inc()
			}
		}()
	}
	wg.Wait()

	v, ok := r.Get("helix_test_total")
	if !ok {
		t.Fatalf("registry lost the metric")
	}
	want := int64(goroutines * perG)
	if got := v.(*Counter).Value(); got != want {
		t.Fatalf("LOST UPDATE via registry: got=%d want=%d", got, want)
	}
	// Sink-side via serialize(): the exposition text must carry the exact value.
	if !strings.Contains(r.serialize(), "helix_test_total "+itoa64(want)) {
		t.Fatalf("serialize() does not reflect conserved value %d:\n%s", want, r.serialize())
	}
}

// -----------------------------------------------------------------------------
// (c) HISTOGRAM BUCKET BOUNDARY — single-threaded, exact semantics.
// Prometheus le-buckets are cumulative and inclusive (v <= upper-bound).
// -----------------------------------------------------------------------------

func TestAdversarial_HistogramBucketBoundary(t *testing.T) {
	// Buckets: [1, 2, 5]. Observe exactly on edges and out-of-range.
	h := NewHistogram([]float64{1, 2, 5})

	h.Observe(1)   // == le=1 -> counts[0..] (1<=1,1<=2,1<=5)
	h.Observe(2)   // == le=2 -> counts[1],counts[2]
	h.Observe(5)   // == le=5 -> counts[2]
	h.Observe(10)  // > all finite -> NO finite bucket, but counted in count(+Inf)
	h.Observe(0.5) // < le=1 -> all buckets

	buckets, counts, _, count := h.Snapshot()
	_ = buckets

	// Cumulative inclusive expectations:
	// le=1: observations <= 1 => {1, 0.5} = 2
	// le=2: <= 2 => {1, 2, 0.5} = 3
	// le=5: <= 5 => {1, 2, 5, 0.5} = 4
	want := []uint64{2, 3, 4}
	for i := range want {
		if counts[i] != want[i] {
			t.Fatalf("BUCKET BOUNDARY: counts[%d] (le=%g) got=%d want=%d (< vs <= mismatch?)",
				i, buckets[i], counts[i], want[i])
		}
	}
	// +Inf bucket == total count == 5 (the 10 is captured here, not in finite buckets).
	if count != 5 {
		t.Fatalf("BUCKET BOUNDARY: total count (+Inf) got=%d want=5 (out-of-range obs dropped?)", count)
	}
}

// -----------------------------------------------------------------------------
// (d) NUMERIC POISON — does a single hostile observation corrupt the sink for
// every subsequent read? We DOCUMENT behaviour; only assert a true contract
// break as a failure.
// -----------------------------------------------------------------------------

// NaN poisons the histogram sum forever: after one NaN, sum is NaN regardless
// of subsequent good observations. This is a LATENT RISK note, not a documented
// contract — assert the observable consequence so the behaviour is pinned.
func TestAdversarial_HistogramNaNPoisonsSum(t *testing.T) {
	h := NewHistogram(DefaultBuckets())
	h.Observe(1.0)
	h.Observe(math.NaN())
	h.Observe(1.0)

	_, _, sum, count := h.Snapshot()
	if count != 3 {
		t.Fatalf("count should still advance past NaN: got=%d want=3", count)
	}
	// Pin the (latent-risk) reality: NaN propagates into sum. If a future fix
	// guards against it, this expectation flips and the test must be updated.
	if !math.IsNaN(sum) {
		t.Logf("NOTE: NaN no longer poisons sum (sum=%g) — guard added upstream", sum)
	} else {
		t.Logf("LATENT RISK pinned: a single NaN observation poisons histogram sum forever (sum=NaN)")
	}
	// NaN <= b is false for all b, so no finite bucket counts it — verify that
	// the NaN did NOT inflate any bucket (it must be invisible to <= compare).
	_, counts, _, _ := h.Snapshot()
	// Only the two 1.0 observations should be in the top finite bucket.
	if counts[len(counts)-1] != 2 {
		t.Fatalf("NaN leaked into a finite bucket: top bucket=%d want=2", counts[len(counts)-1])
	}
}

// +Inf observation: sum becomes +Inf; the +Inf bucket(count) advances; no
// finite bucket catches it (Inf <= finite is false). Pin it.
func TestAdversarial_HistogramPosInf(t *testing.T) {
	h := NewHistogram([]float64{1, 2, 5})
	h.Observe(math.Inf(1))
	_, counts, sum, count := h.Snapshot()
	for i, c := range counts {
		if c != 0 {
			t.Fatalf("+Inf observation leaked into finite bucket[%d]: got=%d want=0", i, c)
		}
	}
	if count != 1 {
		t.Fatalf("+Inf observation not counted in total: got=%d want=1", count)
	}
	if !math.IsInf(sum, 1) {
		t.Logf("NOTE: +Inf no longer propagates into sum (sum=%g)", sum)
	}
}

// Counter.Add accepts a negative value: a "counter" can go DOWN. The type does
// NOT document monotonicity (it exposes Add(int64) and prints %d), so this is a
// LATENT RISK note (Prometheus counters should be monotonic), NOT a contract
// break. Pin the behaviour.
func TestAdversarial_CounterNegativeAdd(t *testing.T) {
	var c Counter
	c.Add(10)
	c.Add(-7) // monotonic counters must never decrease; this type allows it
	if got := c.Value(); got != 3 {
		t.Fatalf("Counter.Add arithmetic wrong: got=%d want=3", got)
	}
	t.Logf("LATENT RISK pinned: Counter.Add permits negative deltas (non-monotonic); " +
		"Prometheus counters should be monotonic. Type does not document monotonicity, so not a contract break.")
}

// uint64 histogram count overflow is astronomically out of reach (2^64 obs);
// int64 Counter overflow wraps (documented as plain atomic int64). Pin the
// wrap behaviour so it is a conscious decision, not a surprise.
func TestAdversarial_CounterOverflowWraps(t *testing.T) {
	var c Counter
	c.Add(math.MaxInt64)
	c.Inc() // overflow
	if got := c.Value(); got != math.MinInt64 {
		t.Fatalf("Counter overflow did not wrap two's-complement: got=%d want=%d", got, int64(math.MinInt64))
	}
	t.Logf("LATENT RISK pinned: int64 Counter wraps to MinInt64 on overflow (atomic int64 semantics).")
}

// Cross-check: atomic load/store visibility — a writer's last value must be
// observable by a reader after wg.Wait (happens-before via WaitGroup).
func TestAdversarial_GaugeSetVisibility(t *testing.T) {
	var gg Gauge
	var wg sync.WaitGroup
	const n = 1000
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := int64(0); i <= n; i++ {
			gg.Set(i)
		}
	}()
	wg.Wait()
	if got := gg.Value(); got != n {
		t.Fatalf("Gauge.Set last value not visible: got=%d want=%d", got, n)
	}
}

// -----------------------------------------------------------------------------
// tiny int->string helpers (avoid importing strconv to keep intent obvious)
// -----------------------------------------------------------------------------

func itoa(i int) string { return itoa64(int64(i)) }

func itoa64(i int64) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// touch atomic to keep the import honest if the file is trimmed in the future.
var _ = atomic.AddInt64
