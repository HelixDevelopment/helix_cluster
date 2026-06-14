package openapivalidate

import (
	"errors"
	"testing"
)

// This adversarial suite pins the EXACT contract the package claims (see the
// package doc): a request is admitted iff (a) its method+path route is known
// AND (b) every required field is present with the correct type. The package
// does NOT model enum, range, or additionalProperties-forbidding — so those are
// deliberately NOT asserted here (a field not listed in Required is permitted).
//
// The dominant risk for an admission gate is FAIL-OPEN VALIDATION: an invalid
// request that the validator WRONGLY ADMITS. Every reject-case below is written
// so that the canonical fail-open mutations (required check always passes; type
// check defaults to valid; route check skipped; Validate short-circuits to nil)
// flip the asserted error to nil and FAIL the test.

// advSpec is a richer route table than newTestSpec: it exercises one field of
// every supported type, a second route that shares a path under a different
// method, and a route whose body has NO required fields (additional-properties
// baseline).
func advSpec() *Spec {
	return NewSpec(
		RouteSpec{
			Method: "POST",
			Path:   "/accounts",
			Body: Schema{Required: map[string]FieldType{
				"username": TypeString,
				"balance":  TypeNumber,
				"verified": TypeBool,
			}},
		},
		// Same path, different method — must be keyed independently.
		RouteSpec{
			Method: "PUT",
			Path:   "/accounts",
			Body: Schema{Required: map[string]FieldType{
				"id": TypeNumber,
			}},
		},
		// A route with no required fields: any (even nil/empty) body is
		// schema-conformant; extra fields are permitted by design.
		RouteSpec{
			Method: "GET",
			Path:   "/health",
			Body:   Schema{},
		},
	)
}

// --- BASELINE: genuinely-valid requests are ADMITTED -------------------------
// Catches an always-reject mutation (and proves the suite is not vacuous).

func TestAdv_ValidRequestsAdmitted(t *testing.T) {
	id := runUUID(t)
	spec := advSpec()
	cases := []struct {
		name string
		req  Request
	}{
		{
			name: "all required fields present, correct types",
			req: Request{Method: "POST", Path: "/accounts",
				Body: map[string]any{"username": "ada", "balance": 100, "verified": true}},
		},
		{
			name: "extra/unknown field permitted (additionalProperties allowed)",
			req: Request{Method: "POST", Path: "/accounts",
				Body: map[string]any{"username": "ada", "balance": 100, "verified": true, "nickname": "countess"}},
		},
		{
			name: "second method on shared path",
			req: Request{Method: "PUT", Path: "/accounts",
				Body: map[string]any{"id": int64(7)}},
		},
		{
			name: "no-required-fields route with nil body",
			req:  Request{Method: "GET", Path: "/health", Body: nil},
		},
		{
			name: "no-required-fields route with extra fields",
			req: Request{Method: "GET", Path: "/health",
				Body: map[string]any{"anything": "goes"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := Validate(spec, tc.req); err != nil {
				t.Fatalf("run=%s case=%q expected admission (nil), got %v", id, tc.name, err)
			}
			t.Logf("run=%s case=%q admitted", id, tc.name)
		})
	}
}

// --- REQUIRED: each required field, omitted one-at-a-time, is REJECTED --------
// Centerpiece #1. Beyond the single existing case, this covers EVERY required
// field of EVERY route independently. A required-check-always-passes mutation
// admits these and fails.

func TestAdv_EachRequiredFieldOmittedRejected(t *testing.T) {
	id := runUUID(t)
	spec := advSpec()

	type routeBase struct {
		method string
		path   string
		base   map[string]any
	}
	routes := []routeBase{
		{"POST", "/accounts", map[string]any{"username": "ada", "balance": 100, "verified": true}},
		{"PUT", "/accounts", map[string]any{"id": int64(7)}},
	}

	for _, rb := range routes {
		// Establish the base is genuinely valid (so omission is the only change).
		if err := Validate(spec, Request{Method: rb.method, Path: rb.path, Body: clone(rb.base)}); err != nil {
			t.Fatalf("run=%s %s %s base expected admit, got %v", id, rb.method, rb.path, err)
		}
		for field := range rb.base {
			missing := clone(rb.base)
			delete(missing, field)
			err := Validate(spec, Request{Method: rb.method, Path: rb.path, Body: missing})
			if !errors.Is(err, ErrSchemaViolation) {
				t.Fatalf("run=%s %s %s omitting %q expected ErrSchemaViolation, got %v",
					id, rb.method, rb.path, field, err)
			}
			t.Logf("run=%s %s %s omitting %q rejected: %v", id, rb.method, rb.path, field, err)
		}
	}
}

// --- TYPE: a wrong-type value for each typed field is REJECTED ----------------
// Centerpiece #2. For each required field, substitute a value of every OTHER
// type (and nil) and assert rejection. A type-check-defaults-to-valid mutation
// admits these and fails.

func TestAdv_WrongTypePerFieldRejected(t *testing.T) {
	id := runUUID(t)
	spec := advSpec()
	base := map[string]any{"username": "ada", "balance": 100, "verified": true}

	// For each field, a set of values whose Go type does NOT satisfy the
	// field's declared FieldType.
	wrongByField := map[string][]any{
		// username is TypeString: numbers, bools, nil are all wrong.
		"username": {123, int64(1), 3.14, true, nil},
		// balance is TypeNumber: strings, bools, nil are wrong.
		"balance": {"100", "lots", true, nil},
		// verified is TypeBool: strings, numbers, nil are wrong.
		"verified": {"true", "yes", 1, 0, nil},
	}

	for field, wrongs := range wrongByField {
		for _, wv := range wrongs {
			corrupt := clone(base)
			corrupt[field] = wv
			err := Validate(spec, Request{Method: "POST", Path: "/accounts", Body: corrupt})
			if !errors.Is(err, ErrSchemaViolation) {
				t.Fatalf("run=%s field=%q value=%#v(%T) expected ErrSchemaViolation, got %v",
					id, field, wv, wv, err)
			}
			t.Logf("run=%s field=%q wrong value %#v(%T) rejected", id, field, wv, wv)
		}
	}
}

// --- EMPTY / NIL body when fields are required -> REJECTED, no panic ----------

func TestAdv_EmptyAndNilBodyRejectedNoPanic(t *testing.T) {
	id := runUUID(t)
	spec := advSpec()
	cases := []struct {
		name string
		body map[string]any
	}{
		{"nil body", nil},
		{"empty object", map[string]any{}},
		{"only-extra fields, no required", map[string]any{"unrelated": "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Must not panic on a nil map read; must reject for missing required.
			err := Validate(spec, Request{Method: "POST", Path: "/accounts", Body: tc.body})
			if !errors.Is(err, ErrSchemaViolation) {
				t.Fatalf("run=%s case=%q expected ErrSchemaViolation, got %v", id, tc.name, err)
			}
			t.Logf("run=%s case=%q rejected: %v", id, tc.name, err)
		})
	}
}

// --- ROUTE: unknown route / wrong method / nil spec -> REJECTED ---------------
// Asserts the route gate even when the body would satisfy SOME route's schema.

func TestAdv_RouteGateRejects(t *testing.T) {
	id := runUUID(t)
	spec := advSpec()

	t.Run("unknown path with otherwise-valid body", func(t *testing.T) {
		err := Validate(spec, Request{Method: "POST", Path: "/admin",
			Body: map[string]any{"username": "ada", "balance": 100, "verified": true}})
		if !errors.Is(err, ErrUnknownRoute) {
			t.Fatalf("run=%s expected ErrUnknownRoute, got %v", id, err)
		}
		t.Logf("run=%s unknown path rejected: %v", id, err)
	})

	t.Run("known path, unregistered method", func(t *testing.T) {
		// DELETE /accounts is not registered (POST and PUT are).
		err := Validate(spec, Request{Method: "DELETE", Path: "/accounts",
			Body: map[string]any{"username": "ada", "balance": 100, "verified": true}})
		if !errors.Is(err, ErrUnknownRoute) {
			t.Fatalf("run=%s expected ErrUnknownRoute, got %v", id, err)
		}
		t.Logf("run=%s wrong method rejected: %v", id, err)
	})

	t.Run("method case sensitivity (post != POST)", func(t *testing.T) {
		// Route keys are exact strings; lowercase "post" must NOT match "POST".
		err := Validate(spec, Request{Method: "post", Path: "/accounts",
			Body: map[string]any{"username": "ada", "balance": 100, "verified": true}})
		if !errors.Is(err, ErrUnknownRoute) {
			t.Fatalf("run=%s expected ErrUnknownRoute for method 'post', got %v", id, err)
		}
		t.Logf("run=%s case-confused method rejected: %v", id, err)
	})

	t.Run("path case/format confusion (/Accounts != /accounts)", func(t *testing.T) {
		err := Validate(spec, Request{Method: "POST", Path: "/Accounts",
			Body: map[string]any{"username": "ada", "balance": 100, "verified": true}})
		if !errors.Is(err, ErrUnknownRoute) {
			t.Fatalf("run=%s expected ErrUnknownRoute for '/Accounts', got %v", id, err)
		}
		t.Logf("run=%s case-confused path rejected: %v", id, err)
	})

	t.Run("nil spec rejected, no panic", func(t *testing.T) {
		err := Validate(nil, Request{Method: "POST", Path: "/accounts",
			Body: map[string]any{"username": "ada", "balance": 100, "verified": true}})
		if !errors.Is(err, ErrUnknownRoute) {
			t.Fatalf("run=%s expected ErrUnknownRoute for nil spec, got %v", id, err)
		}
		t.Logf("run=%s nil spec rejected: %v", id, err)
	})
}

// --- NUMBER boundary/format: confirm the numeric set is exactly as documented -
// TypeNumber accepts int/uint variants and float32/float64; it must reject
// numeric-looking STRINGS (a classic fail-open confusion) and bool.

func TestAdv_NumberAcceptsNumericRejectsNumericString(t *testing.T) {
	id := runUUID(t)
	spec := advSpec()

	good := []any{
		0, -1, int8(1), int16(2), int32(3), int64(4),
		uint(5), uint8(6), uint16(7), uint32(8), uint64(9),
		float32(1.5), float64(-2.5),
	}
	for _, v := range good {
		body := map[string]any{"username": "ada", "balance": v, "verified": true}
		if err := Validate(spec, Request{Method: "POST", Path: "/accounts", Body: body}); err != nil {
			t.Fatalf("run=%s balance=%#v(%T) expected admit, got %v", id, v, v, err)
		}
	}
	t.Logf("run=%s all numeric variants admitted for TypeNumber", id)

	// A numeric-looking string must NOT be coerced into a number.
	bad := map[string]any{"username": "ada", "balance": "100", "verified": true}
	if err := Validate(spec, Request{Method: "POST", Path: "/accounts", Body: bad}); !errors.Is(err, ErrSchemaViolation) {
		t.Fatalf("run=%s balance=\"100\"(string) expected ErrSchemaViolation, got %v", id, err)
	}
	t.Logf("run=%s numeric string rejected (no coercion)", id)
}
