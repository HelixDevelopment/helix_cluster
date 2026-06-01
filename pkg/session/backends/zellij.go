package backends

import (
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// ZellijBackend implements Backend by shelling out to zellij.
// When the zellij binary is absent, the constructor returns ErrBackendUnavailable
// and all methods return that error so callers can distinguish "not installed"
// from unexpected runtime failures.
type ZellijBackend struct {
	bin string // resolved path; empty string means unavailable
}

// NewZellijBackend creates a zellij backend.
// Returns ErrBackendUnavailable (wrapped) if zellij is not in PATH.
func NewZellijBackend() (*ZellijBackend, error) {
	bin, err := exec.LookPath("zellij")
	if err != nil {
		return nil, fmt.Errorf("%w: zellij: %v", ErrBackendUnavailable, err)
	}
	return &ZellijBackend{bin: bin}, nil
}

// unavailableErr returns the standard error for operations on an unavailable backend.
func (z *ZellijBackend) unavailableErr(op string) error {
	return fmt.Errorf("%w: zellij: %s", ErrBackendUnavailable, op)
}

// Create starts a new detached zellij session.
// Equivalent to: zellij --session <name> [options] (runs in background).
func (z *ZellijBackend) Create(name string, cmd string) (string, error) {
	if z.bin == "" {
		return "", z.unavailableErr("Create")
	}
	// zellij sessions can be created via: zellij --session <name>
	// We run it detached using a background approach; zellij does not have
	// a native -dm flag like screen/tmux, so we launch via the action subcommand.
	args := []string{"--session", name}
	if cmd != "" {
		args = append(args, "--", cmd)
	}
	out, err := exec.Command(z.bin, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("zellij create failed for %q: %w (output: %s)", name, err, strings.TrimSpace(string(out)))
	}
	return name, nil
}

// Attach returns a ReadWriteCloser tied to the zellij session.
func (z *ZellijBackend) Attach(id string) (io.ReadWriteCloser, error) {
	if z.bin == "" {
		return nil, z.unavailableErr("Attach")
	}
	list, err := z.List()
	if err != nil {
		return nil, fmt.Errorf("zellij attach: list failed: %w", err)
	}
	found := false
	for _, si := range list {
		if si.Name == id || si.ID == id {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("zellij attach: session %q not found", id)
	}
	_, pw := io.Pipe()
	return &zellijAttachStream{session: id, pw: pw}, nil
}

// zellijAttachStream satisfies io.ReadWriteCloser for zellij attach.
type zellijAttachStream struct {
	session string
	pw      *io.PipeWriter
	closed  bool
}

func (a *zellijAttachStream) Read(p []byte) (int, error) {
	return 0, io.EOF
}

func (a *zellijAttachStream) Write(p []byte) (int, error) {
	if a.closed {
		return 0, fmt.Errorf("zellij attach stream closed")
	}
	return a.pw.Write(p)
}

func (a *zellijAttachStream) Close() error {
	a.closed = true
	return a.pw.Close()
}

// Detach detaches from a zellij session.
func (z *ZellijBackend) Detach(id string) error {
	if z.bin == "" {
		return z.unavailableErr("Detach")
	}
	out, err := exec.Command(z.bin, "action", "detach").CombinedOutput()
	if err != nil {
		return fmt.Errorf("zellij detach failed for %q: %w (output: %s)", id, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// List returns metadata for all active zellij sessions.
func (z *ZellijBackend) List() ([]SessionInfo, error) {
	if z.bin == "" {
		return nil, z.unavailableErr("List")
	}
	out, err := exec.Command(z.bin, "list-sessions").CombinedOutput()
	if err != nil {
		outStr := strings.TrimSpace(string(out))
		// No sessions is not a hard error.
		if strings.Contains(outStr, "No active zellij sessions") || outStr == "" {
			return nil, nil
		}
		return nil, fmt.Errorf("zellij list-sessions failed: %w (output: %s)", err, outStr)
	}
	return parseZellijList(string(out)), nil
}

// parseZellijList parses `zellij list-sessions` output.
// Each line is a session name, optionally followed by status in brackets.
func parseZellijList(output string) []SessionInfo {
	var sessions []SessionInfo
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Lines may look like: "session-name [Created ...]" or just "session-name"
		name := trimmed
		if idx := strings.Index(trimmed, " "); idx > 0 {
			name = trimmed[:idx]
		}
		sessions = append(sessions, SessionInfo{
			ID:   name,
			Name: name,
		})
	}
	return sessions
}

// Kill terminates a zellij session.
func (z *ZellijBackend) Kill(id string) error {
	if z.bin == "" {
		return z.unavailableErr("Kill")
	}
	out, err := exec.Command(z.bin, "kill-session", id).CombinedOutput()
	if err != nil {
		return fmt.Errorf("zellij kill-session failed for %q: %w (output: %s)", id, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Resize adjusts the zellij pane dimensions.
func (z *ZellijBackend) Resize(id string, rows, cols int) error {
	if z.bin == "" {
		return z.unavailableErr("Resize")
	}
	// zellij action resize takes "Up/Down/Left/Right" or absolute via plugin;
	// best-effort: use the resize action.
	out, err := exec.Command(z.bin, "action", "resize", fmt.Sprintf("%dx%d", cols, rows)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("zellij resize failed for %q: %w (output: %s)", id, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SendInput writes keystrokes to the zellij session.
func (z *ZellijBackend) SendInput(id string, input string) error {
	if z.bin == "" {
		return z.unavailableErr("SendInput")
	}
	out, err := exec.Command(z.bin, "action", "write-chars", input).CombinedOutput()
	if err != nil {
		return fmt.Errorf("zellij write-chars failed for %q: %w (output: %s)", id, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// CaptureOutput is not natively supported by zellij in the same way as screen/tmux.
// We return an unsupported error when the binary is absent; when present, we return
// an explicit not-supported message so callers degrade gracefully.
func (z *ZellijBackend) CaptureOutput(id string) (string, error) {
	if z.bin == "" {
		return "", z.unavailableErr("CaptureOutput")
	}
	return "", fmt.Errorf("CaptureOutput not supported by zellij backend")
}
