package session

// session_adversarial2_test.go — second adversarial wave for the Manager session
// store, focused on POINTER/MAP/SLICE ALIASING along the systematic theme found
// and fixed in internal/node and pkg/discovery: a store that hands out (or stores)
// a value sharing memory with its internal state, so a caller mutating the value
// silently corrupts the store for every other holder — with the mutex protecting
// only the container, not the pointee.
//
// The first wave (session_adversarial_test.go) proved Get returns a deep copy for
// the TOP-LEVEL Environment/Labels maps. It did NOT cover two further aliasing
// vectors, both CONFIRMED real on the pre-fix code and FIXED in manager.go:
//
//   BUG 1 (store-side, Create): Create stored the caller's req.Environment /
//   req.Labels / req.GPURequest BY REFERENCE. A caller mutating its CreateRequest
//   maps/slice AFTER Create returned silently corrupted the stored session. FIXED
//   by deep-copying the request's reference fields on store.
//
//   BUG 2 (return-side, copySession Windows): copySession deep-copied the
//   top-level maps but did `copy(cs.Windows, s.Windows)` — a SHALLOW struct copy
//   that left every nested reference field (Window.CRDTState, each Pane.Environment
//   map, Pane.CRDTState bytes) ALIASED to the stored session. A caller mutating a
//   Get-returned window/pane corrupted the store. FIXED by deep-copying Windows
//   (copyWindow/copyPane).
//
// Every test asserts a SINK-SIDE verdict: mutate a value the caller legitimately
// holds, then re-Get from the store and assert the store is UNCHANGED. These FAIL
// on the pre-fix code and PASS on the code left behind.

import (
	"context"
	"sync"
	"testing"
	"time"
)

// withTimeout runs fn under a hard deadline so a buggy/blocking store operation
// surfaces as a test failure rather than hanging the whole package.
func withTimeout(t *testing.T, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("operation did not complete within %v (possible deadlock/hang)", d)
	}
}

// TestManager_CreateDoesNotAliasRequestMaps proves BUG 1 is fixed: after Create
// returns, mutating the SAME maps/slice the caller passed in CreateRequest must
// NOT bleed into the store. Pre-fix, Create did `Environment: req.Environment`
// etc., so the caller's later mutation corrupted the stored session.
//
// MUTATION that makes this FAIL (pre-fix): in Create, store req.Environment /
// req.Labels / req.GPURequest directly instead of deep-copying them.
func TestManager_CreateDoesNotAliasRequestMaps(t *testing.T) {
	mgr := NewManager(nil)

	env := map[string]string{"k": "orig"}
	labels := map[string]string{"l": "orig"}
	gpu := []string{"gpu-orig"}

	s, err := mgr.Create(context.Background(), &CreateRequest{
		Owner:       "alice",
		Backend:     BackendTmux,
		Environment: env,
		Labels:      labels,
		GPURequest:  gpu,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Caller mutates the maps/slice it still holds AFTER Create returned.
	env["k"] = "tampered"
	env["injected"] = "yes"
	labels["l"] = "tampered"
	gpu[0] = "gpu-tampered"

	got, err := mgr.Get(s.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Environment["k"] != "orig" {
		t.Fatalf("store-side aliasing: Create stored req.Environment by reference; got %q want %q", got.Environment["k"], "orig")
	}
	if _, injected := got.Environment["injected"]; injected {
		t.Fatalf("store-side aliasing: caller injected a new Environment key into the store post-Create")
	}
	if got.Labels["l"] != "orig" {
		t.Fatalf("store-side aliasing: Create stored req.Labels by reference; got %q want %q", got.Labels["l"], "orig")
	}
	if len(got.GPURequest) != 1 || got.GPURequest[0] != "gpu-orig" {
		t.Fatalf("store-side aliasing: Create stored req.GPURequest by reference; got %v want [gpu-orig]", got.GPURequest)
	}
}

// TestManager_GetWindowsAreDeepCopied proves BUG 2 is fixed: a session's Windows
// (and their nested Panes, Pane.Environment, Pane.CRDTState, Window.CRDTState)
// returned by Get must be fully isolated from the store. Pre-fix, copySession did
// a shallow copy of the Windows slice, so all nested reference fields were shared.
//
// MUTATION that makes this FAIL (pre-fix): in copySession, replace the per-window
// deep copy with `copy(cs.Windows, s.Windows)`.
func TestManager_GetWindowsAreDeepCopied(t *testing.T) {
	mgr := NewManager(nil)
	s, err := mgr.Create(context.Background(), &CreateRequest{Owner: "bob", Backend: BackendTmux})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Seed a window/pane with nested mutable state.
	if _, err := mgr.Update(s.ID, func(sess *Session) {
		sess.Windows = []Window{{
			ID:        "w1",
			Name:      "win",
			CRDTState: []byte("worig"),
			Panes: []Pane{{
				ID:          "p1",
				Command:     "bash",
				Environment: map[string]string{"pk": "orig"},
				CRDTState:   []byte("porig"),
			}},
		}}
	}); err != nil {
		t.Fatalf("Update seed: %v", err)
	}

	// Caller mutates EVERY nested reference field of the Get-returned copy.
	got1, err := mgr.Get(s.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got1.Windows[0].Panes[0].Environment["pk"] = "tampered"
	got1.Windows[0].Panes[0].Environment["new"] = "x"
	got1.Windows[0].Panes[0].CRDTState[0] = 'X'
	got1.Windows[0].CRDTState[0] = 'X'
	got1.Windows[0].Name = "tampered"

	// The store must be untouched.
	got2, err := mgr.Get(s.ID)
	if err != nil {
		t.Fatalf("Get again: %v", err)
	}
	if got2.Windows[0].Panes[0].Environment["pk"] != "orig" {
		t.Fatalf("aliasing: Pane.Environment bled through Get copy: got %q want %q",
			got2.Windows[0].Panes[0].Environment["pk"], "orig")
	}
	if _, injected := got2.Windows[0].Panes[0].Environment["new"]; injected {
		t.Fatalf("aliasing: caller injected a key into the stored Pane.Environment")
	}
	if string(got2.Windows[0].Panes[0].CRDTState) != "porig" {
		t.Fatalf("aliasing: Pane.CRDTState bled through Get copy: got %q want %q",
			string(got2.Windows[0].Panes[0].CRDTState), "porig")
	}
	if string(got2.Windows[0].CRDTState) != "worig" {
		t.Fatalf("aliasing: Window.CRDTState bled through Get copy: got %q want %q",
			string(got2.Windows[0].CRDTState), "worig")
	}
	if got2.Windows[0].Name != "win" {
		t.Fatalf("aliasing: Window.Name bled through Get copy: got %q want %q",
			got2.Windows[0].Name, "win")
	}
}

// TestManager_ListWindowsAreDeepCopied is the List-side analogue of the Windows
// aliasing proof: List() goes through the same copySession, so a caller mutating a
// nested pane field of a List() result must not corrupt the store either.
func TestManager_ListWindowsAreDeepCopied(t *testing.T) {
	mgr := NewManager(nil)
	s, err := mgr.Create(context.Background(), &CreateRequest{Owner: "carol", Backend: BackendTmux})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := mgr.Update(s.ID, func(sess *Session) {
		sess.Windows = []Window{{
			ID:    "w1",
			Panes: []Pane{{ID: "p1", Environment: map[string]string{"pk": "orig"}}},
		}}
	}); err != nil {
		t.Fatalf("Update seed: %v", err)
	}

	all := mgr.List()
	if len(all) != 1 {
		t.Fatalf("List() returned %d, want 1", len(all))
	}
	all[0].Windows[0].Panes[0].Environment["pk"] = "tampered"

	got, err := mgr.Get(s.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Windows[0].Panes[0].Environment["pk"] != "orig" {
		t.Fatalf("aliasing: mutating a List() result's Pane.Environment corrupted the store: got %q want %q",
			got.Windows[0].Panes[0].Environment["pk"], "orig")
	}
}

// TestManager_UpdateReturnWindowsAreDeepCopied proves the *Session returned by
// Update is also nested-isolated (Update returns copySession(s)). A caller mutating
// a nested pane field of the Update result must not corrupt the store.
func TestManager_UpdateReturnWindowsAreDeepCopied(t *testing.T) {
	mgr := NewManager(nil)
	s, err := mgr.Create(context.Background(), &CreateRequest{Owner: "dave", Backend: BackendTmux})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := mgr.Update(s.ID, func(sess *Session) {
		sess.Windows = []Window{{
			ID:        "w1",
			CRDTState: []byte("worig"),
			Panes:     []Pane{{ID: "p1", Environment: map[string]string{"pk": "orig"}}},
		}}
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Mutate the returned copy's nested state.
	updated.Windows[0].Panes[0].Environment["pk"] = "tampered"
	updated.Windows[0].CRDTState[0] = 'X'

	got, err := mgr.Get(s.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Windows[0].Panes[0].Environment["pk"] != "orig" {
		t.Fatalf("aliasing: mutating Update() return's Pane.Environment corrupted the store: got %q want %q",
			got.Windows[0].Panes[0].Environment["pk"], "orig")
	}
	if string(got.Windows[0].CRDTState) != "worig" {
		t.Fatalf("aliasing: mutating Update() return's Window.CRDTState corrupted the store: got %q want %q",
			string(got.Windows[0].CRDTState), "worig")
	}
}

// TestManager_ConcurrentGetMutate_RaceClean races many readers that each MUTATE
// their own nested copy against writers that Update the same session's window
// state, under -race. With the deep-copy fix, every reader owns isolated nested
// memory, so concurrent mutation of returned copies cannot race the stored
// session. Pre-fix (shared Pane.Environment/CRDTState), this races.
//
// SINK: conservation + no data race. The session always exists; readers mutate
// their isolated copies freely.
func TestManager_ConcurrentGetMutate_RaceClean(t *testing.T) {
	mgr := NewManager(nil)
	s, err := mgr.Create(context.Background(), &CreateRequest{Owner: "erin", Backend: BackendTmux})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := s.ID
	if _, err := mgr.Update(id, func(sess *Session) {
		sess.Windows = []Window{{
			ID:        "w1",
			CRDTState: []byte("worig"),
			Panes:     []Pane{{ID: "p1", Environment: map[string]string{"pk": "orig"}, CRDTState: []byte("porig")}},
		}}
	}); err != nil {
		t.Fatalf("Update seed: %v", err)
	}

	withTimeout(t, 30*time.Second, func() {
		var wg sync.WaitGroup
		stop := make(chan struct{})

		// Writers continuously rewrite the window state under the lock.
		for w := 0; w < 4; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
						_, _ = mgr.Update(id, func(sess *Session) {
							if len(sess.Windows) == 0 {
								return
							}
							sess.Windows[0].CRDTState = []byte("w")
							if len(sess.Windows[0].Panes) > 0 {
								sess.Windows[0].Panes[0].Environment["pk"] = "live"
							}
						})
					}
				}
			}(w)
		}

		// Readers Get and then MUTATE their isolated nested copy.
		var readers sync.WaitGroup
		for r := 0; r < 16; r++ {
			readers.Add(1)
			go func() {
				defer readers.Done()
				for i := 0; i < 500; i++ {
					got, err := mgr.Get(id)
					if err != nil {
						t.Errorf("Get: %v", err)
						return
					}
					if len(got.Windows) > 0 {
						if len(got.Windows[0].CRDTState) > 0 {
							got.Windows[0].CRDTState[0] = 'Z' // mutate isolated copy
						}
						if len(got.Windows[0].Panes) > 0 {
							got.Windows[0].Panes[0].Environment["mine"] = "x" // mutate isolated map
						}
					}
				}
			}()
		}
		readers.Wait()
		close(stop)
		wg.Wait()
	})

	// SINK: the session is still present and well-formed.
	if _, err := mgr.Get(id); err != nil {
		t.Fatalf("post-storm Get: %v", err)
	}
}
