// Package openapivalidate implements a deterministic, self-contained
// OpenAPI-style request-validation admission middleware.
//
// It defines its own minimal schema model (it does NOT import any OpenAPI
// parser). A Spec declares a set of known routes, each keyed by HTTP method
// plus path and carrying a simple body Schema (required fields and their
// expected types). Validate admits a Request only when:
//
//	(a) the route (method+path) is KNOWN, and
//	(b) the body satisfies the route's schema: every required field is
//	    present AND has the correct type.
//
// An unknown route is rejected with ErrUnknownRoute. A malformed body
// (a missing required field or a field of the wrong type) is rejected with
// ErrSchemaViolation. A conformant request is admitted (nil error).
package openapivalidate

import (
	"errors"
	"fmt"
)

// Sentinel errors returned by Validate. Callers compare with errors.Is.
var (
	// ErrUnknownRoute is returned when no route in the Spec matches the
	// request's method and path.
	ErrUnknownRoute = errors.New("openapivalidate: unknown route")
	// ErrSchemaViolation is returned when a known route's body fails its
	// schema: a required field is missing or has the wrong type.
	ErrSchemaViolation = errors.New("openapivalidate: schema violation")
)

// FieldType enumerates the supported body field types.
type FieldType int

const (
	// TypeString matches a Go string value.
	TypeString FieldType = iota
	// TypeNumber matches a numeric value (int variants or float64,
	// the latter being how JSON numbers decode in Go).
	TypeNumber
	// TypeBool matches a Go bool value.
	TypeBool
)

func (t FieldType) String() string {
	switch t {
	case TypeString:
		return "string"
	case TypeNumber:
		return "number"
	case TypeBool:
		return "bool"
	default:
		return fmt.Sprintf("FieldType(%d)", int(t))
	}
}

// Schema is a minimal body schema: a set of required field names mapped to
// their expected types. A field that is not listed here is ignored by
// validation (additional properties are permitted).
type Schema struct {
	Required map[string]FieldType
}

// RouteSpec defines one known route plus the schema its body must satisfy.
type RouteSpec struct {
	Method string
	Path   string
	Body   Schema
}

// Spec is a collection of known routes. Build one with NewSpec.
type Spec struct {
	routes map[routeKey]Schema
}

type routeKey struct {
	method string
	path   string
}

// NewSpec builds a Spec from the given route specifications. Later routes
// with the same method+path override earlier ones (deterministic).
func NewSpec(routes ...RouteSpec) *Spec {
	s := &Spec{routes: make(map[routeKey]Schema, len(routes))}
	for _, r := range routes {
		s.routes[routeKey{method: r.Method, path: r.Path}] = r.Body
	}
	return s
}

// Request is an incoming request to validate.
type Request struct {
	Method string
	Path   string
	Body   map[string]any
}

// Validate admits req against spec. It returns nil if the route is known and
// the body satisfies the route schema; ErrUnknownRoute if no route matches;
// ErrSchemaViolation (wrapped with a reason) if the body is malformed.
func Validate(spec *Spec, req Request) error {
	if spec == nil {
		return fmt.Errorf("%w: nil spec", ErrUnknownRoute)
	}
	// Route check (clause: skipping this must admit an unknown route).
	schema, ok := spec.routes[routeKey{method: req.Method, path: req.Path}]
	if !ok {
		return fmt.Errorf("%w: %s %s", ErrUnknownRoute, req.Method, req.Path)
	}
	// Schema check (clause: skipping the required-field/type check must
	// admit a malformed body).
	for field, want := range schema.Required {
		val, present := req.Body[field]
		if !present {
			return fmt.Errorf("%w: missing required field %q", ErrSchemaViolation, field)
		}
		if !matchesType(val, want) {
			return fmt.Errorf("%w: field %q expected %s, got %T",
				ErrSchemaViolation, field, want, val)
		}
	}
	return nil
}

// matchesType reports whether val conforms to the expected FieldType.
func matchesType(val any, want FieldType) bool {
	switch want {
	case TypeString:
		_, ok := val.(string)
		return ok
	case TypeNumber:
		switch val.(type) {
		case int, int8, int16, int32, int64,
			uint, uint8, uint16, uint32, uint64,
			float32, float64:
			return true
		default:
			return false
		}
	case TypeBool:
		_, ok := val.(bool)
		return ok
	default:
		return false
	}
}
