package build

import (
	"context"
	"fmt"
	"time"
)

// simulatedBuilder is a NON-PRODUCTION OrchestratorBuilder used ONLY to exercise
// the orchestrator/worker-pool/state-machine/gRPC plumbing deterministically
// without a real container build.
//
// WARNING (PCS-6 / CLAUDE-1): it builds NOTHING real — no podman, no image, no
// layer, no digest. It fabricates an ImageTag that does NOT correspond to any
// real image and drives the job to StateSucceeded. A green test using this
// builder proves ONLY that the orchestration plumbing works; it is NOT evidence
// that any image was produced. The production default (NewOrchestrator) uses the
// REAL podmanOrchestratorBuilder; real-build evidence comes from the
// //go:build integration tests that exercise it against a real podman daemon.
type simulatedBuilder struct{}

// NewSimulatedBuilder returns a NON-PRODUCTION OrchestratorBuilder that drives
// jobs to a terminal state deterministically (no real build). Inject it via
// NewOrchestratorWithBuilder for orchestration/plumbing tests that must not
// depend on a real podman daemon. Do NOT use in production.
func NewSimulatedBuilder() OrchestratorBuilder { return simulatedBuilder{} }

// Simulated marks this builder as a non-production simulation so callers/tests
// can assert they are not running against the real builder.
func (simulatedBuilder) Simulated() bool { return true }

// Execute drives j to a terminal state with a fabricated image tag. It honors
// ctx cancellation (StateCancelled) and the sentinel RepoURL "fail"
// (StateFailed) so failure-path plumbing can be exercised too.
func (simulatedBuilder) Execute(ctx context.Context, j *Job) {
	j.AppendLog("[simulated] NON-PRODUCTION build started (no real image is produced)")

	select {
	case <-ctx.Done():
		_ = j.TransitionTo(StateCancelled)
		j.AppendLog("[simulated] build cancelled by context")
		return
	case <-time.After(5 * time.Millisecond):
	}

	if j.RepoURL == "fail" {
		_ = j.TransitionTo(StateFailed)
		j.AppendLog("[simulated] build failed: simulated failure sentinel")
		return
	}

	ref := j.Ref
	if ref == "" {
		ref = "latest"
	}
	tag := fmt.Sprintf("helix-build/%s:%s", j.ID, ref)
	j.mu.Lock()
	j.ImageTag = tag
	j.mu.Unlock()
	_ = j.TransitionTo(StateSucceeded)
	j.AppendLog(fmt.Sprintf("[simulated] build succeeded: fabricated image tag %s (NO real artifact)", tag))
}
