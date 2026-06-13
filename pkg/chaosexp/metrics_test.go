// Copyright 2026 Milos Vasic. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package chaosexp

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// seqClock returns a deterministic clock advancing 1ms per call, so the run
// duration histogram is populated with stable, non-zero observations.
func seqClock() func() time.Time {
	base := time.Unix(0, 0)
	var n int64
	return func() time.Time {
		n++
		return base.Add(time.Duration(n) * time.Millisecond)
	}
}

// TestMetricsCollector_RealSeriesFromCatalog runs the actual 12-experiment
// catalog and asserts the emitted series both (a) number >= 15 distinct series
// and (b) carry values that match an INDEPENDENT recomputation from Run() — so
// a constant/fabricated implementation would fail.
func TestMetricsCollector_RealSeriesFromCatalog(t *testing.T) {
	const seed = int64(42)
	mc := NewMetricsCollectorWithClock(seqClock())
	results := mc.RunCatalog(seed)

	cat := Catalog()
	if len(results) != len(cat) {
		t.Fatalf("RunCatalog returned %d results, want %d", len(results), len(cat))
	}

	// (a) >= 15 distinct helix_chaos_* series.
	series, err := testutil.GatherAndCount(mc.Registry())
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	if series < 15 {
		t.Fatalf("emitted %d distinct series, HXC-1258 requires >= 15", series)
	}
	t.Logf("emitted %d distinct helix_chaos_* series from a real %d-experiment run", series, len(cat))

	// (b) aggregate counters match reality.
	if got := testutil.ToFloat64(mc.experimentsRun); got != float64(len(cat)) {
		t.Errorf("experiments_run_total = %v, want %d", got, len(cat))
	}
	if got := testutil.ToFloat64(mc.catalogSize); got != float64(len(cat)) {
		t.Errorf("catalog_size = %v, want %d", got, len(cat))
	}
	if got := testutil.ToFloat64(mc.lastSeed); got != float64(seed) {
		t.Errorf("last_run_seed = %v, want %d", got, seed)
	}
	pass := testutil.ToFloat64(mc.passTotal)
	fail := testutil.ToFloat64(mc.failTotal)
	if pass+fail != float64(len(cat)) {
		t.Errorf("pass(%v)+fail(%v) != catalog size %d", pass, fail, len(cat))
	}

	// Independent recomputation: re-run each experiment and verify the
	// per-experiment gauges + the delivered/dropped split exactly.
	var wantPass, wantDelivered, wantDropped, wantEvents int
	for _, e := range cat {
		r := Run(e, seed)
		if r.Passed {
			wantPass++
		}
		for _, ev := range r.Events {
			wantEvents++
			if ev.Dropped {
				wantDropped++
			} else {
				wantDelivered++
			}
		}
		gotEvents := testutil.ToFloat64(mc.experimentEvents.WithLabelValues(e.ID))
		if gotEvents != float64(len(r.Events)) {
			t.Errorf("%s experiment_delivery_events = %v, want %d", e.ID, gotEvents, len(r.Events))
		}
		gotBytes := testutil.ToFloat64(mc.experimentBytes.WithLabelValues(e.ID))
		if gotBytes != float64(len(r.Canonical)) {
			t.Errorf("%s experiment_canonical_bytes = %v, want %d", e.ID, gotBytes, len(r.Canonical))
		}
		wantPassed := 0.0
		if r.Passed {
			wantPassed = 1
		}
		if got := testutil.ToFloat64(mc.experimentPassed.WithLabelValues(e.ID)); got != wantPassed {
			t.Errorf("%s experiment_passed = %v, want %v", e.ID, got, wantPassed)
		}
	}
	if int(pass) != wantPass {
		t.Errorf("pass_total = %d, want %d (independent recompute)", int(pass), wantPass)
	}
	if got := int(testutil.ToFloat64(mc.messagesDelivered)); got != wantDelivered {
		t.Errorf("messages_delivered_total = %d, want %d", got, wantDelivered)
	}
	if got := int(testutil.ToFloat64(mc.messagesDropped)); got != wantDropped {
		t.Errorf("messages_dropped_total = %d, want %d", got, wantDropped)
	}
	if wantDelivered+wantDropped != wantEvents {
		t.Fatalf("internal: delivered+dropped(%d) != events(%d)", wantDelivered+wantDropped, wantEvents)
	}
	t.Logf("real values verified: pass=%d/%d delivered=%d dropped=%d events=%d",
		wantPass, len(cat), wantDelivered, wantDropped, wantEvents)
}

// TestMetricsCollector_ScrapeEndpoint proves the /metrics HTTP endpoint serves
// the helix_chaos_* family in Prometheus exposition format with >= 15 series.
func TestMetricsCollector_ScrapeEndpoint(t *testing.T) {
	mc := NewMetricsCollectorWithClock(seqClock())
	mc.RunCatalog(7)

	srv := httptest.NewServer(mc.Handler())
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	text := string(body)

	// Count distinct emitted series lines (non-comment) in the helix_chaos_ family.
	distinct := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "helix_chaos_") {
			distinct++
		}
	}
	if distinct < 15 {
		t.Fatalf("/metrics exposed %d helix_chaos_ series lines, want >= 15", distinct)
	}
	// Spot-check a couple of specific real series are present.
	for _, want := range []string{
		`helix_chaos_experiment_passed{experiment="CE-01"}`,
		"helix_chaos_experiments_run_total",
		"helix_chaos_experiment_duration_seconds_bucket",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("/metrics missing expected series %q", want)
		}
	}
	t.Logf("/metrics served %d helix_chaos_ series lines over HTTP", distinct)
}
