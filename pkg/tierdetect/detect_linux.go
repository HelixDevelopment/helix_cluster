//go:build linux

package tierdetect

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// linuxDetector probes real Linux host capabilities (CLAUDE-2): /dev/kvm for
// hardware virtualization, /proc/cpuinfo to corroborate the CPU architecture,
// and /proc/sys/fs/binfmt_misc for registered cross-arch emulation handlers.
// Every field is read from the real kernel interfaces; an unreadable probe
// degrades to the safe absent value (false/empty), never a fabricated present
// one.
type linuxDetector struct {
	// Paths are fields so unit tests can point them at fixture trees; production
	// uses the real kernel paths below.
	devKVMPath     string
	cpuinfoPath    string
	binfmtMiscPath string
}

// NewDetector returns the linux Detector wired to the real kernel paths.
func NewDetector() Detector {
	return &linuxDetector{
		devKVMPath:     "/dev/kvm",
		cpuinfoPath:    "/proc/cpuinfo",
		binfmtMiscPath: "/proc/sys/fs/binfmt_misc",
	}
}

// Detect probes the real Linux host.
func (d *linuxDetector) Detect() (HostCapabilities, error) {
	caps := HostCapabilities{
		Arch:   runtime.GOARCH,
		KVM:    d.probeKVM(),
		ARM64:  d.probeARM64(),
		Binfmt: d.probeBinfmt(),
	}
	return caps, nil
}

// probeKVM reports whether /dev/kvm exists and is openable. We attempt an
// O_RDWR open because mere existence does not guarantee usability (permissions,
// nested-virt disabled); a successful open is the operative signal that KVM can
// actually be used to provision. Failure (any reason) reports false truthfully.
func (d *linuxDetector) probeKVM() bool {
	f, err := os.OpenFile(d.devKVMPath, os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// probeARM64 reports whether the host is 64-bit ARM. runtime.GOARCH is the
// primary signal; we additionally require that /proc/cpuinfo does not contradict
// it. When cpuinfo is unreadable we trust GOARCH alone (the binary's own arch is
// authoritative for what guests it can run natively).
func (d *linuxDetector) probeARM64() bool {
	if runtime.GOARCH != "arm64" {
		return false
	}
	data, err := os.ReadFile(d.cpuinfoPath)
	if err != nil {
		// cpuinfo unreadable: trust GOARCH, the running binary is arm64.
		return true
	}
	return cpuinfoIsARM64(string(data))
}

// cpuinfoIsARM64 reports whether /proc/cpuinfo describes an AArch64 CPU. ARM
// kernels expose "CPU architecture: 8" (or higher) and/or "Features" lines;
// the most portable AArch64 marker across distros is the absence of x86 vendor
// lines plus an ARM-style "CPU implementer"/"CPU architecture" field. We treat
// presence of "CPU architecture" with a value >= 8, or any "aarch64" token, as
// AArch64.
func cpuinfoIsARM64(raw string) bool {
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		low := strings.ToLower(line)
		if strings.Contains(low, "aarch64") {
			return true
		}
		if strings.HasPrefix(low, "cpu architecture") {
			if i := strings.Index(line, ":"); i >= 0 {
				val := strings.TrimSpace(line[i+1:])
				// Values are like "8" or "8.2"; first field's leading digit.
				if len(val) > 0 && val[0] >= '8' && val[0] <= '9' {
					return true
				}
			}
		}
	}
	return false
}

// probeBinfmt reads /proc/sys/fs/binfmt_misc and returns a map from handler
// name to enabled-state. Each registered handler is a file in that directory
// whose first line is "enabled" or "disabled". The "register"/"status" control
// files are skipped. An unreadable directory yields an empty (non-nil) map.
func (d *linuxDetector) probeBinfmt() map[string]bool {
	out := map[string]bool{}
	entries, err := os.ReadDir(d.binfmtMiscPath)
	if err != nil {
		return out
	}
	for _, e := range entries {
		name := e.Name()
		if name == "register" || name == "status" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(d.binfmtMiscPath, name))
		if err != nil {
			out[name] = false
			continue
		}
		out[name] = binfmtEntryEnabled(string(data))
	}
	return out
}

// binfmtEntryEnabled reports whether a binfmt_misc handler file's content marks
// it enabled. The first line is "enabled" or "disabled".
func binfmtEntryEnabled(raw string) bool {
	scanner := bufio.NewScanner(strings.NewReader(raw))
	if scanner.Scan() {
		return strings.TrimSpace(scanner.Text()) == "enabled"
	}
	return false
}

var _ Detector = (*linuxDetector)(nil)
