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
