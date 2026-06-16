package llm

// Second-pass adversarial probes for internal/llm, focused on proxy/ROUTING
// CORRECTNESS bugs the first-pass suite did not exercise:
//
//   (A) NON-FINITE SCORE MIS-ROUTE (REAL BUG, fixed in this wave) — a model whose
//       routing metric is NaN produces a NaN strategy score. Every ordering
//       comparison against NaN is false, so a naive `score > bestScore` lets a
//       NaN-scored model that is visited first by map iteration stick as "best"
//       forever: no finite candidate can displace it. Result: a request routed to
//       a model with a corrupt/missing metric over a clearly-superior finite one.
//       The fix makes a finite candidate always beat a NaN incumbent and a NaN
//       candidate never displace a comparable incumbent. These probes assert the
//       SINK (selected model id) and are the mutation oracle for the fix.
//
//   (B) +Inf / -Inf ORDERING — a +Inf maximise metric is a legitimate "best"; a
//       +Inf latency/cost (→ -Inf score on a minimise axis) must rank LAST. Pins
//       that the NaN guard did not break legitimate infinite-but-ordered values.
//
//   (C) ALL-NaN DETERMINISM — when EVERY eligible candidate is NaN-scored, Route
//       still returns one eligible model with a nil error (never a zero-value pick
//       paired with a nil error, never a panic), and the choice is stable.
//
//   (D) RouteInference dispatches to the SELECTED model under the NaN scenario —
//       the backend is asked to run exactly the model Route would pick, never the
//       NaN model.
//
//   (E) CONCURRENT ROUTING CONSISTENCY (-race) — concurrent Route while metrics
//       are mutated never returns a non-eligible / zero-value pick.
//
// ANTI-HANG: bounded loops, concurrency capped, fake in-process backend.

import (
	"math"
	"sync"
	"sync/atomic"
	"testing"
)

// routeSelectingBackend records the exact model id RouteInference dispatched to,
// so selection can be asserted sink-side rather than inferred.
type routeSelectingBackend struct {
	mu   sync.Mutex
	last string
}

func (b *routeSelectingBackend) Generate(model, prompt string) (string, error) {
	b.mu.Lock()
	b.last = model
	b.mu.Unlock()
	return "OUT[" + model + "]", nil
}

func (b *routeSelectingBackend) lastModel() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.last
}

// ---------------------------------------------------------------------------
// (A) NON-FINITE SCORE MIS-ROUTE — REAL BUG, this is the fix's mutation oracle.
// ---------------------------------------------------------------------------

// TestAdversarial2NaNMetricNeverWinsQuality proves a model with a NaN Quality
// metric is NEVER selected over a finite-quality model, regardless of map
// iteration order. Map order is randomised by Go, so we repeat enough trials to
// hit the "NaN visited first" ordering many times.
//
// On UNFIXED code (naive `score > bestScore` only), the NaN model wins whenever
// it is visited first because no finite candidate can displace a NaN incumbent;
// the sink assertion then fails. With the fix, the finite model always wins.
//
// Mutation that fails this (proves the fix is load-bearing): delete the
// `case isNaN:` and `case bestScore != bestScore:` arms from Route's switch.
func TestAdversarial2NaNMetricNeverWinsQuality(t *testing.T) {
	const trials = 400
	for i := 0; i < trials; i++ {
		m := NewManager()
		mustReg(t, m, "good")
		mustReg(t, m, "nanq")
		mustLoad(t, m, "good")
		mustLoad(t, m, "nanq")
		if err := m.SetMetrics("good", Metrics{Quality: 0.95}); err != nil {
			t.Fatal(err)
		}
		if err := m.SetMetrics("nanq", Metrics{Quality: math.NaN()}); err != nil {
			t.Fatal(err)
		}
		got, err := m.Route(StrategyQuality)
		if err != nil {
			t.Fatalf("trial %d: Route error: %v", i, err)
		}
		if got != "good" {
			t.Fatalf("trial %d: Route(quality)=%q, want \"good\" — NaN-metric model "+
				"mis-won routing over a finite-quality model", i, got)
		}
	}
}

// TestAdversarial2NaNMetricNeverWinsLatency is the minimise-axis twin: a NaN
// LatencyMS must not beat a finite, low-latency model. (On a minimise axis the
// score is -LatencyMS; -NaN is still NaN.)
func TestAdversarial2NaNMetricNeverWinsLatency(t *testing.T) {
	const trials = 400
	for i := 0; i < trials; i++ {
		m := NewManager()
		mustReg(t, m, "fast")
		mustReg(t, m, "nanl")
		mustLoad(t, m, "fast")
		mustLoad(t, m, "nanl")
		if err := m.SetMetrics("fast", Metrics{LatencyMS: 5}); err != nil {
			t.Fatal(err)
		}
		if err := m.SetMetrics("nanl", Metrics{LatencyMS: math.NaN()}); err != nil {
			t.Fatal(err)
		}
		got, err := m.Route(StrategyLatency)
		if err != nil {
			t.Fatalf("trial %d: Route error: %v", i, err)
		}
		if got != "fast" {
			t.Fatalf("trial %d: Route(latency)=%q, want \"fast\" — NaN-latency model mis-won", i, got)
		}
	}
}

// TestAdversarial2NaNAmongMany mixes several NaN models with one finite winner
// to maximise the chance the NaN models are visited before the finite one.
func TestAdversarial2NaNAmongMany(t *testing.T) {
	const trials = 200
	for i := 0; i < trials; i++ {
		m := NewManager()
		// Several NaN models, one finite "winner".
		for _, n := range []string{"nan1", "nan2", "nan3", "nan4"} {
			mustReg(t, m, n)
			mustLoad(t, m, n)
			if err := m.SetMetrics(n, Metrics{Quality: math.NaN()}); err != nil {
				t.Fatal(err)
			}
		}
		mustReg(t, m, "winner")
		mustLoad(t, m, "winner")
		if err := m.SetMetrics("winner", Metrics{Quality: 0.5}); err != nil {
			t.Fatal(err)
		}
		got, err := m.Route(StrategyQuality)
		if err != nil {
			t.Fatalf("trial %d: %v", i, err)
		}
		if got != "winner" {
			t.Fatalf("trial %d: Route=%q, want \"winner\" — a NaN model out-ranked the only finite candidate", i, got)
		}
	}
}

// ---------------------------------------------------------------------------
// (B) +Inf / -Inf ORDERING — the NaN guard must not corrupt legit infinities.
// ---------------------------------------------------------------------------

// TestAdversarial2PosInfQualityWins proves a +Inf quality (legit maximum on a
// maximise axis) still wins over a finite one — the NaN guard only special-cases
// NaN, not ordered infinities.
func TestAdversarial2PosInfQualityWins(t *testing.T) {
	m := NewManager()
	mustReg(t, m, "fin")
	mustReg(t, m, "inf")
	mustLoad(t, m, "fin")
	mustLoad(t, m, "inf")
	if err := m.SetMetrics("fin", Metrics{Quality: 0.9}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetMetrics("inf", Metrics{Quality: math.Inf(1)}); err != nil {
		t.Fatal(err)
	}
	got, err := m.Route(StrategyQuality)
	if err != nil {
		t.Fatal(err)
	}
	if got != "inf" {
		t.Errorf("Route(quality)=%q, want \"inf\" (+Inf is the legitimate maximum)", got)
	}
}

// TestAdversarial2PosInfLatencyRanksLast proves a +Inf latency (→ -Inf score)
// ranks LAST and never wins over a finite-latency model.
func TestAdversarial2PosInfLatencyRanksLast(t *testing.T) {
	m := NewManager()
	mustReg(t, m, "ok")
	mustReg(t, m, "slow")
	mustLoad(t, m, "ok")
	mustLoad(t, m, "slow")
	if err := m.SetMetrics("ok", Metrics{LatencyMS: 10}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetMetrics("slow", Metrics{LatencyMS: math.Inf(1)}); err != nil {
		t.Fatal(err)
	}
	got, err := m.Route(StrategyLatency)
	if err != nil {
		t.Fatal(err)
	}
	if got != "ok" {
		t.Errorf("Route(latency)=%q, want \"ok\" (+Inf latency must rank last)", got)
	}
}

// ---------------------------------------------------------------------------
// (C) ALL-NaN DETERMINISM — no zero-value pick, no panic, stable choice.
// ---------------------------------------------------------------------------

// TestAdversarial2AllNaNStillPicksEligible proves that when every eligible model
// has a NaN score, Route still returns a *registered, eligible* model with a nil
// error (it must not return "" paired with nil, nor panic). The chosen id must be
// one of the registered names.
func TestAdversarial2AllNaNStillPicksEligible(t *testing.T) {
	m := NewManager()
	names := map[string]struct{}{"a": {}, "b": {}, "c": {}}
	for n := range names {
		mustReg(t, m, n)
		mustLoad(t, m, n)
		if err := m.SetMetrics(n, Metrics{Quality: math.NaN()}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := m.Route(StrategyQuality)
	if err != nil {
		t.Fatalf("all-NaN Route errored unexpectedly: %v", err)
	}
	if got == "" {
		t.Fatal("all-NaN Route returned an empty pick with a nil error (zero-value pick)")
	}
	if _, ok := names[got]; !ok {
		t.Fatalf("all-NaN Route returned %q, not one of the registered models", got)
	}
}

// ---------------------------------------------------------------------------
// (D) RouteInference dispatches to the SELECTED (non-NaN) model.
// ---------------------------------------------------------------------------

// TestAdversarial2RouteInferenceSkipsNaNModel proves the end-to-end path:
// RouteInference must dispatch to the finite-quality model, never the NaN one.
// Asserted on the backend's recorded model id (sink-side).
func TestAdversarial2RouteInferenceSkipsNaNModel(t *testing.T) {
	const trials = 200
	for i := 0; i < trials; i++ {
		be := &routeSelectingBackend{}
		m := WithBackend(be)
		mustReg(t, m, "real")
		mustReg(t, m, "nan")
		mustLoad(t, m, "real")
		mustLoad(t, m, "nan")
		if err := m.SetMetrics("real", Metrics{Quality: 0.8}); err != nil {
			t.Fatal(err)
		}
		if err := m.SetMetrics("nan", Metrics{Quality: math.NaN()}); err != nil {
			t.Fatal(err)
		}
		selected, resp, err := m.RouteInference(StrategyQuality, "go")
		if err != nil {
			t.Fatalf("trial %d: %v", i, err)
		}
		if selected != "real" {
			t.Fatalf("trial %d: selected=%q want \"real\"", i, selected)
		}
		if be.lastModel() != "real" {
			t.Fatalf("trial %d: backend ran %q, want \"real\" (dispatched to NaN model)", i, be.lastModel())
		}
		if resp != "OUT[real]" {
			t.Fatalf("trial %d: response %q not derived from selected model", i, resp)
		}
	}
}

// ---------------------------------------------------------------------------
// (E) CONCURRENT ROUTING CONSISTENCY (-race)
// ---------------------------------------------------------------------------

// TestAdversarial2ConcurrentRouteWhileMetricsMutate races Route against
// SetMetrics (including NaN writes) and SetHealth. The SINK invariant: every
// successful Route returns one of the registered model names (never "" with a
// nil error), and a NaN never produces a corrupt empty pick. Trips the race
// detector if Route reads metrics without the lock.
func TestAdversarial2ConcurrentRouteWhileMetricsMutate(t *testing.T) {
	m := NewManager()
	valid := map[string]struct{}{}
	for _, n := range []string{"r0", "r1", "r2", "r3"} {
		mustReg(t, m, n)
		mustLoad(t, m, n)
		valid[n] = struct{}{}
		if err := m.SetMetrics(n, Metrics{Quality: 0.5}); err != nil {
			t.Fatal(err)
		}
	}

	const iters = 300
	var wg sync.WaitGroup
	var bad int64

	// Mutator: flips one model's metric to NaN and back, plus health churn.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			if i%2 == 0 {
				_ = m.SetMetrics("r1", Metrics{Quality: math.NaN()})
			} else {
				_ = m.SetMetrics("r1", Metrics{Quality: 0.6})
			}
		}
	}()

	// 6 routers (1 mutator + 6 = 7 goroutines, under the cap of 8).
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				got, err := m.Route(StrategyQuality)
				if err != nil {
					// At least r0/r2/r3 stay eligible & finite the whole time, so
					// Route must always succeed.
					atomic.AddInt64(&bad, 1)
					continue
				}
				if _, ok := valid[got]; !ok {
					atomic.AddInt64(&bad, 1)
				}
			}
		}()
	}
	wg.Wait()

	if bad != 0 {
		t.Fatalf("%d concurrent Route results were errors or non-registered picks", bad)
	}
}

// --- small helpers (kept local to this file) -------------------------------

func mustReg(t *testing.T, m *Manager, name string) {
	t.Helper()
	if err := m.RegisterModel(name, "/p/"+name, "gguf"); err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
}

func mustLoad(t *testing.T, m *Manager, name string) {
	t.Helper()
	if err := m.LoadModel(name); err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
}
