package computeproto_test

// Tests for ReadComputeTaskSafe (HXC-1765): the validated decode entry point
// that turns the FlatBuffers raw-accessor DoS (an out-of-range panic on hostile
// bytes) into a typed ErrMalformedBuffer at the trust boundary.
//
// The sibling adversarial file documents that the RAW ReadComputeTask panics on
// ~83-99% of hostile inputs. These tests prove the SAFE wrapper (1) decodes a
// genuine buffer identically and (2) returns an error — never panics — for the
// same hostile battery (empty, nil, every truncated prefix of a valid buffer,
// and deterministic fuzz).

import (
	"errors"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/computeproto"
)

// TestReadComputeTaskSafe_ValidDecodesIdentically proves the safe path is a true
// drop-in for the raw path on a genuine buffer (catches an always-error mutation).
func TestReadComputeTaskSafe_ValidDecodesIdentically(t *testing.T) {
	spec := fullSpec()
	buf := computeproto.BuildComputeTask(spec)

	task, err := computeproto.ReadComputeTaskSafe(buf)
	if err != nil {
		t.Fatalf("valid buffer wrongly rejected: %v", err)
	}
	if task == nil {
		t.Fatal("valid buffer decoded to a nil task")
	}
	if got := string(task.Id()); got != spec.ID {
		t.Errorf("Id: want %q got %q", spec.ID, got)
	}
	if got := string(task.Kernel()); got != spec.Kernel {
		t.Errorf("Kernel: want %q got %q", spec.Kernel, got)
	}
	if got := task.InputsLength(); got != len(spec.Inputs) {
		t.Errorf("InputsLength: want %d got %d", len(spec.Inputs), got)
	}
	if got := task.OutputsLength(); got != len(spec.Outputs) {
		t.Errorf("OutputsLength: want %d got %d", len(spec.Outputs), got)
	}
}

// TestReadComputeTaskSafe_HostileBytesErrorNotPanic is the centerpiece: every
// hostile input — including the truncated prefixes that make the RAW accessor
// panic — must come back as an ErrMalformedBuffer error, never a process crash.
//
// ANTI-BLUFF: a recover() in the test fails loudly if ReadComputeTaskSafe ever
// lets a panic escape, so the test bites if the recover guard or the eager
// field-traversal in validateComputeTask is removed from production.
func TestReadComputeTaskSafe_HostileBytesErrorNotPanic(t *testing.T) {
	valid := computeproto.BuildComputeTask(fullSpec())

	var hostile [][]byte
	hostile = append(hostile,
		nil,
		[]byte{},
		[]byte{0x00},
		[]byte{0xff, 0xff, 0xff, 0xff},
		[]byte("not a flatbuffers buffer at all"),
	)
	// Every truncated prefix of a valid buffer (these are what make the raw
	// accessor panic). The full-length prefix is valid and must decode cleanly.
	for n := 0; n < len(valid); n++ {
		hostile = append(hostile, append([]byte(nil), valid[:n]...))
	}
	// Deterministic LCG fuzz (no rand import; reproducible).
	var seed uint64 = 0x243F6A8885A308D3
	next := func() uint64 { seed = seed*6364136223846793005 + 1442695040888963407; return seed }
	for i := 0; i < 8192; i++ {
		b := make([]byte, int(next()%96))
		for j := range b {
			b[j] = byte(next())
		}
		hostile = append(hostile, b)
	}

	for i, in := range hostile {
		func(i int, in []byte) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("ReadComputeTaskSafe PANICKED on hostile input #%d (len=%d): %v "+
						"— the DoS guard (recover + eager traversal) is not holding", i, len(in), r)
				}
			}()
			task, err := computeproto.ReadComputeTaskSafe(in)
			if err != nil {
				if !errors.Is(err, computeproto.ErrMalformedBuffer) {
					t.Fatalf("hostile input #%d: error is not ErrMalformedBuffer: %v", i, err)
				}
				if task != nil {
					t.Fatalf("hostile input #%d: error returned but task is non-nil", i)
				}
				return
			}
			// No error: the buffer parsed as a (possibly degenerate) task. That is
			// acceptable ONLY if the returned accessor is itself safe to touch
			// without panicking — re-exercise it under the same recover.
			if task != nil {
				_ = string(task.Id())
				_ = string(task.Kernel())
			}
		}(i, in)
	}
}

// TestReadComputeTaskSafe_UndersizedBufferRejected pins the cheap length guard:
// a buffer shorter than a root uoffset is rejected before any accessor runs.
func TestReadComputeTaskSafe_UndersizedBufferRejected(t *testing.T) {
	for _, n := range []int{0, 1, 2, 3} {
		task, err := computeproto.ReadComputeTaskSafe(make([]byte, n))
		if err == nil {
			t.Fatalf("len=%d buffer must be rejected, got nil error", n)
		}
		if !errors.Is(err, computeproto.ErrMalformedBuffer) {
			t.Fatalf("len=%d: error is not ErrMalformedBuffer: %v", n, err)
		}
		if task != nil {
			t.Fatalf("len=%d: rejected but task non-nil", n)
		}
	}
}
