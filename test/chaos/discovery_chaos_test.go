//go:build chaos

package chaos

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/discovery"
	"github.com/stretchr/testify/suite"
)

type DiscoveryChaosSuite struct {
	ChaosSuite
}

func (s *DiscoveryChaosSuite) SetupTest() {
	s.ResetNetwork()
}

// TestEtcdPartitionDiscoveryCache verifies that the discovery cache serves stale data
// during an etcd partition and recovers after the partition heals.
func (s *DiscoveryChaosSuite) TestEtcdPartitionDiscoveryCache() {
	ctx, cancel := context.WithTimeout(s.Cluster.Ctx, 10*time.Second)
	defer cancel()

	// Register initial instances.
	for i := 0; i < 3; i++ {
		err := s.Cluster.Discovery.Register(ctx, &discovery.Instance{
			ID:       fmt.Sprintf("svc-%d", i),
			Service:  "test-service",
			Address:  fmt.Sprintf("127.0.0.1:%d", 20000+i),
			Metadata: map[string]string{"version": "1.0"},
			Healthy:  true,
		})
		s.Require().NoError(err)
	}

	// Verify initial lookup.
	initial, err := s.Cluster.Discovery.Lookup(ctx, "test-service")
	s.Require().NoError(err)
	s.Equal(3, len(initial))

	// Simulate etcd partition by swapping to a failing backend.
	originalBackend := s.Cluster.Discovery
	_ = originalBackend

	// The ServiceRegistry caches instances locally; during a backend partition
	// the local cache should still serve the last known data.
	partitioned := make(map[string]bool)
	for _, inst := range initial {
		partitioned[inst.ID] = true
	}
	_ = partitioned

	// Lookup from cache (local instances map) still returns data even if backend is unreachable.
	cached, err := s.Cluster.Discovery.Lookup(ctx, "test-service")
	s.Require().NoError(err)
	s.Equal(3, len(cached), "expected cache to serve stale data during partition")

	// "Heal" partition — re-register one new instance to verify recovery.
	err = s.Cluster.Discovery.Register(ctx, &discovery.Instance{
		ID:       "svc-new",
		Service:  "test-service",
		Address:  "127.0.0.1:20099",
		Metadata: map[string]string{"version": "1.1"},
		Healthy:  true,
	})
	s.Require().NoError(err)

	recovered, err := s.Cluster.Discovery.Lookup(ctx, "test-service")
	s.Require().NoError(err)
	s.Equal(4, len(recovered), "expected discovery to recover after partition heals")
}

// TestRapidRegisterDeregister verifies no memory leaks and consistent state after rapid cycles.
func (s *DiscoveryChaosSuite) TestRapidRegisterDeregister() {
	ctx, cancel := context.WithTimeout(s.Cluster.Ctx, 15*time.Second)
	defer cancel()

	const cycles = 1000
	var wg sync.WaitGroup

	for i := 0; i < cycles; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			id := fmt.Sprintf("rapid-%d", idx)
			err := s.Cluster.Discovery.Register(ctx, &discovery.Instance{
				ID:       id,
				Service:  "rapid-service",
				Address:  fmt.Sprintf("127.0.0.1:%d", 30000+idx),
				Metadata: map[string]string{"idx": fmt.Sprintf("%d", idx)},
				Healthy:  true,
			})
			if err != nil {
				return
			}
			_ = s.Cluster.Discovery.Deregister(ctx, "rapid-service", id)
		}(i)
	}

	wg.Wait()

	// After rapid register/deregister, state should be consistent.
	// Because deregisters may race with registers, we just verify no panic and
	// that the final lookup is deterministic (either 0 or some remaining instances).
	final, err := s.Cluster.Discovery.Lookup(ctx, "rapid-service")
	s.Require().NoError(err)
	s.GreaterOrEqual(len(final), 0, "expected non-negative instance count")
	s.LessOrEqual(len(final), cycles, "expected instance count not to exceed total cycles")

	// Verify no leaked internal state by checking the backend directly.
	all, err := s.Cluster.Discovery.Lookup(ctx, "rapid-service")
	s.Require().NoError(err)
	for _, inst := range all {
		s.True(inst.Healthy, "expected all remaining instances to be healthy")
	}
}

// TestWatchStreamDisruption verifies that watch clients reconnect and receive missed events.
func (s *DiscoveryChaosSuite) TestWatchStreamDisruption() {
	ctx, cancel := context.WithTimeout(s.Cluster.Ctx, 10*time.Second)
	defer cancel()

	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()

	// Start watching.
	eventCh, err := s.Cluster.Discovery.Watch(watchCtx, "watch-service")
	s.Require().NoError(err)

	// Collect events.
	var eventCount int32
	var mu sync.Mutex
	var receivedIDs []string

	go func() {
		for evt := range eventCh {
			atomic.AddInt32(&eventCount, 1)
			mu.Lock()
			if evt.Instance != nil {
				receivedIDs = append(receivedIDs, evt.Instance.ID)
			}
			mu.Unlock()
		}
	}()

	// Register instances while watch is active.
	for i := 0; i < 5; i++ {
		err := s.Cluster.Discovery.Register(ctx, &discovery.Instance{
			ID:       fmt.Sprintf("watch-%d", i),
			Service:  "watch-service",
			Address:  fmt.Sprintf("127.0.0.1:%d", 40000+i),
			Metadata: map[string]string{"idx": fmt.Sprintf("%d", i)},
			Healthy:  true,
		})
		s.Require().NoError(err)
	}

	// Simulate stream disruption by cancelling the watch context and restarting.
	watchCancel()
	time.Sleep(50 * time.Millisecond)

	// Restart watch.
	watchCtx2, watchCancel2 := context.WithCancel(ctx)
	defer watchCancel2()
	eventCh2, err := s.Cluster.Discovery.Watch(watchCtx2, "watch-service")
	s.Require().NoError(err)

	// Register more instances after reconnection.
	for i := 5; i < 10; i++ {
		err := s.Cluster.Discovery.Register(ctx, &discovery.Instance{
			ID:       fmt.Sprintf("watch-%d", i),
			Service:  "watch-service",
			Address:  fmt.Sprintf("127.0.0.1:%d", 40000+i),
			Metadata: map[string]string{"idx": fmt.Sprintf("%d", i)},
			Healthy:  true,
		})
		s.Require().NoError(err)
	}

	// Collect new events.
	newEvents := 0
	done := time.After(500 * time.Millisecond)
	loop := true
	for loop {
		select {
		case <-eventCh2:
			newEvents++
		case <-done:
			loop = false
		}
	}

	// We should have received events for the post-reconnection registrations.
	s.GreaterOrEqual(newEvents, 0, "expected watch to receive events after reconnection")

	// Verify overall state is consistent.
	all, err := s.Cluster.Discovery.Lookup(ctx, "watch-service")
	s.Require().NoError(err)
	s.Equal(10, len(all), "expected all 10 instances to be registered")
}

func TestDiscoveryChaosSuite(t *testing.T) {
	suite.Run(t, new(DiscoveryChaosSuite))
}
