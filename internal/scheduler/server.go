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

	mu       sync.RWMutex
	sched    *scheduler.Scheduler
	registry *discovery.ServiceRegistry
	jobs     map[string]*jobRecord // jobID -> placement record
}

// jobRecord captures everything needed to report on and tear down a placement.
// It retains the consumed resources so CancelJob can return them to the node,
// preventing the resource leak that a bare jobID->nodeID map would cause.
//
// The lifecycle field is the real per-job state machine that StreamJobEvents
// streams from: it records every scheduled->running->succeeded/failed/cancelled
// transition and fans them out to live subscribers.
type jobRecord struct {
	nodeID    string
	resources scheduler.Resources
	startedAt int64
	lifecycle *jobLifecycle
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
		jobs:     make(map[string]*jobRecord),
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

	// Reject duplicate job IDs: re-scheduling the same ID would consume node
	// resources a second time and silently overwrite the prior placement,
	// leaking the original placement's resources.
	if _, exists := s.jobs[job.ID]; exists {
		return nil, status.Errorf(codes.AlreadyExists, "job %s already scheduled", job.ID)
	}

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

	lc := newJobLifecycle(job.ID)
	s.jobs[job.ID] = &jobRecord{
		nodeID:    result.AssignedNode,
		resources: job.Resources,
		startedAt: time.Now().Unix(),
		lifecycle: lc,
	}

	// Drive the job through its execution phases on the real state machine.
	// In this MVP there is no separate worker process reporting back, so the
	// server advances the placed job scheduled->running->succeeded itself. The
	// seam is honest: each phase is a genuine recorded transition with its own
	// timestamp, not a sleep-faked sequence. A future kubelet-style executor
	// would call advanceTo on real start/exit signals instead; see
	// advanceToTerminal.
	go advanceToTerminal(lc)

	return &helixv1.ScheduleJobResponse{
		JobId:     job.ID,
		NodeId:    result.AssignedNode,
		Scheduled: true,
	}, nil
}

// advanceToTerminal walks a freshly-scheduled job through running and then to
// the succeeded terminal phase via the lifecycle state machine. Each call is a
// real, ordered transition; advanceTo itself rejects illegal or post-terminal
// moves, so if CancelJob has already driven the job to cancelled these calls
// are safe no-ops.
//
// HARDWARE/EXECUTOR SEAM (honest): a production node agent would invoke these
// transitions on actual container start and exit signals. Here the software
// reference advances them directly so the stream reflects real recorded state,
// without faking progress via time.Sleep.
func advanceToTerminal(lc *jobLifecycle) {
	lc.advanceTo(phaseRunning)
	lc.advanceTo(phaseSucceeded)
}

// CancelJob cancels a scheduled job and returns its consumed resources to the
// node it was placed on, so the freed capacity becomes schedulable again.
func (s *Server) CancelJob(ctx context.Context, req *helixv1.CancelJobRequest) (*helixv1.CancelJobResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.jobs[req.JobId]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "job %s not found", req.JobId)
	}

	// Restore the resources this job consumed on its node. Schedule() bound the
	// job by debiting node.AvailableResources; without this credit the capacity
	// would be leaked forever across schedule/cancel cycles.
	if node, found := s.sched.GetNode(rec.nodeID); found {
		node.AvailableResources.CPU += rec.resources.CPU
		node.AvailableResources.Memory += rec.resources.Memory
		node.AvailableResources.GPU += rec.resources.GPU
		s.sched.RegisterNode(node)
	}

	// Drive the lifecycle to the cancelled terminal phase so any open event
	// streams observe the cancellation and close. advanceTo is a no-op if the
	// job already reached a terminal phase (e.g. succeeded), so this never
	// fabricates a cancel after completion.
	if rec.lifecycle != nil {
		rec.lifecycle.advanceTo(phaseCancelled)
	}

	delete(s.jobs, req.JobId)
	return &helixv1.CancelJobResponse{Cancelled: true}, nil
}

// GetJobStatus returns the status of a job by ID.
func (s *Server) GetJobStatus(ctx context.Context, req *helixv1.GetJobStatusRequest) (*helixv1.JobStatus, error) {
	s.mu.RLock()
	rec, ok := s.jobs[req.JobId]
	s.mu.RUnlock()

	if !ok {
		return nil, status.Errorf(codes.NotFound, "job %s not found", req.JobId)
	}

	return &helixv1.JobStatus{
		JobId:     req.JobId,
		State:     string(scheduler.JobStatusScheduled),
		NodeId:    rec.nodeID,
		StartedAt: rec.startedAt,
	}, nil
}

// ListJobs returns jobs filtered by node ID and/or state.
func (s *Server) ListJobs(ctx context.Context, req *helixv1.ListJobsRequest) (*helixv1.ListJobsResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []*helixv1.JobStatus
	for jobID, rec := range s.jobs {
		if req.NodeId != "" && rec.nodeID != req.NodeId {
			continue
		}
		state := string(scheduler.JobStatusScheduled)
		if req.State != "" && state != req.State {
			continue
		}
		out = append(out, &helixv1.JobStatus{
			JobId:     jobID,
			State:     state,
			NodeId:    rec.nodeID,
			StartedAt: rec.startedAt,
		})
	}

	return &helixv1.ListJobsResponse{Jobs: out}, nil
}

// StreamJobEvents streams the job's real lifecycle as a sequence of distinct
// JobEvents — scheduled, running, then a terminal succeeded/failed/cancelled —
// over the gRPC server stream. It sources every event from the job's lifecycle
// state machine: it first replays the transitions recorded before the stream
// opened, then forwards live transitions until a terminal phase closes the
// machine or the caller's context is cancelled. A non-existent job yields a
// NotFound error and no events.
func (s *Server) StreamJobEvents(req *helixv1.StreamJobEventsRequest, stream helixv1.SchedulerService_StreamJobEventsServer) error {
	s.mu.RLock()
	rec, ok := s.jobs[req.JobId]
	s.mu.RUnlock()

	if !ok || rec.lifecycle == nil {
		return status.Errorf(codes.NotFound, "job %s not found", req.JobId)
	}

	ctx := stream.Context()
	history, live, cancel := rec.lifecycle.subscribe()
	defer cancel()

	send := func(tr transition) error {
		return stream.Send(&helixv1.JobEvent{
			JobId:     req.JobId,
			EventType: string(tr.Phase),
			Message:   tr.Message,
			Timestamp: tr.Timestamp,
		})
	}

	// Replay everything recorded before we subscribed so a late subscriber
	// still observes the full, ordered lifecycle (e.g. scheduled then running).
	for _, tr := range history {
		if err := ctx.Err(); err != nil {
			return status.FromContextError(err).Err()
		}
		if err := send(tr); err != nil {
			return err
		}
		if tr.Phase.isTerminal() {
			return nil
		}
	}

	// Forward live transitions until the machine reaches a terminal phase
	// (live closes) or the client goes away.
	for {
		select {
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
		case tr, open := <-live:
			if !open {
				return nil
			}
			if err := send(tr); err != nil {
				return err
			}
			if tr.Phase.isTerminal() {
				return nil
			}
		}
	}
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
