package errors

import (
	"fmt"
	"testing"
)

func TestWrap(t *testing.T) {
	inner := fmt.Errorf("inner error")
	err := Wrap(inner, "E_TEST", "test message")
	if err.Code != "E_TEST" {
		t.Errorf("expected code E_TEST, got %s", err.Code)
	}
	if err.Cause != inner {
		t.Error("expected cause to be inner error")
	}
}
