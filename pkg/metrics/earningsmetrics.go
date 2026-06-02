// This file implements HXC-1483: TAO earnings, GraVal attestation status,
// token throughput, and GPU utilization metrics exposed in Prometheus text
// exposition format.  It is self-contained and additive to the metrics
// package; it does not depend on the NodeCollector resource pipeline or on any
// external Prometheus client library — it renders standard Prometheus text
// exposition directly using the same tiermetrics.go pattern.
//
// NOTE: Live Grafana / Prometheus dashboard wiring is an out-of-host concern
// and is intentionally out of scope for this file.  This file delivers only
// the Go metrics struct + deterministic Render(); the operator is responsible
// for registering the Handler() endpoint with their HTTP multiplexer and for
// pointing a Prometheus scrape config at it.
package metrics

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// EarningsMetrics holds Bittensor-oriented metrics:
//   - tao_earnings_total   — cumulative TAO earned, labeled by hotkey
//   - graval_attestation_status — GraVal device attestation (1.0 = ok, 0.0 = fail), labeled by device ID
//   - token_throughput     — single scalar tokens-per-second gauge
//   - gpu_utilization      — per-GPU utilization [0, 1], labeled by gpu ID
//
// It is safe for concurrent use.  Render() emits all four metric families in
// deterministic order (labels sorted ascending within each family) so output
// is byte-stable across repeated calls on the same state — a property the
// tests verify directly.
type EarningsMetrics struct {
	mu sync.Mutex

	// taoEarnings maps hotkey → cumulative TAO earned.
	taoEarnings map[string]float64

	// gravalStatus maps device ID → attestation status (1.0 ok, 0.0 fail).
	gravalStatus map[string]float64

	// tokenThroughput is the most-recently-set tokens/sec scalar.  A sentinel
	// value of -1 means "never set"; Render() omits the line in that case so
	// callers can distinguish "not yet observed" from "zero throughput".
	tokenThroughput float64
	tokenSet        bool

	// gpuUtilization maps gpu ID → utilization value.
	gpuUtilization map[string]float64
}

// NewEarningsMetrics returns an empty EarningsMetrics ready for concurrent use.
func NewEarningsMetrics() *EarningsMetrics {
	return &EarningsMetrics{
		taoEarnings:    make(map[string]float64),
		gravalStatus:   make(map[string]float64),
		gpuUtilization: make(map[string]float64),
	}
}

// SetTaoEarnings records the cumulative TAO earned for a hotkey.
// The most recent value for a given hotkey wins.
func (e *EarningsMetrics) SetTaoEarnings(hotkey string, v float64) {
	e.mu.Lock()
	e.taoEarnings[hotkey] = v
	e.mu.Unlock()
}

// SetGravalAttestationStatus records the attestation result for a device.
// v should be 1.0 for OK and 0.0 for failure.
func (e *EarningsMetrics) SetGravalAttestationStatus(deviceID string, v float64) {
	e.mu.Lock()
	e.gravalStatus[deviceID] = v
	e.mu.Unlock()
}

// SetTokenThroughput records the current tokens-per-second scalar.
func (e *EarningsMetrics) SetTokenThroughput(tokensPerSec float64) {
	e.mu.Lock()
	e.tokenThroughput = tokensPerSec
	e.tokenSet = true
	e.mu.Unlock()
}

// SetGPUUtilization records the current utilization for a GPU.
// v is a fraction in [0, 1] (e.g. 0.75 = 75 % busy).
func (e *EarningsMetrics) SetGPUUtilization(gpuID string, v float64) {
	e.mu.Lock()
	e.gpuUtilization[gpuID] = v
	e.mu.Unlock()
}

// snapshot returns a locked-copy of the mutable state so the mutex can be
// released before the (potentially slow) string formatting pass.
type earningsSnapshot struct {
	taoEarnings    map[string]float64
	gravalStatus   map[string]float64
	tokenThroughput float64
	tokenSet       bool
	gpuUtilization map[string]float64
}

func (e *EarningsMetrics) snapshot() earningsSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	return earningsSnapshot{
		taoEarnings:     copyMap(e.taoEarnings),
		gravalStatus:    copyMap(e.gravalStatus),
		tokenThroughput: e.tokenThroughput,
		tokenSet:        e.tokenSet,
		gpuUtilization:  copyMap(e.gpuUtilization),
	}
}

// Render serialises all four metric families into Prometheus text exposition
// format.  Within each family, series are emitted in ascending label-key order
// so the output is deterministic.  Families with no recorded series still emit
// their HELP/TYPE headers so scrapers see a stable schema.
//
// The token_throughput gauge is scalar (no labels); its line is omitted if
// SetTokenThroughput has never been called, but the HELP/TYPE header is always
// present.
func (e *EarningsMetrics) Render() string {
	s := e.snapshot()

	var b strings.Builder

	// --- tao_earnings_total{hotkey="..."} counter ---
	fmt.Fprintf(&b, "# HELP tao_earnings_total Cumulative TAO earned per hotkey.\n")
	fmt.Fprintf(&b, "# TYPE tao_earnings_total counter\n")
	for _, k := range sortedKeys(s.taoEarnings) {
		fmt.Fprintf(&b, "tao_earnings_total{hotkey=%q} %g\n", k, s.taoEarnings[k])
	}

	// --- graval_attestation_status{device="..."} gauge ---
	fmt.Fprintf(&b, "# HELP graval_attestation_status GraVal device attestation status (1=ok, 0=fail).\n")
	fmt.Fprintf(&b, "# TYPE graval_attestation_status gauge\n")
	for _, k := range sortedKeys(s.gravalStatus) {
		fmt.Fprintf(&b, "graval_attestation_status{device=%q} %g\n", k, s.gravalStatus[k])
	}

	// --- token_throughput gauge (scalar, no labels) ---
	fmt.Fprintf(&b, "# HELP token_throughput Current token throughput in tokens per second.\n")
	fmt.Fprintf(&b, "# TYPE token_throughput gauge\n")
	if s.tokenSet {
		fmt.Fprintf(&b, "token_throughput %g\n", s.tokenThroughput)
	}

	// --- gpu_utilization{gpu="..."} gauge ---
	fmt.Fprintf(&b, "# HELP gpu_utilization GPU utilization fraction per device.\n")
	fmt.Fprintf(&b, "# TYPE gpu_utilization gauge\n")
	for _, k := range sortedKeys(s.gpuUtilization) {
		fmt.Fprintf(&b, "gpu_utilization{gpu=%q} %g\n", k, s.gpuUtilization[k])
	}

	return b.String()
}

// Handler returns an http.Handler that serves the current Render() output as
// text/plain, suitable for use as a Prometheus scrape endpoint.
func (e *EarningsMetrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, e.Render())
	})
}
