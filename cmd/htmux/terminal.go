// Package main provides the htmux CLI.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"golang.org/x/term"
)

// Terminal handles raw mode setup, teardown, and I/O bridging.
type Terminal struct {
	stdin  *os.File
	stdout *os.File
	// oldState holds the original terminal state for restoration.
	oldState *term.State
	mu       sync.Mutex
}

// NewTerminal creates a Terminal backed by the given files.
func NewTerminal(stdin, stdout *os.File) *Terminal {
	return &Terminal{
		stdin:  stdin,
		stdout: stdout,
	}
}

// IsTerminal reports whether stdin is a terminal.
func (t *Terminal) IsTerminal() bool {
	return term.IsTerminal(int(t.stdin.Fd()))
}

// SetRawMode switches the terminal into raw mode.
func (t *Terminal) SetRawMode() error {
	if !t.IsTerminal() {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	oldState, err := term.MakeRaw(int(t.stdin.Fd()))
	if err != nil {
		return fmt.Errorf("set raw mode: %w", err)
	}
	t.oldState = oldState
	return nil
}

// Restore reverts the terminal to its original state.
func (t *Terminal) Restore() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.oldState == nil {
		return nil
	}
	if err := term.Restore(int(t.stdin.Fd()), t.oldState); err != nil {
		return fmt.Errorf("restore terminal: %w", err)
	}
	t.oldState = nil
	return nil
}

// Size returns the current terminal dimensions (columns, rows).
func (t *Terminal) Size() (int, int, error) {
	if !t.IsTerminal() {
		return 80, 24, nil
	}
	width, height, err := term.GetSize(int(t.stdin.Fd()))
	if err != nil {
		return 0, 0, fmt.Errorf("get terminal size: %w", err)
	}
	return width, height, nil
}

// Stream bridges data between the local terminal and remote streams.
// It copies stdin to w and remote output from r to stdout.
// Resize signals are forwarded via the resize channel.
func (t *Terminal) Stream(ctx context.Context, r io.Reader, w io.Writer) error {
	// Ensure terminal is restored on exit.
	defer t.Restore()

	if err := t.SetRawMode(); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	// Forward local stdin to remote writer.
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := io.Copy(w, t.stdin)
		if err != nil && err != io.EOF {
			errCh <- fmt.Errorf("stdin copy: %w", err)
		}
		cancel()
	}()

	// Forward remote output to local stdout.
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := io.Copy(t.stdout, r)
		if err != nil && err != io.EOF {
			errCh <- fmt.Errorf("stdout copy: %w", err)
		}
		cancel()
	}()

	// Handle window resize signals.
	resizeCh := make(chan os.Signal, 1)
	signal.Notify(resizeCh, syscall.SIGWINCH)
	defer signal.Stop(resizeCh)

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-resizeCh:
				// Resize handling is a hook for future integration.
				// The current session service does not expose a streaming
				// attach RPC, so we simply record the event.
			}
		}
	}()

	// Wait for either direction to finish or context cancellation.
	<-ctx.Done()

	// Close stdin to unblock io.Copy if still waiting.
	// In a real implementation with a bidirectional gRPC stream,
	// closing the stream writer would be used instead.
	_ = t.stdin.Close()

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}
