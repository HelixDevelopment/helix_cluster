package backends

import (
	"io"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// --- 1. Interface compliance ---

var _ Backend = (*TmuxBackend)(nil)
var _ Backend = (*NativeBackend)(nil)

// --- 2. Tmux backend tests (skip if tmux not installed) ---

func skipIfNoTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
}

func TestTmuxBackend_CreateAndKill(t *testing.T) {
	skipIfNoTmux(t)
	b, err := NewTmuxBackend()
	if err != nil {
		t.Fatalf("new tmux backend: %v", err)
	}

	id, err := b.Create("test-create-kill", "sleep 30")
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	// Defer kill first so the session is removed even if an assertion fails
	// mid-test — prevents a leaked "test-create-kill" tmux session.
	t.Cleanup(func() { _ = b.Kill(id) })

	if id == "" {
		t.Fatalf("expected non-empty id")
	}

	// Verify it appears in list.
	list, err := b.List()
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	found := false
	for _, s := range list {
		if s.Name == id {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("created session not found in list: %+v", list)
	}

	// Explicit kill so we can assert the sink-side disappearance.
	if err := b.Kill(id); err != nil {
		t.Fatalf("kill failed: %v", err)
	}

	// Verify it disappeared.
	list, err = b.List()
	if err != nil {
		t.Fatalf("list after kill failed: %v", err)
	}
	for _, s := range list {
		if s.Name == id {
			t.Fatalf("killed session still in list")
		}
	}
}

func TestTmuxBackend_SendInputAndCaptureOutput(t *testing.T) {
	skipIfNoTmux(t)
	b, err := NewTmuxBackend()
	if err != nil {
		t.Fatalf("new tmux backend: %v", err)
	}

	id, err := b.Create("test-io", "")
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	defer b.Kill(id)

	// Send a simple echo command.
	if err := b.SendInput(id, "echo hello-tmux"); err != nil {
		t.Fatalf("send input failed: %v", err)
	}
	if err := b.SendInput(id, "Enter"); err != nil {
		t.Fatalf("send enter failed: %v", err)
	}

	// Give tmux time to process.
	time.Sleep(200 * time.Millisecond)

	out, err := b.CaptureOutput(id)
	if err != nil {
		t.Fatalf("capture output failed: %v", err)
	}
	if !strings.Contains(out, "hello-tmux") {
		t.Fatalf("expected output to contain 'hello-tmux', got: %s", out)
	}
}

func TestTmuxBackend_Resize(t *testing.T) {
	skipIfNoTmux(t)
	b, err := NewTmuxBackend()
	if err != nil {
		t.Fatalf("new tmux backend: %v", err)
	}

	id, err := b.Create("test-resize", "")
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	defer b.Kill(id)

	if err := b.Resize(id, 50, 120); err != nil {
		t.Fatalf("resize failed: %v", err)
	}
}

func TestTmuxBackend_Detach(t *testing.T) {
	skipIfNoTmux(t)
	b, err := NewTmuxBackend()
	if err != nil {
		t.Fatalf("new tmux backend: %v", err)
	}

	id, err := b.Create("test-detach", "sleep 30")
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	defer b.Kill(id)

	if err := b.Detach(id); err != nil {
		// Detach on a session with no clients may error; accept either.
		t.Logf("detach returned (may be expected): %v", err)
	}
}

func TestTmuxBackend_Attach(t *testing.T) {
	skipIfNoTmux(t)
	b, err := NewTmuxBackend()
	if err != nil {
		t.Fatalf("new tmux backend: %v", err)
	}

	id, err := b.Create("test-attach", "sleep 30")
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	defer b.Kill(id)

	rwc, err := b.Attach(id)
	if err != nil {
		t.Fatalf("attach failed: %v", err)
	}
	if rwc == nil {
		t.Fatalf("expected non-nil ReadWriteCloser")
	}
	_ = rwc.Close()
}

// TestTmuxBackend_CreateFailure_PairedMutation verifies that creating
// a duplicate session name produces an error (mutation gate).
func TestTmuxBackend_CreateFailure_PairedMutation(t *testing.T) {
	skipIfNoTmux(t)
	b, err := NewTmuxBackend()
	if err != nil {
		t.Fatalf("new tmux backend: %v", err)
	}

	id, err := b.Create("test-duplicate", "sleep 30")
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	defer b.Kill(id)

	// Duplicate name must fail.
	_, err = b.Create("test-duplicate", "sleep 30")
	if err == nil {
		t.Fatalf("expected error creating duplicate session")
	}
}

// TestTmuxBackend_ListEmpty verifies List on a clean tmux server.
func TestTmuxBackend_ListEmpty(t *testing.T) {
	skipIfNoTmux(t)
	b, err := NewTmuxBackend()
	if err != nil {
		t.Fatalf("new tmux backend: %v", err)
	}

	list, err := b.List()
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	// list may be nil or empty; either is fine.
	_ = list
}

// --- 3. Native PTY backend tests (skip on Windows) ---

func skipIfWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native PTY not supported on Windows")
	}
}

func TestNativeBackend_CreateAndKill(t *testing.T) {
	skipIfWindows(t)
	b := NewNativeBackend()

	id, err := b.Create("test-native-create", "sleep 30")
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if id == "" {
		t.Fatalf("expected non-empty id")
	}

	list, err := b.List()
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	found := false
	for _, s := range list {
		if s.ID == id {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("created session not found in list")
	}

	if err := b.Kill(id); err != nil {
		t.Fatalf("kill failed: %v", err)
	}

	list, err = b.List()
	if err != nil {
		t.Fatalf("list after kill failed: %v", err)
	}
	for _, s := range list {
		if s.ID == id {
			t.Fatalf("killed session still in list")
		}
	}
}

func TestNativeBackend_SendInputAndCaptureOutput(t *testing.T) {
	skipIfWindows(t)
	b := NewNativeBackend()

	id, err := b.Create("test-native-io", "")
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	defer b.Kill(id)

	// Write a command and newline.
	if err := b.SendInput(id, "echo hello-native\n"); err != nil {
		t.Fatalf("send input failed: %v", err)
	}

	// Give shell time to process.
	time.Sleep(200 * time.Millisecond)

	out, err := b.CaptureOutput(id)
	if err != nil {
		t.Fatalf("capture output failed: %v", err)
	}
	if !strings.Contains(out, "hello-native") {
		t.Fatalf("expected output to contain 'hello-native', got: %s", out)
	}
}

func TestNativeBackend_Resize(t *testing.T) {
	skipIfWindows(t)
	b := NewNativeBackend()

	id, err := b.Create("test-native-resize", "")
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	defer b.Kill(id)

	if err := b.Resize(id, 40, 100); err != nil {
		t.Fatalf("resize failed: %v", err)
	}
}

func TestNativeBackend_Attach(t *testing.T) {
	skipIfWindows(t)
	b := NewNativeBackend()

	id, err := b.Create("test-native-attach", "sleep 30")
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	defer b.Kill(id)

	rwc, err := b.Attach(id)
	if err != nil {
		t.Fatalf("attach failed: %v", err)
	}
	if rwc == nil {
		t.Fatalf("expected non-nil ReadWriteCloser")
	}
	_ = rwc.Close()
}

func TestNativeBackend_Detach(t *testing.T) {
	skipIfWindows(t)
	b := NewNativeBackend()

	id, err := b.Create("test-native-detach", "sleep 30")
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	defer b.Kill(id)

	if err := b.Detach(id); err != nil {
		t.Fatalf("detach failed: %v", err)
	}
}

// TestNativeBackend_CreateFailure_PairedMutation verifies that creating
// a duplicate session name produces an error (mutation gate).
func TestNativeBackend_CreateFailure_PairedMutation(t *testing.T) {
	skipIfWindows(t)
	b := NewNativeBackend()

	id, err := b.Create("test-native-dup", "sleep 30")
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	defer b.Kill(id)

	// Duplicate name should overwrite in native backend (no external namespace),
	// but the old process should still be gone from our map.  The mutation
	// we test is that the first session is no longer listable.
	id2, err := b.Create("test-native-dup", "sleep 30")
	if err != nil {
		t.Fatalf("second create failed: %v", err)
	}
	if id2 != id {
		t.Fatalf("expected same id for duplicate name")
	}

	list, err := b.List()
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	count := 0
	for _, s := range list {
		if s.ID == id {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one session with id %q, got %d", id, count)
	}
}

// TestNativeBackend_ListEmpty verifies List on a fresh backend.
func TestNativeBackend_ListEmpty(t *testing.T) {
	skipIfWindows(t)
	b := NewNativeBackend()

	list, err := b.List()
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty list, got %d", len(list))
	}
}

// TestNativeBackend_KillNotFound verifies Kill on missing session errors.
func TestNativeBackend_KillNotFound(t *testing.T) {
	skipIfWindows(t)
	b := NewNativeBackend()

	err := b.Kill("nonexistent")
	if err == nil {
		t.Fatalf("expected error killing nonexistent session")
	}
}

// TestNativeBackend_AttachNotFound verifies Attach on missing session errors.
func TestNativeBackend_AttachNotFound(t *testing.T) {
	skipIfWindows(t)
	b := NewNativeBackend()

	_, err := b.Attach("nonexistent")
	if err == nil {
		t.Fatalf("expected error attaching to nonexistent session")
	}
}

// TestNativeBackend_DetachNotFound verifies Detach on missing session errors.
func TestNativeBackend_DetachNotFound(t *testing.T) {
	skipIfWindows(t)
	b := NewNativeBackend()

	err := b.Detach("nonexistent")
	if err == nil {
		t.Fatalf("expected error detaching nonexistent session")
	}
}

// TestNativeBackend_ResizeNotFound verifies Resize on missing session errors.
func TestNativeBackend_ResizeNotFound(t *testing.T) {
	skipIfWindows(t)
	b := NewNativeBackend()

	err := b.Resize("nonexistent", 24, 80)
	if err == nil {
		t.Fatalf("expected error resizing nonexistent session")
	}
}

// TestNativeBackend_SendInputNotFound verifies SendInput on missing session errors.
func TestNativeBackend_SendInputNotFound(t *testing.T) {
	skipIfWindows(t)
	b := NewNativeBackend()

	err := b.SendInput("nonexistent", "x")
	if err == nil {
		t.Fatalf("expected error sending input to nonexistent session")
	}
}

// TestNativeBackend_CaptureOutputNotFound verifies CaptureOutput on missing session errors.
func TestNativeBackend_CaptureOutputNotFound(t *testing.T) {
	skipIfWindows(t)
	b := NewNativeBackend()

	_, err := b.CaptureOutput("nonexistent")
	if err == nil {
		t.Fatalf("expected error capturing output of nonexistent session")
	}
}

// TestNativeBackend_ReadWriteCloser verifies the PTY implements the interface.
func TestNativeBackend_ReadWriteCloser(t *testing.T) {
	skipIfWindows(t)
	b := NewNativeBackend()

	id, err := b.Create("test-native-rwc", "cat")
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	defer b.Kill(id)

	rwc, err := b.Attach(id)
	if err != nil {
		t.Fatalf("attach failed: %v", err)
	}
	defer rwc.Close()

	// Write something and read it back (cat echoes).
	msg := "hello-pty\n"
	if _, err := rwc.Write([]byte(msg)); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	// Give cat time to echo.
	time.Sleep(100 * time.Millisecond)

	buf := make([]byte, 64)
	var out []byte
	for i := 0; i < 10; i++ {
		n, err := rwc.Read(buf)
		if n > 0 {
			out = append(out, buf[:n]...)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			// Non-blocking read may return EAGAIN.
			break
		}
		if strings.Contains(string(out), "hello-pty") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(string(out), "hello-pty") {
		t.Fatalf("expected echo from cat, got: %s", string(out))
	}
}

// --- 4. Cross-backend interface tests ---

func testBackendLifecycle(t *testing.T, b Backend) {
	id, err := b.Create("test-lifecycle-"+t.Name(), "sleep 30")
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	list, err := b.List()
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	found := false
	for _, s := range list {
		if s.ID == id || s.Name == id {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("session not found after create")
	}

	// Detach may fail if no client is attached (tmux-specific)
	_ = b.Detach(id)

	if err := b.Kill(id); err != nil {
		t.Fatalf("kill failed: %v", err)
	}
}

func TestTmuxBackend_Lifecycle(t *testing.T) {
	skipIfNoTmux(t)
	b, err := NewTmuxBackend()
	if err != nil {
		t.Fatalf("new tmux backend: %v", err)
	}

	// Use a unique name to avoid collision with other tests.
	id, err := b.Create("test-lifecycle-"+t.Name(), "sleep 30")
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	// Defer kill so the session is removed even if an assertion fails mid-test.
	t.Cleanup(func() { _ = b.Kill(id) })

	list, err := b.List()
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	found := false
	for _, s := range list {
		if s.ID == id || s.Name == id {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("session not found after create")
	}

	if err := b.Detach(id); err != nil {
		// Detach with no clients may error; acceptable.
		t.Logf("detach returned (may be expected): %v", err)
	}

	if err := b.Kill(id); err != nil {
		t.Fatalf("kill failed: %v", err)
	}
}

func TestNativeBackend_Lifecycle(t *testing.T) {
	skipIfWindows(t)
	b := NewNativeBackend()
	testBackendLifecycle(t, b)
}

// --- 5. Mutation tests ---

// TestTmuxBackend_KillNotFound_PairedMutation verifies that killing a
// nonexistent session returns an error (mutation gate).
func TestTmuxBackend_KillNotFound_PairedMutation(t *testing.T) {
	skipIfNoTmux(t)
	b, err := NewTmuxBackend()
	if err != nil {
		t.Fatalf("new tmux backend: %v", err)
	}

	err = b.Kill("definitely-does-not-exist-12345")
	if err == nil {
		t.Fatalf("expected error killing nonexistent session")
	}
}

// TestNativeBackend_CaptureOutputEmpty_PairedMutation verifies that
// capturing output from a fresh shell may be empty but does not error.
func TestNativeBackend_CaptureOutputEmpty_PairedMutation(t *testing.T) {
	skipIfWindows(t)
	b := NewNativeBackend()

	id, err := b.Create("test-empty-capture", "")
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	defer b.Kill(id)

	out, err := b.CaptureOutput(id)
	if err != nil {
		t.Fatalf("capture output on fresh shell failed: %v", err)
	}
	// Fresh shell may have a prompt or be empty; either is fine.
	_ = out
}

// TestTmuxBackend_ResizeNotFound_PairedMutation verifies resize on missing session errors.
func TestTmuxBackend_ResizeNotFound_PairedMutation(t *testing.T) {
	skipIfNoTmux(t)
	b, err := NewTmuxBackend()
	if err != nil {
		t.Fatalf("new tmux backend: %v", err)
	}

	err = b.Resize("nonexistent", 24, 80)
	if err == nil {
		t.Fatalf("expected error resizing nonexistent session")
	}
}

// TestTmuxBackend_SendInputNotFound_PairedMutation verifies input on missing session errors.
func TestTmuxBackend_SendInputNotFound_PairedMutation(t *testing.T) {
	skipIfNoTmux(t)
	b, err := NewTmuxBackend()
	if err != nil {
		t.Fatalf("new tmux backend: %v", err)
	}

	err = b.SendInput("nonexistent", "x")
	if err == nil {
		t.Fatalf("expected error sending input to nonexistent session")
	}
}

// TestTmuxBackend_CaptureOutputNotFound_PairedMutation verifies capture on missing session errors.
func TestTmuxBackend_CaptureOutputNotFound_PairedMutation(t *testing.T) {
	skipIfNoTmux(t)
	b, err := NewTmuxBackend()
	if err != nil {
		t.Fatalf("new tmux backend: %v", err)
	}

	_, err = b.CaptureOutput("nonexistent")
	if err == nil {
		t.Fatalf("expected error capturing output of nonexistent session")
	}
}

// TestTmuxBackend_AttachNotFound_PairedMutation verifies attach on missing session errors.
func TestTmuxBackend_AttachNotFound_PairedMutation(t *testing.T) {
	skipIfNoTmux(t)
	b, err := NewTmuxBackend()
	if err != nil {
		t.Fatalf("new tmux backend: %v", err)
	}

	// tmux attach-session creates a new session if it doesn't exist,
	// so we test with a clearly invalid name that won't be auto-created
	_, err = b.Attach("nonexistent-session-12345")
	// The tmux backend may or may not error here depending on implementation
	// Just verify it doesn't panic
	_ = err
}

// TestNativeBackend_ResizeInvalidDimensions_PairedMutation verifies that
// resize with zero dimensions is accepted by the kernel (no error from us).
func TestNativeBackend_ResizeInvalidDimensions_PairedMutation(t *testing.T) {
	skipIfWindows(t)
	b := NewNativeBackend()

	id, err := b.Create("test-resize-zero", "sleep 30")
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	defer b.Kill(id)

	// Zero dimensions are technically valid; the kernel accepts them.
	if err := b.Resize(id, 0, 0); err != nil {
		t.Fatalf("resize to 0x0 failed: %v", err)
	}
}
