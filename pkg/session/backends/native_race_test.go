//go:build !windows
// +build !windows

package backends

import (
	"sync"
	"testing"
)

// TestNativeBackend_KillCaptureRace_F5 surfaces the F5 PTY double-close /
// use-after-close concurrency bug: the reaper goroutine and Kill both close
// the same *os.File, while CaptureOutput/SendInput/Resize read sess.pty and
// then operate on it after releasing n.mu — so they can touch an fd that
// Kill/the reaper just closed (UAF on a possibly-recycled fd).
//
// Run with -race; the fix must make the pty fd close exactly once and prevent
// I/O from racing the close.
func TestNativeBackend_KillCaptureRace_F5(t *testing.T) {
	skipIfWindows(t)

	const iterations = 50
	for i := 0; i < iterations; i++ {
		b := NewNativeBackend()
		id, err := b.Create("test-native-kill-capture-race", "sleep 30")
		if err != nil {
			t.Fatalf("create failed: %v", err)
		}

		var wg sync.WaitGroup
		// Concurrently: Kill (closes pty), CaptureOutput, SendInput, Resize.
		// These must not double-close the fd nor read/write a closed/recycled fd.
		wg.Add(4)
		go func() {
			defer wg.Done()
			_ = b.Kill(id)
		}()
		go func() {
			defer wg.Done()
			_, _ = b.CaptureOutput(id)
		}()
		go func() {
			defer wg.Done()
			_ = b.SendInput(id, "echo race\n")
		}()
		go func() {
			defer wg.Done()
			_ = b.Resize(id, 24, 80)
		}()
		wg.Wait()

		// Ensure cleanup even if Kill lost the race to a duplicate create.
		_ = b.Kill(id)
	}
}

// TestNativeBackend_DoubleKill_NoDoubleClose verifies Kill is idempotent at
// the fd level: a second Kill must not double-close the pty (the first removes
// it from the map, so the second is a not-found error — but we also exercise
// the close-once seam directly below).
func TestNativeBackend_DoubleKill_NoDoubleClose(t *testing.T) {
	skipIfWindows(t)
	b := NewNativeBackend()

	id, err := b.Create("test-native-double-kill", "sleep 30")
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	if err := b.Kill(id); err != nil {
		t.Fatalf("first kill failed: %v", err)
	}
	// Second kill must error (already removed) and must not panic/double-close.
	if err := b.Kill(id); err == nil {
		t.Fatalf("expected error on second kill of removed session")
	}
}

// TestNativeSession_CloseOnce verifies the per-session close-once seam closes
// the underlying fd exactly once even under concurrent close calls. This is a
// deterministic test of the close-once logic that does not depend on shell
// process scheduling.
func TestNativeSession_CloseOnce(t *testing.T) {
	skipIfWindows(t)
	b := NewNativeBackend()

	id, err := b.Create("test-native-close-once", "sleep 30")
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	b.mu.Lock()
	sess := b.sessions[id]
	b.mu.Unlock()
	if sess == nil {
		t.Fatalf("session not found after create")
	}
	defer b.Kill(id)

	// Concurrently invoke the close-once seam many times; it must close the fd
	// exactly once (no double-close) and report closed thereafter.
	var wg sync.WaitGroup
	const n = 32
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			sess.closePTY()
		}()
	}
	wg.Wait()

	if !sess.isClosed() {
		t.Fatalf("session should be marked closed after closePTY")
	}
}
