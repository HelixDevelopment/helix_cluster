// Package scheduler provides a gRPC server implementing the SchedulerService API.
package scheduler

import (
	"context"
	"fmt"
	"sync"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"github.com/HelixDevelopment/helix_cluster/pkg/discovery"
	"github.com/HelixDevelopment/helix_cluster/pkg/scheduler"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements helixv1.SchedulerServiceServer backed by pkg/scheduler.
type Server struct {
	helixv1.UnimplementedSchedulerServiceServer

	mu        sync.RWMutex
	sched     *scheduler.Scheduler
	registry  *discovery.ServiceRegistry
	jobNodes  map[string]string // jobID -> assigned nodeID
}

// NewServer creates a new Scheduler gRPC server wired to pkg/scheduler
// and an in-memory discovery registry.
func NewServer() *Server {
	backend := discovery.NewInMemoryBackend()
	reg := discovery.NewServiceRegistry(backend)
	sched := scheduler.NewScheduler()
	// Register default plugins for real scheduling behaviour.
	sched.AddPlugin(&scheduler.NodeResourcesFit{})
	sched.AddPlugin(&scheduler.CapabilityMatch{})
	sched.AddPlugin(&scheduler.LoadAware{})

	return &Server{
		sched:    sched,
		registry: reg,
		jobNodes: make(map[string]string),
	}
}

// ScheduleJob creates a scheduler.Job, enqueues it, runs scheduling against
// discovered nodes, and returns the assigned node.
func (s *Server) ScheduleJob(ctx context.Context, req *helixv1.ScheduleJobRequest) (*helixv1.ScheduleJobResponse, error) {
	job := &scheduler.Job{
		ID:       req.JobId,
		Labels:   req.Constraints,
		Priority: 0,
		Status:   scheduler.JobStatusPending,
	}
	if job.ID == "" {
		job.ID = fmt.Sprintf("job-%d", time.Now().UnixNano())
	}
	if req.Requirements != nil {
		job.Resources = scheduler.Resources{
			CPU:    float64(req.Requirements.CpuMillicores) / 1000.0,
			Memory: uint64(req.Requirements.MemoryBytes / (1024 * 1024)),
		}
		if len(req.Requirements.GpuIds) > 0 {
			job.Resources.GPU = len(req.Requirements.GpuIds)
		}
	}

	// Discover available nodes from the registry.
	instances, err := s.registry.Lookup(ctx, "helix-node")
	if err != nil {
		return nil, status.Errorf(codes.Internal, "discovery lookup failed: %v", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Register any newly discovered nodes with the scheduler.
	for _, inst := range instances {
		if _, ok := s.sched.GetNode(inst.ID); !ok {
			node := instanceToNode(inst)
			s.sched.RegisterNode(node)
		}
	}

	// Enqueue and schedule.
	s.sched.Queue().Add(job)
	result, err := s.sched.Schedule(job)
	if err != nil {
		return &helixv1.ScheduleJobResponse{
			JobId:     job.ID,
			NodeId:    "",
			Scheduled: false,
		}, status.Errorf(codes.ResourceExhausted, "scheduling failed: %v", err)
	}

	s.jobNodes[job.ID] = result.AssignedNode

	return &helixv1.ScheduleJobResponse{
		JobId:     job.ID,
		NodeId:    result.AssignedNode,
		Scheduled: true,
	}, nil
}

// CancelJob cancels a scheduled job.
func (s *Server) CancelJob(ctx context.Context, req *helixv1.CancelJobRequest) (*helixv1.CancelJobResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.jobNodes[req.JobId]; !ok {
		return nil, status.Errorf(codes.NotFound, "job %s not found", req.JobId)
	}

	delete(s.jobNodes, req.JobId)
	return &helixv1.CancelJobResponse{Cancelled: true}, nil
}

// GetJobStatus returns the status of a job by ID.
func (s *Server) GetJobStatus(ctx context.Context, req *helixv1.GetJobStatusRequest) (*helixv1.JobStatus, error) {
	s.mu.RLock()
	nodeID, ok := s.jobNodes[req.JobId]
	s.mu.RUnlock()

	if !ok {
		return nil, status.Errorf(codes.NotFound, "job %s not found", req.JobId)
	}

	return &helixv1.JobStatus{
		JobId:  req.JobId,
		State:  string(scheduler.JobStatusScheduled),
		NodeId: nodeID,
	}, nil
}

// ListJobs returns jobs filtered by node ID and/or state.
func (s *Server) ListJobs(ctx context.Context, req *helixv1.ListJobsRequest) (*helixv1.ListJobsResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []*helixv1.JobStatus
	for jobID, nodeID := range s.jobNodes {
		if req.NodeId != "" && nodeID != req.NodeId {
			continue
		}
		state := string(scheduler.JobStatusScheduled)
		if req.State != "" && state != req.State {
			continue
		}
		out = append(out, &helixv1.JobStatus{
			JobId:  jobID,
			State:  state,
			NodeId: nodeID,
		})
	}

	return &helixv1.ListJobsResponse{Jobs: out}, nil
}

// StreamJobEvents streams a single mock event for the requested job.
func (s *Server) StreamJobEvents(req *helixv1.StreamJobEventsRequest, stream helixv1.SchedulerService_StreamJobEventsServer) error {
	s.mu.RLock()
	_, ok := s.jobNodes[req.JobId]
	s.mu.RUnlock()

	if !ok {
		return status.Errorf(codes.NotFound, "job %s not found", req.JobId)
	}

	return stream.Send(&helixv1.JobEvent{
		JobId:     req.JobId,
		EventType: "scheduled",
		Message:   "job scheduled",
		Timestamp: time.Now().Unix(),
	})
}

func instanceToNode(inst *discovery.Instance) *scheduler.Node {
	res := scheduler.Resources{}
	if inst.Metadata != nil {
		// Best-effort parsing of resource metadata.
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
	return &scheduler.Node{
		ID:                 inst.ID,
		AvailableResources: res,
		Labels:             inst.Metadata,
	}
}
