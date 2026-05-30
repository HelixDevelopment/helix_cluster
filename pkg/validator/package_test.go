package validator

import "testing"

func TestIsValidID(t *testing.T) {
	v := New()
	if !v.IsValidID("node-1") {
		t.Error("expected 'node-1' to be valid")
	}
	if v.IsValidID("node 1") {
		t.Error("expected 'node 1' to be invalid")
	}
}

func TestNotEmpty(t *testing.T) {
	v := New()
	if err := v.NotEmpty("hello"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := v.NotEmpty(""); err == nil {
		t.Error("expected error for empty string")
	}
}
