package device

import (
	"context"
	"errors"
	"fmt"
)

// ErrHardwareBackendUnavailable is the honest, explicit signal that a real
// hardware or emulator backend has not been wired into this build. It is NOT a
// silent no-op: every method of HardwareProvisioner returns this error so a
// caller can never mistake an absent backend for a working one.
var ErrHardwareBackendUnavailable = errors.New("device: real hardware backend not available in this build")

// HardwareBackend is the seam a real device-matrix backend (USB-attached phones,
// QEMU/Android-emulator farms, bare-metal racks, cloud device clouds) must
// implement to plug into this lifecycle manager. It is deliberately left as an
// interface with no in-tree implementation: the real backends are out of scope
// for this package and modelling them as a working SoftwareProvisioner would be
// a bluff. Implementations live behind build tags / separate modules.
type HardwareBackend interface {
	// Bringup powers on / boots a physical or emulated device for spec and
	// returns an opaque backend handle (e.g. a serial number or emulator port).
	Bringup(ctx context.Context, spec DeviceSpec) (handle string, err error)
	// Teardown powers off / frees the backend device identified by handle.
	Teardown(ctx context.Context, handle string) error
	// Probe reports whether handle is currently healthy/reachable.
	Probe(ctx context.Context, handle string) (healthy bool, err error)
}

// HardwareProvisioner adapts a HardwareBackend to the Provisioner interface. When
// constructed without a backend (NewHardwareProvisioner(nil)) it is an HONEST
// stub: every lifecycle call fails fast with ErrHardwareBackendUnavailable
// rather than pretending to succeed.
type HardwareProvisioner struct {
	backend HardwareBackend
}

// NewHardwareProvisioner wraps a HardwareBackend. Passing nil yields a stub that
// fails every call with ErrHardwareBackendUnavailable.
func NewHardwareProvisioner(backend HardwareBackend) *HardwareProvisioner {
	return &HardwareProvisioner{backend: backend}
}

// Provision implements Provisioner against the real backend, or fails honestly.
func (h *HardwareProvisioner) Provision(ctx context.Context, spec DeviceSpec) (*Device, error) {
	if h.backend == nil {
		return nil, ErrHardwareBackendUnavailable
	}
	handle, err := h.backend.Bringup(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("device: hardware bringup: %w", err)
	}
	d := &Device{
		ID:    handle,
		Spec:  spec,
		state: StateRequested,
		clock: realClock,
	}
	d.recordOrigin()
	if err := d.transition(StateProvisioning); err != nil {
		return nil, err
	}
	if err := d.transition(StateReady); err != nil {
		return nil, err
	}
	return d, nil
}

// Release implements Provisioner against the real backend, or fails honestly.
func (h *HardwareProvisioner) Release(ctx context.Context, d *Device) error {
	if h.backend == nil {
		return ErrHardwareBackendUnavailable
	}
	if err := d.transition(StateReleasing); err != nil {
		return fmt.Errorf("device %s: %w", d.ID, err)
	}
	if err := h.backend.Teardown(ctx, d.ID); err != nil {
		_ = d.transition(StateFailed)
		return fmt.Errorf("device %s: hardware teardown: %w", d.ID, err)
	}
	if err := d.transition(StateReleased); err != nil {
		return fmt.Errorf("device %s: %w", d.ID, err)
	}
	return nil
}

// Status implements Provisioner.
func (h *HardwareProvisioner) Status(d *Device) State { return d.State() }

// Compile-time proof HardwareProvisioner satisfies the interface.
var _ Provisioner = (*HardwareProvisioner)(nil)
