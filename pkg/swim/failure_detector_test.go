package swim

import (
	"testing"
	"time"
)

// TestFailureDetector_Suspect_DoesNotResetTimerOnReSuspect is the §1.1
// regression guard for the suspicion-timeout starvation bug: the probe loop
// re-suspects an unresponsive peer on every failed probe (~every probeInterval).
// FailureDetector.Suspect MUST run a single suspicion period to completion and
// NOT restart the death timer on each re-suspect — otherwise the timeout never
// fires and the peer is never Confirmed dead.
//
// With the correct (no-reset) behaviour, Confirm fires ~timeout after the FIRST
// Suspect even though re-suspects keep arriving. With the buggy reset behaviour,
// the periodic re-suspects (interval < timeout) perpetually reset the timer and
// Confirm never fires (test fails via the 2s safety timeout).
func TestFailureDetector_Suspect_DoesNotResetTimerOnReSuspect(t *testing.T) {
	confirmed := make(chan string, 1)
	const timeout = 150 * time.Millisecond
	fd := NewFailureDetector(timeout, func(id string) { confirmed <- id }, nil)

	fd.Suspect("C", 1)

	// Simulate the probe loop re-suspecting every 50ms (< timeout). A reset-on-
	// re-suspect bug would keep pushing the deadline out, starving Confirm.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		tk := time.NewTicker(50 * time.Millisecond)
		defer tk.Stop()
		inc := uint32(1)
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				inc++
				fd.Suspect("C", inc)
			}
		}
	}()

	select {
	case <-confirmed:
		// Correct: the original suspicion period completed despite re-suspects.
	case <-time.After(2 * time.Second):
		t.Fatal("Confirm never fired — re-suspect resets the death timer (suspicion-timeout starvation)")
	}
}

// TestFailureDetector_Refute_CancelsConfirm verifies a refuted suspect is not
// later Confirmed dead.
func TestFailureDetector_Refute_CancelsConfirm(t *testing.T) {
	confirmed := make(chan string, 1)
	fd := NewFailureDetector(80*time.Millisecond, func(id string) { confirmed <- id }, nil)
	fd.Suspect("C", 1)
	fd.Refute("C")
	select {
	case id := <-confirmed:
		t.Fatalf("Confirm fired for refuted member %q — refute must cancel the death timer", id)
	case <-time.After(250 * time.Millisecond):
		// Correct: refute cancelled the timer.
	}
	if fd.IsSuspected("C") {
		t.Fatal("member must not be suspected after Refute")
	}
}
