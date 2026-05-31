package benchmark

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/internal/messaging"
)

func BenchmarkMessagingPublish10000(b *testing.B) {
	bus := messaging.NewBus()
	ctx := context.Background()
	msgCount := 10000

	var received atomic.Int64
	subID, err := bus.Subscribe(ctx, "bench.topic", func(_ context.Context, msg *messaging.Message) error {
		received.Add(1)
		return nil
	})
	if err != nil {
		b.Fatalf("subscribe: %v", err)
	}
	_ = subID

	payload := []byte("benchmark payload")

	b.ResetTimer()
	start := time.Now()
	for i := 0; i < b.N; i++ {
		var wg sync.WaitGroup
		for j := 0; j < msgCount; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				msg := messaging.NewMessage("bench.topic", payload)
				_ = bus.Publish(ctx, "bench.topic", msg)
			}()
		}
		wg.Wait()
	}
	elapsed := time.Since(start)
	b.ReportMetric(float64(msgCount*b.N)/elapsed.Seconds(), "messages/sec")
	b.ReportMetric(float64(elapsed.Nanoseconds())/float64(msgCount*b.N), "ns/message")
}
