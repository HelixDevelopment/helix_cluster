package benchmark

import (
	"fmt"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/scheduler"
)

func BenchmarkSchedulerSchedule100(b *testing.B) {
	benchSchedule(b, 100)
}

func BenchmarkSchedulerSchedule1000(b *testing.B) {
	benchSchedule(b, 1000)
}

func BenchmarkSchedulerSchedule10000(b *testing.B) {
	benchSchedule(b, 10000)
}

func benchSchedule(b *testing.B, count int) {
	s := scheduler.NewScheduler()
	s.AddPlugin(&scheduler.NodeResourcesFit{})
	s.AddPlugin(&scheduler.CapabilityMatch{})
	s.AddPlugin(&scheduler.LoadAware{})

	// Register a pool of nodes.
	for i := 0; i < 10; i++ {
		s.RegisterNode(&scheduler.Node{
			ID: fmt.Sprintf("node-%d", i),
			AvailableResources: scheduler.Resources{
				CPU:    8.0,
				Memory: 32768,
				GPU:    2,
			},
			Labels: map[string]string{"zone": fmt.Sprintf("zone-%d", i%3)},
		})
	}

	jobs := make([]*scheduler.Job, count)
	for i := 0; i < count; i++ {
		jobs[i] = &scheduler.Job{
			ID:       fmt.Sprintf("job-%d", i),
			Command:  "echo hello",
			Status:   scheduler.JobStatusPending,
			Priority: i % 10,
			Resources: scheduler.Resources{
				CPU:    0.5,
				Memory: 512,
				GPU:    0,
			},
			Labels: map[string]string{},
		}
	}

	b.ResetTimer()
	start := time.Now()
	for i := 0; i < b.N; i++ {
		// Re-enqueue jobs for each iteration.
		for _, job := range jobs {
			job.Status = scheduler.JobStatusPending
			s.Queue().Add(job)
		}
		for j := 0; j < count; j++ {
			item := s.Queue().Pop()
			if item == nil {
				b.Fatal("queue empty")
			}
			_, _ = s.Schedule(item)
			// Ignore no-node errors after resources are exhausted; benchmark measures throughput.
		}
	}
	elapsed := time.Since(start)
	b.ReportMetric(float64(count*b.N)/elapsed.Seconds(), "jobs/sec")
	b.ReportMetric(float64(elapsed.Nanoseconds())/float64(count*b.N), "ns/job")
}
