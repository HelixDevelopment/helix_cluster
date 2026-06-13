// Copyright 2026 Milos Vasic. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// metrics.go implements HXC-1258: a Prometheus-compatible MetricsCollector for
// the chaos experiment subsystem. It emits real helix_chaos_* metric series
// derived from ACTUAL experiment outcomes and the message-delivery event logs
// they produce — never fabricated constants. Running the 12-experiment catalog
// through RunCatalog produces well over 15 distinct series (per-experiment
// gauges, delivered/dropped/drop-reason counters, a duration histogram).
//
// HONEST SCOPE (CLAUDE-1): these metrics reflect the deterministic turmoil
// delivery simulation that backs pkg/chaosexp. The OpenTelemetry distributed
// trace spanning the Rust/Elixir/Go polyglot boundary called for by HXC-1258 is
// DEFERRED to the multi-runtime integration environment — it cannot be produced
// or honestly proven on a single Go host and is intentionally NOT faked here.
package chaosexp

import (
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// metricsNamespace prefixes every emitted series with helix_chaos_.
const metricsNamespace = "helix_chaos"

// MetricsCollector instruments execution of the chaos experiment catalog and
// exposes the results as Prometheus metrics. It owns a private registry so a
// scrape of Registry() (or the /metrics Handler) returns exactly the
// helix_chaos_* family and nothing else.
type MetricsCollector struct {
	reg *prometheus.Registry

	// nowFn supplies wall-clock time for the duration histogram; injectable for
	// deterministic tests. It never affects the deterministic Result.
	nowFn func() time.Time

	experimentsRun    prometheus.Counter
	passTotal         prometheus.Counter
	failTotal         prometheus.Counter
	messagesDelivered prometheus.Counter
	messagesDropped   prometheus.Counter
	dropsByReason     *prometheus.CounterVec

	catalogSize       prometheus.Gauge
	lastSeed          prometheus.Gauge
	experimentPassed  *prometheus.GaugeVec
	experimentEvents  *prometheus.GaugeVec
	experimentMaxTick *prometheus.GaugeVec
	experimentBytes   *prometheus.GaugeVec

	runDuration prometheus.Histogram

	mu sync.Mutex
}

// NewMetricsCollector builds a collector with a fresh registry and all series
// registered. The duration histogram uses time.Now; use
// NewMetricsCollectorWithClock to inject a deterministic clock in tests.
func NewMetricsCollector() *MetricsCollector {
	return NewMetricsCollectorWithClock(time.Now)
}

// NewMetricsCollectorWithClock is NewMetricsCollector with an injectable clock.
func NewMetricsCollectorWithClock(now func() time.Time) *MetricsCollector {
	reg := prometheus.NewRegistry()
	mc := &MetricsCollector{
		reg:   reg,
		nowFn: now,
		experimentsRun: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: metricsNamespace, Name: "experiments_run_total",
			Help: "Total chaos experiments executed.",
		}),
		passTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: metricsNamespace, Name: "experiment_pass_total",
			Help: "Total chaos experiments whose steady-state hypothesis held.",
		}),
		failTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: metricsNamespace, Name: "experiment_fail_total",
			Help: "Total chaos experiments whose steady-state hypothesis failed.",
		}),
		messagesDelivered: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: metricsNamespace, Name: "messages_delivered_total",
			Help: "Total delivered messages across all experiment event logs.",
		}),
		messagesDropped: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: metricsNamespace, Name: "messages_dropped_total",
			Help: "Total dropped (fault-injected) messages across all event logs.",
		}),
		dropsByReason: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricsNamespace, Name: "message_drops_by_reason_total",
			Help: "Dropped messages partitioned by drop reason.",
		}, []string{"reason"}),
		catalogSize: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: metricsNamespace, Name: "catalog_size",
			Help: "Number of experiments in the chaos catalog.",
		}),
		lastSeed: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: metricsNamespace, Name: "last_run_seed",
			Help: "Seed used for the most recent catalog run.",
		}),
		experimentPassed: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: metricsNamespace, Name: "experiment_passed",
			Help: "1 if the experiment's last run passed its steady-state hypothesis, else 0.",
		}, []string{"experiment"}),
		experimentEvents: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: metricsNamespace, Name: "experiment_delivery_events",
			Help: "Number of delivery events in the experiment's last event log.",
		}, []string{"experiment"}),
		experimentMaxTick: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: metricsNamespace, Name: "experiment_max_delivery_tick",
			Help: "Highest virtual-clock tick observed in the experiment's event log (delivery-latency proxy).",
		}, []string{"experiment"}),
		experimentBytes: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: metricsNamespace, Name: "experiment_canonical_bytes",
			Help: "Size in bytes of the experiment's canonical encoded event log.",
		}, []string{"experiment"}),
		runDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: metricsNamespace, Name: "experiment_duration_seconds",
			Help:    "Wall-clock duration of a single experiment run.",
			Buckets: []float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1},
		}),
	}
	reg.MustRegister(
		mc.experimentsRun, mc.passTotal, mc.failTotal,
		mc.messagesDelivered, mc.messagesDropped, mc.dropsByReason,
		mc.catalogSize, mc.lastSeed,
		mc.experimentPassed, mc.experimentEvents, mc.experimentMaxTick, mc.experimentBytes,
		mc.runDuration,
	)
	return mc
}

// Registry exposes the underlying registry for scraping/testing.
func (mc *MetricsCollector) Registry() *prometheus.Registry { return mc.reg }

// Handler returns an http.Handler serving the metrics in Prometheus exposition
// format (mount at /metrics; scraped at 15s via ServiceMonitor in production).
func (mc *MetricsCollector) Handler() http.Handler {
	return promhttp.HandlerFor(mc.reg, promhttp.HandlerOpts{})
}

// RecordRun records the metrics for a single experiment result. dur is the
// measured wall-clock run time. All recorded values are derived from r — the
// real outcome and event log — so the series cannot drift from reality.
func (mc *MetricsCollector) RecordRun(e ChaosExperiment, r Result, dur time.Duration) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	mc.experimentsRun.Inc()
	mc.runDuration.Observe(dur.Seconds())

	passed := 0.0
	if r.Passed {
		mc.passTotal.Inc()
		passed = 1
	} else {
		mc.failTotal.Inc()
	}
	mc.experimentPassed.WithLabelValues(e.ID).Set(passed)

	var maxTick int64
	for _, ev := range r.Events {
		if ev.Dropped {
			mc.messagesDropped.Inc()
			reason := ev.Reason
			if reason == "" {
				reason = "unspecified"
			}
			mc.dropsByReason.WithLabelValues(reason).Inc()
		} else {
			mc.messagesDelivered.Inc()
		}
		if ev.Tick > maxTick {
			maxTick = ev.Tick
		}
	}
	mc.experimentEvents.WithLabelValues(e.ID).Set(float64(len(r.Events)))
	mc.experimentMaxTick.WithLabelValues(e.ID).Set(float64(maxTick))
	mc.experimentBytes.WithLabelValues(e.ID).Set(float64(len(r.Canonical)))
}

// RunCatalog executes every experiment in Catalog() at the given seed, records
// real metrics for each, and returns the results. catalog_size and last_run_seed
// gauges are updated to reflect the run.
func (mc *MetricsCollector) RunCatalog(seed int64) []Result {
	cat := Catalog()
	mc.catalogSize.Set(float64(len(cat)))
	mc.lastSeed.Set(float64(seed))

	results := make([]Result, 0, len(cat))
	for _, e := range cat {
		start := mc.nowFn()
		r := Run(e, seed)
		dur := mc.nowFn().Sub(start)
		mc.RecordRun(e, r, dur)
		results = append(results, r)
	}
	return results
}
