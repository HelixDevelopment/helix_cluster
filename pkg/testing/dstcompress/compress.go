// Package dstcompress measures and proves the virtual-time compression ratio
// of a dst.Engine and provides a batch scheduling helper for independent
// same-timestamp events.
//
// Background: dst.Engine.Run() fast-forwards virtual time by jumping the
// clock directly to the next event's timestamp — there is no real sleep.
// For a simulation spanning many hours of virtual time (e.g. 24 h) the wall
// clock advances only by the CPU time spent dispatching handlers, yielding
// compression ratios of thousands-to-one or higher.
package dstcompress

import (
	"fmt"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/testing/dst"
)

// CompressionMetrics holds the measured virtual-vs-wall-clock compression data
// for a single Measure call.
type CompressionMetrics struct {
	// VirtualElapsed is the amount of simulated (virtual) time advanced during
	// the Run call.
	VirtualElapsed time.Duration

	// WallElapsed is the real wall-clock time spent executing the Run call.
	WallElapsed time.Duration

	// Ratio is VirtualElapsed / WallElapsed  (protected against divide-by-zero
	// by a 1 ns floor on the denominator).
	//
	// A Ratio of 10 means 10 ns of virtual time were simulated for every real
	// ns of wall time, i.e. 10:1 compression.
	Ratio float64

	// EventsProcessed is the number of events dispatched by Run.
	EventsProcessed int
}

// String returns a human-readable summary of the metrics.
func (m CompressionMetrics) String() string {
	return fmt.Sprintf(
		"virtual=%s wall=%s ratio=%.1f:1 events=%d",
		m.VirtualElapsed, m.WallElapsed, m.Ratio, m.EventsProcessed,
	)
}

// floorNs guards against divide-by-zero: returns v if v >= 1, else 1.
func floorNs(v int64) int64 {
	if v >= 1 {
		return v
	}
	return 1
}

// Measure runs up to maxEvents events on eng, records real wall-clock time via
// time.Now() / time.Since(), records the virtual time advanced, and computes the
// compression Ratio.
//
// The caller must have already scheduled events and registered handlers before
// calling Measure; Measure does not modify the engine's event queue or handlers.
//
// Note: vStart / wStart are snapped as close together as possible immediately
// before Run, and wStop is snapped immediately after, so the wall measurement
// covers only the Run execution, not scheduling overhead.
func Measure(eng *dst.Engine, maxEvents int) CompressionMetrics {
	vStart := eng.Now()
	wStart := time.Now()

	processed := eng.Run(maxEvents)

	wallElapsed := time.Since(wStart)
	virtualElapsed := eng.Now().Sub(vStart)

	ratio := float64(virtualElapsed.Nanoseconds()) /
		float64(floorNs(wallElapsed.Nanoseconds()))

	return CompressionMetrics{
		VirtualElapsed:  virtualElapsed,
		WallElapsed:     wallElapsed,
		Ratio:           ratio,
		EventsProcessed: processed,
	}
}

// BatchSchedule schedules len(payloads) independent events of the given kind,
// all at the same virtual timestamp (eng.Now() + delay nanoseconds).
// Each payload in the slice becomes the Payload of one event.
// The returned slice contains the event IDs in the same order as payloads.
//
// Events are independent in the sense that they share only their timestamp;
// the engine dispatches them in the order they were inserted (FIFO within a
// timestamp tier because the min-heap is stable for equal timestamps by
// insertion order via the monotonically incrementing event ID).
func BatchSchedule(eng *dst.Engine, kind string, payloads []interface{}, delay int64) []int64 {
	ids := make([]int64, len(payloads))
	for i, p := range payloads {
		ids[i] = eng.ScheduleEvent(kind, p, delay)
	}
	return ids
}
