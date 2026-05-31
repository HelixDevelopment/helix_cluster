//go:build integration

package build

import (
	"context"
	"testing"
	"time"
)

// realBuilderProbe is a placeholder for a REAL Builder implementation
// (e.g. a docker buildx / buildkit driver). It is intentionally not wired to a
// real engine yet; the real driver must replace this and the t.Skip below.
//
// This file documents, in compilable form, the proof a real build path MUST
// provide per CLAUDE-1.6 (sink-side evidence). It is gated behind
// `//go:build integration` so it never runs in the default unit suite and never
// masquerades as proof of a working builder.
type realBuilderProbe struct{}

func (realBuilderProbe) Build(ctx context.Context, j *Job) {
	// A real implementation MUST:
	//   1. Clone j.RepoURL at j.Ref.
	//   2. Read the Dockerfile at j.DockerfilePath (honoring j.BuildArgs).
	//   3. Invoke a real builder (docker buildx / buildkit) to produce layers.
	//   4. Push the image and set j.ImageTag to the pushed reference.
	//   5. TransitionTo(StateSucceeded) only after the image exists and is
	//      verified pullable; otherwise TransitionTo(StateFailed) with logs.
	j.TransitionTo(StateFailed)
	j.AppendLog("realBuilderProbe is a skeleton; no real engine wired")
}

// TestRealBuild_ImagePullable is the SKELETON for the future real-builder proof.
//
// A genuine pass here (once realBuilderProbe is replaced by a real driver) MUST
// assert end-user-visible, sink-side evidence — NOT just a returned state:
//
//   - The image referenced by Job.ImageTag actually EXISTS in the target
//     registry (e.g. `docker manifest inspect <tag>` / registry v2 manifest
//     GET returns 200 with a valid digest).
//   - The image is PULLABLE from a clean host (`docker pull <tag>` succeeds and
//     the local digest matches the registry digest).
//   - The produced filesystem/layers reflect the fixture repo's Dockerfile
//     (e.g. an expected file/label is present in the pulled image).
//
// Until a real Builder is wired in, this test SKIPS with an explicit reason so
// it can never be mistaken for evidence that a real image was built.
func TestRealBuild_ImagePullable(t *testing.T) {
	t.Skip("PCS-6: real builder not implemented. " +
		"Replace realBuilderProbe with a docker buildx/buildkit driver and assert " +
		"that Job.ImageTag refers to an image that exists and is pullable (sink-side evidence per CLAUDE-1.6).")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := NewServiceWithBuilder(1, realBuilderProbe{})
	svc.Start(ctx)
	defer svc.Stop()

	j := &Job{
		ID:             "real-1",
		RepoURL:        "https://example.com/fixtures/hello.git",
		Ref:            "main",
		DockerfilePath: "Dockerfile",
	}
	if err := svc.Submit(j); err != nil {
		t.Fatalf("submit: %v", err)
	}

	got := waitForTerminal(t, svc, "real-1", 5*time.Minute)
	if got.State != StateSucceeded {
		t.Fatalf("real build state = %s, want succeeded", got.State)
	}
	if got.ImageTag == "" {
		t.Fatal("real build must set a real, pullable ImageTag")
	}

	// TODO(real-builder): replace with actual sink-side assertions:
	//   assertImageExistsInRegistry(t, got.ImageTag)
	//   assertImagePullable(t, got.ImageTag)
}
