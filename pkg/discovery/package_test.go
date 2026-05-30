package discovery

import "testing"

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	r.Register("svc", "inst-1")
	r.Register("svc", "inst-2")
	insts := r.Lookup("svc")
	if len(insts) != 2 {
		t.Errorf("expected 2 instances, got %d", len(insts))
	}
}
