package edgefusion

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"
)

// identityFuse is a FusionOperator that does NOT collapse its input the way a
// mean does.  Instead it folds the bucket into a single deterministic scalar
// that is sensitive to BOTH the multiset of values AND, separately captured via
// a side channel, lets a test recover every reading.  We use it to prove
// fusion completeness: that the readings handed to Fuse for each window are
// EXACTLY the input readings of that window — no loss, no duplication.
//
// It records, per WindowStart-equivalent call, the exact slice it received.
type recordingFuse struct {
	calls [][]SensorReading
}

func (r *recordingFuse) Fuse(window []SensorReading) float64 {
	// Copy defensively so later mutation (there is none, but be safe) cannot
	// corrupt the recorded evidence.
	cp := make([]SensorReading, len(window))
	copy(cp, window)
	r.calls = append(r.calls, cp)
	return float64(len(window))
}

// allReadings flattens streams in input order (no sorting) — the ground-truth
// multiset of what MUST appear in the fused output, exactly once each.
func allReadings(streams []SensorStream) []SensorReading {
	var out []SensorReading
	for _, s := range streams {
		out = append(out, s.Readings...)
	}
	return out
}

// readingKey is a comparable identity for a reading, used to build multisets.
type readingKey struct {
	SensorID  string
	Timestamp time.Duration
	Value     float64
}

func key(r SensorReading) readingKey {
	return readingKey{r.SensorID, r.Timestamp, r.Value}
}

func multiset(rs []SensorReading) map[readingKey]int {
	m := make(map[readingKey]int)
	for _, r := range rs {
		m[key(r)]++
	}
	return m
}

// -----------------------------------------------------------------------------
// CENTERPIECE 1: FUSION COMPLETENESS — no loss, no duplication.
//
// Every input reading must be delivered to op.Fuse EXACTLY once across all
// windows, and SUM(ReadingCount) across outputs must equal the input count.
// We use distinct (SensorID,Timestamp,Value) triples so any drop or dup is
// detectable as a multiset mismatch.
// -----------------------------------------------------------------------------
func TestAdversarial_FusionCompleteness_NoLossNoDup(t *testing.T) {
	streams := []SensorStream{
		{SensorID: "a", Readings: []SensorReading{
			{SensorID: "a", Timestamp: 0 * time.Second, Value: 1},
			{SensorID: "a", Timestamp: 1 * time.Second, Value: 2},
			{SensorID: "a", Timestamp: 2500 * time.Millisecond, Value: 3},
		}},
		{SensorID: "b", Readings: []SensorReading{
			{SensorID: "b", Timestamp: 200 * time.Millisecond, Value: 4},
			{SensorID: "b", Timestamp: 1700 * time.Millisecond, Value: 5},
		}},
		{SensorID: "c", Readings: []SensorReading{
			{SensorID: "c", Timestamp: 900 * time.Millisecond, Value: 6},
			{SensorID: "c", Timestamp: 2999 * time.Millisecond, Value: 7},
		}},
	}

	rf := &recordingFuse{}
	outputs := FuseStreams(streams, time.Second, rf)

	// Reconstruct the multiset of readings actually handed to Fuse.
	var delivered []SensorReading
	for _, c := range rf.calls {
		delivered = append(delivered, c...)
	}

	want := multiset(allReadings(streams))
	got := multiset(delivered)
	if !reflect.DeepEqual(want, got) {
		t.Errorf("fusion lost/duplicated readings.\n want multiset=%v\n  got multiset=%v", want, got)
	}

	// SUM(ReadingCount) must equal total input readings — independent check.
	totalIn := len(allReadings(streams))
	sumReadingCount := 0
	for _, o := range outputs {
		sumReadingCount += o.ReadingCount
	}
	if sumReadingCount != totalIn {
		t.Errorf("ReadingCount conservation violated: sum=%d, input=%d", sumReadingCount, totalIn)
	}

	// Each Fuse call's length must equal the corresponding output ReadingCount,
	// and there must be exactly one call per output window.
	if len(rf.calls) != len(outputs) {
		t.Fatalf("Fuse called %d times but produced %d outputs", len(rf.calls), len(outputs))
	}
	for i := range outputs {
		if len(rf.calls[i]) != outputs[i].ReadingCount {
			t.Errorf("window %d: Fuse got %d readings but ReadingCount=%d",
				i, len(rf.calls[i]), outputs[i].ReadingCount)
		}
	}
}

// -----------------------------------------------------------------------------
// CENTERPIECE 2: WINDOW ORDER + CORRECT BUCKETING.
//
// Outputs must be strictly ascending by WindowStart, and every reading must
// land in window floor(t/window)*window.  Distinct values make a misrouted
// reading detectable.
// -----------------------------------------------------------------------------
func TestAdversarial_BucketingAndOrder(t *testing.T) {
	// interleaved timestamps spanning windows 0,1,3 (window 2 empty -> skipped)
	streams := []SensorStream{
		{SensorID: "x", Readings: []SensorReading{
			{SensorID: "x", Timestamp: 3200 * time.Millisecond, Value: 10}, // win 3
			{SensorID: "x", Timestamp: 100 * time.Millisecond, Value: 11},  // win 0
		}},
		{SensorID: "y", Readings: []SensorReading{
			{SensorID: "y", Timestamp: 1500 * time.Millisecond, Value: 12}, // win 1
			{SensorID: "y", Timestamp: 3900 * time.Millisecond, Value: 13}, // win 3
		}},
	}
	op := WindowedFusion{Window: time.Second}
	outputs := FuseStreams(streams, time.Second, op)

	wantStarts := []time.Duration{0, 1 * time.Second, 3 * time.Second}
	if len(outputs) != len(wantStarts) {
		t.Fatalf("expected %d windows, got %d (%v)", len(wantStarts), len(outputs), outputs)
	}
	for i, o := range outputs {
		if o.WindowStart != wantStarts[i] {
			t.Errorf("output %d WindowStart: want %v, got %v", i, wantStarts[i], o.WindowStart)
		}
		if i > 0 && outputs[i].WindowStart <= outputs[i-1].WindowStart {
			t.Errorf("outputs not strictly ascending at %d: %v <= %v", i,
				outputs[i].WindowStart, outputs[i-1].WindowStart)
		}
	}

	// window 3 must contain exactly the two readings 10 and 13 (mean 11.5),
	// proving a window with readings from BOTH streams buckets correctly.
	w3 := outputs[2]
	if w3.ReadingCount != 2 || w3.SensorCount != 2 {
		t.Errorf("window 3: want ReadingCount=2 SensorCount=2, got RC=%d SC=%d", w3.ReadingCount, w3.SensorCount)
	}
	if w3.FusedValue != 11.5 {
		t.Errorf("window 3 FusedValue: want 11.5, got %.15g", w3.FusedValue)
	}
}

// -----------------------------------------------------------------------------
// CENTERPIECE 3: DETERMINISM / ORDER-INDEPENDENCE (the antientropy tie class).
//
// The fused output MUST be identical regardless of the order streams are fed,
// regardless of the order of readings within a stream, AND even when multiple
// readings share the SAME timestamp in the SAME window across different sensors
// (the tie case).  A non-deterministic or order-dependent result is the bug
// class we hunt.
// -----------------------------------------------------------------------------
func TestAdversarial_OrderIndependentDeterminism(t *testing.T) {
	base := []SensorStream{
		{SensorID: "a", Readings: []SensorReading{
			{SensorID: "a", Timestamp: 500 * time.Millisecond, Value: 1},
			{SensorID: "a", Timestamp: 1500 * time.Millisecond, Value: 2},
		}},
		{SensorID: "b", Readings: []SensorReading{
			// SAME timestamp as a's first reading -> tie in window 0
			{SensorID: "b", Timestamp: 500 * time.Millisecond, Value: 3},
			{SensorID: "b", Timestamp: 1500 * time.Millisecond, Value: 4},
		}},
		{SensorID: "c", Readings: []SensorReading{
			// also same timestamp -> three-way tie in window 0
			{SensorID: "c", Timestamp: 500 * time.Millisecond, Value: 5},
		}},
	}
	op := WindowedFusion{Window: time.Second}
	want := FuseStreams(base, time.Second, op)

	// Permute stream order and reading order many ways; output must be identical.
	rng := rand.New(rand.NewSource(42))
	for iter := 0; iter < 200; iter++ {
		perm := clonePermuted(base, rng)
		got := FuseStreams(perm, time.Second, op)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("non-deterministic / order-dependent fusion at iter %d\n stream order/readings permuted\n want=%+v\n  got=%+v",
				iter, want, got)
		}
	}

	// Pin the tie window's exact value so the determinism above is meaningful:
	// window 0 = {1,3,5} mean = 3, three sensors.
	if len(want) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(want))
	}
	if want[0].WindowStart != 0 || want[0].ReadingCount != 3 || want[0].SensorCount != 3 || want[0].FusedValue != 3.0 {
		t.Errorf("tie window 0: want start=0 RC=3 SC=3 FV=3.0, got %+v", want[0])
	}
}

// clonePermuted deep-copies streams, shuffles their order, and shuffles each
// stream's readings, so the result is a permutation that must fuse identically.
func clonePermuted(streams []SensorStream, rng *rand.Rand) []SensorStream {
	out := make([]SensorStream, len(streams))
	for i, s := range streams {
		rs := make([]SensorReading, len(s.Readings))
		copy(rs, s.Readings)
		rng.Shuffle(len(rs), func(a, b int) { rs[a], rs[b] = rs[b], rs[a] })
		out[i] = SensorStream{SensorID: s.SensorID, Readings: rs}
	}
	rng.Shuffle(len(out), func(a, b int) { out[a], out[b] = out[b], out[a] })
	return out
}

// -----------------------------------------------------------------------------
// PROPERTY: randomized completeness + conservation over many shapes.
// For random streams, SUM(ReadingCount) == input count, distinct windowStarts
// are strictly ascending, and SensorCount<=ReadingCount in every window.
// -----------------------------------------------------------------------------
func TestAdversarial_Property_Conservation(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	op := WindowedFusion{Window: time.Second}

	for trial := 0; trial < 300; trial++ {
		nStreams := rng.Intn(5) + 1
		streams := make([]SensorStream, nStreams)
		totalIn := 0
		for s := 0; s < nStreams; s++ {
			id := fmt.Sprintf("s%d", s)
			n := rng.Intn(6)
			rs := make([]SensorReading, n)
			for k := 0; k < n; k++ {
				// non-negative timestamps only (contract: offset from start)
				ts := time.Duration(rng.Intn(5000)) * time.Millisecond
				rs[k] = SensorReading{SensorID: id, Timestamp: ts, Value: rng.Float64() * 100}
			}
			streams[s] = SensorStream{SensorID: id, Readings: rs}
			totalIn += n
		}

		outputs := FuseStreams(streams, time.Second, op)

		sum := 0
		for i, o := range outputs {
			sum += o.ReadingCount
			if o.SensorCount > o.ReadingCount {
				t.Fatalf("trial %d: SensorCount %d > ReadingCount %d", trial, o.SensorCount, o.ReadingCount)
			}
			if o.SensorCount < 1 || o.ReadingCount < 1 {
				t.Fatalf("trial %d: empty window emitted: %+v", trial, o)
			}
			if i > 0 && outputs[i].WindowStart <= outputs[i-1].WindowStart {
				t.Fatalf("trial %d: windows not strictly ascending", trial)
			}
		}
		if sum != totalIn {
			t.Fatalf("trial %d: ReadingCount conservation: sum=%d input=%d", trial, sum, totalIn)
		}
	}
}

// -----------------------------------------------------------------------------
// WINDOW BOUNDARY: a reading exactly at t==window belongs to the UPPER window.
// floor(window/window)*window == window, never the [0,window) window.
// -----------------------------------------------------------------------------
func TestAdversarial_WindowBoundaryExact(t *testing.T) {
	streams := []SensorStream{
		{SensorID: "a", Readings: []SensorReading{
			{SensorID: "a", Timestamp: 0, Value: 1},                               // win 0
			{SensorID: "a", Timestamp: 1 * time.Second, Value: 2},                 // win 1 (exact edge)
			{SensorID: "a", Timestamp: 2*time.Second - time.Nanosecond, Value: 3}, // win 1 (last ns)
			{SensorID: "a", Timestamp: 2 * time.Second, Value: 4},                 // win 2 (exact edge)
		}},
	}
	op := WindowedFusion{Window: time.Second}
	outputs := FuseStreams(streams, time.Second, op)

	if len(outputs) != 3 {
		t.Fatalf("expected 3 windows, got %d (%+v)", len(outputs), outputs)
	}
	// win 0: {1} ; win 1: {2,3} ; win 2: {4}
	if outputs[0].ReadingCount != 1 || outputs[0].FusedValue != 1 {
		t.Errorf("win0: want RC=1 FV=1, got %+v", outputs[0])
	}
	if outputs[1].WindowStart != time.Second || outputs[1].ReadingCount != 2 || outputs[1].FusedValue != 2.5 {
		t.Errorf("win1 (edge): want start=1s RC=2 FV=2.5, got %+v", outputs[1])
	}
	if outputs[2].WindowStart != 2*time.Second || outputs[2].ReadingCount != 1 || outputs[2].FusedValue != 4 {
		t.Errorf("win2 (edge): want start=2s RC=1 FV=4, got %+v", outputs[2])
	}
}

// -----------------------------------------------------------------------------
// PREFIX: one stream is a strict prefix (in time) of another — the shorter
// stream's readings must still all appear, none swallowed by the longer one.
// -----------------------------------------------------------------------------
func TestAdversarial_PrefixStream(t *testing.T) {
	long := SensorStream{SensorID: "long", Readings: []SensorReading{
		{SensorID: "long", Timestamp: 0, Value: 1},
		{SensorID: "long", Timestamp: 1 * time.Second, Value: 2},
		{SensorID: "long", Timestamp: 2 * time.Second, Value: 3},
	}}
	short := SensorStream{SensorID: "short", Readings: []SensorReading{
		{SensorID: "short", Timestamp: 0, Value: 100}, // overlaps long's first window only
	}}
	op := WindowedFusion{Window: time.Second}
	outputs := FuseStreams([]SensorStream{long, short}, time.Second, op)

	if len(outputs) != 3 {
		t.Fatalf("expected 3 windows, got %d", len(outputs))
	}
	// win0 has both sensors: {1,100} mean 50.5, SensorCount 2
	if outputs[0].SensorCount != 2 || outputs[0].ReadingCount != 2 || outputs[0].FusedValue != 50.5 {
		t.Errorf("win0: want SC=2 RC=2 FV=50.5, got %+v", outputs[0])
	}
	// win1, win2 are long-only
	if outputs[1].SensorCount != 1 || outputs[2].SensorCount != 1 {
		t.Errorf("win1/win2 SensorCount: want 1/1, got %d/%d", outputs[1].SensorCount, outputs[2].SensorCount)
	}
}

// -----------------------------------------------------------------------------
// EDGE: degenerate inputs must not panic and must return nil.
// -----------------------------------------------------------------------------
func TestAdversarial_DegenerateNoPanic(t *testing.T) {
	op := WindowedFusion{Window: time.Second}

	cases := []struct {
		name    string
		streams []SensorStream
		window  time.Duration
	}{
		{"nil streams", nil, time.Second},
		{"empty streams", []SensorStream{}, time.Second},
		{"stream nil readings", []SensorStream{{SensorID: "x"}}, time.Second},
		{"stream empty readings", []SensorStream{{SensorID: "x", Readings: []SensorReading{}}}, time.Second},
		{"zero window", []SensorStream{{SensorID: "x", Readings: []SensorReading{{SensorID: "x", Value: 1}}}}, 0},
		{"negative window", []SensorStream{{SensorID: "x", Readings: []SensorReading{{SensorID: "x", Value: 1}}}}, -time.Second},
		{"mixed empty+nonempty", []SensorStream{
			{SensorID: "empty"},
			{SensorID: "full", Readings: []SensorReading{{SensorID: "full", Timestamp: 0, Value: 9}}},
		}, time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic on %q: %v", tc.name, r)
				}
			}()
			out := FuseStreams(tc.streams, tc.window, op)
			switch tc.name {
			case "mixed empty+nonempty":
				if len(out) != 1 || out[0].ReadingCount != 1 || out[0].SensorCount != 1 || out[0].FusedValue != 9 {
					t.Errorf("mixed: want one window RC=1 SC=1 FV=9, got %+v", out)
				}
			case "zero window", "negative window":
				if out != nil {
					t.Errorf("%s: want nil, got %+v", tc.name, out)
				}
			default:
				if out != nil {
					t.Errorf("%s: want nil, got %+v", tc.name, out)
				}
			}
		})
	}
}

// -----------------------------------------------------------------------------
// NON-MUTATION: FuseStreams must not mutate or reorder the caller's input
// slices (it sorts an internal copy).  A defensive contract pin.
// -----------------------------------------------------------------------------
func TestAdversarial_InputNotMutated(t *testing.T) {
	streams := []SensorStream{
		{SensorID: "a", Readings: []SensorReading{
			{SensorID: "a", Timestamp: 3 * time.Second, Value: 1},
			{SensorID: "a", Timestamp: 1 * time.Second, Value: 2},
			{SensorID: "a", Timestamp: 2 * time.Second, Value: 3},
		}},
	}
	// snapshot
	snap := make([]SensorReading, len(streams[0].Readings))
	copy(snap, streams[0].Readings)

	op := WindowedFusion{Window: time.Second}
	_ = FuseStreams(streams, time.Second, op)

	if !reflect.DeepEqual(snap, streams[0].Readings) {
		t.Errorf("FuseStreams mutated caller input.\n before=%+v\n  after=%+v", snap, streams[0].Readings)
	}
}

// -----------------------------------------------------------------------------
// CHARACTERIZATION (documented boundary, NOT a contract claim): negative
// timestamps.  The type doc states Timestamp is an "offset from start" (>=0),
// and FuseStreams documents windows "starting at time 0", so negative
// timestamps are OUT OF CONTRACT.  Go integer division truncates toward zero,
// so key=(t/window)*window is NOT mathematical floor for t<0.  This test pins
// the ACTUAL truncation behavior so any future change is visible, and documents
// the latent mis-bucketing should negative timestamps ever be admitted.
// -----------------------------------------------------------------------------
func TestAdversarial_NegativeTimestamp_TruncationCharacterization(t *testing.T) {
	streams := []SensorStream{
		{SensorID: "a", Readings: []SensorReading{
			{SensorID: "a", Timestamp: -500 * time.Millisecond, Value: 1},
			{SensorID: "a", Timestamp: 500 * time.Millisecond, Value: 2},
		}},
	}
	op := WindowedFusion{Window: time.Second}
	outputs := FuseStreams(streams, time.Second, op)

	// Truncation-toward-zero maps BOTH -500ms and +500ms to key 0, so they
	// collide in window 0.  Mathematical floor would put -500ms in window -1s.
	// We pin the current (truncation) behavior. If this ever changes to floor,
	// this assertion fails loudly and forces a deliberate decision.
	keys := make([]time.Duration, len(outputs))
	for i, o := range outputs {
		keys[i] = o.WindowStart
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	if len(outputs) != 1 || outputs[0].WindowStart != 0 || outputs[0].ReadingCount != 2 {
		t.Logf("NOTE: negative-timestamp bucketing is out-of-contract; behavior characterized, not guaranteed")
		t.Errorf("truncation characterization changed: want single window start=0 RC=2 (collision), got %+v", outputs)
	}
}
