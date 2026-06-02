package deviceplugin

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// ErrOversubscription is returned by Registry.Allocate when a request would
// allocate more devices of a given model than are currently allocatable.
var ErrOversubscription = errors.New("deviceplugin: oversubscription rejected")

// ErrUnknownModel is returned when a device request names a model that the
// Registry has never been fingerprinted with.
var ErrUnknownModel = errors.New("deviceplugin: unknown device model")

// ErrUnsupportedRequest is returned when Allocate is given a request component
// that is not a counted device request (e.g. a bare quantity like memory:10Gi),
// which the device Registry cannot satisfy.
var ErrUnsupportedRequest = errors.New("deviceplugin: only device (model) requests are allocatable")

// Allocation records a satisfied device request: the concrete device IDs that
// were reserved for it.
type Allocation struct {
	// Request is the canonical resource that was satisfied.
	Request Resource
	// DeviceIDs are the concrete devices reserved, len == Request.Count.
	DeviceIDs []string
}

// Registry is the scheduler-facing device Manager. Plugins push Fingerprints
// into it (in production over gRPC via the RegistrarServer, in-process in tests
// or embedded use). It tracks which devices are healthy/allocatable and which
// have been handed out, and enforces that no model is ever oversubscribed past
// its reported, healthy device count.
//
// Registry is safe for concurrent use.
type Registry struct {
	mu sync.Mutex

	// devices maps device ID -> the last-reported Device fingerprint.
	devices map[string]Device
	// allocated maps device ID -> true when the device has been handed out.
	allocated map[string]bool
	// owner maps device ID -> the PluginName that last reported it. This lets
	// ApplyFingerprint reconcile a plugin's CURRENT full device set against what
	// it previously reported, so a device that disappears from the plugin's
	// latest fingerprint (e.g. a GPU was unplugged) is pruned and stops being
	// allocatable. Without ownership tracking a vanished device would linger
	// forever and Allocate could hand a scheduler a device that no longer
	// exists — the CLAUDE-1 "tests pass but feature is broken" failure mode.
	owner map[string]string
}

// NewRegistry returns an empty Registry ready to receive fingerprints.
func NewRegistry() *Registry {
	return &Registry{
		devices:   make(map[string]Device),
		allocated: make(map[string]bool),
		owner:     make(map[string]string),
	}
}

// ApplyFingerprint ingests a plugin's CURRENT full device set.
//
// Per the Fingerprint.Devices contract ("the full set of devices this plugin
// currently manages"), this is a full reconciliation, not an incremental
// upsert:
//
//   - Every reported device is upserted by ID (re-reporting updates fields such
//     as health/utilization without disturbing an existing allocation on it).
//   - Any device that this same PluginName previously reported but that is ABSENT
//     from this fingerprint is PRUNED: it is removed from the inventory and any
//     allocation on it is released, because the plugin is telling us it no longer
//     manages that device. This prevents stale, unplugged devices from remaining
//     allocatable forever.
//
// Reconciliation is scoped by plugin owner: a fingerprint from plugin A never
// prunes devices owned by plugin B.
//
// The operation is ATOMIC on error: the entire fingerprint is validated before
// ANY mutation is applied. If a device has an empty ID, the call returns an
// error and the registry is left completely unchanged (no partial upsert, no
// partial prune), so a caller that sees rejection can trust inventory did not
// move. PluginName MUST be non-empty so ownership can be tracked for pruning.
func (r *Registry) ApplyFingerprint(fp Fingerprint) error {
	// Validate-all-before-apply: nothing below mutates state until every device
	// has passed validation, guaranteeing atomic ingest.
	if fp.PluginName == "" {
		return fmt.Errorf("deviceplugin: fingerprint with empty PluginName cannot be reconciled (owner tracking requires a plugin identity)")
	}
	seen := make(map[string]struct{}, len(fp.Devices))
	for _, d := range fp.Devices {
		if d.ID == "" {
			return fmt.Errorf("deviceplugin: fingerprint from %q contains a device with empty ID", fp.PluginName)
		}
		if _, dup := seen[d.ID]; dup {
			return fmt.Errorf("deviceplugin: fingerprint from %q reports device ID %q more than once", fp.PluginName, d.ID)
		}
		seen[d.ID] = struct{}{}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Upsert every reported device and (re)stamp its owner.
	for _, d := range fp.Devices {
		r.devices[d.ID] = d
		r.owner[d.ID] = fp.PluginName
	}

	// Prune devices this plugin used to own but no longer reports. We must
	// collect first and delete after, since we are ranging over r.owner.
	var stale []string
	for id, ownedBy := range r.owner {
		if ownedBy != fp.PluginName {
			continue
		}
		if _, present := seen[id]; present {
			continue
		}
		stale = append(stale, id)
	}
	for _, id := range stale {
		delete(r.devices, id)
		delete(r.owner, id)
		delete(r.allocated, id)
	}
	return nil
}

// Device returns the last-reported fingerprint for a device ID.
func (r *Registry) Device(id string) (Device, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[id]
	return d, ok
}

// Count returns the number of known devices. It is a cheap len-under-lock and
// is the preferred way to obtain an inventory size: unlike Inventory it neither
// allocates a snapshot slice nor sorts it.
func (r *Registry) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.devices)
}

// Inventory returns a snapshot of every known device, sorted by ID for
// deterministic output.
func (r *Registry) Inventory() []Device {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Device, 0, len(r.devices))
	for _, d := range r.devices {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Allocatable returns the number of currently allocatable (healthy and not yet
// allocated) devices of the given model. Matching is case-insensitive on model.
func (r *Registry) Allocatable(model string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.allocatableLocked(model)
}

// allocatableLocked returns the IDs of allocatable devices for a model. The
// caller MUST hold r.mu.
func (r *Registry) allocatableLocked(model string) int {
	return len(r.freeDeviceIDsLocked(model))
}

// freeDeviceIDsLocked returns the sorted IDs of healthy, unallocated devices of
// the given model. The caller MUST hold r.mu.
func (r *Registry) freeDeviceIDsLocked(model string) []string {
	var ids []string
	for id, d := range r.devices {
		if !equalModel(d.Model, model) {
			continue
		}
		if d.Health != HealthHealthy {
			continue
		}
		if r.allocated[id] {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// modelKnownLocked reports whether any device of the model has ever been
// reported, regardless of health/allocation. The caller MUST hold r.mu.
func (r *Registry) modelKnownLocked(model string) bool {
	for _, d := range r.devices {
		if equalModel(d.Model, model) {
			return true
		}
	}
	return false
}

// Allocate satisfies a counted device request (e.g. {Kind:"gpu",
// Model:"rtx3080", Count:2}) by reserving that many concrete healthy devices.
//
// It REJECTS the request with ErrOversubscription if fewer allocatable devices
// of the model remain than requested — this is the load-bearing oversubscription
// guard. The reservation is atomic: on rejection no devices are reserved.
func (r *Registry) Allocate(req Resource) (Allocation, error) {
	if !req.IsDeviceRequest() {
		return Allocation{}, fmt.Errorf("%w: %q", ErrUnsupportedRequest, req.String())
	}
	if req.Count <= 0 {
		return Allocation{}, fmt.Errorf("deviceplugin: allocate requires a positive count, got %d", req.Count)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.modelKnownLocked(req.Model) {
		return Allocation{}, fmt.Errorf("%w: %q", ErrUnknownModel, req.Model)
	}

	free := r.freeDeviceIDsLocked(req.Model)
	// Oversubscription guard: reject if demand exceeds the allocatable supply.
	if len(free) < req.Count {
		return Allocation{}, fmt.Errorf("%w: model %q wants %d but only %d allocatable",
			ErrOversubscription, req.Model, req.Count, len(free))
	}

	chosen := free[:req.Count]
	for _, id := range chosen {
		r.allocated[id] = true
	}
	out := make([]string, len(chosen))
	copy(out, chosen)
	return Allocation{Request: req, DeviceIDs: out}, nil
}

// Release returns previously-allocated devices to the allocatable pool and
// reports how many of the given IDs were actually allocated (and thus freed).
//
// Unknown or already-free IDs are ignored so Release is idempotent — but they
// are NOT counted. Callers who want to detect a double-free or a typo'd ID can
// compare the returned count against len(ids): a mismatch means at least one ID
// was not an outstanding allocation. Silently tolerating unknown IDs is a
// deliberate safety choice (release must never panic), while the count gives the
// caller the means to spot a bug instead of having it masked.
func (r *Registry) Release(ids ...string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	released := 0
	for _, id := range ids {
		if r.allocated[id] {
			delete(r.allocated, id)
			released++
		}
	}
	return released
}

// equalModel compares two model strings case-insensitively without allocating.
func equalModel(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
