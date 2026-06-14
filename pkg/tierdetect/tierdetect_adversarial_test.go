package tierdetect

import (
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// Adversarial contract pins for pkg/tierdetect.
//
// DETECTION MODEL (read before editing):
//   - This package does NOT detect virtualization via a /proc/cpuinfo
//     "hypervisor" flag. KVM is detected purely by whether /dev/kvm is
//     openable O_RDWR (linux); on darwin KVM is *always* false (no /dev/kvm —
//     macOS uses the Hypervisor framework). So the "KVM-present vs KVM-absent"
//     centerpiece here is driven through the openable-/dev/kvm signal
//     (HostCapabilities.KVM) and the darwin always-false truth, NOT a cpuinfo
//     flag.
//   - ARCH detection: Arch is always runtime.GOARCH. ARM64 is GOARCH=="arm64"
//     CORROBORATED by a real per-OS facility:
//       * darwin: sysctl hw.optional.arm64 == "1" (testable on THIS host).
//       * linux:  /proc/cpuinfo via cpuinfoIsARM64 (linux-only build tag —
//         covered by detect_linux_test.go fixtures; not compilable here).
//   - ABSENT-SIGNAL DEFAULT: every probe degrades to the SAFE absent value
//     (false/empty), never a fabricated present one. These tests pin that.
//
// CLAUDE-2: the per-OS classifier is driven with representative fixture bytes
// (sysctl output strings), NOT mocked away. On darwin/arm64 (this host) the
// real darwinDetector.Detect arm64 branch is exercised; the linux cpuinfo
// classifier is justifiably not reachable under the darwin build tag.
// ---------------------------------------------------------------------------

// TestValidateTierFirstUnsatisfiedIsDeterministic pins the documented fixed
// check order (ARM64, then KVM, then binfmt). When MULTIPLE requirements are
// unmet, the reported missing capability must be ARM64 first. Mutation bite:
// reordering the checks in ValidateTier (KVM before ARM64) flips the reported
// capability and FAILS this.
func TestValidateTierFirstUnsatisfiedIsDeterministic(t *testing.T) {
	t.Parallel()
	req := TierRequirement{
		Name:          "TMULTI",
		RequireARM64:  true,
		RequireKVM:    true,
		RequireBinfmt: []string{"qemu-aarch64"},
	}
	// Host satisfies NOTHING.
	caps := HostCapabilities{Arch: "amd64"}
	err := ValidateTier(req, caps)
	var mce *MissingCapabilityError
	if !errors.As(err, &mce) {
		t.Fatalf("expected *MissingCapabilityError, got %T (%v)", err, err)
	}
	if mce.Capability != "ARM64" {
		t.Fatalf("first unsatisfied capability = %q, want ARM64 (fixed order ARM64,KVM,binfmt)", mce.Capability)
	}
}

// TestValidateTierKVMBeforeBinfmt pins that KVM is reported before binfmt when
// ARM64 is satisfied but both KVM and binfmt are missing. This corroborates the
// middle of the documented order. Mutation bite: moving the binfmt loop above
// the KVM check FAILS this.
func TestValidateTierKVMBeforeBinfmt(t *testing.T) {
	t.Parallel()
	req := TierRequirement{
		Name:          "TKVMBIN",
		RequireARM64:  true,
		RequireKVM:    true,
		RequireBinfmt: []string{"qemu-aarch64"},
	}
	caps := HostCapabilities{Arch: "arm64", ARM64: true, KVM: false}
	err := ValidateTier(req, caps)
	var mce *MissingCapabilityError
	if !errors.As(err, &mce) {
		t.Fatalf("expected *MissingCapabilityError, got %T (%v)", err, err)
	}
	if mce.Capability != "KVM" {
		t.Fatalf("missing capability = %q, want KVM (KVM checked before binfmt)", mce.Capability)
	}
}

// TestHasBinfmtNilMapSafe pins the absent-signal default for the binfmt map:
// a nil Binfmt map must answer false (no panic), and a present-but-disabled
// handler must NOT count. Mutation bite: dropping the nil guard in HasBinfmt
// panics; treating missing keys as present FAILS the absent assertions.
func TestHasBinfmtNilMapSafe(t *testing.T) {
	t.Parallel()
	var caps HostCapabilities // Binfmt is nil
	if caps.HasBinfmt("anything") {
		t.Fatal("nil Binfmt map must report HasBinfmt=false")
	}
	caps2 := HostCapabilities{Binfmt: map[string]bool{"qemu-arm": false}}
	if caps2.HasBinfmt("qemu-arm") {
		t.Fatal("disabled handler (value=false) must report HasBinfmt=false")
	}
	if caps2.HasBinfmt("not-registered") {
		t.Fatal("unregistered handler must report HasBinfmt=false")
	}
}

// TestMissingCapabilityErrorIsWrapping pins both error-inspection contracts
// simultaneously: errors.Is(ErrUnsupportedTier) AND errors.As(*MissingCapabilityError),
// plus the unnamed-tier rendering. Mutation bite: dropping Unwrap breaks
// errors.Is and FAILS this.
func TestMissingCapabilityErrorIsWrapping(t *testing.T) {
	t.Parallel()
	err := ValidateTier(TierRequirement{RequireARM64: true}, HostCapabilities{Arch: "amd64"})
	if !errors.Is(err, ErrUnsupportedTier) {
		t.Fatalf("rejection must wrap ErrUnsupportedTier; got %v", err)
	}
	var mce *MissingCapabilityError
	if !errors.As(err, &mce) {
		t.Fatalf("rejection must be *MissingCapabilityError; got %T", err)
	}
	// Unnamed tier must render as <unnamed>, not empty quotes.
	if got := mce.Error(); !contains(got, "<unnamed>") {
		t.Fatalf("unnamed tier error should contain <unnamed>; got %q", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// NOTE: the darwin-specific arch-classifier and KVM-contract centerpieces live
// in tierdetect_adversarial_darwin_test.go (//go:build darwin) because they
// reference darwinDetector, which only exists under the darwin build tag. The
// linux per-OS classifier (cpuinfoIsARM64) is covered by detect_linux_test.go.
