package log

import "testing"

func TestLogger(t *testing.T) {
	l := New("test")
	if l.prefix != "test" {
		t.Errorf("expected prefix 'test', got %s", l.prefix)
	}
	l.Info("hello")
	l.Error("world")
}
