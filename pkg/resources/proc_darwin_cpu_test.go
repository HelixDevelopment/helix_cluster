//go:build darwin

package resources

import (
	"testing"
)

// golden `top -l 2 -n 0` excerpt: two "CPU usage:" lines; only the second
// (real interval delta) must be used.
const goldenTopTwoSamples = `Processes: 600 total, 2 running, 598 sleeping
2026/06/01 21:00:00
Load Avg: 3.20, 3.10, 2.95
CPU usage: 50.00% user, 30.00% sys, 20.00% idle
SharedLibs: 400M resident
CPU usage: 6.00% user, 14.00% sys, 80.00% idle
PhysMem: 17G used`

func TestParseTopCPUBusy_UsesSecondSample(t *testing.T) {
	busy, err := parseTopCPUBusy(goldenTopTwoSamples)
	if err != nil {
		t.Fatalf("parseTopCPUBusy: %v", err)
	}
	// Second sample: 100 - 80.00 idle = 20.00 busy (NOT the first sample's 80).
	if busy != 20.0 {
		t.Fatalf("busy = %.2f, want 20.00 (100 - 80.00 idle from the SECOND sample)", busy)
	}
}

func TestParseTopCPUBusy_NoCPULine(t *testing.T) {
	if _, err := parseTopCPUBusy("Processes: 1 total\nPhysMem: 1G used"); err == nil {
		t.Fatal("expected error when no 'CPU usage:' line present, got nil")
	}
}

func TestReadCPUInfo_UsedCoresFromInjectedTop(t *testing.T) {
	r := &DarwinReader{
		sysctlFn: func(key string) (string, error) {
			switch key {
			case "hw.ncpu":
				return "10", nil
			case "machdep.cpu.brand_string":
				return "", nil
			case "hw.model":
				return "Mac15,9", nil
			}
			return "", nil
		},
		topFn: func() (string, error) { return goldenTopTwoSamples, nil },
	}
	info, err := r.ReadCPUInfo()
	if err != nil {
		t.Fatalf("ReadCPUInfo: %v", err)
	}
	if info.Cores != 10 {
		t.Fatalf("Cores = %d, want 10", info.Cores)
	}
	// busy 20% of 10 cores = 2.0 used cores.
	if info.UsedCores != 2.0 {
		t.Fatalf("UsedCores = %.3f, want 2.000 (20%% busy * 10 cores)", info.UsedCores)
	}
}

// In fixture mode WITHOUT an injected top sampler, UsedCores must stay 0 and
// ReadCPUInfo must NOT shell to the real `top` (hermeticity guarantee).
func TestReadCPUInfo_FixtureModeNoTop_UsedCoresZero(t *testing.T) {
	r := &DarwinReader{
		sysctlFn: func(key string) (string, error) {
			if key == "hw.ncpu" {
				return "8", nil
			}
			return "", nil
		},
	}
	info, err := r.ReadCPUInfo()
	if err != nil {
		t.Fatalf("ReadCPUInfo: %v", err)
	}
	if info.UsedCores != 0 {
		t.Fatalf("UsedCores = %.3f, want 0 in fixture mode without a top sampler", info.UsedCores)
	}
}
