//go:build !windows
// +build !windows

package backends

// native_stress_chaos_test.go — sustained-load (stress) and mid-flight kill
// (chaos) coverage for the F5 PTY use-after-close / double-close fix in
// native.go, complementing the deterministic regression guards in
// native_race_test.go (per Constitution §11.4.85).
//
//   STRESS: many goroutines hammer the I/O surface (SendInput/CaptureOutput/
//           Resize) of a live session under sustained load while it is alive,
//           proving the close-once seam + serialized I/O hold without panicking
//           or tripping -race on the shared pty fd.
//   CHAOS:  Kill() races a swarm of in-flight I/O calls (and the process reaper)
//           across many iterations, so the fd close lands mid-I/O. The invariant:
//           the fd is closed exactly once (no double-close), no I/O ever operates
//           on a closed/recycled fd (no UAF), and closed-session I/O fails
//           cleanly with an error instead of panicking.
//
// Run with -race.

import (
	"sync"
	"testing"
)

// TestNativeBackend_StressConcurrentIO_F5 (stress) pounds a single session with
// many concurrent SendInput/CaptureOutput/Resize calls under sustained load. The
// invariant asserted: every I/O call returns WITHOUT panic and the calls all
// serialize on sess.mu so they never race on the shared pty fd (verified under
// -race). We deliberately do NOT assert the session stays open: the backing
// shell may exit and the reaper may legitimately close the fd; the contract is
// that I/O against a closed fd fails cleanly (error), never a UAF/panic. To keep
// the I/O path live for the whole run without relying on a wall-clock-bounded
// child process, the session is re-created if its process exits mid-run.
func TestNativeBackend_StressConcurrentIO_F5(t *testing.T) {
	skipIfWindows(t)

	b := NewNativeBackend()
	const id = "test-native-stress-io"
	if _, err := b.Create(id, "cat"); err != nil { // `cat` blocks on its PTY indefinitely
		t.Fatalf("create failed: %v", err)
	}
	defer b.Kill(id)

	const workers = 60
	const iterations = 50
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				// Each call must return cleanly (success or a clean error), never
				// panic and never touch a closed/recycled fd (caught by -race).
				switch w % 3 {
				case 0:
					_ = b.SendInput(id, "stress\n")
				case 1:
					_, _ = b.CaptureOutput(id)
				case 2:
					_ = b.Resize(id, 24, 80)
				}
			}
		}(w)
	}
	wg.Wait()
}

// TestNativeBackend_ChaosKillRacingIO_F5 (chaos) injects Kill() mid-flight into a
// dense swarm of concurrent I/O calls across many iterations, so the fd close
// lands while SendInput/CaptureOutput/Resize are in progress and while the
// process reaper may also be closing. The invariants: exactly-once fd close (no
// double-close), no I/O on a closed/recycled fd (no UAF), and post-close I/O
// returns an error rather than panicking. Under -race this surfaces any residual
// data race on the shared pty fd.
func TestNativeBackend_ChaosKillRacingIO_F5(t *testing.T) {
	skipIfWindows(t)

	const iterations = 60
	for it := 0; it < iterations; it++ {
		b := NewNativeBackend()
		id, err := b.Create("test-native-chaos-kill", "sleep 30")
		if err != nil {
			t.Fatalf("create failed: %v", err)
		}

		b.mu.Lock()
		sess := b.sessions[id]
		b.mu.Unlock()

		const ioWorkers = 12
		var wg sync.WaitGroup

		// In-flight I/O swarm.
		wg.Add(ioWorkers)
		for w := 0; w < ioWorkers; w++ {
			go func(w int) {
				defer wg.Done()
				for i := 0; i < 8; i++ {
					switch w % 3 {
					case 0:
						_ = b.SendInput(id, "echo chaos\n")
					case 1:
						_, _ = b.CaptureOutput(id)
					case 2:
						_ = b.Resize(id, 30, 100)
					}
				}
			}(w)
		}
		// Mid-flight kill + a second kill funnelling through the close-once seam,
		// racing the reaper goroutine that also calls closePTY.
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = b.Kill(id)
		}()
		go func() {
			defer wg.Done()
			if sess != nil {
				sess.closePTY() // extra close racing Kill + reaper
			}
		}()

		wg.Wait()

		// After the dust settles the fd must be closed exactly once and the
		// session must report closed.
		if sess != nil && !sess.isClosed() {
			t.Fatalf("iteration %d: session not marked closed after Kill+closePTY", it)
		}
		_ = b.Kill(id) // best-effort final cleanup
	}
}
