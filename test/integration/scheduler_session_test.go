package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"github.com/HelixDevelopment/helix_cluster/pkg/discovery"
	pkgscheduler "github.com/HelixDevelopment/helix_cluster/pkg/scheduler"
	"github.com/HelixDevelopment/helix_cluster/pkg/session"
	"github.com/stretchr/testify/suite"
)

type SchedulerSessionSuite struct {
	IntegrationSuite
}

func (s *SchedulerSessionSuite) TestSubmitJobSchedulerAssignsNodeSessionCreated() {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()

	// Use real pkg/scheduler.Scheduler and pkg/session.Manager.
	sched := pkgscheduler.NewScheduler()
	sched.AddPlugin(&pkgscheduler.NodeResourcesFit{})
	sched.AddPlugin(&pkgscheduler.CapabilityMatch{})
	sched.AddPlugin(&pkgscheduler.LoadAware{})

	sched.RegisterNode(&pkgscheduler.Node{
		ID: "node-1",
		AvailableResources: pkgscheduler.Resources{
			CPU:    4.0,
			Memory: 8192,
			GPU:    1,
		},
		Labels: map[string]string{},
	})

	job := &pkgscheduler.Job{
		ID:       "job-001",
		Command:  "echo hello",
		Status:   pkgscheduler.JobStatusPending,
		Priority: 1,
		Resources: pkgscheduler.Resources{
			CPU:    1.0,
			Memory: 1024,
			GPU:    0,
		},
	}
	sched.Queue().Add(job)

	result, err := sched.Schedule(job)
	s.Require().NoError(err)
	s.NotEmpty(result.AssignedNode)
	s.Equal(pkgscheduler.JobStatusScheduled, result.Status)

	// Create a session for the scheduled job using real session.Manager.
	mgr := session.NewManager(nil)
	sess, err := mgr.Create(ctx, &session.CreateRequest{
		Name:    "session-job-001",
		Owner:   "user-1",
		Backend: session.BackendTmux,
	})
	s.Require().NoError(err)
	s.NotEmpty(sess.ID)
	s.Equal(session.StatusRunning, sess.Status)
}

func (s *SchedulerSessionSuite) TestCancelJobSessionCleanedUp() {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()

	// Setup scheduler with a node.
	sched := pkgscheduler.NewScheduler()
	sched.AddPlugin(&pkgscheduler.NodeResourcesFit{})
	sched.RegisterNode(&pkgscheduler.Node{
		ID: "node-2",
		AvailableResources: pkgscheduler.Resources{
			CPU:    4.0,
			Memory: 8192,
			GPU:    0,
		},
		Labels: map[string]string{},
	})

	job := &pkgscheduler.Job{
		ID:       "job-002",
		Command:  "echo hello",
		Status:   pkgscheduler.JobStatusPending,
		Priority: 1,
		Resources: pkgscheduler.Resources{
			CPU:    1.0,
			Memory: 1024,
		},
	}
	sched.Queue().Add(job)
	result, err := sched.Schedule(job)
	s.Require().NoError(err)
	s.Equal("node-2", result.AssignedNode)

	// Create a session for the job.
	mgr := session.NewManager(nil)
	sess, err := mgr.Create(ctx, &session.CreateRequest{
		Name:    "session-job-002",
		Owner:   "user-1",
		Backend: session.BackendTmux,
	})
	s.Require().NoError(err)
	s.Equal(session.StatusRunning, sess.Status)

	// Cancel the job by marking it failed.
	job.Status = pkgscheduler.JobStatusFailed

	// Terminate the session.
	err = mgr.Terminate(ctx, sess.ID)
	s.Require().NoError(err)

	// Verify session is terminated (status changed, but Get still returns it).
	terminatedSess, err := mgr.Get(sess.ID)
	s.Require().NoError(err)
	s.Equal(session.StatusTerminated, terminatedSess.Status)
}

// Helper to adapt discovery Instance to scheduler Node (used conceptually).
func instanceToSchedulerNode(inst *discovery.Instance) *pkgscheduler.Node {
	res := pkgscheduler.Resources{}
	if inst.Metadata != nil {
		if v, ok := inst.Metadata["cpu"]; ok {
			fmt.Sscanf(v, "%f", &res.CPU)
		}
		if v, ok := inst.Metadata["memory_mb"]; ok {
			var mem uint64
			fmt.Sscanf(v, "%d", &mem)
			res.Memory = mem
		}
		if v, ok := inst.Metadata["gpu"]; ok {
			var gpu int
			fmt.Sscanf(v, "%d", &gpu)
			res.GPU = gpu
		}
	}
	return &pkgscheduler.Node{
		ID:                 inst.ID,
		AvailableResources: res,
		Labels:             inst.Metadata,
	}
}

// Ensure helixv1 is used so the import is not removed.
var _ = &helixv1.ScheduleJobRequest{}

func TestSchedulerSessionSuite(t *testing.T) {
	suite.Run(t, new(SchedulerSessionSuite))
}
