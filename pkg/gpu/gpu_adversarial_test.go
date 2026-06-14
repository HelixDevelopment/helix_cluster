package gpu

import (
	"fmt"
	"sync"
	"testing"
)

// advAdapter is a local ProviderAdapter used by the adversarial probes. It is
// distinct from the test helper in package_test.go to keep these probes
// self-contained.
type advAdapter struct {
	name      string
	offerings []string
}

func (a *advAdapter) Name() string        { return a.name }
func (a *advAdapter) Offerings() []string { return a.offerings }

// TestListReturnsIsolatedCopy pins the documented List contract:
//
//	"The returned slice is a fresh copy, so callers may mutate it without
//	 affecting the Registry."
//
// SINK-SIDE: mutate the slice the caller received, then take a fresh List and
// prove the Registry's own view is unchanged (same length, same elements in
// the same registration order). If List ever returned r.order/r.adapters by
// reference (or a slice aliasing internal storage), the first mutation would
// corrupt the second listing — a lost/clobbered adapter visible to a pool
// layer. This is a conservation assertion, not a "no panic" check.
func TestListReturnsIsolatedCopy(t *testing.T) {
	r := NewRegistry()
	a := &advAdapter{name: "iso-a", offerings: []string{"o-a"}}
	b := &advAdapter{name: "iso-b", offerings: []string{"o-b"}}
	if err := r.Register(a); err != nil {
		t.Fatalf("Register(a): %v", err)
	}
	if err := r.Register(b); err != nil {
		t.Fatalf("Register(b): %v", err)
	}

	first := r.List()
	if len(first) != 2 {
		t.Fatalf("List len = %d; want 2", len(first))
	}
	// Adversarially clobber the caller's copy.
	for i := range first {
		first[i] = nil
	}

	second := r.List()
	if len(second) != 2 {
		t.Fatalf("after caller mutation, List len = %d; want 2 (caller mutation leaked into Registry)", len(second))
	}
	// Registration order is documented: a then b.
	if second[0] != ProviderAdapter(a) || second[1] != ProviderAdapter(b) {
		t.Fatalf("List = [%v %v]; want [a b] in registration order (internal slice was aliased/clobbered)", second[0], second[1])
	}
}

// TestConcurrentReadWriteRaceFree exercises the gap the existing suite leaves:
// the existing concurrency test races concurrent *writers* only. Here writers
// (Register) run CONCURRENTLY with readers (List, Names, Get, and the
// package-facing Adapters), so -race exercises the RLock/Lock interplay on the
// shared adapters map and order slice — not just writer-vs-writer.
//
// SINK-SIDE / CONSERVATION: after the storm, every adapter we registered must
// be retrievable, List length must equal the writer count, and Names must be
// complete. A read that observed a torn map/slice write (or a dropped entry
// under a write/read overlap) breaks these counts. -race clean AND the
// conservation count are both required to claim safety.
func TestConcurrentReadWriteRaceFree(t *testing.T) {
	r := NewRegistry()
	const writers = 300

	// Reader goroutines hammer every read path while writes are in flight.
	const readers = 8
	stop := make(chan struct{})
	var readWG sync.WaitGroup
	readWG.Add(readers)
	for i := 0; i < readers; i++ {
		go func() {
			defer readWG.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = r.List()
				_ = r.Names()
				_, _ = r.Get("rw-0001")
			}
		}()
	}

	// Writers register distinct adapters concurrently.
	var writeWG sync.WaitGroup
	writeWG.Add(writers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			defer writeWG.Done()
			name := fmt.Sprintf("rw-%04d", i)
			if err := r.Register(&advAdapter{name: name, offerings: []string{name}}); err != nil {
				t.Errorf("Register(%q): %v", name, err)
			}
		}(i)
	}

	// Once all writers are done, stop the readers and let them drain.
	writeWG.Wait()
	close(stop)
	readWG.Wait()

	// CONSERVATION: every writer's adapter survived concurrent reads.
	if l := r.List(); len(l) != writers {
		t.Fatalf("List len = %d; want %d (entry lost under concurrent read/write)", len(l), writers)
	}
	for i := 0; i < writers; i++ {
		name := fmt.Sprintf("rw-%04d", i)
		if _, ok := r.Get(name); !ok {
			t.Fatalf("Get(%q): not found after concurrent read/write", name)
		}
	}
	if n := len(r.Names()); n != writers {
		t.Fatalf("Names len = %d; want %d", n, writers)
	}
}

// TestRegistrationOrderDeterministic pins List/Adapters registration-order
// determinism against Names' lexicographic order. We register in a
// deliberately NON-lexicographic sequence and assert List preserves insertion
// order while Names re-sorts. A pool layer that enumerates via Adapters relies
// on this stable order; if List were ever backed by raw map iteration it would
// be nondeterministic and this would flake/fail.
func TestRegistrationOrderDeterministic(t *testing.T) {
	r := NewRegistry()
	seq := []string{"zeta", "alpha", "mike", "bravo"}
	for _, n := range seq {
		if err := r.Register(&advAdapter{name: n}); err != nil {
			t.Fatalf("Register(%q): %v", n, err)
		}
	}

	got := r.List()
	if len(got) != len(seq) {
		t.Fatalf("List len = %d; want %d", len(got), len(seq))
	}
	for i, n := range seq {
		if got[i].Name() != n {
			t.Fatalf("List[%d].Name() = %q; want %q (registration order not preserved)", i, got[i].Name(), n)
		}
	}

	names := r.Names()
	wantSorted := []string{"alpha", "bravo", "mike", "zeta"}
	for i := range wantSorted {
		if names[i] != wantSorted[i] {
			t.Fatalf("Names()[%d] = %q; want %q (must be lexicographically sorted)", i, names[i], wantSorted[i])
		}
	}
}
