package swim

import (
	"math"
	"sync"
	"time"
)

// PhiAccrualConfig configures a PhiAccrualDetector.
//
// The phi-accrual failure detector (Hayashibara et al., "The phi Accrual
// Failure Detector", SRDS 2004) replaces a binary up/down timeout with a
// continuously rising suspicion level phi. For each peer it maintains a
// sliding window of inter-arrival times of heartbeats, estimates their
// distribution (mean + variance), and computes
//
//	phi(t_now) = -log10( P_later(t_now - t_lastHeartbeat) )
//
// where P_later(d) is the probability — under the estimated distribution — that
// the next heartbeat arrives later than d after the previous one. As silence
// grows, P_later shrinks towards 0 and phi rises without bound, so a threshold
// (e.g. 8.0) yields a tunable, adaptive suspicion decision rather than a fixed
// timeout. This is ADDITIVE to the existing fixed-timeout SuspicionManager /
// FailureDetector and does not replace them.
type PhiAccrualConfig struct {
	// Clock is the injectable time source. Defaults to SystemClock. Tests inject
	// a ManualClock for deterministic phi evaluation.
	Clock Clock
	// WindowSize is the maximum number of inter-arrival samples retained per
	// peer (the sliding window). Older samples are evicted FIFO. Defaults to 100.
	WindowSize int
	// MinSamples is the minimum number of inter-arrival samples required before
	// the detector will report a meaningful (non-zero) phi. Below this it returns
	// a safe low phi (0) so a freshly observed peer is never spuriously suspected.
	// Defaults to 3.
	MinSamples int
	// MinStdDev is a floor on the estimated standard deviation, guarding against
	// a degenerate window of (near-)identical inter-arrival times that would make
	// the normal CDF a step function and phi jump discontinuously. Defaults to
	// 100ms. Larger jitter tolerance comes from real sample variance on top of
	// this floor.
	MinStdDev time.Duration
}

// PhiAccrualDetector is a per-peer phi-accrual failure detector. It is additive
// to the existing SWIM suspicion machinery: callers feed it heartbeat arrivals
// via Heartbeat(peer) and query a rising suspicion level via Phi(peer) or a
// thresholded decision via Suspect(peer, threshold). Safe for concurrent use.
type PhiAccrualDetector struct {
	clock      Clock
	windowSize int
	minSamples int
	minStdDev  float64 // seconds

	mu    sync.Mutex
	peers map[string]*phiState
}

type phiState struct {
	// intervals holds inter-arrival times (in seconds) as a bounded FIFO window.
	intervals []float64
	// running sum and sum-of-squares for O(1) mean/variance over the window.
	sum   float64
	sumSq float64
	// lastSeen is the arrival time of the most recent heartbeat.
	lastSeen time.Time
	// have indicates lastSeen is valid (at least one heartbeat observed).
	have bool
}

// NewPhiAccrualDetector creates a PhiAccrualDetector from cfg, applying defaults.
func NewPhiAccrualDetector(cfg PhiAccrualConfig) *PhiAccrualDetector {
	clock := cfg.Clock
	if clock == nil {
		clock = NewSystemClock()
	}
	windowSize := cfg.WindowSize
	if windowSize <= 0 {
		windowSize = 100
	}
	minSamples := cfg.MinSamples
	if minSamples <= 0 {
		minSamples = 3
	}
	minStdDev := cfg.MinStdDev
	if minStdDev <= 0 {
		minStdDev = 100 * time.Millisecond
	}
	return &PhiAccrualDetector{
		clock:      clock,
		windowSize: windowSize,
		minSamples: minSamples,
		minStdDev:  minStdDev.Seconds(),
		peers:      make(map[string]*phiState),
	}
}

// Heartbeat records a heartbeat arrival from peer at the current clock time. The
// first heartbeat for a peer only establishes a baseline arrival time; each
// subsequent heartbeat appends the inter-arrival gap to that peer's sliding
// window, evicting the oldest sample once the window is full.
func (d *PhiAccrualDetector) Heartbeat(peer string) {
	d.heartbeatAt(peer, d.clock.Now())
}

// heartbeatAt records a heartbeat arrival from peer at an explicit timestamp.
// Heartbeat delegates here with the current clock time; tests use it to build a
// window on an independent timeline without depending on a particular clock.
func (d *PhiAccrualDetector) heartbeatAt(peer string, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()

	st, ok := d.peers[peer]
	if !ok {
		st = &phiState{intervals: make([]float64, 0, d.windowSize)}
		d.peers[peer] = st
	}
	if st.have {
		interval := now.Sub(st.lastSeen).Seconds()
		// Ignore non-positive intervals (clock not advanced / duplicate); they
		// carry no inter-arrival information and would corrupt the variance.
		if interval > 0 {
			st.push(interval, d.windowSize)
		}
	}
	st.lastSeen = now
	st.have = true
}

// push appends a sample to the bounded window, maintaining sum / sumSq and
// evicting FIFO when the window exceeds windowSize.
func (s *phiState) push(v float64, windowSize int) {
	s.intervals = append(s.intervals, v)
	s.sum += v
	s.sumSq += v * v
	if len(s.intervals) > windowSize {
		old := s.intervals[0]
		s.intervals = s.intervals[1:]
		s.sum -= old
		s.sumSq -= old * old
	}
}

// Phi returns the current suspicion level for peer. It rises monotonically with
// the time elapsed since the peer's last heartbeat: with healthy, regular
// heartbeats phi stays low; as silence grows phi increases without bound.
//
// Phi returns 0 (a safe low value) when:
//   - the peer is unknown,
//   - the peer has fewer than MinSamples inter-arrival samples,
//   - no time has yet elapsed since the last heartbeat.
//
// A peer whose heartbeats arrive with high jitter has a wider estimated
// distribution, so the same elapsed silence yields a LOWER phi than for a
// regular peer — i.e. jittery peers tolerate longer gaps before suspicion.
func (d *PhiAccrualDetector) Phi(peer string) float64 {
	now := d.clock.Now()
	d.mu.Lock()
	defer d.mu.Unlock()

	st, ok := d.peers[peer]
	if !ok || !st.have {
		return 0
	}
	if len(st.intervals) < d.minSamples {
		return 0
	}
	elapsed := now.Sub(st.lastSeen).Seconds()
	if elapsed <= 0 {
		// Defensive: no positive silence has elapsed (queried exactly at a
		// heartbeat, or the clock ran backwards relative to the last heartbeat).
		// A non-positive gap carries no suspicion. NOTE: this is belt-and-braces —
		// the normal upper tail already saturates to ~1 (phi ~0) for non-positive
		// elapsed — so it is not independently asserted by a test.
		return 0
	}

	n := float64(len(st.intervals))
	mean := st.sum / n
	variance := st.sumSq/n - mean*mean
	if variance < 0 {
		variance = 0 // floating-point guard
	}
	stdDev := math.Sqrt(variance)
	if stdDev < d.minStdDev {
		stdDev = d.minStdDev
	}

	return phi(elapsed, mean, stdDev)
}

// Suspect reports whether peer's current phi meets or exceeds threshold. A
// higher threshold demands a longer silence (relative to the learned heartbeat
// distribution) before the peer is suspected, trading detection latency against
// false-positive rate. A non-positive threshold is treated as the conventional
// default of 8.0.
func (d *PhiAccrualDetector) Suspect(peer string, threshold float64) bool {
	if threshold <= 0 {
		threshold = 8.0
	}
	return d.Phi(peer) >= threshold
}

// SampleCount returns the number of inter-arrival samples currently retained in
// peer's sliding window (0 if the peer is unknown). Exposed for observability
// and testing the windowing behaviour.
func (d *PhiAccrualDetector) SampleCount(peer string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	st, ok := d.peers[peer]
	if !ok {
		return 0
	}
	return len(st.intervals)
}

// phi computes -log10(P_later(elapsed)) where P_later is the probability that an
// inter-arrival exceeds elapsed under a Normal(mean, stdDev) model. This is the
// Hayashibara formulation: P_later(t) = 1 - F(t) = 1 - CDF_normal(t), so
//
//	phi(t) = -log10(1 - CDF_normal((t-mean)/stdDev)).
//
// As t grows beyond the mean, 1-CDF -> 0 and phi -> +inf, giving a continuously
// rising suspicion level. stdDev is assumed > 0 (the caller floors it).
func phi(elapsed, mean, stdDev float64) float64 {
	y := (elapsed - mean) / stdDev
	// pLater = 1 - CDF(elapsed). Use the numerically stable upper-tail of the
	// standard normal via the complementary error function:
	//   1 - CDF(z) = 0.5 * erfc(z / sqrt2)
	pLater := 0.5 * math.Erfc(y/math.Sqrt2)
	if pLater < phiMinProb {
		pLater = phiMinProb
	}
	p := -math.Log10(pLater)
	// When elapsed is well below the mean, pLater is ~1 and -log10 yields a tiny
	// negative (or -0) value. Clamp to 0 so phi is a clean, non-negative rising
	// suspicion level (a "healthy" peer sits at phi==0).
	if p < 0 {
		p = 0
	}
	return p
}

// phiMinProb floors the tail probability so phi is large-but-finite under
// extreme silence (avoids +Inf while still crossing any practical threshold).
const phiMinProb = 1e-300
