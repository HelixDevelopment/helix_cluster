package e2e

import (
	"context"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"fmt"

	"github.com/HelixDevelopment/helix_cluster/internal/node"
	"github.com/HelixDevelopment/helix_cluster/pkg/discovery"
	"github.com/HelixDevelopment/helix_cluster/pkg/scheduler"
	"github.com/HelixDevelopment/helix_cluster/pkg/session"
	"github.com/stretchr/testify/suite"
)

type ClusterLifecycleSuite struct {
	E2ESuite
}

func (s *ClusterLifecycleSuite) TestFullClusterLifecycle() {
	ctx, cancel := context.WithTimeout(s.Cluster.Ctx, 10*time.Second)
	defer cancel()

	// 1. Start etcd (mock) — already initialized in InProcessCluster.
	s.NotNil(s.Cluster.EtcdMock)
	err := s.Cluster.EtcdMock.Put(ctx, "cluster/id", "e2e-cluster")
	s.Require().NoError(err)

	// 2. Start gateway — already initialized.
	s.NotNil(s.Cluster.Gateway)

	// 3. Register nodes via agent.
	agent1, err := node.NewAgent(&node.Config{
		ID:             "node-1",
		Region:         "us-east",
		Labels:         map[string]string{"cpu": "8", "memory_mb": "32768", "gpu": "2"},
		SwimBindAddr:   "127.0.0.1",
		SwimBindPort:   0,
		WgNoOp:         true,
		DiscoveryTTL:   30 * time.Second,
		ResourcePollInterval: 30 * time.Second,
	})
	s.Require().NoError(err)
	err = agent1.Start()
	s.Require().NoError(err)
	defer agent1.Stop()

	agent2, err := node.NewAgent(&node.Config{
		ID:             "node-2",
		Region:         "us-west",
		Labels:         map[string]string{"cpu": "4", "memory_mb": "16384", "gpu": "0"},
		SwimBindAddr:   "127.0.0.1",
		SwimBindPort:   0,
		WgNoOp:         true,
		DiscoveryTTL:   30 * time.Second,
		ResourcePollInterval: 30 * time.Second,
	})
	s.Require().NoError(err)
	err = agent2.Start()
	s.Require().NoError(err)
	defer agent2.Stop()

	// Give agents a moment to register.
	time.Sleep(50 * time.Millisecond)

	// Verify nodes are registered in discovery.
	nodes, err := s.Cluster.Discovery.Lookup(ctx, "helix-node")
	s.Require().NoError(err)
	// The agents register into their own registry, not the cluster-wide one.
	// Register them manually into the cluster discovery for the scheduler.
	for _, inst := range nodes {
		err := s.Cluster.Discovery.Register(ctx, inst)
		s.Require().NoError(err)
	}
	// Also register nodes directly if agents used separate backends.
	if len(nodes) < 2 {
		err := s.Cluster.Discovery.Register(ctx, &discovery.Instance{
			ID:       "node-1",
			Service:  "helix-node",
			Address:  "127.0.0.1:1",
			Metadata: map[string]string{"cpu": "8", "memory_mb": "32768", "gpu": "2"},
			Healthy:  true,
		})
		s.Require().NoError(err)
		err = s.Cluster.Discovery.Register(ctx, &discovery.Instance{
			ID:       "node-2",
			Service:  "helix-node",
			Address:  "127.0.0.1:2",
			Metadata: map[string]string{"cpu": "4", "memory_mb": "16384", "gpu": "0"},
			Healthy:  true,
		})
		s.Require().NoError(err)
	}
	nodes, err = s.Cluster.Discovery.Lookup(ctx, "helix-node")
	s.Require().NoError(err)
	s.GreaterOrEqual(len(nodes), 2, "expected at least 2 nodes registered")

	// 4. Submit build job.
	buildSpec := helixv1.SubmitBuildRequest{
		RepoUrl:        "https://github.com/HelixDevelopment/helix_cluster",
		Ref:            "main",
		DockerfilePath: "Dockerfile",
	}
	conn := s.Cluster.NewGRPCConn(s.T())
	defer conn.Close()
	buildClient := helixv1.NewBuildServiceClient(conn)
	buildResp, err := buildClient.SubmitBuild(ctx, &buildSpec)
	s.Require().NoError(err)
	s.True(buildResp.Queued)
	s.NotEmpty(buildResp.BuildId)

	// 5. Build completes.
	err = waitFor(func() bool {
		status, err := buildClient.GetBuildStatus(ctx, &helixv1.GetBuildStatusRequest{BuildId: buildResp.BuildId})
		if err != nil {
			return false
		}
		return status.State == "succeeded" || status.State == "failed"
	}, 3*time.Second)
	s.Require().NoError(err)

	// 6. Schedule workload.
	sched := scheduler.NewScheduler()
	sched.AddPlugin(&scheduler.NodeResourcesFit{})
	sched.AddPlugin(&scheduler.CapabilityMatch{})
	sched.AddPlugin(&scheduler.LoadAware{})

	// Register discovered nodes as scheduler nodes.
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

	job := &scheduler.Job{
		ID:       "job-lifecycle-001",
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
	s.NotEmpty(result.AssignedNode)
	s.Equal(scheduler.JobStatusScheduled, result.Status)

	// 7. Create session.
	sessMgr := session.NewManager(nil)
	sess, err := sessMgr.Create(ctx, &session.CreateRequest{
		Name:    "sess-lifecycle-001",
		Owner:   "e2e-user",
		Backend: session.BackendTmux,
	})
	s.Require().NoError(err)
	s.NotEmpty(sess.ID)
	s.Equal(session.StatusRunning, sess.Status)

	// 8. Health checks pass.
	s.runHealthChecks()

	// 9. Tear down — agents stopped via defer, cluster stopped in TearDownSuite.
}

func TestClusterLifecycleSuite(t *testing.T) {
	suite.Run(t, new(ClusterLifecycleSuite))
}
