// Package backends provides terminal multiplexer backend implementations
// for the Helix Cluster session manager.
package backends

import "io"

// SessionInfo describes a running session for List queries.
type SessionInfo struct {
	ID      string
	Name    string
	Command string
	Active  bool
}

// Backend abstracts terminal multiplexer operations.
type Backend interface {
	// Create starts a new detached session and returns its unique ID.
	Create(name string, cmd string) (string, error)

	// Attach returns a ReadWriteCloser tied to the session's PTY.
	// The caller is responsible for closing the returned stream.
	Attach(id string) (io.ReadWriteCloser, error)

	// Detach disconnects the active client from the session.
	Detach(id string) error

	// List returns metadata for all active sessions.
	List() ([]SessionInfo, error)

	// Kill terminates the session and all its processes.
	Kill(id string) error

	// Resize adjusts the PTY dimensions.
	Resize(id string, rows, cols int) error

	// SendInput writes keystrokes to the session.
	SendInput(id string, input string) error

	// CaptureOutput returns the visible content of the session's pane.
	CaptureOutput(id string) (string, error)
}
