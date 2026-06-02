package gpu

import (
	"errors"
	"fmt"
	"sync"
)

// MIGProfile describes a MIG (Multi-Instance GPU) slice configuration.
//
// WHY a named profile rather than raw integers: NVIDIA MIG profiles have
// well-known SKU names (e.g. "1g.10gb") that hardware operators recognise; the
// name is the stable API handle while slice count and memory are the derived
// implementation values. Keeping the three together prevents callers from
// accidentally inventing profiles that do not correspond to real partition plans.
//
// WHY immutable (read-only exported fields): MIG hardware partitions are FIXED
// at creation time — the GPU must be reset to change them. Representing that
// constraint in the type (no setters, no exported mutating fields behind a
// pointer) prevents any in-process code from attempting a live reconfiguration,
// which would be both incorrect and dangerously misleading to an operator.
type MIGProfile struct {
	// Name is the canonical SKU name, e.g. "1g.10gb". It is the lookup key in
	// the standard catalog.
	Name string
	// Slices is the number of GPU compute slices consumed by this profile. A
	// full H100/H200/B200 class GPU exposes 7 compute slices.
	Slices int
	// MemoryGB is the GPU memory consumed by one instance of this profile.
	MemoryGB int
}

// Standard MIG profile catalog for H100 / H200 / B200 class devices (80 GB
// HBM, 7 compute slices). These are the four vGPU tiers supported by the Helix
// scheduler. The values mirror the NVIDIA-defined partition plan for an 80 GB
// SXM device:
//
//   - 1g.10gb  → 1 slice, 10 GB  → up to 7 instances
//   - 2g.20gb  → 2 slices, 20 GB → up to 3 instances (6 slices, leaving 1 unused)
//   - 3g.40gb  → 3 slices, 40 GB → up to 2 instances (6 slices, leaving 1 unused)
//   - 7g.80gb  → 7 slices, 80 GB → 1 instance (the whole device)
//
// WHY these four: they cover the four standard Helix vGPU tiers (Small/Medium/
// Large/Full). Any other partition shape requires a custom MIGProfile and is
// NOT in the catalog — unknown profiles are rejected by the partition function.
var (
	// ProfileSmall is "vGPU-Small": 1g.10gb.
	ProfileSmall = MIGProfile{Name: "1g.10gb", Slices: 1, MemoryGB: 10}
	// ProfileMedium is "vGPU-Medium": 2g.20gb.
	ProfileMedium = MIGProfile{Name: "2g.20gb", Slices: 2, MemoryGB: 20}
	// ProfileLarge is "vGPU-Large": 3g.40gb.
	ProfileLarge = MIGProfile{Name: "3g.40gb", Slices: 3, MemoryGB: 40}
	// ProfileFull is "full": 7g.80gb.
	ProfileFull = MIGProfile{Name: "7g.80gb", Slices: 7, MemoryGB: 80}
)

// StandardProfiles is the ordered catalog of Helix-supported MIG profiles,
// keyed by their canonical SKU name. It is populated at package init so it is
// always consistent with the profile variables above.
var StandardProfiles map[string]MIGProfile

func init() {
	// Build the catalog from the canonical profile variables — a single source
	// of truth means adding a new profile here automatically exposes it via
	// LookupProfile without a separate catalog-sync step.
	StandardProfiles = map[string]MIGProfile{
		ProfileSmall.Name:  ProfileSmall,
		ProfileMedium.Name: ProfileMedium,
		ProfileLarge.Name:  ProfileLarge,
		ProfileFull.Name:   ProfileFull,
	}
}

// ErrProfileNotFound is returned by LookupProfile when the requested name does
// not correspond to a catalog entry. It is a sentinel so callers can distinguish
// "unknown profile" from other errors without string-matching.
var ErrProfileNotFound = errors.New("mig: profile not found in standard catalog")

// ErrOversubscribed is returned by PartitionGPU when the requested profile
// would exceed the device's slice or memory capacity. It is a sentinel so callers
// can distinguish oversubscription from "no such GPU" or "profile unknown".
var ErrOversubscribed = errors.New("mig: partition would oversubscribe device capacity")

// ErrMIGAlreadyPartitioned is returned by PartitionGPU when a MIGDevice is
// already partitioned and cannot be repartitioned without reset.
var ErrMIGAlreadyPartitioned = errors.New("mig: device is already partitioned; reconfiguration requires a device reset")

// LookupProfile retrieves a MIGProfile from the standard catalog by its SKU
// name. It returns ErrProfileNotFound if the name is not known.
func LookupProfile(name string) (MIGProfile, error) {
	if p, ok := StandardProfiles[name]; ok {
		return p, nil
	}
	return MIGProfile{}, fmt.Errorf("%w: %q", ErrProfileNotFound, name)
}

// MIGInstance represents a single MIG instance carved from a physical GPU. It
// is read-only after creation — per NVIDIA MIG semantics, instances are fixed
// until the device is reset. The only mutable state is its allocation (which
// job, if any, holds it).
type MIGInstance struct {
	// ID uniquely identifies the instance within its parent device.
	// Format: "<gpuID>-mig-<index>".
	ID string
	// Profile is the partition plan that defines this instance's resources.
	Profile MIGProfile
	// ParentGPUID is the ID of the physical GPU from which this instance was
	// created. It is the link back to GPU.ID in the inventory.
	ParentGPUID string
}

// MIGDevice is the in-memory representation of a physical GPU partitioned into
// MIG instances. It is safe for concurrent use.
//
// WHY separate from Manager's GPU slice: MIG operation replaces whole-device
// allocation with fine-grained instance allocation. A MIGDevice tracks both the
// partition plan (profile, instance count) and per-instance allocation state so
// the scheduler can decide which instance to hand to a job without re-reading
// hardware on every decision.
//
// Immutability contract: once Partitioned is true, no field may change except
// per-instance allocation state. Enforced by returning ErrMIGAlreadyPartitioned
// from PartitionGPU.
type MIGDevice struct {
	mu sync.RWMutex

	// GPUID is the Manager inventory ID of the physical GPU backing this
	// MIGDevice.
	GPUID string
	// TotalSlices is the total number of compute slices on this physical GPU.
	// Typically 7 for H100/H200/B200.
	TotalSlices int
	// TotalMemoryGB is the total GPU memory in GB (e.g. 80 for an 80 GB device).
	TotalMemoryGB int

	// Partitioned records whether PartitionGPU has been called. Once true the
	// partition plan cannot change — the MIG hardware constraint: a re-partition
	// requires a GPU reset which is an operator action, not a runtime call.
	Partitioned bool

	// Profile is the MIGProfile used to partition this device. Valid only when
	// Partitioned is true.
	Profile MIGProfile

	// instances are the concrete MIG slices carved from this device.
	// Populated by PartitionGPU; read-only afterwards.
	instances []*MIGInstance
}

// NewMIGDevice creates an unpartitioned MIGDevice for the given GPU.
//
// totalSlices and totalMemoryGB must match the physical device. For H100/H200/
// B200-class devices these are 7 and 80 respectively.
func NewMIGDevice(gpuID string, totalSlices, totalMemoryGB int) (*MIGDevice, error) {
	if gpuID == "" {
		return nil, fmt.Errorf("mig: gpuID must not be empty")
	}
	if totalSlices <= 0 {
		return nil, fmt.Errorf("mig: totalSlices must be > 0, got %d", totalSlices)
	}
	if totalMemoryGB <= 0 {
		return nil, fmt.Errorf("mig: totalMemoryGB must be > 0, got %d", totalMemoryGB)
	}
	return &MIGDevice{
		GPUID:         gpuID,
		TotalSlices:   totalSlices,
		TotalMemoryGB: totalMemoryGB,
	}, nil
}

// PartitionGPU partitions the device into as many MIGInstance objects as the
// profile allows within the device's slice and memory capacity.
//
// Rules enforced (per CLAUDE-1 / spec):
//  1. The profile must fit the device: both floor(TotalSlices/profile.Slices) and
//     floor(TotalMemoryGB/profile.MemoryGB) must be >= 1, i.e. the profile's slice
//     and memory requirements must individually not exceed the device. If either
//     bound is exceeded, ErrOversubscribed is returned and the device is NOT modified.
//  2. The device may be partitioned at most once. A second call returns
//     ErrMIGAlreadyPartitioned (the hardware constraint: live reconfiguration
//     is impossible without a reset, which is an operator action).
//  3. The instance count is floor(TotalSlices / profile.Slices) limited by
//     floor(TotalMemoryGB / profile.MemoryGB) — the binding resource is the
//     more constrained of the two.
//
// On success the device transitions to Partitioned=true and the instances slice
// is populated.
//
// Mutation guard: the load-bearing oversubscription check is the "count < 1"
// comparison at the end of the capacity computation (below). With integer floor
// division, any profile whose Slices or MemoryGB exceeds the device total will
// produce a per-resource ratio of 0, making count = min(0, …) = 0 and
// triggering ErrOversubscribed. Removing that guard would allow count=0 to
// reach the instance-creation loop, returning an empty (non-nil) slice with a
// nil error — TestMIGPartition_OversubscriptionRejected and
// TestMIGPartition_OversubscriptionRejected_MemoryBound both assert that the
// error IS NOT nil and IS ErrOversubscribed, so they fail when the guard is
// removed. The TestMIGPartition_OversubscriptionRejected_GuardIsolation test
// additionally proves that the sole source of the rejection is the count<1
// branch, not any other pre-filter.
func (d *MIGDevice) PartitionGPU(profile MIGProfile) ([]*MIGInstance, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Immutability guard: no live reconfiguration.
	if d.Partitioned {
		return nil, fmt.Errorf("%w: GPU %s", ErrMIGAlreadyPartitioned, d.GPUID)
	}

	// Profile sanity: zero or negative dimensions are always invalid regardless
	// of the device. This is a programmer-error check, not an oversubscription
	// check.
	if profile.Slices <= 0 || profile.MemoryGB <= 0 {
		return nil, fmt.Errorf("mig: profile %q has invalid slices (%d) or memoryGB (%d)", profile.Name, profile.Slices, profile.MemoryGB)
	}

	// Compute instance count: the binding resource (most constrained) determines
	// how many instances can coexist. Integer floor division naturally produces 0
	// whenever the profile dimension exceeds the device total — that is the
	// oversubscription signal.
	//
	// WHY no separate "profile.Slices > d.TotalSlices" guard: such a guard is
	// exactly redundant with the count<1 check below. When profile.Slices >
	// d.TotalSlices, floor(TotalSlices/profile.Slices) == 0, so count == 0 and
	// the count<1 branch fires. Keeping two mechanisms for the same invariant is
	// misleading because it suggests one could be removed while the other holds —
	// but only count<1 is the genuine load-bearing gate, as proven by
	// TestMIGPartition_OversubscriptionRejected_GuardIsolation.
	countBySlices := d.TotalSlices / profile.Slices
	countByMemory := d.TotalMemoryGB / profile.MemoryGB
	count := countBySlices
	if countByMemory < count {
		count = countByMemory
	}

	// LOAD-BEARING oversubscription guard (mutation anchor).
	// Removing or inverting this check ("count < 1" → "count < 0") causes
	// TestMIGPartition_OversubscriptionRejected and
	// TestMIGPartition_OversubscriptionRejected_MemoryBound to pass a nil error
	// to the fatal-if-nil assertion, failing both tests.
	if count < 1 {
		return nil, fmt.Errorf("%w: profile %q cannot fit any instance in device %s (slices %d/%d, memGB %d/%d)",
			ErrOversubscribed, profile.Name, d.GPUID,
			profile.Slices, d.TotalSlices, profile.MemoryGB, d.TotalMemoryGB)
	}

	instances := make([]*MIGInstance, count)
	for i := range instances {
		instances[i] = &MIGInstance{
			ID:          fmt.Sprintf("%s-mig-%d", d.GPUID, i),
			Profile:     profile,
			ParentGPUID: d.GPUID,
		}
	}

	// Commit: from this point the device is permanently partitioned.
	d.Partitioned = true
	d.Profile = profile
	d.instances = instances

	// Return a copy of the slice so callers cannot mutate d.instances directly.
	out := make([]*MIGInstance, count)
	copy(out, instances)
	return out, nil
}

// Instances returns a snapshot of the MIG instances created by PartitionGPU.
// Returns nil if the device has not been partitioned yet.
func (d *MIGDevice) Instances() []*MIGInstance {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if !d.Partitioned {
		return nil
	}
	out := make([]*MIGInstance, len(d.instances))
	copy(out, d.instances)
	return out
}

// IsPartitioned reports whether the device has been partitioned.
func (d *MIGDevice) IsPartitioned() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.Partitioned
}
