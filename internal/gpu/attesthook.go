package gpu

import (
	"errors"
	"fmt"
)

// ErrAttestationRequired is returned by AllocateForChutes when the candidate GPU
// did not PASS attestation. It is a sentinel so callers (and tests) can match the
// "not attested" refusal with errors.Is rather than string-sniffing, and so the
// refusal is unambiguously distinct from a plain "no available GPUs" condition.
//
// WHY a typed sentinel: the Chutes inference allocation gate is security-relevant
// (only GraVal-attested GPUs may serve external inference). A caller that wants to
// surface "attestation pending/failed" to an operator must be able to detect this
// precise condition; a bare fmt error would force brittle string matching.
var ErrAttestationRequired = errors.New("gpu: GPU has not passed attestation; ineligible for Chutes inference")

// ChutesAttestor is the injectable attestation seam. A ChutesAttestor reports whether
// a given GPU has passed hardware/GraVal attestation and is therefore trustworthy
// to serve Chutes (external) inference workloads.
//
//   - (true, nil)  -> attestation PASSED; the GPU is eligible.
//   - (false, nil) -> attestation FAILED cleanly; the GPU is NOT eligible.
//   - (_, err)     -> attestation could not be determined (e.g. the GraVal
//     controller was unreachable); the result is unknown and the GPU is NOT
//     eligible. The error is surfaced honestly (never coerced to a fake PASS),
//     satisfying CLAUDE-1 / CLAUDE-2 degrade-honestly rules.
//
// Production wires this to the GraVal attest controller (pkg/chutes attest) or
// pkg/gpuattest; tests inject a controllable fake. This package intentionally
// does NOT import those packages so the gate stays unit-testable without real
// hardware and without cross-package coupling — the adapter lives at the call
// site that already depends on both.
type ChutesAttestor interface {
	// Attest returns whether gpu has passed attestation. gpu is a defensive
	// clone owned by the caller; implementations must treat it as read-only.
	Attest(gpu *GPU) (bool, error)
}

// AttestGPU runs the given ChutesAttestor against the GPU identified by id, records the
// boolean result in the per-GPU attestation map (in-memory, mu-guarded), and
// returns it. A non-nil attestor error is wrapped and returned, and the result is
// NOT recorded as a pass — an indeterminate attestation must never be cached as
// eligible.
//
// AttestGPU is the single source of truth for "did this GPU pass": both
// AllocateForChutes and IsChutesEligible build on the result it records.
func (m *Manager) AttestGPU(id string, attestor ChutesAttestor) (bool, error) {
	if attestor == nil {
		return false, fmt.Errorf("gpu: AttestGPU: attestor must not be nil")
	}

	// Snapshot the target GPU under the lock, then run attestation WITHOUT
	// holding the lock: a ChutesAttestor may perform I/O (e.g. talk to the GraVal
	// controller) and must not block all other Manager operations.
	m.mu.RLock()
	var target *GPU
	for _, gpu := range m.gpus {
		if gpu.ID == id {
			target = gpu.Clone()
			break
		}
	}
	m.mu.RUnlock()
	if target == nil {
		return false, fmt.Errorf("gpu: AttestGPU: GPU not found: %s", id)
	}

	ok, err := attestor.Attest(target)
	if err != nil {
		// Indeterminate: do not record, do not mark eligible.
		return false, fmt.Errorf("gpu: AttestGPU: attestation for %s failed: %w", id, err)
	}

	m.mu.Lock()
	if m.chutesAttested == nil {
		m.chutesAttested = make(map[string]bool)
	}
	m.chutesAttested[id] = ok
	m.mu.Unlock()
	return ok, nil
}

// IsChutesEligible reports whether the GPU identified by id has a RECORDED,
// PASSED attestation result. A GPU that was never attested, or whose last
// attestation failed, is not eligible. This is the predicate other components use
// to decide if a GPU may serve Chutes inference.
func (m *Manager) IsChutesEligible(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.chutesAttested[id]
}

// AttestationResult returns the last recorded attestation result for the GPU id
// and whether any result has been recorded at all. The ok=false return
// distinguishes "never attested" from "attested and failed" (result=false,
// ok=true).
func (m *Manager) AttestationResult(id string) (result bool, ok bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result, ok = m.chutesAttested[id]
	return result, ok
}

// AllocateForChutes allocates an Available GPU to jobID for Chutes (external)
// inference — but ONLY a GPU that PASSES attestation via the injected attestor.
// This is the attestation-gated allocation path: an unattested or failed GPU is
// REFUSED with ErrAttestationRequired and is left untouched in the pool.
//
// Behaviour:
//   - Idempotent per job: if jobID already holds a GPU it is returned unchanged
//     (no second device consumed), mirroring AllocateGPU.
//   - Each candidate GPU is attested at allocation time (the gate re-evaluates;
//     it is not a permanent denylist), and the result is recorded so
//     IsChutesEligible / AttestationResult reflect it.
//   - On attestor transport error the wrapped error is returned and nothing is
//     allocated.
//
// WHY the gate is load-bearing: per CLAUDE-1, allowing a non-attested GPU to
// serve external inference would be a security PASS-bluff. Removing the
// "must pass attestation" check would let TestAllocateForChutes_RefusesUnattestedGPU
// allocate a failed GPU — that test is the mutation guard for this gate.
func (m *Manager) AllocateForChutes(jobID string, attestor ChutesAttestor) (*GPU, error) {
	if jobID == "" {
		return nil, fmt.Errorf("gpu: AllocateForChutes: jobID must not be empty")
	}
	if attestor == nil {
		return nil, fmt.Errorf("gpu: AllocateForChutes: attestor must not be nil")
	}
	if !m.started.Load() {
		return nil, fmt.Errorf("gpu: AllocateForChutes: GPU inventory not detected; call DetectGPUs first")
	}

	// Honor an existing allocation for this job first (idempotency) — but ONLY if
	// that GPU has a RECORDED, PASSED attestation result. We must NOT blindly
	// return any GPU bound to jobID: a GPU bound via the plain non-attested
	// AllocateGPU path has never cleared the GraVal gate, and returning it for
	// Chutes (external) inference would bypass the very security guarantee this
	// method exists to enforce (a CLAUDE-1 PASS-bluff). The gate is honored even
	// on the idempotent path: an existing-but-unattested binding falls through to
	// the normal attest-then-allocate flow rather than short-circuiting, so the
	// attestor is actually consulted before the device serves external inference.
	m.mu.RLock()
	for _, gpu := range m.gpus {
		if gpu.Status == Allocated && gpu.AllocatedTo == jobID {
			if m.chutesAttested[gpu.ID] {
				clone := gpu.Clone()
				m.mu.RUnlock()
				return clone, nil
			}
			// Bound to jobID but NOT attested for Chutes: refuse the idempotent
			// shortcut. The device is already taken by this job (not Available),
			// so there is nothing to re-attest/allocate here — surface the gate
			// failure honestly rather than returning an unattested device.
			m.mu.RUnlock()
			return nil, fmt.Errorf("gpu: AllocateForChutes: GPU %s is bound to job %s but has not passed Chutes attestation: %w", gpu.ID, jobID, ErrAttestationRequired)
		}
	}
	// Collect the IDs of currently-Available candidates so we can attest each
	// without holding the lock across the (possibly I/O-bound) attestor call.
	var candidates []string
	for _, gpu := range m.gpus {
		if gpu.Status == Available {
			candidates = append(candidates, gpu.ID)
		}
	}
	m.mu.RUnlock()

	if len(candidates) == 0 {
		return nil, fmt.Errorf("gpu: AllocateForChutes: no available GPUs")
	}

	var sawCandidate, sawFailed bool
	for _, id := range candidates {
		passed, err := m.AttestGPU(id, attestor)
		if err != nil {
			// Transport/indeterminate error: surface honestly and stop. We do not
			// silently skip to the next GPU because an unreachable attest
			// controller is an operational fault the caller must see.
			return nil, err
		}
		if !passed {
			sawFailed = true
			continue
		}

		// Attestation passed: try to claim this specific GPU. Re-check Available
		// under the write lock because state may have changed since the snapshot.
		m.mu.Lock()
		for _, gpu := range m.gpus {
			if gpu.ID == id && gpu.Status == Available {
				gpu.Status = Allocated
				gpu.AllocatedTo = jobID
				clone := gpu.Clone()
				m.mu.Unlock()
				return clone, nil
			}
		}
		m.mu.Unlock()
		// GPU was taken by a concurrent caller between snapshot and claim; keep
		// looking among the remaining candidates.
		sawCandidate = true
	}

	if sawFailed {
		return nil, fmt.Errorf("gpu: AllocateForChutes: no attested GPU available for job %s: %w", jobID, ErrAttestationRequired)
	}
	if sawCandidate {
		// Every candidate we attested passed the gate, but each was claimed by a
		// concurrent caller between the snapshot and our write-locked claim. This
		// is distinct from "no available GPUs": the pool was non-empty and the
		// attestor approved, we simply lost every race. Surface that honestly so
		// the caller can retry rather than treating the cluster as exhausted.
		return nil, fmt.Errorf("gpu: AllocateForChutes: all attested candidates were claimed concurrently for job %s; retry", jobID)
	}
	return nil, fmt.Errorf("gpu: AllocateForChutes: no available GPUs")
}
