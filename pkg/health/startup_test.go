package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// startupFail is a Startup probe that has not yet succeeded.
func startupFail(ctx context.Context) error { return errors.New("still initializing") }

// resultByName indexes rollup results by component name.
func resultByName(rollup Rollup) map[string]DependencyResult {
	m := make(map[string]DependencyResult, len(rollup.Checks))
	for _, c := range rollup.Checks {
		m[c.Name] = c
	}
	return m
}

// --- 1. A still-starting component reports Starting (not Unhealthy) and does
// NOT trip liveness, even though its liveness probe is failing. ---
//
// Mutation that this kills: delete the `if isStarting { ... return }` gate in
// DependencyRollup.check (i.e. always call r.runOne and ignore startup). Then
// the failing critical liveness probe runs and overall becomes Unhealthy, so the
// `Starting, not Unhealthy` assertion fails.
func TestStartup_StartingNotUnhealthy_DoesNotTripLiveness(t *testing.T) {
	r := NewRollup()
	// Liveness probe is failing AND critical — without startup gating this would
	// force Unhealthy.
	r.Register(Dependency{
		Name:     "svc",
		Kind:     Liveness,
		Critical: true,
		Check:    func(ctx context.Context) error { return errors.New("not up yet") },
	})
	// Startup probe is failing -> component is inside its grace window.
	r.RegisterStartup("svc", Dependency{Check: startupFail})

	live := r.CheckLiveness(context.Background())
	if live.Status != Starting {
		t.Fatalf("starting component must report Starting, got %s (%+v)", live.Status, live.Checks)
	}
	if live.Status == Unhealthy {
		t.Fatal("a starting component must NOT be Unhealthy for being un-ready")
	}
	res := resultByName(live)["svc"]
	if res.Status != Starting {
		t.Errorf("per-check status for starting component must be Starting, got %s", res.Status)
	}
	// Sink-side: the failing liveness probe must not have run / not have leaked an
	// error, proving it was actually gated rather than coincidentally healthy.
	if res.Error != "" {
		t.Errorf("gated startup component must not surface the liveness error, got %q", res.Error)
	}
}

// --- 2. Once Startup passes it latches; a LATER liveness failure now DOES report
// Unhealthy (the grace window is over). ---
//
// Mutation that this kills: in evaluateStartup, drop `r.gate.MarkComplete(name)`
// on a Healthy probe (never latch). Then the component is re-gated on the second
// CheckLiveness and stays Starting forever, so the post-latch Unhealthy assertion
// fails.
func TestStartup_LatchesThenLaterLivenessFailureUnhealthy(t *testing.T) {
	r := NewRollup()

	livenessErr := make(chan struct{}) // closed -> liveness starts failing
	r.Register(Dependency{
		Name:     "svc",
		Kind:     Liveness,
		Critical: true,
		Check: func(ctx context.Context) error {
			select {
			case <-livenessErr:
				return errors.New("crashed post-startup")
			default:
				return nil
			}
		},
	})

	startupOK := make(chan struct{}) // closed -> startup probe succeeds
	r.RegisterStartup("svc", Dependency{
		Check: func(ctx context.Context) error {
			select {
			case <-startupOK:
				return nil
			default:
				return errors.New("starting")
			}
		},
	})

	// Phase A: startup failing -> Starting, liveness gated.
	if got := r.CheckLiveness(context.Background()).Status; got != Starting {
		t.Fatalf("phase A: expected Starting, got %s", got)
	}
	if r.StartupComplete("svc") {
		t.Fatal("phase A: component must not be latched complete while startup fails")
	}

	// Phase B: startup succeeds -> latches complete, liveness still healthy.
	close(startupOK)
	if got := r.CheckLiveness(context.Background()).Status; got != Healthy {
		t.Fatalf("phase B: after startup success liveness must be Healthy, got %s", got)
	}
	if !r.StartupComplete("svc") {
		t.Fatal("phase B: successful startup probe must latch StartupComplete")
	}

	// Phase C: liveness now fails. Because startup already latched, the grace
	// window is over and the failure MUST surface as Unhealthy.
	close(livenessErr)
	res := r.CheckLiveness(context.Background())
	if res.Status != Unhealthy {
		t.Fatalf("phase C: post-latch critical liveness failure must be Unhealthy, got %s", res.Status)
	}
	if resultByName(res)["svc"].Error == "" {
		t.Error("phase C: surfaced failure must carry the liveness error")
	}
}

// --- 3. Latch is sticky: once complete, a transient startup probe re-fail must
// NOT re-open the grace window (so a real post-startup failure is not masked). ---
//
// Mutation that this kills: in evaluateStartup, remove the leading
// `if r.gate.IsComplete(name) { return false, ... }` short-circuit. Then a probe
// that fails again re-gates the component to Starting and the Unhealthy assertion
// fails.
func TestStartup_LatchIsSticky(t *testing.T) {
	r := NewRollup()
	r.Register(Dependency{
		Name:     "svc",
		Kind:     Readiness,
		Critical: true,
		Check:    failCheck,
	})

	flap := make(chan struct{}) // open: startup ok; closed: startup fails again
	r.RegisterStartup("svc", Dependency{
		Check: func(ctx context.Context) error {
			select {
			case <-flap:
				return errors.New("startup probe flapped")
			default:
				return nil // succeeds on first evaluation -> latches
			}
		},
	})

	// First check latches complete (startup ok) but readiness is failing -> Unhealthy.
	if got := r.CheckReadiness(context.Background()).Status; got != Unhealthy {
		t.Fatalf("first check: latched + failing critical readiness must be Unhealthy, got %s", got)
	}
	// Startup probe now flaps back to failing.
	close(flap)
	// Latch is sticky: still Unhealthy, NOT re-gated to Starting.
	if got := r.CheckReadiness(context.Background()).Status; got != Unhealthy {
		t.Fatalf("sticky latch: must stay Unhealthy after probe re-fails, got %s", got)
	}
}

// --- 4. Readiness is gated until Startup completes. ---
//
// Mutation that this kills: same gate deletion as test 1 but on the readiness
// path — without gating the failing critical readiness probe makes overall
// Unhealthy instead of Starting.
func TestStartup_ReadinessGatedUntilComplete(t *testing.T) {
	r := NewRollup()
	r.Register(Dependency{
		Name:     "db",
		Kind:     Readiness,
		Critical: true,
		Check:    failCheck, // readiness failing the whole time
	})

	startupOK := make(chan struct{})
	r.RegisterStartup("db", Dependency{
		Check: func(ctx context.Context) error {
			select {
			case <-startupOK:
				return nil
			default:
				return errors.New("warming up")
			}
		},
	})

	// While starting: readiness failure is masked -> Starting, served, gated.
	ready := r.CheckReadiness(context.Background())
	if ready.Status != Starting {
		t.Fatalf("readiness must be gated to Starting while startup pending, got %s", ready.Status)
	}

	// Startup completes: now the (still failing) critical readiness probe applies.
	close(startupOK)
	ready = r.CheckReadiness(context.Background())
	if ready.Status != Unhealthy {
		t.Fatalf("after startup completes, failing critical readiness must be Unhealthy, got %s", ready.Status)
	}
}

// --- 5. Backward-compat: with NO Startup probe registered, the original 2-tier
// rollup behavior is preserved (a critical failure is Unhealthy, never Starting). ---
//
// Mutation that this kills: make evaluateStartup return starting=true when a
// component has no startup probe (e.g. default-gate everything). Then this
// untouched component would report Starting instead of Unhealthy.
func TestStartup_NoProbePreserves2TierBehavior(t *testing.T) {
	r := NewRollup()
	r.Register(Dependency{Name: "ok", Kind: Readiness, Check: okCheck})
	r.Register(Dependency{Name: "db", Kind: Readiness, Critical: true, Check: failCheck})

	res := r.CheckReadiness(context.Background())
	if res.Status != Unhealthy {
		t.Fatalf("no startup probe: critical failure must be Unhealthy (2-tier preserved), got %s", res.Status)
	}
	if res.Status == Starting {
		t.Fatal("a component with no startup probe must never be Starting")
	}
	// Non-critical failure with no startup probe must still be Degraded.
	r2 := NewRollup()
	r2.Register(Dependency{Name: "cache", Kind: Readiness, Critical: false, Check: failCheck})
	if got := r2.CheckReadiness(context.Background()).Status; got != Degraded {
		t.Fatalf("no startup probe: non-critical failure must be Degraded, got %s", got)
	}
}

// --- 6. A real failure outranks Starting: when one component is Starting and a
// second, ungated component fails critically, overall is Unhealthy. ---
//
// Mutation that this kills: in the rollup loop, treat Starting as terminal for
// overall (e.g. `if res.Status == Starting { overall = Starting; continue }`
// placed so it wins over Unhealthy, or skipping the critical branch). Then the
// real critical failure would be hidden behind Starting.
func TestStartup_RealCriticalFailureOutranksStarting(t *testing.T) {
	r := NewRollup()
	// Component A: starting.
	r.Register(Dependency{Name: "a", Kind: Readiness, Critical: true, Check: failCheck})
	r.RegisterStartup("a", Dependency{Check: startupFail})
	// Component B: no startup probe, critical readiness failure.
	r.Register(Dependency{Name: "b", Kind: Readiness, Critical: true, Check: failCheck})

	res := r.CheckReadiness(context.Background())
	if res.Status != Unhealthy {
		t.Fatalf("a genuine critical failure must outrank a Starting peer, got %s", res.Status)
	}
	byName := resultByName(res)
	if byName["a"].Status != Starting {
		t.Errorf("component a should still individually report Starting, got %s", byName["a"].Status)
	}
	if byName["b"].Status != Unhealthy {
		t.Errorf("component b should report Unhealthy, got %s", byName["b"].Status)
	}
}

// --- 7. CheckStartup aggregate reports Starting while pending and Healthy once
// latched; component is listed in both states. ---
//
// Mutation that this kills: in CheckStartup, drop the `else if starting {
// res.Status = Starting }` so a pending probe is reported by its raw Unhealthy
// status; the Starting assertion fails.
func TestStartup_CheckStartupAggregate(t *testing.T) {
	r := NewRollup()
	startupOK := make(chan struct{})
	r.RegisterStartup("svc", Dependency{
		Check: func(ctx context.Context) error {
			select {
			case <-startupOK:
				return nil
			default:
				return errors.New("warming")
			}
		},
	})

	got := r.CheckStartup(context.Background())
	if got.Status != Starting {
		t.Fatalf("CheckStartup while pending must be Starting, got %s", got.Status)
	}
	if s := resultByName(got)["svc"].Status; s != Starting {
		t.Errorf("pending startup component must report Starting, got %s", s)
	}

	close(startupOK)
	got = r.CheckStartup(context.Background())
	if got.Status != Healthy {
		t.Fatalf("CheckStartup after success must be Healthy, got %s", got.Status)
	}
	if !r.StartupComplete("svc") {
		t.Error("CheckStartup success must latch StartupComplete")
	}
	// A subsequent CheckStartup still lists the now-complete component as Healthy.
	got = r.CheckStartup(context.Background())
	if s := resultByName(got)["svc"].Status; s != Healthy {
		t.Errorf("latched component must remain listed Healthy, got %s", s)
	}
}

// --- 8. StartupHandler: Starting serves 200 (orchestrator should wait, not
// restart) and the body carries the per-component Starting result. ---
//
// Mutation that this kills: change statusCode so Starting maps to 503 — the test
// asserts 200. Equally, dropping the Starting overall in CheckStartup would make
// the body Healthy and the body-status assertion fail.
func TestStartupHandler_StartingServes200(t *testing.T) {
	r := NewRollup()
	r.RegisterStartup("svc", Dependency{Check: startupFail})

	req := httptest.NewRequest(http.MethodGet, "/startupz", nil)
	rec := httptest.NewRecorder()
	StartupHandler(r).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Starting must serve 200 (keep waiting, do not restart), got %d", rec.Code)
	}
	var got Rollup
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != Starting {
		t.Errorf("body overall status must be Starting, got %s", got.Status)
	}
	if s := resultByName(got)["svc"].Status; s != Starting {
		t.Errorf("body must carry per-component Starting result, got %s", s)
	}
}

// --- 9. StartupGate latch primitive: MarkComplete is idempotent and IsComplete
// reflects it; an unmarked component is not complete. ---
//
// Mutation that this kills: make IsComplete always return true (or MarkComplete a
// no-op) — the unmarked-not-complete or marked-complete assertion fails.
func TestStartupGate_LatchSemantics(t *testing.T) {
	g := NewStartupGate()
	if g.IsComplete("x") {
		t.Fatal("fresh gate must report x incomplete")
	}
	g.MarkComplete("x")
	if !g.IsComplete("x") {
		t.Fatal("after MarkComplete, x must be complete")
	}
	g.MarkComplete("x") // idempotent
	if !g.IsComplete("x") {
		t.Fatal("MarkComplete must be idempotent")
	}
	if g.IsComplete("y") {
		t.Fatal("unrelated component y must remain incomplete")
	}
}

// --- 10. RegisterStartup forces Kind=Startup and Name=component, and the startup
// probe is NOT selected by CheckLiveness/CheckReadiness/CheckAll (it is addressed
// only via the startup tier). ---
//
// Mutation that this kills: store the startup probe under its raw Name (not
// startupKey) in RegisterStartup — then CheckAll would select it as an extra
// readiness/own-kind check and the count/exclusion assertions fail.
func TestStartup_ProbeNotSelectedByOtherTiers(t *testing.T) {
	r := NewRollup()
	r.Register(Dependency{Name: "svc", Kind: Liveness, Check: okCheck})
	r.RegisterStartup("svc", Dependency{Name: "ignored", Kind: Readiness, Check: startupFail})

	// CheckAll must see exactly the one liveness dep, not the startup probe.
	all := r.CheckAll(context.Background())
	if len(all.Checks) != 1 || all.Checks[0].Name != "svc" || all.Checks[0].Kind != Liveness {
		t.Fatalf("startup probe must not be selected by CheckAll, got %+v", all.Checks)
	}
	// CheckStartup sees exactly the startup probe, keyed by component name with
	// forced Kind=Startup.
	su := r.CheckStartup(context.Background())
	if len(su.Checks) != 1 || su.Checks[0].Name != "svc" || su.Checks[0].Kind != Startup {
		t.Fatalf("startup tier must address probe by component name with Kind=Startup, got %+v", su.Checks)
	}
}
