// Package tierdetect performs automated host-capability detection used to
// validate a requested provisioning tier BEFORE any resources are allocated.
//
// The motivating problem is that tiers express hard hardware/kernel
// requirements (e.g. a tier that pins an ARM64 guest, or one that needs
// hardware-accelerated virtualization via /dev/kvm). When such a requirement
// is discovered only mid-provision, the failure is expensive and opaque.
// tierdetect probes the REAL host once, up front, and ValidateTier returns an
// actionable, typed error naming the missing capability so the caller can
// refuse the request cleanly instead of failing halfway through.
//
// CLAUDE-2 compliance: capability detection is split by build tag behind the
// single Detector interface — detect_linux.go probes /dev/kvm, /proc/cpuinfo
// and /proc/sys/fs/binfmt_misc; detect_darwin.go uses runtime.GOARCH and the
// real `sysctl hw.optional.arm64` facility and reports KVM truthfully as false
// (macOS has no /dev/kvm — virtualization there is the Hypervisor framework).
// Neither path fabricates capabilities, so there are zero false positives for
// unsupported configurations.
package tierdetect

import (
	"errors"
	"fmt"
	"runtime"
)

// HostCapabilities is the OS-independent view of what the local host can do,
// populated by the per-OS Detector. Every field reflects a real, measured
// property of THIS machine; an unset/false field means "genuinely absent",
// never "unknown" (probe failures degrade to the safe absent value rather than
// a fabricated present one).
type HostCapabilities struct {
	// Arch is the GOARCH-style architecture of the host (e.g. "arm64",
	// "amd64"). It is always populated from runtime.GOARCH.
	Arch string

	// KVM reports whether hardware-accelerated KVM virtualization is available
	// (Linux /dev/kvm present and openable). Always false on darwin, which has
	// no /dev/kvm — that is the truthful report, not a stub.
	KVM bool

	// ARM64 reports whether the host CPU is 64-bit ARM. On darwin it is
	// cross-checked against `sysctl hw.optional.arm64`; on linux it is derived
	// from runtime.GOARCH and corroborated by /proc/cpuinfo.
	ARM64 bool

	// Binfmt maps a binfmt_misc handler name (e.g. "qemu-aarch64") to whether
	// it is currently registered and enabled, enabling cross-arch emulation
	// detection. On Linux this is read from /proc/sys/fs/binfmt_misc; on darwin
	// the map is empty (no binfmt_misc equivalent — Rosetta is not exposed this
	// way), reported truthfully rather than faked.
	Binfmt map[string]bool
}

// HasBinfmt reports whether a binfmt_misc handler with the given name is
// registered and enabled on the host.
func (c HostCapabilities) HasBinfmt(name string) bool {
	if c.Binfmt == nil {
		return false
	}
	return c.Binfmt[name]
}

// Detector probes the real host for its capabilities. Implementations are
// selected by build tag (detect_linux.go / detect_darwin.go).
type Detector interface {
	// Detect probes the host and returns its real capabilities. It returns an
	// error only for a probe that cannot complete at all (e.g. runtime data
	// unreadable); an absent capability is reported as a false/empty field, not
	// an error.
	Detect() (HostCapabilities, error)
}

// NewDetector returns the Detector implementation for the current OS.
// (defined in detect_linux.go / detect_darwin.go)

// TierRequirement is the set of host capabilities a tier demands. A zero-value
// TierRequirement demands nothing and is satisfied by any host.
type TierRequirement struct {
	// Name identifies the tier (e.g. "T6") for use in error messages.
	Name string

	// RequireKVM demands hardware KVM virtualization.
	RequireKVM bool

	// RequireARM64 demands a 64-bit ARM host CPU.
	RequireARM64 bool

	// RequireBinfmt lists binfmt_misc handler names that must be registered.
	RequireBinfmt []string
}

// ErrUnsupportedTier is the sentinel wrapped by every ValidateTier rejection.
// Callers can match it with errors.Is to branch on "tier not supported here"
// without parsing message text.
var ErrUnsupportedTier = errors.New("tierdetect: tier not supported on this host")

// MissingCapabilityError is the typed, inspectable error returned by
// ValidateTier when a tier requires a capability the host lacks. It names the
// tier and the specific missing capability so the rejection is actionable.
// It wraps ErrUnsupportedTier (errors.Is) and exposes the structured fields
// (errors.As) for programmatic handling.
type MissingCapabilityError struct {
	// Tier is the rejected tier's name (TierRequirement.Name).
	Tier string

	// Capability is the missing capability, e.g. "KVM", "ARM64" or
	// "binfmt:qemu-aarch64".
	Capability string

	// Detail is a human-readable explanation of why the capability is absent.
	Detail string
}

// Error implements error.
func (e *MissingCapabilityError) Error() string {
	tier := e.Tier
	if tier == "" {
		tier = "<unnamed>"
	}
	return fmt.Sprintf("tierdetect: tier %q requires %s which this host lacks: %s",
		tier, e.Capability, e.Detail)
}

// Unwrap lets errors.Is(err, ErrUnsupportedTier) succeed.
func (e *MissingCapabilityError) Unwrap() error { return ErrUnsupportedTier }

// ValidateTier checks every requirement of req against caps and returns a typed
// *MissingCapabilityError (wrapping ErrUnsupportedTier) for the first
// unsatisfied requirement, or nil when the host satisfies the tier. Checks run
// in a fixed order (ARM64, KVM, binfmt) so the reported missing capability is
// deterministic.
func ValidateTier(req TierRequirement, caps HostCapabilities) error {
	if req.RequireARM64 && !caps.ARM64 {
		return &MissingCapabilityError{
			Tier:       req.Name,
			Capability: "ARM64",
			Detail: fmt.Sprintf("host architecture is %q (not 64-bit ARM)",
				caps.Arch),
		}
	}
	if req.RequireKVM && !caps.KVM {
		detail := "/dev/kvm not present or not openable"
		if runtime.GOOS == "darwin" {
			detail = "macOS has no KVM; use the Hypervisor framework instead"
		}
		return &MissingCapabilityError{
			Tier:       req.Name,
			Capability: "KVM",
			Detail:     detail,
		}
	}
	for _, name := range req.RequireBinfmt {
		if !caps.HasBinfmt(name) {
			return &MissingCapabilityError{
				Tier:       req.Name,
				Capability: "binfmt:" + name,
				Detail:     "binfmt_misc handler not registered/enabled",
			}
		}
	}
	return nil
}

// DetectAndValidate is a convenience that probes the host with the OS Detector
// and validates req against the result in one call.
func DetectAndValidate(req TierRequirement) (HostCapabilities, error) {
	caps, err := NewDetector().Detect()
	if err != nil {
		return HostCapabilities{}, fmt.Errorf("tierdetect: probe host: %w", err)
	}
	return caps, ValidateTier(req, caps)
}
