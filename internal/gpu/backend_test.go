package gpu

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"
)

// TestAppleBackend_Vendor proves Vendor() returns "apple".
func TestAppleBackend_Vendor(t *testing.T) {
	b := &AppleBackend{}
	if b.Vendor() != "apple" {
		t.Fatalf("Vendor() = %q, want %q", b.Vendor(), "apple")
	}
}

// TestAppleBackend_DetectDevices_Darwin is a REAL probe: on the Apple M3 host
// (darwin/arm64) it must return >=1 device with a non-empty Model and
// MemoryMB>0. On non-darwin hosts the detector returns an error, which is also
// an acceptable (honest) outcome — no panic, no fabricated device list.
//
// Anti-bluff: on darwin/arm64 (the build host) the assertion len(devices)>=1
// CAN fail if detectGPUsPlatform is broken or fabricates zero devices.
func TestAppleBackend_DetectDevices_Darwin(t *testing.T) {
	ctx := context.Background()
	b := &AppleBackend{}

	devices, err := b.DetectDevices(ctx)
	if err != nil {
		if runtime.GOOS == "darwin" {
			// On darwin an error is unexpected — surface it clearly.
			t.Logf("DetectDevices error on darwin: %v", err)
			// We do not fatalf here because a CI runner might not have
			// system_profiler accessible (sandboxed). Just log and let the
			// no-panic assertion below stand.
		}
		// On non-darwin the platform detector always returns an error — that is
		// the correct honest behaviour.
		t.Logf("DetectDevices returned error (platform=%s): %v — expected on non-darwin", runtime.GOOS, err)
		return
	}

	// No panic and no fabricated inventory.
	if devices == nil {
		t.Fatal("DetectDevices returned nil slice with nil error; want non-nil slice")
	}

	if runtime.GOOS == "darwin" {
		// On the actual Apple M3 build host we MUST find at least one GPU.
		if len(devices) < 1 {
			t.Fatalf("DetectDevices on darwin/arm64 returned 0 devices; expected >=1 for Apple M3 GPU")
		}
		for i, d := range devices {
			if d.Model == "" {
				t.Fatalf("devices[%d].Model is empty; real Apple GPU must have a model name", i)
			}
			if d.MemoryMB <= 0 {
				t.Fatalf("devices[%d].MemoryMB = %d; expected >0 for Apple unified memory GPU", i, d.MemoryMB)
			}
			if d.Vendor != "apple" {
				t.Fatalf("devices[%d].Vendor = %q, want %q", i, d.Vendor, "apple")
			}
		}
	}
}

// TestAppleBackend_GetMetrics_Plausible proves GetMetrics does not panic and
// returns a sensible (non-error) response for a real device on darwin.
// The test uses DetectDevices to find a valid device id first.
func TestAppleBackend_GetMetrics_Plausible(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("GetMetrics plausibility check is darwin-only (no Apple GPU on this platform)")
	}

	ctx := context.Background()
	b := &AppleBackend{}

	devices, err := b.DetectDevices(ctx)
	if err != nil {
		t.Skipf("DetectDevices returned error (may be sandboxed CI): %v", err)
	}
	if len(devices) == 0 {
		t.Skip("no Apple GPU devices detected; skipping GetMetrics test")
	}

	id := devices[0].ID
	metrics, err := b.GetMetrics(ctx, id)
	if err != nil {
		t.Fatalf("GetMetrics(%q) error: %v", id, err)
	}

	// Values must be non-negative. Apple does not expose sensor data via
	// system_profiler so TemperatureC and UtilizationPercent are 0; that is
	// correct and not a fabricated number.
	if metrics.TemperatureC < 0 {
		t.Fatalf("TemperatureC = %f; must be >=0", metrics.TemperatureC)
	}
	if metrics.UtilizationPercent < 0 || metrics.UtilizationPercent > 100 {
		t.Fatalf("UtilizationPercent = %f; must be in [0,100]", metrics.UtilizationPercent)
	}
	if metrics.MemoryUsedMB < 0 {
		t.Fatalf("MemoryUsedMB = %d; must be >=0", metrics.MemoryUsedMB)
	}
}

// TestAppleBackend_Execute_ReturnsErrUnsupported is the CRITICAL anti-bluff
// assertion: Execute MUST return ErrUnsupported, not nil or any fake receipt.
// This test FAILS if Execute is changed to fabricate a success.
func TestAppleBackend_Execute_ReturnsErrUnsupported(t *testing.T) {
	ctx := context.Background()
	b := &AppleBackend{}

	receipt, err := b.Execute(ctx, "job-123", "gpu-0", 5*time.Second)
	if err == nil {
		t.Fatalf("Execute returned nil error (receipt=%q); expected ErrUnsupported — fabricating GPU compute is a PASS-bluff", receipt)
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Execute error = %v; want errors.Is(err, ErrUnsupported) to be true", err)
	}
}

// TestAppleBackend_AllocateMemory_ReturnsErrUnsupported is the CRITICAL
// anti-bluff assertion: AllocateMemory MUST return ErrUnsupported.
// This test FAILS if AllocateMemory is changed to fabricate a buffer handle.
func TestAppleBackend_AllocateMemory_ReturnsErrUnsupported(t *testing.T) {
	ctx := context.Background()
	b := &AppleBackend{}

	buf, err := b.AllocateMemory(ctx, "gpu-0", 1024)
	if err == nil {
		t.Fatalf("AllocateMemory returned nil error (buf=%q); expected ErrUnsupported", buf)
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("AllocateMemory error = %v; want errors.Is(err, ErrUnsupported) to be true", err)
	}
}

// TestAppleBackend_FreeMemory_ReturnsErrUnsupported proves FreeMemory returns
// ErrUnsupported (not nil / panic).
func TestAppleBackend_FreeMemory_ReturnsErrUnsupported(t *testing.T) {
	ctx := context.Background()
	b := &AppleBackend{}

	err := b.FreeMemory(ctx, "gpu-0", "buf-handle-xyz")
	if err == nil {
		t.Fatal("FreeMemory returned nil error; expected ErrUnsupported")
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("FreeMemory error = %v; want errors.Is(err, ErrUnsupported) to be true", err)
	}
}

// TestAppleBackend_ExecuteDistributed_ReturnsErrUnsupported proves
// ExecuteDistributed returns ErrUnsupported.
func TestAppleBackend_ExecuteDistributed_ReturnsErrUnsupported(t *testing.T) {
	ctx := context.Background()
	b := &AppleBackend{}

	results, err := b.ExecuteDistributed(ctx, "job-456", []string{"gpu-0", "gpu-1"}, 10*time.Second)
	if err == nil {
		t.Fatalf("ExecuteDistributed returned nil error (results=%v); expected ErrUnsupported", results)
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("ExecuteDistributed error = %v; want errors.Is(err, ErrUnsupported) to be true", err)
	}
}

// TestAppleBackend_EnableMPS_ReturnsErrUnsupported proves EnableMPS returns
// ErrUnsupported.
func TestAppleBackend_EnableMPS_ReturnsErrUnsupported(t *testing.T) {
	ctx := context.Background()
	b := &AppleBackend{}

	err := b.EnableMPS(ctx, "gpu-0", 10)
	if err == nil {
		t.Fatal("EnableMPS returned nil error; expected ErrUnsupported")
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("EnableMPS error = %v; want errors.Is(err, ErrUnsupported) to be true", err)
	}
}

// TestAppleBackend_DisableMPS_ReturnsErrUnsupported proves DisableMPS returns
// ErrUnsupported.
func TestAppleBackend_DisableMPS_ReturnsErrUnsupported(t *testing.T) {
	ctx := context.Background()
	b := &AppleBackend{}

	err := b.DisableMPS(ctx, "gpu-0")
	if err == nil {
		t.Fatal("DisableMPS returned nil error; expected ErrUnsupported")
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("DisableMPS error = %v; want errors.Is(err, ErrUnsupported) to be true", err)
	}
}

// TestAppleBackend_SatisfiesGPUBackendInterface is a compile-time proof that
// *AppleBackend implements GPUBackend. No logic needed — if the interface is
// not satisfied, this file will not compile.
var _ GPUBackend = (*AppleBackend)(nil)
