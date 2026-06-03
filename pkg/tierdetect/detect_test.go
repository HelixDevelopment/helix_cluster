package tierdetect

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// TestValidateTierRejectsMissingKVM proves ValidateTier rejects a tier that
// requires a capability the host lacks, with a typed, inspectable error that
// (a) matches ErrUnsupportedTier via errors.Is and (b) exposes the missing
// capability via errors.As. Mutation bite: changing the RequireKVM guard in
// ValidateTier (e.g. `!caps.KVM` -> `caps.KVM`) makes this FAIL.
func TestValidateTierRejectsMissingKVM(t *testing.T) {
	t.Parallel()
	req := TierRequirement{Name: "T6", RequireKVM: true}
	caps := HostCapabilities{Arch: "arm64", ARM64: true, KVM: false}

	err := ValidateTier(req, caps)
	if err == nil {
		t.Fatal("expected rejection for KVM-required tier on KVM-less host, got nil")
	}
	if !errors.Is(err, ErrUnsupportedTier) {
		t.Fatalf("rejection must wrap ErrUnsupportedTier; got %v", err)
	}
	var mce *MissingCapabilityError
	if !errors.As(err, &mce) {
		t.Fatalf("rejection must be a *MissingCapabilityError; got %T", err)
	}
	if mce.Capability != "KVM" {
		t.Fatalf("missing capability = %q, want %q", mce.Capability, "KVM")
	}
	if mce.Tier != "T6" {
		t.Fatalf("tier = %q, want %q", mce.Tier, "T6")
	}
}

// TestValidateTierRejectsMissingARM64 proves architecture rejection is typed
// and names ARM64. Mutation bite: flipping `!caps.ARM64` -> `caps.ARM64` makes
// this FAIL.
func TestValidateTierRejectsMissingARM64(t *testing.T) {
	t.Parallel()
	req := TierRequirement{Name: "TARM", RequireARM64: true}
	caps := HostCapabilities{Arch: "amd64", ARM64: false}

	err := ValidateTier(req, caps)
	var mce *MissingCapabilityError
	if !errors.As(err, &mce) {
		t.Fatalf("expected *MissingCapabilityError; got %T (%v)", err, err)
	}
	if mce.Capability != "ARM64" {
		t.Fatalf("missing capability = %q, want ARM64", mce.Capability)
	}
	if !strings.Contains(mce.Error(), "amd64") {
		t.Fatalf("error should name the actual arch: %q", mce.Error())
	}
}

// TestValidateTierRejectsMissingBinfmt proves binfmt requirements are checked
// against the real Binfmt map. Mutation bite: changing `!caps.HasBinfmt(name)`
// -> `caps.HasBinfmt(name)` makes this FAIL.
func TestValidateTierRejectsMissingBinfmt(t *testing.T) {
	t.Parallel()
	req := TierRequirement{Name: "TEMU", RequireBinfmt: []string{"qemu-aarch64"}}
	caps := HostCapabilities{Arch: "amd64", Binfmt: map[string]bool{"qemu-arm": true}}

	err := ValidateTier(req, caps)
	var mce *MissingCapabilityError
	if !errors.As(err, &mce) {
		t.Fatalf("expected *MissingCapabilityError; got %T (%v)", err, err)
	}
	if mce.Capability != "binfmt:qemu-aarch64" {
		t.Fatalf("missing capability = %q, want binfmt:qemu-aarch64", mce.Capability)
	}
}

// TestValidateTierPassesWhenSatisfied proves the success path: a host that
// satisfies every requirement is accepted (nil). Mutation bite: making
// ValidateTier always return a MissingCapabilityError makes this FAIL.
func TestValidateTierPassesWhenSatisfied(t *testing.T) {
	t.Parallel()
	req := TierRequirement{
		Name:          "TFULL",
		RequireKVM:    true,
		RequireARM64:  true,
		RequireBinfmt: []string{"qemu-aarch64"},
	}
	caps := HostCapabilities{
		Arch:   "arm64",
		ARM64:  true,
		KVM:    true,
		Binfmt: map[string]bool{"qemu-aarch64": true},
	}
	if err := ValidateTier(req, caps); err != nil {
		t.Fatalf("fully-satisfied tier must pass, got %v", err)
	}
}

// TestValidateTierZeroReqAlwaysPasses proves a tier with no requirements is
// satisfied by any host (including a totally empty capability set).
func TestValidateTierZeroReqAlwaysPasses(t *testing.T) {
	t.Parallel()
	if err := ValidateTier(TierRequirement{Name: "T0"}, HostCapabilities{}); err != nil {
		t.Fatalf("zero-requirement tier must always pass, got %v", err)
	}
}

// TestDetectRealHostCapabilities is the SINK-SIDE closure proof (CLAUDE-1 +
// CLAUDE-2). It runs the REAL OS Detector on THIS host and cross-checks the
// reported capabilities against an INDEPENDENT OS oracle:
//
//   - Arch must equal runtime.GOARCH.
//   - ARM64 must agree with `runtime.GOARCH == "arm64"` corroborated by an
//     independent uname/sysctl probe.
//
// It then drives a real rejection AND a real success through ValidateTier using
// the genuinely detected caps, capturing both error objects.
func TestDetectRealHostCapabilities(t *testing.T) {
	caps, err := NewDetector().Detect()
	if err != nil {
		t.Fatalf("Detect on real host failed: %v", err)
	}

	// Oracle 1: architecture must match the Go runtime's own view.
	if caps.Arch != runtime.GOARCH {
		t.Fatalf("detected Arch=%q != runtime.GOARCH=%q", caps.Arch, runtime.GOARCH)
	}

	// Oracle 2: independent OS arch probe (uname -m). Map its answer to the
	// boolean we expect for ARM64 and require Detect to agree.
	unameOut, unameErr := exec.Command("uname", "-m").Output()
	if unameErr != nil {
		t.Fatalf("uname oracle unavailable: %v", unameErr)
	}
	machine := strings.TrimSpace(string(unameOut))
	oracleARM64 := machine == "arm64" || machine == "aarch64"
	// uname machine and GOARCH can both be authoritative; require consistency
	// only when GOARCH itself says arm64 (a translated/rosetta binary may report
	// a different GOARCH than the bare-metal uname, which Detect handles by
	// keying on GOARCH — the safe, native-capability answer).
	if runtime.GOARCH == "arm64" && !oracleARM64 {
		t.Fatalf("GOARCH=arm64 but uname reports %q; oracle disagreement", machine)
	}
	if runtime.GOARCH == "arm64" {
		if !caps.ARM64 {
			t.Fatalf("on arm64 host (uname=%q) Detect reported ARM64=false", machine)
		}
	} else {
		if caps.ARM64 {
			t.Fatalf("non-arm64 GOARCH=%q but Detect reported ARM64=true", runtime.GOARCH)
		}
	}

	osSpecificHostChecks(t, caps)

	// SINK-SIDE rejection: build a tier requiring a capability THIS host lacks
	// and assert ValidateTier rejects it with a typed, inspectable error.
	missing, why := absentRequirement(caps)
	rejectReq := TierRequirement{Name: "T6-reject"}
	switch missing {
	case "KVM":
		rejectReq.RequireKVM = true
	case "ARM64":
		rejectReq.RequireARM64 = true
	default:
		rejectReq.RequireBinfmt = []string{missing}
	}
	rejErr := ValidateTier(rejectReq, caps)
	if rejErr == nil {
		t.Fatalf("expected rejection for absent capability %q (%s), got nil", missing, why)
	}
	var mce *MissingCapabilityError
	if !errors.As(rejErr, &mce) {
		t.Fatalf("rejection not typed: %T %v", rejErr, rejErr)
	}
	t.Logf("captured rejection: %v", mce)

	// SINK-SIDE success: a tier that requires only what the host genuinely has
	// (here: its real architecture) must pass.
	okReq := TierRequirement{Name: "T-ok", RequireARM64: caps.ARM64}
	if okErr := ValidateTier(okReq, caps); okErr != nil {
		t.Fatalf("tier matching real host caps must pass, got %v", okErr)
	}
	t.Logf("captured success: tier %q accepted on %s/%s (ARM64=%v, KVM=%v)",
		okReq.Name, runtime.GOOS, caps.Arch, caps.ARM64, caps.KVM)
}

// runtimeIsARM64 reports whether this binary was built for arm64.
func runtimeIsARM64() bool { return runtime.GOARCH == "arm64" }

// absentRequirement returns a capability name the host genuinely lacks, for use
// in building a guaranteed-rejection tier. Prefers KVM on darwin (always
// absent); otherwise picks a binfmt handler that is definitely not registered.
func absentRequirement(caps HostCapabilities) (name, why string) {
	if !caps.KVM {
		return "KVM", "host has no usable /dev/kvm"
	}
	if !caps.ARM64 {
		return "ARM64", "host is not arm64"
	}
	// Both present: require a binfmt handler that cannot exist.
	return "tierdetect-nonexistent-handler", "synthetic unregistered binfmt handler"
}
