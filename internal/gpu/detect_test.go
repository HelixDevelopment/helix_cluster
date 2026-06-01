package gpu

import (
	"strings"
	"testing"
)

// TestDetectGPUsNeverFabricatesMock proves the CLAUDE-2 fix: production
// DetectGPUs reads real hardware via detectGPUsPlatform and NEVER substitutes a
// fabricated "GPU-MOCK-..." inventory. Either detection succeeds with real
// devices (whose UUIDs are not the mock sentinels) or it returns an honest
// error -- it must never silently populate the mock A100/H100 pool.
//
// Mutation: reintroduce a `gpus = mockInventory()` fallback in DetectGPUs on
// the error/zero path -> on a host without the relevant GPUs this test sees the
// "GPU-MOCK-" UUIDs (or A100/H100 models) and fails.
func TestDetectGPUsNeverFabricatesMock(t *testing.T) {
	mgr := NewManager()
	err := mgr.DetectGPUs()

	gpus := mgr.ListGPUs()
	if err != nil {
		// Honest failure path: no inventory must have been installed.
		if len(gpus) != 0 {
			t.Fatalf("DetectGPUs errored but still populated %d GPUs: %+v", len(gpus), gpus)
		}
		return
	}

	// Success path: every device must be real, never a mock sentinel.
	if len(gpus) == 0 {
		t.Fatal("DetectGPUs returned nil error but zero GPUs (should have errored)")
	}
	for _, g := range gpus {
		if strings.HasPrefix(g.UUID, "GPU-MOCK-") {
			t.Fatalf("production detection returned a fabricated mock GPU: %+v", g)
		}
	}
}
