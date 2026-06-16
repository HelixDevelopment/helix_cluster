package main

// Adversarial, sink-side tests for the helix-health HTTP probe layer.
//
// The daemon exposes /readyz and /livez backed by the internal/health
// Aggregator (which folds unknown/malformed statuses to Unknown, never to
// Healthy). These tests assert the HTTP HANDLER honours that: a non-Healthy
// readiness/liveness aggregate MUST surface as 503 (not 200/ready). We probe
// the real handler via httptest.ResponseRecorder and assert the ACTUAL status
// code and JSON body — never "it compiles".
//
// The /health and /check/<svc> paths are backed by pkg/health.CompositeChecker
// (a SEPARATE checker). We pin its documented contract too, including the one
// place it does NOT fold an unknown status — flagged as a LATENT RISK below.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	ihealth "github.com/HelixDevelopment/helix_cluster/internal/health"
	pkghealth "github.com/HelixDevelopment/helix_cluster/pkg/health"
)

// kindReadiness registers a readiness check returning a fixed (status, err).
func registerReadiness(a *ihealth.Aggregator, name string, st ihealth.Status, err error) {
	a.RegisterCheckKind(name, ihealth.CheckFunc(
		func(ctx context.Context) (ihealth.Status, error) { return st, err },
	), ihealth.KindReadiness)
}

func registerLiveness(a *ihealth.Aggregator, name string, st ihealth.Status, err error) {
	a.RegisterCheckKind(name, ihealth.CheckFunc(
		func(ctx context.Context) (ihealth.Status, error) { return st, err },
	), ihealth.KindLiveness)
}

// hitReadyz drives the real /readyz handler through a recorder and returns the
// sink-side HTTP status code plus the decoded body.
func hitReadyz(t *testing.T, s *httpServer) (int, struct {
	Status string `json:"status"`
	Ready  bool   `json:"ready"`
	Probe  string `json:"probe"`
}) {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	s.readyz(rr, req)
	var body struct {
		Status string `json:"status"`
		Ready  bool   `json:"ready"`
		Probe  string `json:"probe"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /readyz body: %v (raw=%q)", err, rr.Body.String())
	}
	return rr.Code, body
}

func hitLivez(t *testing.T, s *httpServer) (int, string) {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/livez", nil)
	s.livez(rr, req)
	var body struct {
		Status string `json:"status"`
		Probe  string `json:"probe"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /livez body: %v (raw=%q)", err, rr.Body.String())
	}
	return rr.Code, body.Status
}

// -----------------------------------------------------------------------------
// (a) HANDLER FAIL-OPEN — the headline: a non-Healthy readiness aggregate must
// NOT yield 200/ready at the HTTP sink.
// -----------------------------------------------------------------------------

func TestAdversarial_Readyz_NonHealthyAggregate_MustBe503(t *testing.T) {
	// A malformed/unknown status value: ihealth.Status is an int enum; a value
	// outside the known set models a corrupted/unknown check result. The
	// internal/health fix folds this to Unknown; Ready() requires Healthy, so
	// the handler MUST emit 503. If the handler ever maps non-Healthy → 200,
	// THIS bites.
	malformed := ihealth.Status(99)

	cases := []struct {
		name string
		st   ihealth.Status
	}{
		{"critical readiness check", ihealth.Critical},
		{"degraded readiness check", ihealth.Degraded},
		{"explicit unknown readiness check", ihealth.Unknown},
		{"malformed/unrecognised status (fold must not be Healthy)", malformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := ihealth.NewAggregator()
			registerReadiness(a, "dep", tc.st, nil)
			a.RunChecks(context.Background())

			s := newHTTPServer(a)
			code, body := hitReadyz(t, s)

			if code != http.StatusServiceUnavailable {
				t.Fatalf("FAIL-OPEN: /readyz returned %d for non-Healthy readiness aggregate %v; want 503. body=%+v",
					code, tc.st, body)
			}
			if body.Ready {
				t.Fatalf("FAIL-OPEN: /readyz JSON reports ready=true for non-Healthy aggregate %v; want ready=false. code=%d", tc.st, code)
			}
		})
	}
}

func TestAdversarial_Livez_NonHealthyAggregate_MustBe503(t *testing.T) {
	malformed := ihealth.Status(99)
	cases := []struct {
		name string
		st   ihealth.Status
	}{
		{"critical liveness check", ihealth.Critical},
		{"degraded liveness check", ihealth.Degraded},
		{"explicit unknown liveness check", ihealth.Unknown},
		{"malformed/unrecognised liveness status", malformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := ihealth.NewAggregator()
			registerLiveness(a, "live", tc.st, nil)
			a.RunChecks(context.Background())

			s := newHTTPServer(a)
			code, status := hitLivez(t, s)
			if code != http.StatusServiceUnavailable {
				t.Fatalf("FAIL-OPEN: /livez returned %d for non-Healthy liveness aggregate %v; want 503 (status=%q)", code, tc.st, status)
			}
		})
	}
}

// Positive control: a genuinely healthy readiness aggregate IS 200/ready, so
// the 503 assertions above are discriminating (not trivially always-503).
func TestAdversarial_Readyz_HealthyAggregate_Is200Ready(t *testing.T) {
	a := ihealth.NewAggregator()
	registerReadiness(a, "dep", ihealth.Healthy, nil)
	a.RunChecks(context.Background())

	s := newHTTPServer(a)
	code, body := hitReadyz(t, s)
	if code != http.StatusOK || !body.Ready {
		t.Fatalf("healthy readiness aggregate should be 200/ready; got code=%d ready=%v", code, body.Ready)
	}
}

// -----------------------------------------------------------------------------
// (b) AGGREGATE — a single failing check must not be lost; empty set is unready.
// -----------------------------------------------------------------------------

func TestAdversarial_Readyz_SingleCriticalAmongHealthy_Is503(t *testing.T) {
	a := ihealth.NewAggregator()
	registerReadiness(a, "ok1", ihealth.Healthy, nil)
	registerReadiness(a, "ok2", ihealth.Healthy, nil)
	registerReadiness(a, "bad", ihealth.Critical, nil) // the one rotten apple
	a.RunChecks(context.Background())

	s := newHTTPServer(a)
	code, body := hitReadyz(t, s)
	if code != http.StatusServiceUnavailable || body.Ready {
		t.Fatalf("single Critical check must drive /readyz to 503/not-ready; got code=%d ready=%v status=%q", code, body.Ready, body.Status)
	}
}

func TestAdversarial_Readyz_EmptyCheckSet_Is503(t *testing.T) {
	// No checks registered, RunChecks produces an empty result set. Readiness
	// rollup => Unknown => Ready()==false => 503. A node with nothing proving
	// it can serve must NOT advertise readiness.
	a := ihealth.NewAggregator()
	a.RunChecks(context.Background())

	s := newHTTPServer(a)
	code, body := hitReadyz(t, s)
	if code != http.StatusServiceUnavailable || body.Ready {
		t.Fatalf("empty readiness set must be 503/not-ready; got code=%d ready=%v status=%q", code, body.Ready, body.Status)
	}
}

// Documented liveness asymmetry: with NO liveness checks, Liveness()==Healthy
// (absence of a liveness probe must never wedge a process into restart). Pin it.
func TestAdversarial_Livez_NoLivenessChecks_Is200(t *testing.T) {
	a := ihealth.NewAggregator()
	a.RunChecks(context.Background())
	s := newHTTPServer(a)
	code, status := hitLivez(t, s)
	if code != http.StatusOK {
		t.Fatalf("no liveness checks should be 200 (alive); got code=%d status=%q", code, status)
	}
}

// -----------------------------------------------------------------------------
// (c) UNGUARDED SHARED STATE — concurrent check register/run + probe hits.
// Run with -race. Concurrency capped to keep the suite fast and non-hanging.
// -----------------------------------------------------------------------------

func TestAdversarial_ConcurrentRegisterRunAndProbe_NoRace(t *testing.T) {
	a := ihealth.NewAggregator()
	registerReadiness(a, "seed", ihealth.Healthy, nil)
	a.RunChecks(context.Background())
	s := newHTTPServer(a)

	const workers = 8 // cap per ANTI-HANG
	const iters = 50

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				switch i % 3 {
				case 0:
					registerReadiness(a, "dyn", ihealth.Status(i%4), nil)
					a.RunChecks(context.Background())
				case 1:
					rr := httptest.NewRecorder()
					s.readyz(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
				case 2:
					rr := httptest.NewRecorder()
					s.livez(rr, httptest.NewRequest(http.MethodGet, "/livez", nil))
				}
			}
		}(w)
	}
	wg.Wait()
}

// -----------------------------------------------------------------------------
// (d) DETERMINISM — same readiness check set yields the same sink code/body.
// -----------------------------------------------------------------------------

func TestAdversarial_Readyz_Deterministic(t *testing.T) {
	build := func() *httpServer {
		a := ihealth.NewAggregator()
		registerReadiness(a, "a", ihealth.Healthy, nil)
		registerReadiness(a, "b", ihealth.Degraded, nil)
		registerReadiness(a, "c", ihealth.Healthy, nil)
		a.RunChecks(context.Background())
		return newHTTPServer(a)
	}
	var firstCode int
	var firstReady bool
	for i := 0; i < 25; i++ {
		code, body := hitReadyz(t, build())
		if i == 0 {
			firstCode, firstReady = code, body.Ready
			continue
		}
		if code != firstCode || body.Ready != firstReady {
			t.Fatalf("non-deterministic /readyz: run %d got code=%d ready=%v; first was code=%d ready=%v",
				i, code, body.Ready, firstCode, firstReady)
		}
	}
	// Degraded readiness must be not-ready/503 (conservative for load balancers).
	if firstCode != http.StatusServiceUnavailable || firstReady {
		t.Fatalf("a Degraded readiness check must be 503/not-ready; got code=%d ready=%v", firstCode, firstReady)
	}
}

// -----------------------------------------------------------------------------
// /health + /check/<svc> are backed by pkg/health.CompositeChecker (a SEPARATE
// checker). Pin its CONTRACT and surface the one fold gap as a LATENT RISK.
// -----------------------------------------------------------------------------

// CONTRACT: an Unhealthy composite check drives /health to 503.
func TestAdversarial_Health_UnhealthyComposite_Is503(t *testing.T) {
	a := ihealth.NewAggregator()
	s := newHTTPServer(a)
	s.registerDefaults()
	s.checker.AddCheck("dep", func() pkghealth.Status { return pkghealth.Unhealthy })

	rr := httptest.NewRecorder()
	s.clusterHealth(rr, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("/health with an Unhealthy composite check must be 503; got %d body=%q", rr.Code, rr.Body.String())
	}
}

// CONTRACT (documented): Degraded composite => 200 on /health (degraded is a
// warning, not an outage). Pin so a regression is visible.
func TestAdversarial_Health_DegradedComposite_Is200_Documented(t *testing.T) {
	a := ihealth.NewAggregator()
	s := newHTTPServer(a)
	s.registerDefaults()
	s.checker.AddCheck("dep", func() pkghealth.Status { return pkghealth.Degraded })

	rr := httptest.NewRecorder()
	s.clusterHealth(rr, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("documented contract: Degraded composite => 200 on /health; got %d", rr.Code)
	}
}

// LATENT RISK (documented, NOT a probe): pkg/health.CompositeChecker.Check only
// recognises Unhealthy/Degraded; ANY other status string (incl. a malformed /
// unknown one) is treated as Healthy and yields 200 on /health and /check/<svc>.
// This is the SAME fail-open class the internal/health Aggregator was hardened
// against — but /health and /check are status-report endpoints, NOT the daemon's
// readiness/liveness probes (/readyz, /livez), which are correctly guarded.
// We PIN the current behaviour so any future change (good or bad) is visible.
func TestAdversarial_Health_MalformedComposite_Is200_LATENT_RISK(t *testing.T) {
	a := ihealth.NewAggregator()
	s := newHTTPServer(a)
	s.registerDefaults()
	// A status string outside {healthy,unhealthy,degraded}: models a corrupted
	// or unknown check result. CompositeChecker.Check leaves overall == Healthy.
	s.checker.AddCheck("weird", func() pkghealth.Status { return pkghealth.Status("garbage-status") })

	rr := httptest.NewRecorder()
	s.clusterHealth(rr, httptest.NewRequest(http.MethodGet, "/health", nil))

	// Current (pinned) behaviour: 200. If pkg/health is hardened to fold unknown
	// statuses to non-OK, update this pin accordingly.
	if rr.Code != http.StatusOK {
		t.Fatalf("LATENT-RISK pin changed: /health with a malformed composite status now returns %d (was 200). "+
			"If pkg/health.CompositeChecker was hardened to fail-closed, update this pin.", rr.Code)
	}
	t.Logf("LATENT RISK pinned: pkg/health.CompositeChecker folds unknown/malformed status -> Healthy (200) "+
		"on /health and /check/<svc>. Not a probe; /readyz and /livez are guarded. raw=%q", rr.Body.String())
}
