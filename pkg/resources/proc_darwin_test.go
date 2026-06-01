//go:build darwin

package resources

import (
	"embed"
	"io/fs"
	"strconv"
	"strings"
	"testing"
)

//go:embed testdata/darwin
var darwinFixtures embed.FS

// darwinFixtureStr reads a file from the embedded darwin fixtures and returns
// its trimmed string contents. Fatal on missing file so test failures are clear.
func darwinFixtureStr(t *testing.T, name string) string {
	t.Helper()
	b, err := fs.ReadFile(darwinFixtures, "testdata/darwin/"+name)
	if err != nil {
		t.Fatalf("darwinFixture %q: %v", name, err)
	}
	return strings.TrimSpace(string(b))
}

// ---------- parseVMStat unit tests ----------

// Golden values derived from testdata/darwin/vm_stat:
//   Pages free:        10000
//   Pages inactive:   150000
//   Pages speculative:  5000
//   Total freePages = 165000
//   Page size = 16384 bytes
//   Available bytes = 165000 * 16384 = 2,703,360,000 bytes

const vmStatFixturePageSize = int64(16384)
const vmStatFixtureFreePages = int64(10000 + 150000 + 5000) // 165000

func TestParseVMStat_GoldenFixture(t *testing.T) {
	raw := darwinFixtureStr(t, "vm_stat")
	// vm_stat fixture has a trailing newline which is fine for the scanner.
	pageSize, freePages, err := parseVMStat(raw + "\n")
	if err != nil {
		t.Fatalf("parseVMStat: %v", err)
	}
	// Mutation-paired: changing the sum formula (e.g. omitting "speculative")
	// would change freePages, failing this exact assertion.
	if pageSize != vmStatFixturePageSize {
		t.Errorf("pageSize = %d, want %d", pageSize, vmStatFixturePageSize)
	}
	if freePages != vmStatFixtureFreePages {
		t.Errorf("freePages = %d, want %d (free+inactive+speculative)", freePages, vmStatFixtureFreePages)
	}
	// Derived available bytes must equal freePages * pageSize exactly.
	wantAvailBytes := vmStatFixtureFreePages * vmStatFixturePageSize
	gotAvailBytes := freePages * pageSize
	if gotAvailBytes != wantAvailBytes {
		t.Errorf("availableBytes = %d, want %d", gotAvailBytes, wantAvailBytes)
	}
}

// Mutation-paired: if the page size parser ignores the header, pageSize == 0
// and every caller gets divide-by-zero or wrong memory. Verify the header is
// mandatory.
func TestParseVMStat_MissingHeader_Errors(t *testing.T) {
	// A vm_stat body without the header line.
	raw := "Pages free:                               10000.\nPages inactive:                          150000.\n"
	_, _, err := parseVMStat(raw)
	if err == nil {
		t.Fatal("expected error when vm_stat header is missing, got nil")
	}
	if !strings.Contains(err.Error(), "page size not found") {
		t.Errorf("error = %q; want mention of page size not found", err.Error())
	}
}

// Mutation-paired: a garbled page size value must be rejected, not coerced to 0.
func TestParseVMStat_GarbledPageSize_Errors(t *testing.T) {
	raw := "Mach Virtual Memory Statistics: (page size of GARBAGE bytes)\nPages free: 100.\n"
	_, _, err := parseVMStat(raw)
	if err == nil {
		t.Fatal("expected error for garbled page size, got nil")
	}
}

// Mutation-paired: a negative page count must be rejected, not silently summed.
func TestParseVMStat_NegativeFreePages_Errors(t *testing.T) {
	raw := "Mach Virtual Memory Statistics: (page size of 4096 bytes)\nPages free:                               -100.\n"
	_, _, err := parseVMStat(raw)
	if err == nil {
		t.Fatal("expected error for negative Pages free count, got nil")
	}
}

// Verify that "Pages inactive" and "Pages speculative" each contribute to the
// sum independently. Mutation: commenting out either accumulation in the switch
// would change the result below.
func TestParseVMStat_AllThreeCategories_Summed(t *testing.T) {
	raw := "Mach Virtual Memory Statistics: (page size of 4096 bytes)\n" +
		"Pages free:                                1000.\n" +
		"Pages inactive:                            2000.\n" +
		"Pages speculative:                          500.\n" +
		"Pages wired down:                          3000.\n" // must NOT be counted
	pageSize, freePages, err := parseVMStat(raw)
	if err != nil {
		t.Fatalf("parseVMStat: %v", err)
	}
	if pageSize != 4096 {
		t.Errorf("pageSize = %d, want 4096", pageSize)
	}
	wantFree := int64(1000 + 2000 + 500)
	if freePages != wantFree {
		t.Errorf("freePages = %d, want %d (free+inactive+speculative only)", freePages, wantFree)
	}
}

// ---------- DarwinReader unit tests using injected fakes ----------

// makeSysctlFake returns a sysctl fake that serves fixed values for specific
// keys and errors for any unknown key.
func makeSysctlFake(vals map[string]string) func(key string) (string, error) {
	return func(key string) (string, error) {
		if v, ok := vals[key]; ok {
			return v, nil
		}
		return "", nil // unknown keys return empty (like machdep.cpu.brand_string on Apple Silicon)
	}
}

// makeVMStatFake returns a vm_stat fake serving the given raw output.
func makeVMStatFake(raw string) func() (string, error) {
	return func() (string, error) { return raw, nil }
}

// Golden fixture values:
//   hw.memsize = 17179869184 (16 GiB)
//   hw.ncpu    = 8
//   vm_stat: page size 16384, freePages 165000
//     → availableBytes = 165000 * 16384 = 2703360000
//     → availableKB    = 2703360000 / 1024 = 2640000
//     → totalKB        = 17179869184 / 1024 = 16777216
//     → usedKB         = 16777216 - 2640000 = 14137216

const darwinFixtureTotalKB = int64(17179869184 / 1024) // 16777216
const darwinFixtureAvailKB = int64(2703360000 / 1024)  // 2640000 (165000 * 16384 / 1024)
const darwinFixtureUsedKB = darwinFixtureTotalKB - darwinFixtureAvailKB

func TestDarwinReader_ReadMemInfo_GoldenFixture(t *testing.T) {
	memsizeRaw := darwinFixtureStr(t, "sysctl_memsize") // "17179869184"
	vmstatRaw := darwinFixtureStr(t, "vm_stat")

	r := newDarwinReaderWithFakes(
		makeSysctlFake(map[string]string{"hw.memsize": memsizeRaw}),
		makeVMStatFake(vmstatRaw+"\n"),
	)
	mem, err := r.ReadMemInfo()
	if err != nil {
		t.Fatalf("ReadMemInfo: %v", err)
	}

	// Mutation-paired: if the sysctl parse is skipped (totalKB left 0), this
	// assertion fails. If the vm_stat sum excludes "inactive", availKB changes.
	if mem.TotalKB != darwinFixtureTotalKB {
		t.Errorf("TotalKB = %d, want %d (hw.memsize/1024)", mem.TotalKB, darwinFixtureTotalKB)
	}
	if mem.AvailableKB != darwinFixtureAvailKB {
		t.Errorf("AvailableKB = %d, want %d (freePages*pageSize/1024)", mem.AvailableKB, darwinFixtureAvailKB)
	}
	if mem.UsedKB != darwinFixtureUsedKB {
		t.Errorf("UsedKB = %d, want %d (total-available)", mem.UsedKB, darwinFixtureUsedKB)
	}
	// Invariant: total = used + available (within 1 KiB rounding tolerance).
	if mem.TotalKB != mem.UsedKB+mem.AvailableKB {
		t.Errorf("invariant broken: TotalKB(%d) != UsedKB(%d) + AvailableKB(%d)",
			mem.TotalKB, mem.UsedKB, mem.AvailableKB)
	}
}

// Mutation-paired: zeroing the sysctl hw.memsize parse (e.g. returning 0) must
// fail the TotalKB assertion above AND this error-path test.
func TestDarwinReader_ReadMemInfo_InvalidMemsize_Errors(t *testing.T) {
	r := newDarwinReaderWithFakes(
		makeSysctlFake(map[string]string{"hw.memsize": "not-a-number"}),
		makeVMStatFake("Mach Virtual Memory Statistics: (page size of 4096 bytes)\nPages free: 100.\n"),
	)
	_, err := r.ReadMemInfo()
	if err == nil {
		t.Fatal("expected error for non-numeric hw.memsize, got nil")
	}
}

func TestDarwinReader_ReadMemInfo_ZeroMemsize_Errors(t *testing.T) {
	r := newDarwinReaderWithFakes(
		makeSysctlFake(map[string]string{"hw.memsize": "0"}),
		makeVMStatFake("Mach Virtual Memory Statistics: (page size of 4096 bytes)\nPages free: 100.\n"),
	)
	_, err := r.ReadMemInfo()
	if err == nil {
		t.Fatal("expected error for zero hw.memsize, got nil")
	}
}

// TestDarwinReader_ReadCPUInfo_GoldenFixture: hw.ncpu from fixture → Cores.
//
// Mutation-paired: if ncpu parse is skipped (Cores left 0), assertion fails.
// If model fallback logic is broken (returns "" when machdep.cpu.brand_string
// is absent), the hw.model fallback assertion fails.
func TestDarwinReader_ReadCPUInfo_GoldenFixture(t *testing.T) {
	ncpuRaw := darwinFixtureStr(t, "sysctl_ncpu") // "8"
	ncpu, _ := strconv.Atoi(ncpuRaw)

	r := newDarwinReaderWithFakes(
		makeSysctlFake(map[string]string{
			"hw.ncpu":                  ncpuRaw,
			"machdep.cpu.brand_string": "", // Apple Silicon: empty
			"hw.model":                 "MacBookPro18,3",
		}),
		makeVMStatFake(""),
	)
	cpu, err := r.ReadCPUInfo()
	if err != nil {
		t.Fatalf("ReadCPUInfo: %v", err)
	}
	if cpu.Cores != ncpu {
		t.Errorf("Cores = %d, want %d (hw.ncpu)", cpu.Cores, ncpu)
	}
	// machdep.cpu.brand_string is empty → must fall back to hw.model.
	if cpu.Model != "MacBookPro18,3" {
		t.Errorf("Model = %q, want hw.model fallback %q", cpu.Model, "MacBookPro18,3")
	}
}

// Mutation-paired: a non-numeric or zero ncpu must be rejected, not silently
// accepted as 0 or negative (which would be an invalid cpu count).
func TestDarwinReader_ReadCPUInfo_InvalidNCPU_Errors(t *testing.T) {
	r := newDarwinReaderWithFakes(
		makeSysctlFake(map[string]string{"hw.ncpu": "garbage"}),
		makeVMStatFake(""),
	)
	_, err := r.ReadCPUInfo()
	if err == nil {
		t.Fatal("expected error for non-numeric hw.ncpu, got nil")
	}
}

func TestDarwinReader_ReadCPUInfo_ZeroNCPU_Errors(t *testing.T) {
	r := newDarwinReaderWithFakes(
		makeSysctlFake(map[string]string{"hw.ncpu": "0"}),
		makeVMStatFake(""),
	)
	_, err := r.ReadCPUInfo()
	if err == nil {
		t.Fatal("expected error for zero hw.ncpu, got nil")
	}
}

// Intel brand string: when machdep.cpu.brand_string is non-empty, it takes
// precedence over hw.model. Mutation: reversing precedence makes this fail.
func TestDarwinReader_ReadCPUInfo_IntelBrandString_Preferred(t *testing.T) {
	r := newDarwinReaderWithFakes(
		makeSysctlFake(map[string]string{
			"hw.ncpu":                  "4",
			"machdep.cpu.brand_string": "Intel(R) Core(TM) i7-9750H CPU @ 2.60GHz",
			"hw.model":                 "MacBookPro16,1",
		}),
		makeVMStatFake(""),
	)
	cpu, err := r.ReadCPUInfo()
	if err != nil {
		t.Fatalf("ReadCPUInfo: %v", err)
	}
	if cpu.Model != "Intel(R) Core(TM) i7-9750H CPU @ 2.60GHz" {
		t.Errorf("Model = %q, want Intel brand string", cpu.Model)
	}
}

// TestDarwinReader_Read_IntegratesWithAggregator: wire the real DarwinReader
// into a NodeAggregator with injected fakes and assert the CPU+Memory fields
// surface in the aggregated snapshot.
//
// This is the sink-side assertion: it proves data actually flows through Read()
// into NodeResources and then into the aggregator's merged snapshot.
func TestDarwinReader_Read_IntegratesWithAggregator(t *testing.T) {
	vmstatRaw := darwinFixtureStr(t, "vm_stat")
	r := newDarwinReaderWithFakes(
		makeSysctlFake(map[string]string{
			"hw.memsize": "17179869184",
			"hw.ncpu":    "8",
			"hw.model":   "MacBookPro18,3",
		}),
		makeVMStatFake(vmstatRaw+"\n"),
	)

	res, err := r.Read("darwin-node")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if res.NodeID != "darwin-node" {
		t.Errorf("NodeID = %q, want darwin-node", res.NodeID)
	}
	// Sink-side: CPU.Cores must be exactly 8 (from hw.ncpu fixture).
	if res.CPU.Cores != 8 {
		t.Errorf("CPU.Cores = %d, want 8", res.CPU.Cores)
	}
	// Sink-side: Memory.TotalKB must be exactly hw.memsize/1024.
	if res.Memory.TotalKB != darwinFixtureTotalKB {
		t.Errorf("Memory.TotalKB = %d, want %d", res.Memory.TotalKB, darwinFixtureTotalKB)
	}

	// Wire through aggregator and assert the SAME values appear in the snapshot.
	var _ Reader = r
	agg := NewNodeAggregator(5 * 60 * 1e9)
	agg.RegisterReader(r)
	agg.RegisterNode("darwin-node")
	if err := agg.Collect(); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	snap, ok := agg.GetNode("darwin-node")
	if !ok {
		t.Fatal("expected darwin-node snapshot in aggregator")
	}
	// Mutation-paired: if mergeResources drops the CPU branch (e.g. Cores==0
	// guard), this fails.
	if snap.Resources.CPU.Cores != 8 {
		t.Errorf("aggregated CPU.Cores = %d, want 8", snap.Resources.CPU.Cores)
	}
	if snap.Resources.Memory.TotalKB != darwinFixtureTotalKB {
		t.Errorf("aggregated Memory.TotalKB = %d, want %d",
			snap.Resources.Memory.TotalKB, darwinFixtureTotalKB)
	}
}

// ---------- parseSPDisplays unit tests ----------

// TestParseSPDisplays_GoldenFixture: the fixture has one GPU entry with
// "Apple M1 Pro" and 16 cores.
//
// Mutation-paired: if Chipset Model parsing is skipped (count stays 0), or
// the model string is mangled, these assertions fail.
func TestParseSPDisplays_GoldenFixture(t *testing.T) {
	raw := darwinFixtureStr(t, "system_profiler_gpu")
	info, err := parseSPDisplays(raw)
	if err != nil {
		t.Fatalf("parseSPDisplays: %v", err)
	}
	// Count must be exactly 1 (one GPU entry in the fixture).
	if info.Count != 1 {
		t.Errorf("Count = %d, want 1", info.Count)
	}
	// Model must be the exact chipset model string from the fixture.
	if info.Model != "Apple M1 Pro" {
		t.Errorf("Model = %q, want %q", info.Model, "Apple M1 Pro")
	}
	// Memory is 0 for integrated GPU (unified memory, no fixed VRAM).
	// Mutation: fabricating a non-zero Memory here would be a bluff.
	if info.Memory != 0 {
		t.Errorf("Memory = %d, want 0 (integrated GPU / unified memory)", info.Memory)
	}
}

// TestParseSPDisplays_Empty: no GPU output → Count==0, no error.
// Mutation-paired: returning Count==1 for empty input, or returning an error,
// fails this test.
func TestParseSPDisplays_Empty(t *testing.T) {
	info, err := parseSPDisplays("")
	if err != nil {
		t.Fatalf("parseSPDisplays empty: %v", err)
	}
	if info.Count != 0 {
		t.Errorf("Count = %d, want 0 for empty input", info.Count)
	}
}

// TestParseSPDisplays_TwoGPUs: two Chipset Model entries → Count==2 and
// the first entry's model is used.
// Mutation: counting only one entry makes Count==1, failing the assertion.
func TestParseSPDisplays_TwoGPUs(t *testing.T) {
	raw := `Graphics/Displays:

    AMD Radeon Pro 5500M:

      Chipset Model: AMD Radeon Pro 5500M
      Type: GPU
      Bus: PCIe
      Vendor: AMD (0x1002)

    Intel UHD Graphics 630:

      Chipset Model: Intel UHD Graphics 630
      Type: GPU
      Bus: Built-In
      Vendor: Intel (0x8086)
`
	info, err := parseSPDisplays(raw)
	if err != nil {
		t.Fatalf("parseSPDisplays: %v", err)
	}
	if info.Count != 2 {
		t.Errorf("Count = %d, want 2", info.Count)
	}
	// First entry must be used for Model.
	if info.Model != "AMD Radeon Pro 5500M" {
		t.Errorf("Model = %q, want %q", info.Model, "AMD Radeon Pro 5500M")
	}
}

// TestParseSPDisplays_GarbledCores_Errors: a non-numeric "Total Number of
// Cores:" value must error, not silently produce 0 cores.
// Mutation: swallowing the Atoi error and defaulting to 0 makes this return nil.
func TestParseSPDisplays_GarbledCores_Errors(t *testing.T) {
	raw := `Graphics/Displays:

    Some GPU:

      Chipset Model: Some GPU
      Type: GPU
      Total Number of Cores: GARBAGE
`
	_, err := parseSPDisplays(raw)
	if err == nil {
		t.Fatal("expected error for garbled Total Number of Cores, got nil")
	}
}

// TestDarwinGPUReader_Read_IntegratesWithAggregator: wire DarwinGPUReader with
// an injected fixture into a NodeAggregator and assert GPU.Count surfaces.
//
// Sink-side evidence: GPU data flows from system_profiler output (via fake)
// through Read() into NodeResources and into the aggregator's merged snapshot.
func TestDarwinGPUReader_Read_IntegratesWithAggregator(t *testing.T) {
	spRaw := darwinFixtureStr(t, "system_profiler_gpu")
	r := newDarwinGPUReaderWithFake(func() (string, error) { return spRaw, nil })

	res, err := r.Read("darwin-node")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	// Sink-side: Count must be 1 (one GPU in fixture).
	if res.GPU.Count != 1 {
		t.Errorf("GPU.Count = %d, want 1", res.GPU.Count)
	}
	if res.GPU.Model != "Apple M1 Pro" {
		t.Errorf("GPU.Model = %q, want Apple M1 Pro", res.GPU.Model)
	}

	// Wire through aggregator.
	var _ Reader = r
	agg := NewNodeAggregator(5 * 60 * 1e9)
	agg.RegisterReader(r)
	agg.RegisterNode("darwin-node")
	if err := agg.Collect(); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	snap, ok := agg.GetNode("darwin-node")
	if !ok {
		t.Fatal("expected darwin-node in aggregator")
	}
	// Mutation-paired: if mergeResources drops GPU (Count==0 guard not triggered
	// because Count==1), this assertion fails.
	if snap.Resources.GPU.Count != 1 {
		t.Errorf("aggregated GPU.Count = %d, want 1", snap.Resources.GPU.Count)
	}
}
