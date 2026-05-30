// Package session provides distributed terminal session lifecycle,
// CRDT-based state synchronization, and cross-node migration.
package session

import (
	"time"
)

// SessionID is a unique session identifier.
type SessionID string

// Session represents a distributed terminal session.
type Session struct {
	ID        SessionID
	Owner     string
	Status    SessionStatus
	Backend   SessionBackend
	NodeID    string
	CreatedAt time.Time
	UpdatedAt time.Time

	// Resources
	CPURequest    int64    // millicores
	MemoryRequest int64    // bytes
	GPURequest    []string // GPU device IDs

	// State
	Windows     []Window
	Environment map[string]string
	Labels      map[string]string
}

// SessionStatus represents the lifecycle state.
type SessionStatus int

const (
	StatusPending SessionStatus = iota
	StatusCreating
	StatusRunning
	StatusMigrating
	StatusPaused
	StatusTerminated
	StatusFailed
)

// SessionBackend identifies the terminal backend.
type SessionBackend string

const (
	BackendTmux   SessionBackend = "tmux"
	BackendZellij SessionBackend = "zellij"
	BackendScreen SessionBackend = "screen"
)

// Window represents a tmux window within a session.
type Window struct {
	ID        string
	Name      string
	Layout    string // tmux layout string
	Active    bool
	Panes     []Pane
	CRDTState []byte // serialized CRDT state
}

// Pane represents a tmux pane within a window.
type Pane struct {
	ID          string
	Command     string
	WorkingDir  string
	Environment map[string]string
	PTYPath     string
	CPULimit    int64 // millicores
	MemoryLimit int64 // bytes
	GPUDevice   string
	Status      PaneStatus
	CRDTState   []byte
}

// PaneStatus represents the lifecycle state of a pane.
type PaneStatus int

const (
	PaneRunning PaneStatus = iota
	PaneExited
	PaneFrozen
)

// ResourceUsage tracks real-time resource consumption.
type ResourceUsage struct {
	CPUMillis      int64
	MemoryBytes    int64
	GPUMemoryBytes map[string]int64 // per GPU
	NetworkRx      int64
	NetworkTx      int64
}
