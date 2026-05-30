//go:build windows
// +build windows

package backends

import (
	"fmt"
	"io"
)

// NativeBackend is a stub on Windows because PTYs are unsupported.
type NativeBackend struct{}

// NewNativeBackend returns a stub backend on Windows.
func NewNativeBackend() *NativeBackend {
	return &NativeBackend{}
}

func (n *NativeBackend) Create(name string, cmd string) (string, error) {
	return "", fmt.Errorf("native PTY backend is not supported on Windows")
}

func (n *NativeBackend) Attach(id string) (io.ReadWriteCloser, error) {
	return nil, fmt.Errorf("native PTY backend is not supported on Windows")
}

func (n *NativeBackend) Detach(id string) error {
	return fmt.Errorf("native PTY backend is not supported on Windows")
}

func (n *NativeBackend) List() ([]SessionInfo, error) {
	return nil, fmt.Errorf("native PTY backend is not supported on Windows")
}

func (n *NativeBackend) Kill(id string) error {
	return fmt.Errorf("native PTY backend is not supported on Windows")
}

func (n *NativeBackend) Resize(id string, rows, cols int) error {
	return fmt.Errorf("native PTY backend is not supported on Windows")
}

func (n *NativeBackend) SendInput(id string, input string) error {
	return fmt.Errorf("native PTY backend is not supported on Windows")
}

func (n *NativeBackend) CaptureOutput(id string) (string, error) {
	return "", fmt.Errorf("native PTY backend is not supported on Windows")
}
