package device

import (
	"context"
	"errors"
	"testing"
)

// TestHardwareProvisioner_NilBackendFailsHonestly asserts the absent-backend
// stub fails fast on every lifecycle call instead of pretending to work.
//
// Mutation that kills it: make Provision return (&Device{}, nil) when backend is
// nil -> the want-error check fails (a silent no-op pretending to succeed is the
// exact anti-bluff this guards against).
func TestHardwareProvisioner_NilBackendFailsHonestly(t *testing.T) {
	h := NewHardwareProvisioner(nil)
	if _, err := h.Provision(context.Background(), DeviceSpec{}); !errors.Is(err, ErrHardwareBackendUnavailable) {
		t.Errorf("Provision err = %v, want ErrHardwareBackendUnavailable", err)
	}
	if err := h.Release(context.Background(), &Device{ID: "x"}); !errors.Is(err, ErrHardwareBackendUnavailable) {
		t.Errorf("Release err = %v, want ErrHardwareBackendUnavailable", err)
	}
}

// fakeBackend is a test-only HardwareBackend that records calls, proving the
// adapter actually drives the backend (a real observable effect), not a stub.
type fakeBackend struct {
	broughtUp []DeviceSpec
	tornDown  []string
	bringErr  error
	teardErr  error
}

func (f *fakeBackend) Bringup(_ context.Context, spec DeviceSpec) (string, error) {
	if f.bringErr != nil {
		return "", f.bringErr
	}
	f.broughtUp = append(f.broughtUp, spec)
	return "hw-handle-1", nil
}
func (f *fakeBackend) Teardown(_ context.Context, handle string) error {
	if f.teardErr != nil {
		return f.teardErr
	}
	f.tornDown = append(f.tornDown, handle)
	return nil
}
func (f *fakeBackend) Probe(context.Context, string) (bool, error) { return true, nil }

// TestHardwareProvisioner_DrivesBackend asserts the adapter forwards lifecycle
// calls to the backend and walks the FSM, with the backend's recorded calls as
// sink-side evidence.
//
// Mutation that kills it: in HardwareProvisioner.Release, skip the
// backend.Teardown call -> f.tornDown stays empty and the want-1-teardown check
// fails.
func TestHardwareProvisioner_DrivesBackend(t *testing.T) {
	fb := &fakeBackend{}
	h := NewHardwareProvisioner(fb)
	spec := DeviceSpec{Tier: "T5", Arch: "arm64", GPU: true}
	d, err := h.Provision(context.Background(), spec)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if len(fb.broughtUp) != 1 || fb.broughtUp[0].Tier != "T5" {
		t.Fatalf("backend bringup calls = %+v, want exactly [T5]", fb.broughtUp)
	}
	if d.ID != "hw-handle-1" {
		t.Errorf("device ID = %q, want backend handle hw-handle-1", d.ID)
	}
	if h.Status(d) != StateReady {
		t.Errorf("status = %s, want Ready", h.Status(d))
	}
	if err := h.Release(context.Background(), d); err != nil {
		t.Fatalf("release: %v", err)
	}
	if len(fb.tornDown) != 1 || fb.tornDown[0] != "hw-handle-1" {
		t.Errorf("backend teardown calls = %+v, want exactly [hw-handle-1]", fb.tornDown)
	}
	if d.State() != StateReleased {
		t.Errorf("final state = %s, want Released", d.State())
	}
}

// TestHardwareProvisioner_TeardownFailureMarksFailed asserts a backend teardown
// error drives the device to Failed (real fault propagation, not swallowed).
//
// Mutation that kills it: ignore the Teardown error in Release (proceed to
// Released) -> state is Released and err is nil, failing both assertions.
func TestHardwareProvisioner_TeardownFailureMarksFailed(t *testing.T) {
	fb := &fakeBackend{teardErr: errors.New("usb reset failed")}
	h := NewHardwareProvisioner(fb)
	d, err := h.Provision(context.Background(), DeviceSpec{})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	err = h.Release(context.Background(), d)
	if err == nil {
		t.Fatal("release with backend teardown error returned nil")
	}
	if d.State() != StateFailed {
		t.Errorf("state after teardown failure = %s, want Failed", d.State())
	}
}
