//go:build darwin

package tierdetect

import (
	"os/exec"
	"runtime"
	"strings"
)

// darwinDetector probes real macOS host capabilities (CLAUDE-2). It uses
// runtime.GOARCH for architecture and the standard `sysctl hw.optional.arm64`
// facility (present on every macOS install) to corroborate ARM64. KVM is always
// reported false because macOS has no /dev/kvm — virtualization on macOS is the
// Hypervisor framework, not KVM; reporting false here is the truthful answer,
// not a stub. binfmt_misc has no macOS equivalent, so Binfmt is empty.
type darwinDetector struct {
	// sysctlFn is injectable so unit tests can replay captured `sysctl` output
	// without depending on the host. Production leaves it nil and shells out to
	// the real sysctl(8).
	sysctlFn func(key string) (string, error)
}

// NewDetector returns the darwin Detector backed by real sysctl.
func NewDetector() Detector { return &darwinDetector{} }

// sysctl runs "sysctl -n <key>" and returns the trimmed output.
func (d *darwinDetector) sysctl(key string) (string, error) {
	if d.sysctlFn != nil {
		return d.sysctlFn(key)
	}
	out, err := exec.Command("sysctl", "-n", key).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Detect probes the real macOS host. Arch is GOARCH. ARM64 is true when GOARCH
// is arm64 AND `sysctl hw.optional.arm64` reports "1" — requiring both means a
// mislabelled GOARCH (e.g. an amd64 binary under Rosetta) cannot produce a
// false ARM64 positive: Rosetta reports hw.optional.arm64=0 to the translated
// process, so a translated amd64 build correctly detects ARM64=false.
func (d *darwinDetector) Detect() (HostCapabilities, error) {
	caps := HostCapabilities{
		Arch:   runtime.GOARCH,
		KVM:    false, // macOS has no /dev/kvm — truthful, not a stub.
		Binfmt: map[string]bool{},
	}

	// runtime.GOARCH is the primary signal; corroborate with the kernel's own
	// arm64 optional-feature flag. We only set ARM64 true when both agree.
	if runtime.GOARCH == "arm64" {
		if v, err := d.sysctl("hw.optional.arm64"); err == nil && strings.TrimSpace(v) == "1" {
			caps.ARM64 = true
		}
	}
	return caps, nil
}

var _ Detector = (*darwinDetector)(nil)
