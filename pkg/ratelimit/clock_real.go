package ratelimit

import "time"

// realNowNano returns the current real wall-clock time in nanoseconds.
// It is the single point that bridges RealClock to the time package.
func realNowNano() int64 {
	return time.Now().UnixNano()
}
