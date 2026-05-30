//go:build !windows
// +build !windows

package backends

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"github.com/creack/pty"
)

// NativeBackend implements Backend using local PTYs.
type NativeBackend struct {
	mu       sync.Mutex
	sessions map[string]*nativeSession
}

type nativeSession struct {
	id   string
	name string
	cmd  *exec.Cmd
	pty  *os.File
	mu   sync.Mutex
}

// NewNativeBackend creates a fallback backend that allocates PTYs directly.
func NewNativeBackend() *NativeBackend {
	return &NativeBackend{
		sessions: make(map[string]*nativeSession),
	}
}

// Create starts a new command inside a PTY.
func (n *NativeBackend) Create(name string, cmd string) (string, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	var c *exec.Cmd
	if cmd != "" {
		c = exec.Command(shell, "-c", cmd)
	} else {
		c = exec.Command(shell)
	}

	ptmx, err := pty.Start(c)
	if err != nil {
		return "", fmt.Errorf("pty start failed: %w", err)
	}

	sess := &nativeSession{
		id:   name,
		name: name,
		cmd:  c,
		pty:  ptmx,
	}

	n.mu.Lock()
	n.sessions[name] = sess
	n.mu.Unlock()

	// Reap the process when it exits.
	go func() {
		_ = c.Wait()
		n.mu.Lock()
		delete(n.sessions, name)
		n.mu.Unlock()
		_ = ptmx.Close()
	}()

	return name, nil
}

// Attach returns the PTY master as a ReadWriteCloser.
func (n *NativeBackend) Attach(id string) (io.ReadWriteCloser, error) {
	n.mu.Lock()
	sess, ok := n.sessions[id]
	n.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("session %q not found", id)
	}
	return sess.pty, nil
}

// Detach is a no-op for the native backend (there is no multiplexed client).
func (n *NativeBackend) Detach(id string) error {
	n.mu.Lock()
	_, ok := n.sessions[id]
	n.mu.Unlock()
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}
	return nil
}

// List returns all active native sessions.
func (n *NativeBackend) List() ([]SessionInfo, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	out := make([]SessionInfo, 0, len(n.sessions))
	for _, s := range n.sessions {
		out = append(out, SessionInfo{
			ID:   s.id,
			Name: s.name,
		})
	}
	return out, nil
}

// Kill sends SIGKILL to the session's process group.
func (n *NativeBackend) Kill(id string) error {
	n.mu.Lock()
	sess, ok := n.sessions[id]
	delete(n.sessions, id)
	n.mu.Unlock()
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}

	if sess.cmd != nil && sess.cmd.Process != nil {
		_ = sess.cmd.Process.Kill()
	}
	if sess.pty != nil {
		_ = sess.pty.Close()
	}
	return nil
}

// Resize adjusts the PTY window size using TIOCSWINSZ.
func (n *NativeBackend) Resize(id string, rows, cols int) error {
	n.mu.Lock()
	sess, ok := n.sessions[id]
	n.mu.Unlock()
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}

	ws := &winsize{
		Row: uint16(rows),
		Col: uint16(cols),
	}
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		sess.pty.Fd(),
		syscall.TIOCSWINSZ,
		uintptr(unsafe.Pointer(ws)),
	)
	if errno != 0 {
		return fmt.Errorf("ioctl TIOCSWINSZ failed: %v", errno)
	}
	return nil
}

// SendInput writes raw bytes to the PTY master.
func (n *NativeBackend) SendInput(id string, input string) error {
	n.mu.Lock()
	sess, ok := n.sessions[id]
	n.mu.Unlock()
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}
	_, err := sess.pty.Write([]byte(input))
	return err
}

// CaptureOutput reads all currently available bytes from the PTY.
// This is best-effort and non-blocking; for the MVP it drains the buffer.
func (n *NativeBackend) CaptureOutput(id string) (string, error) {
	n.mu.Lock()
	sess, ok := n.sessions[id]
	n.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("session %q not found", id)
	}

	// Set non-blocking read temporarily.
	fd := sess.pty.Fd()
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFL, 0)
	if errno != 0 {
		return "", fmt.Errorf("fcntl get failed: %v", errno)
	}
	_, _, errno = syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_SETFL, flags|syscall.O_NONBLOCK)
	if errno != 0 {
		return "", fmt.Errorf("fcntl set nonblock failed: %v", errno)
	}
	defer func() {
		_, _, _ = syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_SETFL, flags)
	}()

	buf := make([]byte, 4096)
	var out []byte
	for {
		nr, err := sess.pty.Read(buf)
		if nr > 0 {
			out = append(out, buf[:nr]...)
		}
		if err != nil {
			break
		}
	}
	return string(out), nil
}

// winsize mirrors the C struct used by TIOCSWINSZ.
type winsize struct {
	Row    uint16
	Col    uint16
	Xpixel uint16
	Ypixel uint16
}
