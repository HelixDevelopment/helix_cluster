package gpu

import (
	"context"
	"fmt"
	"sync"
)

// SharingMode describes how a single GPU device is shared among multiple jobs.
type SharingMode int

const (
	// ShareExclusive grants the device to a single job at a time.  Any attempt
	// to co-locate a second job is rejected until the first one is released.
	ShareExclusive SharingMode = iota

	// ShareMPS allows multiple jobs to share the device concurrently using
	// NVIDIA Multi-Process Service (or equivalent).  The device must not
	// already be set to Exclusive mode.
	ShareMPS

	// ShareTimeSlice admits jobs against a per-device time budget.  Each
	// SharingRequest carries its own TimeSliceMS; admission is rejected when
	// quantumUsedMS + req.TimeSliceMS would exceed the device total quantum.
	ShareTimeSlice

	// ShareMIG admits jobs against a per-device MIG partition count.  Each
	// admitted job consumes one partition; admission is rejected when the
	// device limit is reached.
	ShareMIG
)

var sharingModeNames = map[SharingMode]string{
	ShareExclusive: "exclusive",
	ShareMPS:       "mps",
	ShareTimeSlice: "time-slice",
	ShareMIG:       "mig",
}

// String returns the human-readable name of the SharingMode.
func (m SharingMode) String() string {
	if name, ok := sharingModeNames[m]; ok {
		return name
	}
	return fmt.Sprintf("SharingMode(%d)", int(m))
}

// SharingRequest is the per-job admission request for a device.
type SharingRequest struct {
	// Mode is the sharing mode being requested.
	Mode SharingMode

	// JobID is a unique identifier for the job.
	JobID string

	// TimeSliceMS is the milliseconds of time-slice budget the job requires.
	// Only relevant for ShareTimeSlice; ignored for other modes.
	TimeSliceMS int

	// MIGProfile is the requested MIG profile identifier (e.g. "1g.5gb").
	// Only relevant for ShareMIG; ignored for other modes.
	MIGProfile string
}

// DeviceSharingState tracks the live sharing state for a single GPU device.
// It is safe for concurrent use.
type DeviceSharingState struct {
	mu sync.Mutex

	// DeviceID is the backend-assigned identifier for the device.
	DeviceID string

	// Mode is the sharing mode currently in effect for the device.
	// Unset (zero value = ShareExclusive) until the first job is admitted.
	Mode SharingMode

	// jobs maps jobID to the SharingRequest that was admitted.
	jobs map[string]SharingRequest

	// quantumUsedMS is the sum of TimeSliceMS across all admitted time-slice
	// jobs; only meaningful when Mode == ShareTimeSlice.
	quantumUsedMS int

	// migPartitionsUsed is the count of admitted MIG jobs; only meaningful
	// when Mode == ShareMIG.
	migPartitionsUsed int
}

// NewDeviceSharingState constructs an empty DeviceSharingState for deviceID.
func NewDeviceSharingState(deviceID string) *DeviceSharingState {
	return &DeviceSharingState{
		DeviceID: deviceID,
		jobs:     make(map[string]SharingRequest),
	}
}

// Admit attempts to admit req onto the device.  deviceTotalQuantumMS is the
// maximum milliseconds budget for the device (used by ShareTimeSlice).
// deviceMIGmax is the maximum number of MIG partitions supported (used by
// ShareMIG).  On success the job is tracked and nil is returned.  On failure
// a descriptive error is returned and state is unchanged.
func (s *DeviceSharingState) Admit(req SharingRequest, deviceTotalQuantumMS, deviceMIGmax int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if req.JobID == "" {
		return fmt.Errorf("gpu sharing: JobID must not be empty")
	}
	if _, exists := s.jobs[req.JobID]; exists {
		return fmt.Errorf("gpu sharing: job %q is already admitted on device %q", req.JobID, s.DeviceID)
	}

	// If there are already jobs present, the incoming mode must be compatible
	// with the mode that was established by the first admitted job.
	if len(s.jobs) > 0 && req.Mode != s.Mode {
		return fmt.Errorf(
			"gpu sharing: mode mismatch on device %q: device is in %s mode, request is %s",
			s.DeviceID, s.Mode, req.Mode,
		)
	}

	switch req.Mode {
	case ShareExclusive:
		// Exclusive: reject co-location — any existing job blocks admission.
		if len(s.jobs) > 0 {
			return fmt.Errorf(
				"gpu sharing: exclusive mode on device %q: already has %d job(s) co-located",
				s.DeviceID, len(s.jobs),
			)
		}

	case ShareMPS:
		// MPS allows multiple concurrent clients.  The only additional check
		// is the mode-mismatch guard above (already applied).

	case ShareTimeSlice:
		required := req.TimeSliceMS
		if required <= 0 {
			return fmt.Errorf("gpu sharing: TimeSliceMS must be > 0 for time-slice mode, got %d", required)
		}
		if s.quantumUsedMS+required > deviceTotalQuantumMS {
			return fmt.Errorf(
				"gpu sharing: time-slice over-subscription on device %q: %dms used + %dms requested > %dms budget",
				s.DeviceID, s.quantumUsedMS, required, deviceTotalQuantumMS,
			)
		}

	case ShareMIG:
		if s.migPartitionsUsed >= deviceMIGmax {
			return fmt.Errorf(
				"gpu sharing: MIG partition limit reached on device %q: %d/%d partitions in use",
				s.DeviceID, s.migPartitionsUsed, deviceMIGmax,
			)
		}

	default:
		return fmt.Errorf("gpu sharing: unknown SharingMode %d", int(req.Mode))
	}

	// Commit.
	s.Mode = req.Mode
	s.jobs[req.JobID] = req
	switch req.Mode {
	case ShareTimeSlice:
		s.quantumUsedMS += req.TimeSliceMS
	case ShareMIG:
		s.migPartitionsUsed++
	}
	return nil
}

// Release removes the job identified by jobID from the device, freeing its
// associated quantum or partition slot.  Returns an error if the job is not
// currently admitted.
func (s *DeviceSharingState) Release(jobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	req, exists := s.jobs[jobID]
	if !exists {
		return fmt.Errorf("gpu sharing: job %q is not admitted on device %q", jobID, s.DeviceID)
	}

	delete(s.jobs, jobID)

	switch req.Mode {
	case ShareTimeSlice:
		s.quantumUsedMS -= req.TimeSliceMS
		if s.quantumUsedMS < 0 {
			s.quantumUsedMS = 0 // defensive floor
		}
	case ShareMIG:
		s.migPartitionsUsed--
		if s.migPartitionsUsed < 0 {
			s.migPartitionsUsed = 0 // defensive floor
		}
	}

	// When the device becomes empty, reset Mode to the zero value so the next
	// Admit can establish a fresh mode without a stale mismatch.
	if len(s.jobs) == 0 {
		s.Mode = ShareExclusive
		s.quantumUsedMS = 0
		s.migPartitionsUsed = 0
	}
	return nil
}

// ConfigureSharing attempts to apply the hardware-level sharing configuration
// described by req to the device identified by deviceID on backend b.
//
// For AppleBackend (and any backend that does not expose MPS/MIG hardware
// partitioning), this returns ErrUnsupported.  The caller MUST test via
// errors.Is(err, ErrUnsupported).  The admission state machine (DeviceSharingState)
// is always host-provable and independent of hardware support; this function is
// ONLY the hardware-enable seam and must NOT fake success.
func ConfigureSharing(ctx context.Context, b GPUBackend, deviceID string, req SharingRequest) error {
	switch req.Mode {
	case ShareMPS, ShareTimeSlice:
		// Both modes map to the MPS/time-slicing facility exposed by GPUBackend.
		sliceMS := req.TimeSliceMS
		if sliceMS <= 0 {
			sliceMS = 1 // default: backend's minimum slice
		}
		if err := b.EnableMPS(ctx, deviceID, sliceMS); err != nil {
			// ErrUnsupported propagates unchanged so callers can errors.Is check.
			return fmt.Errorf("gpu sharing: ConfigureSharing %s on device %q: %w", req.Mode, deviceID, err)
		}
		return nil

	case ShareMIG:
		// MIG partitioning is a separate hardware facility.  No current
		// GPUBackend exposes a MIG-configure method; return ErrUnsupported so
		// that the caller knows hardware MIG is not available on this host.
		return fmt.Errorf("gpu sharing: ConfigureSharing MIG on device %q: %w", deviceID, ErrUnsupported)

	case ShareExclusive:
		// Exclusive mode does not require any hardware configuration change;
		// it is purely a scheduler-side constraint.
		return nil

	default:
		return fmt.Errorf("gpu sharing: ConfigureSharing unknown mode %d on device %q: %w",
			int(req.Mode), deviceID, ErrUnsupported)
	}
}
