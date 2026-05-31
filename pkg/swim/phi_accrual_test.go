package swim

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// PhiAccrualDetector — phi-accrual failure detector (additive to suspicion)
// ============================================================================

// setLastSeen is a same-package test helper that realigns a peer's last-heartbeat
// timestamp WITHOUT adding an inter-arrival sample, so phi for two peers can be
// compared at an identical elapsed silence while their learned windows differ.
func (d *PhiAccrualDetector) setLastSeen(peer string, t time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if st, ok := d.peers[peer]; ok {
		st.lastSeen = t
	}
}

// newPhiTestDetector builds a detector driven by a ManualClock so phi can be
// evaluated at exact, deterministic elapsed-silence points.
func newPhiTestDetector(t *testing.T) (*PhiAccrualDetector, *ManualClock) {
	t.Helper()
	clk := NewManualClock(time.Unix(0, 0))
	d := NewPhiAccrualDetector(PhiAccrualConfig{
		Clock:      clk,
		WindowSize: 100,
		MinSamples: 3,
		MinStdDev:  10 * time.Millisecond,
	})
	return d, clk
}

// beat feeds n heartbeats spaced exactly interval apart (driving the clock).
func beat(d *PhiAccrualDetector, clk *ManualClock, peer string, interval time.Duration, n int) {
	for i := 0; i < n; i++ {
		d.Heartbeat(peer)
		clk.Advance(interval)
	}
	// Final heartbeat at the last position so lastSeen == clock.Now().
	d.Heartbeat(peer)
}

// beatJittered feeds n heartbeats whose intervals oscillate deterministically
// around interval by ±spread. This yields a realistic, non-zero variance (so the
// normal CDF is a smooth curve and phi rises gradually rather than as a near-step
// off the tiny MinStdDev floor) while keeping the mean equal to interval.
func beatJittered(d *PhiAccrualDetector, clk *ManualClock, peer string, interval, spread time.Duration, n int) {
	for i := 0; i < n; i++ {
		d.Heartbeat(peer)
		delta := spread
		if i%2 == 1 {
			delta = -spread
		}
		clk.Advance(interval + delta)
	}
	d.Heartbeat(peer)
}

// TestPhi_RegularHeartbeatsStayLow proves that under healthy, regular
// heartbeats phi remains well below a normal threshold while the peer is only a
// fraction of an interval into silence.
//
// Mutation that kills it: in phi(), compute pLater without the elapsed term
// (e.g. y := -mean/stdDev, ignoring elapsed) so phi no longer tracks a small
// elapsed — it would report a constant non-trivial phi and could exceed 8.
func TestPhi_RegularHeartbeatsStayLow(t *testing.T) {
	d, clk := newPhiTestDetector(t)
	beat(d, clk, "p", 1*time.Second, 20)

	// Only 200ms into silence — well within the regular 1s cadence.
	clk.Advance(200 * time.Millisecond)

	phi := d.Phi("p")
	assert.Less(t, phi, 8.0, "phi must stay low under a healthy heartbeat cadence")
	assert.False(t, d.Suspect("p", 8.0), "peer must not be suspected mid-interval")
}

// TestPhi_RisesMonotonicallyWithSilence is the central anti-bluff law: phi is a
// strictly rising function of elapsed silence, and Suspect flips true only after
// the gap grows large relative to the learned interval.
//
// Mutation that kills it: make Phi ignore elapsed-since-last-heartbeat and
// return a constant (e.g. `return d.someConst`). The strict-increase chain and
// the "stays low early, crosses later" assertions both fail.
func TestPhi_RisesMonotonicallyWithSilence(t *testing.T) {
	d, clk := newPhiTestDetector(t)
	// mean 1s, ±200ms jitter => stdDev ~200ms, so phi rises smoothly (not as a
	// near-step off the MinStdDev floor) as silence accumulates.
	beatJittered(d, clk, "p", 1*time.Second, 200*time.Millisecond, 40)

	// Sample phi at increasing silence past the learned mean (~1s), where the
	// upper tail is strictly decreasing so phi strictly increases each step.
	var prev float64 = -1
	var phis []float64
	steps := []time.Duration{
		1100 * time.Millisecond, // ~1 interval of silence: low phi
		300 * time.Millisecond,
		300 * time.Millisecond,
		300 * time.Millisecond,
		400 * time.Millisecond,
		500 * time.Millisecond,
	}
	var elapsed time.Duration
	for _, step := range steps {
		clk.Advance(step)
		elapsed += step
		cur := d.Phi("p")
		require.Greater(t, cur, prev, "phi must rise as silence accumulates (elapsed=%s)", elapsed)
		prev = cur
		phis = append(phis, cur)
	}

	// Early in the silence phi is low; after several intervals of silence it has
	// crossed the threshold — the binary timeout replacement actually fires.
	assert.Less(t, phis[0], 8.0, "phi must be low shortly after last heartbeat")
	last := phis[len(phis)-1]
	assert.Greater(t, last, 8.0, "phi must cross the threshold after prolonged silence")
	assert.True(t, d.Suspect("p", 8.0), "Suspect must flip true once phi crosses threshold")
}

// TestPhi_SuspectFlipsAfterExpectedGap asserts the threshold crossing happens
// AFTER (not before) the heartbeat interval has clearly lapsed: not suspected at
// a small fraction of an interval, suspected after a multiple of it.
//
// Mutation that kills it: a constant Phi (ignoring elapsed) would be either
// always-suspect (fails the early not-suspected assert) or never-suspect (fails
// the late suspected assert).
func TestPhi_SuspectFlipsAfterExpectedGap(t *testing.T) {
	d, clk := newPhiTestDetector(t)
	beat(d, clk, "p", 500*time.Millisecond, 40)

	// 100ms into silence (1/5 of the interval): healthy, not suspected.
	clk.Advance(100 * time.Millisecond)
	assert.False(t, d.Suspect("p", 8.0), "must not suspect 100ms into a 500ms cadence")

	// Now let many intervals worth of silence elapse: must be suspected.
	clk.Advance(5 * time.Second)
	assert.True(t, d.Suspect("p", 8.0), "must suspect after >>interval of silence")
}

// TestPhi_HighJitterToleratesLongerGaps proves variance widens the window: a
// jittery peer reaches a given phi at a LONGER silence than a regular peer.
// This asserts the distribution estimate (variance) actually feeds phi.
//
// Mutation that kills it: drop the variance term (use a fixed stdDev floor
// regardless of samples) — both peers would then yield identical phi for the
// same elapsed time and jitterPhi < regularPhi would fail.
func TestPhi_HighJitterToleratesLongerGaps(t *testing.T) {
	clk := NewManualClock(time.Unix(0, 0))
	d := NewPhiAccrualDetector(PhiAccrualConfig{
		Clock:      clk,
		MinSamples: 3,
		MinStdDev:  1 * time.Millisecond, // tiny floor so real variance dominates
	})

	// Both peers share the SAME mean inter-arrival (1s) so the comparison is
	// purely about variance. Each set of intervals sums to the same total and has
	// the same count, hence identical mean — but the jittery set has large swings
	// around 1s (high variance) while the regular set is exactly 1s every time
	// (variance 0, clamped to the tiny floor).
	regularIntervals := make([]time.Duration, 24)
	for i := range regularIntervals {
		regularIntervals[i] = 1 * time.Second
	}
	// Jittery: pairs of (1s-δ, 1s+δ) so each pair still averages 1s. Mean over the
	// whole set is exactly 1s, matching the regular peer.
	jitterIntervals := []time.Duration{}
	for rep := 0; rep < 12; rep++ {
		jitterIntervals = append(jitterIntervals,
			100*time.Millisecond, 1900*time.Millisecond)
	}
	require.Equal(t, len(regularIntervals), len(jitterIntervals),
		"sanity: equal sample counts so means match")

	feed := func(peer string, intervals []time.Duration) {
		// Feed on an independent clock so each peer's window is built in
		// isolation; we realign lastSeen afterwards for an apples-to-apples phi.
		local := NewManualClock(time.Unix(0, 0))
		for _, iv := range intervals {
			d.heartbeatAt(peer, local.Now())
			local.Advance(iv)
		}
	}
	feed("regular", regularIntervals)
	feed("jittery", jitterIntervals)

	// Realign BOTH peers' lastSeen to the same instant (now) WITHOUT adding a new
	// inter-arrival sample, so the only difference between them is window
	// variance, evaluated at an identical elapsed silence.
	now := clk.Now()
	d.setLastSeen("regular", now)
	d.setLastSeen("jittery", now)

	// Advance a fixed silence and compare phi.
	clk.Advance(2500 * time.Millisecond)
	regularPhi := d.Phi("regular")
	jitterPhi := d.Phi("jittery")

	require.Greater(t, regularPhi, 0.0, "sanity: regular peer has a positive phi at this silence")
	assert.Less(t, jitterPhi, regularPhi,
		"high-jitter peer (wider variance) must tolerate the same gap with LOWER phi: jitter=%.3f regular=%.3f",
		jitterPhi, regularPhi)
}

// TestPhi_BelowMinSamplesReturnsSafeLow asserts a peer with fewer than
// MinSamples inter-arrival samples is never suspected, regardless of silence.
//
// Mutation that kills it: remove the `len(intervals) < minSamples` early-return
// in Phi — with 1 sample the variance is 0, stdDev hits the floor, and a long
// silence would push phi over the threshold, wrongly suspecting a barely-seen
// peer.
func TestPhi_BelowMinSamplesReturnsSafeLow(t *testing.T) {
	clk := NewManualClock(time.Unix(0, 0))
	d := NewPhiAccrualDetector(PhiAccrualConfig{Clock: clk, MinSamples: 5})

	// Only 2 heartbeats => 1 inter-arrival sample (< MinSamples).
	d.Heartbeat("p")
	clk.Advance(1 * time.Second)
	d.Heartbeat("p")

	clk.Advance(1 * time.Hour) // huge silence
	assert.Equal(t, 1, d.SampleCount("p"))
	assert.Equal(t, 0.0, d.Phi("p"), "insufficient samples must yield a safe low phi (0)")
	assert.False(t, d.Suspect("p", 8.0), "must not suspect a peer with too few samples")
}

// TestPhi_UnknownPeerSafe asserts an entirely unknown peer is safe-low.
//
// Mutation that kills it: return a non-zero default from Phi for missing peers.
func TestPhi_UnknownPeerSafe(t *testing.T) {
	d, _ := newPhiTestDetector(t)
	assert.Equal(t, 0.0, d.Phi("ghost"))
	assert.False(t, d.Suspect("ghost", 8.0))
}

// TestPhi_SlidingWindowEvictsOldest asserts the window is bounded and evicts
// FIFO, and that a regime change (slow->fast cadence) is learned: after the
// window fills with fast intervals, phi for a given silence reflects the fast
// cadence (rises sooner) rather than the stale slow one.
//
// Mutation that kills it: remove the eviction branch in push() (unbounded
// window) — the stale large intervals persist, the learned mean stays high, and
// phi at the post-regime silence stays lower than the bound asserted here.
func TestPhi_SlidingWindowEvictsOldest(t *testing.T) {
	clk := NewManualClock(time.Unix(0, 0))
	d := NewPhiAccrualDetector(PhiAccrualConfig{
		Clock:      clk,
		WindowSize: 5,
		MinSamples: 3,
		MinStdDev:  10 * time.Millisecond,
	})

	// Fill with SLOW 10s intervals.
	for i := 0; i < 10; i++ {
		d.Heartbeat("p")
		clk.Advance(10 * time.Second)
	}
	// Window is capped at 5 samples regardless of 10 beats.
	assert.Equal(t, 5, d.SampleCount("p"), "window must be bounded to WindowSize")

	// Now switch to FAST 100ms intervals; after 5+ of them the slow samples are
	// fully evicted and the learned cadence is ~100ms.
	for i := 0; i < 12; i++ {
		d.Heartbeat("p")
		clk.Advance(100 * time.Millisecond)
	}
	d.Heartbeat("p")
	assert.Equal(t, 5, d.SampleCount("p"))

	// 2 seconds of silence is ~20x the FAST interval: phi must be high. If the
	// stale 10s samples had survived (no eviction), 2s would be a fraction of the
	// interval and phi would be low.
	clk.Advance(2 * time.Second)
	assert.Greater(t, d.Phi("p"), 8.0,
		"after window evicts slow samples, 2s silence (>>fast interval) must drive phi high")
}

// TestPhi_DefaultThreshold asserts Suspect treats a non-positive threshold as
// the conventional 8.0 default.
//
// Mutation that kills it: change the `threshold <= 0` default to a different
// constant — the boundary case below would diverge.
func TestPhi_DefaultThreshold(t *testing.T) {
	d, clk := newPhiTestDetector(t)
	beatJittered(d, clk, "p", 1*time.Second, 200*time.Millisecond, 40)
	// ~1.3 intervals of silence: phi is meaningfully positive but below 8.
	clk.Advance(1300 * time.Millisecond)

	phi := d.Phi("p")
	require.Greater(t, phi, 0.01, "sanity: phi is positive at this silence")
	require.Less(t, phi, 8.0, "sanity: phi below the default threshold at this silence")

	// Default (0 -> 8.0) must NOT suspect; a tiny explicit threshold WOULD,
	// proving 0 mapped to a high (8.0) default rather than to ~0.
	assert.False(t, d.Suspect("p", 0), "default threshold (8.0) must not suspect this peer")
	assert.True(t, d.Suspect("p", 0.01), "tiny explicit threshold must suspect the same peer")
}
