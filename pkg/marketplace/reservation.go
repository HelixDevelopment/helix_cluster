package marketplace

import (
	"errors"
	"fmt"
	"sync"
)

// When the cluster rents GPU capacity from a marketplace it must not over-commit
// a GPU class. Capacity is partitioned BY CLASS (e.g. "A100", "H100"): a job that
// wants H100s can only draw from the H100 pool, never from spare A100 capacity,
// and the sum of live reservations in a class must never exceed that class's
// total. This file implements a GPUReservationTable that enforces exactly that —
// it REALLY denies cross-class over-use rather than silently merging pools.

// ErrUnknownClass is returned when a reservation names a GPU class that has no
// configured capacity. A job for a class the cluster does not provision must be
// denied, not served from another class.
var ErrUnknownClass = errors.New("marketplace: unknown gpu class")

// ErrInsufficientCapacity is returned when a reservation would push a class's
// live usage above its total capacity. The class is named in the wrapped error.
var ErrInsufficientCapacity = errors.New("marketplace: insufficient gpu capacity in class")

// ErrInvalidReservation is returned for a non-positive requested count.
var ErrInvalidReservation = errors.New("marketplace: reservation count must be positive")

// Reservation is a handle to a successful GPU reservation. Release returns the
// units to the class pool. Release is idempotent: a second call is a no-op.
type Reservation struct {
	Class string
	Count int

	table    *GPUReservationTable
	released bool
}

// GPUReservationTable tracks per-class GPU capacity and live usage and enforces
// that reservations never exceed a class's capacity and never cross class
// boundaries. It is safe for concurrent use.
type GPUReservationTable struct {
	mu       sync.Mutex
	capacity map[string]int // class -> total units
	used     map[string]int // class -> currently reserved units
}

// NewGPUReservationTable builds a table from a class -> total-capacity map. The
// map is copied so later mutation of the caller's map cannot corrupt the table.
// Classes absent from the map are unknown and will be denied.
func NewGPUReservationTable(capacity map[string]int) *GPUReservationTable {
	cap2 := make(map[string]int, len(capacity))
	for k, v := range capacity {
		cap2[k] = v
	}
	return &GPUReservationTable{
		capacity: cap2,
		used:     make(map[string]int, len(capacity)),
	}
}

// Available reports the remaining free units in a class. A class with no
// configured capacity reports 0 (and ok=false).
func (t *GPUReservationTable) Available(class string) (free int, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	total, known := t.capacity[class]
	if !known {
		return 0, false
	}
	return total - t.used[class], true
}

// Reserve attempts to reserve count units in the named class. It returns a
// Reservation on success. It denies — returning an error and a nil reservation —
// when:
//
//   - count <= 0 (ErrInvalidReservation),
//   - the class is not configured (ErrUnknownClass): NO cross-class fallback,
//   - the reservation would exceed the class's remaining capacity
//     (ErrInsufficientCapacity).
//
// Crucially, capacity is checked WITHIN the requested class only. Spare units in
// a different class can never satisfy this request — that is the cross-class
// over-use denial.
func (t *GPUReservationTable) Reserve(class string, count int) (*Reservation, error) {
	if count <= 0 {
		return nil, fmt.Errorf("%w: %d", ErrInvalidReservation, count)
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	total, known := t.capacity[class]
	if !known {
		return nil, fmt.Errorf("%w: %q", ErrUnknownClass, class)
	}
	if t.used[class]+count > total {
		return nil, fmt.Errorf("%w %q: have %d free, requested %d",
			ErrInsufficientCapacity, class, total-t.used[class], count)
	}
	t.used[class] += count
	return &Reservation{Class: class, Count: count, table: t}, nil
}

// Release returns this reservation's units to its class pool. It is idempotent.
func (r *Reservation) Release() {
	if r == nil || r.released || r.table == nil {
		return
	}
	t := r.table
	t.mu.Lock()
	defer t.mu.Unlock()
	t.used[r.Class] -= r.Count
	if t.used[r.Class] < 0 {
		t.used[r.Class] = 0
	}
	r.released = true
}
