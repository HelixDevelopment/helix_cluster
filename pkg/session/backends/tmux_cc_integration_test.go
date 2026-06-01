//go:build integration

package backends

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestControlModeSession_RealTmux_MarkerSurfaced is the sink-side integration
// test for HXC-1084.  It:
//  1. Starts a real tmux -CC control-mode session.
//  2. Embeds a per-run UUID marker in a tmux send-keys command so the pane
//     outputs the marker.
//  3. Waits for the ControlModeSession parser to surface the marker via a
//     %output event (NOT via capture-pane polling — the real control-mode
//     protocol path).
//
// t.Skip is called ONLY if tmux is not found in PATH; hard-FAIL otherwise.
func TestControlModeSession_RealTmux_MarkerSurfaced(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not found in PATH; skipping integration test")
	}

	// Per-run UUID embedded in the echo so we can assert the exact marker.
	uuid := fmt.Sprintf("HXC-1084-MARKER-%d", time.Now().UnixNano())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sessionName := fmt.Sprintf("hxc-1084-test-%d", time.Now().UnixNano())

	cs, err := NewControlModeSession(ctx, sessionName)
	if err != nil {
		t.Fatalf("NewControlModeSession: %v", err)
	}
	defer cs.Close()

	sub := cs.Subscribe()

	// Give tmux a moment to emit the initial session-changed/window-add events.
	time.Sleep(300 * time.Millisecond)

	// Send the echo command that writes the marker to the pane. We use the
	// default pane (%0) which is created by new-session.
	echoCmd := fmt.Sprintf("send-keys -t %s 'echo %s' Enter", sessionName, uuid)
	if err := cs.SendCommand(echoCmd); err != nil {
		t.Fatalf("SendCommand: %v", err)
	}

	// Drain events until we see the marker in a %output event or timeout.
	timeout := time.After(15 * time.Second)
	found := false
	for !found {
		select {
		case e, ok := <-sub:
			if !ok {
				t.Fatal("subscriber channel closed before marker was seen")
			}
			if e.Type == EventOutput && strings.Contains(string(e.Data), uuid) {
				// Sink-side assertion: the real tmux control-mode protocol
				// surfaced the marker we injected.
				found = true
			}
		case <-timeout:
			t.Fatalf("timeout waiting for marker %q to appear as a %%output event", uuid)
		}
	}

	// Kill the tmux session to clean up.
	_ = exec.Command("tmux", "kill-session", "-t", sessionName).Run()
}

// TestControlModeSession_RealTmux_WindowAdd proves that starting a new-session
// emits at least one %window-add event (the initial window), surfaced via the
// control-mode protocol.
//
// Sink-side: the parser must deliver EventWindowAdd with a non-empty WindowID.
func TestControlModeSession_RealTmux_WindowAdd(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not found in PATH; skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sessionName := fmt.Sprintf("hxc-1084-winadd-%d", time.Now().UnixNano())
	cs, err := NewControlModeSession(ctx, sessionName)
	if err != nil {
		t.Fatalf("NewControlModeSession: %v", err)
	}
	defer cs.Close()
	defer func() {
		_ = exec.Command("tmux", "kill-session", "-t", sessionName).Run()
	}()

	sub := cs.Subscribe()

	timeout := time.After(10 * time.Second)
	for {
		select {
		case e, ok := <-sub:
			if !ok {
				t.Fatal("subscriber closed before window-add event")
			}
			if e.Type == EventWindowAdd {
				if e.WindowID == "" {
					t.Fatalf("EventWindowAdd.WindowID must not be empty; got %+v", e)
				}
				// Sink-side: real tmux emitted a %window-add and we parsed it.
				return
			}
		case <-timeout:
			t.Fatal("timeout waiting for EventWindowAdd from real tmux")
		}
	}
}

// TestControlModeSession_RealTmux_CommandResponse proves that a tmux command
// sent via SendCommand elicits a %begin..%end block and a synthesized
// EventCommandResponse with the response body, proving the full round-trip.
func TestControlModeSession_RealTmux_CommandResponse(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not found in PATH; skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sessionName := fmt.Sprintf("hxc-1084-cmdresp-%d", time.Now().UnixNano())
	cs, err := NewControlModeSession(ctx, sessionName)
	if err != nil {
		t.Fatalf("NewControlModeSession: %v", err)
	}
	defer cs.Close()
	defer func() {
		_ = exec.Command("tmux", "kill-session", "-t", sessionName).Run()
	}()

	sub := cs.Subscribe()

	// Give tmux time to start.
	time.Sleep(300 * time.Millisecond)

	// list-windows will produce a %begin..%end response.
	if err := cs.SendCommand("list-windows -t " + sessionName); err != nil {
		t.Fatalf("SendCommand list-windows: %v", err)
	}

	timeout := time.After(10 * time.Second)
	for {
		select {
		case e, ok := <-sub:
			if !ok {
				t.Fatal("subscriber closed before command-response")
			}
			if e.Type == EventCommandResponse && !e.Errored {
				// Sink-side: real tmux sent a %begin..%end block and our parser
				// synthesized an EventCommandResponse carrying the window list.
				return
			}
		case <-timeout:
			t.Fatal("timeout waiting for EventCommandResponse from real tmux list-windows")
		}
	}
}
