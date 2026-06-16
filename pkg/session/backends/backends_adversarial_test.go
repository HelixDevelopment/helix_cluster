//go:build !windows
// +build !windows

package backends

// backends_adversarial_test.go — adversarial probes against the NativeBackend
// session store (the in-memory map of live PTY sessions). These exercise the
// store contract directly: Create registers a session, List reflects a
// consistent snapshot of registered sessions, the I/O surface
// (SendInput/Resize/CaptureOutput/Attach/Detach) addresses exactly the
// registered session, and Kill removes it (sink-side) so subsequent operations
// observe its absence. Concurrency probes look for lost sessions, torn map
// state, and "operation says success but the store is inconsistent" gaps.
//
// Run the concurrency probe with -race exactly once:
//   go test ./pkg/session/backends/ -race -count=1 -run Adversarial -v

import (
	"sync"
	"testing"
	"time"
)

// adversarialDeadline guards every potentially-blocking probe so a regression
// can never hang the suite; it fails loudly instead.
func adversarialDeadline(t *testing.T, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("probe exceeded deadline %s (possible deadlock/hang)", d)
	}
}

// TestAdversarial_CreateThenList_SinkSide proves the store round-trips: after
// Create, the exact session ID/Name is visible in List (the store's snapshot),
// and after Kill it is gone. This is the get/put/delete + list contract.
func TestAdversarial_CreateThenList_SinkSide(t *testing.T) {
	skipIfWindows(t)
	b := NewNativeBackend()
	const id = "adv-create-list"

	got, err := b.Create(id, "sleep 30")
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	t.Cleanup(func() { _ = b.Kill(id) })
	if got != id {
		t.Fatalf("Create returned id %q, want %q", got, id)
	}

	list, err := b.List()
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if !listContains(list, id) {
		t.Fatalf("created session %q absent from List snapshot: %+v", id, list)
	}
	// Sink-side fields the store DOES populate must round-trip exactly.
	for _, s := range list {
		if s.ID == id && s.Name != id {
			t.Fatalf("List entry for %q has Name=%q, want %q (store mangled the record)", id, s.Name, id)
		}
	}

	if err := b.Kill(id); err != nil {
		t.Fatalf("kill failed: %v", err)
	}
	list2, err := b.List()
	if err != nil {
		t.Fatalf("list after kill failed: %v", err)
	}
	if listContains(list2, id) {
		t.Fatalf("killed session %q still present in List (delete not durable): %+v", id, list2)
	}
}

// TestAdversarial_OperationsOnMissingKey verifies every store operation returns
// an error (not a silent success / not a panic) for an unknown session ID.
func TestAdversarial_OperationsOnMissingKey(t *testing.T) {
	skipIfWindows(t)
	b := NewNativeBackend()
	const missing = "adv-does-not-exist"

	if err := b.Kill(missing); err == nil {
		t.Fatalf("Kill(missing) returned nil, want not-found error")
	}
	if err := b.Detach(missing); err == nil {
		t.Fatalf("Detach(missing) returned nil, want not-found error")
	}
	if err := b.SendInput(missing, "x"); err == nil {
		t.Fatalf("SendInput(missing) returned nil, want not-found error")
	}
	if err := b.Resize(missing, 24, 80); err == nil {
		t.Fatalf("Resize(missing) returned nil, want not-found error")
	}
	if _, err := b.CaptureOutput(missing); err == nil {
		t.Fatalf("CaptureOutput(missing) returned nil, want not-found error")
	}
	if _, err := b.Attach(missing); err == nil {
		t.Fatalf("Attach(missing) returned nil, want not-found error")
	}
}

// TestAdversarial_OperationsAfterKill_RejectCleanly verifies that once a session
// is killed (removed from the store), the I/O surface rejects it cleanly with an
// error rather than succeeding against a dead/closed session. This is the
// "expired/deleted entry must not be served" contract for this store.
func TestAdversarial_OperationsAfterKill_RejectCleanly(t *testing.T) {
	skipIfWindows(t)
	b := NewNativeBackend()
	const id = "adv-after-kill"

	if _, err := b.Create(id, "sleep 30"); err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if err := b.Kill(id); err != nil {
		t.Fatalf("kill failed: %v", err)
	}

	// After Kill the session is removed from the store, so every operation must
	// report not-found (clean error), never silently succeed.
	if err := b.SendInput(id, "x"); err == nil {
		t.Fatalf("SendInput after Kill returned nil, want error (served a deleted session)")
	}
	if err := b.Resize(id, 24, 80); err == nil {
		t.Fatalf("Resize after Kill returned nil, want error (served a deleted session)")
	}
	if _, err := b.CaptureOutput(id); err == nil {
		t.Fatalf("CaptureOutput after Kill returned nil, want error (served a deleted session)")
	}
}

// TestAdversarial_CreateOverwrite_StoreConsistent probes overwriting an existing
// key: a second Create with the same name must leave the store addressing the
// NEW session, and a single List entry for that key (no torn/duplicate record).
// It also asserts the reaper of the first session does not delete the second's
// entry (the documented "only drop if still ours" guard).
func TestAdversarial_CreateOverwrite_StoreConsistent(t *testing.T) {
	skipIfWindows(t)
	b := NewNativeBackend()
	const id = "adv-overwrite"

	// First session is SHORT-LIVED on purpose: after it is overwritten its
	// process exits and its reaper goroutine fires. That reaper must NOT delete
	// the surviving (second) entry — the store's "only drop if still ours" guard
	// keys deletion on the exact session pointer, not just the name.
	if _, err := b.Create(id, "true"); err != nil {
		t.Fatalf("first create failed: %v", err)
	}
	if _, err := b.Create(id, "sleep 30"); err != nil {
		t.Fatalf("second create (overwrite) failed: %v", err)
	}
	t.Cleanup(func() { _ = b.Kill(id) })

	// Exactly one live entry for the key — the store is a map keyed by name, so
	// duplicate keys collapse to one. Assert the snapshot is consistent.
	count := 0
	list, err := b.List()
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	for _, s := range list {
		if s.ID == id {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("after overwrite, List has %d entries for %q, want exactly 1: %+v", count, id, list)
	}

	// The key must still be addressable (the new session is live).
	if err := b.SendInput(id, "echo hi\n"); err != nil {
		t.Fatalf("SendInput to overwritten key failed: %v (store lost the live session)", err)
	}

	// Give any reaper of the first (replaced) session a chance to run; it must
	// NOT delete the surviving entry, because the stored pointer no longer
	// matches the replaced session.
	time.Sleep(50 * time.Millisecond)
	list2, err := b.List()
	if err != nil {
		t.Fatalf("list after settle failed: %v", err)
	}
	if !listContains(list2, id) {
		t.Fatalf("reaper of replaced session wrongly deleted the live entry %q: %+v", id, list2)
	}
}

// TestAdversarial_EmptyCommand_RoundTrip verifies the empty-value path: Create
// with an empty command launches the operator's $SHELL and still registers a
// fully-addressable session (no nil-deref, store entry well-formed).
func TestAdversarial_EmptyCommand_RoundTrip(t *testing.T) {
	skipIfWindows(t)
	b := NewNativeBackend()
	const id = "adv-empty-cmd"

	if _, err := b.Create(id, ""); err != nil {
		t.Fatalf("create with empty command failed: %v", err)
	}
	t.Cleanup(func() { _ = b.Kill(id) })

	list, err := b.List()
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if !listContains(list, id) {
		t.Fatalf("empty-command session %q not registered: %+v", id, list)
	}
	rw, err := b.Attach(id)
	if err != nil {
		t.Fatalf("attach to empty-command session failed: %v", err)
	}
	if rw == nil {
		t.Fatalf("Attach returned a nil stream for a live session")
	}
}

// TestAdversarial_ConcurrentCreateKillList probes the store under concurrent
// Create/Kill/List/SendInput across distinct keys for lost sessions, torn map
// reads, or panics. Run with -race. Invariant: List never panics, never
// observes a half-written entry, and a key that Create reported success for is
// addressable until Killed.
func TestAdversarial_ConcurrentCreateKillList(t *testing.T) {
	skipIfWindows(t)
	b := NewNativeBackend()

	adversarialDeadline(t, 30*time.Second, func() {
		const workers = 16
		var wg sync.WaitGroup
		wg.Add(workers)
		for w := 0; w < workers; w++ {
			go func(w int) {
				defer wg.Done()
				id := keyFor(w)
				for i := 0; i < 20; i++ {
					if _, err := b.Create(id, "sleep 30"); err != nil {
						// Create may fail only on a real pty exhaustion; tolerate
						// but do not require success.
						continue
					}
					_ = b.SendInput(id, "x")
					_, _ = b.CaptureOutput(id)
					_ = b.Resize(id, 24, 80)
					_ = b.Kill(id)
				}
			}(w)
		}
		// A concurrent reader hammering the snapshot for torn reads/panics.
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 400; i++ {
				list, err := b.List()
				if err != nil {
					t.Errorf("List failed under concurrency: %v", err)
					return
				}
				for _, s := range list {
					// A registered entry must always have its key fields set —
					// never a zero-value half-written record.
					if s.ID == "" {
						t.Errorf("List observed a half-written entry: %+v", s)
						return
					}
				}
			}
		}()
		wg.Wait()
	})

	// Final state: every key killable/clean; store must end empty of our keys
	// after a sweep.
	for w := 0; w < 16; w++ {
		_ = b.Kill(keyFor(w))
	}
	list, err := b.List()
	if err != nil {
		t.Fatalf("final list failed: %v", err)
	}
	for w := 0; w < 16; w++ {
		if listContains(list, keyFor(w)) {
			t.Fatalf("after sweeping kills, key %q still present (leaked store entry): %+v", keyFor(w), list)
		}
	}
}

func listContains(list []SessionInfo, id string) bool {
	for _, s := range list {
		if s.ID == id {
			return true
		}
	}
	return false
}

func keyFor(w int) string {
	return "adv-conc-" + string(rune('a'+w))
}
