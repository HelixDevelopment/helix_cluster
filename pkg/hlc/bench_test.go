package hlc

import (
	"testing"
	"time"
)

// BenchmarkUpdate measures the HLC merge/update hot path: Clock.Update folds a
// remote timestamp into local state under the mutex, computing the max physical
// component and the dominating logical counter. A monotonically advancing stub
// clock keeps the physical component moving so the benchmark exercises the
// common "physical advances" branch rather than a degenerate frozen-clock loop.
func BenchmarkUpdate(b *testing.B) {
	var ns int64
	c := New(func() time.Time {
		ns += 100 // advance 100ns per read so physical time progresses
		return time.Unix(0, ns)
	})
	remote := Timestamp{Physical: 1, Logical: 0}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		remote = c.Update(remote)
	}
	_ = remote
}

// BenchmarkUpdateContended measures Update under the frozen-physical-clock
// branch, where the logical counter must be bumped every call — the worst case
// for the tie-break arithmetic and the dominating-counter selection.
func BenchmarkUpdateContended(b *testing.B) {
	c := New(func() time.Time { return time.Unix(0, 1000) }) // frozen
	remote := Timestamp{Physical: 1000, Logical: 0}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		remote = c.Update(remote)
	}
	_ = remote
}

// BenchmarkNow measures the local-event timestamp hot path for comparison.
func BenchmarkNow(b *testing.B) {
	var ns int64
	c := New(func() time.Time {
		ns += 100
		return time.Unix(0, ns)
	})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Now()
	}
}
