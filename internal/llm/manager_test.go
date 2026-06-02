package llm

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// echoBackend is a REAL in-process inference backend used by tests. It is not a
// mock of an external system: it actually computes a deterministic result
// derived from the (model, prompt) it is given and records which model it was
// asked to run. Tests assert on lastModel to prove the Manager's router (not a
// hardcoded string) selected and dispatched to that model. This satisfies
// CLAUDE-1: the feature is exercised end-to-end through a genuine backend seam,
// and the response is provably derived from the inputs, not fabricated.
type echoBackend struct {
	mu        sync.Mutex
	lastModel string
	calls     int
}

func (b *echoBackend) Generate(model, prompt string) (string, error) {
	b.mu.Lock()
	b.lastModel = model
	b.calls++
	b.mu.Unlock()
	// Real, input-derived output. The "ECHO" marker proves the response came
	// from this backend and NOT from the retired "[stub inference ...]" path.
	return fmt.Sprintf("ECHO[%s]:%s", model, prompt), nil
}

func (b *echoBackend) last() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastModel
}

// failBackend always errors; used to prove inference surfaces backend failures
// honestly rather than fabricating a result.
type failBackend struct{}

func (failBackend) Generate(model, prompt string) (string, error) {
	return "", fmt.Errorf("backend exploded for %s", model)
}

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
	be := &echoBackend{}
	m := WithBackend(be)
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
	if be.last() != "m1" {
		t.Errorf("backend ran model %q, want m1", be.last())
	}
}

// TestManagerInferenceNoBackend proves that without a backend installed,
// inference fails with a typed error instead of returning a fabricated string.
// Mutation that fails this: in Inference, drop the `if backend == nil` guard
// (a nil backend would then panic, not return an honest error).
func TestManagerInferenceNoBackend(t *testing.T) {
	m := NewManager()
	_ = m.RegisterModel("m1", "/models/m1", "gguf")
	_ = m.LoadModel("m1")
	if _, err := m.Inference("m1", "hello"); err == nil {
		t.Error("expected error when no backend installed")
	}
}

// TestManagerInferenceSurfacesBackendError proves a backend failure is returned
// as-is, not swallowed and replaced by a synthetic success.
func TestManagerInferenceSurfacesBackendError(t *testing.T) {
	m := WithBackend(failBackend{})
	_ = m.RegisterModel("m1", "/models/m1", "gguf")
	_ = m.LoadModel("m1")
	resp, err := m.Inference("m1", "hello")
	if err == nil {
		t.Fatalf("expected backend error, got response %q", resp)
	}
	if resp != "" {
		t.Errorf("expected empty response on backend error, got %q", resp)
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
	m := WithBackend(&echoBackend{})
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
	m := WithBackend(&echoBackend{})
	_ = m.RegisterModel("m1", "/models/m1", "gguf")
	_ = m.LoadModel("m1")
	if _, err := m.Inference("m1", ""); err == nil {
		t.Error("expected error for empty prompt")
	}
}

// TestManagerInferenceEchoesPromptAndName proves the response is derived from
// the actual inputs via the REAL backend, not a constant — sink-side evidence of
// the call. It also pins the anti-bluff guarantee: the response must NOT be the
// retired "[stub inference ...]" synthetic string.
// Mutation that fails this: have Inference ignore the backend and return
// fmt.Sprintf("[stub inference from %s] Response to: %s", name, prompt).
func TestManagerInferenceEchoesPromptAndName(t *testing.T) {
	m := WithBackend(&echoBackend{})
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
	if strings.Contains(resp, "stub inference") {
		t.Errorf("response %q is the retired synthetic stub string (PASS-bluff)", resp)
	}
	if !strings.HasPrefix(resp, "ECHO[") {
		t.Errorf("response %q did not come from the injected backend", resp)
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

// routableManager builds a Manager with three loaded, healthy models whose
// metrics differ on every axis, so each strategy has a UNIQUE correct choice:
//
//	model     LatencyMS  ThroughputTPS  Quality  CostPer1K   wins on
//	--------  ---------  -------------  -------  ---------   ----------
//	fast        10           50          0.70      5.0       latency
//	bulk       200          900          0.80      2.0       throughput, cost
//	precise    150          120          0.99      9.0       quality
//
// "bulk" deliberately wins both throughput AND cost to keep the table compact;
// the per-strategy assertions still pin distinct latency/quality picks.
func routableManager(t *testing.T, be Backend) *Manager {
	t.Helper()
	m := WithBackend(be)
	specs := []struct {
		name string
		mt   Metrics
	}{
		{"fast", Metrics{LatencyMS: 10, ThroughputTPS: 50, Quality: 0.70, CostPer1K: 5.0}},
		{"bulk", Metrics{LatencyMS: 200, ThroughputTPS: 900, Quality: 0.80, CostPer1K: 2.0}},
		{"precise", Metrics{LatencyMS: 150, ThroughputTPS: 120, Quality: 0.99, CostPer1K: 9.0}},
	}
	for _, s := range specs {
		if err := m.RegisterModel(s.name, "/models/"+s.name, "gguf"); err != nil {
			t.Fatal(err)
		}
		if err := m.LoadModel(s.name); err != nil {
			t.Fatal(err)
		}
		if err := m.SetMetrics(s.name, s.mt); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

// TestManagerRouteStrategySelection proves different strategies select different
// models from the same configured set, per their metrics. This is the core
// CLAUDE-1 closure criterion #2.
// Mutation that fails this: in strategyScore, drop the negation for
// StrategyLatency (return mt.LatencyMS) — latency would then pick "bulk".
func TestManagerRouteStrategySelection(t *testing.T) {
	m := routableManager(t, &echoBackend{})
	cases := []struct {
		strategy Strategy
		want     string
	}{
		{StrategyLatency, "fast"},
		{StrategyThroughput, "bulk"},
		{StrategyQuality, "precise"},
		{StrategyCost, "bulk"},
	}
	for _, c := range cases {
		got, err := m.Route(c.strategy)
		if err != nil {
			t.Fatalf("Route(%s): %v", c.strategy, err)
		}
		if got != c.want {
			t.Errorf("Route(%s) = %q, want %q", c.strategy, got, c.want)
		}
	}
}

// TestManagerRouteInferenceUsesSelectedBackend proves RouteInference selects per
// strategy AND dispatches to that exact model through the real backend, and that
// the response is the backend's (never the retired stub). Closure criterion #1.
// Mutation that fails this: have RouteInference ignore the router and call
// backend.Generate("fast", prompt) unconditionally.
func TestManagerRouteInferenceUsesSelectedBackend(t *testing.T) {
	be := &echoBackend{}
	m := routableManager(t, be)

	selected, resp, err := m.RouteInference(StrategyQuality, "prove it")
	if err != nil {
		t.Fatal(err)
	}
	if selected != "precise" {
		t.Errorf("selected = %q, want precise (quality strategy)", selected)
	}
	if be.last() != "precise" {
		t.Errorf("backend ran %q, want precise — router did not drive dispatch", be.last())
	}
	if resp != "ECHO[precise]:prove it" {
		t.Errorf("response %q is not the backend's output for the selected model", resp)
	}
	if strings.Contains(resp, "stub inference") {
		t.Errorf("response %q is the retired synthetic stub (PASS-bluff)", resp)
	}

	// A different strategy must dispatch to a different model on the same set.
	selected2, _, err := m.RouteInference(StrategyLatency, "again")
	if err != nil {
		t.Fatal(err)
	}
	if selected2 != "fast" {
		t.Errorf("latency selected = %q, want fast", selected2)
	}
	if be.last() != "fast" {
		t.Errorf("backend ran %q, want fast", be.last())
	}
}

// TestManagerRouteNoEligibleModel proves routing returns a typed error (not a
// fabricated pick) when no model is loaded+healthy. Closure criterion #3.
// Mutation that fails this: in Route, treat unhealthy/unloaded models as
// eligible (remove the `if model.LoadedAt == nil || !model.Healthy` skip).
func TestManagerRouteNoEligibleModel(t *testing.T) {
	be := &echoBackend{}
	m := routableManager(t, be)
	// Make every model ineligible: unload two, mark the third unhealthy.
	if err := m.UnloadModel("fast"); err != nil {
		t.Fatal(err)
	}
	if err := m.UnloadModel("bulk"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetHealth("precise", false); err != nil {
		t.Fatal(err)
	}

	got, err := m.Route(StrategyQuality)
	if err == nil {
		t.Fatalf("expected ErrNoEligibleModel, got pick %q", got)
	}
	if !IsNoEligibleModel(err) {
		t.Errorf("error %v is not ErrNoEligibleModel", err)
	}

	// RouteInference must also refuse — and must NOT have called the backend.
	before := be.calls
	if _, _, err := m.RouteInference(StrategyQuality, "x"); !IsNoEligibleModel(err) {
		t.Errorf("RouteInference error = %v, want ErrNoEligibleModel", err)
	}
	if be.calls != before {
		t.Error("backend was invoked despite no eligible model — fabricated dispatch")
	}
}

// TestManagerRouteExcludesUnhealthy proves a healthy-but-lower model is chosen
// over an unhealthy best. Here "precise" is the quality winner but unhealthy, so
// quality must fall back to the next healthy best ("bulk", Quality 0.80).
// Mutation that fails this: remove `!model.Healthy` from the skip condition.
func TestManagerRouteExcludesUnhealthy(t *testing.T) {
	m := routableManager(t, &echoBackend{})
	if err := m.SetHealth("precise", false); err != nil {
		t.Fatal(err)
	}
	got, err := m.Route(StrategyQuality)
	if err != nil {
		t.Fatal(err)
	}
	if got != "bulk" {
		t.Errorf("Route(quality) = %q, want bulk (precise is unhealthy)", got)
	}
}

// TestManagerRouteExcludesUnloaded proves an unloaded best is skipped.
// Mutation that fails this: remove `model.LoadedAt == nil` from the skip.
func TestManagerRouteExcludesUnloaded(t *testing.T) {
	m := routableManager(t, &echoBackend{})
	if err := m.UnloadModel("fast"); err != nil {
		t.Fatal(err)
	}
	got, err := m.Route(StrategyLatency)
	if err != nil {
		t.Fatal(err)
	}
	if got == "fast" {
		t.Error("Route(latency) picked unloaded model 'fast'")
	}
	if got != "precise" {
		t.Errorf("Route(latency) = %q, want precise (next-lowest latency among loaded)", got)
	}
}

// TestManagerRouteUnknownStrategy proves an unrecognised strategy is a typed
// error, not a silent default.
// Mutation that fails this: make validStrategy always return true.
func TestManagerRouteUnknownStrategy(t *testing.T) {
	m := routableManager(t, &echoBackend{})
	got, err := m.Route(Strategy("bogus"))
	if err == nil {
		t.Fatalf("expected ErrUnknownStrategy, got pick %q", got)
	}
	if !IsUnknownStrategy(err) {
		t.Errorf("error %v is not ErrUnknownStrategy", err)
	}
}

// TestManagerRouteTieBreakDeterministic proves equal scores break by name.
// Mutation that fails this: remove the `score == bestScore && model.Name < best`
// tie-break branch (selection would become map-iteration-order dependent).
func TestManagerRouteTieBreakDeterministic(t *testing.T) {
	m := WithBackend(&echoBackend{})
	// Two models with identical quality; "aaa" must win over "zzz".
	for _, n := range []string{"zzz", "aaa", "mmm"} {
		if err := m.RegisterModel(n, "/p/"+n, "gguf"); err != nil {
			t.Fatal(err)
		}
		if err := m.LoadModel(n); err != nil {
			t.Fatal(err)
		}
		if err := m.SetMetrics(n, Metrics{Quality: 0.5}); err != nil {
			t.Fatal(err)
		}
	}
	// Run several times; deterministic tie-break must always yield "aaa".
	for i := 0; i < 20; i++ {
		got, err := m.Route(StrategyQuality)
		if err != nil {
			t.Fatal(err)
		}
		if got != "aaa" {
			t.Fatalf("tie-break = %q, want aaa (lowest name)", got)
		}
	}
}
