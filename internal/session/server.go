// Package session provides a gRPC server implementing the SessionService API.
package session

import (
	"context"
	"sync"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"github.com/HelixDevelopment/helix_cluster/pkg/session"
	"github.com/HelixDevelopment/helix_cluster/pkg/session/backends"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements helixv1.SessionServiceServer backed by pkg/session.Manager.
type Server struct {
	helixv1.UnimplementedSessionServiceServer

	mu      sync.RWMutex
	manager *session.Manager
}

// NewServer creates a new Session gRPC server with a native backend.
func NewServer() *Server {
	return &Server{
		manager: session.NewManager(nil),
	}
}

// NewServerWithBackend creates a new Session gRPC server with the provided backend.
func NewServerWithBackend(be backends.Backend) *Server {
	// The pkg/session Manager expects a TmuxBackend interface, but for the
	// native backend we wrap it so the gRPC server can operate without tmux.
	// For now we use NewManager(nil) which skips tmux operations and keeps
	// sessions in-memory. If a real backend is provided we still use the
	// manager's in-memory tracking and optionally invoke the backend.
	_ = be
	return NewServer()
}

// CreateSession creates a new session via the session manager.
func (s *Server) CreateSession(ctx context.Context, req *helixv1.CreateSessionRequest) (*helixv1.Session, error) {
	backend := session.BackendTmux
	if req.Backend != "" {
		backend = session.SessionBackend(req.Backend)
	}

	var cpuReq, memReq int64
	if req.Resources != nil {
		cpuReq = int64(req.Resources.CpuMillicores)
		memReq = req.Resources.MemoryBytes
	}

	sess, err := s.manager.Create(ctx, &session.CreateRequest{
		Name:          req.Name,
		Owner:         req.Owner,
		Backend:       backend,
		CPURequest:    cpuReq,
		MemoryRequest: memReq,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create session: %v", err)
	}

	return sessionToProto(sess), nil
}

// GetSession retrieves a session by ID.
func (s *Server) GetSession(ctx context.Context, req *helixv1.GetSessionRequest) (*helixv1.Session, error) {
	sess, err := s.manager.Get(session.SessionID(req.SessionId))
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "session %s not found", req.SessionId)
	}
	return sessionToProto(sess), nil
}

// ListSessions returns sessions filtered by owner and/or status.
func (s *Server) ListSessions(ctx context.Context, req *helixv1.ListSessionsRequest) (*helixv1.ListSessionsResponse, error) {
	var sessions []*session.Session
	if req.Owner != "" {
		sessions = s.manager.ListByOwner(req.Owner)
	} else {
		sessions = s.manager.List()
	}

	var out []*helixv1.Session
	for _, sess := range sessions {
		if req.Status != "" && sess.Status.String() != req.Status {
			continue
		}
		out = append(out, sessionToProto(sess))
	}

	return &helixv1.ListSessionsResponse{Sessions: out}, nil
}

// UpdateSession updates fields of an existing session.
func (s *Server) UpdateSession(ctx context.Context, req *helixv1.UpdateSessionRequest) (*helixv1.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, err := s.manager.Get(session.SessionID(req.SessionId))
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "session %s not found", req.SessionId)
	}

	sess, err = s.manager.Update(session.SessionID(req.SessionId), func(s *session.Session) {
		if req.Status != "" {
			s.Status = session.StatusFromString(req.Status)
		}
		if req.Resources != nil {
			s.CPURequest = int64(req.Resources.CpuMillicores)
			s.MemoryRequest = req.Resources.MemoryBytes
		}
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "update session: %v", err)
	}
	return sessionToProto(sess), nil
}

// DeleteSession removes a session via the manager.
func (s *Server) DeleteSession(ctx context.Context, req *helixv1.DeleteSessionRequest) (*helixv1.DeleteSessionResponse, error) {
	if err := s.manager.Terminate(ctx, session.SessionID(req.SessionId)); err != nil {
		return nil, status.Errorf(codes.NotFound, "session %s not found", req.SessionId)
	}
	return &helixv1.DeleteSessionResponse{Success: true}, nil
}

func sessionToProto(sess *session.Session) *helixv1.Session {
	if sess == nil {
		return nil
	}
	return &helixv1.Session{
		Id:     string(sess.ID),
		Name:   sess.Name,
		Owner:  sess.Owner,
		Status: sess.Status.String(),
		Backend: string(sess.Backend),
		NodeId: sess.NodeID,
		Resources: &helixv1.ResourceAllocation{
			CpuMillicores: int32(sess.CPURequest),
			MemoryBytes:   sess.MemoryRequest,
		},
	}
}

// RegisterNode exposes a helper to register a node with the session manager
// for migration planning. This is not part of the gRPC interface but is
// useful for wiring.
func (s *Server) RegisterNode(nodeID string) {
	_ = nodeID
	// No-op until the manager exposes node registration.
}

// Ensure the Server satisfies the interface.
var _ helixv1.SessionServiceServer = (*Server)(nil)
