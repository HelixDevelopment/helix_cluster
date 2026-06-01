package backends

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// tmuxFunctionalAttachStream is a fully functional io.ReadWriteCloser for a
// live tmux session.
//
// Write(p): delivers p as real keystrokes to the session via
//   tmux send-keys -t <id> -l <literal-text>
// followed by a separate tmux send-keys -t <id> Enter for each trailing '\n',
// so that command lines are actually executed by the shell.
//
// Read(p): returns REAL pane content by calling
//   tmux capture-pane -t <id> -p
// on each call.  To avoid returning the same bytes on every call, we track how
// many bytes of the captured output have already been returned and only hand
// back new bytes.  When the capture has grown beyond what was previously seen,
// the delta is returned; otherwise 0 bytes and no error are returned (the
// caller should poll with a short sleep between reads).
//
// Close(): marks the stream closed; further Write/Read calls return an error.
type tmuxFunctionalAttachStream struct {
	session string
	mu      sync.Mutex
	closed  bool
	// readOffset is the number of bytes of captured pane output already
	// returned to the caller.  Capture-pane returns the full visible pane
	// content on every call, so we track how much we have already yielded.
	readOffset int
}

// Write delivers p as literal keystrokes to the tmux session.
// If p ends with '\n', the trailing newline is replaced by a separate
// "Enter" key event so that the shell executes the typed line.
func (s *tmuxFunctionalAttachStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, fmt.Errorf("tmux attach stream closed")
	}
	if len(p) == 0 {
		return 0, nil
	}

	// Split on newlines so that multi-line input is handled correctly:
	// each line is sent literally and then an Enter key event is injected.
	text := string(p)
	parts := strings.Split(text, "\n")

	// parts will have len >= 1.
	// e.g. "echo hi\n"  -> ["echo hi", ""]
	// e.g. "echo hi"    -> ["echo hi"]
	// e.g. "a\nb\n"     -> ["a", "b", ""]

	for i, part := range parts {
		// Send the literal text for this part (may be empty string).
		if part != "" {
			out, err := exec.Command("tmux", "send-keys", "-t", s.session, "-l", part).CombinedOutput()
			if err != nil {
				return 0, fmt.Errorf("tmux send-keys -l failed: %w (output: %s)", err, strings.TrimSpace(string(out)))
			}
		}
		// If this is not the last part, there was a '\n' here — send Enter.
		if i < len(parts)-1 {
			out, err := exec.Command("tmux", "send-keys", "-t", s.session, "Enter").CombinedOutput()
			if err != nil {
				return 0, fmt.Errorf("tmux send-keys Enter failed: %w (output: %s)", err, strings.TrimSpace(string(out)))
			}
		}
	}

	return len(p), nil
}

// Read returns newly captured pane content from the real tmux session.
// It calls capture-pane on each invocation and returns bytes that have not
// yet been returned by a previous Read call.  The caller should poll with
// a short bounded sleep when 0 bytes are returned.
func (s *tmuxFunctionalAttachStream) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, fmt.Errorf("tmux attach stream closed")
	}

	out, err := exec.Command("tmux", "capture-pane", "-t", s.session, "-p").CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("tmux capture-pane failed: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}

	content := string(out)
	if s.readOffset >= len(content) {
		// No new bytes yet.
		return 0, nil
	}

	newBytes := []byte(content[s.readOffset:])
	n := copy(p, newBytes)
	s.readOffset += n
	return n, nil
}

// Close marks the stream closed.  Subsequent Read/Write calls return an error.
func (s *tmuxFunctionalAttachStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}
