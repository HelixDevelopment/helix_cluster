// Package metrics — Mount wires a Registry's Prometheus handler onto a ServeMux.
package metrics

import (
	"fmt"
	"net/http"
)

// Mount registers reg's Prometheus exposition handler at "/metrics" on mux.
//
// Both mux and reg must be non-nil; Mount panics if either is nil.  This
// design matches the rest of the pkg/metrics API, which relies on non-nil
// Registry instances throughout (created by NewRegistry or NewServiceRegistry).
//
// Usage:
//
//	reg := metrics.NewServiceRegistry("my-service")
//	mux := http.NewServeMux()
//	metrics.Mount(mux, reg)
func Mount(mux *http.ServeMux, reg *Registry) {
	if mux == nil {
		panic("metrics.Mount: mux must not be nil")
	}
	if reg == nil {
		panic("metrics.Mount: reg must not be nil")
	}
	mux.Handle("/metrics", reg.PrometheusHandler())
}

// NewServiceRegistry creates a new Registry pre-seeded with a
// helix_<service>_build_info gauge set to 1.  The gauge provides a stable
// scrape target that carries service identity and can act as an up-indicator
// in Prometheus recording rules.
//
// service must be a non-empty string consisting of characters valid in a
// Prometheus metric name (letters, digits, underscores).  NewServiceRegistry
// panics if service is empty.
func NewServiceRegistry(service string) *Registry {
	if service == "" {
		panic("metrics.NewServiceRegistry: service must not be empty")
	}
	reg := NewRegistry()
	name := fmt.Sprintf("helix_%s_build_info", service)
	g := reg.RegisterGauge(name, fmt.Sprintf("Build-info gauge for helix-%s; value is always 1.", service))
	g.Set(1)
	return reg
}
