package serde

import "testing"

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
	data := MustMarshal(map[string]string{"k": "v"})
	if len(data) == 0 {
		t.Error("expected non-empty data")
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
