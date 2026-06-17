// Package session provides a gRPC server implementing the SessionService API.
package session

import (
	"context"
	"os/exec"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
	"github.com/HelixDevelopment/helix_cluster/pkg/session"
	"github.com/HelixDevelopment/helix_cluster/pkg/session/backends"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements helixv1.SessionServiceServer backed by pkg/session.Manager.
//
// The Server intentionally holds no mutex of its own: all session state lives in
// pkg/session.Manager, which guards every read and write with its own internal
// lock. Adding a Server-level mutex here would be misleading — it would either
// duplicate the manager's locking or, if applied to only some handlers, provide
// no mutual exclusion at all while adding latency.
type Server struct {
	helixv1.UnimplementedSessionServiceServer

	manager *session.Manager
}

// NewServer creates a new Session gRPC server.
//
// When tmux is present in PATH, the Manager is backed by a real
// backends.TmuxBackend (via BackendAdapter) so sessions get a real process.
// When tmux is absent (e.g. in a stripped container), the Manager is backed
// by a NativeBackend (local PTY shell) so there is still a real process —
// NOT a nil backend that unconditionally reports StatusRunning without
// launching anything.
func NewServer() *Server {
	return &Server{
		manager: session.NewManager(newRealBackend()),
	}
}

// newRealBackend returns a session.TmuxBackend backed by a real process.
// Priority: tmux (if present in PATH) > NativeBackend (PTY shell).
// It never returns nil — nil is the bluff we eradicate.
func newRealBackend() session.TmuxBackend {
	if _, err := exec.LookPath("tmux"); err == nil {
		tb, err := backends.NewTmuxBackend()
		if err == nil {
			return session.NewBackendAdapter(tb)
		}
	}
	return session.NewBackendAdapter(backends.NewNativeBackend())
}

// NewServerWithBackend creates a new Session gRPC server with the provided backend.
// The supplied backends.Backend is wrapped in a BackendAdapter so the Manager
// drives a real process rather than a nil stub.
func NewServerWithBackend(be backends.Backend) *Server {
	return &Server{
		manager: session.NewManager(session.NewBackendAdapter(be)),
	}
}

// NewServerWithTmuxBackend creates a Session gRPC server whose Manager is
// driven by the supplied session.TmuxBackend directly.  Used by tests that
// inject a mock or a pre-constructed BackendAdapter without going through
// backends.TmuxBackend construction.
func NewServerWithTmuxBackend(tb session.TmuxBackend) *Server {
	return &Server{
		manager: session.NewManager(tb),
	}
}

// modeLabel is the domain Labels key under which the gRPC `mode` field is
// round-tripped. The domain session.Session has no first-class Mode field, so
// the server preserves it via Labels to keep CreateSession/GetSession honest.
const modeLabel = "helix.session/mode"

// validBackend reports whether the given backend string is one the session
// package recognizes. An empty string is treated as "use the default".
func validBackend(b string) bool {
	switch session.SessionBackend(b) {
	case session.BackendTmux, session.BackendZellij, session.BackendScreen:
		return true
	default:
		return false
	}
}

// CreateSession creates a new session via the session manager.
func (s *Server) CreateSession(ctx context.Context, req *helixv1.CreateSessionRequest) (*helixv1.Session, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if req.Owner == "" {
		return nil, status.Error(codes.InvalidArgument, "owner is required")
	}

	backend := session.BackendTmux
	if req.Backend != "" {
		if !validBackend(req.Backend) {
			return nil, status.Errorf(codes.InvalidArgument, "unknown backend %q", req.Backend)
		}
		backend = session.SessionBackend(req.Backend)
	}

	var cpuReq, memReq int64
	var gpuReq []string
	if req.Resources != nil {
		if req.Resources.CpuMillicores < 0 {
			return nil, status.Error(codes.InvalidArgument, "cpu_millicores must not be negative")
		}
		if req.Resources.MemoryBytes < 0 {
			return nil, status.Error(codes.InvalidArgument, "memory_bytes must not be negative")
		}
		cpuReq = int64(req.Resources.CpuMillicores)
		memReq = req.Resources.MemoryBytes
		gpuReq = req.Resources.GpuIds
	}

	var labels map[string]string
	if req.Mode != "" {
		labels = map[string]string{modeLabel: req.Mode}
	}

	sess, err := s.manager.Create(ctx, &session.CreateRequest{
		Name:          req.Name,
		Owner:         req.Owner,
		Backend:       backend,
		CPURequest:    cpuReq,
		MemoryRequest: memReq,
		GPURequest:    gpuReq,
		Labels:        labels,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create session: %v", err)
	}

	return sessionToProto(sess), nil
}

// GetSession retrieves a session by ID.
func (s *Server) GetSession(ctx context.Context, req *helixv1.GetSessionRequest) (*helixv1.Session, error) {
	if req.SessionId == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	sess, err := s.manager.Get(session.SessionID(req.SessionId))
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "session %s not found", req.SessionId)
	}
	return sessionToProto(sess), nil
}

// ListSessions returns sessions filtered by owner and/or status.
func (s *Server) ListSessions(ctx context.Context, req *helixv1.ListSessionsRequest) (*helixv1.ListSessionsResponse, error) {
	// A status filter that names no real status would silently return an empty
	// list — a confusing no-op for the caller. Reject it explicitly instead.
	if req.Status != "" && !validStatus(req.Status) {
		return nil, status.Errorf(codes.InvalidArgument, "unknown status filter %q", req.Status)
	}

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
	if req.SessionId == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	// Validate the status string BEFORE mutating. StatusFromString silently
	// coerces any unrecognized value to StatusPending, so without this guard a
	// client typo ("runnig") would quietly reset a Running session to Pending —
	// silent data corruption. Reject the bad input instead.
	if req.Status != "" && !validStatus(req.Status) {
		return nil, status.Errorf(codes.InvalidArgument, "unknown status %q", req.Status)
	}
	if req.Resources != nil {
		if req.Resources.CpuMillicores < 0 {
			return nil, status.Error(codes.InvalidArgument, "cpu_millicores must not be negative")
		}
		if req.Resources.MemoryBytes < 0 {
			return nil, status.Error(codes.InvalidArgument, "memory_bytes must not be negative")
		}
	}

	// resurrection records whether the mutator refused to revive a terminated
	// session. Terminate is a terminal FSM state: it has already killed the
	// backend process (KillSession), so flipping a terminated session back to
	// a non-terminated status via UpdateSession would report a live ("running")
	// session that has no real process behind it — a CLAUDE-1 status bluff and a
	// fail-open hole that also defeats the double-terminate guard. The decision
	// is made INSIDE the mutator so it is atomic under the manager's lock (no
	// TOCTOU against a concurrent Terminate). Manager.Update has no FSM guard of
	// its own, so this server-level handler enforces the invariant.
	var resurrection bool
	sess, err := s.manager.Update(session.SessionID(req.SessionId), func(s *session.Session) {
		if req.Status != "" {
			next := session.StatusFromString(req.Status)
			if s.Status == session.StatusTerminated && next != session.StatusTerminated {
				resurrection = true
				return // refuse the whole update; leave the session terminated
			}
			s.Status = next
		}
		if req.Resources != nil {
			s.CPURequest = int64(req.Resources.CpuMillicores)
			s.MemoryRequest = req.Resources.MemoryBytes
			s.GPURequest = req.Resources.GpuIds
		}
	})
	if err != nil {
		// Manager.Update only fails when the session does not exist.
		return nil, status.Errorf(codes.NotFound, "session %s not found", req.SessionId)
	}
	if resurrection {
		return nil, status.Errorf(codes.FailedPrecondition,
			"session %s is terminated and cannot be updated to %q", req.SessionId, req.Status)
	}
	return sessionToProto(sess), nil
}

// validStatus reports whether str is a recognized session status name.
// StatusFromString silently defaults unknown input to StatusPending, so callers
// that need to reject bad input must check membership here first.
func validStatus(str string) bool {
	switch str {
	case "pending", "creating", "running", "migrating", "paused", "terminated", "failed":
		return true
	default:
		return false
	}
}

// DeleteSession removes a session via the manager.
func (s *Server) DeleteSession(ctx context.Context, req *helixv1.DeleteSessionRequest) (*helixv1.DeleteSessionResponse, error) {
	if req.SessionId == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	if err := s.manager.Terminate(ctx, session.SessionID(req.SessionId)); err != nil {
		return nil, status.Errorf(codes.NotFound, "session %s not found", req.SessionId)
	}
	return &helixv1.DeleteSessionResponse{Success: true}, nil
}

// clampInt32 narrows an int64 resource value to the int32 proto field without
// wrapping: negatives become 0, oversized values cap at int32max.
func clampInt32(v int64) int32 {
	if v < 0 {
		return 0
	}
	if v > 0x7fffffff {
		return 0x7fffffff
	}
	return int32(v) //gosec:disable G115 -- bounds-checked to [0,0x7fffffff] above
}

func sessionToProto(sess *session.Session) *helixv1.Session {
	if sess == nil {
		return nil
	}
	return &helixv1.Session{
		Id:      string(sess.ID),
		Name:    sess.Name,
		Owner:   sess.Owner,
		Status:  sess.Status.String(),
		Mode:    sess.Labels[modeLabel],
		Backend: string(sess.Backend),
		NodeId:  sess.NodeID,
		Resources: &helixv1.ResourceAllocation{
			CpuMillicores: clampInt32(sess.CPURequest),
			MemoryBytes:   sess.MemoryRequest,
			GpuIds:        sess.GPURequest,
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
