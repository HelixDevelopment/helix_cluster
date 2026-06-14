package tierdef_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/tierdef"
)

// This file is an ADVERSARIAL companion to tierdef_test.go. It pins parts of the
// tier-config contract that the existing suite leaves uncovered:
//
//   - the FULL T1..T15 default value table (existing suite pins only T1 & T15);
//   - the documented NEGATIVE-min_* rejection path (loader.go:75-89) — entirely
//     untested before this file;
//   - the documented "min_* validated, max_power_w NOT validated" asymmetry;
//   - type-mismatch malformed input → parse error (not a silent partial set);
//   - empty / nil input handled per contract (schema_version 0 → rejected);
//   - the monotonic-ladder data invariant (T1..T15 thresholds never decrease);
//   - a full round-trip: re-loading the rendered default registry reproduces it.
//
// Every oracle below is HARDCODED from tierdef.go's documentation and the
// tierdef.yaml fixture, NOT read back from the loaded registry, so a mutated
// default value or a skipped validation step breaks a test rather than silently
// passing.

// fullLadder is the complete, independently-transcribed oracle for all 15 tiers.
// Fields: cpu, memMB, gpu, vramMB, netMbps, powerW.
var fullLadder = []struct {
	name  string
	cpu   int
	mem   int
	gpu   int
	vram  int
	net   int
	power float64
}{
	{"T1", 1, 256, 0, 0, 10, 5.0},
	{"T2", 2, 512, 0, 0, 100, 15.0},
	{"T3", 4, 2048, 0, 0, 1000, 25.0},
	{"T4", 8, 8192, 0, 0, 1000, 65.0},
	{"T5", 12, 16384, 1, 4096, 1000, 150.0},
	{"T6", 16, 32768, 1, 8192, 2500, 300.0},
	{"T7", 24, 65536, 1, 16384, 10000, 500.0},
	{"T8", 32, 131072, 2, 16384, 10000, 700.0},
	{"T9", 48, 196608, 2, 24576, 25000, 1000.0},
	{"T10", 64, 262144, 4, 24576, 25000, 1500.0},
	{"T11", 96, 393216, 4, 40960, 50000, 2000.0},
	{"T12", 128, 524288, 4, 40960, 50000, 2500.0},
	{"T13", 128, 786432, 8, 40960, 100000, 4000.0},
	{"T14", 192, 1048576, 8, 80000, 100000, 6000.0},
	{"T15", 256, 2097152, 8, 80000, 400000, 10000.0},
}

// ── A. DEFAULTS: full T1..T15 value table (centerpiece) ─────────────────────────

// TestDefault_FullLadderValues pins every field of every one of the 15 tiers
// against an independently-written oracle. The existing suite only pins T1 and
// T15; this closes T2..T14. Mutating ANY default value in tierdef.yaml breaks
// exactly the corresponding sub-assertion here.
func TestDefault_FullLadderValues(t *testing.T) {
	reg, err := tierdef.Default()
	if err != nil {
		t.Fatalf("Default(): %v", err)
	}
	if len(reg.Tiers) != len(fullLadder) {
		t.Fatalf("len(Tiers) = %d; want %d", len(reg.Tiers), len(fullLadder))
	}
	for _, want := range fullLadder {
		want := want
		t.Run(want.name, func(t *testing.T) {
			td, ok := reg.Get(want.name)
			if !ok {
				t.Fatalf("Get(%q) not found", want.name)
			}
			if td.MinCPUCores != want.cpu {
				t.Errorf("%s MinCPUCores = %d; want %d", want.name, td.MinCPUCores, want.cpu)
			}
			if td.MinMemoryMB != want.mem {
				t.Errorf("%s MinMemoryMB = %d; want %d", want.name, td.MinMemoryMB, want.mem)
			}
			if td.MinGPU != want.gpu {
				t.Errorf("%s MinGPU = %d; want %d", want.name, td.MinGPU, want.gpu)
			}
			if td.MinGPUVRAMMB != want.vram {
				t.Errorf("%s MinGPUVRAMMB = %d; want %d", want.name, td.MinGPUVRAMMB, want.vram)
			}
			if td.MinNetworkMbps != want.net {
				t.Errorf("%s MinNetworkMbps = %d; want %d", want.name, td.MinNetworkMbps, want.net)
			}
			if td.MaxPowerW != want.power {
				t.Errorf("%s MaxPowerW = %v; want %v", want.name, td.MaxPowerW, want.power)
			}
		})
	}
}

// TestDefault_LadderIsMonotonic pins the documented "rung ladder from T1 ...
// to T15" data invariant: every min_* threshold (and the power ceiling) is
// non-decreasing as the tier index grows. This guards against a future edit
// that reorders or mis-scales a tier's requirements while keeping names valid.
//
// NOTE: this asserts a property of the SHIPPED DATA. The loader does NOT enforce
// monotonicity (see TestLoadTiers_MonotonicityNotEnforced below), so this test
// is the only sink-side guard for it.
func TestDefault_LadderIsMonotonic(t *testing.T) {
	reg, err := tierdef.Default()
	if err != nil {
		t.Fatalf("Default(): %v", err)
	}
	for i := 1; i < len(reg.Tiers); i++ {
		prev, cur := reg.Tiers[i-1], reg.Tiers[i]
		if cur.MinCPUCores < prev.MinCPUCores {
			t.Errorf("%s->%s MinCPUCores decreased: %d < %d", prev.Tier, cur.Tier, cur.MinCPUCores, prev.MinCPUCores)
		}
		if cur.MinMemoryMB < prev.MinMemoryMB {
			t.Errorf("%s->%s MinMemoryMB decreased: %d < %d", prev.Tier, cur.Tier, cur.MinMemoryMB, prev.MinMemoryMB)
		}
		if cur.MinGPU < prev.MinGPU {
			t.Errorf("%s->%s MinGPU decreased: %d < %d", prev.Tier, cur.Tier, cur.MinGPU, prev.MinGPU)
		}
		if cur.MinGPUVRAMMB < prev.MinGPUVRAMMB {
			t.Errorf("%s->%s MinGPUVRAMMB decreased: %d < %d", prev.Tier, cur.Tier, cur.MinGPUVRAMMB, prev.MinGPUVRAMMB)
		}
		if cur.MinNetworkMbps < prev.MinNetworkMbps {
			t.Errorf("%s->%s MinNetworkMbps decreased: %d < %d", prev.Tier, cur.Tier, cur.MinNetworkMbps, prev.MinNetworkMbps)
		}
		if cur.MaxPowerW < prev.MaxPowerW {
			t.Errorf("%s->%s MaxPowerW decreased: %v < %v", prev.Tier, cur.Tier, cur.MaxPowerW, prev.MaxPowerW)
		}
	}
}

// TestDefault_SchemaVersionIsOne pins the loaded schema version, so a fixture
// edit to schema_version (which would also have to flip the loader constant)
// is caught.
func TestDefault_SchemaVersionIsOne(t *testing.T) {
	reg, err := tierdef.Default()
	if err != nil {
		t.Fatalf("Default(): %v", err)
	}
	if reg.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d; want 1", reg.SchemaVersion)
	}
}

// ── B. VALIDATION: negative min_* rejection (uncovered before this file) ─────────

// TestLoadTiers_NegativeMinField_IsRejected exercises the per-field
// non-negativity guard at loader.go:75-89 for EACH min_* field. This path had
// ZERO coverage in the pre-existing suite. Each case injects a single negative
// value into an otherwise-valid 15-tier document and asserts rejection with a
// nil registry and a message naming the offending field.
func TestLoadTiers_NegativeMinField_IsRejected(t *testing.T) {
	cases := []struct {
		field   string // YAML key to set negative
		wantMsg string // substring the error should mention
	}{
		{"min_cpu_cores", "min_cpu_cores"},
		{"min_memory_mb", "min_memory_mb"},
		{"min_gpu", "min_gpu"},
		{"min_gpu_vram_mb", "min_gpu_vram_mb"},
		{"min_network_mbps", "min_network_mbps"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.field, func(t *testing.T) {
			doc := buildLadderWithNegative(tc.field, 5 /* inject at T5 */)
			reg, err := tierdef.LoadTiers([]byte(doc))
			if err == nil {
				t.Fatalf("LoadTiers accepted a negative %s; want error", tc.field)
			}
			if reg != nil {
				t.Errorf("LoadTiers returned non-nil registry on negative %s", tc.field)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q does not mention offending field %q", err.Error(), tc.wantMsg)
			}
		})
	}
}

// TestLoadTiers_NegativeMaxPowerW_IsAccepted pins the documented asymmetry: only
// min_* fields are range-checked; max_power_w is NOT validated (it is an
// informational ceiling, not enforced by Meets). A negative max_power_w is
// therefore ACCEPTED. This is intentional contract behavior — pinning it means
// any future change that starts rejecting it is a deliberate, visible decision.
func TestLoadTiers_NegativeMaxPowerW_IsAccepted(t *testing.T) {
	doc := buildLadderWithNegative("max_power_w", 5)
	reg, err := tierdef.LoadTiers([]byte(doc))
	if err != nil {
		t.Fatalf("LoadTiers rejected a negative max_power_w (%v); contract validates only min_* fields", err)
	}
	if reg == nil {
		t.Fatal("LoadTiers returned nil registry despite no error")
	}
	td, ok := reg.Get("T5")
	if !ok {
		t.Fatal("T5 not found")
	}
	if td.MaxPowerW != -1.0 {
		t.Errorf("T5.MaxPowerW = %v; want -1 (negative power passes through unchanged)", td.MaxPowerW)
	}
}

// TestLoadTiers_MonotonicityNotEnforced pins the (deliberate) gap: the loader
// does NOT verify that thresholds form a non-decreasing ladder. A document in
// which T2 demands FEWER cores than T1 is still accepted, as long as names are
// T1..T15 in order and all values are non-negative. This documents that the
// monotonicity guarantee lives in the DATA (tierdef.yaml), enforced by
// TestDefault_LadderIsMonotonic, not in the parser.
func TestLoadTiers_MonotonicityNotEnforced(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("schema_version: 1\ntiers:\n")
	for i := 1; i <= 15; i++ {
		cpu := i
		if i == 2 {
			cpu = 0 // T2 weaker than T1 (which has cpu=1): a non-monotone ladder
		}
		sb.WriteString(fmt.Sprintf(
			"  - tier: T%d\n    min_cpu_cores: %d\n    min_memory_mb: %d\n    min_gpu: 0\n    min_gpu_vram_mb: 0\n    min_network_mbps: 10\n    max_power_w: 5.0\n",
			i, cpu, 256*i,
		))
	}
	reg, err := tierdef.LoadTiers([]byte(sb.String()))
	if err != nil {
		t.Fatalf("LoadTiers rejected a non-monotone (but well-named, non-negative) ladder: %v; "+
			"contract does NOT validate monotonicity", err)
	}
	if reg == nil {
		t.Fatal("nil registry without error")
	}
	t1, _ := reg.Get("T1")
	t2, _ := reg.Get("T2")
	if !(t2.MinCPUCores < t1.MinCPUCores) {
		t.Fatalf("test setup wrong: expected T2.cpu(%d) < T1.cpu(%d)", t2.MinCPUCores, t1.MinCPUCores)
	}
}

// ── C. MALFORMED LOAD: type mismatch / empty / nil ──────────────────────────────

// TestLoadTiers_TypeMismatch_IsRejected confirms a type-mismatched field (a
// string where an int is required) surfaces a YAML parse error rather than
// being silently coerced or dropped into a partial set.
func TestLoadTiers_TypeMismatch_IsRejected(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("schema_version: 1\ntiers:\n")
	for i := 1; i <= 15; i++ {
		cpu := fmt.Sprintf("%d", i)
		if i == 1 {
			cpu = "not_a_number" // type mismatch: string into int
		}
		sb.WriteString(fmt.Sprintf(
			"  - tier: T%d\n    min_cpu_cores: %s\n    min_memory_mb: %d\n    min_gpu: 0\n    min_gpu_vram_mb: 0\n    min_network_mbps: 10\n    max_power_w: 5.0\n",
			i, cpu, 256*i,
		))
	}
	reg, err := tierdef.LoadTiers([]byte(sb.String()))
	if err == nil {
		t.Fatal("LoadTiers accepted a type-mismatched min_cpu_cores; want parse error")
	}
	if reg != nil {
		t.Error("LoadTiers returned non-nil registry on type mismatch (partial set leaked)")
	}
	if !strings.Contains(err.Error(), "parse error") {
		t.Errorf("error %q is not surfaced as a YAML parse error", err.Error())
	}
}

// TestLoadTiers_EmptyAndNil_IsRejected pins the contract for degenerate input:
// empty bytes and nil both unmarshal to schema_version 0 (and zero tiers), which
// the loader rejects at the schema-version gate — NOT a silent empty registry.
func TestLoadTiers_EmptyAndNil_IsRejected(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"nil", nil},
		{"whitespace_only", []byte("   \n  \n")},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			reg, err := tierdef.LoadTiers(tc.data)
			if err == nil {
				t.Fatalf("LoadTiers(%s) returned nil error; want rejection (no silent empty set)", tc.name)
			}
			if reg != nil {
				t.Errorf("LoadTiers(%s) returned non-nil registry; want nil", tc.name)
			}
		})
	}
}

// ── D. ROUND-TRIP / completeness ────────────────────────────────────────────────

// TestRoundTrip_RenderedDefaultReloads renders the loaded default registry back
// to YAML and re-loads it, asserting the second registry is byte-for-field equal
// to the first across all 15 tiers. This proves no tier is dropped or mutated by
// a load->render->load cycle (completeness + idempotence of the contract).
func TestRoundTrip_RenderedDefaultReloads(t *testing.T) {
	orig, err := tierdef.Default()
	if err != nil {
		t.Fatalf("Default(): %v", err)
	}

	rendered := renderRegistryYAML(orig)
	reloaded, err := tierdef.LoadTiers([]byte(rendered))
	if err != nil {
		t.Fatalf("re-loading rendered default failed: %v\n--- rendered ---\n%s", err, rendered)
	}

	if len(reloaded.Tiers) != len(orig.Tiers) {
		t.Fatalf("round-trip tier count = %d; want %d (a tier was dropped)", len(reloaded.Tiers), len(orig.Tiers))
	}
	if reloaded.SchemaVersion != orig.SchemaVersion {
		t.Errorf("round-trip SchemaVersion = %d; want %d", reloaded.SchemaVersion, orig.SchemaVersion)
	}
	for i := range orig.Tiers {
		o, r := orig.Tiers[i], reloaded.Tiers[i]
		if o != r {
			t.Errorf("tier[%d] changed across round-trip:\n  orig=%+v\n  got =%+v", i, o, r)
		}
		// Index look-up must also survive.
		got, ok := reloaded.Get(o.Tier)
		if !ok {
			t.Errorf("round-trip lost index entry for %s", o.Tier)
		} else if got != o {
			t.Errorf("round-trip Get(%s) mismatch:\n  orig=%+v\n  got =%+v", o.Tier, o, got)
		}
	}
}

// ── helpers (suffixed _adv to avoid clashing with tierdef_test.go helpers) ───────

// buildLadderWithNegative renders a valid 15-tier ladder but sets exactly one
// field on tier T<at> to -1.
func buildLadderWithNegative(field string, at int) string {
	var sb strings.Builder
	sb.WriteString("schema_version: 1\ntiers:\n")
	for i := 1; i <= 15; i++ {
		fields := map[string]string{
			"min_cpu_cores":    fmt.Sprintf("%d", i),
			"min_memory_mb":    fmt.Sprintf("%d", 256*i),
			"min_gpu":          "0",
			"min_gpu_vram_mb":  "0",
			"min_network_mbps": "10",
			"max_power_w":      "5.0",
		}
		if i == at {
			fields[field] = "-1"
		}
		sb.WriteString(fmt.Sprintf(
			"  - tier: T%d\n    min_cpu_cores: %s\n    min_memory_mb: %s\n    min_gpu: %s\n    min_gpu_vram_mb: %s\n    min_network_mbps: %s\n    max_power_w: %s\n",
			i,
			fields["min_cpu_cores"], fields["min_memory_mb"], fields["min_gpu"],
			fields["min_gpu_vram_mb"], fields["min_network_mbps"], fields["max_power_w"],
		))
	}
	return sb.String()
}

// renderRegistryYAML serializes a Registry back into the on-disk YAML schema.
// It is intentionally hand-written (not yaml.Marshal of the private types) so
// the round-trip exercises the real LoadTiers parse path on independent text.
func renderRegistryYAML(r *tierdef.Registry) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("schema_version: %d\ntiers:\n", r.SchemaVersion))
	for _, td := range r.Tiers {
		sb.WriteString(fmt.Sprintf(
			"  - tier: %s\n    min_cpu_cores: %d\n    min_memory_mb: %d\n    min_gpu: %d\n    min_gpu_vram_mb: %d\n    min_network_mbps: %d\n    max_power_w: %g\n",
			td.Tier, td.MinCPUCores, td.MinMemoryMB, td.MinGPU, td.MinGPUVRAMMB, td.MinNetworkMbps, td.MaxPowerW,
		))
	}
	return sb.String()
}
