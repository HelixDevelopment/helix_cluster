package session

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"fmt"
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
		// NOTE: a shallow copy(cs.Windows, s.Windows) copies the Window structs
		// but leaves their nested reference fields (Window.CRDTState, each
		// Pane.Environment map, Pane.CRDTState) ALIASED to the stored session. A
		// caller mutating a returned window/pane would then corrupt the store for
		// every other holder under the lock. Deep-copy every nested container.
		cs.Windows = make([]Window, len(s.Windows))
		for i := range s.Windows {
			cs.Windows[i] = copyWindow(s.Windows[i])
		}
	}
	return cs
}

// copyWindow returns a deep copy of a Window, including its nested Panes (and
// each pane's Environment map and CRDTState bytes) and the window's own
// CRDTState bytes, so the result shares no mutable memory with the input.
func copyWindow(w Window) Window {
	cw := Window{
		ID:     w.ID,
		Name:   w.Name,
		Layout: w.Layout,
		Active: w.Active,
	}
	if w.CRDTState != nil {
		cw.CRDTState = make([]byte, len(w.CRDTState))
		copy(cw.CRDTState, w.CRDTState)
	}
	if len(w.Panes) > 0 {
		cw.Panes = make([]Pane, len(w.Panes))
		for i := range w.Panes {
			cw.Panes[i] = copyPane(w.Panes[i])
		}
	}
	return cw
}

// copyPane returns a deep copy of a Pane, including its Environment map and
// CRDTState bytes.
func copyPane(p Pane) Pane {
	cp := p // scalar fields copied by value
	if p.Environment != nil {
		cp.Environment = make(map[string]string, len(p.Environment))
		for k, v := range p.Environment {
			cp.Environment[k] = v
		}
	}
	if p.CRDTState != nil {
		cp.CRDTState = make([]byte, len(p.CRDTState))
		copy(cp.CRDTState, p.CRDTState)
	}
	return cp
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

	// Session IDs gate Attach/Detach access, so the random component MUST be
	// unpredictable. Use crypto/rand (16 random bytes) instead of math/rand.
	var randBytes [16]byte
	if _, err := cryptorand.Read(randBytes[:]); err != nil {
		return nil, fmt.Errorf("session: generate id: %w", err)
	}
	id := SessionID(fmt.Sprintf("session-%d-%s", time.Now().UnixNano(), hex.EncodeToString(randBytes[:])))

	// Defensively copy the request's reference fields (Environment, Labels,
	// GPURequest) into the stored session. Storing the caller's maps/slice by
	// reference would alias the store: a caller mutating its CreateRequest maps
	// after Create returns would silently corrupt the stored session for every
	// other holder, with the mutex protecting only the container, not the pointee.
	env := make(map[string]string, len(req.Environment))
	for k, v := range req.Environment {
		env[k] = v
	}
	labels := make(map[string]string, len(req.Labels))
	for k, v := range req.Labels {
		labels[k] = v
	}
	var gpu []string
	if len(req.GPURequest) > 0 {
		gpu = make([]string, len(req.GPURequest))
		copy(gpu, req.GPURequest)
	}

	s := &Session{
		ID:            id,
		Name:          req.Name,
		Owner:         req.Owner,
		Status:        StatusCreating,
		Backend:       req.Backend,
		CPURequest:    req.CPURequest,
		MemoryRequest: req.MemoryRequest,
		GPURequest:    gpu,
		Environment:   env,
		Labels:        labels,
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

// Reap removes every session whose Status is StatusTerminated from the store and
// returns the number of entries removed.
//
// MOTIVATION (resource-leak fix): Terminate intentionally LEAVES the terminated
// entry in the map (so Get/List still observe it and a double-terminate is
// rejected). For a long-lived Manager — e.g. the internal/session gRPC server —
// terminated sessions would otherwise accumulate forever, an unbounded map-growth
// leak. Reap is the explicit, operator-approved opt-in that releases that memory.
//
// CONTRACT: Reaping is NEVER performed implicitly inside Terminate; it happens
// ONLY when a caller invokes Reap (or ReapOlderThan). This keeps Terminate's
// documented behavior — and every existing caller/test — unchanged. Active
// (non-terminated) sessions are always retained.
//
// Reap removes ALL terminated sessions regardless of age. Callers that want to
// retain recently terminated sessions for a grace window should use
// ReapOlderThan instead.
func (m *Manager) Reap() int {
	return m.ReapOlderThan(0)
}

// ReapOlderThan removes terminated sessions whose last update (UpdatedAt, which
// Terminate stamps at termination time) is at least d in the past, and returns
// the number removed. A non-positive d reaps every terminated session
// immediately (identical to Reap). Active sessions are never removed.
//
// This is the grace-window variant of Reap: it lets an operator retain freshly
// terminated sessions (e.g. for status queries or audit) while still bounding
// map growth by evicting older tombstones.
func (m *Manager) ReapOlderThan(d time.Duration) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	var cutoff time.Time
	if d > 0 {
		cutoff = time.Now().Add(-d)
	}

	removed := 0
	for id, s := range m.sessions {
		if s.Status != StatusTerminated {
			continue
		}
		if d > 0 && s.UpdatedAt.After(cutoff) {
			continue
		}
		delete(m.sessions, id)
		removed++
	}
	return removed
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
