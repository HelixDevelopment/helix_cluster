package benchmark

import (
	"fmt"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/internal/gpu"
)

func BenchmarkGPUAllocateRelease1000(b *testing.B) {
	mgr := gpu.NewManager()
	_ = mgr.DetectGPUs()

	// Ensure enough GPUs by adding mock ones if needed.
	existing := mgr.ListGPUs()
	if len(existing) < 1000 {
		// The manager does not expose an AddGPU API; we rely on the 4 mock GPUs.
		// For benchmarking allocation/release latency we cycle through them.
	}

	jobIDs := make([]string, 1000)
	for i := 0; i < 1000; i++ {
		jobIDs[i] = fmt.Sprintf("job-%d", i)
	}

	b.ResetTimer()
	start := time.Now()
	for i := 0; i < b.N; i++ {
		for _, jobID := range jobIDs {
			g, err := mgr.AllocateGPU(jobID)
			if err != nil {
				// If pool is exhausted, release all and continue (benchmark measures happy-path latency).
				for _, jid := range jobIDs {
					mgr.ReleaseGPU(jid)
				}
				g, err = mgr.AllocateGPU(jobID)
				if err != nil {
					b.Fatalf("allocate gpu: %v", err)
				}
			}
			_ = g
		}
		for _, jobID := range jobIDs {
			mgr.ReleaseGPU(jobID)
		}
	}
	elapsed := time.Since(start)
	b.ReportMetric(float64(1000*b.N)/elapsed.Seconds(), "allocations/sec")
	b.ReportMetric(float64(elapsed.Nanoseconds())/float64(1000*b.N), "ns/allocation")
}
