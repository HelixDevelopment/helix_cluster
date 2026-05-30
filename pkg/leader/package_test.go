package leader

import "testing"

func TestElection(t *testing.T) {
	e := NewElection()
	if e.IsLeader() {
		t.Error("expected not leader initially")
	}
	e.BecomeLeader()
	if !e.IsLeader() {
		t.Error("expected leader after becoming leader")
	}
	e.Resign()
	if e.IsLeader() {
		t.Error("expected not leader after resign")
	}
}
