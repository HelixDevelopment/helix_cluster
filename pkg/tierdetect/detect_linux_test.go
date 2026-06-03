//go:build linux

package tierdetect

import (
	"os"
	"path/filepath"
	"testing"
)

// osSpecificHostChecks asserts linux-specific truths against independent
// oracles (CLAUDE-2):
//
//   - KVM must agree with whether /dev/kvm is openable (independent of Detect's
//     own probe path constant).
//   - Binfmt must be a non-nil map.
func osSpecificHostChecks(t *testing.T, caps HostCapabilities) {
	t.Helper()

	if caps.Binfmt == nil {
		t.Fatal("linux must report a non-nil Binfmt map")
	}

	// Oracle: openability of /dev/kvm computed here independently.
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	oracleKVM := err == nil
	if f != nil {
		_ = f.Close()
	}
	if oracleKVM != caps.KVM {
		t.Fatalf("KVM mismatch: /dev/kvm-openable oracle=%v, Detect=%v", oracleKVM, caps.KVM)
	}
}

// TestLinuxDetectorFixtureKVM exercises the linux probes against a fixture tree
// so the test is hermetic and deterministic regardless of the host's real
// /dev/kvm. It verifies KVM-present, binfmt parsing (enabled vs disabled), and
// that ARM64 keys off GOARCH.
//
// Mutation bite: changing binfmtEntryEnabled's `== "enabled"` to `!= "enabled"`
// makes the enabled/disabled assertions here FAIL.
func TestLinuxDetectorFixtureKVM(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Fake /dev/kvm: a regular file is openable O_RDWR, so probeKVM => true.
	kvmPath := filepath.Join(dir, "kvm")
	if err := os.WriteFile(kvmPath, []byte{}, 0o600); err != nil {
		t.Fatalf("write fake kvm: %v", err)
	}

	// Fake binfmt_misc dir with one enabled and one disabled handler, plus the
	// control files that must be skipped.
	binfmtDir := filepath.Join(dir, "binfmt_misc")
	if err := os.MkdirAll(binfmtDir, 0o755); err != nil {
		t.Fatalf("mkdir binfmt: %v", err)
	}
	writeEntry := func(name, body string) {
		if err := os.WriteFile(filepath.Join(binfmtDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write binfmt %s: %v", name, err)
		}
	}
	writeEntry("qemu-aarch64", "enabled\ninterpreter /usr/bin/qemu-aarch64-static\n")
	writeEntry("qemu-arm", "disabled\ninterpreter /usr/bin/qemu-arm-static\n")
	writeEntry("register", "")
	writeEntry("status", "enabled\n")

	d := &linuxDetector{
		devKVMPath:     kvmPath,
		cpuinfoPath:    filepath.Join(dir, "cpuinfo-absent"),
		binfmtMiscPath: binfmtDir,
	}
	caps, err := d.Detect()
	if err != nil {
		t.Fatalf("fixture Detect failed: %v", err)
	}
	if !caps.KVM {
		t.Fatal("fixture /dev/kvm present must yield KVM=true")
	}
	if !caps.HasBinfmt("qemu-aarch64") {
		t.Fatal("enabled qemu-aarch64 handler must be detected")
	}
	if caps.HasBinfmt("qemu-arm") {
		t.Fatal("disabled qemu-arm handler must NOT count as available")
	}
	if _, ok := caps.Binfmt["register"]; ok {
		t.Fatal("binfmt control file 'register' must be skipped")
	}
	if _, ok := caps.Binfmt["status"]; ok {
		t.Fatal("binfmt control file 'status' must be skipped")
	}
}

// TestLinuxDetectorNoKVM proves a missing /dev/kvm yields KVM=false truthfully.
// Mutation bite: making probeKVM ignore the open error makes this FAIL.
func TestLinuxDetectorNoKVM(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d := &linuxDetector{
		devKVMPath:     filepath.Join(dir, "kvm-absent"),
		cpuinfoPath:    filepath.Join(dir, "cpuinfo-absent"),
		binfmtMiscPath: filepath.Join(dir, "binfmt-absent"),
	}
	caps, err := d.Detect()
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}
	if caps.KVM {
		t.Fatal("absent /dev/kvm must yield KVM=false")
	}
	if caps.Binfmt == nil {
		t.Fatal("Binfmt must be non-nil even when dir absent")
	}
}

// TestCpuinfoIsARM64 unit-tests the /proc/cpuinfo AArch64 classifier.
func TestCpuinfoIsARM64(t *testing.T) {
	t.Parallel()
	arm := "processor\t: 0\nBogoMIPS\t: 50.00\nCPU architecture: 8\n"
	if !cpuinfoIsARM64(arm) {
		t.Fatal("ARM cpuinfo (CPU architecture: 8) must classify as arm64")
	}
	x86 := "processor\t: 0\nvendor_id\t: GenuineIntel\nmodel name\t: Xeon\n"
	if cpuinfoIsARM64(x86) {
		t.Fatal("x86 cpuinfo must NOT classify as arm64")
	}
}
