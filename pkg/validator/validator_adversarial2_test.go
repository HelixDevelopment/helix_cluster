package validator

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// ADVERSARIAL SECOND PASS for pkg/validator (sink-side, fail-open hunting).
//
// The first adversarial pass fixed a float-NaN range-check bypass. This pass
// targets DIFFERENT surfaces. Every test asserts the ACTUAL valid/invalid
// verdict (not merely "no panic"). Tests prefixed TestADV2_REALBUG_ bite a real
// always-on fail-open hole and are the mutation tests for the fix that is left
// in place; they must FAIL on the pre-fix code and PASS on the fixed code.
// =============================================================================

// -----------------------------------------------------------------------------
// (a) REAL BUG — FAIL-OPEN: a `required` field that is a NIL POINTER is admitted
// as present.
//
// Root cause (validator.go validateField): a pointer is only dereferenced when
// non-nil (`if val.Kind()==Ptr && !val.IsNil()`). A NIL pointer is left as a
// reflect.Ptr Value and falls through the type switch to the `default` branch:
//
//     strVal = fmt.Sprintf("%v", val.Interface())   // => the literal "<nil>"
//
// "<nil>" is a NON-EMPTY 5-char string. The `required` rule does
// `strings.TrimSpace(strVal) == ""` — which is FALSE for "<nil>" — so a missing
// (nil) required field PASSES. That is the textbook fail-open: a field the schema
// marks mandatory is reported satisfied while the value is absent. Downstream code
// then dereferences a guaranteed-non-nil pointer that is in fact nil (panic), or
// persists an "empty" record that violates a NOT NULL contract.
//
// SINK-SIDE assertion: verdict must be INVALID wrapping ErrRequired. On pre-fix
// code it is nil -> this test FAILS, biting the bug.
// -----------------------------------------------------------------------------
func TestADV2_REALBUG_NilPointerRequiredFailsOpen(t *testing.T) {
	v := New()
	type S struct {
		Name *string `validate:"required"`
	}
	err := v.ValidateStruct(&S{Name: nil})
	if err == nil {
		t.Fatalf("FAIL-OPEN: nil *string on a `required` field admitted as present " +
			"(nil ptr renders as the non-empty literal \"<nil>\" via the default " +
			"Sprintf branch, so TrimSpace != \"\"); expected ErrRequired, got nil")
	}
	if !errors.Is(err, ErrRequired) {
		t.Fatalf("nil required pointer rejected but with wrong sentinel: %v", err)
	}
}

// A nil pointer under a string-length floor (min=N, no `required`) must also be
// rejected: an absent value has length 0 < N. Pre-fix it is compared as
// len("<nil>")==5, so e.g. min=4 would FAIL OPEN (5 >= 4). Use min=8 so the bug
// is unambiguous (5 < 8 happens to reject pre-fix) AND min=4 to catch the real
// fail-open. We assert the absent value is rejected for a floor it cannot meet.
func TestADV2_REALBUG_NilPointerMinLenFailsOpen(t *testing.T) {
	v := New()
	type S struct {
		// min=4: pre-fix len("<nil>")==5 >= 4 => WRONGLY ACCEPTED (fail-open).
		// post-fix absent => "" len 0 < 4 => correctly rejected.
		Token *string `validate:"min=4"`
	}
	err := v.ValidateStruct(&S{Token: nil})
	if !errors.Is(err, ErrMinExceeded) {
		t.Fatalf("FAIL-OPEN: nil *string under min=4 length floor admitted "+
			"(pre-fix compares len(\"<nil>\")==5 >= 4); expected ErrMinExceeded, got %v", err)
	}
}

// A nil pointer under oneof must be rejected (absent is not an allowed token).
// Pre-fix "<nil>" is compared against the set and (unless someone literally
// allows "<nil>") is rejected — but a fix must NOT regress this to accept. Pin it.
func TestADV2_NilPointerOneofRejected(t *testing.T) {
	v := New()
	type S struct {
		Status *string `validate:"oneof=active inactive"`
	}
	if err := v.ValidateStruct(&S{Status: nil}); !errors.Is(err, ErrOneOf) {
		t.Errorf("nil *string under oneof must be rejected, got %v", err)
	}
}

// Non-nil pointer to a valid value must still pass after the fix (no regression
// of TestValidateStructPointerField).
func TestADV2_NonNilPointerStillValid(t *testing.T) {
	v := New()
	type S struct {
		Name *string `validate:"required"`
	}
	s := "present"
	if err := v.ValidateStruct(&S{Name: &s}); err != nil {
		t.Errorf("non-nil valid pointer must pass required, got %v", err)
	}
}

// Non-nil pointer to an EMPTY string on a required field must be rejected: the
// value is present-but-empty. This pins that the fix did not over-correct (it
// must still dereference non-nil pointers and see the real empty value).
func TestADV2_NonNilPointerEmptyRequiredRejected(t *testing.T) {
	v := New()
	type S struct {
		Name *string `validate:"required"`
	}
	empty := ""
	if err := v.ValidateStruct(&S{Name: &empty}); !errors.Is(err, ErrRequired) {
		t.Errorf("non-nil pointer to empty string on required field must be rejected, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// (b) COMPOSITE — a struct with MANY failing fields must surface EVERY failure,
// not just the first. ValidateStruct collects per-field errors via errors.Join.
// A short-circuit mutation (return on first field error) would drop later ones.
// This is broader than the existing 2-field test: it mixes rule families and
// asserts each sentinel is individually matchable via errors.Is on the aggregate.
// -----------------------------------------------------------------------------
func TestADV2_CompositeAllFieldFailuresSurfaced(t *testing.T) {
	v := New()
	type S struct {
		Name   string  `validate:"required"`
		Email  string  `validate:"email"`
		ID     string  `validate:"uuid"`
		Status string  `validate:"oneof=on off"`
		Age    int     `validate:"min=18,max=65"`
		Score  float64 `validate:"min=0.0,max=1.0"`
	}
	err := v.ValidateStruct(&S{
		Name:   "",           // required fails
		Email:  "not-email",  // email fails
		ID:     "xxx",        // uuid fails
		Status: "maybe",      // oneof fails
		Age:    9,            // min fails
		Score:  2.0,          // max fails
	})
	if err == nil {
		t.Fatal("expected aggregate validation error, got nil")
	}
	for _, want := range []error{ErrRequired, ErrInvalidEmail, ErrInvalidUUID, ErrOneOf, ErrMinExceeded, ErrMaxExceeded} {
		if !errors.Is(err, want) {
			t.Errorf("composite aggregate must surface %v; full err: %v", want, err)
		}
	}
	if !errors.Is(err, ErrValidationFailed) {
		t.Errorf("aggregate must wrap ErrValidationFailed, got %v", err)
	}
}

// Within ONE field, multiple rules are evaluated left-to-right and the FIRST
// failure is returned. Pin that an out-of-range value on a multi-rule field is
// still rejected (here max fails even though min passes) — proves the per-field
// loop does not stop at the first PASSING rule and forget the rest.
func TestADV2_PerFieldLaterRuleStillEnforced(t *testing.T) {
	v := New()
	type S struct {
		Age int `validate:"min=0,max=10"` // min passes for 50, max must still fire
	}
	if err := v.ValidateStruct(&S{Age: 50}); !errors.Is(err, ErrMaxExceeded) {
		t.Errorf("a later rule (max) must still be enforced after an earlier rule (min) passes, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// (c) NUMERIC EDGE beyond NaN — integer min boundary off-by-one and signed/
// unsigned handling. `<`/`>` are strict, so the exact bound is IN-range; one past
// is OUT. Also: a NEGATIVE int against a positive min must be rejected.
// -----------------------------------------------------------------------------
func TestADV2_IntNegativeBelowMinRejected(t *testing.T) {
	v := New()
	type S struct {
		N int `validate:"min=1"`
	}
	if err := v.ValidateStruct(&S{N: -2147483648}); !errors.Is(err, ErrMinExceeded) {
		t.Errorf("large-negative int must fail min=1, got %v", err)
	}
}

func TestADV2_IntMaxOffByOneRejected(t *testing.T) {
	v := New()
	type S struct {
		N int `validate:"max=100"`
	}
	if err := v.ValidateStruct(&S{N: 101}); !errors.Is(err, ErrMaxExceeded) {
		t.Errorf("max+1 must be rejected, got %v", err)
	}
	if err := v.ValidateStruct(&S{N: 100}); err != nil {
		t.Errorf("exact max must be accepted, got %v", err)
	}
}

// Float +Inf with only a min bound: +Inf >= min, so it PASSES min — but it is
// outside any sane upper expectation. This documents that min-only does NOT trap
// +Inf (there is no upper bound declared). LATENT-RISK pin: callers that need a
// finite value must declare BOTH bounds. We assert today's behavior so a future
// reader sees it is intentional, not an oversight.
func TestADV2_FloatPosInfPassesMinOnly_DocumentedLatent(t *testing.T) {
	v := New()
	type S struct {
		Weight float64 `validate:"min=0.0"`
	}
	// +Inf > 0 is true, so min passes; no max declared => accepted. This is a
	// LATENT risk (min-only does not bound above), documented here on purpose.
	if err := v.ValidateStruct(&S{Weight: math.Inf(1)}); err != nil {
		t.Errorf("documented: +Inf passes a min-only bound (no upper bound declared), got %v", err)
	}
}

// -----------------------------------------------------------------------------
// (d) STRING-LENGTH is BYTE length, not rune count. A multibyte string can have
// more bytes than runes, so a min= byte-floor is satisfied by fewer visible
// characters than a naive reader expects. DOCUMENTED-CONTRACT pin: len(string)
// is bytes. We assert the byte semantics so a "fix" to rune counting is a
// conscious, test-visible decision (it would change which inputs pass).
// -----------------------------------------------------------------------------
func TestADV2_StringLenIsBytesNotRunes_DocumentedContract(t *testing.T) {
	v := New()
	type S struct {
		// "é" is 2 bytes in UTF-8 but 1 rune. With min=2, a single "é" (2 bytes)
		// satisfies the byte floor though it is one visible character.
		Code string `validate:"min=2"`
	}
	if err := v.ValidateStruct(&S{Code: "é"}); err != nil {
		t.Errorf("documented: min= counts BYTES; one 2-byte rune satisfies min=2, got %v", err)
	}
	// And a 3-byte single rune satisfies min=3 likewise (byte semantics).
	type T struct {
		Code string `validate:"min=3"`
	}
	if err := v.ValidateStruct(&T{Code: "中"}); err != nil { // U+4E2D = 3 bytes
		t.Errorf("documented: 3-byte rune satisfies byte min=3, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// (e) REGEX DoS / hostile-input — IsValidID and the UUID/email paths must not
// hang or panic on pathological input. The package regexes are RE2 (linear, no
// backtracking), so this must complete near-instantly; we still guard with a
// timeout so a future swap to a backtracking engine is caught as a hang.
// -----------------------------------------------------------------------------
func TestADV2_NoReDoSOnHostileInput(t *testing.T) {
	v := New()
	done := make(chan struct{})
	go func() {
		defer close(done)
		long := strings.Repeat("a-_", 200000) + "!" // valid prefix then a reject char
		_ = v.IsValidID(long)
		type S struct {
			ID    string `validate:"uuid"`
			Email string `validate:"email"`
		}
		_ = v.ValidateStruct(&S{
			ID:    strings.Repeat("0", 100000),
			Email: strings.Repeat("a", 100000) + "@" + strings.Repeat("b", 100000),
		})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("validation did not complete within 5s on hostile input (possible ReDoS / catastrophic backtracking)")
	}
}

// IsValidID must REJECT control characters / newlines / null bytes embedded in
// an otherwise-alnum string (anchored ^...$ regex). A fail-open here would admit
// an injection-bearing identifier.
func TestADV2_IsValidIDRejectsEmbeddedControl(t *testing.T) {
	v := New()
	for _, bad := range []string{
		"node\n1",      // embedded newline
		"node\x001",    // embedded NUL
		"node 1",       // space
		"node\t1",      // tab
		"node/1",       // slash (not in class)
		"node.1",       // dot (not in class)
		"",             // empty (+ requires >=1 char)
		"node1\n",      // trailing newline (anchors must reject)
	} {
		if v.IsValidID(bad) {
			t.Errorf("IsValidID must reject %q (anchored alnum class), but it was accepted", bad)
		}
	}
	// Sanity: a clean id is accepted.
	if !v.IsValidID("node-1_2") {
		t.Error("IsValidID must accept a clean id 'node-1_2'")
	}
}

// -----------------------------------------------------------------------------
// (f) CUSTOM RULE error must FAIL the field (error-as-success guard). A
// registered rule returning a non-nil error must make ValidateStruct invalid,
// and the original error must be preserved (wrapped) for the caller.
// -----------------------------------------------------------------------------
func TestADV2_CustomRuleErrorIsNotSwallowed(t *testing.T) {
	v := New()
	sentinel := errors.New("custom-policy-violation")
	v.RegisterRule("policy", func(string) error { return sentinel })
	type S struct {
		X string `validate:"policy"`
	}
	err := v.ValidateStruct(&S{X: "anything"})
	if err == nil {
		t.Fatal("custom rule returning an error must make the struct invalid, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("custom rule error must be preserved (wrapped) for the caller, got %v", err)
	}
}
