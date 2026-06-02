package hlc

import (
	"testing"
	"time"
)

// TestDriftClamp_FarFutureRemoteIsClamped is the primary anti-bluff test for
// the drift clamp introduced in HXC-1334. A remote timestamp whose physical
// component is 10×maxDrift ahead of the local wall-clock must NOT advance the
// clock past physicalNow+maxDrift.
//
// Anti-bluff mutation: remove the clamping block in Update (set remotePhys =
// remote.Physical unconditionally). Then Last().Physical becomes T+10*maxDrift,
// which is > T+maxDrift, and the assertion fails.
func TestDriftClamp_FarFutureRemoteIsClamped(t *testing.T) {
	const T int64 = 1_000_000_000 // arbitrary fixed local physical time (1 s)
	clk := &stubClock{ns: T}
	maxDrift := 50 * time.Millisecond
	c := NewWithMaxDrift(clk.now, maxDrift)

	// Remote is 10× maxDrift ahead of local wall-clock — clearly malicious or
	// severely misconfigured.
	farFuture := Timestamp{Physical: T + 10*int64(maxDrift), Logical: 0}
	merged := c.Update(farFuture)

	ceiling := T + int64(maxDrift)
	if merged.Physical > ceiling {
		t.Fatalf("clamp failed: merged.Physical=%d > T+maxDrift=%d (remote physical=%d)",
			merged.Physical, ceiling, farFuture.Physical)
	}

	// Also verify via Last() — must not have permanently poisoned the clock.
	last := c.Last()
	if last.Physical > ceiling {
		t.Fatalf("clock poisoned: Last().Physical=%d > T+maxDrift=%d",
			last.Physical, ceiling)
	}
}

// TestDriftClamp_InDriftRemoteNotClamped asserts the clamp does NOT fire for a
// remote that is only maxDrift/2 ahead — a normal, well-behaved peer whose
// clock is slightly ahead. The merged physical should equal the remote physical
// (which is larger than local), and the causal bump must still hold.
//
// Anti-bluff mutation: set the clamping ceiling to physicalNow (maxDrift=0
// effectively) — the previously-unclamped remote would be clamped, causing
// merged.Physical < remote.Physical and the assertion fails.
func TestDriftClamp_InDriftRemoteNotClamped(t *testing.T) {
	const T int64 = 2_000_000_000
	clk := &stubClock{ns: T}
	maxDrift := 100 * time.Millisecond
	c := NewWithMaxDrift(clk.now, maxDrift)

	halfDrift := int64(maxDrift) / 2
	normalRemote := Timestamp{Physical: T + halfDrift, Logical: 3}
	merged := c.Update(normalRemote)

	// Remote physical is within drift window — must NOT be clamped.
	if merged.Physical != T+halfDrift {
		t.Fatalf("in-drift remote was incorrectly clamped: merged.Physical=%d, want %d",
			merged.Physical, T+halfDrift)
	}
	// Causality: merged must still be strictly greater than the remote.
	if !normalRemote.Less(merged) {
		t.Fatalf("causality violated: merged %+v must be > remote %+v", merged, normalRemote)
	}
}

// TestDriftClamp_MonotonicityAfterClamp asserts that after a far-future remote
// is clamped, subsequent Now() and Update() calls are still strictly
// monotonically increasing (the clamp must not break the HLC invariant).
func TestDriftClamp_MonotonicityAfterClamp(t *testing.T) {
	const T int64 = 500_000_000
	clk := &stubClock{ns: T}
	maxDrift := 20 * time.Millisecond
	c := NewWithMaxDrift(clk.now, maxDrift)

	farFuture := Timestamp{Physical: T + 10*int64(maxDrift), Logical: 0}
	after := c.Update(farFuture)

	// Now() after the clamped Update must be strictly greater.
	next := c.Now()
	if !after.Less(next) {
		t.Fatalf("not monotonic after clamp: %+v !< %+v", after, next)
	}

	// Another Update with a normal remote must also be strictly greater.
	normalRemote := Timestamp{Physical: T + int64(maxDrift)/2, Logical: 0}
	next2 := c.Update(normalRemote)
	if !next.Less(next2) {
		t.Fatalf("not monotonic on subsequent Update after clamp: %+v !< %+v", next, next2)
	}
}

// TestNewWithMaxDrift_PanicsOnNilNow ensures the safety invariant mirrors New.
func TestNewWithMaxDrift_PanicsOnNilNow(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic when now is nil in NewWithMaxDrift")
		}
	}()
	NewWithMaxDrift(nil, 100*time.Millisecond)
}

// TestDefaultMaxDrift_ExportedAndReasonable asserts the exported constant is
// non-zero and is exactly 500 ms, as documented.
func TestDefaultMaxDrift_ExportedAndReasonable(t *testing.T) {
	if DefaultMaxDrift <= 0 {
		t.Fatalf("DefaultMaxDrift must be positive, got %v", DefaultMaxDrift)
	}
	want := 500 * time.Millisecond
	if DefaultMaxDrift != want {
		t.Fatalf("DefaultMaxDrift = %v, want %v", DefaultMaxDrift, want)
	}
}

// TestNew_UsesDefaultMaxDrift confirms that New() (without explicit maxDrift)
// still clamps far-future remotes using DefaultMaxDrift. This ensures backward-
// compatible protection without any caller change.
func TestNew_UsesDefaultMaxDrift(t *testing.T) {
	const T int64 = 1_000_000_000
	clk := &stubClock{ns: T}
	c := New(clk.now)

	farFuture := Timestamp{Physical: T + 10*int64(DefaultMaxDrift), Logical: 0}
	merged := c.Update(farFuture)

	ceiling := T + int64(DefaultMaxDrift)
	if merged.Physical > ceiling {
		t.Fatalf("New() clamp failed: merged.Physical=%d > T+DefaultMaxDrift=%d",
			merged.Physical, ceiling)
	}
}
