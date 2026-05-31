package benchmark

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/discovery"
)

func BenchmarkDiscoveryRegister1000(b *testing.B) {
	ctx := context.Background()
	backend := discovery.NewInMemoryBackend()
	reg := discovery.NewServiceRegistry(backend)

	instances := make([]*discovery.Instance, 1000)
	for i := 0; i < 1000; i++ {
		instances[i] = &discovery.Instance{
			ID:       fmt.Sprintf("svc-%d", i),
			Service:  "bench-service",
			Address:  fmt.Sprintf("10.0.0.%d", i%256),
			Port:     8080 + i,
			Healthy:  true,
			LastSeen: time.Now(),
			TTL:      30 * time.Second,
		}
	}

	b.ResetTimer()
	start := time.Now()
	for i := 0; i < b.N; i++ {
		for _, inst := range instances {
			_ = reg.Register(ctx, inst)
		}
	}
	elapsed := time.Since(start)
	b.ReportMetric(float64(1000*b.N)/elapsed.Seconds(), "registrations/sec")
	b.ReportMetric(float64(elapsed.Nanoseconds())/float64(1000*b.N), "ns/registration")

	// Benchmark list all.
	b.Run("ListAll", func(b *testing.B) {
		start := time.Now()
		for i := 0; i < b.N; i++ {
			_, _ = reg.Lookup(ctx, "bench-service")
		}
		elapsed := time.Since(start)
		b.ReportMetric(float64(elapsed.Nanoseconds())/float64(b.N), "ns/list")
	})
}
