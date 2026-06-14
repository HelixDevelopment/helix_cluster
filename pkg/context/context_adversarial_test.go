package context

import (
	stdctx "context"
	"sync"
	"testing"
	"time"
)

// advKey is a distinct, unexported key type so these adversarial probes cannot
// collide with the test file's ctxKey or any production key.
type advKey string

// ============================================================================
// (a) VALUE FIDELITY / KEY COLLISION
// ============================================================================

// TestAdv_Detach_ValueFidelityAtDepth proves that a value set on a deeply nested
// parent chain survives Detach and is returned EXACTLY (sink-side: the retrieved
// value), and that two distinct keys do not collide.
func TestAdv_Detach_ValueFidelityAtDepth(t *testing.T) {
	const kA advKey = "tenant"
	const kB advKey = "role"
	const wantA = "tenant-7"
	const wantB = "admin"

	base := stdctx.Background()
	base = stdctx.WithValue(base, kA, wantA)
	// intervening derivations between the two values (depth + a cancel layer)
	mid, cancel := stdctx.WithCancel(base)
	defer cancel()
	mid = stdctx.WithValue(mid, kB, wantB)

	d := Detach(mid)

	if got := d.Value(kA); got != wantA {
		t.Fatalf("value A lost/wrong through Detach: got %v (%T), want %q", got, got, wantA)
	}
	if got := d.Value(kB); got != wantB {
		t.Fatalf("value B lost/wrong through Detach: got %v (%T), want %q", got, got, wantB)
	}
	// Distinct keys must not collide: A's value must not leak under B's key.
	if d.Value(kA) == d.Value(kB) {
		t.Fatalf("key collision: distinct keys returned same value %v", d.Value(kA))
	}
	// A genuinely-absent key must return nil (no fail-open default masking).
	if got := d.Value(advKey("never-set")); got != nil {
		t.Fatalf("absent key returned non-nil default %v (fail-open)", got)
	}
}

// TestAdv_Detach_StringKeyVsTypedKey proves the same string literal under two
// different key TYPES does not collide (Go's value-key type identity).
func TestAdv_Detach_StringKeyVsTypedKey(t *testing.T) {
	type otherKey string
	const lit = "id"
	parent := stdctx.WithValue(stdctx.Background(), advKey(lit), "from-advKey")
	parent = stdctx.WithValue(parent, otherKey(lit), "from-otherKey")
	d := Detach(parent)

	if got := d.Value(advKey(lit)); got != "from-advKey" {
		t.Errorf("typed-key confusion: advKey(%q) = %v, want from-advKey", lit, got)
	}
	if got := d.Value(otherKey(lit)); got != "from-otherKey" {
		t.Errorf("typed-key confusion: otherKey(%q) = %v, want from-otherKey", lit, got)
	}
}

// ============================================================================
// (b) PROPAGATION / IMMUTABILITY
// ============================================================================

// TestAdv_Detach_ParentImmutable proves Detach does not corrupt the parent: the
// parent's own cancellation and value lookups continue to behave normally after
// a detached child is derived from it.
func TestAdv_Detach_ParentImmutable(t *testing.T) {
	const k advKey = "x"
	parent, cancel := stdctx.WithCancel(stdctx.WithValue(stdctx.Background(), k, "v"))
	d := Detach(parent)
	_ = d

	// Parent value still readable.
	if got := parent.Value(k); got != "v" {
		t.Fatalf("Detach corrupted parent value: got %v", got)
	}
	// Parent cancellation still works (detach must not have neutered the parent).
	cancel()
	select {
	case <-parent.Done():
		if parent.Err() != stdctx.Canceled {
			t.Errorf("parent Err() = %v, want Canceled", parent.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("Detach neutered parent cancellation")
	}
	// And the detached child must NOT have followed the parent into cancel.
	if d.Err() != nil {
		t.Errorf("detached child cancelled with parent: Err()=%v", d.Err())
	}
}

// ============================================================================
// (c) UNGUARDED SHARED STATE — concurrent value lookups under -race
// ============================================================================

func TestAdv_Detach_ConcurrentValueLookup(t *testing.T) {
	const k advKey = "shared"
	parent := stdctx.WithValue(stdctx.Background(), k, "value")
	d := Detach(parent)

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				if got := d.Value(k); got != "value" {
					t.Errorf("concurrent lookup got %v", got)
					return
				}
				_, _ = d.Deadline()
				_ = d.Err()
				select {
				case <-d.Done():
				default:
				}
			}
		}()
	}
	wg.Wait()
}

// ============================================================================
// (d) THE CONTRACT BITE: "never cancelled" vs a leaked, already-expired Deadline
// ============================================================================
//
// package.go:14 — Detach "returns a context that is never cancelled, keeping only
// values." The implementation embeds the parent Context and overrides Done()->nil
// and Err()->nil, but does NOT override Deadline(). Therefore a detached context
// derived from a parent that carries a deadline still REPORTS that (possibly
// already-expired) deadline via Deadline(), while Done() never fires and Err() is
// nil. That is a self-contradictory, fail-open state for any deadline-aware caller
// (gRPC/http propagate the wire deadline by reading Deadline()): they will see an
// expired deadline on a context that is, by contract, never cancelled — and either
// fail-fast locally or forward an already-dead deadline downstream, silently
// defeating Detach.
//
// Sink-side assertion: ok must be false (no deadline) for a context that is never
// cancelled. This is the canonical context.WithoutCancel semantics.
func TestAdv_Detach_NeverCancelled_NoLeakedDeadline(t *testing.T) {
	// Parent already past its deadline.
	parent, cancel := stdctx.WithDeadline(stdctx.Background(), time.Now().Add(-1*time.Hour))
	defer cancel()

	d := Detach(parent)

	// Cross-check the three signals an executor reads to decide "how long do I have":
	dl, ok := d.Deadline()
	err := d.Err()

	doneFired := false
	select {
	case <-d.Done():
		doneFired = true
	default:
	}

	// Contract: "never cancelled, keeping only values."
	// Err()==nil and Done() not fired are the live behavior (good).
	if err != nil {
		t.Errorf("detached Err() = %v, want nil (never cancelled)", err)
	}
	if doneFired {
		t.Errorf("detached Done() fired; context must never be cancelled")
	}

	// SINK-SIDE BITE: a never-cancelled context must NOT advertise a deadline,
	// least of all one already in the past. If it does, a deadline-aware caller
	// computes a non-positive budget and fail-fasts on a context that will in
	// fact never expire.
	if ok {
		t.Errorf("detached context leaked a deadline: Deadline() ok=true dl=%v (in the past=%v) "+
			"while Err()=nil and Done() never fires — a never-cancelled context must report ok=false",
			dl, dl.Before(time.Now()))
	}
}
