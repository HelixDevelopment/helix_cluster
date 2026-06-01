package session

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"
)

// Manager handles session lifecycle operations.
type Manager struct {
	mu       sync.RWMutex
	sessions map[SessionID]*Session

	// Backends
	tmuxBackend TmuxBackend

	// Event handlers
	onCreate    func(*Session)
	onMigrate   func(*Session, string, string) // session, fromNode, toNode
	onTerminate func(SessionID)
}

// TmuxBackend abstracts tmux operations.
type TmuxBackend interface {
	CreateSession(name string, env map[string]string) (string, error)
	AttachSession(name string) error
	DetachSession(name string) error
	KillSession(name string) error
	ListSessions() ([]string, error)
	// SessionExists reports whether a session with the given name is alive in
	// the backend. It is the sink-side probe used by tests and health checks to
	// prove a real process was launched (not merely StatusRunning in memory).
	SessionExists(name string) (bool, error)
	CreateWindow(session, name string) (string, error)
	SplitWindow(session, window string) (string, error)
	ResizePane(session, pane string, rows, cols int) error
	SendKeys(session, pane string, keys string) error
	CapturePane(session, pane string) (string, error)
	GetSessionState(session string) ([]byte, error)
	RestoreSessionState(session string, state []byte) error
}

// NewManager creates a session manager.
func NewManager(tmux TmuxBackend) *Manager {
	return &Manager{
		sessions:    make(map[SessionID]*Session),
		tmuxBackend: tmux,
	}
}

// copySession returns a deep copy of a Session.
func copySession(s *Session) *Session {
	if s == nil {
		return nil
	}
	cs := &Session{
		ID:            s.ID,
		Name:          s.Name,
		Owner:         s.Owner,
		Status:        s.Status,
		Backend:       s.Backend,
		NodeID:        s.NodeID,
		CreatedAt:     s.CreatedAt,
		UpdatedAt:     s.UpdatedAt,
		CPURequest:    s.CPURequest,
		MemoryRequest: s.MemoryRequest,
		Environment:   make(map[string]string, len(s.Environment)),
		Labels:        make(map[string]string, len(s.Labels)),
	}
	for k, v := range s.Environment {
		cs.Environment[k] = v
	}
	for k, v := range s.Labels {
		cs.Labels[k] = v
	}
	if len(s.GPURequest) > 0 {
		cs.GPURequest = make([]string, len(s.GPURequest))
		copy(cs.GPURequest, s.GPURequest)
	}
	if len(s.Windows) > 0 {
		cs.Windows = make([]Window, len(s.Windows))
		copy(cs.Windows, s.Windows)
	}
	return cs
}

// CreateRequest holds parameters for session creation.
type CreateRequest struct {
	Name          string
	Owner         string
	Backend       SessionBackend
	CPURequest    int64
	MemoryRequest int64
	GPURequest    []string
	Environment   map[string]string
	Labels        map[string]string
}

// Create creates a new session.
func (m *Manager) Create(ctx context.Context, req *CreateRequest) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := SessionID(fmt.Sprintf("session-%d-%d", time.Now().UnixNano(), rand.Int()))

	s := &Session{
		ID:            id,
		Name:          req.Name,
		Owner:         req.Owner,
		Status:        StatusCreating,
		Backend:       req.Backend,
		CPURequest:    req.CPURequest,
		MemoryRequest: req.MemoryRequest,
		GPURequest:    req.GPURequest,
		Environment:   req.Environment,
		Labels:        req.Labels,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	if m.tmuxBackend != nil {
		tmuxName := string(id)
		_, err := m.tmuxBackend.CreateSession(tmuxName, req.Environment)
		if err != nil {
			s.Status = StatusFailed
			m.sessions[id] = s
			return s, fmt.Errorf("tmux create session: %w", err)
		}
	}

	s.Status = StatusRunning
	m.sessions[id] = s

	if m.onCreate != nil {
		m.onCreate(s)
	}

	return copySession(s), nil
}

// Get returns a session by ID.
func (m *Manager) Get(id SessionID) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	s, ok := m.sessions[id]
	if !ok {
		return nil, fmt.Errorf("session %q not found", id)
	}
	return copySession(s), nil
}

// List returns all sessions.
func (m *Manager) List() []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, copySession(s))
	}
	return out
}

// ListByOwner returns sessions for a specific owner.
func (m *Manager) ListByOwner(owner string) []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []*Session
	for _, s := range m.sessions {
		if s.Owner == owner {
			out = append(out, copySession(s))
		}
	}
	return out
}

// ListByNode returns sessions on a specific node.
func (m *Manager) ListByNode(nodeID string) []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []*Session
	for _, s := range m.sessions {
		if s.NodeID == nodeID {
			out = append(out, copySession(s))
		}
	}
	return out
}

// Attach attaches to a running session.
func (m *Manager) Attach(ctx context.Context, id SessionID, user string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[id]
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}
	// FSM guard: attach is only legal from Running. Reject (not silently
	// succeed) any illegal transition.
	if err := validateTransition(s.Status, actionAttach); err != nil {
		return err
	}

	if m.tmuxBackend != nil {
		if err := m.tmuxBackend.AttachSession(string(id)); err != nil {
			return fmt.Errorf("tmux attach: %w", err)
		}
	}

	s.UpdatedAt = time.Now()
	return nil
}

// Detach detaches from a session.
func (m *Manager) Detach(ctx context.Context, id SessionID, user string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[id]
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}
	// FSM guard: detach is legal only from Running or Paused.
	if err := validateTransition(s.Status, actionDetach); err != nil {
		return err
	}

	if m.tmuxBackend != nil {
		if err := m.tmuxBackend.DetachSession(string(id)); err != nil {
			return fmt.Errorf("tmux detach: %w", err)
		}
	}

	s.UpdatedAt = time.Now()
	return nil
}

// Terminate gracefully terminates a session.
func (m *Manager) Terminate(ctx context.Context, id SessionID) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[id]
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}
	// FSM guard: terminate is legal from any non-terminal state but NOT from
	// Terminated (double-terminate must be rejected, not silently succeed).
	if err := validateTransition(s.Status, actionTerminate); err != nil {
		return err
	}

	if m.tmuxBackend != nil {
		if err := m.tmuxBackend.KillSession(string(id)); err != nil {
			return fmt.Errorf("tmux kill: %w", err)
		}
	}

	s.Status = StatusTerminated
	s.UpdatedAt = time.Now()

	if m.onTerminate != nil {
		m.onTerminate(id)
	}

	return nil
}

// Migrate initiates session migration to another node.
func (m *Manager) Migrate(ctx context.Context, id SessionID, targetNode string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[id]
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}
	// FSM guard: migrate is legal only from Running. A session that is already
	// Migrating, Terminated, Creating, Paused or Failed must be rejected WITHOUT
	// mutating its status or NodeID.
	if err := validateTransition(s.Status, actionMigrate); err != nil {
		return err
	}

	fromNode := s.NodeID
	s.Status = StatusMigrating
	s.NodeID = targetNode
	s.UpdatedAt = time.Now()

	if m.onMigrate != nil {
		m.onMigrate(s, fromNode, targetNode)
	}

	return nil
}

// ResizePTY resizes the PTY for a pane.
func (m *Manager) ResizePTY(ctx context.Context, id SessionID, paneID string, rows, cols int) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	s, ok := m.sessions[id]
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}
	if s.Status != StatusRunning {
		return fmt.Errorf("session %q is not running", id)
	}

	if m.tmuxBackend != nil {
		if err := m.tmuxBackend.ResizePane(string(id), paneID, rows, cols); err != nil {
			return fmt.Errorf("tmux resize: %w", err)
		}
	}

	return nil
}

// SendInput sends input to a pane.
func (m *Manager) SendInput(ctx context.Context, id SessionID, paneID string, input string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	s, ok := m.sessions[id]
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}
	if s.Status != StatusRunning {
		return fmt.Errorf("session %q is not running", id)
	}

	if m.tmuxBackend != nil {
		if err := m.tmuxBackend.SendKeys(string(id), paneID, input); err != nil {
			return fmt.Errorf("tmux send keys: %w", err)
		}
	}

	return nil
}

// Update modifies an existing session's mutable fields.
func (m *Manager) Update(id SessionID, fn func(*Session)) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[id]
	if !ok {
		return nil, fmt.Errorf("session %q not found", id)
	}

	fn(s)
	s.UpdatedAt = time.Now()
	return copySession(s), nil
}

// GetResourceUsage returns current resource usage for a session.
func (m *Manager) GetResourceUsage(id SessionID) (*ResourceUsage, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	s, ok := m.sessions[id]
	if !ok {
		return nil, fmt.Errorf("session %q not found", id)
	}

	// Placeholder: real implementation would query cgroups / metrics.
	_ = s
	return &ResourceUsage{
		CPUMillis:      0,
		MemoryBytes:    0,
		GPUMemoryBytes: make(map[string]int64),
		NetworkRx:      0,
		NetworkTx:      0,
	}, nil
}
