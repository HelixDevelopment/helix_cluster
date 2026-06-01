package edgefusion

import "time"

// WorkloadClass identifies the sensor-fusion workload, distinct from batch or inference workloads.
const WorkloadClass = "sensor-fusion"

// SensorReading is a single measurement emitted by one edge device.
// Timestamp is an offset from an arbitrary epoch (not wall-clock) so that
// deterministic tests never depend on time.Now().
type SensorReading struct {
	SensorID  string
	Timestamp time.Duration // offset from start
	Value     float64
}

// SensorStream is the complete ordered stream of readings from one sensor device.
type SensorStream struct {
	SensorID string
	Readings []SensorReading
}

// FusedOutput is the result of applying a FusionOperator to all readings that
// fall inside a single tumbling window.
type FusedOutput struct {
	WindowStart  time.Duration
	FusedValue   float64
	SensorCount  int // distinct SensorIDs contributing readings in this window
	ReadingCount int // total number of readings in this window
}

// FusionOperator combines a slice of readings (all from the same window) into
// a single scalar value.
type FusionOperator interface {
	Fuse(window []SensorReading) float64
}
