package federation

import (
	"errors"
	"testing"
	"time"
)

// TestCellLifecycle_LegalPipeline drives a cell through the exact legal FSM
// pipeline Joining -> Active -> Degraded -> Active -> Draining -> Left and
// asserts the concrete phase after EACH step (sink-side state, not "no error").
//
// MUTATION that kills this: in Cell.advance, replace `c.phase = to` with a
// no-op (leave phase unchanged). The first assertPhase(PhaseActive) fails
// because the cell is still PhaseJoining.
func TestCellLifecycle_LegalPipeline(t *testing.T) {
	at := time.Unix(1000, 0)
	c, err := NewCell("c1", WithRegion("eu"))
	if err != nil {
		t.Fatalf("NewCell: %v", err)
	}
	if c.Phase() != PhaseJoining {
		t.Fatalf("initial phase = %s, want Joining", c.Phase())
	}

	steps := []struct {
		to   CellPhase
		want CellPhase
	}{
		{PhaseActive, PhaseActive},
		{PhaseDegraded, PhaseDegraded},
		{PhaseActive, PhaseActive},
		{PhaseDraining, PhaseDraining},
		{PhaseLeft, PhaseLeft},
	}
	for i, s := range steps {
		at = at.Add(time.Second)
		if err := c.advance(s.to, at); err != nil {
			t.Fatalf("step %d advance to %s: unexpected err %v", i, s.to, err)
		}
		if c.Phase() != s.want {
			t.Fatalf("step %d: phase = %s, want %s", i, c.Phase(), s.want)
		}
		if !c.UpdatedAt().Equal(at) {
			t.Fatalf("step %d: updatedAt = %v, want %v", i, c.UpdatedAt(), at)
		}
	}
}

// TestCellLifecycle_RejectsIllegalJumps asserts that illegal edges are refused
// AND that the cell's phase is unchanged after a rejected attempt.
//
// MUTATION that kills this: make LegalTransition return true unconditionally
// (e.g. `return true`). Then Joining -> Draining is accepted and the
// ErrIllegalTransition assertion fails; also the "phase unchanged" assertion
// fails because the cell moved to Draining.
func TestCellLifecycle_RejectsIllegalJumps(t *testing.T) {
	illegal := []struct {
		from, to CellPhase
	}{
		{PhaseJoining, PhaseDegraded},
		{PhaseJoining, PhaseDraining},
		{PhaseActive, PhaseJoining},
		{PhaseDraining, PhaseActive},
		{PhaseDraining, PhaseDegraded},
		{PhaseLeft, PhaseActive},
		{PhaseLeft, PhaseDraining},
		{PhaseActive, PhaseActive}, // self-transition is not in the graph
	}
	for _, tc := range illegal {
		if LegalTransition(tc.from, tc.to) {
			t.Fatalf("LegalTransition(%s,%s) = true, want false", tc.from, tc.to)
		}
		c := &Cell{ID: "x", phase: tc.from}
		err := c.advance(tc.to, time.Now())
		if !errors.Is(err, ErrIllegalTransition) {
			t.Fatalf("advance %s->%s err = %v, want ErrIllegalTransition", tc.from, tc.to, err)
		}
		if c.Phase() != tc.from {
			t.Fatalf("after rejected %s->%s, phase = %s, want unchanged %s", tc.from, tc.to, c.Phase(), tc.from)
		}
	}
}

// TestLegalTransition_GraphExact pins the EXACT legal edge set so a mutation
// that widens or narrows the graph is caught. Every (from,to) pair in the 5x5
// space is checked against the spec table.
//
// MUTATION that kills this: add PhaseDraining->PhaseActive to legalTransitions
// (illegal "un-drain"). The want=false case for that pair fails.
func TestLegalTransition_GraphExact(t *testing.T) {
	want := map[CellPhase]map[CellPhase]bool{
		PhaseJoining:  {PhaseActive: true, PhaseLeft: true},
		PhaseActive:   {PhaseDegraded: true, PhaseDraining: true, PhaseLeft: true},
		PhaseDegraded: {PhaseActive: true, PhaseDraining: true, PhaseLeft: true},
		PhaseDraining: {PhaseLeft: true},
		PhaseLeft:     {},
	}
	all := []CellPhase{PhaseJoining, PhaseActive, PhaseDegraded, PhaseDraining, PhaseLeft}
	for _, from := range all {
		for _, to := range all {
			got := LegalTransition(from, to)
			expect := want[from][to]
			if got != expect {
				t.Fatalf("LegalTransition(%s,%s) = %v, want %v", from, to, got, expect)
			}
		}
	}
}

// TestNewCell_EmptyID asserts construction rejects an empty id.
//
// MUTATION that kills this: remove the `if id == ""` guard in NewCell. Then
// err is nil and the test fails.
func TestNewCell_EmptyID(t *testing.T) {
	if _, err := NewCell(""); !errors.Is(err, ErrEmptyCellID) {
		t.Fatalf("NewCell(\"\") err = %v, want ErrEmptyCellID", err)
	}
}
