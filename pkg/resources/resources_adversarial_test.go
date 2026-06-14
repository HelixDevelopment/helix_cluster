package resources

// Adversarial sweep for pkg/resources — the LAST pkg/ package.
//
// This file is intentionally NON-BLOCKING. The package's host probes shell out
// to sysctl/vm_stat/top/system_profiler. A prior sweep HUNG by fanning out many
// concurrent probe calls (each forks a subprocess) or by waiting on a goroutine
// with no deadline. To avoid that, this test:
//
//   - calls the real darwin probe ONCE (not in a loop, not in a waited goroutine);
//   - reads its OWN OS oracle via exec.CommandContext with a 5s timeout and the
//     context attached, never a bare exec.Command(...).Output();
//   - caps any concurrency at <= 8 with a per-goroutine deadline;
//   - exercises the numeric / byte-accounting / divide-by-zero surfaces purely
//     in-process (no subprocess) by constructing inputs directly.
//
// SINK-SIDE assertions: a real measured value compared to an independent OS
// oracle within tolerance, an actual percentage/byte figure asserted in range —
// never merely "no panic".

import (
	"context"
	"math"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

// mapFSCgroup builds an in-memory cgroup v2 fixture FS carrying the controller
// files CgroupV2Reader.ReadStats requires (cpu.max, cpu.stat, memory.current,
// memory.max). io.stat is intentionally omitted — it is optional and the reader
// treats its absence as an empty map. memMax/memCur are the values under test.
func mapFSCgroup(memMax, memCur string) fstest.MapFS {
	return fstest.MapFS{
		"cpu.max":        {Data: []byte("max 100000\n")},
		"cpu.stat":       {Data: []byte("usage_usec 0\n")},
		"memory.current": {Data: []byte(memCur + "\n")},
		"memory.max":     {Data: []byte(memMax + "\n")},
	}
}

// oracleSysctl runs `sysctl -n <key>` under a bounded context so it can NEVER
// block the test (anti-hang). It is the independent oracle the darwin reader is
// measured against.
func oracleSysctl(t *testing.T, key string) (string, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sysctl", "-n", key).Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// ----------------------------------------------------------------------------
// (a) CLAUDE-2 parity: the darwin path returns REAL measured values, not a stub.
// ----------------------------------------------------------------------------

// TestAdversarial_DarwinMemInfo_MatchesSysctlOracle proves ReadMemInfo on macOS
// returns the REAL total physical RAM (from hw.memsize) — a mock/stub/hardcoded
// value would diverge from the kernel oracle. Single probe call; no loop.
func TestAdversarial_DarwinMemInfo_MatchesSysctlOracle(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		// Genuinely-no-equivalent platforms use proc_mock.go (a development stub
		// behind !linux && !darwin). That is the documented, justified fallback
		// for unsupported OSes, not a hidden port — see CLAUDE-2 §3.
		t.Skipf("no real proc reader on %s (mock fallback is the documented contract)", runtime.GOOS)
	}
	if runtime.GOOS != "darwin" {
		t.Skip("sysctl hw.memsize oracle is macOS-specific; linux verified by its own oracle test")
	}

	oracleStr, ok := oracleSysctl(t, "hw.memsize")
	if !ok {
		t.Skip("sysctl oracle unavailable")
	}
	oracleBytes, err := strconv.ParseInt(oracleStr, 10, 64)
	if err != nil || oracleBytes <= 0 {
		t.Fatalf("bad oracle hw.memsize %q: %v", oracleStr, err)
	}

	// REAL probe — exactly once.
	mem, err := NewDarwinReader().ReadMemInfo()
	if err != nil {
		t.Fatalf("ReadMemInfo: %v", err)
	}

	// hw.memsize is exact; the reader reports TotalKB = bytes/1024. A stub would
	// not equal the kernel value.
	wantKB := oracleBytes / 1024
	if mem.TotalKB != wantKB {
		t.Fatalf("CLAUDE-2 stub suspicion: TotalKB=%d, sysctl oracle=%d KB (delta=%d)",
			mem.TotalKB, wantKB, mem.TotalKB-wantKB)
	}

	// Byte-accounting invariants on the REAL reading: used+available must not
	// exceed total, and none may go negative.
	if mem.UsedKB < 0 || mem.AvailableKB < 0 || mem.TotalKB < 0 {
		t.Fatalf("negative memory field: total=%d used=%d avail=%d",
			mem.TotalKB, mem.UsedKB, mem.AvailableKB)
	}
	if mem.UsedKB > mem.TotalKB {
		t.Fatalf("UsedKB %d exceeds TotalKB %d", mem.UsedKB, mem.TotalKB)
	}
	if mem.AvailableKB > mem.TotalKB {
		t.Fatalf("AvailableKB %d exceeds TotalKB %d", mem.AvailableKB, mem.TotalKB)
	}
	t.Logf("OK darwin mem: total=%d KB (oracle %d KB) used=%d avail=%d",
		mem.TotalKB, wantKB, mem.UsedKB, mem.AvailableKB)
}

// TestAdversarial_DarwinCPUInfo_MatchesSysctlOracle proves ReadCPUInfo returns
// the REAL logical CPU count (hw.ncpu) and a UsedCores figure that stays within
// [0, Cores] — a utilization that exceeds the core count or goes negative would
// be a numeric leak. Single probe call.
func TestAdversarial_DarwinCPUInfo_MatchesSysctlOracle(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("hw.ncpu sysctl oracle is macOS-specific")
	}
	oracleStr, ok := oracleSysctl(t, "hw.ncpu")
	if !ok {
		t.Skip("sysctl oracle unavailable")
	}
	oracleNCPU, err := strconv.Atoi(oracleStr)
	if err != nil || oracleNCPU <= 0 {
		t.Fatalf("bad oracle hw.ncpu %q: %v", oracleStr, err)
	}

	cpu, err := NewDarwinReader().ReadCPUInfo()
	if err != nil {
		t.Fatalf("ReadCPUInfo: %v", err)
	}
	if cpu.Cores != oracleNCPU {
		t.Fatalf("CLAUDE-2 stub suspicion: Cores=%d, sysctl hw.ncpu oracle=%d",
			cpu.Cores, oracleNCPU)
	}
	// UsedCores is sampled from `top` (real interval). It must be a sane fraction
	// of the core count — never NaN/Inf, never negative, never above the count.
	if math.IsNaN(cpu.UsedCores) || math.IsInf(cpu.UsedCores, 0) {
		t.Fatalf("UsedCores is NaN/Inf: %v", cpu.UsedCores)
	}
	if cpu.UsedCores < 0 || cpu.UsedCores > float64(cpu.Cores) {
		t.Fatalf("UsedCores %.4f out of [0,%d]", cpu.UsedCores, cpu.Cores)
	}
	t.Logf("OK darwin cpu: cores=%d (oracle %d) used=%.3f model=%q",
		cpu.Cores, oracleNCPU, cpu.UsedCores, cpu.Model)
}

// TestAdversarial_DarwinGPUTFLOPS_DerivedFromRealCores proves the darwin GPU
// TFLOPS figure is DERIVED from the host's real Apple GPU core count (it scales
// with cores), not a hardcoded constant. It compares the probe output to the
// formula applied to the core count system_profiler honestly reports. This is
// the CLAUDE-2 sink-side check for accel_darwin.go.
//
// Non-blocking: system_profiler is invoked exactly once via the package's own
// injectable accessor (a single subprocess), with a hard skip if it is slow or
// returns no Apple GPU (e.g. a discrete-only / headless host).
func TestAdversarial_DarwinGPUTFLOPS_DerivedFromRealCores(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Apple GPU core-count TFLOPS derivation is macOS-specific")
	}

	// Run the real system_profiler accessor ONCE, in a goroutine guarded by a
	// deadline so a hung tool can never hang the test.
	type spResult struct {
		raw string
		err error
	}
	ch := make(chan spResult, 1)
	go func() {
		raw, err := darwinSystemProfilerFn()
		ch <- spResult{raw, err}
	}()

	var raw string
	select {
	case r := <-ch:
		if r.err != nil {
			t.Skipf("system_profiler unavailable: %v", r.err)
		}
		raw = r.raw
	case <-time.After(8 * time.Second):
		t.Skip("system_profiler too slow; skipping live GPU derivation (anti-hang)")
	}

	chipset, cores, ok := parseAppleGPU(raw)
	if !ok || cores <= 0 {
		t.Skipf("no Apple integrated GPU with a positive core count on this host (chipset=%q)", chipset)
	}

	// The probe credits the host integrated GPU when given its own chipset.
	got, ok := probeGPUTFLOPS(GPUInfo{Model: chipset})
	if !ok {
		t.Fatalf("probeGPUTFLOPS returned no value for host chipset %q (cores=%d)", chipset, cores)
	}
	want := tflopsFromAppleCores(cores)

	// SINK-SIDE: the reported figure must equal the formula applied to the REAL
	// core count — proving it is derived, not constant.
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("TFLOPS not derived from real cores: got=%.4f want=%.4f (cores=%d)",
			got, want, cores)
	}
	if got <= 0 || math.IsNaN(got) || math.IsInf(got, 0) {
		t.Fatalf("derived TFLOPS not a sane positive number: %v", got)
	}

	// Prove it SCALES with cores (not a fixed constant): one more core yields a
	// strictly larger figure, and the delta equals the per-core contribution.
	perCore := tflopsFromAppleCores(cores+1) - want
	if perCore <= 0 {
		t.Fatalf("TFLOPS does not increase with core count (per-core delta=%.6f)", perCore)
	}
	t.Logf("OK darwin GPU TFLOPS: chipset=%q cores=%d tflops=%.3f (per-core=%.4f)",
		chipset, cores, got, perCore)
}

// ----------------------------------------------------------------------------
// (b) NUMERIC: divide-by-zero, NaN/Inf, >100%, byte overflow — all in-process.
// ----------------------------------------------------------------------------

// TestAdversarial_CPUMaxCores_NoDivideByZero asserts CPUMax.Cores never produces
// NaN/Inf when the period is zero or the group is unlimited, and never returns a
// negative core count. Pure in-process; no subprocess.
func TestAdversarial_CPUMaxCores_NoDivideByZero(t *testing.T) {
	cases := []struct {
		name string
		c    CPUMax
	}{
		{"zero-period", CPUMax{QuotaUsec: 50000, PeriodUsec: 0}},
		{"unlimited", CPUMax{Unlimited: true, QuotaUsec: 50000, PeriodUsec: 100000}},
		{"zero-quota", CPUMax{QuotaUsec: 0, PeriodUsec: 100000}},
		{"negative-period", CPUMax{QuotaUsec: 50000, PeriodUsec: -1}},
		{"normal", CPUMax{QuotaUsec: 150000, PeriodUsec: 100000}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.c.Cores()
			if math.IsNaN(got) || math.IsInf(got, 0) {
				t.Fatalf("Cores() leaked NaN/Inf for %+v: %v", tc.c, got)
			}
			if got < 0 {
				t.Fatalf("Cores() negative for %+v: %v", tc.c, got)
			}
		})
	}
	// Sink-side value check on the normal case: 150000/100000 == 1.5 cores.
	if got := (CPUMax{QuotaUsec: 150000, PeriodUsec: 100000}).Cores(); math.Abs(got-1.5) > 1e-9 {
		t.Fatalf("normal CPUMax.Cores: got %.4f want 1.5", got)
	}
}

// TestAdversarial_CgroupMemoryAccounting_NeverNegative drives the byte-accounting
// path in CgroupV2Reader.Read with adversarial fixtures: memory.current GREATER
// than memory.max (over-limit, which the kernel can transiently report), and a
// near-int64-max memory.max (overflow probe). AvailableKB must clamp at 0, never
// go negative, and used/total must stay consistent. In-process via an fstest FS.
func TestAdversarial_CgroupMemoryAccounting_NeverNegative(t *testing.T) {
	build := func(memMax, memCur string) *CgroupV2Reader {
		fsys := mapFSCgroup(memMax, memCur)
		return NewCgroupV2Reader(fsys, ".")
	}

	cases := []struct {
		name           string
		memMax, memCur string
	}{
		// current > max: the dangerous case for availableKB = total - used.
		{"over-limit", "1048576", "4194304"},            // 1 MiB cap, 4 MiB used
		{"equal", "2097152", "2097152"},                 // used == cap
		{"normal", "8388608", "2097152"},                // 8 MiB cap, 2 MiB used
		{"huge-cap", "9223372036854775807", "1048576"},  // int64-max cap (overflow probe)
		{"unlimited", "max", "1048576"},                 // no cap
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := build(tc.memMax, tc.memCur).Read("n1")
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			m := res.Memory
			if m.AvailableKB < 0 {
				t.Fatalf("AvailableKB negative: %d (total=%d used=%d)", m.AvailableKB, m.TotalKB, m.UsedKB)
			}
			if m.TotalKB < 0 || m.UsedKB < 0 {
				t.Fatalf("negative total/used: total=%d used=%d", m.TotalKB, m.UsedKB)
			}
			// When a finite cap is present, available = total - used clamped at 0.
			if tc.memMax != "max" {
				wantTotal, _ := strconv.ParseInt(tc.memMax, 10, 64)
				wantTotalKB := wantTotal / 1024
				if m.TotalKB != wantTotalKB {
					t.Fatalf("TotalKB=%d want %d", m.TotalKB, wantTotalKB)
				}
				wantAvail := wantTotalKB - m.UsedKB
				if wantAvail < 0 {
					wantAvail = 0
				}
				if m.AvailableKB != wantAvail {
					t.Fatalf("AvailableKB=%d want %d (clamp)", m.AvailableKB, wantAvail)
				}
			}
			t.Logf("OK cgroup mem[%s]: total=%d used=%d avail=%d", tc.name, m.TotalKB, m.UsedKB, m.AvailableKB)
		})
	}
}

// TestAdversarial_GPUInfoFromDevices_VRAMOverflow checks the byte->MiB VRAM sum
// across many large-VRAM cards does not overflow into a negative Memory value
// and that the SUM (not max/first) is reported. Pure in-process.
func TestAdversarial_GPUInfoFromDevices_VRAMSumNotOverflow(t *testing.T) {
	// 8 cards of 80 GiB each = 640 GiB; well within int64 but a real stress on
	// the byte->MiB sum. A buggy accumulator (e.g. int32 or per-card max) would
	// diverge from the expected sum.
	const perCard = int64(80) * 1024 * 1024 * 1024 // 80 GiB in bytes
	devs := make([]GPUDevice, 0, 8)
	for i := 0; i < 8; i++ {
		devs = append(devs, GPUDevice{
			Card:      "card" + strconv.Itoa(i),
			VendorID:  "0x10de",
			DeviceID:  "0x2330",
			VRAMBytes: perCard,
		})
	}
	info := GPUInfoFromDevices(devs)
	if info.Count != 8 {
		t.Fatalf("Count=%d want 8", info.Count)
	}
	wantMiB := (perCard * 8) / (1024 * 1024)
	if info.Memory != wantMiB {
		t.Fatalf("VRAM sum wrong/overflowed: Memory=%d MiB want %d MiB", info.Memory, wantMiB)
	}
	if info.Memory < 0 {
		t.Fatalf("VRAM Memory negative (overflow): %d", info.Memory)
	}
}

// ----------------------------------------------------------------------------
// (c) UNGUARDED SHARED STATE: aggregator cache map under CAPPED concurrency.
// ----------------------------------------------------------------------------

// fakeReader is an in-process Reader (NO subprocess) returning a fixed snapshot.
// Used so the concurrency test never forks a process per goroutine.
type fakeReader struct{ cores int }

func (f fakeReader) Read(nodeID string) (NodeResources, error) {
	return NodeResources{
		NodeID: nodeID,
		CPU:    CPUInfo{Cores: f.cores, UsedCores: float64(f.cores) * 0.5},
		Memory: MemoryInfo{TotalKB: 1024, AvailableKB: 512, UsedKB: 512},
	}, nil
}

// TestAdversarial_AggregatorConcurrency_RaceClean hammers the NodeAggregator's
// nodes map and per-node state from concurrent Collect / GetNode / ListNodes /
// Register / Deregister callers. With -race this surfaces any unguarded shared
// state in the cache. CAPPED at 8 goroutines, each with a deadline (anti-hang),
// and the readers are in-process fakes (no subprocess fan-out).
func TestAdversarial_AggregatorConcurrency_RaceClean(t *testing.T) {
	agg := NewNodeAggregator(50 * time.Millisecond)
	agg.RegisterReader(fakeReader{cores: 8})
	for i := 0; i < 4; i++ {
		agg.RegisterNode("node" + strconv.Itoa(i))
	}

	const workers = 8 // cap per anti-hang guidance
	deadline := time.Now().Add(2 * time.Second)
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(id int) {
			defer wg.Done()
			i := 0
			for time.Now().Before(deadline) {
				switch (id + i) % 5 {
				case 0:
					_ = agg.Collect()
				case 1:
					_, _ = agg.GetNode("node1")
				case 2:
					_ = agg.ListNodes()
				case 3:
					agg.RegisterNode("dyn" + strconv.Itoa(id))
				case 4:
					agg.DeregisterNode("dyn" + strconv.Itoa(id))
				}
				i++
				if i%256 == 0 {
					runtime.Gosched()
				}
			}
		}(w)
	}

	// Hard backstop: never wait past the deadline + slack.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("aggregator concurrency test exceeded backstop (possible deadlock)")
	}

	// Sink-side: after the storm a Collect + GetNode still returns a coherent
	// snapshot for a stable node (the cache survived concurrent mutation).
	if err := agg.Collect(); err != nil {
		t.Fatalf("final Collect: %v", err)
	}
	snap, ok := agg.GetNode("node1")
	if !ok {
		t.Fatal("node1 missing after concurrency storm")
	}
	if snap.Resources.CPU.Cores != 8 {
		t.Fatalf("node1 cores=%d want 8 (state corrupted)", snap.Resources.CPU.Cores)
	}
}
