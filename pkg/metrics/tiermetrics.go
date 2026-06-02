// This file implements HXC-1535: GPU-tier utilization, cost-per-hour, and
// provider-health metrics exposed in Prometheus text exposition format.  It is
// self-contained and additive to the metrics package; it does not depend on
// the NodeCollector resource pipeline.
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// TierMetrics holds GPU-tier utilization, per-provider cost-per-hour, and
// per-provider health-score gauges and renders them in Prometheus text
// exposition format.  It is safe for concurrent use.
type TierMetrics struct {
	mu sync.Mutex
	// tierUtil maps a GPU tier name (e.g. "a100", "h100") to its current
	// utilization value as last set by SetTierUtilization.
	tierUtil map[string]float64
	// costPerHour maps a provider name (e.g. "aws", "gcp") to its current
	// USD-per-hour cost as last set by SetCostPerHour.
	costPerHour map[string]float64
	// providerHealth maps a provider name to its current health score as last
	// set by SetProviderHealth.
	providerHealth map[string]float64
}

// NewTierMetrics returns an empty TierMetrics ready for concurrent use.
func NewTierMetrics() *TierMetrics {
	return &TierMetrics{
		tierUtil:       make(map[string]float64),
		costPerHour:    make(map[string]float64),
		providerHealth: make(map[string]float64),
	}
}

// SetTierUtilization records the current utilization value for a GPU tier.
// The most recent value for a given tier wins.
func (t *TierMetrics) SetTierUtilization(tier string, v float64) {
	t.mu.Lock()
	t.tierUtil[tier] = v
	t.mu.Unlock()
}

// SetCostPerHour records the current USD-per-hour cost for a provider.
// The most recent value for a given provider wins.
func (t *TierMetrics) SetCostPerHour(provider string, usd float64) {
	t.mu.Lock()
	t.costPerHour[provider] = usd
	t.mu.Unlock()
}

// SetProviderHealth records the current health score for a provider.
// The most recent value for a given provider wins.
func (t *TierMetrics) SetProviderHealth(provider string, score float64) {
	t.mu.Lock()
	t.providerHealth[provider] = score
	t.mu.Unlock()
}

// Render serialises the three metric families into Prometheus text exposition
// format.  Each family is preceded by its # HELP and # TYPE lines, and within
// each family series are emitted in ascending label-key order so the output is
// deterministic.  A family with no recorded series still emits its HELP/TYPE
// header so scrapers see a stable schema.
func (t *TierMetrics) Render() string {
	t.mu.Lock()
	tierUtil := copyMap(t.tierUtil)
	costPerHour := copyMap(t.costPerHour)
	providerHealth := copyMap(t.providerHealth)
	t.mu.Unlock()

	var b strings.Builder

	// gpu_tier_utilization{tier="..."}
	fmt.Fprintf(&b, "# HELP gpu_tier_utilization GPU utilization per hardware tier.\n")
	fmt.Fprintf(&b, "# TYPE gpu_tier_utilization gauge\n")
	for _, k := range sortedKeys(tierUtil) {
		fmt.Fprintf(&b, "gpu_tier_utilization{tier=%q} %g\n", k, tierUtil[k])
	}

	// gpu_cost_per_hour{provider="..."}
	fmt.Fprintf(&b, "# HELP gpu_cost_per_hour GPU cost in USD per hour per provider.\n")
	fmt.Fprintf(&b, "# TYPE gpu_cost_per_hour gauge\n")
	for _, k := range sortedKeys(costPerHour) {
		fmt.Fprintf(&b, "gpu_cost_per_hour{provider=%q} %g\n", k, costPerHour[k])
	}

	// provider_health{provider="..."}
	fmt.Fprintf(&b, "# HELP provider_health Provider health score.\n")
	fmt.Fprintf(&b, "# TYPE provider_health gauge\n")
	for _, k := range sortedKeys(providerHealth) {
		fmt.Fprintf(&b, "provider_health{provider=%q} %g\n", k, providerHealth[k])
	}

	return b.String()
}

// Handler returns an http.Handler that serves the current Render() output as
// text/plain.
func (t *TierMetrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, t.Render())
	})
}

// copyMap returns a shallow copy of m so the caller can release the lock before
// formatting.
func copyMap(m map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// sortedKeys returns the keys of m in ascending order for deterministic output.
func sortedKeys(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
