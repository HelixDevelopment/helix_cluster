//go:build darwin

package npudetect

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// oracleChip independently asks system_profiler for the host chip. The detector
// MUST agree with this oracle — this is the anti-bluff cross-check: if the
// detector hardcodes a fake chip, this assertion fails.
func oracleChip(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("system_profiler", "SPHardwareDataType").Output()
	if err != nil {
		t.Fatalf("oracle system_profiler failed: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "Chip:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// chipFamily reduces a chip string to its family token, e.g.
// "Apple M3 Pro" -> "M3", "Apple M2 Ultra" -> "M2".
func chipFamily(chip string) string {
	for _, f := range strings.Fields(chip) {
		if len(f) == 2 && f[0] == 'M' && f[1] >= '0' && f[1] <= '9' {
			return f
		}
	}
	return ""
}

func TestDetectNPUs_HostHasANE(t *testing.T) {
	oracle := oracleChip(t)
	if oracle == "" || !strings.HasPrefix(oracle, "Apple ") {
		t.Skipf("host is not Apple silicon (oracle chip=%q); ANE detection N/A", oracle)
	}
	t.Logf("ORACLE chip (system_profiler SPHardwareDataType): %q", oracle)

	npus, err := DetectNPUs(context.Background())
	if err != nil {
		t.Fatalf("DetectNPUs error: %v", err)
	}
	if len(npus) < 1 {
		t.Fatalf("expected >=1 NPU on Apple-silicon host, got 0")
	}

	var ane *NPU
	for i := range npus {
		if npus[i].Vendor == "Apple" && npus[i].Model == "Apple Neural Engine" {
			ane = &npus[i]
		}
	}
	if ane == nil {
		t.Fatalf("no Apple Neural Engine in result: %+v", npus)
	}
	if ane.Vendor == "" || ane.Model == "" {
		t.Errorf("ANE has empty Vendor/Model: %+v", ane)
	}
	t.Logf("DETECTED NPU: Vendor=%q Model=%q Runtime=%q TOPS=%.1f", ane.Vendor, ane.Model, ane.Runtime, ane.TOPS)
	t.Logf("DETECTION SOURCE: %s", ane.Source)

	// ANTI-BLUFF CROSS-CHECK: the chip the detector read (embedded in Source)
	// must match the oracle's chip family. A hardcoded fake chip fails here.
	oracleFam := chipFamily(oracle)
	if oracleFam == "" {
		t.Fatalf("could not derive family from oracle chip %q", oracle)
	}
	if !strings.Contains(ane.Source, oracle) {
		t.Errorf("detector Source %q does not contain the live oracle chip %q (possible bypass/hardcode)", ane.Source, oracle)
	}
	if !strings.Contains(ane.Source, oracleFam) {
		t.Errorf("detector did not detect oracle chip family %q; Source=%q", oracleFam, ane.Source)
	}
	t.Logf("CROSS-CHECK OK: detected chip family %q matches system_profiler oracle", oracleFam)
}

// TestDetectChip_IsLive proves the chip is read live from system_profiler and
// not a constant: we run the real probe and compare to the oracle directly.
func TestDetectChip_IsLive(t *testing.T) {
	d := &darwinDetector{run: runCommand}
	chip, err := d.detectChip(context.Background())
	if err != nil {
		t.Fatalf("detectChip: %v", err)
	}
	oracle := oracleChip(t)
	if chip != oracle {
		t.Errorf("live-detected chip %q != oracle %q", chip, oracle)
	}
}

func TestParseChipLine(t *testing.T) {
	sample := "      Model Name: MacBook Pro\n      Chip: Apple M3 Pro\n      Total Number of Cores: 11\n"
	if got := parseChipLine(sample); got != "Apple M3 Pro" {
		t.Errorf("parseChipLine = %q, want %q", got, "Apple M3 Pro")
	}
	intel := "      Model Name: MacBook Pro\n      Processor Name: 6-Core Intel Core i7\n"
	if got := parseChipLine(intel); got != "" {
		t.Errorf("parseChipLine(intel) = %q, want empty", got)
	}
}

func TestMatchANESpec(t *testing.T) {
	if s := matchANESpec("Apple M3 Pro"); s == nil || s.aneCores != 16 {
		t.Errorf("M3 Pro spec wrong: %+v", s)
	}
	if s := matchANESpec("Apple M9 Ultra"); s != nil {
		t.Errorf("unknown chip should map to nil, got %+v", s)
	}
}
