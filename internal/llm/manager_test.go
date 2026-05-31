package llm

import (
	"testing"
)

func TestManagerRegisterAndList(t *testing.T) {
	m := NewManager()
	if err := m.RegisterModel("gpt-stub", "/models/gpt", "gguf"); err != nil {
		t.Fatal(err)
	}
	models := m.ListModels()
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	if models[0].Name != "gpt-stub" {
		t.Errorf("name = %q, want gpt-stub", models[0].Name)
	}
}

func TestManagerRegisterEmptyName(t *testing.T) {
	m := NewManager()
	if err := m.RegisterModel("", "/models/x", "gguf"); err == nil {
		t.Error("expected error for empty name")
	}
}

func TestManagerLoadUnload(t *testing.T) {
	m := NewManager()
	_ = m.RegisterModel("m1", "/models/m1", "gguf")

	if err := m.LoadModel("m1"); err != nil {
		t.Fatal(err)
	}
	if m.ListModels()[0].LoadedAt == nil {
		t.Error("expected LoadedAt to be set")
	}

	if err := m.UnloadModel("m1"); err != nil {
		t.Fatal(err)
	}
	if m.ListModels()[0].LoadedAt != nil {
		t.Error("expected LoadedAt to be nil after unload")
	}
}

func TestManagerInference(t *testing.T) {
	m := NewManager()
	_ = m.RegisterModel("m1", "/models/m1", "gguf")

	resp, err := m.Inference("m1", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if resp == "" {
		t.Error("expected non-empty response")
	}
}

func TestManagerInferenceMissingModel(t *testing.T) {
	m := NewManager()
	_, err := m.Inference("missing", "hello")
	if err == nil {
		t.Error("expected error for missing model")
	}
}
