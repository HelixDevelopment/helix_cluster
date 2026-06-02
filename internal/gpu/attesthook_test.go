package gpu

import (
	"errors"
	"sync"
	"testing"
)

// fakeAttestor is a controllable Attestor used to drive the attestation hook in
// tests WITHOUT a real GPU or a real GraVal attest controller. It records every
// GPU it was asked to attest so a test can prove the hook actually invoked it
// (CLAUDE-1: the seam must be exercised, not merely present).
type fakeAttestor struct {
	mu      sync.Mutex
	ok      bool
	err     error
	calls   int
	calledX []string // IDs of GPUs passed to Attest, in call order
}

func (f *fakeAttestor) Attest(g *GPU) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if g != nil {
		f.calledX = append(f.calledX, g.ID)
	}
	return f.ok, f.err
}

func (f *fakeAttestor) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeAttestor) sawGPU(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calledX {
		if c == id {
			return true
		}
	}
	return false
}

// newTestManagerWithOneGPU returns a Manager pre-loaded with a single available
// GPU via the InjectGPUsForTest seam.
func newTestManagerWithOneGPU(t *testing.T, id string) *Manager {
	t.Helper()
	m := NewManager()
	m.InjectGPUsForTest([]*GPU{
		{ID: id, UUID: "GPU-" + id, Model: "Test Model", MemoryMB: 8192, Status: Available},
	})
	return m
}

// TestAllocateForChutes_RefusesUnattestedGPU is the load-bearing closure test:
// a GPU that FAILS attestation MUST NOT be allocatable for Chutes inference.
//
// Mutation guard: if the "must be attested" gate in AllocateForChutes is removed
// (i.e. the code allocates regardless of attestation result), this test FAILS
// because err would be nil and a GPU would be returned despite a failed attest.
func TestAllocateForChutes_RefusesUnattestedGPU(t *testing.T) {
	m := newTestManagerWithOneGPU(t, "gpu-0")
	att := &fakeAttestor{ok: false} // attestation FAILS

	// Before: not yet attested -> not eligible.
	if m.IsChutesEligible("gpu-0") {
		t.Fatalf("before attestation: gpu-0 should NOT be Chutes-eligible")
	}

	gpu, err := m.AllocateForChutes("job-1", att)
	if err == nil {
		t.Fatalf("expected refusal for unattested GPU, got gpu=%+v err=nil", gpu)
	}
	if gpu != nil {
		t.Fatalf("expected no GPU returned on attestation failure, got %+v", gpu)
	}
	if !errors.Is(err, ErrAttestationRequired) {
		t.Fatalf("expected ErrAttestationRequired, got %v", err)
	}

	// The hook MUST have been invoked (CLAUDE-1: seam actually exercised).
	if att.callCount() == 0 {
		t.Fatalf("attestor was never called; hook not wired")
	}
	if !att.sawGPU("gpu-0") {
		t.Fatalf("attestor was not asked about gpu-0")
	}

	// After a failed attestation the GPU must remain Available (not silently
	// consumed) and must remain ineligible.
	if m.IsChutesEligible("gpu-0") {
		t.Fatalf("after FAILED attestation: gpu-0 must NOT be Chutes-eligible")
	}
	g, _ := m.GetGPUByID("gpu-0")
	if g.Status != Available {
		t.Fatalf("failed-attest GPU must stay Available, got %s", g.Status)
	}
	if g.AllocatedTo != "" {
		t.Fatalf("failed-attest GPU must not be allocated, AllocatedTo=%q", g.AllocatedTo)
	}
}

// TestAllocateForChutes_AllowsAttestedGPU proves the positive path: a GPU that
// PASSES attestation becomes eligible and is actually allocated to the job.
func TestAllocateForChutes_AllowsAttestedGPU(t *testing.T) {
	m := newTestManagerWithOneGPU(t, "gpu-0")
	att := &fakeAttestor{ok: true} // attestation PASSES

	if m.IsChutesEligible("gpu-0") {
		t.Fatalf("before attestation: gpu-0 should NOT be Chutes-eligible")
	}

	gpu, err := m.AllocateForChutes("job-1", att)
	if err != nil {
		t.Fatalf("expected successful Chutes allocation, got err=%v", err)
	}
	if gpu == nil {
		t.Fatalf("expected a GPU, got nil")
	}
	if gpu.AllocatedTo != "job-1" {
		t.Fatalf("expected GPU allocated to job-1, got %q", gpu.AllocatedTo)
	}
	if gpu.Status != Allocated {
		t.Fatalf("expected Allocated status, got %s", gpu.Status)
	}

	// The hook must have run for the positive path too.
	if att.callCount() == 0 {
		t.Fatalf("attestor was never called on success path")
	}

	// Eligibility must now be recorded sink-side.
	if !m.IsChutesEligible("gpu-0") {
		t.Fatalf("after PASSED attestation: gpu-0 must be Chutes-eligible")
	}

	// Idempotency: a second AllocateForChutes for the same job returns the same
	// GPU and does not consume another device.
	gpu2, err := m.AllocateForChutes("job-1", att)
	if err != nil {
		t.Fatalf("idempotent re-allocate failed: %v", err)
	}
	if gpu2.ID != gpu.ID {
		t.Fatalf("idempotent re-allocate returned different GPU: %s vs %s", gpu2.ID, gpu.ID)
	}
}

// TestAllocateForChutes_AttestorError surfaces an attestor transport/logic error
// honestly (typed-wrapped) and does NOT allocate, distinguishing it from a clean
// "attestation failed" boolean.
func TestAllocateForChutes_AttestorError(t *testing.T) {
	m := newTestManagerWithOneGPU(t, "gpu-0")
	wantErr := errors.New("graval controller unreachable")
	att := &fakeAttestor{ok: false, err: wantErr}

	gpu, err := m.AllocateForChutes("job-1", att)
	if err == nil {
		t.Fatalf("expected error when attestor errors, got nil (gpu=%+v)", gpu)
	}
	if gpu != nil {
		t.Fatalf("expected no GPU on attestor error, got %+v", gpu)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped attestor error, got %v", err)
	}
	// A transport error must NOT mark the GPU eligible.
	if m.IsChutesEligible("gpu-0") {
		t.Fatalf("attestor error must not produce eligibility")
	}
}

// TestAllocateForChutes_NilAttestor rejects a missing attestor rather than
// defaulting to "no attestation required" (which would be a CLAUDE-1 PASS-bluff).
func TestAllocateForChutes_NilAttestor(t *testing.T) {
	m := newTestManagerWithOneGPU(t, "gpu-0")
	gpu, err := m.AllocateForChutes("job-1", nil)
	if err == nil {
		t.Fatalf("expected error for nil attestor, got nil (gpu=%+v)", gpu)
	}
	if gpu != nil {
		t.Fatalf("expected no GPU for nil attestor, got %+v", gpu)
	}
}

// TestAllocateForChutes_EmptyJobID mirrors AllocateGPU's guard.
func TestAllocateForChutes_EmptyJobID(t *testing.T) {
	m := newTestManagerWithOneGPU(t, "gpu-0")
	att := &fakeAttestor{ok: true}
	if _, err := m.AllocateForChutes("", att); err == nil {
		t.Fatalf("expected error for empty jobID")
	}
}

// TestAllocateForChutes_NotStarted refuses allocation before detection/inject.
func TestAllocateForChutes_NotStarted(t *testing.T) {
	m := NewManager()
	att := &fakeAttestor{ok: true}
	if _, err := m.AllocateForChutes("job-1", att); err == nil {
		t.Fatalf("expected error before inventory detected")
	}
}

// TestAllocateForChutes_RetriesAfterPriorFailure proves that a GPU which failed
// attestation earlier can later become eligible once attestation passes, since
// the per-GPU result is re-evaluated on each allocation attempt (the gate is not
// a permanent denylist).
func TestAllocateForChutes_RetriesAfterPriorFailure(t *testing.T) {
	m := newTestManagerWithOneGPU(t, "gpu-0")

	failing := &fakeAttestor{ok: false}
	if _, err := m.AllocateForChutes("job-1", failing); err == nil {
		t.Fatalf("expected first attempt to fail")
	}
	if m.IsChutesEligible("gpu-0") {
		t.Fatalf("gpu-0 must remain ineligible after failed attest")
	}

	passing := &fakeAttestor{ok: true}
	gpu, err := m.AllocateForChutes("job-2", passing)
	if err != nil {
		t.Fatalf("expected success after passing attest, got %v", err)
	}
	if gpu.AllocatedTo != "job-2" {
		t.Fatalf("expected allocation to job-2, got %q", gpu.AllocatedTo)
	}
	if !m.IsChutesEligible("gpu-0") {
		t.Fatalf("gpu-0 must be eligible after passing attest")
	}
}

// TestAllocateForChutes_RefusesUnattestedExistingAllocation is the regression
// guard for the attestation-gate bypass via the idempotency shortcut. A GPU bound
// to a job via the PLAIN, non-attested AllocateGPU path must NOT be returned for
// Chutes (external) inference: that device never cleared the GraVal gate, so
// short-circuiting on "already bound to this jobID" would silently serve external
// inference on an unattested device (a CLAUDE-1 security PASS-bluff).
//
// Mutation guard: if the idempotency branch drops the
// m.chutesAttested[gpu.ID] check and returns ANY GPU bound to jobID, this test
// FAILS — err would be nil, a GPU would be returned, and the attestor would never
// be called despite being a FAILING attestor.
func TestAllocateForChutes_RefusesUnattestedExistingAllocation(t *testing.T) {
	m := newTestManagerWithOneGPU(t, "gpu-0")

	// Bind gpu-0 to job-x via the plain (non-attested) allocation path.
	if _, err := m.AllocateGPU("job-x"); err != nil {
		t.Fatalf("plain AllocateGPU failed: %v", err)
	}
	if m.IsChutesEligible("gpu-0") {
		t.Fatalf("plain AllocateGPU must NOT make gpu-0 Chutes-eligible")
	}

	// A FAILING attestor: if the gate is honored, it would be consulted (and the
	// device refused); if the bypass exists, the idempotency branch returns the
	// GPU WITHOUT ever calling the attestor.
	att := &fakeAttestor{ok: false}
	gpu, err := m.AllocateForChutes("job-x", att)
	if err == nil {
		t.Fatalf("expected refusal of unattested existing allocation, got gpu=%+v err=nil", gpu)
	}
	if gpu != nil {
		t.Fatalf("expected no GPU returned for unattested existing allocation, got %+v", gpu)
	}
	if !errors.Is(err, ErrAttestationRequired) {
		t.Fatalf("expected ErrAttestationRequired, got %v", err)
	}
	if m.IsChutesEligible("gpu-0") {
		t.Fatalf("refused device must remain Chutes-ineligible")
	}
}

// TestAllocateForChutes_AttestedAllocationIsIdempotent proves the idempotent
// shortcut STILL works for a GPU that was attested for Chutes: a second
// AllocateForChutes for the same job returns the same device without consuming
// another and without needing a fresh attestor call to succeed.
func TestAllocateForChutes_AttestedAllocationIsIdempotent(t *testing.T) {
	m := newTestManagerWithOneGPU(t, "gpu-0")
	att := &fakeAttestor{ok: true}

	first, err := m.AllocateForChutes("job-1", att)
	if err != nil {
		t.Fatalf("first attested allocate failed: %v", err)
	}
	callsAfterFirst := att.callCount()

	// Second call hits the idempotency branch; gpu-0 IS attested so it returns.
	second, err := m.AllocateForChutes("job-1", att)
	if err != nil {
		t.Fatalf("idempotent attested re-allocate failed: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("idempotent re-allocate returned different GPU: %s vs %s", second.ID, first.ID)
	}
	if got := att.callCount(); got != callsAfterFirst {
		t.Fatalf("idempotent attested path should not re-run attestor: calls %d -> %d", callsAfterFirst, got)
	}
}

// TestChutesAttestationResult_Tracked verifies the per-GPU result is stored and
// readable (in-memory, mu-guarded) so other components can inspect eligibility.
func TestChutesAttestationResult_Tracked(t *testing.T) {
	m := newTestManagerWithOneGPU(t, "gpu-0")

	// Unknown GPU -> not attested.
	if res, ok := m.AttestationResult("nope"); ok {
		t.Fatalf("unknown GPU should report no result, got %v", res)
	}

	att := &fakeAttestor{ok: true}
	if _, err := m.AllocateForChutes("job-1", att); err != nil {
		t.Fatalf("allocate failed: %v", err)
	}
	res, ok := m.AttestationResult("gpu-0")
	if !ok {
		t.Fatalf("expected a recorded attestation result for gpu-0")
	}
	if !res {
		t.Fatalf("expected recorded result true, got %v", res)
	}
}

// TestAttestGPU_DirectGate exercises the lower-level AttestGPU helper directly so
// the recording logic is covered independently of allocation.
func TestAttestGPU_DirectGate(t *testing.T) {
	m := newTestManagerWithOneGPU(t, "gpu-0")
	att := &fakeAttestor{ok: true}

	ok, err := m.AttestGPU("gpu-0", att)
	if err != nil {
		t.Fatalf("AttestGPU error: %v", err)
	}
	if !ok {
		t.Fatalf("expected AttestGPU to pass")
	}
	if !m.IsChutesEligible("gpu-0") {
		t.Fatalf("gpu-0 should be eligible after direct AttestGPU pass")
	}

	// Unknown GPU id is a typed error, not a panic.
	if _, err := m.AttestGPU("ghost", att); err == nil {
		t.Fatalf("expected error attesting unknown GPU")
	}
}
