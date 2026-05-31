package validator

import (
	"errors"
	"testing"
)

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
	if err := v.NotEmpty("   "); err == nil {
		t.Error("expected error for whitespace-only string")
	}
}

func TestValidateStructRequired(t *testing.T) {
	v := New()
	type S struct {
		Name string `validate:"required"`
	}
	if err := v.ValidateStruct(&S{Name: ""}); err == nil {
		t.Error("expected error for empty required field")
	}
	if err := v.ValidateStruct(&S{Name: "ok"}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateStructMinMax(t *testing.T) {
	v := New()
	type S struct {
		Age  int    `validate:"min=18,max=65"`
		Code string `validate:"min=2,max=5"`
	}
	if err := v.ValidateStruct(&S{Age: 17, Code: "AB"}); err == nil {
		t.Error("expected error for age < min")
	}
	if err := v.ValidateStruct(&S{Age: 66, Code: "AB"}); err == nil {
		t.Error("expected error for age > max")
	}
	if err := v.ValidateStruct(&S{Age: 30, Code: "A"}); err == nil {
		t.Error("expected error for code length < min")
	}
	if err := v.ValidateStruct(&S{Age: 30, Code: "ABCDEF"}); err == nil {
		t.Error("expected error for code length > max")
	}
	if err := v.ValidateStruct(&S{Age: 30, Code: "ABC"}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateStructEmail(t *testing.T) {
	v := New()
	type S struct {
		Email string `validate:"email"`
	}
	if err := v.ValidateStruct(&S{Email: "bad"}); err == nil {
		t.Error("expected error for invalid email")
	}
	if err := v.ValidateStruct(&S{Email: "user@example.com"}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateStructUUID(t *testing.T) {
	v := New()
	type S struct {
		ID string `validate:"uuid"`
	}
	if err := v.ValidateStruct(&S{ID: "not-a-uuid"}); err == nil {
		t.Error("expected error for invalid uuid")
	}
	if err := v.ValidateStruct(&S{ID: "550e8400-e29b-41d4-a716-446655440000"}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateStructOneOf(t *testing.T) {
	v := New()
	type S struct {
		Status string `validate:"oneof=active inactive"`
	}
	if err := v.ValidateStruct(&S{Status: "unknown"}); err == nil {
		t.Error("expected error for invalid oneof")
	}
	if err := v.ValidateStruct(&S{Status: "active"}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateStructMultipleErrors(t *testing.T) {
	v := New()
	type S struct {
		Name  string `validate:"required"`
		Email string `validate:"email"`
	}
	err := v.ValidateStruct(&S{Name: "", Email: "bad"})
	if err == nil {
		t.Fatal("expected validation error")
	}
	msg := err.Error()
	if !contains(msg, "Name") || !contains(msg, "Email") {
		t.Error("expected multiple field errors")
	}
}

func TestValidateStructNil(t *testing.T) {
	v := New()
	if err := v.ValidateStruct(nil); err == nil {
		t.Error("expected error for nil struct")
	}
}

func TestValidateStructNonStruct(t *testing.T) {
	v := New()
	if err := v.ValidateStruct("string"); err == nil {
		t.Error("expected error for non-struct")
	}
}

func TestValidateStructNoTags(t *testing.T) {
	v := New()
	type S struct {
		Name string
	}
	if err := v.ValidateStruct(&S{Name: ""}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRegisterRule(t *testing.T) {
	v := New()
	v.RegisterRule("startswith_h", func(value string) error {
		if len(value) == 0 || value[0] != 'h' {
			return errors.New("must start with h")
		}
		return nil
	})
	type S struct {
		Name string `validate:"required,startswith_h"`
	}
	if err := v.ValidateStruct(&S{Name: "hello"}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := v.ValidateStruct(&S{Name: "world"}); err == nil {
		t.Error("expected custom rule to fail")
	}
}

func TestValidateStructPointerField(t *testing.T) {
	v := New()
	type S struct {
		Name *string `validate:"required"`
	}
	s := "ok"
	if err := v.ValidateStruct(&S{Name: &s}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateStructFloatMinMax(t *testing.T) {
	v := New()
	type S struct {
		Score float64 `validate:"min=0.0,max=100.0"`
	}
	if err := v.ValidateStruct(&S{Score: -1}); err == nil {
		t.Error("expected error for float < min")
	}
	if err := v.ValidateStruct(&S{Score: 101}); err == nil {
		t.Error("expected error for float > max")
	}
	if err := v.ValidateStruct(&S{Score: 50}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- Mutation tests ---

func TestMutationValidateStructCollectsAllErrors(t *testing.T) {
	v := New()
	type S struct {
		A string `validate:"required"`
		B string `validate:"required"`
	}
	err := v.ValidateStruct(&S{A: "", B: ""})
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !contains(msg, "A") || !contains(msg, "B") {
		t.Error("mutation: should report errors for both fields")
	}
}

func TestMutationMinMaxOnStringLength(t *testing.T) {
	v := New()
	type S struct {
		Code string `validate:"min=3,max=5"`
	}
	if err := v.ValidateStruct(&S{Code: "AB"}); err == nil {
		t.Error("mutation: string min should check length")
	}
	if err := v.ValidateStruct(&S{Code: "ABCDEF"}); err == nil {
		t.Error("mutation: string max should check length")
	}
}

func TestMutationUUIDCaseInsensitive(t *testing.T) {
	v := New()
	type S struct {
		ID string `validate:"uuid"`
	}
	upper := "550E8400-E29B-41D4-A716-446655440000"
	if err := v.ValidateStruct(&S{ID: upper}); err != nil {
		t.Error("mutation: UUID validation should be case-insensitive")
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
