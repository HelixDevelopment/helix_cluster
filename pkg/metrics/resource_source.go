// Package metrics — production ResourceSource backed by pkg/resources + internal/gpu.
package metrics

import (
	"fmt"

	"github.com/HelixDevelopment/helix_cluster/internal/gpu"
	"github.com/HelixDevelopment/helix_cluster/pkg/resources"
)

// ResourcesSource is the production ResourceSource implementation.  It reads
// CPU and memory from a resources.Reader (e.g. resources.NewDarwinReader() on
// macOS) and GPU utilization from a gpu.Manager.  GPU utilization is derived
// from the average UtilizationPercent across all GPUs reported by the manager.
//
// Callers that do not have a gpu.Manager (or run on a node without GPUs) may
// pass nil; in that case HasGPU will be false and GPUPct will be zero.
type ResourcesSource struct {
	reader resources.Reader
	gpuMgr *gpu.Manager
	nodeID string
}

// NewResourcesSource creates a production ResourceSource.
//   - reader must be non-nil; on darwin pass resources.NewDarwinReader().
//   - gpuMgr may be nil if GPU metrics are not required.
//   - nodeID is the node label; use the hostname or cluster-assigned ID.
func NewResourcesSource(reader resources.Reader, gpuMgr *gpu.Manager, nodeID string) *ResourcesSource {
	return &ResourcesSource{
		reader: reader,
		gpuMgr: gpuMgr,
		nodeID: nodeID,
	}
}

// Sample implements ResourceSource. It calls reader.Read to get CPU/mem and
// queries the gpu.Manager (if present) to compute average GPU utilization.
func (s *ResourcesSource) Sample() (ResourceSample, error) {
	nr, err := s.reader.Read(s.nodeID)
	if err != nil {
		return ResourceSample{}, fmt.Errorf("resources read: %w", err)
	}

	// CPU utilization: UsedCores / Cores * 100.
	// pkg/resources.DarwinReader does not yet populate UsedCores (it tracks
	// logical core count only); we emit 0 % rather than fabricate a number.
	var cpuPct float64
	if nr.CPU.Cores > 0 && nr.CPU.UsedCores > 0 {
		cpuPct = (nr.CPU.UsedCores / float64(nr.CPU.Cores)) * 100.0
	}

	// Memory utilization: UsedKB / TotalKB * 100.
	var memPct float64
	if nr.Memory.TotalKB > 0 {
		memPct = float64(nr.Memory.UsedKB) / float64(nr.Memory.TotalKB) * 100.0
	}

	// GPU utilization from manager when present.
	var gpuPct float64
	hasGPU := false
	if s.gpuMgr != nil {
		gpus := s.gpuMgr.ListGPUs()
		if len(gpus) > 0 {
			hasGPU = true
			var sum float64
			for _, g := range gpus {
				sum += g.UtilizationPercent
			}
			gpuPct = sum / float64(len(gpus))
		}
	}

	return ResourceSample{
		NodeID: s.nodeID,
		CPUPct: cpuPct,
		MemPct: memPct,
		GPUPct: gpuPct,
		HasGPU: hasGPU,
	}, nil
}
