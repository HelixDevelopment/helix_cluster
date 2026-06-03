package gpu

import (
	"crypto/rand"
	"fmt"
	"sort"
	"sync"
	"testing"
)

// fakeAdapter is a minimal ProviderAdapter used to prove the registration hook
// end to end. It carries a run-UUID so the test can assert the *same* object
// it registered surfaces through the listing — not merely that some adapter
// with a matching name exists.
type fakeAdapter struct {
	name      string
	offerings []string
}

func (f *fakeAdapter) Name() string        { return f.name }
func (f *fakeAdapter) Offerings() []string { return f.offerings }

// runUUID returns a fresh random hex identity so each test run tags its fake
// adapter uniquely; sink-side assertions key off this value.
func runUUID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return fmt.Sprintf("%x", b[:])
}

// TestRegisterSurfacesThroughListing proves that an adapter registered through
// the pkg/gpu hook is observable, by the SAME identity, via List, Get, and the
// pool-facing exported accessors (Adapters / Lookup). Mutation bite: deleting
// `r.adapters[name] = a` in Register makes the List/Get/Adapters assertions
// here fail.
func TestRegisterSurfacesThroughListing(t *testing.T) {
	id := runUUID(t)
	adapter := &fakeAdapter{name: "fake-" + id, offerings: []string{"nvidia-a100-" + id}}

	// Use the process-wide default registry, which is what a pool layer would
	// consume via Adapters/Lookup.
	if err := Register(adapter); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	// Sink-side via Lookup (the pool-facing single-name accessor).
	got, ok := Lookup(adapter.Name())
	if !ok {
		t.Fatalf("Lookup(%q) = not found; want found", adapter.Name())
	}
	if got != ProviderAdapter(adapter) {
		t.Fatalf("Lookup returned a different object than was registered")
	}
	// Prove the run-UUID-tagged payload round-tripped (real value, not just non-nil).
	if offs := got.Offerings(); len(offs) != 1 || offs[0] != "nvidia-a100-"+id {
		t.Fatalf("Offerings = %v; want [nvidia-a100-%s]", offs, id)
	}

	// Sink-side via Adapters (the pool-facing listing accessor).
	if !containsAdapter(Adapters(), adapter) {
		t.Fatalf("Adapters() does not contain the registered adapter %q", adapter.Name())
	}

	// And via the underlying default registry's List/Get directly.
	if !containsAdapter(DefaultRegistry().List(), adapter) {
		t.Fatalf("DefaultRegistry().List() does not contain %q", adapter.Name())
	}
	if g, ok := DefaultRegistry().Get(adapter.Name()); !ok || g != ProviderAdapter(adapter) {
		t.Fatalf("DefaultRegistry().Get(%q) = (%v,%v); want the registered adapter", adapter.Name(), g, ok)
	}
}

// TestDuplicateRegistrationRejected proves a second Register under the same
// name fails and does not clobber the first registration. Mutation bite:
// removing the `if _, exists := r.adapters[name]; exists { return ... }` guard
// in Register makes this test fail (err would be nil).
func TestDuplicateRegistrationRejected(t *testing.T) {
	r := NewRegistry()
	id := runUUID(t)
	first := &fakeAdapter{name: "dup-" + id, offerings: []string{"first-" + id}}
	second := &fakeAdapter{name: "dup-" + id, offerings: []string{"second-" + id}}

	if err := r.Register(first); err != nil {
		t.Fatalf("first Register: unexpected error %v", err)
	}
	if err := r.Register(second); err == nil {
		t.Fatalf("second Register: got nil error; want duplicate-name rejection")
	}

	// The original registration must be intact (not overwritten by `second`).
	got, ok := r.Get("dup-" + id)
	if !ok {
		t.Fatalf("Get after duplicate attempt: not found")
	}
	if got != ProviderAdapter(first) {
		t.Fatalf("duplicate Register clobbered the original adapter")
	}
	if l := r.List(); len(l) != 1 {
		t.Fatalf("List len = %d; want 1 (duplicate must not add an entry)", len(l))
	}
}

// TestRegisterRejectsNilAndEmptyName proves the input guards. Mutation bite:
// removing the empty-name guard in Register makes the empty-name sub-case fail.
func TestRegisterRejectsNilAndEmptyName(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(nil); err == nil {
		t.Fatalf("Register(nil): got nil error; want rejection")
	}
	if err := r.Register(&fakeAdapter{name: ""}); err == nil {
		t.Fatalf("Register(empty name): got nil error; want rejection")
	}
	if l := r.List(); len(l) != 0 {
		t.Fatalf("List len = %d; want 0 after rejected registrations", len(l))
	}
}

// TestConcurrentRegistrationRaceFree registers many distinct adapters from many
// goroutines concurrently and asserts every one is present afterwards. Run
// under -race, this proves the mutex actually guards adapters/order. Mutation
// bite: dropping the `r.mu.Lock()`/`defer r.mu.Unlock()` in Register makes the
// race detector flag a data race (and/or the count assertion fail) here.
func TestConcurrentRegistrationRaceFree(t *testing.T) {
	r := NewRegistry()
	const n = 200
	id := runUUID(t)

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("c-%s-%04d", id, i)
			if err := r.Register(&fakeAdapter{name: name, offerings: []string{name}}); err != nil {
				t.Errorf("Register(%q): %v", name, err)
			}
		}(i)
	}
	wg.Wait()

	if l := r.List(); len(l) != n {
		t.Fatalf("List len = %d; want %d after concurrent registration", len(l), n)
	}

	// Every expected name must be retrievable, and Names() must be sorted+complete.
	want := make([]string, 0, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("c-%s-%04d", id, i)
		want = append(want, name)
		if _, ok := r.Get(name); !ok {
			t.Fatalf("Get(%q): not found after concurrent registration", name)
		}
	}
	sort.Strings(want)
	got := r.Names()
	if len(got) != len(want) {
		t.Fatalf("Names len = %d; want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Names()[%d] = %q; want %q (must be sorted+complete)", i, got[i], want[i])
		}
	}
}

func containsAdapter(list []ProviderAdapter, want ProviderAdapter) bool {
	for _, a := range list {
		if a == want {
			return true
		}
	}
	return false
}
