package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	internalhealth "github.com/HelixDevelopment/helix_cluster/internal/health"
	"github.com/HelixDevelopment/helix_cluster/pkg/discovery"
	"github.com/HelixDevelopment/helix_cluster/pkg/scheduler"
	"github.com/HelixDevelopment/helix_cluster/pkg/session"
	"github.com/stretchr/testify/suite"
)

type FailureRecoverySuite struct {
	E2ESuite
}

func (s *FailureRecoverySuite) TestNodeFailureWorkloadRescheduledSessionRecovered() {
	ctx, cancel := context.WithTimeout(s.Cluster.Ctx, 10*time.Second)
	defer cancel()

	// Register two nodes in discovery.
	err := s.Cluster.Discovery.Register(ctx, &discovery.Instance{
		ID:       "node-a",
		Service:  "helix-node",
		Address:  "127.0.0.1:1",
		Metadata: map[string]string{"cpu": "8", "memory_mb": "32768", "gpu": "2"},
		Healthy:  true,
	})
	s.Require().NoError(err)
	err = s.Cluster.Discovery.Register(ctx, &discovery.Instance{
		ID:       "node-b",
		Service:  "helix-node",
		Address:  "127.0.0.1:2",
		Metadata: map[string]string{"cpu": "4", "memory_mb": "16384", "gpu": "0"},
		Healthy:  true,
	})
	s.Require().NoError(err)

	// Create scheduler and register nodes.
	sched := scheduler.NewScheduler()
	sched.AddPlugin(&scheduler.NodeResourcesFit{})
	sched.AddPlugin(&scheduler.CapabilityMatch{})
	sched.AddPlugin(&scheduler.LoadAware{})

	nodes, err := s.Cluster.Discovery.Lookup(ctx, "helix-node")
	s.Require().NoError(err)
	for _, inst := range nodes {
		res := scheduler.Resources{}
		if inst.Metadata != nil {
			if v, ok := inst.Metadata["cpu"]; ok {
				_, _ = fmt.Sscanf(v, "%f", &res.CPU)
			}
			if v, ok := inst.Metadata["memory_mb"]; ok {
				var mem uint64
				_, _ = fmt.Sscanf(v, "%d", &mem)
				res.Memory = mem
			}
			if v, ok := inst.Metadata["gpu"]; ok {
				var gpu int
				_, _ = fmt.Sscanf(v, "%d", &gpu)
				res.GPU = gpu
			}
		}
		sched.RegisterNode(&scheduler.Node{
			ID:                 inst.ID,
			AvailableResources: res,
			Labels:             inst.Metadata,
		})
	}

	// Schedule a job.
	job := &scheduler.Job{
		ID:       "job-recovery-001",
		Command:  "echo hello",
		Status:   scheduler.JobStatusPending,
		Priority: 1,
		Resources: scheduler.Resources{
			CPU:    1.0,
			Memory: 1024,
			GPU:    0,
		},
	}
	sched.Queue().Add(job)
	result, err := sched.Schedule(job)
	s.Require().NoError(err)
	originalNode := result.AssignedNode
	s.NotEmpty(originalNode)

	// Create a session for the job.
	sessMgr := session.NewManager(nil)
	sess, err := sessMgr.Create(ctx, &session.CreateRequest{
		Name:    "sess-recovery-001",
		Owner:   "e2e-user",
		Backend: session.BackendTmux,
	})
	s.Require().NoError(err)
	s.Equal(session.StatusRunning, sess.Status)

	// Simulate node failure: remove the assigned node.
	sched.UnregisterNode(originalNode)
	err = s.Cluster.Discovery.Deregister(ctx, "helix-node", originalNode)
	s.Require().NoError(err)

	// Reschedule the same job.
	job.Status = scheduler.JobStatusPending
	sched.Queue().Add(job)
	result2, err := sched.Schedule(job)
	s.Require().NoError(err)
	s.NotEmpty(result2.AssignedNode)
	s.NotEqual(originalNode, result2.AssignedNode, "expected job to be rescheduled to a different node")

	// Migrate session to the new node.
	err = sessMgr.Migrate(ctx, sess.ID, result2.AssignedNode)
	s.Require().NoError(err)

	migratedSess, err := sessMgr.Get(sess.ID)
	s.Require().NoError(err)
	s.Equal(result2.AssignedNode, migratedSess.NodeID)
	s.Equal(session.StatusMigrating, migratedSess.Status)
}

func (s *FailureRecoverySuite) TestEtcdPartitionServicesDegradeGracefully() {
	ctx, cancel := context.WithTimeout(s.Cluster.Ctx, 10*time.Second)
	defer cancel()

	// Simulate etcd partition by making the mock fail all operations.
	partitioned := &partitionedEtcdMock{EtcdClientMock: NewEtcdClientMock()}

	// Register a check that uses etcd.
	s.Cluster.HealthAgg.RegisterCheck("etcd-partition", internalhealth.CheckFunc(func(_ context.Context) (internalhealth.Status, error) {
		if partitioned.partitioned {
			return internalhealth.Degraded, fmt.Errorf("etcd partition simulated")
		}
		return internalhealth.Healthy, nil
	}))

	// Before partition: healthy.
	s.Cluster.HealthAgg.RunChecks(ctx)
	st, _ := s.Cluster.HealthAgg.StatusWithDetails()
	s.Equal(internalhealth.Healthy, st)

	// Trigger partition.
	partitioned.partitioned = true
	s.Cluster.HealthAgg.RunChecks(ctx)
	st, _ = s.Cluster.HealthAgg.StatusWithDetails()
	s.Equal(internalhealth.Degraded, st)

	// Other services should still report healthy.
	gwStatus, ok := s.Cluster.HealthAgg.GetServiceStatus("gateway")
	s.True(ok)
	s.Equal(internalhealth.Healthy, gwStatus.Status)
}

type partitionedEtcdMock struct {
	*EtcdClientMock
	partitioned bool
}

func TestFailureRecoverySuite(t *testing.T) {
	suite.Run(t, new(FailureRecoverySuite))
}
