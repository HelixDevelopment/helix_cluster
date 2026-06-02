// Package e2eebench implements HXC-1556: a benchmark harness for the
// ML-KEM-768 / hybrid end-to-end-encryption handshake latency (target median
// < 1ms).
//
// It drives the SHIPPED github.com/HelixDevelopment/helix_cluster/pkg/hybridkex
// handshake — a fresh Initiator + Responder each iteration — and records the
// per-iteration wall-clock latency, returning the MEDIAN and the P95.
//
// Pure Go standard library only (bytes, sort, time + the shipped hybridkex).
package e2eebench

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/hybridkex"
)

// LatencyReport is the result of MeasureHandshakeLatency: the number of
// handshakes measured and the MEDIAN / P95 of their wall-clock durations in
// nanoseconds.
type LatencyReport struct {
	Iterations  int
	MedianNanos int64
	P95Nanos    int64
}

// ErrKeyDisagreement is returned by MeasureHandshakeLatency when an iteration's
// two derived session keys differ. This is the LOAD-BEARING guard: a handshake
// that does not agree on a key is not a real handshake, so it must surface as an
// error rather than being silently averaged into the latency report.
var ErrKeyDisagreement = errors.New("e2eebench: handshake derived two different session keys")

// runHandshake is the inner hook driving exactly one full hybridkex handshake.
// It returns the Initiator-side and Responder-side derived keys so the caller
// can confirm agreement. It is a package var so tests can wrap it to inject a
// deliberate key mismatch (the load-bearing-guard falsification path) without
// touching the real measurement path used in production.
var runHandshake = func() (initKey, respKey []byte, err error) {
	in, hello, err := hybridkex.NewInitiator()
	if err != nil {
		return nil, nil, fmt.Errorf("e2eebench: new initiator: %w", err)
	}
	_, rhello, respKey, err := hybridkex.Respond(hello)
	if err != nil {
		return nil, nil, fmt.Errorf("e2eebench: respond: %w", err)
	}
	initKey, err = in.Finish(rhello)
	if err != nil {
		return nil, nil, fmt.Errorf("e2eebench: finish: %w", err)
	}
	return initKey, respKey, nil
}

// MeasureHandshakeLatency runs a full hybridkex handshake `iterations` times.
// Each iteration builds a fresh Initiator + Responder, derives BOTH session
// keys, and confirms they are byte-equal; if any iteration's two keys differ it
// returns ErrKeyDisagreement (the load-bearing guard). The per-iteration
// wall-clock latency is recorded via time.Now/time.Since and the MEDIAN and P95
// of those durations are returned.
func MeasureHandshakeLatency(iterations int) (LatencyReport, error) {
	if iterations <= 0 {
		return LatencyReport{}, fmt.Errorf("e2eebench: iterations must be > 0, got %d", iterations)
	}

	durations := make([]time.Duration, 0, iterations)
	for i := 0; i < iterations; i++ {
		start := time.Now()
		initKey, respKey, err := runHandshake()
		elapsed := time.Since(start)
		if err != nil {
			return LatencyReport{}, err
		}
		// LOAD-BEARING: both sides must derive the identical session key. A real
		// hybrid handshake agrees; a corrupted one does not.
		if !bytes.Equal(initKey, respKey) {
			return LatencyReport{}, fmt.Errorf("%w: iteration %d", ErrKeyDisagreement, i)
		}
		durations = append(durations, elapsed)
	}

	sort.Slice(durations, func(a, b int) bool { return durations[a] < durations[b] })

	return LatencyReport{
		Iterations:  iterations,
		MedianNanos: percentileNanos(durations, 50),
		P95Nanos:    percentileNanos(durations, 95),
	}, nil
}

// percentileNanos returns the p-th percentile (0..100) of an already-sorted
// slice of durations, in nanoseconds, using nearest-rank.
func percentileNanos(sorted []time.Duration, p int) int64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	// nearest-rank: rank = ceil(p/100 * n), 1-based, clamped to [1, n].
	rank := (p*n + 99) / 100
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1].Nanoseconds()
}
