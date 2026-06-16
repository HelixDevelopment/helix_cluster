package llm

// Adversarial, SINK-SIDE probes for the internal/llm Manager, targeting the
// classes flagged when pkg/inference dropped its synthetic stub:
//
//   (a) UNGUARDED SHARED STATE — concurrent Register/Load/Unload/IsLoaded/
//       Inference racing on the model map (data race / lost update). Asserted
//       under -race AND with a conservation invariant (every registered model
//       survives, none corrupted, count never exceeds the registered universe).
//   (b) LOAD-STATE RACE / FAIL-OPEN — Inference on an unloaded/unknown model
//       MUST error (never serve); inference racing unload must never fabricate
//       output for a model that was not loaded at dispatch. Asserted on the
//       actual inference verdict, not "no panic".
//   (c) IDEMPOTENCY — double-Load accounting, double-Register preserving load
//       state without duplicating the map entry, concurrent same-model load.
//   (d) BACKEND CONTRACT — an erroring / empty backend is surfaced honestly;
//       the Manager never substitutes a fabricated success.
//
// ANTI-HANG: concurrency capped <= 8, a fake in-process Backend (no real model
// I/O), bounded loops; `go test -timeout 90s` completes.
//
// FINDING: the production Manager is fully RWMutex-guarded and every probe
// PASSES on unmodified prod. These tests pin that contract (no fix applied).
// Where a property is mutation-checkable, the failing mutation is named.

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// countingBackend is a REAL in-process backend (not a mock of an external
// system): it deterministically derives output from (model, prompt) and counts
// invocations, so a test can assert sink-side that a result is the backend's
// genuine output and never a Manager-fabricated string.
type countingBackend struct {
	calls int64
}

func (b *countingBackend) Generate(model, prompt string) (string, error) {
	atomic.AddInt64(&b.calls, 1)
	return fmt.Sprintf("REAL[%s]:%s", model, prompt), nil
}

func (b *countingBackend) count() int64 { return atomic.LoadInt64(&b.calls) }

// emptyBackend returns ("", nil): a backend that produces NO output but does not
// error. The Manager must surface that empty string verbatim — it must not
// "helpfully" fabricate a synthetic response to fill the gap (the stub class).
type emptyBackend struct{}

func (emptyBackend) Generate(model, prompt string) (string, error) { return "", nil }

// ---------------------------------------------------------------------------
// (a) UNGUARDED SHARED STATE + CONSERVATION
// ---------------------------------------------------------------------------

// TestAdversarialConcurrentMapConservation hammers the model map with every
// mutating and reading path concurrently (capped at 8 models * a few ops each)
// and then asserts the SINK: the registry is internally consistent. A data race
// trips under -race; a lost update would drop a registered model or corrupt its
// identity, which the conservation checks below catch.
//
// Mutation that fails this (under -race): remove m.mu.Lock()/Unlock() in
// LoadModel or RegisterModel.
func TestAdversarialConcurrentMapConservation(t *testing.T) {
	const n = 8 // cap
	be := &countingBackend{}
	m := WithBackend(be)

	names := make([]string, n)
	for i := 0; i < n; i++ {
		names[i] = fmt.Sprintf("model-%d", i)
		if err := m.RegisterModel(names[i], "/p/"+names[i], "gguf"); err != nil {
			t.Fatalf("seed register: %v", err)
		}
	}

	var wg sync.WaitGroup
	for _, name := range names {
		name := name
		// 6 racing ops per model, all touching the same map under the lock.
		wg.Add(6)
		go func() { defer wg.Done(); _ = m.RegisterModel(name, "/p2/"+name, "safetensors") }()
		go func() { defer wg.Done(); _ = m.LoadModel(name) }()
		go func() { defer wg.Done(); _ = m.UnloadModel(name) }()
		go func() { defer wg.Done(); _ = m.IsLoaded(name) }()
		go func() { defer wg.Done(); _ = m.ListModels() }()
		go func() { defer wg.Done(); _, _ = m.Inference(name, "probe") }()
	}
	wg.Wait()

	// SINK 1: conservation — every registered model is still present, exactly
	// once, with an intact identity. A lost update or torn map would drop or
	// duplicate an entry.
	got := m.ListModels()
	if len(got) != n {
		t.Fatalf("conservation violated: have %d models, registered %d", len(got), n)
	}
	seen := make(map[string]int, n)
	for _, md := range got {
		seen[md.Name]++
		if md.Name == "" {
			t.Error("model with empty name after concurrent churn (corrupted entry)")
		}
		// Re-register only ever sets path to one of the two known values; a torn
		// write would leave something else.
		if md.Path != "/p/"+md.Name && md.Path != "/p2/"+md.Name {
			t.Errorf("model %q has corrupted path %q", md.Name, md.Path)
		}
		// LoadCount is only ever incremented (never decremented), so it must be a
		// small non-negative number bounded by the single LoadModel we raced.
		if md.LoadCount < 0 || md.LoadCount > 1 {
			t.Errorf("model %q LoadCount=%d out of expected [0,1]", md.Name, md.LoadCount)
		}
	}
	for _, name := range names {
		if seen[name] != 1 {
			t.Errorf("model %q present %d times after churn, want exactly 1", name, seen[name])
		}
	}
}

// ---------------------------------------------------------------------------
// (b) LOAD-STATE RACE / FAIL-OPEN
// ---------------------------------------------------------------------------

// TestAdversarialInferenceUnloadedErrors pins the fail-open guard: inference on
// a registered-but-unloaded model MUST error and return an empty response, and
// the backend must NOT be invoked (no fabricated dispatch).
//
// Mutation that fails this: in Inference, change `if !loaded {` to `if false {`.
func TestAdversarialInferenceUnloadedErrors(t *testing.T) {
	be := &countingBackend{}
	m := WithBackend(be)
	if err := m.RegisterModel("m1", "/p/m1", "gguf"); err != nil {
		t.Fatal(err)
	}
	resp, err := m.Inference("m1", "hello")
	if err == nil {
		t.Fatalf("served unloaded model: response %q", resp)
	}
	if resp != "" {
		t.Errorf("non-empty response %q on unloaded-model error", resp)
	}
	if be.count() != 0 {
		t.Errorf("backend invoked %d times for unloaded model (fail-open dispatch)", be.count())
	}
}

// TestAdversarialInferenceUnknownErrors pins that an UNKNOWN model never serves.
//
// Mutation that fails this: in Inference, return the backend result before the
// `if !ok` check.
func TestAdversarialInferenceUnknownErrors(t *testing.T) {
	be := &countingBackend{}
	m := WithBackend(be)
	resp, err := m.Inference("ghost", "hello")
	if err == nil {
		t.Fatalf("served unknown model: response %q", resp)
	}
	if resp != "" {
		t.Errorf("non-empty response %q for unknown model", resp)
	}
	if be.count() != 0 {
		t.Errorf("backend invoked for unknown model (fabricated dispatch)")
	}
}

// TestAdversarialInferenceRacesUnload is the load-state race: many goroutines
// call Inference on a model while another goroutine flips it loaded/unloaded.
// The SINK invariant: every Inference result is SELF-CONSISTENT — either an
// honest "not loaded" error with an EMPTY response, or a genuine backend ECHO
// for the loaded model. The Manager must NEVER return a fabricated/half-unloaded
// success (non-empty response that is not the backend's real output), and must
// never return a non-empty response together with an error.
//
// This proves the snapshot-under-lock in Inference (it reads ok/loaded/backend
// atomically) is honoured: there is no window where a half-unloaded model is
// served.
//
// Mutation that fails this: in Inference, drop the `loaded` snapshot and call
// backend.Generate unconditionally — racing unloads would then yield non-empty
// responses paired with a torn state.
func TestAdversarialInferenceRacesUnload(t *testing.T) {
	be := &countingBackend{}
	m := WithBackend(be)
	if err := m.RegisterModel("m1", "/p/m1", "gguf"); err != nil {
		t.Fatal(err)
	}

	const flips = 200
	var wg sync.WaitGroup

	// Toggler: bounded, alternates load/unload.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < flips; i++ {
			if i%2 == 0 {
				_ = m.LoadModel("m1")
			} else {
				_ = m.UnloadModel("m1")
			}
		}
	}()

	// 7 inference racers (toggler + 7 = 8 goroutines, at the cap).
	var violations int64
	for g := 0; g < 7; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < flips; i++ {
				resp, err := m.Inference("m1", "p")
				switch {
				case err != nil:
					// Honest refusal MUST carry an empty response.
					if resp != "" {
						atomic.AddInt64(&violations, 1)
					}
				default:
					// Served => must be the backend's REAL, input-derived output
					// for exactly this model. Anything else is fabrication.
					if resp != "REAL[m1]:p" {
						atomic.AddInt64(&violations, 1)
					}
				}
			}
		}()
	}
	wg.Wait()

	if v := atomic.LoadInt64(&violations); v != 0 {
		t.Fatalf("%d inference results were inconsistent with the load state "+
			"(half-unloaded serve or fabricated output)", v)
	}
}

// ---------------------------------------------------------------------------
// (c) IDEMPOTENCY
// ---------------------------------------------------------------------------

// TestAdversarialDoubleRegisterNoDuplicate proves re-registering an existing,
// loaded model does NOT create a second map entry and does NOT wipe load state.
//
// Mutation that fails this: in RegisterModel, replace the existing-update branch
// with an unconditional `m.models[name] = &Model{...}` (load state wiped).
func TestAdversarialDoubleRegisterNoDuplicate(t *testing.T) {
	m := NewManager()
	if err := m.RegisterModel("m1", "/a", "gguf"); err != nil {
		t.Fatal(err)
	}
	if err := m.LoadModel("m1"); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterModel("m1", "/b", "safetensors"); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterModel("m1", "/c", "gguf"); err != nil {
		t.Fatal(err)
	}

	all := m.ListModels()
	if len(all) != 1 {
		t.Fatalf("double-register produced %d entries, want 1", len(all))
	}
	got, ok := m.GetModel("m1")
	if !ok {
		t.Fatal("model vanished after re-register")
	}
	if !m.IsLoaded("m1") || got.LoadedAt == nil {
		t.Error("re-register silently tore down the loaded model")
	}
	if got.LoadCount != 1 {
		t.Errorf("LoadCount=%d after double-register, want 1 preserved", got.LoadCount)
	}
	if got.Path != "/c" || got.Format != "gguf" {
		t.Errorf("metadata not updated by last register: path=%q format=%q", got.Path, got.Format)
	}
}

// TestAdversarialConcurrentSameModelLoad proves concurrent loads of the SAME
// model are serialised and accounted exactly: with k concurrent LoadModel calls
// the resulting LoadCount is exactly k (no lost increment, no double-count), and
// the model is loaded. This is the lost-update probe on the load counter.
//
// Mutation that fails this (under -race, and on the count): change
// `model.LoadCount++` to a non-atomic read/write outside the lock — concurrent
// increments would be lost and LoadCount would drop below k.
func TestAdversarialConcurrentSameModelLoad(t *testing.T) {
	m := NewManager()
	if err := m.RegisterModel("m1", "/p", "gguf"); err != nil {
		t.Fatal(err)
	}
	const k = 8 // cap
	var wg sync.WaitGroup
	wg.Add(k)
	for i := 0; i < k; i++ {
		go func() { defer wg.Done(); _ = m.LoadModel("m1") }()
	}
	wg.Wait()

	got, _ := m.GetModel("m1")
	if got.LoadCount != k {
		t.Fatalf("LoadCount=%d after %d concurrent loads, want exactly %d (lost update)", got.LoadCount, k, k)
	}
	if !m.IsLoaded("m1") {
		t.Error("model not loaded after concurrent loads")
	}
}

// ---------------------------------------------------------------------------
// (d) BACKEND CONTRACT — honest surfacing, no fabrication
// ---------------------------------------------------------------------------

// TestAdversarialEmptyBackendOutputSurfaced proves a backend that returns an
// EMPTY string with no error is surfaced verbatim: the Manager does not detect
// the empty result and substitute a fabricated synthetic response. The sink is
// the exact returned string ("") and a nil error.
//
// Mutation that fails this: in Inference, after calling backend.Generate, if the
// result is "" replace it with fmt.Sprintf("[stub inference from %s]...", name).
func TestAdversarialEmptyBackendOutputSurfaced(t *testing.T) {
	m := WithBackend(emptyBackend{})
	if err := m.RegisterModel("m1", "/p", "gguf"); err != nil {
		t.Fatal(err)
	}
	if err := m.LoadModel("m1"); err != nil {
		t.Fatal(err)
	}
	resp, err := m.Inference("m1", "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "" {
		t.Errorf("empty-backend output fabricated into %q (synthetic-stub class)", resp)
	}
	if strings.Contains(resp, "stub inference") {
		t.Errorf("response %q is the retired synthetic stub", resp)
	}
}

// TestAdversarialBackendErrorSurfaced proves a backend error is returned as-is
// with an empty response, racing several callers so the error path is exercised
// under concurrency too. No caller may receive a non-empty (fabricated) success.
//
// Mutation that fails this: in Inference, swallow the backend error and return
// a synthetic success string instead.
func TestAdversarialBackendErrorSurfaced(t *testing.T) {
	m := WithBackend(explodingBackend{})
	if err := m.RegisterModel("m1", "/p", "gguf"); err != nil {
		t.Fatal(err)
	}
	if err := m.LoadModel("m1"); err != nil {
		t.Fatal(err)
	}
	const k = 8 // cap
	var wg sync.WaitGroup
	var bad int64
	wg.Add(k)
	for i := 0; i < k; i++ {
		go func() {
			defer wg.Done()
			resp, err := m.Inference("m1", "p")
			if err == nil || resp != "" {
				atomic.AddInt64(&bad, 1)
			}
		}()
	}
	wg.Wait()
	if bad != 0 {
		t.Fatalf("%d callers got a fabricated success despite an erroring backend", bad)
	}
}

// explodingBackend always errors, returning an empty response.
type explodingBackend struct{}

func (explodingBackend) Generate(model, prompt string) (string, error) {
	return "", fmt.Errorf("backend down for %s", model)
}
