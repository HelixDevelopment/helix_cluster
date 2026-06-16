// Package metrics provides metrics collection for Helix Cluster OS.
package metrics

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Counter is a simple atomic counter.
type Counter struct {
	val int64
}

// Inc increments the counter by 1.
func (c *Counter) Inc() {
	atomic.AddInt64(&c.val, 1)
}

// Value returns the current counter value.
func (c *Counter) Value() int64 {
	return atomic.LoadInt64(&c.val)
}

// Add adds n to the counter.
func (c *Counter) Add(n int64) {
	atomic.AddInt64(&c.val, n)
}

// Gauge is an atomic gauge that can go up and down.
type Gauge struct {
	val int64
}

// Set sets the gauge to n.
func (g *Gauge) Set(n int64) {
	atomic.StoreInt64(&g.val, n)
}

// Inc increments the gauge by 1.
func (g *Gauge) Inc() {
	atomic.AddInt64(&g.val, 1)
}

// Dec decrements the gauge by 1.
func (g *Gauge) Dec() {
	atomic.AddInt64(&g.val, -1)
}

// Value returns the current gauge value.
func (g *Gauge) Value() int64 {
	return atomic.LoadInt64(&g.val)
}

// Add adds n to the gauge.
func (g *Gauge) Add(n int64) {
	atomic.AddInt64(&g.val, n)
}

// Histogram records observations in configurable buckets.
type Histogram struct {
	mu        sync.RWMutex
	buckets   []float64
	counts    []uint64
	sum       float64
	count     uint64
	createdAt time.Time
}

// NewHistogram creates a Histogram with the given bucket upper bounds.
// The buckets slice must be sorted in strictly increasing order.
func NewHistogram(buckets []float64) *Histogram {
	b := make([]float64, len(buckets))
	copy(b, buckets)
	return &Histogram{
		buckets:   b,
		counts:    make([]uint64, len(b)),
		createdAt: time.Now(),
	}
}

// DefaultBuckets returns the default Prometheus-style histogram buckets.
func DefaultBuckets() []float64 {
	return []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
}

// Observe records a value into the histogram.
func (h *Histogram) Observe(v float64) {
	// Drop a non-finite observation: a single Observe(NaN)/Observe(±Inf) would
	// poison the histogram _sum exposition permanently (every later _sum reads
	// NaN). A non-finite latency/duration sample is a sensor error; finite
	// observations are unaffected.
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sum += v
	h.count++
	for i, b := range h.buckets {
		if v <= b {
			h.counts[i]++
		}
	}
}

// Snapshot returns the current histogram state.
func (h *Histogram) Snapshot() (buckets []float64, counts []uint64, sum float64, count uint64) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	b := make([]float64, len(h.buckets))
	c := make([]uint64, len(h.counts))
	copy(b, h.buckets)
	copy(c, h.counts)
	return b, c, h.sum, h.count
}

// metric holds a named metric instance.
type metric struct {
	name  string
	help  string
	mtype string
	value interface{}
}

// Registry collects and exposes metrics in Prometheus text format.
type Registry struct {
	mu      sync.RWMutex
	metrics map[string]*metric
}

// NewRegistry creates a new Registry.
func NewRegistry() *Registry {
	return &Registry{metrics: make(map[string]*metric)}
}

// RegisterCounter registers a Counter under the given name.
func (r *Registry) RegisterCounter(name, help string) *Counter {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := &Counter{}
	r.metrics[name] = &metric{name: name, help: help, mtype: "counter", value: c}
	return c
}

// RegisterGauge registers a Gauge under the given name.
func (r *Registry) RegisterGauge(name, help string) *Gauge {
	r.mu.Lock()
	defer r.mu.Unlock()
	g := &Gauge{}
	r.metrics[name] = &metric{name: name, help: help, mtype: "gauge", value: g}
	return g
}

// RegisterHistogram registers a Histogram under the given name.
func (r *Registry) RegisterHistogram(name, help string, buckets []float64) *Histogram {
	r.mu.Lock()
	defer r.mu.Unlock()
	h := NewHistogram(buckets)
	r.metrics[name] = &metric{name: name, help: help, mtype: "histogram", value: h}
	return h
}

// Get retrieves a registered metric by name.
func (r *Registry) Get(name string) (interface{}, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.metrics[name]
	if !ok {
		return nil, false
	}
	return m.value, true
}

// PrometheusHandler returns an http.Handler that serves /metrics in Prometheus text format.
func (r *Registry) PrometheusHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		data := r.serialize()
		w.Write([]byte(data))
	})
}

func (r *Registry) serialize() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var b strings.Builder
	names := make([]string, 0, len(r.metrics))
	for name := range r.metrics {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		m := r.metrics[name]
		if m.help != "" {
			fmt.Fprintf(&b, "# HELP %s %s\n", name, m.help)
		}
		fmt.Fprintf(&b, "# TYPE %s %s\n", name, m.mtype)
		switch v := m.value.(type) {
		case *Counter:
			fmt.Fprintf(&b, "%s %d\n", name, v.Value())
		case *Gauge:
			fmt.Fprintf(&b, "%s %d\n", name, v.Value())
		case *Histogram:
			buckets, counts, sum, count := v.Snapshot()
			for i, bucket := range buckets {
				fmt.Fprintf(&b, "%s_bucket{le=\"%g\"} %d\n", name, bucket, counts[i])
			}
			fmt.Fprintf(&b, "%s_bucket{le=\"+Inf\"} %d\n", name, count)
			fmt.Fprintf(&b, "%s_sum %g\n", name, sum)
			fmt.Fprintf(&b, "%s_count %d\n", name, count)
		}
	}
	return b.String()
}
