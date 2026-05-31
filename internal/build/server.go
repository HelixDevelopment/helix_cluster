// Package build provides the build orchestration service for Helix Cluster OS.
package build

import (
	"context"
	"fmt"
	"sync"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"google.golang.org/grpc"
)

// Server implements the helix.v1.BuildService gRPC interface.
type Server struct {
	helixv1.UnimplementedBuildServiceServer
	orch      *Orchestrator
	pool      *WorkerPool
	streamMu  sync.RWMutex
	streams   map[string]chan *helixv1.BuildLogLine
}

// Ensure Server implements the interface at compile time.
var _ helixv1.BuildServiceServer = (*Server)(nil)

// NewServer creates a gRPC build service backed by an orchestrator and worker pool.
func NewServer(orch *Orchestrator, pool *WorkerPool) *Server {
	return &Server{
		orch:    orch,
		pool:    pool,
		streams: make(map[string]chan *helixv1.BuildLogLine),
	}
}

// SubmitBuild handles build submission requests.
func (s *Server) SubmitBuild(ctx context.Context, req *helixv1.SubmitBuildRequest) (*helixv1.SubmitBuildResponse, error) {
	spec := BuildSpec{
		ID:             generateID(),
		RepoURL:        req.GetRepoUrl(),
		Ref:            req.GetRef(),
		DockerfilePath: req.GetDockerfilePath(),
		BuildArgs:      req.GetBuildArgs(),
	}

	j, err := s.orch.SubmitBuild(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("submit build: %w", err)
	}

	return &helixv1.SubmitBuildResponse{
		BuildId: j.ID,
		Queued:  true,
	}, nil
}

// GetBuildStatus returns the current status of a build.
func (s *Server) GetBuildStatus(ctx context.Context, req *helixv1.GetBuildStatusRequest) (*helixv1.BuildStatus, error) {
	j, err := s.orch.GetBuildStatus(ctx, req.GetBuildId())
	if err != nil {
		return nil, fmt.Errorf("get build status: %w", err)
	}

	return &helixv1.BuildStatus{
		BuildId:     j.ID,
		State:       string(j.State),
		StartedAt:   timestamp(j.StartedAt),
		CompletedAt: timestamp(j.CompletedAt),
		ImageTag:    j.ImageTag,
	}, nil
}

// CancelBuild handles build cancellation requests.
func (s *Server) CancelBuild(ctx context.Context, req *helixv1.CancelBuildRequest) (*helixv1.CancelBuildResponse, error) {
	if err := s.orch.CancelBuild(ctx, req.GetBuildId()); err != nil {
		return nil, fmt.Errorf("cancel build: %w", err)
	}
	return &helixv1.CancelBuildResponse{Cancelled: true}, nil
}

// StreamBuildLogs streams build log lines to the client.
func (s *Server) StreamBuildLogs(req *helixv1.StreamBuildLogsRequest, stream helixv1.BuildService_StreamBuildLogsServer) error {
	buildID := req.GetBuildId()
	ctx := stream.Context()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var lastSent int

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		j, err := s.orch.GetBuildStatus(ctx, buildID)
		if err != nil {
			return err
		}

		logs := j.GetLogs()
		if len(logs) > lastSent {
			for i := lastSent; i < len(logs); i++ {
				if err := stream.Send(&helixv1.BuildLogLine{
					BuildId:   j.ID,
					Line:      logs[i],
					Timestamp: time.Now().Unix(),
				}); err != nil {
					return err
				}
			}
			lastSent = len(logs)
		}

		if j.IsTerminal() {
			return nil
		}
	}
}

// Register registers the server with a gRPC registrar.
func (s *Server) Register(registrar grpc.ServiceRegistrar) {
	helixv1.RegisterBuildServiceServer(registrar, s)
}

var idCounter int
var idMu sync.Mutex

func generateID() string {
	idMu.Lock()
	defer idMu.Unlock()
	idCounter++
	return fmt.Sprintf("build-%d-%d", time.Now().UnixNano(), idCounter)
}

func timestamp(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return t.Unix()
}
