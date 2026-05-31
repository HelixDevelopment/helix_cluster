package hlc

import (
	"sync"
	"testing"
	"time"
)

// stubClock is a deterministic, mutable physical clock for tests.
type stubClock struct {
	mu sync.Mutex
	ns int64
}

func (s *stubClock) now() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Unix(0, s.ns)
}

func (s *stubClock) set(ns int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ns = ns
}

// TestNew_PanicsOnNilNow proves the injection seam is mandatory.
// Mutation: change `if now == nil { panic(...) }` to `if false {` -> no panic,
// test fails.
func TestNew_PanicsOnNilNow(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic when now is nil")
		}
	}()
	New(nil)
}

// TestNow_AdvancesWithPhysicalClock asserts the physical component is adopted
// from the injected clock and the logical counter resets when time advances.
// Mutation: in Now(), replace `Timestamp{Physical: phys, Logical: 0}` with
// `Timestamp{Physical: c.last.Physical, Logical: 0}` -> physical never tracks
// the injected clock, assertion ts.Physical == 5000 fails.
func TestNow_AdvancesWithPhysicalClock(t *testing.T) {
	clk := &stubClock{ns: 1000}
	c := New(clk.now)

	t1 := c.Now()
	if t1.Physical != 1000 || t1.Logical != 0 {
		t.Fatalf("t1 = %+v, want {1000 0}", t1)
	}
	clk.set(5000)
	t2 := c.Now()
	if t2.Physical != 5000 {
		t.Fatalf("t2.Physical = %d, want 5000", t2.Physical)
	}
	if t2.Logical != 0 {
		t.Fatalf("t2.Logical = %d, want 0 (reset on physical advance)", t2.Logical)
	}
	if !t1.Less(t2) {
		t.Fatalf("expected t1 < t2 (%+v vs %+v)", t1, t2)
	}
}

// TestNow_StrictlyMonotonic_FrozenClock is the core monotonicity law: with a
// frozen physical clock, the logical counter must compensate so each call is
// strictly greater.
// Mutation: replace the else branch logical bump
// `Logical: c.last.Logical + 1` with `Logical: c.last.Logical` -> consecutive
// timestamps become equal, ti.Less(tnext) fails.
func TestNow_StrictlyMonotonic_FrozenClock(t *testing.T) {
	clk := &stubClock{ns: 42}
	c := New(clk.now)

	prev := c.Now()
	for i := 0; i < 1000; i++ {
		next := c.Now()
		if !prev.Less(next) {
			t.Fatalf("not strictly monotonic at i=%d: %+v !< %+v", i, prev, next)
		}
		if next.Physical != 42 {
			t.Fatalf("frozen physical changed: %d", next.Physical)
		}
		prev = next
	}
	// After 1001 frozen calls the logical counter must have counted them.
	if prev.Logical != 1000 {
		t.Fatalf("logical = %d, want 1000", prev.Logical)
	}
}

// TestNow_StrictlyMonotonic_RegressingClock asserts monotonicity survives the
// physical clock running BACKWARDS, which is the central HLC guarantee.
// Mutation: in Now(), change the condition `if phys > c.last.Physical` to
// `if true` -> a regressing clock overwrites with a smaller physical and zero
// logical, producing ts2 < ts1; the ts1.Less(ts2) assertion fails.
func TestNow_StrictlyMonotonic_RegressingClock(t *testing.T) {
	clk := &stubClock{ns: 1_000_000}
	c := New(clk.now)

	ts1 := c.Now()
	// Physical clock jumps backwards (NTP step, leap, VM migration).
	clk.set(1)
	ts2 := c.Now()

	if !ts1.Less(ts2) {
		t.Fatalf("regression broke monotonicity: ts1=%+v ts2=%+v", ts1, ts2)
	}
	if ts2.Physical != ts1.Physical {
		t.Fatalf("ts2.Physical = %d, want %d (must keep larger past physical)",
			ts2.Physical, ts1.Physical)
	}
	if ts2.Logical != ts1.Logical+1 {
		t.Fatalf("ts2.Logical = %d, want %d", ts2.Logical, ts1.Logical+1)
	}
}

// TestUpdate_Causality_RemoteAhead is the causality law: a node receives a
// message whose physical time is AHEAD, then performs a local event; the local
// timestamp must be strictly greater than the remote one (remote -> local).
// Mutation (the one named in the task): drop the logical bump in the
// `case maxPhys == remote.Physical` branch — set `logical = remote.Logical`
// instead of `remote.Logical + 1`. Then local == remote, so
// merged.Less-checks: merged > remote fails (remote.Less(merged) == false).
func TestUpdate_Causality_RemoteAhead(t *testing.T) {
	clk := &stubClock{ns: 100} // local physical clock is BEHIND remote
	c := New(clk.now)

	remote := Timestamp{Physical: 10_000, Logical: 7}
	merged := c.Update(remote)

	if merged.Physical != 10_000 {
		t.Fatalf("merged.Physical = %d, want 10000 (adopt remote max)", merged.Physical)
	}
	if !remote.Less(merged) {
		t.Fatalf("causality violated: merged %+v must be > remote %+v", merged, remote)
	}
	if merged.Logical != remote.Logical+1 {
		t.Fatalf("merged.Logical = %d, want %d", merged.Logical, remote.Logical+1)
	}

	// And a subsequent local event must still be strictly greater.
	next := c.Now()
	if !merged.Less(next) {
		t.Fatalf("post-update local event not monotonic: %+v !< %+v", merged, next)
	}
}

// TestUpdate_Causality_BothSamePhysical exercises the branch where the prior
// local timestamp and the remote share the winning physical time; the merged
// logical must exceed BOTH.
// Mutation: in the both-equal branch, remove the `logical++` -> merged equals
// the larger of the two inputs, so one of the strict-greater assertions fails.
func TestUpdate_Causality_BothSamePhysical(t *testing.T) {
	clk := &stubClock{ns: 500}
	c := New(clk.now)

	local := c.Now() // {500, 0}
	// Force a higher local logical so the "max of two logicals" matters.
	c.Update(Timestamp{Physical: 500, Logical: 3}) // last -> {500, 4}
	local = c.Last()
	if local.Physical != 500 || local.Logical != 4 {
		t.Fatalf("setup local = %+v, want {500 4}", local)
	}

	remote := Timestamp{Physical: 500, Logical: 9}
	merged := c.Update(remote)

	if merged.Physical != 500 {
		t.Fatalf("merged.Physical = %d, want 500", merged.Physical)
	}
	if !local.Less(merged) {
		t.Fatalf("merged %+v must exceed prior local %+v", merged, local)
	}
	if !remote.Less(merged) {
		t.Fatalf("merged %+v must exceed remote %+v", merged, remote)
	}
	if merged.Logical != 10 { // max(4,9)+1
		t.Fatalf("merged.Logical = %d, want 10", merged.Logical)
	}
}

// TestUpdate_LocalAhead covers the branch where the local physical clock leads
// both the prior timestamp and remote: physical advances, logical resets to 0.
// Mutation: change the default branch `logical = 0` to `logical = remote.Logical`
// -> the assertion merged.Logical == 0 fails.
func TestUpdate_LocalAhead(t *testing.T) {
	clk := &stubClock{ns: 9000}
	c := New(clk.now)

	remote := Timestamp{Physical: 1000, Logical: 42}
	merged := c.Update(remote)
	if merged.Physical != 9000 {
		t.Fatalf("merged.Physical = %d, want 9000", merged.Physical)
	}
	if merged.Logical != 0 {
		t.Fatalf("merged.Logical = %d, want 0 (physical strictly advanced)", merged.Logical)
	}
}

// TestCompare_EqualPhysicalOrdersByLogical asserts the tie-break in the total
// order is the logical counter.
// Mutation: in Compare, remove the two logical comparison cases (return 0 when
// physical is equal) -> a{...,1} and a{...,2} compare equal, Less assertion
// fails.
func TestCompare_EqualPhysicalOrdersByLogical(t *testing.T) {
	a := Timestamp{Physical: 7, Logical: 1}
	b := Timestamp{Physical: 7, Logical: 2}

	if a.Compare(b) != -1 {
		t.Fatalf("a.Compare(b) = %d, want -1", a.Compare(b))
	}
	if b.Compare(a) != 1 {
		t.Fatalf("b.Compare(a) = %d, want 1", b.Compare(a))
	}
	if !a.Less(b) {
		t.Fatalf("expected a < b for equal physical, lower logical")
	}
	if a.Equal(b) {
		t.Fatal("a and b must not be Equal")
	}
	same := Timestamp{Physical: 7, Logical: 1}
	if !a.Equal(same) || a.Compare(same) != 0 {
		t.Fatalf("a must equal an identical timestamp")
	}
}

// TestCompare_PhysicalDominates asserts physical is the primary key: a larger
// physical wins even with a smaller logical.
// Mutation: in Compare swap the first two cases so larger physical returns -1
// -> assertion big.Compare(small) == 1 fails.
func TestCompare_PhysicalDominates(t *testing.T) {
	small := Timestamp{Physical: 5, Logical: 999}
	big := Timestamp{Physical: 6, Logical: 0}
	if big.Compare(small) != 1 {
		t.Fatalf("bigger physical must compare greater regardless of logical")
	}
	if !small.Less(big) {
		t.Fatalf("expected small < big")
	}
}

// TestEncodeDecode_RoundTrip asserts encode/decode is an exact inverse over a
// spread of values including extremes.
// Mutation: in Decode, change `binary.BigEndian.Uint32(b[8:12])` to a constant
// like `0` -> the logical component is lost, round-trip equality fails.
func TestEncodeDecode_RoundTrip(t *testing.T) {
	cases := []Timestamp{
		{},
		{Physical: 1, Logical: 0},
		{Physical: 0, Logical: 1},
		{Physical: 1_700_000_000_000_000_000, Logical: 65535},
		{Physical: 9_223_372_036_854_775_807, Logical: 4_294_967_295}, // max int64, max uint32
		{Physical: 123456789, Logical: 42},
	}
	for _, want := range cases {
		b := want.Encode()
		if len(b) != encodedLen {
			t.Fatalf("Encode(%+v) len = %d, want %d", want, len(b), encodedLen)
		}
		got, err := Decode(b)
		if err != nil {
			t.Fatalf("Decode error for %+v: %v", want, err)
		}
		if !got.Equal(want) || got != want {
			t.Fatalf("round-trip mismatch: got %+v, want %+v", got, want)
		}
	}
}

// TestEncode_ByteOrderMatchesTotalOrder asserts the big-endian encoding sorts
// lexicographically in the same order as Compare — a real property relied on
// when timestamps are used as storage keys.
// Mutation: switch Encode to binary.LittleEndian -> lexicographic byte order no
// longer matches numeric order, the assertion fails.
func TestEncode_ByteOrderMatchesTotalOrder(t *testing.T) {
	pairs := [][2]Timestamp{
		{{Physical: 1, Logical: 0}, {Physical: 2, Logical: 0}},
		{{Physical: 7, Logical: 1}, {Physical: 7, Logical: 2}},
		{{Physical: 0, Logical: 0}, {Physical: 0, Logical: 1}},
	}
	for _, p := range pairs {
		lo, hi := p[0], p[1]
		if !lo.Less(hi) {
			t.Fatalf("test setup wrong: %+v should be < %+v", lo, hi)
		}
		bl, bh := lo.Encode(), hi.Encode()
		if !lexLess(bl, bh) {
			t.Fatalf("byte order mismatch: %v not lexicographically < %v (ts %+v,%+v)",
				bl, bh, lo, hi)
		}
	}
}

func lexLess(a, b []byte) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// TestDecode_RejectsBadLength asserts Decode validates its input rather than
// silently truncating or panicking.
// Mutation: change `if len(b) != encodedLen` to `if false` -> Decode indexes
// out of range and panics on a short buffer; the recover-less call fails.
func TestDecode_RejectsBadLength(t *testing.T) {
	for _, b := range [][]byte{nil, {}, make([]byte, 11), make([]byte, 13)} {
		if _, err := Decode(b); err == nil {
			t.Fatalf("Decode(len=%d) = nil error, want ErrShortBuffer", len(b))
		}
	}
}

// TestCRDT_Style_MergeAssociativeMonotone is a multi-replica causal scenario:
// node A emits e1; B receives e1 and emits e2; A receives e2 and emits e3. The
// HLC must reflect the causal chain e1 -> e2 -> e3 with a strictly increasing
// total order across nodes, the distributed-primitive law this clock exists to
// uphold.
// Mutation: drop the logical bump in Update's remote-ahead branch (same as the
// task's named mutation) -> the cross-node chain stops being strictly
// increasing and e1.Less(e2) or e2.Less(e3) fails.
func TestCRDT_Style_MergeAssociativeMonotone(t *testing.T) {
	clkA := &stubClock{ns: 100}
	clkB := &stubClock{ns: 50} // B is behind A
	a := New(clkA.now)
	b := New(clkB.now)

	e1 := a.Now()      // {100,0}
	e2 := b.Update(e1) // B adopts 100, bumps logical -> {100,1}
	clkA.set(80)       // A's clock even regresses before receiving e2
	e3 := a.Update(e2) // A must still go strictly past e2

	if !e1.Less(e2) {
		t.Fatalf("e1 !< e2: %+v %+v", e1, e2)
	}
	if !e2.Less(e3) {
		t.Fatalf("e2 !< e3: %+v %+v", e2, e3)
	}
	if !e1.Less(e3) { // transitivity of the causal chain
		t.Fatalf("e1 !< e3: %+v %+v", e1, e3)
	}
}

// TestConcurrent_Monotonic runs Now/Update concurrently under -race and asserts
// every emitted timestamp is unique and the data structure stays consistent.
// Mutation: remove the mutex Lock/Unlock in Now -> the race detector reports a
// data race and/or duplicate timestamps appear, failing the uniqueness check.
func TestConcurrent_Monotonic(t *testing.T) {
	clk := &stubClock{ns: 1}
	c := New(clk.now)

	const goroutines = 8
	const perG = 500

	var mu sync.Mutex
	seen := make(map[Timestamp]struct{}, goroutines*perG)

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				ts := c.Now()
				mu.Lock()
				if _, dup := seen[ts]; dup {
					mu.Unlock()
					t.Errorf("duplicate timestamp emitted: %+v", ts)
					return
				}
				seen[ts] = struct{}{}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(seen) != goroutines*perG {
		t.Fatalf("expected %d unique timestamps, got %d", goroutines*perG, len(seen))
	}
}
