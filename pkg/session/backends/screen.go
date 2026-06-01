package backends

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// ErrBackendUnavailable is returned by a backend constructor when the
// required binary is not found on the host. Callers may use errors.Is to
// distinguish "binary absent" from other setup failures.
var ErrBackendUnavailable = errors.New("backend binary not available on this host")

// ScreenBackend implements Backend by shelling out to GNU screen.
type ScreenBackend struct {
	bin string // resolved path to the screen binary
	mu  sync.Mutex
}

// NewScreenBackend creates a screen backend.
// Returns ErrBackendUnavailable (wrapped) if screen is not in PATH.
func NewScreenBackend() (*ScreenBackend, error) {
	bin, err := exec.LookPath("screen")
	if err != nil {
		return nil, fmt.Errorf("%w: screen: %v", ErrBackendUnavailable, err)
	}
	return &ScreenBackend{bin: bin}, nil
}

// Create starts a new detached screen session named name.
// If cmd is non-empty it is run via "sh -c <cmd>" so that shell syntax
// (arguments, pipes, etc.) works correctly. Returns the session name as the ID.
func (s *ScreenBackend) Create(name string, cmd string) (string, error) {
	// screen -dmS <name> [sh -c <cmd>]
	// Passing cmd as a bare string to exec.Command would make screen treat
	// the entire string as an executable name; wrapping in sh -c preserves
	// shell semantics (argument splitting, built-ins, etc.).
	args := []string{"-dmS", name}
	if cmd != "" {
		args = append(args, "sh", "-c", cmd)
	}
	out, err := exec.Command(s.bin, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("screen create failed for %q: %w (output: %s)", name, err, strings.TrimSpace(string(out)))
	}
	return name, nil
}

// Attach returns a best-effort pipe tied to the session.
// A full PTY attach is interactive and not suitable for programmatic use;
// we provide the same synthetic pipe approach as TmuxBackend.
func (s *ScreenBackend) Attach(id string) (io.ReadWriteCloser, error) {
	// Verify the session exists first.
	list, err := s.List()
	if err != nil {
		return nil, fmt.Errorf("screen attach: list failed: %w", err)
	}
	found := false
	for _, si := range list {
		if si.Name == id || si.ID == id {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("screen attach: session %q not found", id)
	}
	return &screenAttachStream{session: id, backend: s}, nil
}

// screenAttachStream satisfies io.ReadWriteCloser for screen attach.
//
// Write delivers its bytes as real keystrokes to the live screen session via
// SendInput (screen -X stuff) — it is a genuine sink-side operation, not a
// pipe to nowhere. Read returns EOF: this programmatic attach is write-oriented
// (use CaptureOutput to read the pane), so there is no streaming read channel.
type screenAttachStream struct {
	session string
	backend *ScreenBackend
	closed  bool
	mu      sync.Mutex
}

func (a *screenAttachStream) Read(p []byte) (int, error) {
	return 0, io.EOF
}

func (a *screenAttachStream) Write(p []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return 0, fmt.Errorf("screen attach stream closed")
	}
	if err := a.backend.SendInput(a.session, string(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (a *screenAttachStream) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	return nil
}

// Detach sends the detach command to the session's clients.
func (s *ScreenBackend) Detach(id string) error {
	out, err := exec.Command(s.bin, "-S", id, "-X", "detach").CombinedOutput()
	if err != nil {
		return fmt.Errorf("screen detach failed for %q: %w (output: %s)", id, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// List returns metadata for all active screen sessions on this host.
// screen -ls always exits non-zero; we parse the output regardless.
func (s *ScreenBackend) List() ([]SessionInfo, error) {
	out, _ := exec.Command(s.bin, "-ls").CombinedOutput()
	return parseScreenList(string(out)), nil
}

// parseScreenList parses the output of `screen -ls`.
// Each session line has the form:
//
//	\t<pid>.<name>\t(State)
func parseScreenList(output string) []SessionInfo {
	var sessions []SessionInfo
	for _, line := range strings.Split(output, "\n") {
		// Trim leading/trailing whitespace (tabs, spaces, \r).
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Lines we care about contain a dot (pid.name) and parentheses (State).
		if !strings.Contains(trimmed, ".") || !strings.Contains(trimmed, "(") {
			continue
		}
		// Format: <pid>.<name>\t(State)   or   <pid>.<name>   (State)
		// Split on the first whitespace to separate the socket from the state.
		fields := strings.Fields(trimmed)
		if len(fields) < 2 {
			continue
		}
		socket := fields[0] // e.g. "41319.hxc-fmt-41316"
		stateField := fields[1] // e.g. "(Detached)"

		dotIdx := strings.Index(socket, ".")
		if dotIdx < 0 {
			continue
		}
		name := socket[dotIdx+1:]
		active := strings.Contains(stateField, "Attached")

		sessions = append(sessions, SessionInfo{
			ID:     socket,
			Name:   name,
			Active: active,
		})
	}
	return sessions
}

// Kill terminates a screen session by sending the quit command.
func (s *ScreenBackend) Kill(id string) error {
	out, err := exec.Command(s.bin, "-S", id, "-X", "quit").CombinedOutput()
	if err != nil {
		return fmt.Errorf("screen kill failed for %q: %w (output: %s)", id, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Resize sends a resize command to the screen session.
func (s *ScreenBackend) Resize(id string, rows, cols int) error {
	// screen's resize command: resize -v <lines>  sets vertical; width is set
	// by the terminal emulator.  We use the "fit" subcommand as a best-effort
	// and then send an explicit width command.
	out, err := exec.Command(s.bin, "-S", id, "-X", "resize", fmt.Sprintf("-v %d", rows)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("screen resize rows failed for %q: %w (output: %s)", id, err, strings.TrimSpace(string(out)))
	}
	out, err = exec.Command(s.bin, "-S", id, "-X", "width", fmt.Sprintf("%d", cols)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("screen resize cols failed for %q: %w (output: %s)", id, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SendInput sends keystrokes to the session via screen's stuff command.
func (s *ScreenBackend) SendInput(id string, input string) error {
	out, err := exec.Command(s.bin, "-S", id, "-X", "stuff", input).CombinedOutput()
	if err != nil {
		return fmt.Errorf("screen stuff failed for %q: %w (output: %s)", id, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// CaptureOutput captures the session's visible screen content via hardcopy.
func (s *ScreenBackend) CaptureOutput(id string) (string, error) {
	s.mu.Lock()
	// Create a temp file for the hardcopy output.
	tmpFile, err := os.CreateTemp("", "helix-screen-hardcopy-*")
	if err != nil {
		s.mu.Unlock()
		return "", fmt.Errorf("screen capture: create tempfile: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	s.mu.Unlock()

	defer os.Remove(tmpPath)

	// screen hardcopy writes to an absolute path.
	absPath, err := filepath.Abs(tmpPath)
	if err != nil {
		return "", fmt.Errorf("screen capture: abs path: %w", err)
	}

	out, err := exec.Command(s.bin, "-S", id, "-X", "hardcopy", absPath).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("screen hardcopy failed for %q: %w (output: %s)", id, err, strings.TrimSpace(string(out)))
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("screen capture: read hardcopy file: %w", err)
	}
	return string(data), nil
}
