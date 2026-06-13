package backends

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestTmuxFunctionalAttach_MarkerRoundTrip is the CLAUDE-1 anti-bluff proof
// for HXC-1160.  It:
//  1. Creates a real tmux session running /bin/sh (unique name, leak-free).
//  2. Calls Attach() to get the functional stream.
//  3. Writes "echo HELIXMARKER_<unique>\n" via Write().
//  4. Polls Read() up to 20 times (100 ms apart, max 2 s) until the marker
//     appears in the accumulated pane output.
//  5. Asserts the marker IS present — proving Write reached the real tmux
//     session AND Read returns real pane content.
//  6. Defers Kill() so zero tmux sessions leak even on failure.
//
// Mutation intent: a Write that does not call send-keys, or a Read that
// returns EOF / fabricated bytes, makes the marker assertion fail.
func TestTmuxFunctionalAttach_MarkerRoundTrip(t *testing.T) {
	skipIfNoTmux(t)

	b, err := NewTmuxBackend()
	if err != nil {
		t.Fatalf("NewTmuxBackend: %v", err)
	}

	// Unique session name: combines pid and test name to avoid any collision.
	name := fmt.Sprintf("hxc-attach-%d-%s", os.Getpid(), sanitizeName(t.Name()))
	if len(name) > 50 {
		name = name[:50]
	}

	id, err := b.Create(name, "/bin/sh")
	if err != nil {
		t.Fatalf("Create session %q: %v", name, err)
	}
	defer func() {
		if kerr := b.Kill(id); kerr != nil {
			t.Logf("Kill %q: %v (may already be gone)", id, kerr)
		}
	}()

	// Give the shell a moment to start before attaching.
	time.Sleep(150 * time.Millisecond)

	stream, err := b.Attach(id)
	if err != nil {
		t.Fatalf("Attach(%q): %v", id, err)
	}
	defer stream.Close()

	// Unique marker — includes pid so two parallel test runs don't collide.
	marker := fmt.Sprintf("HELIXMARKER_%d", os.Getpid())
	cmd := fmt.Sprintf("echo %s\n", marker)

	n, err := stream.Write([]byte(cmd))
	if err != nil {
		t.Fatalf("Write command: %v", err)
	}
	if n != len(cmd) {
		t.Fatalf("Write: wrote %d bytes, want %d", n, len(cmd))
	}

	// Bounded poll: up to 20 iterations × 100 ms = 2 s max.
	const (
		maxPolls  = 20
		pollSleep = 100 * time.Millisecond
	)

	buf := make([]byte, 4096)
	var accumulated string

	for i := 0; i < maxPolls; i++ {
		time.Sleep(pollSleep)

		rn, rerr := stream.Read(buf)
		if rerr != nil {
			t.Fatalf("Read error on poll %d: %v", i, rerr)
		}
		if rn > 0 {
			accumulated += string(buf[:rn])
		}
		if strings.Contains(accumulated, marker) {
			break
		}
	}

	if !strings.Contains(accumulated, marker) {
		t.Fatalf(
			"marker %q not found in pane output after %d polls.\n"+
				"Accumulated pane output:\n%s",
			marker, maxPolls, accumulated,
		)
	}
	// Sink-side proof: marker reached the real tmux session (Write worked)
	// AND was returned by Read from the real capture-pane (Read works).
}

// TestTmuxFunctionalAttach_WriteDelivery verifies Write returns len(p) and
// does not error — paired mutation: a closed-stream Write must error.
func TestTmuxFunctionalAttach_WriteDelivery(t *testing.T) {
	skipIfNoTmux(t)

	b, err := NewTmuxBackend()
	if err != nil {
		t.Fatalf("NewTmuxBackend: %v", err)
	}

	name := fmt.Sprintf("hxc-wdel-%d", os.Getpid())
	id, err := b.Create(name, "/bin/sh")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer b.Kill(id)

	time.Sleep(100 * time.Millisecond)

	stream, err := b.Attach(id)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	payload := []byte("echo hi\n")
	n, err := stream.Write(payload)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(payload) {
		t.Fatalf("Write returned %d, want %d", n, len(payload))
	}

	// Close and verify subsequent Write errors (mutation gate).
	if err := stream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err = stream.Write([]byte("x"))
	if err == nil {
		t.Fatalf("expected error writing to closed stream")
	}
}

// TestTmuxFunctionalAttach_ReadReturnsBytes verifies Read returns > 0 bytes
// from a real session (not EOF, not fabricated).
func TestTmuxFunctionalAttach_ReadReturnsBytes(t *testing.T) {
	skipIfNoTmux(t)

	b, err := NewTmuxBackend()
	if err != nil {
		t.Fatalf("NewTmuxBackend: %v", err)
	}

	name := fmt.Sprintf("hxc-rread-%d", os.Getpid())
	id, err := b.Create(name, "/bin/sh")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer b.Kill(id)

	time.Sleep(200 * time.Millisecond)

	stream, err := b.Attach(id)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer stream.Close()

	// A fresh shell pane should have some content (at least a prompt).
	// Poll up to 5 times to get non-zero bytes.
	buf := make([]byte, 4096)
	var total int
	for i := 0; i < 5; i++ {
		n, err := stream.Read(buf)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		total += n
		if total > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	// A shell pane always has at least a prompt; if we get 0 bytes the
	// Read implementation is returning fabricated/empty data instead of
	// real capture-pane output.
	if total == 0 {
		t.Fatalf("Read returned 0 bytes across 5 polls — expected real pane content from capture-pane")
	}
}

// sanitizeName replaces characters that are illegal in tmux session names.
func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}
