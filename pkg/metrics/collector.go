// Package metrics provides metrics collection for Helix Cluster OS.
// This file implements a per-node resource metrics collector that reads CPU,
// memory, and GPU utilization via an injected ResourceSource seam, and exposes
// them as a Prometheus-format scrape endpoint.
package metrics

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// ResourceSample holds the per-node resource snapshot that the collector
// converts into gauge values. All utilization fields are expressed as a
// percentage in the range [0, 100].
type ResourceSample struct {
	// NodeID identifies the node this sample belongs to.
	NodeID string
	// CPUPct is CPU utilization in the range [0, 100].
	// It is computed as (UsedCores / Cores) * 100.
	CPUPct float64
	// MemPct is memory utilization in the range [0, 100].
	// It is computed as (UsedKB / TotalKB) * 100.
	MemPct float64
	// GPUPct is the average GPU utilization across all GPUs in [0, 100].
	// If the node has no GPU this is zero and HasGPU must be false.
	GPUPct float64
	// HasGPU reports whether the node has at least one GPU.  When false, no
	// helix_node_gpu_pct gauge is emitted in the scrape output.
	HasGPU bool
}

// ResourceSource is the seam that the collector reads from. Production code
// injects a source backed by pkg/resources + internal/gpu; unit tests inject a
// fake that returns known, deterministic values.
type ResourceSource interface {
	// Sample returns the current ResourceSample for the local node.
	Sample() (ResourceSample, error)
}

// CollectorConfig carries optional configuration for NewNodeCollector.
type CollectorConfig struct {
	// NodeID is the label value used in every emitted metric.  When empty, the
	// collector uses "local".
	NodeID string
}

// NodeCollector holds the latest scraped resource gauges and serves a
// Prometheus-format /metrics endpoint.  It is safe for concurrent use.
type NodeCollector struct {
	mu     sync.RWMutex
	source ResourceSource
	nodeID string
	last   ResourceSample
}

// NewNodeCollector creates a NodeCollector that reads resources from src.
// cfg may be zero-valued; defaults are applied automatically.
func NewNodeCollector(src ResourceSource, cfg CollectorConfig) *NodeCollector {
	nid := cfg.NodeID
	if nid == "" {
		nid = "local"
	}
	return &NodeCollector{
		source: src,
		nodeID: nid,
	}
}

// Collect reads the current resource sample from the source and stores it for
// the next scrape.  It is safe to call concurrently; the latest written sample
// wins.
func (nc *NodeCollector) Collect() error {
	s, err := nc.source.Sample()
	if err != nil {
		return fmt.Errorf("collector sample: %w", err)
	}
	nc.mu.Lock()
	nc.last = s
	nc.mu.Unlock()
	return nil
}

// Handler returns an http.Handler that, on every request, (1) reads a fresh
// sample from the source and (2) writes Prometheus-format gauge lines to the
// response.  A sample error causes a 500 response with the error text.
func (nc *NodeCollector) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := nc.source.Sample()
		if err != nil {
			http.Error(w, fmt.Sprintf("metrics sample error: %v", err), http.StatusInternalServerError)
			return
		}
		nc.mu.Lock()
		nc.last = s
		nc.mu.Unlock()

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, nc.render(s))
	})
}

// Snapshot returns the most recently collected ResourceSample.
// If Collect has never been called the zero value is returned.
func (nc *NodeCollector) Snapshot() ResourceSample {
	nc.mu.RLock()
	defer nc.mu.RUnlock()
	return nc.last
}

// render serialises s into Prometheus text exposition format.  The output is
// deterministic: gauges are emitted in a fixed order (cpu, mem, gpu-when-present)
// with HELP and TYPE lines preceding each value line.  The node_id label is
// present on every gauge to support multi-node aggregation.
func (nc *NodeCollector) render(s ResourceSample) string {
	var b strings.Builder

	nid := s.NodeID
	if nid == "" {
		nid = nc.nodeID
	}

	// helix_node_cpu_pct
	fmt.Fprintf(&b, "# HELP helix_node_cpu_pct Node CPU utilization percent.\n")
	fmt.Fprintf(&b, "# TYPE helix_node_cpu_pct gauge\n")
	fmt.Fprintf(&b, "helix_node_cpu_pct{node_id=%q} %g\n", nid, s.CPUPct)

	// helix_node_mem_pct
	fmt.Fprintf(&b, "# HELP helix_node_mem_pct Node memory utilization percent.\n")
	fmt.Fprintf(&b, "# TYPE helix_node_mem_pct gauge\n")
	fmt.Fprintf(&b, "helix_node_mem_pct{node_id=%q} %g\n", nid, s.MemPct)

	// helix_node_gpu_pct — only when the node actually has a GPU.
	if s.HasGPU {
		fmt.Fprintf(&b, "# HELP helix_node_gpu_pct Node GPU utilization percent (average across all GPUs).\n")
		fmt.Fprintf(&b, "# TYPE helix_node_gpu_pct gauge\n")
		fmt.Fprintf(&b, "helix_node_gpu_pct{node_id=%q} %g\n", nid, s.GPUPct)
	}

	return b.String()
}
