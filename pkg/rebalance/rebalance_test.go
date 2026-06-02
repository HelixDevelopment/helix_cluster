package rebalance

import (
	"sort"
	"testing"
)

const runUUID = "rebalance-test-3f7a9c21-HXC-1416"

// loadSlice returns sorted member loads for stable assertion/logging.
func loadSlice(a Assignment) []int {
	ls := make([]int, 0, len(a))
	for _, parts := range a {
		ls = append(ls, len(parts))
	}
	sort.Ints(ls)
	return ls
}

func maxMinSpread(a Assignment) (int, int) {
	mn, mx := 1<<30, -1
	for _, parts := range a {
		l := len(parts)
		if l < mn {
			mn = l
		}
		if l > mx {
			mx = l
		}
	}
	if mx == -1 {
		return 0, 0
	}
	return mn, mx
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// keptAll asserts that member m in next retains every partition it held in prev.
func keptAll(t *testing.T, prev, next Assignment, m string) {
	t.Helper()
	for _, p := range prev[m] {
		if !contains(next[m], p) {
			t.Fatalf("member %s lost previously-owned partition %s (churn): prev=%v next=%v",
				m, p, prev[m], next[m])
		}
	}
}

// TestAddConsumer_MinimalMovement covers CLOSURE CLAUSE 1.
//
// Mutation: a from-scratch round-robin reassignment (ignoring stickiness) would
// reshuffle partitions across ALL members, so Moved() would exceed 2 and the
// unaffected member would churn. This test fails if Reassign stops keeping
// existing (consumer,partition) pairs sticky.
func TestAddConsumer_MinimalMovement(t *testing.T) {
	parts := []string{"p0", "p1", "p2", "p3", "p4", "p5", "p6", "p7", "p8"}
	prev := Assignment{
		"c1": {"p0", "p1", "p2"},
		"c2": {"p3", "p4", "p5"},
		"c3": {"p6", "p7", "p8"},
	}
	members := []string{"c1", "c2", "c3", "c4"} // c4 joins

	next := Reassign(prev, members, parts)

	t.Logf("run=%s clause=1 BEFORE loads(sorted)=%v prev=%v", runUUID, loadSlice(prev), prev)
	t.Logf("run=%s clause=1 AFTER  loads(sorted)=%v next=%v", runUUID, loadSlice(next), next)

	// Balanced: 9 partitions / 4 members -> loads {2,2,2,3}.
	want := []int{2, 2, 2, 3}
	got := loadSlice(next)
	if len(got) != len(want) {
		t.Fatalf("member count mismatch: got %d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unbalanced loads: got %v want %v", got, want)
		}
	}

	// The newcomer must receive exactly its minimal target share = floor(9/4)=2.
	if l := len(next["c4"]); l != 2 {
		t.Fatalf("newcomer c4 should receive minimal share 2, got %d (%v)", l, next["c4"])
	}

	// Minimal movement: only the 2 partitions handed to c4 may move.
	moved := Moved(prev, next)
	t.Logf("run=%s clause=1 MOVED=%v (count=%d, minimal=2)", runUUID, moved, len(moved))
	if len(moved) != 2 {
		t.Fatalf("expected exactly 2 partitions to move (minimal), got %d: %v", len(moved), moved)
	}
	// Every moved partition must now be owned by the newcomer (came from donors).
	for _, mp := range moved {
		if !contains(next["c4"], mp) {
			t.Fatalf("moved partition %s did not land on newcomer c4: next=%v", mp, next)
		}
	}

	// Exactly one prior member is unaffected and keeps ALL 3 of its partitions.
	unaffected := 0
	for _, m := range []string{"c1", "c2", "c3"} {
		lost := false
		for _, p := range prev[m] {
			if !contains(next[m], p) {
				lost = true
			}
		}
		if !lost {
			unaffected++
			keptAll(t, prev, next, m)
			if len(next[m]) != 3 {
				t.Fatalf("unaffected member %s should keep all 3, has %d", m, len(next[m]))
			}
		}
	}
	if unaffected != 1 {
		t.Fatalf("expected exactly 1 fully-unaffected prior member, got %d", unaffected)
	}

	// The two donors keep 2 of their 3 (only one revoked each).
	for _, m := range []string{"c1", "c2", "c3"} {
		if len(next[m]) == 2 {
			// donor: must still hold 2 of its original 3.
			keep := 0
			for _, p := range prev[m] {
				if contains(next[m], p) {
					keep++
				}
			}
			if keep != 2 {
				t.Fatalf("donor %s should retain 2 of its 3 original partitions, retained %d", m, keep)
			}
		}
	}
}

// TestRemoveConsumer_OnlyDepartedMoves covers CLOSURE CLAUSE 2.
//
// Mutation: if Reassign did NOT rebalance on leave (e.g. returned prev minus the
// departed member, dropping its partitions), the survivors would be unbalanced
// (or partitions would be lost) and this test fails. Equally, a from-scratch
// round-robin would churn survivors' partitions and inflate Moved().
func TestRemoveConsumer_OnlyDepartedMoves(t *testing.T) {
	parts := []string{"p0", "p1", "p2", "p3", "p4", "p5"}
	prev := Assignment{
		"c1": {"p0", "p1"},
		"c2": {"p2", "p3"},
		"c3": {"p4", "p5"}, // c3 will leave
	}
	members := []string{"c1", "c2"} // c3 departs

	next := Reassign(prev, members, parts)

	t.Logf("run=%s clause=2 BEFORE loads(sorted)=%v prev=%v", runUUID, loadSlice(prev), prev)
	t.Logf("run=%s clause=2 AFTER  loads(sorted)=%v next=%v", runUUID, loadSlice(next), next)

	// Survivors stay balanced: 6 partitions / 2 members -> {3,3}.
	got := loadSlice(next)
	if len(got) != 2 || got[0] != 3 || got[1] != 3 {
		t.Fatalf("survivors not balanced after leave: got %v want [3 3]", got)
	}

	// Departed member is gone entirely.
	if _, ok := next["c3"]; ok {
		t.Fatalf("departed member c3 still present: %v", next)
	}

	// Survivors keep ALL their previous partitions (zero churn for them).
	keptAll(t, prev, next, "c1")
	keptAll(t, prev, next, "c2")

	// Only the departed member's partitions move; nothing else.
	moved := Moved(prev, next)
	t.Logf("run=%s clause=2 MOVED=%v (count=%d, expected only c3's freed parts)", runUUID, moved, len(moved))
	wantMoved := []string{"p4", "p5"}
	if len(moved) != len(wantMoved) {
		t.Fatalf("expected only departed partitions to move %v, got %v", wantMoved, moved)
	}
	for i := range wantMoved {
		if moved[i] != wantMoved[i] {
			t.Fatalf("moved set mismatch: got %v want %v", moved, wantMoved)
		}
	}

	// The two freed partitions are absorbed one each by the survivors.
	if !contains(next["c1"], "p4") && !contains(next["c2"], "p4") {
		t.Fatalf("freed partition p4 not absorbed by a survivor: %v", next)
	}
	if !contains(next["c1"], "p5") && !contains(next["c2"], "p5") {
		t.Fatalf("freed partition p5 not absorbed by a survivor: %v", next)
	}
	// No partition is lost or duplicated.
	assertExactCover(t, next, parts)
}

// TestBalanceInvariant covers CLOSURE CLAUSE 3 across a sequence of membership
// changes (join, join, leave, leave). After EVERY reassign, max-min <= 1 and
// every partition is owned exactly once.
//
// Mutation: removing the rebalanceDown / cap-respecting placement logic would
// let one member accumulate freed partitions, pushing max-min > 1 and tripping
// this assertion at the leave steps.
func TestBalanceInvariant(t *testing.T) {
	parts := []string{}
	for _, p := range []string{"p0", "p1", "p2", "p3", "p4", "p5", "p6", "p7", "p8", "p9", "p10"} {
		parts = append(parts, p)
	}
	prev := Assignment{}

	steps := []struct {
		name    string
		members []string
	}{
		{"start-2", []string{"a", "b"}},
		{"join-c", []string{"a", "b", "c"}},
		{"join-d", []string{"a", "b", "c", "d"}},
		{"leave-b", []string{"a", "c", "d"}},
		{"leave-a", []string{"c", "d"}},
	}

	for _, st := range steps {
		next := Reassign(prev, st.members, parts)
		mn, mx := maxMinSpread(next)
		t.Logf("run=%s clause=3 step=%-8s members=%v loads(sorted)=%v min=%d max=%d spread=%d",
			runUUID, st.name, st.members, loadSlice(next), mn, mx, mx-mn)

		if mx-mn > 1 {
			t.Fatalf("step %s: balance invariant violated, max-min=%d (loads=%v)", st.name, mx-mn, loadSlice(next))
		}
		// Every member present, no extras.
		if len(next) != len(st.members) {
			t.Fatalf("step %s: member set mismatch got %d want %d", st.name, len(next), len(st.members))
		}
		for _, m := range st.members {
			if _, ok := next[m]; !ok {
				t.Fatalf("step %s: member %s missing from assignment", st.name, m)
			}
		}
		assertExactCover(t, next, parts)
		prev = next
	}
}

// TestDeterminism proves identical inputs yield byte-identical output (no
// time.Now / map-iteration nondeterminism leaks into the result).
//
// Mutation: introducing map-order-dependent placement (e.g. ranging members
// without sorting) would make repeated runs differ and fail this.
func TestDeterminism(t *testing.T) {
	parts := []string{"p0", "p1", "p2", "p3", "p4", "p5", "p6"}
	prev := Assignment{
		"x": {"p0", "p1", "p2"},
		"y": {"p3", "p4", "p5", "p6"},
	}
	members := []string{"x", "y", "z"}

	first := Reassign(prev, members, parts)
	for i := 0; i < 25; i++ {
		again := Reassign(prev, members, parts)
		for m, fp := range first {
			ap := again[m]
			if len(fp) != len(ap) {
				t.Fatalf("nondeterministic on iter %d member %s: %v vs %v", i, m, fp, ap)
			}
			for j := range fp {
				if fp[j] != ap[j] {
					t.Fatalf("nondeterministic on iter %d member %s: %v vs %v", i, m, fp, ap)
				}
			}
		}
	}
	t.Logf("run=%s determinism=stable result=%v", runUUID, first)
}

// TestMovedHelper directly exercises Moved on owner-change, add, and drop.
//
// Mutation: if Moved only diffed by partition presence (ignoring owner change),
// the "owner changed" case (p1: a->b) would be missed and this fails.
func TestMovedHelper(t *testing.T) {
	prev := Assignment{"a": {"p0", "p1"}, "b": {"p2"}}
	next := Assignment{"a": {"p0"}, "b": {"p1", "p2"}, "c": {"p3"}}
	// p1 owner changed a->b; p3 newly assigned; nothing dropped.
	got := Moved(prev, next)
	want := []string{"p1", "p3"}
	t.Logf("run=%s movedHelper got=%v want=%v", runUUID, got, want)
	if len(got) != len(want) {
		t.Fatalf("Moved got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Moved got %v want %v", got, want)
		}
	}

	// Drop case: a partition that disappears counts as moved.
	prev2 := Assignment{"a": {"p0", "p9"}}
	next2 := Assignment{"a": {"p0"}}
	gotDrop := Moved(prev2, next2)
	if len(gotDrop) != 1 || gotDrop[0] != "p9" {
		t.Fatalf("drop not detected by Moved: got %v want [p9]", gotDrop)
	}
}

// assertExactCover verifies every partition is owned by exactly one member and
// no foreign partitions appear.
func assertExactCover(t *testing.T, a Assignment, parts []string) {
	t.Helper()
	seen := make(map[string]int)
	for _, ps := range a {
		for _, p := range ps {
			seen[p]++
		}
	}
	for _, p := range parts {
		if seen[p] != 1 {
			t.Fatalf("partition %s owned %d times (want exactly 1): %v", p, seen[p], a)
		}
	}
	if len(seen) != len(parts) {
		t.Fatalf("ownership covers %d distinct partitions, want %d: %v", len(seen), len(parts), a)
	}
}
