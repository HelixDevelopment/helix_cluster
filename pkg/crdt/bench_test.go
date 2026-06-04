package crdt

import (
	"strconv"
	"testing"
)

// BenchmarkGCounterMerge measures the grow-only counter merge hot path:
// Clone + per-replica max over both replica-slot maps. Populated with 64
// replica slots on each side so the inner max loop does real work.
func BenchmarkGCounterMerge(b *testing.B) {
	a := NewGCounter()
	c := NewGCounter()
	for i := 0; i < 64; i++ {
		id := ReplicaID(strconv.Itoa(i))
		a.Inc(id, uint64(i))
		c.Inc(id, uint64(i*2))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.Merge(c)
	}
}

// BenchmarkPNCounterMerge measures the increment/decrement counter merge, which
// merges two GCounter halves independently.
func BenchmarkPNCounterMerge(b *testing.B) {
	a := NewPNCounter()
	c := NewPNCounter()
	for i := 0; i < 64; i++ {
		id := ReplicaID(strconv.Itoa(i))
		a.Inc(id, uint64(i))
		a.Dec(id, uint64(i/2))
		c.Inc(id, uint64(i*2))
		c.Dec(id, uint64(i))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.Merge(c)
	}
}

// BenchmarkLWWRegisterMerge measures the last-writer-wins register merge: a
// clone plus the timestamp/replica-tiebreak comparison.
func BenchmarkLWWRegisterMerge(b *testing.B) {
	a := NewLWWRegister()
	a.Set("alpha", 100, "A")
	c := NewLWWRegister()
	c.Set("beta", 200, "B")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.Merge(c)
	}
}

// BenchmarkORSetMerge measures the observed-remove set merge: the heaviest CRDT
// merge here, unioning per-element add-tag and tombstone-tag maps and taking the
// per-replica clock max. Populated with 32 elements across 8 replicas plus some
// tombstones so the union loops do real work.
func BenchmarkORSetMerge(b *testing.B) {
	a := NewORSet()
	c := NewORSet()
	for i := 0; i < 32; i++ {
		el := "el-" + strconv.Itoa(i)
		for r := 0; r < 8; r++ {
			a.Add(el, ReplicaID(strconv.Itoa(r)))
			c.Add(el, ReplicaID(strconv.Itoa(r+8)))
		}
		if i%2 == 0 {
			a.Remove(el)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.Merge(c)
	}
}
