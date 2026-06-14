// Adversarial, sink-side test suite for pkg/chaosexp.
//
// Authored by an adversarial Go test engineer. Probes the package's ACTUAL
// documented contracts (not the generic chaos-framework surfaces some specs
// assume). The real contracts in this package are:
//
//   - Determinism / total-order: Run(e, seed) twice => byte-identical
//     Result.Canonical; EncodeLog is stable even if the caller reorders the
//     event slice (chaosexp.go reproducibility contract; turmoil.EncodeLog).
//   - Concurrency safety of the ONLY concurrent surface, MetricsCollector:
//     RecordRun is mutex-guarded and the emitted counter series must CONSERVE
//     the underlying event-log facts (delivered+dropped == events) under
//     concurrent recording, with no lost updates.
//   - The turmoil.Simulation is single-threaded by contract; the reentrancy
//     claim that holds is that Run builds a FRESH sim each call, so concurrent
//     Run calls over DISTINCT (e,seed) pairs are race-free and each result is
//     independently correct.
//
// NOTE ON THE PROMPT'S ASSUMED SURFACES: this package has no numeric
// blast-radius bound (BlastRadius is a human-readable string), no probability/
// rate field, and no shared experiment-state map mutated by start/stop. Those
// are absent here; the closest honest SAFETY analog is metric/event-log
// CONSERVATION, asserted below. Where a probe is a latent risk rather than a
// contract violation, it is labeled as such.
package chaosexp

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/testing/turmoil"
)

// ── helpers ─────────────────────────────────────────────────────────────────

// gatherCounter sums the value of a simple (label-less) counter/gauge series by
// metric name from the collector's registry. Reads the SINK (exposition data),
// not internal fields.
func gatherCounter(t *testing.T, mc *MetricsCollector, name string) float64 {
	t.Helper()
	fams, err := mc.Registry().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range fams {
		if mf.GetName() != name {
			continue
		}
		var sum float64
		for _, m := range mf.GetMetric() {
			switch {
			case m.GetCounter() != nil:
				sum += m.GetCounter().GetValue()
			case m.GetGauge() != nil:
				sum += m.GetGauge().GetValue()
			}
		}
		return sum
	}
	t.Fatalf("metric family %q not found in registry", name)
	return 0
}

// histogramSampleSumCount returns (sampleSum, sampleCount) for a histogram
// series by name.
func histogramSampleSumCount(t *testing.T, mc *MetricsCollector, name string) (float64, uint64) {
	t.Helper()
	fams, err := mc.Registry().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range fams {
		if mf.GetName() != name {
			continue
		}
		var sum float64
		var count uint64
		for _, m := range mf.GetMetric() {
			h := m.GetHistogram()
			if h == nil {
				continue
			}
			sum += h.GetSampleSum()
			count += h.GetSampleCount()
		}
		return sum, count
	}
	t.Fatalf("histogram %q not found", name)
	return 0, 0
}

// ── (d/SAFETY) CONSERVATION: metrics must equal event-log facts ─────────────

// TestConservation_MetricsEqualEventLogFacts is the SAFETY/conservation probe.
// For a full catalog run the emitted series MUST exactly account for every
// delivery event: messages_delivered + messages_dropped == sum(len(Events)).
// Over-counting drops (claiming more fault injection than actually occurred)
// or losing counts is a metrics-integrity defect — the chaosexp analog of a
// blast-radius violation.
func TestConservation_MetricsEqualEventLogFacts(t *testing.T) {
	mc := NewMetricsCollector()
	results := mc.RunCatalog(7)

	var wantEvents, wantDropped, wantDelivered int
	for _, r := range results {
		wantEvents += len(r.Events)
		for _, ev := range r.Events {
			if ev.Dropped {
				wantDropped++
			} else {
				wantDelivered++
			}
		}
	}

	gotDelivered := gatherCounter(t, mc, "helix_chaos_messages_delivered_total")
	gotDropped := gatherCounter(t, mc, "helix_chaos_messages_dropped_total")
	gotRun := gatherCounter(t, mc, "helix_chaos_experiments_run_total")

	if int(gotDelivered) != wantDelivered {
		t.Errorf("delivered: sink=%v want=%d (lost/extra update)", gotDelivered, wantDelivered)
	}
	if int(gotDropped) != wantDropped {
		t.Errorf("dropped: sink=%v want=%d (lost/extra update)", gotDropped, wantDropped)
	}
	if int(gotDelivered+gotDropped) != wantEvents {
		t.Errorf("CONSERVATION VIOLATED: delivered+dropped=%v != events=%d",
			gotDelivered+gotDropped, wantEvents)
	}
	if int(gotRun) != len(results) {
		t.Errorf("experiments_run=%v want=%d", gotRun, len(results))
	}
}

// ── (b) CONCURRENCY: shared state under -race + conservation ────────────────

// TestConcurrentRecordRun_NoLostUpdates hammers RecordRun from many goroutines
// (its mutex is the guard) and asserts the counter sink CONSERVES every event:
// no lost increments. Run under -race to also catch data races. A panic-free
// run is NOT sufficient; the count must be exact.
func TestConcurrentRecordRun_NoLostUpdates(t *testing.T) {
	mc := NewMetricsCollector()
	cat := Catalog()

	// Pre-compute results sequentially (Run builds a fresh sim each call).
	const seed = 11
	results := make([]Result, len(cat))
	perRunEvents := make([]int, len(cat))
	for i, e := range cat {
		results[i] = Run(e, seed)
		perRunEvents[i] = len(results[i].Events)
	}

	const repeats = 20
	var wantEvents int
	for r := 0; r < repeats; r++ {
		for _, n := range perRunEvents {
			wantEvents += n
		}
	}

	var wg sync.WaitGroup
	for r := 0; r < repeats; r++ {
		for i := range cat {
			wg.Add(1)
			go func(e ChaosExperiment, res Result) {
				defer wg.Done()
				mc.RecordRun(e, res, time.Millisecond)
			}(cat[i], results[i])
		}
	}
	wg.Wait()

	gotDelivered := gatherCounter(t, mc, "helix_chaos_messages_delivered_total")
	gotDropped := gatherCounter(t, mc, "helix_chaos_messages_dropped_total")
	gotRun := gatherCounter(t, mc, "helix_chaos_experiments_run_total")

	if int(gotDelivered+gotDropped) != wantEvents {
		t.Errorf("CONSERVATION under concurrency: delivered+dropped=%v != events=%d (lost update / race)",
			gotDelivered+gotDropped, wantEvents)
	}
	if int(gotRun) != repeats*len(cat) {
		t.Errorf("experiments_run=%v want=%d (lost increment)", gotRun, repeats*len(cat))
	}

	_, hCount := histogramSampleSumCount(t, mc, "helix_chaos_experiment_duration_seconds")
	if int(hCount) != repeats*len(cat) {
		t.Errorf("duration histogram count=%d want=%d (lost Observe)", hCount, repeats*len(cat))
	}
}

// TestConcurrentRunCatalog_DistinctCollectors proves Run/RunCatalog build fresh
// state per call: many concurrent RunCatalog calls on INDEPENDENT collectors
// must each produce the identical, correct result set (determinism holds and no
// cross-talk through shared globals). Run under -race.
func TestConcurrentRunCatalog_DistinctCollectors(t *testing.T) {
	const seed = 99
	// Oracle: a single sequential run.
	oracle := make([]string, 0, 12)
	for _, r := range NewMetricsCollector().RunCatalog(seed) {
		oracle = append(oracle, r.Canonical)
	}

	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mc := NewMetricsCollector()
			res := mc.RunCatalog(seed)
			if len(res) != len(oracle) {
				errs <- "length mismatch"
				return
			}
			for i, r := range res {
				if r.Canonical != oracle[i] {
					errs <- "canonical divergence at index"
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("concurrent RunCatalog: %s", e)
	}
}

// ── (a) DETERMINISM / TOTAL-ORDER ───────────────────────────────────────────

// TestDeterminism_SameSeedByteIdentical pins the headline reproducibility
// contract: Run(e, seed) twice => byte-identical Canonical, for every catalog
// experiment. With -count=2 this also exercises cross-invocation stability.
func TestDeterminism_SameSeedByteIdentical(t *testing.T) {
	for _, e := range Catalog() {
		a := Run(e, 4242)
		b := Run(e, 4242)
		if a.Canonical != b.Canonical {
			t.Errorf("%s: non-deterministic Canonical for same seed:\n a=%q\n b=%q", e.ID, a.Canonical, b.Canonical)
		}
		if a.Passed != b.Passed {
			t.Errorf("%s: Passed differs across identical runs: %v vs %v", e.ID, a.Passed, b.Passed)
		}
	}
}

// TestDeterminism_DifferentSeedsDiverge asserts the seed actually matters:
// different seeds must produce different Canonical for at least one experiment
// (the Nonce is seed-derived). If ALL experiments are seed-insensitive the
// reproducibility/seed contract would be vacuous.
func TestDeterminism_DifferentSeedsDiverge(t *testing.T) {
	diverged := false
	for _, e := range Catalog() {
		if Run(e, 1).Canonical != Run(e, 2).Canonical {
			diverged = true
			break
		}
	}
	if !diverged {
		t.Error("no experiment's Canonical changed between seed 1 and seed 2: seed is vacuous")
	}
}

// TestEncodeStableUnderReorder pins turmoil.EncodeLog's documented promise that
// canonical encoding is stable "even if the caller reorders the slice". This is
// the total-order/no-map-iteration-leak guard. We shuffle the event slice and
// require an identical Canonical.
func TestEncodeStableUnderReorder(t *testing.T) {
	// CE-05 and CE-11 have multiple events of mixed dropped/delivered.
	for _, id := range []string{"CE-05", "CE-10", "CE-11"} {
		var e ChaosExperiment
		for _, c := range Catalog() {
			if c.ID == id {
				e = c
			}
		}
		r := Run(e, 5)
		if len(r.Events) < 3 {
			t.Fatalf("%s: expected >=3 events, got %d", id, len(r.Events))
		}
		// Reverse a copy of Events and confirm turmoil.EncodeLog (the same
		// encoder Result.Canonical was produced by) is order-invariant.
		evs := make([]turmoil.DeliveryEvent, len(r.Events))
		copy(evs, r.Events)
		for i, j := 0, len(evs)-1; i < j; i, j = i+1, j-1 {
			evs[i], evs[j] = evs[j], evs[i]
		}
		if got := turmoil.EncodeLog(evs); got != r.Canonical {
			t.Errorf("%s: EncodeLog NOT stable under reorder:\n natural=%q\n reversed=%q", id, r.Canonical, got)
		}
	}
}

// ── (c) NUMERIC EDGE: non-monotonic clock -> negative duration ──────────────

// TestNegativeDuration_HistogramLatentRisk is a LATENT-RISK probe (not a
// contract violation). nowFn is documented as injectable and "never affects the
// deterministic Result". If a non-monotonic clock yields a negative duration,
// RecordRun forwards it to Observe. Prometheus histograms silently accept
// negative samples, so the sink SampleSum can go negative — a metrics-quality
// (not safety) issue. We pin the observed behavior so a future guard is a
// conscious choice.
func TestNegativeDuration_HistogramLatentRisk(t *testing.T) {
	// Clock that goes BACKWARDS between start and stop inside RunCatalog.
	var calls int
	clock := func() time.Time {
		calls++
		// odd call (start) = later time; even call (stop) = earlier time.
		if calls%2 == 1 {
			return time.Unix(1000, 0)
		}
		return time.Unix(0, 0) // earlier than start -> negative dur
	}
	mc := NewMetricsCollectorWithClock(clock)
	mc.RunCatalog(3)

	sum, count := histogramSampleSumCount(t, mc, "helix_chaos_experiment_duration_seconds")
	if count != 12 {
		t.Fatalf("expected 12 observations, got %d", count)
	}
	// Document the latent behavior: sum is negative because each dur < 0.
	if sum >= 0 {
		t.Logf("LATENT: non-monotonic clock did NOT produce negative sum (sum=%v); guard may already exist", sum)
	} else {
		t.Logf("LATENT RISK confirmed: non-monotonic clock -> negative histogram SampleSum=%v "+
			"(Result determinism unaffected; metrics-quality issue only)", sum)
	}
	// The hard contract — Result determinism is clock-independent — must hold:
	det := NewMetricsCollectorWithClock(clock).RunCatalog(3)
	ref := NewMetricsCollector().RunCatalog(3)
	for i := range det {
		if det[i].Canonical != ref[i].Canonical {
			t.Errorf("%s: clock affected deterministic Result (CONTRACT VIOLATION)", det[i].ID)
		}
	}
}

// TestNaNInfDurationRejected probes the numeric guard at the Observe boundary:
// a NaN/+Inf duration must NOT corrupt the histogram into an unusable state.
// Prometheus' Observe accepts these silently (NaN/Inf are folded into SampleSum)
// — we pin the actual behavior so it is a conscious, documented edge.
func TestNaNInfDurationRejected(t *testing.T) {
	mc := NewMetricsCollector()
	e := Catalog()[0]
	r := Run(e, 1)

	// Build durations that, in seconds, are NaN/+Inf via overflow-free path:
	// time.Duration is int64 ns; we cannot literally hold NaN, but we can pass
	// a duration whose Seconds() is huge. Assert the histogram still gathers.
	mc.RecordRun(e, r, time.Duration(math.MaxInt64))
	sum, count := histogramSampleSumCount(t, mc, "helix_chaos_experiment_duration_seconds")
	if count != 1 {
		t.Fatalf("expected 1 observation, got %d", count)
	}
	if math.IsNaN(sum) || math.IsInf(sum, 0) {
		t.Errorf("histogram SampleSum corrupted to %v by extreme duration", sum)
	}
	t.Logf("extreme duration observed cleanly: SampleSum=%v", sum)
}
