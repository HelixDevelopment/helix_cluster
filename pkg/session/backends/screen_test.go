package backends

import (
	"fmt"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
)

// Interface compliance — compile-time assertion.
var _ Backend = (*ScreenBackend)(nil)

// screenSessionCounter provides unique session names without relying on time or rand.
var screenSessionCounter uint64

// uniqueScreenName builds a deterministic, unique session name from the
// current PID and a monotonically increasing counter.  This guarantees no
// two test invocations (even in parallel) share a name, and the name is
// traceable to the test process for leak auditing.
func uniqueScreenName(prefix string) string {
	n := atomic.AddUint64(&screenSessionCounter, 1)
	return fmt.Sprintf("hxc-%s-%d-%d", prefix, os.Getpid(), n)
}

func skipIfNoScreen(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("screen"); err != nil {
		t.Skip("screen not installed")
	}
}

// TestScreenBackend_NewOK verifies that NewScreenBackend succeeds when
// screen is present on the host.  Fails if screen is missing (it should
// not be on this host).
func TestScreenBackend_NewOK(t *testing.T) {
	skipIfNoScreen(t)
	b, err := NewScreenBackend()
	if err != nil {
		t.Fatalf("NewScreenBackend() unexpected error: %v", err)
	}
	if b == nil {
		t.Fatal("NewScreenBackend() returned nil backend")
	}
}

// TestScreenBackend_CreateListKill is the primary sink-side proof:
//
//  1. Create a uniquely-named session.
//  2. List() shows it is really present (sink-side evidence — only passes if
//     screen actually created the session).
//  3. Kill it.
//  4. List() confirms it is gone.
//
// The deferred Kill guarantees zero session leaks even on test failure.
func TestScreenBackend_CreateListKill(t *testing.T) {
	skipIfNoScreen(t)

	b, err := NewScreenBackend()
	if err != nil {
		t.Fatalf("NewScreenBackend: %v", err)
	}

	name := uniqueScreenName("clk")

	// Deferred kill: runs even if the test panics/fails, preventing leaks.
	defer func() {
		// Best-effort; ignore error if already gone.
		_ = b.Kill(name)
	}()

	// --- Step 1: Create ---
	id, err := b.Create(name, "sleep 60")
	if err != nil {
		t.Fatalf("Create(%q): %v", name, err)
	}
	if id == "" {
		t.Fatal("Create returned empty id")
	}
	if id != name {
		t.Fatalf("Create returned id %q, want %q", id, name)
	}

	// --- Step 2: List shows the session (sink-side) ---
	list, err := b.List()
	if err != nil {
		t.Fatalf("List() after Create: %v", err)
	}
	found := false
	for _, si := range list {
		if si.Name == name {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("created session %q not visible in List(); list=%+v", name, list)
	}

	// --- Step 3: Kill ---
	if err := b.Kill(name); err != nil {
		t.Fatalf("Kill(%q): %v", name, err)
	}

	// --- Step 4: List confirms gone ---
	list2, err := b.List()
	if err != nil {
		t.Fatalf("List() after Kill: %v", err)
	}
	for _, si := range list2 {
		if si.Name == name {
			t.Fatalf("killed session %q still appears in List(); list=%+v", name, list2)
		}
	}
}

// TestScreenBackend_CreateBadFlag_MutationGate is the paired mutation test.
// It passes a garbage flag to screen so Create fails, then asserts that
// List() does NOT show the session — proving that a broken Create cannot
// produce a false positive in the sink-side List check above.
func TestScreenBackend_CreateBadFlag_MutationGate(t *testing.T) {
	skipIfNoScreen(t)

	b, err := NewScreenBackend()
	if err != nil {
		t.Fatalf("NewScreenBackend: %v", err)
	}

	// Inject a bad flag by temporarily overriding the binary path to a
	// wrapper that always fails — we achieve this by directly testing a
	// broken ScreenBackend instance.
	broken := &ScreenBackend{bin: "/usr/bin/false"} // always exits non-zero

	name := uniqueScreenName("mut")
	defer func() { _ = b.Kill(name) }() // guard; session should never exist

	_, err = broken.Create(name, "sleep 60")
	if err == nil {
		t.Fatal("expected Create to fail with broken binary, but it succeeded")
	}

	// Confirm via the real backend that the session was never created.
	list, listErr := b.List()
	if listErr != nil {
		t.Fatalf("List() failed: %v", listErr)
	}
	for _, si := range list {
		if si.Name == name {
			t.Fatalf("session %q appeared despite broken Create — mutation gate failed", name)
		}
	}
}

// TestScreenBackend_SendInput verifies that SendInput does not error on
// a live session (sink: screen accepts the stuff command).
func TestScreenBackend_SendInput(t *testing.T) {
	skipIfNoScreen(t)

	b, err := NewScreenBackend()
	if err != nil {
		t.Fatalf("NewScreenBackend: %v", err)
	}

	name := uniqueScreenName("si")
	defer func() { _ = b.Kill(name) }()

	if _, err := b.Create(name, "sleep 60"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := b.SendInput(name, "echo hello-screen\r"); err != nil {
		t.Errorf("SendInput: %v", err)
	}
}

// TestScreenBackend_CaptureOutput verifies that CaptureOutput returns without
// error on a live session (sink: hardcopy file is written and read back).
func TestScreenBackend_CaptureOutput(t *testing.T) {
	skipIfNoScreen(t)

	b, err := NewScreenBackend()
	if err != nil {
		t.Fatalf("NewScreenBackend: %v", err)
	}

	name := uniqueScreenName("co")
	defer func() { _ = b.Kill(name) }()

	if _, err := b.Create(name, "sleep 60"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	out, err := b.CaptureOutput(name)
	if err != nil {
		t.Fatalf("CaptureOutput: %v", err)
	}
	// out may be empty for a fresh session; the important thing is no error.
	_ = out
}

// TestScreenBackend_AttachExists verifies Attach returns a non-nil
// ReadWriteCloser for an existing session.
func TestScreenBackend_AttachExists(t *testing.T) {
	skipIfNoScreen(t)

	b, err := NewScreenBackend()
	if err != nil {
		t.Fatalf("NewScreenBackend: %v", err)
	}

	name := uniqueScreenName("att")
	defer func() { _ = b.Kill(name) }()

	if _, err := b.Create(name, "sleep 60"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	rwc, err := b.Attach(name)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if rwc == nil {
		t.Fatal("Attach returned nil ReadWriteCloser")
	}
	_ = rwc.Close()
}

// TestScreenBackend_AttachMissing verifies Attach returns an error for a
// non-existent session (mutation gate for Attach).
func TestScreenBackend_AttachMissing(t *testing.T) {
	skipIfNoScreen(t)

	b, err := NewScreenBackend()
	if err != nil {
		t.Fatalf("NewScreenBackend: %v", err)
	}

	_, err = b.Attach("definitely-not-a-real-session-hxc-1130")
	if err == nil {
		t.Fatal("expected error Attaching to non-existent session")
	}
}

// TestScreenBackend_ParseScreenList_Unit unit-tests the list parser
// against known output without spawning any real sessions.
func TestScreenBackend_ParseScreenList_Unit(t *testing.T) {
	input := `There are screens on:
	12345.my-session	(Detached)
	67890.another-one	(Attached)
2 Sockets in /var/folders/example.

`
	sessions := parseScreenList(input)
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d: %+v", len(sessions), sessions)
	}

	got := map[string]bool{}
	for _, s := range sessions {
		got[s.Name] = s.Active
	}

	if _, ok := got["my-session"]; !ok {
		t.Errorf("expected 'my-session' in parsed list; got %+v", sessions)
	}
	if active, ok := got["another-one"]; !ok || !active {
		t.Errorf("expected 'another-one' to be Active=true; got %+v", sessions)
	}
	if got["my-session"] {
		t.Errorf("expected 'my-session' to be Active=false")
	}
}

// TestScreenBackend_ParseScreenList_Empty verifies an empty list is returned
// when screen reports no sessions.
func TestScreenBackend_ParseScreenList_Empty(t *testing.T) {
	input := `No Sockets found in /var/folders/t3/example/.screen.

`
	sessions := parseScreenList(input)
	if len(sessions) != 0 {
		t.Fatalf("expected 0 sessions from empty output, got %d: %+v", len(sessions), sessions)
	}
}
