package openapivalidate

import (
	"crypto/rand"
	"errors"
	"fmt"
	"testing"
)

// runUUID returns a random run identifier for forensic logging. It is used
// only for log correlation and never participates in assertion logic.
func runUUID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// newTestSpec builds the spec used across the closure tests: one POST route
// whose body requires a string "name", a number "age", and a bool "active".
func newTestSpec() *Spec {
	return NewSpec(RouteSpec{
		Method: "POST",
		Path:   "/users",
		Body: Schema{Required: map[string]FieldType{
			"name":   TypeString,
			"age":    TypeNumber,
			"active": TypeBool,
		}},
	})
}

// Clause 1: a VALID request (known route, schema-conformant body) is ADMITTED.
// Mutation: if Validate drops the route OR the schema check and returns an
// error for conformant input, this asserts nil and fails.
func TestValidRequestAdmitted(t *testing.T) {
	id := runUUID(t)
	spec := newTestSpec()
	req := Request{
		Method: "POST",
		Path:   "/users",
		Body:   map[string]any{"name": "ada", "age": 36, "active": true},
	}
	t.Logf("run=%s before: admitting req method=%s path=%s body=%v", id, req.Method, req.Path, req.Body)

	err := Validate(spec, req)
	if err != nil {
		t.Fatalf("run=%s expected admission (nil), got %v", id, err)
	}
	t.Logf("run=%s after: admitted=true err=nil", id)
}

// Clause 2a: a malformed body with a MISSING required field is REJECTED with
// ErrSchemaViolation.
// Mutation: skipping the required-field presence check would admit this body,
// flipping err to nil and failing the errors.Is assertion.
func TestMissingRequiredFieldRejected(t *testing.T) {
	id := runUUID(t)
	spec := newTestSpec()
	// "active" is omitted.
	req := Request{
		Method: "POST",
		Path:   "/users",
		Body:   map[string]any{"name": "ada", "age": 36},
	}
	t.Logf("run=%s before: received req body=%v (missing 'active')", id, req.Body)

	err := Validate(spec, req)
	if !errors.Is(err, ErrSchemaViolation) {
		t.Fatalf("run=%s expected ErrSchemaViolation, got %v", id, err)
	}
	t.Logf("run=%s after: rejected=true reason=%q", id, err.Error())
}

// Clause 2b: a malformed body with a WRONG-TYPE field is REJECTED with
// ErrSchemaViolation.
// Mutation: skipping the type check would admit a string where a number is
// required, flipping err to nil and failing.
func TestWrongTypeFieldRejected(t *testing.T) {
	id := runUUID(t)
	spec := newTestSpec()
	// "age" is a string instead of a number.
	req := Request{
		Method: "POST",
		Path:   "/users",
		Body:   map[string]any{"name": "ada", "age": "thirty-six", "active": true},
	}
	t.Logf("run=%s before: received req body=%v ('age' wrong type)", id, req.Body)

	err := Validate(spec, req)
	if !errors.Is(err, ErrSchemaViolation) {
		t.Fatalf("run=%s expected ErrSchemaViolation, got %v", id, err)
	}
	t.Logf("run=%s after: rejected=true reason=%q", id, err.Error())
}

// Clause 2c: an UNKNOWN route is REJECTED with ErrUnknownRoute even when the
// body would otherwise be schema-conformant for some other route.
// Mutation: skipping the route check would fall through to schema validation
// (which passes for this body) and admit, flipping err to nil and failing.
func TestUnknownRouteRejected(t *testing.T) {
	id := runUUID(t)
	spec := newTestSpec()
	tests := []struct {
		name string
		req  Request
	}{
		{
			name: "unknown path",
			req: Request{Method: "POST", Path: "/admin",
				Body: map[string]any{"name": "ada", "age": 36, "active": true}},
		},
		{
			name: "known path wrong method",
			req: Request{Method: "GET", Path: "/users",
				Body: map[string]any{"name": "ada", "age": 36, "active": true}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("run=%s before: received req method=%s path=%s", id, tc.req.Method, tc.req.Path)
			err := Validate(spec, tc.req)
			if !errors.Is(err, ErrUnknownRoute) {
				t.Fatalf("run=%s expected ErrUnknownRoute, got %v", id, err)
			}
			t.Logf("run=%s after: rejected=true reason=%q", id, err.Error())
		})
	}
}

// Clause 3: each schema rule is independently load-bearing. Starting from a
// fully valid body, mutating exactly ONE field (drop it, or corrupt its type)
// must independently cause rejection; the baseline (untouched) must be admitted.
// Mutation: if any single required rule were not enforced, its corresponding
// case below would be wrongly admitted and fail.
func TestEachRuleIndependentlyLoadBearing(t *testing.T) {
	id := runUUID(t)
	spec := newTestSpec()
	base := map[string]any{"name": "ada", "age": 36, "active": true}

	// Baseline admitted.
	if err := Validate(spec, Request{Method: "POST", Path: "/users", Body: clone(base)}); err != nil {
		t.Fatalf("run=%s baseline expected admit, got %v", id, err)
	}
	t.Logf("run=%s baseline admitted", id)

	fields := []string{"name", "age", "active"}
	// Wrong-type substitute per field: pick a value of a type the field rejects.
	wrongVal := map[string]any{
		"name":   123,     // string field, number value
		"age":    "x",     // number field, string value
		"active": "yes",   // bool field, string value
	}

	for _, f := range fields {
		// Case: drop the field -> missing required -> reject.
		missing := clone(base)
		delete(missing, f)
		errMissing := Validate(spec, Request{Method: "POST", Path: "/users", Body: missing})
		if !errors.Is(errMissing, ErrSchemaViolation) {
			t.Fatalf("run=%s dropping %q expected ErrSchemaViolation, got %v", id, f, errMissing)
		}
		t.Logf("run=%s rule %q load-bearing on presence: reason=%q", id, f, errMissing.Error())

		// Case: corrupt the type -> wrong type -> reject.
		corrupt := clone(base)
		corrupt[f] = wrongVal[f]
		errType := Validate(spec, Request{Method: "POST", Path: "/users", Body: corrupt})
		if !errors.Is(errType, ErrSchemaViolation) {
			t.Fatalf("run=%s corrupting %q type expected ErrSchemaViolation, got %v", id, f, errType)
		}
		t.Logf("run=%s rule %q load-bearing on type: reason=%q", id, f, errType.Error())
	}
}

// TestNumberTypeAcceptsVariants confirms TypeNumber accepts int and float64
// (JSON-decoded numbers) but rejects non-numeric. Derived from matchesType.
// Mutation: removing float64 from the numeric set would reject 3.14 and fail.
func TestNumberTypeAcceptsVariants(t *testing.T) {
	id := runUUID(t)
	spec := newTestSpec()
	good := []any{36, int64(36), float64(3.14)}
	for _, v := range good {
		body := map[string]any{"name": "ada", "age": v, "active": true}
		if err := Validate(spec, Request{Method: "POST", Path: "/users", Body: body}); err != nil {
			t.Fatalf("run=%s age=%v(%T) expected admit, got %v", id, v, v, err)
		}
		t.Logf("run=%s age=%v(%T) admitted", id, v, v)
	}
	// Bool is not a number.
	bad := map[string]any{"name": "ada", "age": true, "active": true}
	if err := Validate(spec, Request{Method: "POST", Path: "/users", Body: bad}); !errors.Is(err, ErrSchemaViolation) {
		t.Fatalf("run=%s age=bool expected ErrSchemaViolation, got %v", id, err)
	}
	t.Logf("run=%s age=bool rejected as non-number", id)
}

func clone(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
