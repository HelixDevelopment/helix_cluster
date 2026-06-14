//go:build darwin

package tierdetect

import (
	"errors"
	"runtime"
	"testing"
)

// TestDarwinArchClassifierVariants is the ARCH-DETECTION centerpiece on THIS
// host (darwin). It drives the REAL darwinDetector.Detect with representative
// `sysctl hw.optional.arm64` output variants (CLAUDE-2: fixture bytes through
// the real per-OS classifier, not a mock of the feature) and pins:
//   - "1" => ARM64 true (on arm64 build)
//   - "1\n" / " 1 " (whitespace variants) => still true (trim-robust)
//   - "0" => ARM64 false (truthful corroboration, no false positive)
//   - ""  (key absent) => ARM64 false (ABSENT-SIGNAL safe default)
//   - sysctl error => ARM64 false (probe failure degrades to absent)
//   - KVM is ALWAYS false and Binfmt ALWAYS empty regardless of arch.
//
// Mutation bite: changing the `== "1"` arm64 corroboration to `!= "1"`, or
// dropping the corroboration so GOARCH alone sets ARM64, FAILS the "0"/""/error
// cases on an arm64 build.
func TestDarwinArchClassifierVariants(t *testing.T) {
	t.Parallel()

	type tc struct {
		name    string
		out     string
		err     error
		wantARM bool // expected ARM64 ON AN arm64 BUILD; non-arm64 builds always false
	}
	sentinel := errors.New("sysctl: nonexistent key")
	cases := []tc{
		{"plain-1", "1", nil, true},
		{"trailing-newline", "1\n", nil, true},
		{"surrounding-space", " 1 ", nil, true},
		{"zero", "0", nil, false},
		{"empty-key-absent", "", nil, false},
		{"probe-error", "", sentinel, false},
		{"garbage", "yes", nil, false},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			d := &darwinDetector{
				sysctlFn: func(key string) (string, error) {
					if key == "hw.optional.arm64" {
						return c.out, c.err
					}
					return "", nil
				},
			}
			caps, err := d.Detect()
			if err != nil {
				t.Fatalf("Detect must not error on classifier inputs; got %v", err)
			}
			// KVM and Binfmt invariants hold for EVERY input.
			if caps.KVM {
				t.Fatalf("[%s] darwin KVM must always be false", c.name)
			}
			if len(caps.Binfmt) != 0 {
				t.Fatalf("[%s] darwin Binfmt must always be empty; got %v", c.name, caps.Binfmt)
			}
			if caps.Arch != runtime.GOARCH {
				t.Fatalf("[%s] Arch=%q must equal GOARCH=%q", c.name, caps.Arch, runtime.GOARCH)
			}
			// ARM64 is only ever true on an arm64 build AND a "1" signal.
			want := c.wantARM && runtime.GOARCH == "arm64"
			if caps.ARM64 != want {
				t.Fatalf("[%s] ARM64=%v, want %v (GOARCH=%s, sysctl=%q err=%v)",
					c.name, caps.ARM64, want, runtime.GOARCH, c.out, c.err)
			}
		})
	}
}

// TestDarwinKVMRequiredAlwaysRejected ties the darwin KVM=false truth to the
// ValidateTier contract end to end: a tier requiring KVM on a real macOS host
// must ALWAYS be rejected with the macOS-specific detail. Mutation bite:
// hard-coding KVM:true in detect_darwin.go FAILS this (the tier would pass).
func TestDarwinKVMRequiredAlwaysRejected(t *testing.T) {
	t.Parallel()
	caps, err := NewDetector().Detect()
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}
	err = ValidateTier(TierRequirement{Name: "TVIRT", RequireKVM: true}, caps)
	var mce *MissingCapabilityError
	if !errors.As(err, &mce) {
		t.Fatalf("KVM-required tier on macOS must be rejected; got %T %v", err, err)
	}
	if mce.Capability != "KVM" {
		t.Fatalf("missing capability = %q, want KVM", mce.Capability)
	}
	if !darwinDetailContains(mce.Detail, "macOS") {
		t.Fatalf("darwin KVM rejection detail should mention macOS; got %q", mce.Detail)
	}
}

func darwinDetailContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
