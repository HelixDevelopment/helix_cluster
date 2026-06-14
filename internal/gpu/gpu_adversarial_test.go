package gpu

// Adversarial, sink-side test suite for the REAL GPU device + allocation layer.
//
// These tests probe the documented invariants of internal/gpu under concurrency
// and at the OS boundary:
//
//   (a) OVER-COMMIT / DOUBLE-ALLOCATION UNDER RACE
//       Manager.AllocateGPU promises (manager.go) idempotent per-job allocation
//       and "at most one holder per device". We launch N goroutines contending
//       for K GPUs and assert SINK-SIDE that no physical GPU is ever handed to
//       two DISTINCT jobs and that total allocated <= K (never over-allocate).
//       Reservation.Reserve promises (reservation.go) "never exceed physical
//       capacity"; we hammer it and assert total used <= capacity.
//
//   (b) UNGUARDED SHARED STATE
//       Concurrent Allocate/Release/List/Stats on one Manager must be race-free
//       and conserve units: allocated + available == K at every quiescent point.
//
//   (c) DOUBLE-FREE / FREE-FOREIGN
//       ReleaseGPU on a job twice, or on a foreign job, must not inflate the
//       free pool beyond K (phantom capacity). DeviceSharingState.Release of an
//       unknown job must be a typed error, not a silent quantum refund.
//
//   (d) CLAUDE-2 PER-OS REAL PROBE + NUMERIC SANITY
//       Monitor-refreshed UtilizationPercent / TemperatureC must be finite and
//       in range (no >100%, negative, or NaN). On the host OS the detection
//       probe must yield REAL values measured against an independent OS oracle
//       (macOS: sysctl hw.memsize; Linux: a synthetic /proc oracle), never a
//       fabricated constant.
//
// Every assertion is SINK-SIDE (no "did not panic"): we check the actual
// contents of the device map, the conserved unit count, and real OS values.

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// writeProcGPU writes a synthetic /proc/driver/nvidia/gpus/<pci>/information
// file under gpuDir describing a GPU with the given model and memory (MiB). It
// is the Linux OS-oracle fixture for TestAdversarial_PerOSProbeIsReal.
func writeProcGPU(t *testing.T, gpuDir, model string, memMB int) error {
	t.Helper()
	if err := os.MkdirAll(gpuDir, 0o755); err != nil {
		return err
	}
	body := "Model: \t" + model + "\nVideo Memory: \t" + strconv.Itoa(memMB) + " MiB\n"
	return os.WriteFile(filepath.Join(gpuDir, "information"), []byte(body), 0o644)
}

// makeInventory builds k Available GPUs with deterministic IDs gpu-0..gpu-(k-1).
func makeInventory(k, memMB int) []*GPU {
	gpus := make([]*GPU, k)
	for i := 0; i < k; i++ {
		gpus[i] = &GPU{
			ID:       "gpu-" + strconv.Itoa(i),
			UUID:     "UUID-" + strconv.Itoa(i),
			Model:    "Test GPU",
			MemoryMB: memMB,
			Status:   Available,
		}
	}
	return gpus
}

// ---------------------------------------------------------------------------
// (a) OVER-COMMIT / DOUBLE-ALLOCATION UNDER RACE
// ---------------------------------------------------------------------------

// TestAdversarial_NoDoubleAllocationUnderRace launches many goroutines, each
// with a UNIQUE jobID, all racing AllocateGPU on K GPUs. The promised invariant
// is "at most one holder per device". We assert SINK-SIDE that:
//   - no GPU's AllocatedTo is shared by two distinct successful holders,
//   - the number of distinct successful holders <= K (no over-allocation),
//   - exactly (successful holders) GPUs are Allocated in the final map.
func TestAdversarial_NoDoubleAllocationUnderRace(t *testing.T) {
	const k = 8
	const workers = 200

	m := NewManager()
	m.InjectGPUsForTest(makeInventory(k, 1024))

	var mu sync.Mutex
	// gpuID -> jobID that the Manager reported as holding it.
	holderOf := make(map[string]string)
	var successes int64

	var wg sync.WaitGroup
	start := make(chan struct{})
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(jobID string) {
			defer wg.Done()
			<-start
			g, err := m.AllocateGPU(jobID)
			if err != nil {
				return // pool full for this worker — legitimate.
			}
			atomic.AddInt64(&successes, 1)
			mu.Lock()
			if prev, ok := holderOf[g.ID]; ok && prev != jobID {
				mu.Unlock()
				t.Errorf("DOUBLE-ALLOCATION: GPU %s handed to both %q and %q", g.ID, prev, jobID)
				return
			}
			holderOf[g.ID] = jobID
			mu.Unlock()
		}("job-" + strconv.Itoa(w))
	}
	close(start)
	wg.Wait()

	// SINK-SIDE invariant 1: distinct successful holders never exceed capacity.
	if int(successes) > k {
		t.Fatalf("OVER-COMMIT: %d successful allocations from a %d-GPU pool", successes, k)
	}

	// SINK-SIDE invariant 2: every GPU in the final map has at most one holder,
	// and the count of Allocated GPUs equals the count of distinct holders.
	if len(holderOf) > k {
		t.Fatalf("OVER-COMMIT: %d distinct GPUs reported held from a %d-GPU pool", len(holderOf), k)
	}
	allocated := 0
	seenJob := make(map[string]string) // jobID -> gpuID, to catch a job on 2 GPUs
	for _, g := range m.ListGPUs() {
		if g.Status == Allocated {
			allocated++
			if g.AllocatedTo == "" {
				t.Errorf("GPU %s is Allocated but AllocatedTo is empty", g.ID)
			}
			if prevGPU, ok := seenJob[g.AllocatedTo]; ok {
				t.Errorf("job %q holds two GPUs: %s and %s", g.AllocatedTo, prevGPU, g.ID)
			}
			seenJob[g.AllocatedTo] = g.ID
		}
	}
	if allocated != len(holderOf) {
		t.Fatalf("map inconsistency: %d GPUs Allocated but %d distinct holders tracked", allocated, len(holderOf))
	}
	t.Logf("race over-commit probe: %d workers, %d/%d GPUs allocated, no double-allocation", workers, allocated, k)
}

// TestAdversarial_ReservationNeverOverCapacity hammers Reservation.Reserve from
// many goroutines and asserts the promised "never exceed physical capacity":
// at no observed moment may total used exceed capacity, and the sum of granted
// units never exceeds capacity.
func TestAdversarial_ReservationNeverOverCapacity(t *testing.T) {
	const capacity = 16
	r, err := NewReservation(ReservationConfig{
		Capacity:         capacity,
		InteractiveRatio: 0.5,
		HighWaterRatio:   1.0, // disable the batch hard-cap so we test raw capacity.
	})
	if err != nil {
		t.Fatalf("NewReservation: %v", err)
	}

	const workers = 128
	var granted int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for w := 0; w < workers; w++ {
		wg.Add(1)
		// Alternate classes so both admission branches are exercised.
		class := Interactive
		if w%2 == 0 {
			class = Batch
		}
		go func(c WorkloadClass) {
			defer wg.Done()
			<-start
			if _, err := r.Reserve(c, 1); err == nil {
				atomic.AddInt64(&granted, 1)
				// SINK-SIDE: observe total used immediately after a grant.
				if used := r.Used(Interactive) + r.Used(Batch); used > capacity {
					t.Errorf("OVER-COMMIT: total used %d exceeds capacity %d after grant", used, capacity)
				}
			}
		}(class)
	}
	close(start)
	wg.Wait()

	if g := atomic.LoadInt64(&granted); g > capacity {
		t.Fatalf("OVER-COMMIT: %d units granted from a %d-unit pool", g, capacity)
	}
	finalUsed := r.Used(Interactive) + r.Used(Batch)
	if finalUsed > capacity {
		t.Fatalf("OVER-COMMIT: final used %d > capacity %d", finalUsed, capacity)
	}
	if int64(finalUsed) != atomic.LoadInt64(&granted) {
		t.Fatalf("conservation broken: final used %d != granted %d", finalUsed, granted)
	}
	t.Logf("reservation race: %d granted, final used %d, capacity %d", granted, finalUsed, capacity)
}

// ---------------------------------------------------------------------------
// (b) UNGUARDED SHARED STATE — conservation under concurrent mutation
// ---------------------------------------------------------------------------

// TestAdversarial_ConcurrentAllocateReleaseConserves runs a churn of
// allocate/release/list/stats on one Manager and asserts at the quiescent end
// that allocated + available == K (no unit lost or duplicated) and that Stats
// agrees with a hand count of the device map.
func TestAdversarial_ConcurrentAllocateReleaseConserves(t *testing.T) {
	const k = 6
	m := NewManager()
	m.InjectGPUsForTest(makeInventory(k, 2048))

	const rounds = 400
	const jobs = 12
	var wg sync.WaitGroup
	for j := 0; j < jobs; j++ {
		wg.Add(1)
		go func(jobID string) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				if g, err := m.AllocateGPU(jobID); err == nil {
					_ = g.ID
					m.ReleaseGPU(jobID)
				}
				_ = m.ListGPUs()
				_ = m.Stats()
			}
		}("churn-" + strconv.Itoa(j))
	}
	wg.Wait()

	// Quiescent: every job released what it held; the pool must be whole.
	s := m.Stats()
	if s.Total != k {
		t.Fatalf("inventory size changed: Total=%d want %d", s.Total, k)
	}
	hand := struct{ avail, alloc, other int }{}
	for _, g := range m.ListGPUs() {
		switch g.Status {
		case Available:
			hand.avail++
		case Allocated:
			hand.alloc++
		default:
			hand.other++
		}
	}
	if hand.avail+hand.alloc+hand.other != k {
		t.Fatalf("CONSERVATION VIOLATION: counted %d GPUs, want %d", hand.avail+hand.alloc+hand.other, k)
	}
	if s.Available != hand.avail || s.Allocated != hand.alloc {
		t.Fatalf("Stats disagrees with map: Stats(avail=%d,alloc=%d) hand(avail=%d,alloc=%d)",
			s.Available, s.Allocated, hand.avail, hand.alloc)
	}
	// After all jobs released, nothing should remain allocated.
	if hand.alloc != 0 {
		t.Fatalf("LOST RELEASE: %d GPUs still allocated after every job released", hand.alloc)
	}
	t.Logf("conservation: %d available + %d allocated == %d (whole pool)", hand.avail, hand.alloc, k)
}

// TestAdversarial_SharingStateConcurrentAdmitConserves drives DeviceSharingState
// in MIG mode from many goroutines against a fixed partition budget and asserts
// the admitted count never exceeds the device MIG max (no over-subscription
// from a lost update on migPartitionsUsed).
func TestAdversarial_SharingStateConcurrentAdmitConserves(t *testing.T) {
	const migMax = 7
	const workers = 64
	s := NewDeviceSharingState("dev-0")

	var admitted int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(job string) {
			defer wg.Done()
			<-start
			req := SharingRequest{Mode: ShareMIG, JobID: job, MIGProfile: "1g.10gb"}
			if err := s.Admit(req, 0, migMax); err == nil {
				atomic.AddInt64(&admitted, 1)
			}
		}("mig-" + strconv.Itoa(w))
	}
	close(start)
	wg.Wait()

	if a := atomic.LoadInt64(&admitted); a > migMax {
		t.Fatalf("MIG OVER-SUBSCRIPTION: %d partitions admitted, device max %d", a, migMax)
	} else {
		t.Logf("MIG admission: %d/%d partitions admitted under race", a, migMax)
	}
}

// ---------------------------------------------------------------------------
// (c) DOUBLE-FREE / FREE-FOREIGN — phantom capacity
// ---------------------------------------------------------------------------

// TestAdversarial_DoubleReleaseNoPhantomCapacity allocates one GPU then releases
// it twice (and releases a never-held foreign job). The promised invariant is
// that the pool never gains phantom capacity: Available must equal K after the
// dust settles, never K+something.
func TestAdversarial_DoubleReleaseNoPhantomCapacity(t *testing.T) {
	const k = 4
	m := NewManager()
	m.InjectGPUsForTest(makeInventory(k, 512))

	if _, err := m.AllocateGPU("job-A"); err != nil {
		t.Fatalf("AllocateGPU: %v", err)
	}
	if got := m.Stats().Available; got != k-1 {
		t.Fatalf("after one alloc, Available=%d want %d", got, k-1)
	}

	// Double free of the same job.
	m.ReleaseGPU("job-A")
	m.ReleaseGPU("job-A")
	// Free a job that never held anything.
	m.ReleaseGPU("ghost-job")

	s := m.Stats()
	if s.Available != k {
		t.Fatalf("PHANTOM CAPACITY: Available=%d after double/foreign free, want exactly %d", s.Available, k)
	}
	if s.Available+s.Allocated != k {
		t.Fatalf("CONSERVATION: available(%d)+allocated(%d) != %d", s.Available, s.Allocated, k)
	}
	// SINK-SIDE: no GPU should be Available-but-marked-held or vice versa.
	for _, g := range m.ListGPUs() {
		if g.Status == Available && g.AllocatedTo != "" {
			t.Errorf("GPU %s Available but AllocatedTo=%q (stale holder)", g.ID, g.AllocatedTo)
		}
	}
	t.Logf("double/foreign free left Available=%d (== capacity %d), no phantom units", s.Available, k)
}

// TestAdversarial_SharingReleaseForeignIsError verifies that releasing an
// unknown job from a sharing state is a typed error and does not refund a
// phantom MIG partition (which would let an extra job in later).
func TestAdversarial_SharingReleaseForeignIsError(t *testing.T) {
	const migMax = 2
	s := NewDeviceSharingState("dev-1")

	// Fill the device.
	for i := 0; i < migMax; i++ {
		req := SharingRequest{Mode: ShareMIG, JobID: "real-" + strconv.Itoa(i), MIGProfile: "1g.10gb"}
		if err := s.Admit(req, 0, migMax); err != nil {
			t.Fatalf("Admit real job %d: %v", i, err)
		}
	}

	// Foreign release must be rejected, not silently refund a partition.
	if err := s.Release("never-admitted"); err == nil {
		t.Fatal("FREE-FOREIGN accepted: Release of unknown job returned nil error")
	}

	// The device must still be full: a new job must be rejected.
	extra := SharingRequest{Mode: ShareMIG, JobID: "extra", MIGProfile: "1g.10gb"}
	if err := s.Admit(extra, 0, migMax); err == nil {
		t.Fatal("PHANTOM PARTITION: a foreign Release refunded capacity; over-subscription admitted")
	}
	t.Log("foreign release rejected; no phantom partition refunded")
}

// ---------------------------------------------------------------------------
// (d) CLAUDE-2 PER-OS REAL PROBE + NUMERIC SANITY
// ---------------------------------------------------------------------------

// TestAdversarial_MonitorMetricsNumericSanity runs the real Monitor against an
// injected inventory and asserts the refreshed metrics are finite and in range:
// utilization in [0,100], temperature finite and not absurd, no NaN/Inf. A
// metric outside these bounds is a numeric defect that would mislead schedulers.
func TestAdversarial_MonitorMetricsNumericSanity(t *testing.T) {
	const k = 4
	m := NewManager()
	m.InjectGPUsForTest(makeInventory(k, 4096))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mon, err := m.StartMonitoring(ctx, WithInterval(2*time.Millisecond))
	if err != nil {
		t.Fatalf("StartMonitoring: %v", err)
	}
	// Let several refresh cycles run.
	time.Sleep(40 * time.Millisecond)
	m.StopMonitoring()
	_ = mon

	for _, g := range m.ListGPUs() {
		u := g.UtilizationPercent
		tC := g.TemperatureC
		if math.IsNaN(u) || math.IsInf(u, 0) {
			t.Errorf("GPU %s utilization is NaN/Inf: %v", g.ID, u)
		}
		if u < 0 || u > 100 {
			t.Errorf("GPU %s utilization %v out of [0,100]", g.ID, u)
		}
		if math.IsNaN(tC) || math.IsInf(tC, 0) {
			t.Errorf("GPU %s temperature is NaN/Inf: %v", g.ID, tC)
		}
		if tC < -50 || tC > 200 {
			t.Errorf("GPU %s temperature %v outside plausible [-50,200]C", g.ID, tC)
		}
	}
	t.Logf("monitor metrics for %d GPUs are finite and in range", k)
}

// TestAdversarial_PerOSProbeIsReal proves the host-OS detection probe returns
// REAL values measured against an independent OS oracle, not a fabricated
// constant — the CLAUDE-2 guarantee. It does NOT blanket-skip non-Linux: each
// OS asserts against its own oracle.
//
//   - macOS: parseSystemProfilerDisplays with the real darwinUnifiedMemoryMB must
//     equal `sysctl -n hw.memsize`/MiB for the Apple GPU; the probe shells out
//     ONCE via a 5s-bounded command (anti-hang).
//   - Linux: detectGPUsFrom against a synthetic /proc oracle must surface the
//     exact memory we wrote — proving it parses real bytes, not a constant.
//   - other: detectGPUsPlatform must return an HONEST error (no mock inventory).
func TestAdversarial_PerOSProbeIsReal(t *testing.T) {
	switch runtime.GOOS {
	case "darwin":
		// Independent OS oracle: total physical RAM in MiB.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "sysctl", "-n", "hw.memsize").Output()
		if err != nil {
			t.Skipf("sysctl hw.memsize unavailable as oracle: %v", err)
		}
		bytes, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
		if err != nil {
			t.Fatalf("parse oracle hw.memsize %q: %v", strings.TrimSpace(string(out)), err)
		}
		oracleMB := int(bytes / (1024 * 1024))
		if oracleMB <= 0 {
			t.Fatalf("oracle returned non-positive memory: %d", oracleMB)
		}

		// Drive the REAL parser with an Apple-Silicon display entry and the REAL
		// memory function. The value must equal the OS oracle exactly — proving
		// it is measured, not fabricated.
		fixture := []byte(`{"SPDisplaysDataType":[{"_name":"Apple M-series GPU","spdisplays_vendor":"Apple","sppci_cores":"10"}]}`)
		gpus, err := parseSystemProfilerDisplays(fixture, darwinUnifiedMemoryMB)
		if err != nil {
			t.Fatalf("parseSystemProfilerDisplays: %v", err)
		}
		if len(gpus) != 1 {
			t.Fatalf("expected 1 Apple GPU, got %d", len(gpus))
		}
		if gpus[0].MemoryMB != oracleMB {
			t.Fatalf("FABRICATED VALUE: probe reported %dMB but OS oracle says %dMB",
				gpus[0].MemoryMB, oracleMB)
		}
		// A fabricated constant would be the same regardless of input function;
		// prove the value tracks the injected oracle by feeding a different one.
		const sentinel = 123456
		gpus2, err := parseSystemProfilerDisplays(fixture, func() (int, error) { return sentinel, nil })
		if err != nil {
			t.Fatalf("parseSystemProfilerDisplays (sentinel): %v", err)
		}
		if gpus2[0].MemoryMB != sentinel {
			t.Fatalf("probe ignores its memory source (got %d, want sentinel %d): value is hard-coded",
				gpus2[0].MemoryMB, sentinel)
		}
		t.Logf("darwin probe == OS oracle: %dMB unified memory (real, measured)", oracleMB)

	case "linux":
		// Synthetic /proc oracle: write a known memory size and require the real
		// parser to surface exactly it (parses real bytes, not a constant).
		dir := t.TempDir()
		const wantMem = 40960
		gpuDir := dir + "/0000:01:00.0"
		if err := writeProcGPU(t, gpuDir, "NVIDIA H100", wantMem); err != nil {
			t.Fatalf("writeProcGPU: %v", err)
		}
		gpus, err := detectGPUsFrom(dir)
		if err != nil {
			t.Fatalf("detectGPUsFrom: %v", err)
		}
		if len(gpus) != 1 {
			t.Fatalf("expected 1 GPU from synthetic /proc, got %d", len(gpus))
		}
		if gpus[0].MemoryMB != wantMem {
			t.Fatalf("FABRICATED VALUE: parser reported %dMB but /proc oracle wrote %dMB",
				gpus[0].MemoryMB, wantMem)
		}
		t.Logf("linux probe == /proc oracle: %dMB (real, parsed)", wantMem)

	default:
		// CLAUDE-2: an unsupported OS must HONESTLY error, never fabricate.
		if _, err := detectGPUsPlatform(); err == nil {
			t.Fatalf("CLAUDE-2 PASS-BLUFF: detectGPUsPlatform returned nil error (fabricated inventory) on %s", runtime.GOOS)
		}
		t.Logf("unsupported OS %s honestly errors (no mock inventory)", runtime.GOOS)
	}
}
