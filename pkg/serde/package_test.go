package serde

import (
	"reflect"
	"testing"
)

func TestMarshalUnmarshal(t *testing.T) {
	type Item struct {
		Name string `json:"name"`
	}
	original := Item{Name: "test"}
	data, err := Marshal(original)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded Item
	if err := Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decoded.Name != original.Name {
		t.Errorf("expected %s, got %s", original.Name, decoded.Name)
	}
}

func TestMustMarshal(t *testing.T) {
	// Assert EXACT serialized content, not merely non-emptiness: garbage
	// non-empty output (e.g. arbitrary bytes) must NOT pass this test.
	data := MustMarshal(map[string]string{"k": "v"})
	const want = `{"k":"v"}`
	if string(data) != want {
		t.Errorf("expected exact JSON %s, got %s", want, string(data))
	}
}

// --- Mutation tests ---

func TestUnmarshal_RoundTrip_Mutation(t *testing.T) {
	// Mutation: Unmarshal ignores input and returns nil → round-trip breaks
	type Item struct {
		Name string `json:"name"`
	}
	original := Item{Name: "roundtrip"}
	data, err := Marshal(original)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var decoded Item
	if err := Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if decoded.Name != original.Name {
		t.Errorf("round-trip failed: expected %s, got %s", original.Name, decoded.Name)
	}
}

func TestMustMarshal_PanicsOnError_Mutation(t *testing.T) {
	// Mutation: MustMarshal returns nil instead of panicking on error
	panicked := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
			}
		}()
		// Channel cannot be marshaled to JSON
		_ = MustMarshal(make(chan int))
	}()
	if !panicked {
		t.Error("MustMarshal must panic on non-marshalable input")
	}
}

func TestMarshal_ErrorPropagated_Mutation(t *testing.T) {
	// Mutation: Marshal swallows errors and returns empty bytes
	_, err := Marshal(make(chan int))
	if err == nil {
		t.Error("Marshal must return an error for non-marshalable types")
	}
}

func TestUnmarshal_MalformedInput_Error(t *testing.T) {
	// Mutation: Unmarshal swallows the decode error and returns nil →
	// malformed JSON would be silently accepted. Feeding syntactically
	// invalid JSON MUST yield a non-nil error.
	var decoded map[string]string
	err := Unmarshal([]byte("{not json"), &decoded)
	if err == nil {
		t.Fatal("Unmarshal must return an error for malformed JSON input")
	}
}

func TestUnmarshal_TypeMismatch_Error(t *testing.T) {
	// Mutation: Unmarshal ignores type-incompatible input. A JSON string
	// decoded into an int target MUST surface a non-nil error.
	var n int
	if err := Unmarshal([]byte(`"a string"`), &n); err == nil {
		t.Error("Unmarshal must return an error when JSON type does not match target")
	}
}

func TestUnmarshal_NilTarget_Error(t *testing.T) {
	// Document the end-user-misuse contract: a nil pointer target must
	// produce an error (encoding/json returns *InvalidUnmarshalError),
	// not silently succeed.
	if err := Unmarshal([]byte(`{"k":"v"}`), nil); err == nil {
		t.Error("Unmarshal must return an error for a nil target")
	}
}

func TestUnmarshal_NonPointerTarget_Error(t *testing.T) {
	// A non-pointer target cannot be written to and must yield an error.
	var m map[string]string
	if err := Unmarshal([]byte(`{"k":"v"}`), m); err == nil {
		t.Error("Unmarshal must return an error for a non-pointer target")
	}
}

func TestMarshalUnmarshal_NestedTagged_RoundTrip(t *testing.T) {
	// Prove structural fidelity and json-tag honoring beyond a single
	// string field: nested struct, slice, map, omitempty, and json:"-".
	type Inner struct {
		Labels map[string]int `json:"labels"`
		Tags   []string       `json:"tags,omitempty"`
	}
	type Outer struct {
		ID       int    `json:"id"`
		Renamed  string `json:"display_name"`
		Inner    Inner  `json:"inner"`
		Secret   string `json:"-"`
		Optional string `json:"optional,omitempty"`
	}

	original := Outer{
		ID:      42,
		Renamed: "helix",
		Inner: Inner{
			Labels: map[string]int{"a": 1, "b": 2},
			Tags:   []string{"x", "y"},
		},
		Secret: "do-not-serialize",
	}

	data, err := Marshal(original)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	// json:"-" field must be absent; renamed tag must be honored;
	// omitempty empty field must be omitted.
	got := string(data)
	want := `{"id":42,"display_name":"helix","inner":{"labels":{"a":1,"b":2},"tags":["x","y"]}}`
	if got != want {
		t.Errorf("tag-honoring serialization mismatch:\n got: %s\nwant: %s", got, want)
	}

	var decoded Outer
	if err := Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	// Secret is never serialized, so it cannot round-trip; compare the rest.
	want2 := original
	want2.Secret = ""
	if !reflect.DeepEqual(decoded, want2) {
		t.Errorf("nested round-trip mismatch:\n got: %+v\nwant: %+v", decoded, want2)
	}
}
