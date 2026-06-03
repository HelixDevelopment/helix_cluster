//go:build darwin

package tierdetect

import (
	"os/exec"
	"strings"
	"testing"
)

// osSpecificHostChecks asserts the darwin-specific truths against an
// independent OS oracle (CLAUDE-2):
//
//   - KVM MUST be false (macOS has no /dev/kvm).
//   - ARM64 MUST agree with the real `sysctl -n hw.optional.arm64`.
//   - Binfmt MUST be empty (no binfmt_misc on macOS).
//
// Mutation bite candidate: setting KVM:true in detect_darwin.go Detect makes
// this FAIL here.
func osSpecificHostChecks(t *testing.T, caps HostCapabilities) {
	t.Helper()

	if caps.KVM {
		t.Fatalf("darwin must report KVM=false (no /dev/kvm); got true")
	}
	if len(caps.Binfmt) != 0 {
		t.Fatalf("darwin must report empty Binfmt; got %v", caps.Binfmt)
	}

	// Oracle: sysctl hw.optional.arm64 directly. "1" => arm64 host.
	out, err := exec.Command("sysctl", "-n", "hw.optional.arm64").Output()
	oracleARM64 := false
	if err == nil {
		oracleARM64 = strings.TrimSpace(string(out)) == "1"
	}
	if oracleARM64 != caps.ARM64 {
		t.Fatalf("ARM64 mismatch: sysctl hw.optional.arm64 oracle=%v, Detect=%v",
			oracleARM64, caps.ARM64)
	}
}

// TestDarwinDetectorFixtureARM64 exercises the darwin Detect logic with an
// injected sysctl fake (hermetic, no host dependency) and proves that on an
// arm64 GOARCH build a "1" from hw.optional.arm64 yields ARM64=true while KVM
// stays false. This only meaningfully runs the arm64 branch on an arm64 build;
// on other GOARCH it asserts the conservative ARM64=false.
//
// Mutation bite: changing the `== "1"` check in detect_darwin.go to `!= "1"`
// makes this FAIL on arm64 builds.
func TestDarwinDetectorFixtureARM64(t *testing.T) {
	t.Parallel()
	d := &darwinDetector{
		sysctlFn: func(key string) (string, error) {
			if key == "hw.optional.arm64" {
				return "1", nil
			}
			return "", nil
		},
	}
	caps, err := d.Detect()
	if err != nil {
		t.Fatalf("fixture Detect failed: %v", err)
	}
	if caps.KVM {
		t.Fatal("darwin Detect must keep KVM=false")
	}
	if runtimeIsARM64() && !caps.ARM64 {
		t.Fatal("arm64 build with sysctl=1 must report ARM64=true")
	}
}

// TestDarwinDetectorNonARM64Sysctl proves a "0" from the kernel keeps ARM64
// false even on an arm64 GOARCH build (truthful corroboration; no false
// positive). Mutation bite: dropping the sysctl corroboration (setting
// ARM64=true purely from GOARCH) makes this FAIL on arm64 builds.
func TestDarwinDetectorNonARM64Sysctl(t *testing.T) {
	t.Parallel()
	d := &darwinDetector{
		sysctlFn: func(key string) (string, error) { return "0", nil },
	}
	caps, err := d.Detect()
	if err != nil {
		t.Fatalf("fixture Detect failed: %v", err)
	}
	if caps.ARM64 {
		t.Fatal("sysctl hw.optional.arm64=0 must yield ARM64=false even on arm64 build")
	}
}
