package gpu

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// ---- Exclusive mode tests -----------------------------------------------

// TestSharingExclusive_FirstJobAdmitted verifies that the first job is admitted
// on an empty exclusive device.
func TestSharingExclusive_FirstJobAdmitted(t *testing.T) {
	s := NewDeviceSharingState("dev-0")
	req := SharingRequest{Mode: ShareExclusive, JobID: "job-A"}
	if err := s.Admit(req, 0, 0); err != nil {
		t.Fatalf("first exclusive job must be admitted, got error: %v", err)
	}
}

// TestSharingExclusive_SecondJobRejected verifies that a second job is REJECTED
// while the first exclusive job is still admitted.  If Admit always returned
// nil this test would fail — the anti-bluff bite.
func TestSharingExclusive_SecondJobRejected(t *testing.T) {
	s := NewDeviceSharingState("dev-0")

	first := SharingRequest{Mode: ShareExclusive, JobID: "job-A"}
	if err := s.Admit(first, 0, 0); err != nil {
		t.Fatalf("unexpected error admitting first job: %v", err)
	}

	second := SharingRequest{Mode: ShareExclusive, JobID: "job-B"}
	if err := s.Admit(second, 0, 0); err == nil {
		t.Fatal("second exclusive job must be rejected while first is active — Admit returned nil (PASS-bluff)")
	}
}

// TestSharingExclusive_ReleaseAllowsNext verifies that after releasing the
// first job, a new exclusive job is admitted.
func TestSharingExclusive_ReleaseAllowsNext(t *testing.T) {
	s := NewDeviceSharingState("dev-0")

	first := SharingRequest{Mode: ShareExclusive, JobID: "job-A"}
	if err := s.Admit(first, 0, 0); err != nil {
		t.Fatalf("unexpected error admitting first job: %v", err)
	}
	if err := s.Release("job-A"); err != nil {
		t.Fatalf("unexpected error releasing job-A: %v", err)
	}

	second := SharingRequest{Mode: ShareExclusive, JobID: "job-B"}
	if err := s.Admit(second, 0, 0); err != nil {
		t.Fatalf("second exclusive job must be admitted after first is released: %v", err)
	}
}

// ---- TimeSlice mode tests -----------------------------------------------

// TestSharingTimeSlice_WithinBudget verifies that two jobs whose combined
// TimeSliceMS is exactly equal to the device budget are both admitted.
func TestSharingTimeSlice_WithinBudget(t *testing.T) {
	const totalQuantum = 100
	s := NewDeviceSharingState("dev-1")

	r1 := SharingRequest{Mode: ShareTimeSlice, JobID: "ts-1", TimeSliceMS: 60}
	if err := s.Admit(r1, totalQuantum, 0); err != nil {
		t.Fatalf("60ms job must be admitted in a 100ms budget: %v", err)
	}

	r2 := SharingRequest{Mode: ShareTimeSlice, JobID: "ts-2", TimeSliceMS: 40}
	if err := s.Admit(r2, totalQuantum, 0); err != nil {
		t.Fatalf("40ms job must be admitted (60+40=100): %v", err)
	}
}

// TestSharingTimeSlice_OverBudgetRejected verifies that a job is REJECTED when
// it would push quantumUsedMS over the device total.  If Admit always returned
// nil this test would fail — the primary anti-bluff bite for TimeSlice.
func TestSharingTimeSlice_OverBudgetRejected(t *testing.T) {
	const totalQuantum = 100
	s := NewDeviceSharingState("dev-1")

	r1 := SharingRequest{Mode: ShareTimeSlice, JobID: "ts-1", TimeSliceMS: 60}
	if err := s.Admit(r1, totalQuantum, 0); err != nil {
		t.Fatalf("60ms job must be admitted: %v", err)
	}

	// 60 + 50 = 110 > 100 — must be rejected.
	r2 := SharingRequest{Mode: ShareTimeSlice, JobID: "ts-2", TimeSliceMS: 50}
	if err := s.Admit(r2, totalQuantum, 0); err == nil {
		t.Fatal("50ms job must be REJECTED (would exceed 100ms budget) — Admit returned nil (PASS-bluff)")
	}
}

// TestSharingTimeSlice_ReleaseFreesQuota verifies that releasing a job frees
// its quantum, allowing a previously over-budget job to be admitted.
func TestSharingTimeSlice_ReleaseFreesQuota(t *testing.T) {
	const totalQuantum = 100
	s := NewDeviceSharingState("dev-1")

	r1 := SharingRequest{Mode: ShareTimeSlice, JobID: "ts-1", TimeSliceMS: 60}
	if err := s.Admit(r1, totalQuantum, 0); err != nil {
		t.Fatalf("60ms job must be admitted: %v", err)
	}
	if err := s.Release("ts-1"); err != nil {
		t.Fatalf("unexpected error releasing ts-1: %v", err)
	}

	// After release, the full 100ms budget is available again.
	r2 := SharingRequest{Mode: ShareTimeSlice, JobID: "ts-2", TimeSliceMS: 100}
	if err := s.Admit(r2, totalQuantum, 0); err != nil {
		t.Fatalf("100ms job must be admitted after previous was released: %v", err)
	}
}

// TestSharingTimeSlice_SumExact verifies the sequence 60+50 rejected, 60+40
// admitted, demonstrating that accounting tracks correctly.
func TestSharingTimeSlice_SumExact(t *testing.T) {
	const totalQuantum = 100
	s := NewDeviceSharingState("dev-1")

	// Admit 60ms — used = 60.
	r1 := SharingRequest{Mode: ShareTimeSlice, JobID: "ts-A", TimeSliceMS: 60}
	if err := s.Admit(r1, totalQuantum, 0); err != nil {
		t.Fatalf("unexpected: %v", err)
	}

	// Reject 50ms (60+50=110 > 100).
	rOver := SharingRequest{Mode: ShareTimeSlice, JobID: "ts-over", TimeSliceMS: 50}
	if err := s.Admit(rOver, totalQuantum, 0); err == nil {
		t.Fatal("50ms must be rejected (60+50>100) — PASS-bluff")
	}

	// Admit 40ms (60+40=100 == limit).
	r2 := SharingRequest{Mode: ShareTimeSlice, JobID: "ts-B", TimeSliceMS: 40}
	if err := s.Admit(r2, totalQuantum, 0); err != nil {
		t.Fatalf("40ms job must be admitted (60+40=100): %v", err)
	}

	// Now full — any positive request must be rejected.
	rFull := SharingRequest{Mode: ShareTimeSlice, JobID: "ts-C", TimeSliceMS: 1}
	if err := s.Admit(rFull, totalQuantum, 0); err == nil {
		t.Fatal("budget is full: any additional job must be rejected")
	}
}

// ---- MIG mode tests ------------------------------------------------------

// TestSharingMIG_MaxPartitionsAdmitted verifies that exactly deviceMIGmax jobs
// are admitted.
func TestSharingMIG_MaxPartitionsAdmitted(t *testing.T) {
	const maxPartitions = 7
	s := NewDeviceSharingState("dev-mig")

	for i := 0; i < maxPartitions; i++ {
		req := SharingRequest{
			Mode:       ShareMIG,
			JobID:      fmt.Sprintf("mig-%d", i),
			MIGProfile: "1g.5gb",
		}
		if err := s.Admit(req, 0, maxPartitions); err != nil {
			t.Fatalf("partition %d of %d must be admitted: %v", i+1, maxPartitions, err)
		}
	}
}

// TestSharingMIG_OverLimitRejected verifies that the (max+1)th job is
// REJECTED.  If Admit always returned nil this would fail — the anti-bluff
// bite for MIG.
func TestSharingMIG_OverLimitRejected(t *testing.T) {
	const maxPartitions = 7
	s := NewDeviceSharingState("dev-mig")

	for i := 0; i < maxPartitions; i++ {
		req := SharingRequest{
			Mode:       ShareMIG,
			JobID:      fmt.Sprintf("mig-%d", i),
			MIGProfile: "1g.5gb",
		}
		if err := s.Admit(req, 0, maxPartitions); err != nil {
			t.Fatalf("partition %d of %d must be admitted: %v", i+1, maxPartitions, err)
		}
	}

	// 8th job must be rejected.
	over := SharingRequest{Mode: ShareMIG, JobID: "mig-overflow", MIGProfile: "1g.5gb"}
	if err := s.Admit(over, 0, maxPartitions); err == nil {
		t.Fatal("8th MIG job must be REJECTED when limit is 7 — Admit returned nil (PASS-bluff)")
	}
}

// TestSharingMIG_ReleaseFreesPartition verifies that releasing one job allows
// the 8th to be admitted.
func TestSharingMIG_ReleaseFreesPartition(t *testing.T) {
	const maxPartitions = 7
	s := NewDeviceSharingState("dev-mig")

	for i := 0; i < maxPartitions; i++ {
		req := SharingRequest{
			Mode:       ShareMIG,
			JobID:      fmt.Sprintf("mig-%d", i),
			MIGProfile: "1g.5gb",
		}
		if err := s.Admit(req, 0, maxPartitions); err != nil {
			t.Fatalf("partition %d must be admitted: %v", i+1, err)
		}
	}
	if err := s.Release("mig-0"); err != nil {
		t.Fatalf("unexpected error releasing mig-0: %v", err)
	}

	extra := SharingRequest{Mode: ShareMIG, JobID: "mig-extra", MIGProfile: "1g.5gb"}
	if err := s.Admit(extra, 0, maxPartitions); err != nil {
		t.Fatalf("extra MIG job must be admitted after a partition was freed: %v", err)
	}
}

// ---- Mode mismatch tests -------------------------------------------------

// TestSharingModeMismatch_ExclusiveThenMPS verifies that once an exclusive job
// is present, an MPS request on the same device is rejected.
func TestSharingModeMismatch_ExclusiveThenMPS(t *testing.T) {
	s := NewDeviceSharingState("dev-2")

	excl := SharingRequest{Mode: ShareExclusive, JobID: "excl-job"}
	if err := s.Admit(excl, 0, 0); err != nil {
		t.Fatalf("exclusive job must be admitted first: %v", err)
	}

	// An MPS request while the device is in exclusive mode must be rejected.
	mps := SharingRequest{Mode: ShareMPS, JobID: "mps-job"}
	if err := s.Admit(mps, 0, 0); err == nil {
		t.Fatal("MPS request must be rejected on an exclusive device — Admit returned nil (PASS-bluff)")
	}
}

// TestSharingModeMismatch_MPSThenExclusive verifies the reverse: once MPS is
// established, an Exclusive request must be rejected.
func TestSharingModeMismatch_MPSThenExclusive(t *testing.T) {
	s := NewDeviceSharingState("dev-3")

	m1 := SharingRequest{Mode: ShareMPS, JobID: "mps-1"}
	if err := s.Admit(m1, 0, 0); err != nil {
		t.Fatalf("first MPS job must be admitted: %v", err)
	}

	excl := SharingRequest{Mode: ShareExclusive, JobID: "excl-job"}
	if err := s.Admit(excl, 0, 0); err == nil {
		t.Fatal("exclusive request must be rejected on an MPS device — Admit returned nil (PASS-bluff)")
	}
}

// TestSharingMPS_MultipleConcurrent verifies that several MPS jobs are all
// admitted on the same device (MPS allows concurrent clients).
func TestSharingMPS_MultipleConcurrent(t *testing.T) {
	s := NewDeviceSharingState("dev-mps")
	for i := 0; i < 5; i++ {
		req := SharingRequest{Mode: ShareMPS, JobID: fmt.Sprintf("mps-%d", i)}
		if err := s.Admit(req, 0, 0); err != nil {
			t.Fatalf("MPS job %d must be admitted (concurrent sharing): %v", i, err)
		}
	}
}

// ---- Hardware-seam tests ------------------------------------------------

// TestConfigureSharing_AppleBackendReturnsErrUnsupported verifies that
// ConfigureSharing with MPS mode on an AppleBackend returns ErrUnsupported.
// This proves that ConfigureSharing does NOT fake hardware success on a host
// that lacks MPS/MIG hardware.
func TestConfigureSharing_AppleBackendReturnsErrUnsupported(t *testing.T) {
	b := &AppleBackend{}
	req := SharingRequest{Mode: ShareMPS, JobID: "job-mps", TimeSliceMS: 10}
	err := ConfigureSharing(context.Background(), b, "gpu-0", req)
	if err == nil {
		t.Fatal("ConfigureSharing on AppleBackend must return an error — nil means fake hardware success (PASS-bluff)")
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected errors.Is(err, ErrUnsupported) to be true, got: %v", err)
	}
}

// TestConfigureSharing_MIGReturnsErrUnsupported verifies that MIG mode always
// returns ErrUnsupported because no wired GPUBackend exposes a MIG-configure
// method.
func TestConfigureSharing_MIGReturnsErrUnsupported(t *testing.T) {
	b := &AppleBackend{}
	req := SharingRequest{Mode: ShareMIG, JobID: "job-mig", MIGProfile: "1g.5gb"}
	err := ConfigureSharing(context.Background(), b, "gpu-0", req)
	if err == nil {
		t.Fatal("ConfigureSharing MIG must return an error")
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected errors.Is(err, ErrUnsupported), got: %v", err)
	}
}

// TestConfigureSharing_ExclusiveNoHardwareNeeded verifies that exclusive mode
// requires no hardware configuration change and returns nil.
func TestConfigureSharing_ExclusiveNoHardwareNeeded(t *testing.T) {
	b := &AppleBackend{}
	req := SharingRequest{Mode: ShareExclusive, JobID: "job-excl"}
	if err := ConfigureSharing(context.Background(), b, "gpu-0", req); err != nil {
		t.Fatalf("exclusive mode needs no hardware config; expected nil, got: %v", err)
	}
}

// ---- SharingMode String() tests -----------------------------------------

func TestSharingMode_String(t *testing.T) {
	tests := []struct {
		mode SharingMode
		want string
	}{
		{ShareExclusive, "exclusive"},
		{ShareMPS, "mps"},
		{ShareTimeSlice, "time-slice"},
		{ShareMIG, "mig"},
		{SharingMode(99), "SharingMode(99)"},
	}
	for _, tt := range tests {
		if got := tt.mode.String(); got != tt.want {
			t.Errorf("SharingMode(%d).String() = %q, want %q", int(tt.mode), got, tt.want)
		}
	}
}

// ---- Release error cases ------------------------------------------------

// TestRelease_UnknownJobReturnsError verifies that releasing a job that was
// never admitted returns an error.
func TestRelease_UnknownJobReturnsError(t *testing.T) {
	s := NewDeviceSharingState("dev-rel")
	if err := s.Release("ghost-job"); err == nil {
		t.Fatal("releasing an unadmitted job must return an error")
	}
}

// TestAdmit_EmptyJobIDReturnsError ensures empty JobID is rejected before any
// state mutation.
func TestAdmit_EmptyJobIDReturnsError(t *testing.T) {
	s := NewDeviceSharingState("dev-id")
	req := SharingRequest{Mode: ShareExclusive, JobID: ""}
	if err := s.Admit(req, 0, 0); err == nil {
		t.Fatal("empty JobID must be rejected")
	}
}

// TestAdmit_DuplicateJobIDReturnsError ensures admitting the same JobID twice
// is rejected.
func TestAdmit_DuplicateJobIDReturnsError(t *testing.T) {
	s := NewDeviceSharingState("dev-dup")
	req := SharingRequest{Mode: ShareMPS, JobID: "dup"}
	if err := s.Admit(req, 0, 0); err != nil {
		t.Fatalf("first admit must succeed: %v", err)
	}
	if err := s.Admit(req, 0, 0); err == nil {
		t.Fatal("duplicate JobID must be rejected")
	}
}
