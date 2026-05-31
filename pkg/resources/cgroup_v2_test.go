package resources

import (
	"embed"
	"io/fs"
	"testing"
)

//go:embed testdata/cgroup_limited testdata/cgroup_unlimited testdata/cgroup_malformed
var cgroupFixtures embed.FS

// fixtureFS returns a sub-FS rooted at the given testdata directory so the
// reader sees cgroup files (cpu.max, memory.max, ...) at its root, exactly as
// they would appear under /sys/fs/cgroup/<group> on a real Linux host.
func fixtureFS(t *testing.T, dir string) fs.FS {
	t.Helper()
	sub, err := fs.Sub(cgroupFixtures, "testdata/"+dir)
	if err != nil {
		t.Fatalf("fs.Sub(%q): %v", dir, err)
	}
	return sub
}

// ---------- cpu.max parsing ----------

func TestCgroupV2_ReadCPUMax_Quota(t *testing.T) {
	r := NewCgroupV2Reader(fixtureFS(t, "cgroup_limited"), ".")
	cm, err := r.ReadCPUMax()
	if err != nil {
		t.Fatalf("ReadCPUMax: %v", err)
	}
	if cm.Unlimited {
		t.Fatal("expected limited cpu.max, got Unlimited=true")
	}
	if cm.QuotaUsec != 50000 {
		t.Errorf("QuotaUsec = %d, want 50000", cm.QuotaUsec)
	}
	if cm.PeriodUsec != 100000 {
		t.Errorf("PeriodUsec = %d, want 100000", cm.PeriodUsec)
	}
	// Mutation-paired: "50000 100000" MUST derive exactly 0.5 cores.
	if got := cm.Cores(); got != 0.5 {
		t.Errorf("Cores() = %v, want 0.5", got)
	}
}

func TestCgroupV2_ReadCPUMax_Unlimited(t *testing.T) {
	r := NewCgroupV2Reader(fixtureFS(t, "cgroup_unlimited"), ".")
	cm, err := r.ReadCPUMax()
	if err != nil {
		t.Fatalf("ReadCPUMax: %v", err)
	}
	// Mutation-paired: the "max" sentinel MUST mean unlimited, NOT 0.
	if !cm.Unlimited {
		t.Fatal("expected Unlimited=true for cpu.max sentinel \"max\"")
	}
	if cm.Cores() != 0 {
		t.Errorf("Cores() for unlimited = %v, want 0 (no cap)", cm.Cores())
	}
}

func TestCgroupV2_ReadCPUMax_Malformed(t *testing.T) {
	r := NewCgroupV2Reader(fixtureFS(t, "cgroup_malformed"), ".")
	_, err := r.ReadCPUMax()
	// Mutation-paired: a malformed quota MUST be rejected with an error, not
	// silently parsed as 0.
	if err == nil {
		t.Fatal("expected error for malformed cpu.max, got nil")
	}
}

func TestCgroupV2_ReadCPUMax_Missing(t *testing.T) {
	// Empty FS: cpu.max does not exist.
	r := NewCgroupV2Reader(fstestEmpty{}, ".")
	_, err := r.ReadCPUMax()
	if err == nil {
		t.Fatal("expected error for missing cpu.max")
	}
}

// ---------- cpu.stat parsing ----------

func TestCgroupV2_ReadCPUStat(t *testing.T) {
	r := NewCgroupV2Reader(fixtureFS(t, "cgroup_limited"), ".")
	st, err := r.ReadCPUStat()
	if err != nil {
		t.Fatalf("ReadCPUStat: %v", err)
	}
	if st.UsageUsec != 123456789 {
		t.Errorf("UsageUsec = %d, want 123456789", st.UsageUsec)
	}
	if st.UserUsec != 80000000 {
		t.Errorf("UserUsec = %d, want 80000000", st.UserUsec)
	}
	if st.SystemUsec != 43456789 {
		t.Errorf("SystemUsec = %d, want 43456789", st.SystemUsec)
	}
	if st.NrThrottled != 12 {
		t.Errorf("NrThrottled = %d, want 12", st.NrThrottled)
	}
	if st.ThrottledUsec != 345678 {
		t.Errorf("ThrottledUsec = %d, want 345678", st.ThrottledUsec)
	}
}

// ---------- memory.current / memory.max ----------

func TestCgroupV2_ReadMemoryCurrent(t *testing.T) {
	r := NewCgroupV2Reader(fixtureFS(t, "cgroup_limited"), ".")
	cur, err := r.ReadMemoryCurrent()
	if err != nil {
		t.Fatalf("ReadMemoryCurrent: %v", err)
	}
	if cur != 536870912 {
		t.Errorf("memory.current = %d, want 536870912", cur)
	}
}

func TestCgroupV2_ReadMemoryMax_Limited(t *testing.T) {
	r := NewCgroupV2Reader(fixtureFS(t, "cgroup_limited"), ".")
	mm, err := r.ReadMemoryMax()
	if err != nil {
		t.Fatalf("ReadMemoryMax: %v", err)
	}
	if mm.Unlimited {
		t.Fatal("expected limited memory.max")
	}
	if mm.Bytes != 2147483648 {
		t.Errorf("memory.max bytes = %d, want 2147483648", mm.Bytes)
	}
}

func TestCgroupV2_ReadMemoryMax_Unlimited(t *testing.T) {
	r := NewCgroupV2Reader(fixtureFS(t, "cgroup_unlimited"), ".")
	mm, err := r.ReadMemoryMax()
	if err != nil {
		t.Fatalf("ReadMemoryMax: %v", err)
	}
	// Mutation-paired: "max" => Unlimited, bytes MUST be 0 (no limit), not a
	// bogus parse.
	if !mm.Unlimited {
		t.Fatal("expected Unlimited=true for memory.max sentinel \"max\"")
	}
	if mm.Bytes != 0 {
		t.Errorf("memory.max bytes = %d, want 0 for unlimited", mm.Bytes)
	}
}

func TestCgroupV2_ReadMemoryMax_Overflow(t *testing.T) {
	r := NewCgroupV2Reader(fixtureFS(t, "cgroup_malformed"), ".")
	_, err := r.ReadMemoryMax()
	// Mutation-paired: a value that overflows int64 MUST error, not wrap.
	if err == nil {
		t.Fatal("expected overflow error for memory.max")
	}
}

func TestCgroupV2_ReadMemoryCurrent_Negative(t *testing.T) {
	r := NewCgroupV2Reader(fixtureFS(t, "cgroup_malformed"), ".")
	_, err := r.ReadMemoryCurrent()
	// Mutation-paired: a negative byte count is invalid and MUST be rejected,
	// not accepted as -5.
	if err == nil {
		t.Fatal("expected error for negative memory.current")
	}
}

// ---------- io.stat ----------

func TestCgroupV2_ReadIOStat(t *testing.T) {
	r := NewCgroupV2Reader(fixtureFS(t, "cgroup_limited"), ".")
	devs, err := r.ReadIOStat()
	if err != nil {
		t.Fatalf("ReadIOStat: %v", err)
	}
	if len(devs) != 2 {
		t.Fatalf("expected 2 devices, got %d", len(devs))
	}
	d, ok := devs["259:0"]
	if !ok {
		t.Fatal("expected device 259:0")
	}
	if d.RBytes != 1048576 {
		t.Errorf("259:0 rbytes = %d, want 1048576", d.RBytes)
	}
	if d.WBytes != 2097152 {
		t.Errorf("259:0 wbytes = %d, want 2097152", d.WBytes)
	}
	if d.RIOs != 100 || d.WIOs != 200 {
		t.Errorf("259:0 rios/wios = %d/%d, want 100/200", d.RIOs, d.WIOs)
	}
}

func TestCgroupV2_ReadIOStat_Missing(t *testing.T) {
	// The unlimited fixture deliberately has no io.stat. Missing io.stat is
	// not fatal: an empty map and nil error are returned.
	r := NewCgroupV2Reader(fixtureFS(t, "cgroup_unlimited"), ".")
	devs, err := r.ReadIOStat()
	if err != nil {
		t.Fatalf("expected nil error for missing io.stat, got %v", err)
	}
	if len(devs) != 0 {
		t.Errorf("expected 0 devices for missing io.stat, got %d", len(devs))
	}
}

// ---------- aggregate ReadStats ----------

func TestCgroupV2_ReadStats_Limited(t *testing.T) {
	r := NewCgroupV2Reader(fixtureFS(t, "cgroup_limited"), ".")
	s, err := r.ReadStats()
	if err != nil {
		t.Fatalf("ReadStats: %v", err)
	}
	if s.CPUMax.Cores() != 0.5 {
		t.Errorf("CPUMax.Cores() = %v, want 0.5", s.CPUMax.Cores())
	}
	if s.MemoryCurrent != 536870912 {
		t.Errorf("MemoryCurrent = %d, want 536870912", s.MemoryCurrent)
	}
	if s.MemoryMax.Bytes != 2147483648 {
		t.Errorf("MemoryMax.Bytes = %d, want 2147483648", s.MemoryMax.Bytes)
	}
	if s.CPUStat.UsageUsec != 123456789 {
		t.Errorf("CPUStat.UsageUsec = %d, want 123456789", s.CPUStat.UsageUsec)
	}
	if len(s.IO) != 2 {
		t.Errorf("len(IO) = %d, want 2", len(s.IO))
	}
}

func TestCgroupV2_ReadStats_Unlimited(t *testing.T) {
	r := NewCgroupV2Reader(fixtureFS(t, "cgroup_unlimited"), ".")
	s, err := r.ReadStats()
	if err != nil {
		t.Fatalf("ReadStats: %v", err)
	}
	if !s.CPUMax.Unlimited {
		t.Error("expected CPUMax.Unlimited")
	}
	if !s.MemoryMax.Unlimited {
		t.Error("expected MemoryMax.Unlimited")
	}
	if len(s.IO) != 0 {
		t.Errorf("len(IO) = %d, want 0 (missing io.stat)", len(s.IO))
	}
}

// ---------- aggregator integration ----------

func TestCgroupV2_ReaderIntegratesWithAggregator(t *testing.T) {
	r := NewCgroupV2Reader(fixtureFS(t, "cgroup_limited"), ".")
	res, err := r.Read("node-cg")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if res.NodeID != "node-cg" {
		t.Errorf("NodeID = %q, want node-cg", res.NodeID)
	}
	// 0.5 cores cap => UsedCores reflects the derived cap.
	if res.CPU.UsedCores != 0.5 {
		t.Errorf("CPU.UsedCores = %v, want 0.5", res.CPU.UsedCores)
	}
	// memory.max 2 GiB => TotalKB = 2147483648/1024 = 2097152.
	if res.Memory.TotalKB != 2097152 {
		t.Errorf("Memory.TotalKB = %d, want 2097152", res.Memory.TotalKB)
	}
	// memory.current 512 MiB => UsedKB = 536870912/1024 = 524288.
	if res.Memory.UsedKB != 524288 {
		t.Errorf("Memory.UsedKB = %d, want 524288", res.Memory.UsedKB)
	}

	// Verify it satisfies the Reader interface and merges in the aggregator.
	var _ Reader = r
	agg := NewNodeAggregator(5 * 60 * 1e9)
	agg.RegisterReader(r)
	agg.RegisterNode("node-cg")
	if err := agg.Collect(); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	snap, ok := agg.GetNode("node-cg")
	if !ok {
		t.Fatal("expected node-cg snapshot")
	}
	if snap.Resources.Memory.TotalKB != 2097152 {
		t.Errorf("aggregated TotalKB = %d, want 2097152", snap.Resources.Memory.TotalKB)
	}
}

// fstestEmpty is an fs.FS with no files, used to exercise missing-file paths.
type fstestEmpty struct{}

func (fstestEmpty) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}
