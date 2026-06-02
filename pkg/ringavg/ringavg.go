// Package ringavg provides a fixed-window ring-buffer moving average that
// smooths a noisy utilization stream so a single transient spike does not
// trip a threshold.
//
// The smoother is deterministic and self-contained: it holds the most recent
// N samples in a circular buffer and reports the arithmetic mean of those
// buffered samples. A single transient spike is diluted across the window, so
// the averaged value stays below a threshold that the raw spike exceeds. A
// sustained high signal (>= N samples) fully populates the window and so does
// drive the average above the threshold — proving the smoother does not blanket
// suppress real signals.
package ringavg

// RingAverage is a fixed-capacity ring-buffer moving average.
//
// Push records the newest value, evicting the oldest once the buffer is full.
// Average returns the mean of the currently buffered samples.
type RingAverage struct {
	buf   []float64 // circular storage of capacity cap
	cap   int       // window size N
	head  int       // index where the next Push will write
	count int       // number of valid samples currently buffered (<= cap)
	sum   float64   // running sum of the buffered samples (sum of buf[0:count slots])
}

// NewRingAverage returns a RingAverage with the given window capacity n.
// n must be >= 1; values <= 0 are clamped to 1 so the buffer is always usable.
func NewRingAverage(n int) *RingAverage {
	if n < 1 {
		n = 1
	}
	return &RingAverage{
		buf: make([]float64, n),
		cap: n,
	}
}

// Push records v as the newest sample. Once the window is full the oldest
// sample is evicted (its contribution removed from the running sum) before v
// is recorded, so the average always reflects only the last N samples.
func (r *RingAverage) Push(v float64) {
	if r.count == r.cap {
		// Buffer full: r.head points at the oldest sample, which we evict.
		r.sum -= r.buf[r.head]
	} else {
		r.count++
	}
	r.buf[r.head] = v
	r.sum += v
	r.head++
	if r.head == r.cap {
		r.head = 0
	}
}

// Average returns the arithmetic mean of the buffered samples. With no samples
// it returns 0.
func (r *RingAverage) Average() float64 {
	if r.count == 0 {
		return 0
	}
	return r.sum / float64(r.count)
}

// Len reports how many samples are currently buffered (0..N).
func (r *RingAverage) Len() int { return r.count }

// Full reports whether the window has filled to capacity.
func (r *RingAverage) Full() bool { return r.count == r.cap }
