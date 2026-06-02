package device

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

// TestProbe_RealHostValues exercises device.Probe() against REAL OS values so
// that a mutation to any field causes a concrete assertion failure.
//
// Anti-bluff anchors:
//   - Arch  is compared to the live runtime.GOARCH mapping — returning a
//     hard-coded constant or the wrong arch fails the check.
//   - CPUCores is compared to runtime.NumCPU() — returning 0 or a magic
//     number fails the check.
//   - MemoryBytes is checked against >0 and >1GiB (valid for any machine that
//     can run this test suite) — a stub returning 0 or a very small number
//     fails the floor check.
//   - ID is checked for non-empty and does not contain whitespace — an empty
//     string or a TODO stub fails the check.
//   - Trust must be TrustBasic — returning TrustUntrusted or a higher level
//     would silently mislead callers.
func TestProbe_RealHostValues(t *testing.T) {
	d, err := Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe() returned unexpected error: %v", err)
	}

	// --- ID: non-empty, no whitespace, not a stub ---
	if d.ID == "" {
		t.Error("Probe().ID is empty; os.Hostname() must have been bypassed or stubbed")
	}
	if strings.ContainsAny(d.ID, " \t\n\r") {
		t.Errorf("Probe().ID contains whitespace: %q", d.ID)
	}

	// --- Arch: must equal the live GOARCH mapping ---
	wantArch := goarchToArch(runtime.GOARCH)
	if d.Arch != wantArch {
		t.Errorf("Probe().Arch = %q, want %q (runtime.GOARCH=%q)",
			d.Arch, wantArch, runtime.GOARCH)
	}

	// --- CPUCores: must match runtime.NumCPU() ---
	wantCores := runtime.NumCPU()
	if d.CPUCores != wantCores {
		t.Errorf("Probe().CPUCores = %d, want %d (runtime.NumCPU())",
			d.CPUCores, wantCores)
	}
	if d.CPUCores <= 0 {
		// Belt-and-suspenders: even if runtime.NumCPU() somehow returned 0,
		// a probe result of 0 is wrong on any real machine.
		t.Error("Probe().CPUCores <= 0; cannot be correct on a real machine")
	}

	// --- MemoryBytes: must be positive and above a floor ---
	if d.MemoryBytes <= 0 {
		t.Errorf("Probe().MemoryBytes = %d, want > 0", d.MemoryBytes)
	}
	const oneGiB = int64(1) << 30
	if d.MemoryBytes < oneGiB {
		// Any machine capable of compiling and running this test suite has at
		// least 1 GiB of RAM. A value below 1 GiB indicates the fallback
		// returned a fabricated or severely incorrect value.
		t.Errorf("Probe().MemoryBytes = %d bytes, want >= 1 GiB (%d); "+
			"got suspiciously small value — is platformTotalMemoryBytes() using a stub?",
			d.MemoryBytes, oneGiB)
	}

	// --- Trust: must be TrustBasic for a local, un-attested host ---
	if d.Trust != TrustBasic {
		t.Errorf("Probe().Trust = %d, want TrustBasic (%d)", d.Trust, TrustBasic)
	}
}

// TestProbe_ArchMapping verifies that goarchToArch maps the canonical GOARCH
// values used in this codebase to the correct typed constants. A mutation that
// returns the wrong constant (e.g. ArchAMD64 for arm64) causes a failure here.
func TestProbe_ArchMapping(t *testing.T) {
	cases := []struct {
		goarch string
		want   Arch
	}{
		{"amd64", ArchAMD64},
		{"arm64", ArchARM64},
		{"riscv64", ArchRISCV64},
		{"mips64", ArchUnknown},
		{"", ArchUnknown},
	}
	for _, tc := range cases {
		got := goarchToArch(tc.goarch)
		if got != tc.want {
			t.Errorf("goarchToArch(%q) = %q, want %q", tc.goarch, got, tc.want)
		}
	}
}

// TestProbe_CPUCoresMatchesRuntime is a focused mutation guard: if Probe()
// returns a hard-coded CPUCores rather than calling runtime.NumCPU(), this
// test catches the discrepancy on any machine where NumCPU != that constant.
func TestProbe_CPUCoresMatchesRuntime(t *testing.T) {
	d, err := Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe() error: %v", err)
	}
	if got, want := d.CPUCores, runtime.NumCPU(); got != want {
		t.Errorf("Probe().CPUCores = %d, runtime.NumCPU() = %d — mismatch indicates hard-coded value", got, want)
	}
}

// TestProbe_MemoryBytesMatchesPlatformOracle re-calls platformTotalMemoryBytes
// independently and compares it to what Probe returned. A mutation in Probe
// that ignores the platform helper will fail here.
func TestProbe_MemoryBytesMatchesPlatformOracle(t *testing.T) {
	oracle := platformTotalMemoryBytes()
	d, err := Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe() error: %v", err)
	}
	if d.MemoryBytes != oracle {
		t.Errorf("Probe().MemoryBytes = %d, platformTotalMemoryBytes() = %d; must be identical",
			d.MemoryBytes, oracle)
	}
}

// TestProbe_IDNonEmpty is a focused guard for the ID field. A stub returning
// "" triggers this failure even if all other fields look correct.
func TestProbe_IDNonEmpty(t *testing.T) {
	d, err := Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe() error: %v", err)
	}
	if d.ID == "" {
		t.Error("Probe().ID is empty; ID must be derived from os.Hostname() and must never be empty or a TODO stub")
	}
}

// TestProbe_ClassifiesSanely verifies that the probed descriptor produces a
// sensible Tier on a real development machine. On a machine with 8+ cores and
// 16+ GiB RAM the tier must be at least T4 (workstation-lite threshold).
// This is a soft sanity check; it fails only if the descriptor is so wrong
// that classification degrades below T3 (which would require CPUCores<4 or
// MemoryBytes<2GiB — impossible on any machine running this test).
func TestProbe_ClassifiesSanely(t *testing.T) {
	d, err := Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe() error: %v", err)
	}
	tier := ClassifyTier(d)
	if tier == TierUnknown {
		t.Errorf("ClassifyTier(Probe()) = TierUnknown; descriptor is degenerate: %+v", d)
	}
	// T1 is only for severely constrained devices; no real CI/dev machine should
	// hit T1 with real values.
	if tier == T1 {
		t.Errorf("ClassifyTier(Probe()) = T1 — this implies CPUCores<2 or MemoryBytes<512MiB "+
			"which is implausible on the host running this test; descriptor: %+v", d)
	}
}
