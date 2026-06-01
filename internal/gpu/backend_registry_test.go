package gpu

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// fakeBackend is an in-test GPUBackend that returns a configurable set of
// devices. It is used exclusively in registry tests — never in production.
type fakeBackend struct {
	vendor  string
	devices []DeviceInfo
}

func (f *fakeBackend) Vendor() string { return f.vendor }

func (f *fakeBackend) DetectDevices(_ context.Context) ([]DeviceInfo, error) {
	return f.devices, nil
}

func (f *fakeBackend) GetDeviceStatus(_ context.Context, id string) (DeviceStatus, error) {
	for _, d := range f.devices {
		if d.ID == id {
			return DeviceStatus{ID: id, Healthy: true}, nil
		}
	}
	return DeviceStatus{}, fmt.Errorf("fake: device %q not found", id)
}

func (f *fakeBackend) GetMetrics(_ context.Context, id string) (GPUMetrics, error) {
	for _, d := range f.devices {
		if d.ID == id {
			return GPUMetrics{}, nil
		}
	}
	return GPUMetrics{}, fmt.Errorf("fake: device %q not found", id)
}

func (f *fakeBackend) AllocateMemory(_ context.Context, _ string, _ int) (string, error) {
	return "", ErrUnsupported
}

func (f *fakeBackend) FreeMemory(_ context.Context, _ string, _ string) error {
	return ErrUnsupported
}

func (f *fakeBackend) Execute(_ context.Context, _, _ string, _ time.Duration) (string, error) {
	return "", ErrUnsupported
}

func (f *fakeBackend) ExecuteDistributed(_ context.Context, _ string, _ []string, _ time.Duration) (map[string]string, error) {
	return nil, ErrUnsupported
}

func (f *fakeBackend) EnableMPS(_ context.Context, _ string, _ int) error { return ErrUnsupported }
func (f *fakeBackend) DisableMPS(_ context.Context, _ string) error       { return ErrUnsupported }

// TestRegistry_RegisterGetList proves that Register/Get/List work correctly
// for two distinct fake backends.
func TestRegistry_RegisterGetList(t *testing.T) {
	r := NewBackendRegistry()

	alpha := &fakeBackend{vendor: "alpha"}
	beta := &fakeBackend{vendor: "beta"}

	if err := r.Register(alpha); err != nil {
		t.Fatalf("Register alpha: %v", err)
	}
	if err := r.Register(beta); err != nil {
		t.Fatalf("Register beta: %v", err)
	}

	got, ok := r.Get("alpha")
	if !ok {
		t.Fatal("Get(alpha) not found")
	}
	if got.Vendor() != "alpha" {
		t.Fatalf("Get(alpha).Vendor() = %q, want %q", got.Vendor(), "alpha")
	}

	got, ok = r.Get("beta")
	if !ok {
		t.Fatal("Get(beta) not found")
	}
	if got.Vendor() != "beta" {
		t.Fatalf("Get(beta).Vendor() = %q, want %q", got.Vendor(), "beta")
	}

	list := r.List()
	if len(list) != 2 {
		t.Fatalf("List() len = %d, want 2; got %v", len(list), list)
	}
	// Registration order must be preserved.
	if list[0] != "alpha" || list[1] != "beta" {
		t.Fatalf("List() order = %v, want [alpha beta]", list)
	}
}

// TestRegistry_RegisterDuplicateVendorError proves that registering two backends
// for the same vendor is rejected. This assertion CAN fail if the duplicate
// check is removed.
func TestRegistry_RegisterDuplicateVendorError(t *testing.T) {
	r := NewBackendRegistry()

	b1 := &fakeBackend{vendor: "myvendor"}
	b2 := &fakeBackend{vendor: "myvendor"}

	if err := r.Register(b1); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := r.Register(b2); err == nil {
		t.Fatal("second Register for same vendor should have returned an error, got nil")
	}
}

// TestRegistry_AutoDetect_FindsBackendWithDevice proves that AutoDetect returns
// the first backend that reports at least one device. The test uses a fake
// backend with a device, bypassing real hardware.
//
// Anti-bluff: AutoDetect's real device-count check means this test FAILS if
// AutoDetect is changed to return any backend unconditionally.
func TestRegistry_AutoDetect_FindsBackendWithDevice(t *testing.T) {
	ctx := context.Background()
	r := NewBackendRegistry()

	// empty backend — no devices
	empty := &fakeBackend{vendor: "empty_vendor"}
	// non-preferred vendor name so it avoids the toolchain LookPath fast path
	// and still reaches DetectDevices.
	withDevice := &fakeBackend{
		vendor: "fake_with_device",
		devices: []DeviceInfo{
			{ID: "gpu-0", UUID: "FAKE-UUID-0", Vendor: "fake_with_device", Model: "FakeGPU-9000", MemoryMB: 8192},
		},
	}

	if err := r.Register(empty); err != nil {
		t.Fatalf("Register empty: %v", err)
	}
	if err := r.Register(withDevice); err != nil {
		t.Fatalf("Register withDevice: %v", err)
	}

	backend, err := r.AutoDetect(ctx)
	if err != nil {
		t.Fatalf("AutoDetect returned error: %v", err)
	}
	if backend.Vendor() != "fake_with_device" {
		t.Fatalf("AutoDetect returned vendor %q, want %q", backend.Vendor(), "fake_with_device")
	}

	// Prove the returned backend actually reports a device (sink-side).
	devices, err := backend.DetectDevices(ctx)
	if err != nil {
		t.Fatalf("DetectDevices on AutoDetect result: %v", err)
	}
	if len(devices) < 1 {
		t.Fatalf("backend returned by AutoDetect has 0 devices; expected >=1")
	}
}

// TestRegistry_AutoDetect_NoDevices proves that AutoDetect returns
// ErrNoBackendDetected when all registered backends report zero devices.
//
// Anti-bluff: this FAILS if AutoDetect is changed to return a backend even
// when no devices are found (which would be a PASS-bluff stub).
func TestRegistry_AutoDetect_NoDevices(t *testing.T) {
	ctx := context.Background()
	r := NewBackendRegistry()

	// Two backends, each with zero devices.
	e1 := &fakeBackend{vendor: "e1_empty"}
	e2 := &fakeBackend{vendor: "e2_empty"}
	if err := r.Register(e1); err != nil {
		t.Fatalf("Register e1: %v", err)
	}
	if err := r.Register(e2); err != nil {
		t.Fatalf("Register e2: %v", err)
	}

	_, err := r.AutoDetect(ctx)
	if err == nil {
		t.Fatal("AutoDetect returned nil error with all-empty backends; expected ErrNoBackendDetected")
	}
	if !errors.Is(err, ErrNoBackendDetected) {
		t.Fatalf("AutoDetect error = %v; want errors.Is(err, ErrNoBackendDetected) to be true", err)
	}
}

// TestRegistry_AutoDetect_EmptyRegistry proves the not-found error on an empty
// registry as well.
func TestRegistry_AutoDetect_EmptyRegistry(t *testing.T) {
	ctx := context.Background()
	r := NewBackendRegistry()

	_, err := r.AutoDetect(ctx)
	if err == nil {
		t.Fatal("AutoDetect on empty registry returned nil error; expected error")
	}
	if !errors.Is(err, ErrNoBackendDetected) {
		t.Fatalf("AutoDetect error = %v; want errors.Is(err, ErrNoBackendDetected) to be true", err)
	}
}
