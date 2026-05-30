package errors

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	err := New(E_NOT_FOUND, "resource missing")
	if err.Code != E_NOT_FOUND {
		t.Errorf("expected code %s, got %s", E_NOT_FOUND, err.Code)
	}
	if err.Message != "resource missing" {
		t.Errorf("expected message 'resource missing', got %s", err.Message)
	}
	if len(err.Stack) == 0 {
		t.Error("expected non-empty stack trace")
	}
	if !strings.Contains(err.Error(), "resource missing") {
		t.Errorf("expected error string to contain message, got %s", err.Error())
	}
}

func TestNew_Mutation(t *testing.T) {
	// Mutation: New does not capture stack trace
	err := New(E_INTERNAL, "boom")
	if len(err.Stack) == 0 {
		t.Error("expected stack trace to be captured")
	}
}

func TestWrap(t *testing.T) {
	inner := fmt.Errorf("inner error")
	err := Wrap(inner, E_TIMEOUT, "request timed out")
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	if err.Code != E_TIMEOUT {
		t.Errorf("expected code %s, got %s", E_TIMEOUT, err.Code)
	}
	if err.Cause != inner {
		t.Error("expected cause to be inner error")
	}
	if !errors.Is(err, inner) {
		t.Error("expected errors.Is to match inner")
	}
	if len(err.Stack) == 0 {
		t.Error("expected stack trace on wrapped error")
	}
}

func TestWrap_Mutation(t *testing.T) {
	// Mutation: Wrap returns non-nil for nil input
	err := Wrap(nil, E_UNKNOWN, "should be nil")
	if err != nil {
		t.Error("expected nil when wrapping nil")
	}
}

func TestWithField(t *testing.T) {
	err := New(E_INVALID, "bad input").WithField("field", "email")
	if err.Fields["field"] != "email" {
		t.Errorf("expected field email, got %v", err.Fields["field"])
	}
}

func TestWithField_Mutation(t *testing.T) {
	// Mutation: WithField on nil panics or mutates nil
	var nilErr *Error
	result := nilErr.WithField("k", "v")
	if result != nil {
		t.Error("expected nil result from WithField on nil")
	}
}

func TestWithFields(t *testing.T) {
	err := New(E_INTERNAL, "failure").WithFields(map[string]interface{}{
		"node_id": "node-1",
		"shard":   42,
	})
	if err.Fields["node_id"] != "node-1" {
		t.Errorf("expected node_id node-1, got %v", err.Fields["node_id"])
	}
	if err.Fields["shard"] != 42 {
		t.Errorf("expected shard 42, got %v", err.Fields["shard"])
	}
}

func TestIsCode(t *testing.T) {
	inner := New(E_NOT_FOUND, "missing")
	outer := Wrap(inner, E_UNAVAILABLE, "service down")

	if !IsCode(outer, E_UNAVAILABLE) {
		t.Error("expected IsCode to match outer code")
	}
	if !IsCode(outer, E_NOT_FOUND) {
		t.Error("expected IsCode to match inner code through cause")
	}
	if IsCode(outer, E_INVALID) {
		t.Error("expected IsCode to not match unrelated code")
	}
	if IsCode(nil, E_UNKNOWN) {
		t.Error("expected IsCode(nil) to be false")
	}
}

func TestIsCode_Mutation(t *testing.T) {
	// Mutation: IsCode does not traverse cause chain
	inner := New(E_NOT_FOUND, "missing")
	outer := Wrap(inner, E_UNAVAILABLE, "service down")
	if !IsCode(outer, E_NOT_FOUND) {
		t.Error("expected IsCode to traverse cause chain")
	}
}

func TestGetFields(t *testing.T) {
	inner := New(E_NOT_FOUND, "missing").WithField("resource", "user")
	outer := Wrap(inner, E_UNAVAILABLE, "down").WithField("retry_after", 30)

	fields := GetFields(outer)
	if fields["resource"] != "user" {
		t.Errorf("expected resource=user from inner, got %v", fields["resource"])
	}
	if fields["retry_after"] != 30 {
		t.Errorf("expected retry_after=30 from outer, got %v", fields["retry_after"])
	}
}

func TestGetFields_Mutation(t *testing.T) {
	// Mutation: GetFields overwrites inner fields with outer instead of merging
	inner := New(E_NOT_FOUND, "missing").WithField("key", "inner")
	outer := Wrap(inner, E_UNAVAILABLE, "down").WithField("key", "outer")

	fields := GetFields(outer)
	// Inner field should be preserved (first seen wins)
	if fields["key"] != "inner" {
		t.Errorf("expected inner field to be preserved, got %v", fields["key"])
	}
}

func TestStackTrace(t *testing.T) {
	err := New(E_INTERNAL, "boom")
	st := err.StackTrace()
	if st == "" {
		t.Error("expected non-empty stack trace string")
	}
	if !strings.Contains(st, "package_test.go") {
		t.Error("expected stack trace to reference test file")
	}
}

func TestStackTrace_Mutation(t *testing.T) {
	// Mutation: StackTrace returns garbage or panics on nil
	var nilErr *Error
	st := nilErr.StackTrace()
	if st != "" {
		t.Errorf("expected empty stack trace for nil, got %s", st)
	}
}

func TestErrorFormatting(t *testing.T) {
	err := New(E_INVALID, "bad request").
		WithField("code", 400).
		WithField("path", "/api/v1/users")
	str := err.Error()
	if !strings.Contains(str, "bad request") {
		t.Errorf("expected message in error string, got %s", str)
	}
	if !strings.Contains(str, "fields=") {
		t.Errorf("expected fields in error string, got %s", str)
	}
}

func TestErrorFormattingWithCause(t *testing.T) {
	inner := New(E_NOT_FOUND, "user not found")
	outer := Wrap(inner, E_UNAVAILABLE, "query failed")
	str := outer.Error()
	if !strings.Contains(str, "query failed") {
		t.Errorf("expected outer message, got %s", str)
	}
	if !strings.Contains(str, "user not found") {
		t.Errorf("expected cause message, got %s", str)
	}
}

func TestErrorFormattingWithCause_Mutation(t *testing.T) {
	// Mutation: Error() does not include cause
	inner := fmt.Errorf("inner")
	outer := Wrap(inner, E_INTERNAL, "outer")
	str := outer.Error()
	if !strings.Contains(str, "inner") {
		t.Error("expected cause in error string")
	}
}

func TestCodeEnumValues(t *testing.T) {
	codes := []Code{
		E_UNKNOWN, E_NOT_FOUND, E_INVALID, E_TIMEOUT,
		E_UNAVAILABLE, E_INTERNAL, E_UNAUTHORIZED, E_CONFLICT,
	}
	for _, c := range codes {
		if c == "" {
			t.Error("expected non-empty code enum")
		}
	}
}

func TestConcurrentFieldAccess(t *testing.T) {
	err := New(E_INTERNAL, "race test")
	done := make(chan struct{}, 100)
	for i := 0; i < 50; i++ {
		go func(i int) {
			err.WithField(fmt.Sprintf("key%d", i), i)
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 50; i++ {
		go func() {
			_ = err.Fields
			done <- struct{}{}
		}()
	}
	for i := 0; i < 100; i++ {
		<-done
	}
}
