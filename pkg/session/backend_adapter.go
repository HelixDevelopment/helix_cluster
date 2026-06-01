package session

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/HelixDevelopment/helix_cluster/pkg/session/backends"
)

// BackendAdapter adapts a pkg/session/backends.Backend (the low-level tmux /
// native-PTY interface) to the pkg/session.TmuxBackend interface consumed by
// Manager.  This is the bridge that eliminates the nil-backend StatusRunning
// bluff: when a real backends.Backend is wired in, Manager.Create actually
// starts a process and SessionExists can verify it.
//
// Only operations that backends.Backend exposes are forwarded to the real
// implementation; operations with no direct equivalent (window management,
// state save/restore) are fulfilled via direct tmux shell-out so the Manager
// still has a complete interface implementation.
type BackendAdapter struct {
	be backends.Backend
}

// NewBackendAdapter wraps be in a BackendAdapter.
func NewBackendAdapter(be backends.Backend) *BackendAdapter {
	return &BackendAdapter{be: be}
}

// CreateSession creates a detached tmux session via the wrapped backend.
// env is applied as KEY=VALUE pairs passed to the shell command.
func (a *BackendAdapter) CreateSession(name string, env map[string]string) (string, error) {
	// Build an environment-prefixed shell invocation if any env vars were given.
	cmd := ""
	if len(env) > 0 {
		var pairs []string
		for k, v := range env {
			pairs = append(pairs, fmt.Sprintf("%s=%s", k, v))
		}
		cmd = strings.Join(pairs, " ")
	}
	id, err := a.be.Create(name, cmd)
	if err != nil {
		return "", err
	}
	return id, nil
}

// AttachSession delegates to the backend's Attach (we open and immediately
// close the stream — Manager.Attach is a side-effectful marker, not a
// long-running read/write loop at this level).
func (a *BackendAdapter) AttachSession(name string) error {
	rwc, err := a.be.Attach(name)
	if err != nil {
		return err
	}
	_ = rwc.Close()
	return nil
}

// DetachSession delegates to the backend.
func (a *BackendAdapter) DetachSession(name string) error {
	return a.be.Detach(name)
}

// KillSession delegates to the backend.
func (a *BackendAdapter) KillSession(name string) error {
	return a.be.Kill(name)
}

// ListSessions returns the names of all active sessions from the backend.
func (a *BackendAdapter) ListSessions() ([]string, error) {
	infos, err := a.be.List()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}
	return names, nil
}

// SessionExists reports whether a session with the given name is alive in the
// underlying backend.  This is the sink-side probe used by unit tests and
// health checks to prove a real process was launched.
func (a *BackendAdapter) SessionExists(name string) (bool, error) {
	infos, err := a.be.List()
	if err != nil {
		return false, err
	}
	for _, info := range infos {
		if info.Name == name || info.ID == name {
			return true, nil
		}
	}
	return false, nil
}

// CreateWindow creates a new window inside the named session.
// Delegated via direct tmux call because backends.Backend has no window API.
func (a *BackendAdapter) CreateWindow(sessionName, windowName string) (string, error) {
	out, err := exec.Command("tmux", "new-window", "-t", sessionName, "-n", windowName).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tmux new-window: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return windowName, nil
}

// SplitWindow splits a window in the named session.
func (a *BackendAdapter) SplitWindow(sessionName, windowName string) (string, error) {
	target := sessionName + ":" + windowName
	out, err := exec.Command("tmux", "split-window", "-t", target).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tmux split-window: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return target + ".1", nil
}

// ResizePane resizes a pane.
func (a *BackendAdapter) ResizePane(sessionName, pane string, rows, cols int) error {
	return a.be.Resize(sessionName, rows, cols)
}

// SendKeys sends keystrokes to the session/pane.
func (a *BackendAdapter) SendKeys(sessionName, pane string, keys string) error {
	return a.be.SendInput(sessionName, keys)
}

// CapturePane returns visible content of a pane.
func (a *BackendAdapter) CapturePane(sessionName, pane string) (string, error) {
	return a.be.CaptureOutput(sessionName)
}

// GetSessionState serializes session state via tmux show-environment.
func (a *BackendAdapter) GetSessionState(sessionName string) ([]byte, error) {
	out, err := exec.Command("tmux", "show-environment", "-t", sessionName).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("tmux show-environment: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// RestoreSessionState restores session state via tmux set-environment.
func (a *BackendAdapter) RestoreSessionState(sessionName string, state []byte) error {
	// Best-effort: set-environment line by line.
	for _, line := range strings.Split(strings.TrimSpace(string(state)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "-") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		if err := exec.Command("tmux", "set-environment", "-t", sessionName, parts[0], parts[1]).Run(); err != nil {
			return fmt.Errorf("tmux set-environment %s: %w", parts[0], err)
		}
	}
	return nil
}

// Ensure BackendAdapter satisfies the TmuxBackend interface at compile time.
var _ TmuxBackend = (*BackendAdapter)(nil)
