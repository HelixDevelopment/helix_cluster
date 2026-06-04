package scheduler

// complete_stress_chaos_test.go — sustained-load (stress) and mid-flight cancel
// (chaos) coverage for the F6 placement/Complete/Release leak fix, complementing
// the unit guards in complete_test.go (per Constitution §11.4.85).
//
//   STRESS: many goroutines churn Schedule -> Complete/Release over many rounds
//           on a shared scheduler. The leak invariant: after every job that was
//           scheduled is completed/released, the placement map drains back to
//           empty AND every node's resources are restored EXACTLY to baseline
//           (no double-restore, no leaked consumption) — proving placements do
//           not accumulate and accounting stays balanced under contention.
//   CHAOS:  Complete and Release race on the SAME jobID (mid-flight teardown
//           from two paths at once, e.g. normal completion racing a cancel).
//           The invariant: the placement is released exactly once — resources are
//           restored exactly once (idempotent teardown), never double-counted —
//           and no panic/deadlock occurs.
//
// Run with -race.

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestComplete_StressNoPlacementLeak_F6 schedules and completes jobs across many
// concurrent workers over many rounds, then asserts the placement map is empty
// and node resources are restored exactly to baseline. A leak (placements not
// released) or an accounting imbalance (double-restore / lost consumption) fails.
func TestComplete_StressNoPlacementLeak_F6(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})

	const (
		nodes       = 8
		baselineCPU = 1000.0
		baselineMem = uint64(1 << 20)
		baselineGPU = 256
	)
	for i := 0; i < nodes; i++ {
		s.RegisterNode(&Node{
			ID:                 nodeID(i),
			AvailableResources: Resources{CPU: baselineCPU, Memory: baselineMem, GPU: baselineGPU},
		})
	}

	const (
		workers       = 32
		jobsPerWorker = 80
	)
	var scheduled int64

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for j := 0; j < jobsPerWorker; j++ {
				job := &Job{
					ID:        jobID(w, j),
					Resources: Resources{CPU: 1, Memory: 1024, GPU: 1},
					Priority:  5,
				}
				res, err := s.Schedule(job)
				if err != nil {
					// Capacity is large vs. live placements; a transient
					// ErrNoNodeAvailable would itself be a leak symptom.
					t.Errorf("schedule %s failed: %v", job.ID, err)
					return
				}
				_ = res
				atomic.AddInt64(&scheduled, 1)
				// Tear the placement down via Complete or Release (both must
				// release exactly one placement and restore resources once).
				if j%2 == 0 {
					s.Complete(job.ID)
				} else {
					s.Release(job.ID)
				}
			}
		}(w)
	}
	wg.Wait()

	require.Equal(t, int64(workers*jobsPerWorker), atomic.LoadInt64(&scheduled),
		"every job must have scheduled successfully (no capacity leak from un-released placements)")

	// Leak invariant: placement map fully drained.
	require.Empty(t, s.RunningPlacements(),
		"placement map must drain to empty after all jobs complete (F6 leak)")

	// Accounting invariant: every node restored exactly to baseline.
	for i := 0; i < nodes; i++ {
		node, ok := s.GetNode(nodeID(i))
		require.True(t, ok)
		require.Equalf(t, baselineCPU, node.AvailableResources.CPU,
			"node %s CPU not restored exactly to baseline", nodeID(i))
		require.Equalf(t, baselineMem, node.AvailableResources.Memory,
			"node %s Memory not restored exactly to baseline", nodeID(i))
		require.Equalf(t, baselineGPU, node.AvailableResources.GPU,
			"node %s GPU not restored exactly to baseline", nodeID(i))
	}
}

// TestComplete_ChaosCompleteRacesRelease_F6 races Complete and Release (plus a
// duplicate of each) on the SAME jobID for many independently-scheduled jobs.
// This is the mid-flight double-teardown scenario (completion racing cancel).
// The invariant: exactly ONE of the racing teardowns reports true (released the
// placement), the rest are no-ops, and the node's resources are restored EXACTLY
// once — never double-counted past baseline.
func TestComplete_ChaosCompleteRacesRelease_F6(t *testing.T) {
	const (
		baselineCPU = 100000.0
		baselineMem = uint64(1 << 30)
		baselineGPU = 100000
		jobs        = 300
	)
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.RegisterNode(&Node{
		ID:                 "n1",
		AvailableResources: Resources{CPU: baselineCPU, Memory: baselineMem, GPU: baselineGPU},
	})

	// Schedule all jobs up front.
	ids := make([]string, 0, jobs)
	for i := 0; i < jobs; i++ {
		job := &Job{ID: jobID(0, i), Resources: Resources{CPU: 1, Memory: 1024, GPU: 1}, Priority: 5}
		if _, err := s.Schedule(job); err != nil {
			t.Fatalf("schedule %s: %v", job.ID, err)
		}
		ids = append(ids, job.ID)
	}

	// For each job, fire 4 racing teardowns: Complete, Complete, Release, Release.
	var wg sync.WaitGroup
	releasedCounts := make([]int64, jobs)
	for idx, id := range ids {
		idx, id := idx, id
		for k := 0; k < 4; k++ {
			wg.Add(1)
			k := k
			go func() {
				defer wg.Done()
				var ok bool
				if k%2 == 0 {
					ok = s.Complete(id)
				} else {
					ok = s.Release(id)
				}
				if ok {
					atomic.AddInt64(&releasedCounts[idx], 1)
				}
			}()
		}
	}
	wg.Wait()

	// Exactly-once teardown per job.
	for idx, id := range ids {
		require.Equalf(t, int64(1), atomic.LoadInt64(&releasedCounts[idx]),
			"job %s must be released exactly once across racing Complete/Release", id)
	}

	// Placement map drained and resources restored EXACTLY to baseline (a
	// double-restore would push availability above baseline).
	require.Empty(t, s.RunningPlacements())
	node, ok := s.GetNode("n1")
	require.True(t, ok)
	require.Equal(t, baselineCPU, node.AvailableResources.CPU, "CPU double-restored or leaked")
	require.Equal(t, baselineMem, node.AvailableResources.Memory, "Memory double-restored or leaked")
	require.Equal(t, baselineGPU, node.AvailableResources.GPU, "GPU double-restored or leaked")
}

func nodeID(i int) string {
	return "node-" + itoa(i)
}

func jobID(w, j int) string {
	return "job-" + itoa(w) + "-" + itoa(j)
}

// itoa is a tiny base-10 formatter to avoid pulling strconv into a test that
// already keeps its imports minimal; values here are small non-negative ints.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
