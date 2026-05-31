// Package scheduler provides a gRPC server implementing the SchedulerService API.
package scheduler

import (
	"context"
	"fmt"
	"sync"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"github.com/HelixDevelopment/helix_cluster/pkg/scheduler"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements helixv1.SchedulerServiceServer with in-memory state.
type Server struct {
	helixv1.UnimplementedSchedulerServiceServer

	mu   sync.RWMutex
	jobs map[string]*scheduler.Job
}

// NewServer creates a new Scheduler gRPC server.
func NewServer() *Server {
	return &Server{
		jobs: make(map[string]*scheduler.Job),
	}
}

// ScheduleJob accepts a job request and stores it in memory.
func (s *Server) ScheduleJob(ctx context.Context, req *helixv1.ScheduleJobRequest) (*helixv1.ScheduleJobResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job := &scheduler.Job{
		ID:       req.JobId,
		Status:   scheduler.JobStatusScheduled,
		Labels:   req.Constraints,
		Priority: 0,
	}
	if job.ID == "" {
		job.ID = fmt.Sprintf("job-%d", time.Now().UnixNano())
	}

	s.jobs[job.ID] = job

	return &helixv1.ScheduleJobResponse{
		JobId:     job.ID,
		NodeId:    "",
		Scheduled: true,
	}, nil
}

// CancelJob removes a job from the in-memory store.
func (s *Server) CancelJob(ctx context.Context, req *helixv1.CancelJobRequest) (*helixv1.CancelJobResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.jobs[req.JobId]; !ok {
		return nil, status.Errorf(codes.NotFound, "job %s not found", req.JobId)
	}

	delete(s.jobs, req.JobId)
	return &helixv1.CancelJobResponse{Cancelled: true}, nil
}

// GetJobStatus returns the status of a job by ID.
func (s *Server) GetJobStatus(ctx context.Context, req *helixv1.GetJobStatusRequest) (*helixv1.JobStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	job, ok := s.jobs[req.JobId]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "job %s not found", req.JobId)
	}

	return &helixv1.JobStatus{
		JobId: job.ID,
		State: string(job.Status),
	}, nil
}

// ListJobs returns jobs filtered by node ID and/or state.
func (s *Server) ListJobs(ctx context.Context, req *helixv1.ListJobsRequest) (*helixv1.ListJobsResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []*helixv1.JobStatus
	for _, job := range s.jobs {
		if req.State != "" && string(job.Status) != req.State {
			continue
		}
		out = append(out, &helixv1.JobStatus{
			JobId: job.ID,
			State: string(job.Status),
		})
	}

	return &helixv1.ListJobsResponse{Jobs: out}, nil
}

// StreamJobEvents streams a single mock event for the requested job.
func (s *Server) StreamJobEvents(req *helixv1.StreamJobEventsRequest, stream helixv1.SchedulerService_StreamJobEventsServer) error {
	s.mu.RLock()
	_, ok := s.jobs[req.JobId]
	s.mu.RUnlock()

	if !ok {
		return status.Errorf(codes.NotFound, "job %s not found", req.JobId)
	}

	return stream.Send(&helixv1.JobEvent{
		JobId:     req.JobId,
		EventType: "mock",
		Message:   "mock event",
		Timestamp: time.Now().Unix(),
	})
}
