package e2eebench

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// This file is an ADVERSARIAL probe of pkg/e2eebench. e2eebench is a
// BENCHMARK/STATS harness that drives the SHIPPED pkg/hybridkex handshake and
// reports MEDIAN / P95 latency, gated by a load-bearing key-agreement check.
//
// The real attack surface that lives IN THIS PACKAGE (not in hybridkex) is:
//   (1) the nearest-rank percentile math (percentileNanos),
//   (2) the byte-equality key-agreement guard,
//   (3) the report invariants computed from real measurements,
//   (4) concurrency-safety of MeasureHandshakeLatency under -race.
// The crypto correctness itself is hybridkex's contract and is probed there;
// here we only assert that e2eebench faithfully MEASURES and GUARDS it.
//
// Every assertion is SINK-SIDE: it checks the computed statistic / the
// guard's accept|reject decision, never merely "did not panic".

// --- (1) PERCENTILE MATH: nearest-rank exact values at small-n boundaries ----
//
// The existing TestPercentileNanos pins n=10 and n=1. Small n (2,3) is where an
// off-by-one in rank=(p*n+99)/100 would bite. These expected values are
// hand-derived from the documented nearest-rank definition: rank = ceil(p/100*n),
// 1-based, then sorted[rank-1].
func TestAdversarial_PercentileNearestRank_SmallN(t *testing.T) {
	mk := func(ns ...int) []time.Duration {
		out := make([]time.Duration, len(ns))
		for i, v := range ns {
			out[i] = time.Duration(v) * time.Nanosecond
		}
		return out
	}

	type tc struct {
		name   string
		sorted []time.Duration
		p      int
		want   int64
	}
	cases := []tc{
		// n=2: values {10,20}.
		// p50: ceil(0.50*2)=1 -> sorted[0]=10.
		{"n2_p50", mk(10, 20), 50, 10},
		// p95: ceil(0.95*2)=2 -> sorted[1]=20.
		{"n2_p95", mk(10, 20), 95, 20},
		// p100: ceil(1.00*2)=2 -> sorted[1]=20.
		{"n2_p100", mk(10, 20), 100, 20},
		// p0: ceil(0)=0 -> clamped to rank 1 -> sorted[0]=10.
		{"n2_p0", mk(10, 20), 0, 10},

		// n=3: values {10,20,30}.
		// p50: ceil(0.50*3)=ceil(1.5)=2 -> sorted[1]=20.
		{"n3_p50", mk(10, 20, 30), 50, 20},
		// p95: ceil(0.95*3)=ceil(2.85)=3 -> sorted[2]=30.
		{"n3_p95", mk(10, 20, 30), 95, 30},
		// p33: ceil(0.33*3)=ceil(0.99)=1 -> sorted[0]=10.
		{"n3_p33", mk(10, 20, 30), 33, 10},
		// p34: ceil(0.34*3)=ceil(1.02)=2 -> sorted[1]=20.
		{"n3_p34", mk(10, 20, 30), 34, 20},
	}
	for _, c := range cases {
		got := percentileNanos(c.sorted, c.p)
		if got != c.want {
			t.Fatalf("percentileNanos(%v, %d) = %d, want %d (hand-derived nearest-rank)",
				c.sorted, c.p, got, c.want)
		}
		t.Logf("OK %s: percentileNanos(p=%d)=%dns", c.name, c.p, got)
	}
}

// --- (1b) PERCENTILE MONOTONICITY: p95 must never fall below p50, for ALL n ---
//
// The report promises P95Nanos >= MedianNanos (the existing KPI test asserts it
// for one real run; the test (line 37) even calls a violation "percentile
// ordering is broken"). Prove it holds structurally: for a non-decreasing sorted
// slice, percentileNanos must be monotonic non-decreasing in p. A rank off-by-one
// at any n is the bug that would silently invert median/p95.
func TestAdversarial_PercentileMonotonicInP(t *testing.T) {
	for n := 1; n <= 64; n++ {
		sorted := make([]time.Duration, n)
		for i := 0; i < n; i++ {
			sorted[i] = time.Duration(i) * time.Nanosecond // strictly non-decreasing
		}
		prev := int64(-1)
		for p := 0; p <= 100; p++ {
			cur := percentileNanos(sorted, p)
			if cur < prev {
				t.Fatalf("non-monotonic percentile: n=%d p=%d gave %d but p=%d gave %d (median<p95 invariant can break)",
					n, p, cur, p-1, prev)
			}
			prev = cur
		}
		// Specifically pin the median<=p95 relationship the report relies on.
		if med, p95 := percentileNanos(sorted, 50), percentileNanos(sorted, 95); p95 < med {
			t.Fatalf("n=%d: p95(%d) < median(%d) — report invariant violated at the math level", n, p95, med)
		}
	}
	t.Logf("OK: nearest-rank percentile monotonic non-decreasing in p for n=1..64; p95>=median holds at every n")
}

// percentileNanos on an empty slice returns 0 (documented). Pin it — a stats
// harness must not panic or produce garbage on a zero-sample input.
func TestAdversarial_PercentileEmptyIsZero(t *testing.T) {
	if got := percentileNanos(nil, 50); got != 0 {
		t.Fatalf("percentileNanos(nil,50)=%d, want 0", got)
	}
	if got := percentileNanos([]time.Duration{}, 95); got != 0 {
		t.Fatalf("percentileNanos(empty,95)=%d, want 0", got)
	}
	t.Logf("OK: empty/nil sample -> percentile 0, no panic")
}

// --- (2) KEY-AGREEMENT GUARD: real discrimination, SINK-SIDE accept|reject -----
//
// The guard is bytes.Equal(initKey, respKey). Drive MeasureHandshakeLatency with
// the injectable runHandshake hook and assert the ACTUAL accept/reject decision
// across the equality boundary: equal-but-distinct slices accept; any single-bit
// difference, length difference, or content difference REJECTS with
// ErrKeyDisagreement. This is the load-bearing CLAUDE-1 surface: a key
// disagreement that slips through would average a BROKEN handshake's latency into
// a green KPI report.
func TestAdversarial_KeyAgreementGuard_Boundary(t *testing.T) {
	orig := runHandshake
	t.Cleanup(func() { runHandshake = orig })

	type tc struct {
		name       string
		mk         func() (a, b []byte)
		wantReject bool
	}
	base := func() []byte { return bytes.Repeat([]byte{0x5A}, 32) }
	cases := []tc{
		{"equal_distinct_slices", func() ([]byte, []byte) {
			return append([]byte(nil), base()...), append([]byte(nil), base()...)
		}, false},
		{"one_bit_flip", func() ([]byte, []byte) {
			a, b := base(), base()
			b[31] ^= 0x01
			return a, b
		}, true},
		{"first_byte_diff", func() ([]byte, []byte) {
			a, b := base(), base()
			b[0] = 0xFF
			return a, b
		}, true},
		{"length_mismatch_prefix", func() ([]byte, []byte) {
			a := base()
			return a, a[:31] // responder one byte short
		}, true},
		{"length_mismatch_longer", func() ([]byte, []byte) {
			a := base()
			return a, append(append([]byte(nil), a...), 0x00)
		}, true},
	}

	for _, c := range cases {
		a, b := c.mk()
		runHandshake = func() (initKey, respKey []byte, err error) {
			return append([]byte(nil), a...), append([]byte(nil), b...), nil
		}
		_, err := MeasureHandshakeLatency(8)
		gotReject := err != nil
		if gotReject != c.wantReject {
			t.Fatalf("%s: guard reject=%v (err=%v), want reject=%v", c.name, gotReject, err, c.wantReject)
		}
		if c.wantReject && !errors.Is(err, ErrKeyDisagreement) {
			t.Fatalf("%s: rejected but not via ErrKeyDisagreement: %v", c.name, err)
		}
		t.Logf("OK %s: reject=%v err=%v", c.name, gotReject, err)
	}
}

// CONTRACT-PIN / LATENT RISK: bytes.Equal treats nil and empty as equal, so a
// degenerate handshake that derived TWO EMPTY keys would be ACCEPTED as
// "agreement". The shipped hybridkex returns 32-byte keys, so this is not
// reachable in production — but it is a genuine PASS-bluff shape (empty==empty)
// that the guard cannot distinguish from real agreement. This test PINS the
// current behavior (accept) so any future change is visible, and documents the
// risk rather than asserting a non-existent rejection.
func TestAdversarial_KeyAgreementGuard_EmptyKeysDegenerateAccept(t *testing.T) {
	orig := runHandshake
	t.Cleanup(func() { runHandshake = orig })

	runHandshake = func() (initKey, respKey []byte, err error) {
		// Two empty keys: bytes.Equal(nil, []byte{}) == true.
		return []byte{}, nil, nil
	}
	rep, err := MeasureHandshakeLatency(4)
	if err != nil {
		t.Fatalf("PINNED CONTRACT CHANGED: empty/nil keys now rejected (%v). "+
			"Previously accepted as degenerate agreement; update this pin and the LATENT-RISK note.", err)
	}
	if rep.Iterations != 4 {
		t.Fatalf("Iterations=%d, want 4", rep.Iterations)
	}
	t.Logf("PINNED (latent risk): empty/nil keys are ACCEPTED as agreement (bytes.Equal nil==empty). "+
		"Not reachable via real hybridkex (32-byte keys); noted, not a prod bug.")
}

// --- (3) REPORT INVARIANTS against the REAL shipped hybridkex ----------------
//
// No hook injection: drive the actual handshake. Assert the report is a real
// measurement (median>0), preserves Iterations, and that the percentile ordering
// the harness promises (P95>=Median) holds on live data.
func TestAdversarial_RealReportInvariants(t *testing.T) {
	const iters = 64
	rep, err := MeasureHandshakeLatency(iters)
	if err != nil {
		t.Fatalf("real hybridkex handshake errored (key disagreement?): %v", err)
	}
	if rep.Iterations != iters {
		t.Fatalf("Iterations=%d, want %d", rep.Iterations, iters)
	}
	if rep.MedianNanos <= 0 {
		t.Fatalf("MedianNanos=%d, want >0 (real wall-clock measurement)", rep.MedianNanos)
	}
	if rep.P95Nanos < rep.MedianNanos {
		t.Fatalf("P95Nanos(%d) < MedianNanos(%d) on live data — ordering broken", rep.P95Nanos, rep.MedianNanos)
	}
	t.Logf("OK: real handshake report median=%dns p95=%dns over %d iters; invariants hold",
		rep.MedianNanos, rep.P95Nanos, iters)
}

// --- (4) CONCURRENCY: MeasureHandshakeLatency under -race --------------------
//
// MeasureHandshakeLatency is documented as driving a FRESH Initiator+Responder
// each iteration and uses only local slices — so concurrent independent calls
// must be data-race-free. Run many in parallel; `go test -race` is the sink.
func TestAdversarial_ConcurrentMeasureRaceFree(t *testing.T) {
	const goroutines = 16
	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			rep, err := MeasureHandshakeLatency(10)
			if err != nil {
				errCh <- fmt.Errorf("goroutine %d: %w", id, err)
				return
			}
			if rep.MedianNanos <= 0 || rep.Iterations != 10 {
				errCh <- fmt.Errorf("goroutine %d: bad report %+v", id, rep)
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent measure failed: %v", err)
	}
	t.Logf("OK: %d concurrent MeasureHandshakeLatency calls race-free with valid reports", goroutines)
}
