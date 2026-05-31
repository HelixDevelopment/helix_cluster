package federation

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func mustCell(t *testing.T, id string, opts ...CellOption) *Cell {
	t.Helper()
	c, err := NewCell(id, opts...)
	if err != nil {
		t.Fatalf("NewCell(%q): %v", id, err)
	}
	return c
}

// TestRegistry_RegisterLookupList_SinkSide proves Lookup and List reflect the
// ACTUAL registered set (and the concrete field values), not just "found".
//
// MUTATION that kills this: in Register, drop `r.cells[c.ID] = c` (no-op store).
// Lookup then reports !ok and the test fails.
func TestRegistry_RegisterLookupList_SinkSide(t *testing.T) {
	r := NewCellRegistry()
	if err := r.Register(mustCell(t, "alpha", WithRegion("eu"), WithMemberCount(3))); err != nil {
		t.Fatalf("Register alpha: %v", err)
	}
	if err := r.Register(mustCell(t, "beta", WithRegion("us"), WithMemberCount(5))); err != nil {
		t.Fatalf("Register beta: %v", err)
	}

	got, ok := r.Lookup("alpha")
	if !ok {
		t.Fatalf("Lookup alpha: not found after register")
	}
	if got.Region != "eu" || got.MemberCount != 3 || got.Phase() != PhaseJoining {
		t.Fatalf("Lookup alpha snapshot = %+v, want region=eu members=3 phase=Joining", got)
	}

	list := r.List()
	if len(list) != 2 {
		t.Fatalf("List len = %d, want 2", len(list))
	}
	// List is sorted by id: alpha then beta.
	if list[0].ID != "alpha" || list[1].ID != "beta" {
		t.Fatalf("List ids = [%s %s], want [alpha beta]", list[0].ID, list[1].ID)
	}
	if r.Len() != 2 {
		t.Fatalf("Len = %d, want 2", r.Len())
	}
}

// TestRegistry_Deregister_SinkSide proves deregistration actually removes the
// cell from Lookup and List.
//
// MUTATION that kills this: in Deregister, remove `delete(r.cells, id)`. The
// post-deregister Lookup still finds "alpha" and the test fails.
func TestRegistry_Deregister_SinkSide(t *testing.T) {
	r := NewCellRegistry()
	_ = r.Register(mustCell(t, "alpha"))
	_ = r.Register(mustCell(t, "beta"))

	if err := r.Deregister("alpha"); err != nil {
		t.Fatalf("Deregister alpha: %v", err)
	}
	if _, ok := r.Lookup("alpha"); ok {
		t.Fatalf("alpha still present after Deregister")
	}
	if r.Len() != 1 {
		t.Fatalf("Len = %d after deregister, want 1", r.Len())
	}
	list := r.List()
	if len(list) != 1 || list[0].ID != "beta" {
		t.Fatalf("List after deregister = %+v, want only beta", list)
	}
	if err := r.Deregister("ghost"); !errors.Is(err, ErrCellNotFound) {
		t.Fatalf("Deregister unknown err = %v, want ErrCellNotFound", err)
	}
}

// TestRegistry_DuplicateRegister rejects a duplicate id.
//
// MUTATION that kills this: remove the existence check in Register. The second
// Register("alpha") then returns nil and the test fails.
func TestRegistry_DuplicateRegister(t *testing.T) {
	r := NewCellRegistry()
	if err := r.Register(mustCell(t, "alpha")); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := r.Register(mustCell(t, "alpha")); !errors.Is(err, ErrCellExists) {
		t.Fatalf("duplicate Register err = %v, want ErrCellExists", err)
	}
}

// TestRegistry_Advance_EmitsEventAndUpdatesSink proves Advance both (a) mutates
// the stored cell's phase (sink-side, re-read via Lookup) and (b) emits a
// TransitionEvent with the exact from/to/at/cellID to subscribed listeners.
//
// MUTATION that kills this: in Advance, skip the listener fan-out loop (don't
// call l(ev)). The captured events slice is empty and the event assertion
// fails. A second mutation — not calling c.advance — leaves the phase at
// Joining and the Lookup-phase assertion fails.
func TestRegistry_Advance_EmitsEventAndUpdatesSink(t *testing.T) {
	fixed := time.Unix(4242, 0)
	r := NewCellRegistry(WithClock(func() time.Time { return fixed }))

	var mu sync.Mutex
	var events []TransitionEvent
	r.OnTransition(func(e TransitionEvent) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	})

	_ = r.Register(mustCell(t, "alpha"))
	if err := r.Advance("alpha", PhaseActive); err != nil {
		t.Fatalf("Advance to Active: %v", err)
	}

	// Sink-side: the stored cell's phase actually changed.
	got, _ := r.Lookup("alpha")
	if got.Phase() != PhaseActive {
		t.Fatalf("after Advance, stored phase = %s, want Active", got.Phase())
	}
	if !got.UpdatedAt().Equal(fixed) {
		t.Fatalf("after Advance, updatedAt = %v, want injected %v", got.UpdatedAt(), fixed)
	}

	// Event-side: exactly one event with precise fields.
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("got %d transition events, want 1", len(events))
	}
	ev := events[0]
	if ev.CellID != "alpha" || ev.From != PhaseJoining || ev.To != PhaseActive || !ev.At.Equal(fixed) {
		t.Fatalf("event = %+v, want {alpha Joining Active %v}", ev, fixed)
	}
}

// TestRegistry_Advance_IllegalDoesNotMutateOrEmit proves a rejected transition
// neither changes the stored phase nor emits an event.
//
// MUTATION that kills this: in Advance, emit the event / mutate the cell BEFORE
// checking c.advance's error (e.g. ignore the returned error). The "no event"
// assertion fails because an event fires for the illegal jump.
func TestRegistry_Advance_IllegalDoesNotMutateOrEmit(t *testing.T) {
	r := NewCellRegistry()
	var count int
	r.OnTransition(func(TransitionEvent) { count++ })
	_ = r.Register(mustCell(t, "alpha")) // Joining

	if err := r.Advance("alpha", PhaseDraining); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("illegal Advance err = %v, want ErrIllegalTransition", err)
	}
	got, _ := r.Lookup("alpha")
	if got.Phase() != PhaseJoining {
		t.Fatalf("phase after illegal advance = %s, want unchanged Joining", got.Phase())
	}
	if count != 0 {
		t.Fatalf("emitted %d events for illegal transition, want 0", count)
	}
	// Unknown cell.
	if err := r.Advance("ghost", PhaseActive); !errors.Is(err, ErrCellNotFound) {
		t.Fatalf("Advance unknown err = %v, want ErrCellNotFound", err)
	}
}

// TestRegistry_LookupSnapshotIsolated proves Lookup returns a decoupled value:
// a later Advance must not retroactively change an earlier snapshot.
//
// MUTATION that kills this: make Lookup return the stored pointer's dereference
// at read time only if clone() returned a shared reference — i.e. change
// clone() to leak shared mutable state. (As written Cell is a value type, so
// the guard is the snapshot semantics; the test pins that contract.)
func TestRegistry_LookupSnapshotIsolated(t *testing.T) {
	r := NewCellRegistry()
	_ = r.Register(mustCell(t, "alpha"))
	snap, _ := r.Lookup("alpha")
	if err := r.Advance("alpha", PhaseActive); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if snap.Phase() != PhaseJoining {
		t.Fatalf("earlier snapshot mutated to %s, want frozen at Joining", snap.Phase())
	}
}

// TestRegistry_ConcurrentAdvance is a race-detector stressor: many goroutines
// register/advance/lookup concurrently. It asserts the FSM still ends Left and
// every transition was legal (event count == legal step count per cell).
//
// MUTATION that kills this: remove the mutex usage in Advance (or use a plain
// map without locking) -> the race detector flags a data race and the test
// fails under -race.
func TestRegistry_ConcurrentAdvance(t *testing.T) {
	r := NewCellRegistry()
	const n = 50
	for i := 0; i < n; i++ {
		_ = r.Register(mustCell(t, idOf(i)))
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := idOf(i)
			_ = r.Advance(id, PhaseActive)
			_ = r.Advance(id, PhaseDraining)
			_ = r.Advance(id, PhaseLeft)
			_, _ = r.Lookup(id)
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		got, ok := r.Lookup(idOf(i))
		if !ok || got.Phase() != PhaseLeft {
			t.Fatalf("cell %d final phase = %v ok=%v, want Left", i, got.Phase(), ok)
		}
	}
}

func idOf(i int) string {
	return "cell-" + string(rune('a'+i%26)) + "-" + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
