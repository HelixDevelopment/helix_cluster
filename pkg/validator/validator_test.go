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
	if !v.IsValidID("node_1") {
		t.Error("expected 'node_1' to be valid")
	}
	if v.IsValidID("node 1") {
		t.Error("expected 'node 1' to be invalid")
	}
	// Boundary: alphaNum uses '+', so the empty string must be rejected.
	if v.IsValidID("") {
		t.Error("expected empty string to be invalid")
	}
}

func TestNotEmpty(t *testing.T) {
	v := New()
	if err := v.NotEmpty("hello"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := v.NotEmpty(""); !errors.Is(err, ErrEmptyValue) {
		t.Errorf("expected ErrEmptyValue for empty string, got %v", err)
	}
	if err := v.NotEmpty("   "); !errors.Is(err, ErrEmptyValue) {
		t.Errorf("expected ErrEmptyValue for whitespace-only string, got %v", err)
	}
}

func TestValidateStructRequired(t *testing.T) {
	v := New()
	type S struct {
		Name string `validate:"required"`
	}
	err := v.ValidateStruct(&S{Name: ""})
	if !errors.Is(err, ErrRequired) {
		t.Errorf("expected ErrRequired for empty required field, got %v", err)
	}
	if !errors.Is(err, ErrValidationFailed) {
		t.Errorf("expected aggregate error to wrap ErrValidationFailed, got %v", err)
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
	if err := v.ValidateStruct(&S{Age: 17, Code: "AB"}); !errors.Is(err, ErrMinExceeded) {
		t.Errorf("expected ErrMinExceeded for age < min, got %v", err)
	}
	if err := v.ValidateStruct(&S{Age: 66, Code: "AB"}); !errors.Is(err, ErrMaxExceeded) {
		t.Errorf("expected ErrMaxExceeded for age > max, got %v", err)
	}
	if err := v.ValidateStruct(&S{Age: 30, Code: "A"}); !errors.Is(err, ErrMinExceeded) {
		t.Errorf("expected ErrMinExceeded for code length < min, got %v", err)
	}
	if err := v.ValidateStruct(&S{Age: 30, Code: "ABCDEF"}); !errors.Is(err, ErrMaxExceeded) {
		t.Errorf("expected ErrMaxExceeded for code length > max, got %v", err)
	}
	if err := v.ValidateStruct(&S{Age: 30, Code: "ABC"}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateStructUintMinMax(t *testing.T) {
	v := New()
	type S struct {
		Port  uint16 `validate:"min=1024,max=65535"`
		Count uint64 `validate:"min=1"`
	}
	if err := v.ValidateStruct(&S{Port: 80, Count: 5}); !errors.Is(err, ErrMinExceeded) {
		t.Errorf("expected ErrMinExceeded for uint < min, got %v", err)
	}
	if err := v.ValidateStruct(&S{Port: 1024, Count: 0}); !errors.Is(err, ErrMinExceeded) {
		t.Errorf("expected ErrMinExceeded for uint64 == 0 (< min 1), got %v", err)
	}
	// uint max boundary: declare a value above the max in a fresh field.
	type M struct {
		Level uint8 `validate:"max=10"`
	}
	if err := v.ValidateStruct(&M{Level: 11}); !errors.Is(err, ErrMaxExceeded) {
		t.Errorf("expected ErrMaxExceeded for uint > max, got %v", err)
	}
	if err := v.ValidateStruct(&S{Port: 8080, Count: 1}); err != nil {
		t.Errorf("unexpected error for in-range uints: %v", err)
	}
}

func TestValidateStructEmail(t *testing.T) {
	v := New()
	type S struct {
		Email string `validate:"email"`
	}
	if err := v.ValidateStruct(&S{Email: "bad"}); !errors.Is(err, ErrInvalidEmail) {
		t.Errorf("expected ErrInvalidEmail for invalid email, got %v", err)
	}
	if err := v.ValidateStruct(&S{Email: "user@example.com"}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	// Boundary: net/mail.ParseAddress accepts "a@b" (no dotted TLD required).
	if err := v.ValidateStruct(&S{Email: "a@b"}); err != nil {
		t.Errorf("expected 'a@b' accepted by net/mail, got %v", err)
	}
	// Boundary: a trailing-@ with no domain must be rejected.
	if err := v.ValidateStruct(&S{Email: "user@"}); !errors.Is(err, ErrInvalidEmail) {
		t.Errorf("expected ErrInvalidEmail for 'user@', got %v", err)
	}
}

func TestValidateStructUUID(t *testing.T) {
	v := New()
	type S struct {
		ID string `validate:"uuid"`
	}
	if err := v.ValidateStruct(&S{ID: "not-a-uuid"}); !errors.Is(err, ErrInvalidUUID) {
		t.Errorf("expected ErrInvalidUUID for invalid uuid, got %v", err)
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
	if err := v.ValidateStruct(&S{Status: "unknown"}); !errors.Is(err, ErrOneOf) {
		t.Errorf("expected ErrOneOf for invalid oneof, got %v", err)
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
	if err := v.ValidateStruct(nil); !errors.Is(err, ErrNilStruct) {
		t.Errorf("expected ErrNilStruct for nil struct, got %v", err)
	}
}

func TestValidateStructNonStruct(t *testing.T) {
	v := New()
	if err := v.ValidateStruct("string"); !errors.Is(err, ErrNotStruct) {
		t.Errorf("expected ErrNotStruct for non-struct, got %v", err)
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
	if err := v.ValidateStruct(&S{Score: -1}); !errors.Is(err, ErrMinExceeded) {
		t.Errorf("expected ErrMinExceeded for float < min, got %v", err)
	}
	if err := v.ValidateStruct(&S{Score: 101}); !errors.Is(err, ErrMaxExceeded) {
		t.Errorf("expected ErrMaxExceeded for float > max, got %v", err)
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
	if err := v.ValidateStruct(&S{Code: "AB"}); !errors.Is(err, ErrMinExceeded) {
		t.Errorf("mutation: string min should wrap ErrMinExceeded, got %v", err)
	}
	if err := v.ValidateStruct(&S{Code: "ABCDEF"}); !errors.Is(err, ErrMaxExceeded) {
		t.Errorf("mutation: string max should wrap ErrMaxExceeded, got %v", err)
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

// TestUnregisteredRuleIsSilentlyIgnored pins the documented behavior of
// checkRule's default fall-through: a tag name with no registered function is
// silently skipped. A mutation that instead returns an error for unknown rules
// breaks this test.
func TestUnregisteredRuleIsSilentlyIgnored(t *testing.T) {
	v := New()
	type S struct {
		Name string `validate:"nonexistent_rule"`
	}
	// An unregistered rule must not cause an error.
	if err := v.ValidateStruct(&S{Name: "hello"}); err != nil {
		t.Errorf("unregistered rule should be silently ignored, got error: %v", err)
	}
}

// TestRegisteredRuleRequiredOrdering proves that when both required and a
// custom rule appear on the same field, required is evaluated first (it is the
// first token in the tag string) and short-circuits before the custom rule if
// the field is empty.
func TestRegisteredRuleRequiredOrdering(t *testing.T) {
	v := New()
	v.RegisterRule("mustbe_x", func(value string) error {
		if value != "x" {
			return errors.New("must be x")
		}
		return nil
	})
	type S struct {
		Name string `validate:"required,mustbe_x"`
	}
	// Empty field: required fires first, mustbe_x is never reached.
	err := v.ValidateStruct(&S{Name: ""})
	if !errors.Is(err, ErrRequired) {
		t.Errorf("expected ErrRequired for empty required field, got %v", err)
	}
	// Correct value: both rules pass.
	if err := v.ValidateStruct(&S{Name: "x"}); err != nil {
		t.Errorf("unexpected error for valid value: %v", err)
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
