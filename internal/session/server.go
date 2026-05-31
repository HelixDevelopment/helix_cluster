// Package session provides a gRPC server implementing the SessionService API.
package session

import (
	"context"
	"fmt"
	"sync"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements helixv1.SessionServiceServer with in-memory state.
type Server struct {
	helixv1.UnimplementedSessionServiceServer

	mu       sync.RWMutex
	sessions map[string]*helixv1.Session
}

// NewServer creates a new Session gRPC server.
func NewServer() *Server {
	return &Server{
		sessions: make(map[string]*helixv1.Session),
	}
}

// CreateSession creates a new session and stores it in memory.
func (s *Server) CreateSession(ctx context.Context, req *helixv1.CreateSessionRequest) (*helixv1.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess := &helixv1.Session{
		Id:        fmt.Sprintf("session-%d", time.Now().UnixNano()),
		Name:      req.Name,
		Owner:     req.Owner,
		Status:    "active",
		Mode:      req.Mode,
		Backend:   req.Backend,
		Resources: req.Resources,
	}

	s.sessions[sess.Id] = sess
	return sess, nil
}

// GetSession retrieves a session by ID.
func (s *Server) GetSession(ctx context.Context, req *helixv1.GetSessionRequest) (*helixv1.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sess, ok := s.sessions[req.SessionId]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "session %s not found", req.SessionId)
	}
	return sess, nil
}

// ListSessions returns sessions filtered by owner and/or status.
func (s *Server) ListSessions(ctx context.Context, req *helixv1.ListSessionsRequest) (*helixv1.ListSessionsResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []*helixv1.Session
	for _, sess := range s.sessions {
		if req.Owner != "" && sess.Owner != req.Owner {
			continue
		}
		if req.Status != "" && sess.Status != req.Status {
			continue
		}
		out = append(out, sess)
	}

	return &helixv1.ListSessionsResponse{Sessions: out}, nil
}

// UpdateSession updates fields of an existing session.
func (s *Server) UpdateSession(ctx context.Context, req *helixv1.UpdateSessionRequest) (*helixv1.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[req.SessionId]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "session %s not found", req.SessionId)
	}

	if req.Status != "" {
		sess.Status = req.Status
	}
	if req.Resources != nil {
		sess.Resources = req.Resources
	}

	return sess, nil
}

// DeleteSession removes a session from the in-memory store.
func (s *Server) DeleteSession(ctx context.Context, req *helixv1.DeleteSessionRequest) (*helixv1.DeleteSessionResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.sessions[req.SessionId]; !ok {
		return nil, status.Errorf(codes.NotFound, "session %s not found", req.SessionId)
	}

	delete(s.sessions, req.SessionId)
	return &helixv1.DeleteSessionResponse{Success: true}, nil
}
