package validator

import (
	"errors"
	"math"
	"sync"
	"testing"
)

// =============================================================================
// ADVERSARIAL SINK-SIDE PROBES for pkg/validator.
//
// The canonical FAIL-OPEN failure mode for a validator is returning a "valid"
// verdict (nil error) on hostile / malformed / out-of-range input. Every test
// below asserts the ACTUAL valid/invalid verdict, never merely "no panic".
//
// Classification of each finding is in the FINAL REPORT. Tests whose name is
// prefixed `TestADV_REALBUG_` FAIL against unmodified prod and are the
// mutation-bite tests the coordinator's fix must turn green. They are NOT gated
// behind any env var (per protocol step 6).
// =============================================================================

// -----------------------------------------------------------------------------
// (a) FAIL-OPEN — NaN slips past a min/max range guard.
//
// Contract (validator.go:219-233 + TestValidateStructFloatMinMax): a field
// tagged `min=0.0,max=100.0` declares a BOUNDED range; a value below min must
// yield ErrMinExceeded and a value above max ErrMaxExceeded. checkMin/checkMax
// implement this with `val.Float() < n` / `val.Float() > n`.
//
// IEEE-754: every ordered comparison against NaN is false. So for x = NaN:
//   NaN < 0.0   == false  -> checkMin passes
//   NaN > 100.0 == false  -> checkMax passes
// => NaN is admitted as an in-range value. A NaN score / weight / ratio is the
// textbook poison value (propagates through every downstream arithmetic, breaks
// ordering, corrupts aggregates). The validator that exists specifically to keep
// a number inside [0,100] reports NaN as VALID. This is FAIL-OPEN: accept-of-
// invalid on out-of-range input, the same high-severity class as edge admitting
// invalid trust levels.
//
// SINK-SIDE assertion: verdict must be INVALID (non-nil error). On unfixed prod
// it is nil -> this test FAILS, biting the bug.
// -----------------------------------------------------------------------------
func TestADV_REALBUG_NaNBypassesFloatRange(t *testing.T) {
	v := New()
	type S struct {
		Score float64 `validate:"min=0.0,max=100.0"`
	}
	err := v.ValidateStruct(&S{Score: math.NaN()})
	if err == nil {
		t.Fatalf("FAIL-OPEN: NaN admitted as a valid value for min=0.0,max=100.0 "+
			"(NaN<0 and NaN>100 are both false, so both guards pass); "+
			"expected ErrMinExceeded or ErrMaxExceeded, got nil")
	}
	// A correct rejection wraps one of the range sentinels.
	if !errors.Is(err, ErrMinExceeded) && !errors.Is(err, ErrMaxExceeded) {
		t.Fatalf("NaN rejected but with the wrong sentinel: %v", err)
	}
}

// +Inf is correctly rejected today: +Inf > 100.0 is true. This pins that the
// fix must NOT regress the ordered-infinity path (a valid contract today).
func TestADV_PosInfRejectedByMax(t *testing.T) {
	v := New()
	type S struct {
		Score float64 `validate:"min=0.0,max=100.0"`
	}
	if err := v.ValidateStruct(&S{Score: math.Inf(1)}); !errors.Is(err, ErrMaxExceeded) {
		t.Errorf("expected +Inf rejected by max (ErrMaxExceeded), got %v", err)
	}
}

// -Inf is correctly rejected today: -Inf < 0.0 is true. Pin it.
func TestADV_NegInfRejectedByMin(t *testing.T) {
	v := New()
	type S struct {
		Score float64 `validate:"min=0.0,max=100.0"`
	}
	if err := v.ValidateStruct(&S{Score: math.Inf(-1)}); !errors.Is(err, ErrMinExceeded) {
		t.Errorf("expected -Inf rejected by min (ErrMinExceeded), got %v", err)
	}
}

// NaN with only an upper bound (max, no min) must also be rejected once fixed —
// a NaN is by definition outside any declared numeric bound.
func TestADV_REALBUG_NaNBypassesMaxOnly(t *testing.T) {
	v := New()
	type S struct {
		Ratio float64 `validate:"max=1.0"`
	}
	if err := v.ValidateStruct(&S{Ratio: math.NaN()}); err == nil {
		t.Fatalf("FAIL-OPEN: NaN admitted for max=1.0 (NaN>1 is false); expected rejection, got nil")
	}
}

// NaN with only a lower bound (min, no max) must also be rejected once fixed.
func TestADV_REALBUG_NaNBypassesMinOnly(t *testing.T) {
	v := New()
	type S struct {
		Weight float64 `validate:"min=0.0"`
	}
	if err := v.ValidateStruct(&S{Weight: math.NaN()}); err == nil {
		t.Fatalf("FAIL-OPEN: NaN admitted for min=0.0 (NaN<0 is false); expected rejection, got nil")
	}
}

// -----------------------------------------------------------------------------
// (b) BOUNDARY — exactly-at-limit must be ACCEPTED (`<`/`>` are inclusive-edge).
// These pin the documented inclusive semantics so a fix that flips to `<=`/`>=`
// (which would wrongly reject the edge) is caught.
// -----------------------------------------------------------------------------
func TestADV_BoundaryFloatExactEdgesAccepted(t *testing.T) {
	v := New()
	type S struct {
		Score float64 `validate:"min=0.0,max=100.0"`
	}
	if err := v.ValidateStruct(&S{Score: 0.0}); err != nil {
		t.Errorf("min edge 0.0 must be accepted (>=), got %v", err)
	}
	if err := v.ValidateStruct(&S{Score: 100.0}); err != nil {
		t.Errorf("max edge 100.0 must be accepted (<=), got %v", err)
	}
}

func TestADV_BoundaryIntExactEdgesAccepted(t *testing.T) {
	v := New()
	type S struct {
		Age int `validate:"min=18,max=65"`
	}
	if err := v.ValidateStruct(&S{Age: 18}); err != nil {
		t.Errorf("min edge 18 must be accepted, got %v", err)
	}
	if err := v.ValidateStruct(&S{Age: 65}); err != nil {
		t.Errorf("max edge 65 must be accepted, got %v", err)
	}
}

func TestADV_BoundaryStringLenExactEdgesAccepted(t *testing.T) {
	v := New()
	type S struct {
		Code string `validate:"min=2,max=5"`
	}
	if err := v.ValidateStruct(&S{Code: "AB"}); err != nil {
		t.Errorf("string len min edge 2 must be accepted, got %v", err)
	}
	if err := v.ValidateStruct(&S{Code: "ABCDE"}); err != nil {
		t.Errorf("string len max edge 5 must be accepted, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// (a) FAIL-OPEN — whitespace-only required field is correctly REJECTED.
// checkRule trims before the empty test, so "   " on a required field is
// rejected. Pin that contract (a fix must not regress it).
// -----------------------------------------------------------------------------
func TestADV_WhitespaceOnlyRequiredRejected(t *testing.T) {
	v := New()
	type S struct {
		Name string `validate:"required"`
	}
	if err := v.ValidateStruct(&S{Name: "   \t\n"}); !errors.Is(err, ErrRequired) {
		t.Errorf("whitespace-only required field must be rejected, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// (a) FAIL-OPEN probe — empty string against `min=N` string-length guard.
// An EMPTY string has length 0 < any positive min, so it must be rejected even
// WITHOUT `required`. This proves min= alone is a real length floor.
// -----------------------------------------------------------------------------
func TestADV_EmptyStringFailsMinLength(t *testing.T) {
	v := New()
	type S struct {
		Token string `validate:"min=8"`
	}
	if err := v.ValidateStruct(&S{Token: ""}); !errors.Is(err, ErrMinExceeded) {
		t.Errorf("empty string must fail min=8 length floor, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// (c) IDENTITY / normalization — oneof is case-SENSITIVE and exact.
// Documented behavior: strVal == a, byte-exact. "ACTIVE" must NOT match
// "active". Pin it so nobody silently loosens it to a case-insensitive compare
// (which would admit a different identity than the policy author allowed).
// -----------------------------------------------------------------------------
func TestADV_OneOfCaseSensitiveRejectsWrongCase(t *testing.T) {
	v := New()
	type S struct {
		Status string `validate:"oneof=active inactive"`
	}
	if err := v.ValidateStruct(&S{Status: "ACTIVE"}); !errors.Is(err, ErrOneOf) {
		t.Errorf("oneof is case-sensitive: 'ACTIVE' must not match 'active', got %v", err)
	}
	// Leading/trailing whitespace must not be silently trimmed into a match.
	if err := v.ValidateStruct(&S{Status: " active"}); !errors.Is(err, ErrOneOf) {
		t.Errorf("oneof must not trim: ' active' != 'active', got %v", err)
	}
}

// -----------------------------------------------------------------------------
// (a) FAIL-OPEN — empty value against oneof (no required tag).
// strVal "" is not in {active,inactive}, so it must be rejected. Proves oneof
// alone rejects the zero value rather than failing open.
// -----------------------------------------------------------------------------
func TestADV_OneOfEmptyValueRejected(t *testing.T) {
	v := New()
	type S struct {
		Status string `validate:"oneof=active inactive"`
	}
	if err := v.ValidateStruct(&S{Status: ""}); !errors.Is(err, ErrOneOf) {
		t.Errorf("empty value must not satisfy oneof, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// (a) FAIL-OPEN — malformed email shapes must all be rejected.
// -----------------------------------------------------------------------------
func TestADV_EmailMalformedRejected(t *testing.T) {
	v := New()
	type S struct {
		Email string `validate:"email"`
	}
	for _, bad := range []string{"", "   ", "@", "@example.com", "user@", "plainstring"} {
		if err := v.ValidateStruct(&S{Email: bad}); !errors.Is(err, ErrInvalidEmail) {
			t.Errorf("malformed email %q must be rejected, got %v", bad, err)
		}
	}
}

// -----------------------------------------------------------------------------
// (a) FAIL-OPEN — UUID guard rejects near-miss / wrong-length / empty.
// -----------------------------------------------------------------------------
func TestADV_UUIDMalformedRejected(t *testing.T) {
	v := New()
	type S struct {
		ID string `validate:"uuid"`
	}
	bads := []string{
		"",
		"550e8400-e29b-41d4-a716-44665544000",   // 11 trailing digits (one short)
		"550e8400-e29b-41d4-a716-4466554400000", // one long
		"550e8400e29b41d4a716446655440000",      // no dashes
		"g50e8400-e29b-41d4-a716-446655440000",  // non-hex 'g'
		" 550e8400-e29b-41d4-a716-446655440000", // leading space (anchored regex)
	}
	for _, bad := range bads {
		if err := v.ValidateStruct(&S{ID: bad}); !errors.Is(err, ErrInvalidUUID) {
			t.Errorf("malformed UUID %q must be rejected, got %v", bad, err)
		}
	}
}

// -----------------------------------------------------------------------------
// (d) UNGUARDED SHARED STATE — the validator's rule map.
// New() builds a fresh map per validator; RegisterRule writes to it. If a single
// *Validator is shared and ValidateStruct'd concurrently while also being
// registered, that's a data race. Here we exercise the read path (ValidateStruct
// reading v.rules via checkRule's default branch) concurrently to surface any
// race in the shared read path under -race. Pure concurrent reads of a map are
// safe in Go; this asserts no HIDDEN write occurs on the validate path.
// -----------------------------------------------------------------------------
func TestADV_ConcurrentValidateNoRace(t *testing.T) {
	v := New()
	v.RegisterRule("noop", func(string) error { return nil })
	type S struct {
		Name string `validate:"required,noop"`
		Age  int    `validate:"min=1,max=10"`
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = v.ValidateStruct(&S{Name: "ok", Age: 5})
		}()
	}
	wg.Wait()
}
