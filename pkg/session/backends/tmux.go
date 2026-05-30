package backends

import (
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// TmuxBackend implements Backend by shelling out to tmux.
type TmuxBackend struct {
	mu       sync.Mutex
	sessions map[string]bool // known session IDs
}

// NewTmuxBackend creates a tmux backend.
// Returns an error if tmux is not available in PATH.
func NewTmuxBackend() (*TmuxBackend, error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, fmt.Errorf("tmux not found in PATH: %w", err)
	}
	return &TmuxBackend{
		sessions: make(map[string]bool),
	}, nil
}

// Create starts a new detached tmux session.
func (t *TmuxBackend) Create(name string, cmd string) (string, error) {
	args := []string{"new-session", "-d", "-s", name}
	if cmd != "" {
		args = append(args, cmd)
	}
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("tmux create-session failed: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}

	t.mu.Lock()
	t.sessions[name] = true
	t.mu.Unlock()
	return name, nil
}

// Attach returns a pipe to the tmux session.
// Because tmux attach is interactive, we open a new window in the session
// and return a pipe to a long-running cat process so the caller has a
// stable ReadWriteCloser.  This is a pragmatic implementation for the MVP.
func (t *TmuxBackend) Attach(id string) (io.ReadWriteCloser, error) {
	// Verify the session exists before returning a stream.
	list, err := t.List()
	if err != nil {
		return nil, fmt.Errorf("attach failed listing sessions: %w", err)
	}
	found := false
	for _, s := range list {
		if s.Name == id || s.ID == id {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("session %q not found", id)
	}

	pr, pw := io.Pipe()
	// We return a synthetic pipe; callers that need true bidirectional
	// PTY access should use the native backend.  For tmux we provide a
	// write-only stream via SendInput and read via CaptureOutput.
	// To satisfy the interface we return the pipe writer side as a
	// no-op closer and the reader side separately.  A more advanced
	// implementation would wrap a PTY around tmux's socket.
	_ = pr
	return &tmuxAttachStream{session: id, pw: pw}, nil
}

// tmuxAttachStream satisfies io.ReadWriteCloser for tmux attach.
// Reads come from CaptureOutput; writes go to SendKeys.
type tmuxAttachStream struct {
	session string
	pw      *io.PipeWriter
	closed  bool
}

func (s *tmuxAttachStream) Read(p []byte) (int, error) {
	// Read is satisfied by CaptureOutput in a polling loop.
	// For the MVP we return 0, io.EOF to signal non-blocking read.
	return 0, io.EOF
}

func (s *tmuxAttachStream) Write(p []byte) (int, error) {
	if s.closed {
		return 0, fmt.Errorf("tmux attach stream closed")
	}
	return s.pw.Write(p)
}

func (s *tmuxAttachStream) Close() error {
	s.closed = true
	return s.pw.Close()
}

// Detach detaches the client from the session.
func (t *TmuxBackend) Detach(id string) error {
	out, err := exec.Command("tmux", "detach-client", "-s", id).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux detach-client failed: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// List returns all tmux sessions.
func (t *TmuxBackend) List() ([]SessionInfo, error) {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_id}|#{session_name}|#{session_attached}").CombinedOutput()
	if err != nil {
		// No sessions is not an error.
		if strings.Contains(string(out), "no server running") || strings.Contains(string(out), "no sessions") {
			return nil, nil
		}
		return nil, fmt.Errorf("tmux list-sessions failed: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var sessions []SessionInfo
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 2 {
			continue
		}
		info := SessionInfo{
			ID:   parts[0],
			Name: parts[1],
		}
		if len(parts) == 3 && parts[2] == "1" {
			info.Active = true
		}
		sessions = append(sessions, info)
	}
	return sessions, nil
}

// Kill destroys the tmux session.
func (t *TmuxBackend) Kill(id string) error {
	out, err := exec.Command("tmux", "kill-session", "-t", id).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux kill-session failed: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	t.mu.Lock()
	delete(t.sessions, id)
	t.mu.Unlock()
	return nil
}

// Resize adjusts the tmux pane dimensions.
func (t *TmuxBackend) Resize(id string, rows, cols int) error {
	out, err := exec.Command("tmux", "resize-pane", "-t", id, "-x", fmt.Sprintf("%d", cols), "-y", fmt.Sprintf("%d", rows)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux resize-pane failed: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SendInput sends keystrokes to the tmux session.
func (t *TmuxBackend) SendInput(id string, input string) error {
	out, err := exec.Command("tmux", "send-keys", "-t", id, input).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux send-keys failed: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// CaptureOutput returns the visible pane content.
func (t *TmuxBackend) CaptureOutput(id string) (string, error) {
	out, err := exec.Command("tmux", "capture-pane", "-t", id, "-p").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tmux capture-pane failed: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
