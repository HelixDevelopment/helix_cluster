//go:build integration && darwin

package gpu

import (
	"strings"
	"testing"
)

// TestDetectGPUsPlatformRealHostDarwin is the §7.1 real-host proof for macOS.
// It runs the actual darwin detector (system_profiler + sysctl) against THIS
// machine and asserts at least one GPU whose Model contains "Apple". It does
// NOT t.Skip on darwin: a Mac with no detectable GPU is a hard failure, because
// CLAUDE-2 forbids declaring the feature working without real sink-side proof.
//
// Run with: go test -race -count=1 -tags=integration ./internal/gpu/...
func TestDetectGPUsPlatformRealHostDarwin(t *testing.T) {
	gpus, err := detectGPUsPlatform()
	if err != nil {
		t.Fatalf("real-host GPU detection failed on darwin: %v", err)
	}
	if len(gpus) == 0 {
		t.Fatal("real-host GPU detection returned zero GPUs on darwin")
	}

	var foundApple bool
	for _, g := range gpus {
		t.Logf("detected GPU: id=%s model=%q memoryMB=%d status=%s", g.ID, g.Model, g.MemoryMB, g.Status)
		if g.UUID == "" || g.Model == "" {
			t.Errorf("GPU %s missing UUID/Model: %+v", g.ID, g)
		}
		if g.Status != Available {
			t.Errorf("GPU %s expected Available, got %s", g.ID, g.Status)
		}
		if strings.Contains(g.Model, "Apple") {
			foundApple = true
			// Apple Silicon shares unified memory; the real host must report a
			// positive pool, not a fabricated or zero value.
			if g.MemoryMB <= 0 {
				t.Errorf("Apple GPU %s reported non-positive unified memory %d", g.ID, g.MemoryMB)
			}
		}
	}

	if !foundApple {
		t.Fatalf("expected at least one GPU whose Model contains \"Apple\"; got %+v", gpus)
	}
}

// TestDetectGPUsRealHostDarwin proves the public Manager.DetectGPUs entry point
// also works end-to-end on the real macOS host (no mock).
func TestDetectGPUsRealHostDarwin(t *testing.T) {
	mgr := NewManager()
	if err := mgr.DetectGPUs(); err != nil {
		t.Fatalf("Manager.DetectGPUs failed on real darwin host: %v", err)
	}
	gpus := mgr.ListGPUs()
	if len(gpus) == 0 {
		t.Fatal("Manager.DetectGPUs populated zero GPUs on darwin")
	}
	var foundApple bool
	for _, g := range gpus {
		if strings.HasPrefix(g.UUID, "GPU-MOCK-") {
			t.Fatalf("real host returned a mock GPU: %+v", g)
		}
		if strings.Contains(g.Model, "Apple") {
			foundApple = true
		}
	}
	if !foundApple {
		t.Fatalf("expected an Apple GPU from real detection, got %+v", gpus)
	}
}
