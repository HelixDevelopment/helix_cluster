package benchmark

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/session"
)

func BenchmarkSessionCreate1000(b *testing.B) {
	ctx := context.Background()
	mgr := session.NewManager(nil)

	var m1, m2 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m1)

	b.ResetTimer()
	start := time.Now()
	for i := 0; i < b.N; i++ {
		for j := 0; j < 1000; j++ {
			_, err := mgr.Create(ctx, &session.CreateRequest{
				Name:    fmt.Sprintf("bench-sess-%d-%d", i, j),
				Owner:   "bench-user",
				Backend: session.BackendTmux,
			})
			if err != nil {
				b.Fatalf("create session: %v", err)
			}
		}
	}
	elapsed := time.Since(start)
	b.ReportMetric(float64(1000*b.N)/elapsed.Seconds(), "sessions/sec")
	b.ReportMetric(float64(elapsed.Nanoseconds())/float64(1000*b.N), "ns/session")

	runtime.GC()
	runtime.ReadMemStats(&m2)
	allocBytes := int64(m2.TotalAlloc - m1.TotalAlloc)
	b.ReportMetric(float64(allocBytes)/float64(1000*b.N), "bytes/session")
}
