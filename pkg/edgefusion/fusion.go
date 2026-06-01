// Package edgefusion provides an in-process stream-processing framework that
// merges sensor readings from multiple edge devices into fused outputs using
// tumbling windows.  The framework is synchronous and deterministic: given the
// same input it always produces the same output, making it straightforward to
// test with exact assertions.
package edgefusion

import (
	"sort"
	"time"
)

// WindowedFusion is a FusionOperator that computes the arithmetic mean of all
// readings in a window, giving equal weight to every sample regardless of
// which sensor produced it.
type WindowedFusion struct {
	Window time.Duration // tumbling-window size; set at construction, not used by Fuse itself
}

// Fuse returns the arithmetic mean of the Value field of every reading in
// window.  It returns 0 when the slice is empty (no division by zero).
func (WindowedFusion) Fuse(window []SensorReading) float64 {
	if len(window) == 0 {
		return 0
	}
	var sum float64
	for _, r := range window {
		sum += r.Value
	}
	return sum / float64(len(window))
}

// FuseStreams merges all readings from every stream, partitions them into
// non-overlapping tumbling windows of width `window` starting at time 0, and
// applies op.Fuse to each non-empty bucket.
//
// A reading with timestamp t falls into window floor(t/window)*window.
//
// The returned slice is ordered by WindowStart ascending.  The algorithm is
// fully synchronous (no goroutines) and deterministic.
func FuseStreams(streams []SensorStream, window time.Duration, op FusionOperator) []FusedOutput {
	if len(streams) == 0 || window <= 0 {
		return nil
	}

	// 1. Merge all readings into a flat slice.
	var all []SensorReading
	for _, s := range streams {
		all = append(all, s.Readings...)
	}
	if len(all) == 0 {
		return nil
	}

	// 2. Sort by Timestamp so that window iteration is deterministic.
	sort.Slice(all, func(i, j int) bool {
		if all[i].Timestamp != all[j].Timestamp {
			return all[i].Timestamp < all[j].Timestamp
		}
		// Secondary sort by SensorID for full determinism when timestamps are equal.
		return all[i].SensorID < all[j].SensorID
	})

	// 3. Bucket readings by their window key.
	type bucket struct {
		readings []SensorReading
	}
	buckets := make(map[time.Duration]*bucket)
	// Track insertion order so we can iterate windows in ascending order.
	var windowKeys []time.Duration
	seen := make(map[time.Duration]bool)

	for _, r := range all {
		// floor(Timestamp / window) * window
		key := (r.Timestamp / window) * window
		if !seen[key] {
			seen[key] = true
			windowKeys = append(windowKeys, key)
		}
		b := buckets[key]
		if b == nil {
			b = &bucket{}
			buckets[key] = b
		}
		b.readings = append(b.readings, r)
	}

	// windowKeys are already in ascending order because all readings were
	// sorted by Timestamp before bucketing; new keys are added in first-seen
	// order which is monotonically non-decreasing.  Sort explicitly for
	// correctness even if readings were out of order originally.
	sort.Slice(windowKeys, func(i, j int) bool {
		return windowKeys[i] < windowKeys[j]
	})

	// 4. For each non-empty window, compute FusedOutput.
	outputs := make([]FusedOutput, 0, len(windowKeys))
	for _, key := range windowKeys {
		b := buckets[key]
		if len(b.readings) == 0 {
			continue
		}

		// Count distinct sensor IDs.
		sensorSeen := make(map[string]bool)
		for _, r := range b.readings {
			sensorSeen[r.SensorID] = true
		}

		outputs = append(outputs, FusedOutput{
			WindowStart:  key,
			FusedValue:   op.Fuse(b.readings),
			SensorCount:  len(sensorSeen),
			ReadingCount: len(b.readings),
		})
	}

	return outputs
}
