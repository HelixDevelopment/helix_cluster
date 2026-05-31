package llm

import (
	"strings"
	"sync"
	"testing"
	"time"
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
	if err := m.LoadModel("m1"); err != nil {
		t.Fatal(err)
	}

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

// TestManagerInferenceRequiresLoaded proves Inference refuses to fabricate a
// response for a registered-but-unloaded model — the core usability guarantee.
// Mutation that fails this: in Inference, change `if !loaded {` to `if false {`.
func TestManagerInferenceRequiresLoaded(t *testing.T) {
	m := NewManager()
	if err := m.RegisterModel("m1", "/models/m1", "gguf"); err != nil {
		t.Fatal(err)
	}
	resp, err := m.Inference("m1", "hello")
	if err == nil {
		t.Fatalf("expected error for unloaded model, got response %q", resp)
	}
	if resp != "" {
		t.Errorf("expected empty response on error, got %q", resp)
	}
	if !strings.Contains(err.Error(), "not loaded") {
		t.Errorf("error = %q, want it to mention 'not loaded'", err.Error())
	}
	// After loading, the same call must succeed.
	if err := m.LoadModel("m1"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Inference("m1", "hello"); err != nil {
		t.Errorf("expected success after load, got %v", err)
	}
}

// TestManagerInferenceEmptyPrompt proves an empty prompt is rejected.
// Mutation that fails this: delete the `if prompt == ""` guard in Inference.
func TestManagerInferenceEmptyPrompt(t *testing.T) {
	m := NewManager()
	_ = m.RegisterModel("m1", "/models/m1", "gguf")
	_ = m.LoadModel("m1")
	if _, err := m.Inference("m1", ""); err == nil {
		t.Error("expected error for empty prompt")
	}
}

// TestManagerInferenceEchoesPromptAndName proves the response is derived from
// the actual inputs, not a constant — sink-side evidence of the call.
// Mutation that fails this: make Inference return a fixed string "ok".
func TestManagerInferenceEchoesPromptAndName(t *testing.T) {
	m := NewManager()
	_ = m.RegisterModel("alpha", "/models/alpha", "gguf")
	_ = m.LoadModel("alpha")
	resp, err := m.Inference("alpha", "what is 2+2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp, "alpha") {
		t.Errorf("response %q does not reference model name", resp)
	}
	if !strings.Contains(resp, "what is 2+2") {
		t.Errorf("response %q does not reference prompt", resp)
	}
}

// TestManagerRegisterEmptyPath proves a path is required.
// Mutation that fails this: delete the `if path == ""` guard in RegisterModel.
func TestManagerRegisterEmptyPath(t *testing.T) {
	m := NewManager()
	if err := m.RegisterModel("m1", "", "gguf"); err == nil {
		t.Error("expected error for empty path")
	}
	if len(m.ListModels()) != 0 {
		t.Error("model with empty path must not be registered")
	}
}

// TestManagerReRegisterPreservesLoadState proves re-registering an already
// loaded model updates metadata but does NOT wipe its runtime load state.
// Mutation that fails this: in RegisterModel, replace the existing-update
// branch body with `m.models[name] = &Model{Name: name, Path: path, Format: format}`.
func TestManagerReRegisterPreservesLoadState(t *testing.T) {
	m := NewManager()
	_ = m.RegisterModel("m1", "/old/path", "gguf")
	if err := m.LoadModel("m1"); err != nil {
		t.Fatal(err)
	}
	// Re-register with new path/format.
	if err := m.RegisterModel("m1", "/new/path", "safetensors"); err != nil {
		t.Fatal(err)
	}
	got, ok := m.GetModel("m1")
	if !ok {
		t.Fatal("model disappeared after re-register")
	}
	if got.Path != "/new/path" || got.Format != "safetensors" {
		t.Errorf("metadata not updated: path=%q format=%q", got.Path, got.Format)
	}
	if got.LoadedAt == nil {
		t.Error("load state was wiped by re-register; loaded model torn down silently")
	}
	if got.LoadCount != 1 {
		t.Errorf("LoadCount = %d, want 1 preserved across re-register", got.LoadCount)
	}
	if !m.IsLoaded("m1") {
		t.Error("IsLoaded should report true after re-register of loaded model")
	}
}

// TestManagerLoadCountIncrements proves repeated loads accumulate.
// Mutation that fails this: change `model.LoadCount++` to a no-op in LoadModel.
func TestManagerLoadCountIncrements(t *testing.T) {
	m := NewManager()
	_ = m.RegisterModel("m1", "/models/m1", "gguf")
	for i := 0; i < 3; i++ {
		if err := m.LoadModel("m1"); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := m.GetModel("m1")
	if got.LoadCount != 3 {
		t.Errorf("LoadCount = %d, want 3", got.LoadCount)
	}
}

// TestManagerUnregisterModel proves removal works and is reported, and that
// removing an unknown model is an error (not a silent no-op).
// Mutation that fails this: make UnregisterModel always `return nil`.
func TestManagerUnregisterModel(t *testing.T) {
	m := NewManager()
	_ = m.RegisterModel("m1", "/models/m1", "gguf")
	if err := m.UnregisterModel("m1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.GetModel("m1"); ok {
		t.Error("model still present after UnregisterModel")
	}
	if err := m.UnregisterModel("ghost"); err == nil {
		t.Error("expected error unregistering unknown model")
	}
}

// TestManagerListModelsIsSnapshot proves ListModels returns copies so callers
// cannot corrupt internal state through the returned pointers.
// Mutation that fails this: in ListModels, append `model` instead of `model.clone()`.
func TestManagerListModelsIsSnapshot(t *testing.T) {
	m := NewManager()
	_ = m.RegisterModel("m1", "/models/m1", "gguf")
	// Forge a LoadedAt on the returned copy.
	forged := time.Now()
	m.ListModels()[0].LoadedAt = &forged
	m.ListModels()[0].LoadCount = 999
	if m.IsLoaded("m1") {
		t.Error("internal state mutated via ListModels copy; not a snapshot")
	}
	got, _ := m.GetModel("m1")
	if got.LoadCount != 0 {
		t.Errorf("LoadCount = %d, internal state mutated via copy", got.LoadCount)
	}
}

// TestManagerGetModelCopyIsolated proves GetModel returns an isolated copy.
// Mutation that fails this: in GetModel, return `model, ok` instead of `model.clone(), true`.
func TestManagerGetModelCopyIsolated(t *testing.T) {
	m := NewManager()
	_ = m.RegisterModel("m1", "/models/m1", "gguf")
	_ = m.LoadModel("m1")
	c1, _ := m.GetModel("m1")
	c1.LoadCount = 42
	*c1.LoadedAt = time.Unix(0, 0)
	c2, _ := m.GetModel("m1")
	if c2.LoadCount == 42 {
		t.Error("mutating GetModel copy leaked into internal state")
	}
	if c2.LoadedAt.Equal(time.Unix(0, 0)) {
		t.Error("mutating GetModel copy's LoadedAt leaked into internal state")
	}
}

// TestManagerConcurrentAccess exercises the RWMutex under -race across all
// mutating and reading paths. A data race fails the test under -race.
// Mutation that fails this (under -race): remove m.mu.Lock()/Unlock() in LoadModel.
func TestManagerConcurrentAccess(t *testing.T) {
	m := NewManager()
	for i := 0; i < 8; i++ {
		_ = m.RegisterModel("m"+string(rune('0'+i)), "/p", "gguf")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		name := "m" + string(rune('0'+i))
		wg.Add(3)
		go func() { defer wg.Done(); _ = m.LoadModel(name) }()
		go func() { defer wg.Done(); _ = m.RegisterModel(name, "/p2", "safetensors") }()
		go func() { defer wg.Done(); _ = m.ListModels(); _ = m.IsLoaded(name) }()
	}
	wg.Wait()
}
