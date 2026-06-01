package edgefusion

import (
	"math"
	"testing"
	"time"
)

// streamFixtures returns three deterministic sensor streams covering three
// one-second tumbling windows.  The values are chosen so that the fused mean
// is computable exactly (window 0 and 2) or within floating-point tolerance
// (window 1).
func streamFixtures() []SensorStream {
	return []SensorStream{
		{
			SensorID: "temp",
			Readings: []SensorReading{
				{SensorID: "temp", Timestamp: 0 * time.Second, Value: 20},
				{SensorID: "temp", Timestamp: 1 * time.Second, Value: 21},
				{SensorID: "temp", Timestamp: 2 * time.Second, Value: 22},
			},
		},
		{
			SensorID: "humidity",
			Readings: []SensorReading{
				{SensorID: "humidity", Timestamp: 500 * time.Millisecond, Value: 50},
				{SensorID: "humidity", Timestamp: 1500 * time.Millisecond, Value: 52},
			},
		},
		{
			SensorID: "pressure",
			Readings: []SensorReading{
				{SensorID: "pressure", Timestamp: 200 * time.Millisecond, Value: 1013},
				{SensorID: "pressure", Timestamp: 1200 * time.Millisecond, Value: 1012},
			},
		},
	}
}

// TestFuseStreams_ExactWindows is the primary anti-bluff test.  It feeds three
// sensor streams into FuseStreams and asserts the EXACT fused output for each
// of the three 1-second windows.
//
// Anti-bluff properties:
//   - If Fuse computes a sum instead of a mean, window 0 gives 1083 ≠ 361.
//   - If any stream is dropped, SensorCount < 3 in windows 0 and 1.
//   - If the wrong readings land in a bucket, FusedValue diverges from the
//     expected mean.
//   - If ReadingCount is wrong, the fraction is computed over the wrong n.
func TestFuseStreams_ExactWindows(t *testing.T) {
	streams := streamFixtures()
	op := WindowedFusion{Window: time.Second}
	outputs := FuseStreams(streams, time.Second, op)

	if len(outputs) != 3 {
		t.Fatalf("expected 3 fused windows, got %d", len(outputs))
	}

	// ---- window 0: [0s, 1s) ------------------------------------------------
	// readings: temp=20, pressure=1013, humidity=50
	// mean = (20 + 1013 + 50) / 3 = 1083/3 = 361.0 exactly
	w0 := outputs[0]
	if w0.WindowStart != 0 {
		t.Errorf("window 0 WindowStart: want 0, got %v", w0.WindowStart)
	}
	if w0.SensorCount != 3 {
		t.Errorf("window 0 SensorCount: want 3, got %d", w0.SensorCount)
	}
	if w0.ReadingCount != 3 {
		t.Errorf("window 0 ReadingCount: want 3, got %d", w0.ReadingCount)
	}
	if w0.FusedValue != 361.0 {
		t.Errorf("window 0 FusedValue: want 361.0 exactly, got %.15g", w0.FusedValue)
	}

	// ---- window 1: [1s, 2s) ------------------------------------------------
	// readings: temp=21, humidity=52, pressure=1012
	// mean = (21 + 52 + 1012) / 3 = 1085/3 ≈ 361.6̄
	w1 := outputs[1]
	if w1.WindowStart != time.Second {
		t.Errorf("window 1 WindowStart: want 1s, got %v", w1.WindowStart)
	}
	if w1.SensorCount != 3 {
		t.Errorf("window 1 SensorCount: want 3, got %d", w1.SensorCount)
	}
	if w1.ReadingCount != 3 {
		t.Errorf("window 1 ReadingCount: want 3, got %d", w1.ReadingCount)
	}
	const want1 = 1085.0 / 3
	if math.Abs(w1.FusedValue-want1) > 1e-9 {
		t.Errorf("window 1 FusedValue: want %.15g, got %.15g (delta=%.3e)", want1, w1.FusedValue, math.Abs(w1.FusedValue-want1))
	}

	// ---- window 2: [2s, 3s) ------------------------------------------------
	// readings: temp=22 only
	// mean = 22.0 / 1 = 22.0 exactly
	w2 := outputs[2]
	if w2.WindowStart != 2*time.Second {
		t.Errorf("window 2 WindowStart: want 2s, got %v", w2.WindowStart)
	}
	if w2.SensorCount != 1 {
		t.Errorf("window 2 SensorCount: want 1, got %d", w2.SensorCount)
	}
	if w2.ReadingCount != 1 {
		t.Errorf("window 2 ReadingCount: want 1, got %d", w2.ReadingCount)
	}
	if w2.FusedValue != 22.0 {
		t.Errorf("window 2 FusedValue: want 22.0 exactly, got %.15g", w2.FusedValue)
	}
}

// TestWorkloadClass ensures the package-level constant is set to the distinct
// sensor-fusion class and is not accidentally equal to batch or inference.
func TestWorkloadClass(t *testing.T) {
	if WorkloadClass != "sensor-fusion" {
		t.Errorf("WorkloadClass: want %q, got %q", "sensor-fusion", WorkloadClass)
	}
	if WorkloadClass == "batch" || WorkloadClass == "inference" {
		t.Errorf("WorkloadClass must be distinct from batch/inference, got %q", WorkloadClass)
	}
}

// TestWindowedFusion_Mean directly exercises the FusionOperator to verify it
// computes a mean (not a sum, not a max, not the first element).
func TestWindowedFusion_Mean(t *testing.T) {
	op := WindowedFusion{}

	readings := []SensorReading{
		{SensorID: "a", Value: 10},
		{SensorID: "b", Value: 20},
		{SensorID: "c", Value: 30},
	}
	// mean = 60/3 = 20 — if Fuse returned sum it would be 60, max 30, first 10.
	got := op.Fuse(readings)
	if got != 20.0 {
		t.Errorf("Fuse mean of [10,20,30]: want 20.0, got %.15g", got)
	}

	// Single reading: mean == value.
	single := []SensorReading{{SensorID: "x", Value: 7.5}}
	if op.Fuse(single) != 7.5 {
		t.Errorf("Fuse single reading: want 7.5, got %.15g", op.Fuse(single))
	}

	// Empty slice: must return 0 without panicking.
	if op.Fuse(nil) != 0 {
		t.Errorf("Fuse empty: want 0, got %.15g", op.Fuse(nil))
	}
}

// TestFuseStreams_EmptyInputs verifies that degenerate inputs do not panic and
// return nil (or empty) rather than garbage.
func TestFuseStreams_EmptyInputs(t *testing.T) {
	op := WindowedFusion{Window: time.Second}

	if got := FuseStreams(nil, time.Second, op); got != nil {
		t.Errorf("nil streams: want nil, got %v", got)
	}
	if got := FuseStreams([]SensorStream{}, time.Second, op); got != nil {
		t.Errorf("empty streams slice: want nil, got %v", got)
	}
	emptyReadings := []SensorStream{{SensorID: "x", Readings: nil}}
	if got := FuseStreams(emptyReadings, time.Second, op); got != nil {
		t.Errorf("stream with no readings: want nil, got %v", got)
	}
}

// TestFuseStreams_WindowOrdering verifies that outputs are always sorted by
// WindowStart ascending, even when readings arrive out of chronological order
// in the input slices.
func TestFuseStreams_WindowOrdering(t *testing.T) {
	streams := []SensorStream{
		{
			SensorID: "s1",
			Readings: []SensorReading{
				{SensorID: "s1", Timestamp: 3 * time.Second, Value: 1},
				{SensorID: "s1", Timestamp: 1 * time.Second, Value: 2},
				{SensorID: "s1", Timestamp: 5 * time.Second, Value: 3},
			},
		},
	}
	op := WindowedFusion{Window: time.Second}
	outputs := FuseStreams(streams, time.Second, op)

	if len(outputs) != 3 {
		t.Fatalf("expected 3 windows, got %d", len(outputs))
	}
	for i := 1; i < len(outputs); i++ {
		if outputs[i].WindowStart <= outputs[i-1].WindowStart {
			t.Errorf("outputs not sorted: outputs[%d].WindowStart=%v <= outputs[%d].WindowStart=%v",
				i, outputs[i].WindowStart, i-1, outputs[i-1].WindowStart)
		}
	}
}

// TestFuseStreams_SingleStream verifies a single sensor stream with multiple
// windows to ensure SensorCount==1 throughout and ReadingCount is correct.
func TestFuseStreams_SingleStream(t *testing.T) {
	streams := []SensorStream{
		{
			SensorID: "temp",
			Readings: []SensorReading{
				{SensorID: "temp", Timestamp: 0, Value: 100},
				{SensorID: "temp", Timestamp: 500 * time.Millisecond, Value: 200},
				{SensorID: "temp", Timestamp: 1 * time.Second, Value: 300},
			},
		},
	}
	op := WindowedFusion{Window: time.Second}
	outputs := FuseStreams(streams, time.Second, op)

	if len(outputs) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(outputs))
	}
	// window 0: [100, 200] → mean = 150
	if outputs[0].SensorCount != 1 {
		t.Errorf("window 0 SensorCount: want 1, got %d", outputs[0].SensorCount)
	}
	if outputs[0].ReadingCount != 2 {
		t.Errorf("window 0 ReadingCount: want 2, got %d", outputs[0].ReadingCount)
	}
	if outputs[0].FusedValue != 150.0 {
		t.Errorf("window 0 FusedValue: want 150.0, got %.15g", outputs[0].FusedValue)
	}
	// window 1: [300] → mean = 300
	if outputs[1].SensorCount != 1 {
		t.Errorf("window 1 SensorCount: want 1, got %d", outputs[1].SensorCount)
	}
	if outputs[1].FusedValue != 300.0 {
		t.Errorf("window 1 FusedValue: want 300.0, got %.15g", outputs[1].FusedValue)
	}
}
