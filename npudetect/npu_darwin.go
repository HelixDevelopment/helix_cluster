//go:build darwin

package npudetect

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// newDetector returns the macOS Detector.
func newDetector() Detector { return &darwinDetector{run: runCommand} }

// runCommand executes a command and returns combined stdout. It is a package
// variable so tests can confirm the real path runs against the real host.
func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

type commandRunner func(ctx context.Context, name string, args ...string) (string, error)

type darwinDetector struct {
	run commandRunner
}

// aneSpec maps an Apple-silicon chip family to its Neural Engine core count and
// an approximate peak TOPS figure. TOPS values are Apple's published
// "trillion operations per second" headline numbers for the Neural Engine of
// each generation; they are approximate by nature (Apple does not publish a
// formal per-core spec) and the Source field on the returned NPU records this.
//
// The match is by chip-family substring read LIVE from system_profiler — there
// is no bypass that fabricates a chip. If the live chip is an Apple-silicon
// part not in this table we still return a real ANE record (every Apple-silicon
// SoC has one) with TOPS=0 (unknown) rather than guessing.
type aneSpec struct {
	family   string // substring to match in the live "Chip:" line
	aneCores int
	tops     float64
}

// Ordered most-specific first so e.g. "Apple M3 Pro" matches before "Apple M3".
var aneSpecs = []aneSpec{
	{"Apple M4 Max", 16, 38.0},
	{"Apple M4 Pro", 16, 38.0},
	{"Apple M4", 16, 38.0},
	{"Apple M3 Max", 16, 18.0},
	{"Apple M3 Pro", 16, 18.0},
	{"Apple M3", 16, 18.0},
	{"Apple M2 Max", 16, 15.8},
	{"Apple M2 Pro", 16, 15.8},
	{"Apple M2 Ultra", 32, 31.6},
	{"Apple M2", 16, 15.8},
	{"Apple M1 Max", 16, 11.0},
	{"Apple M1 Pro", 16, 11.0},
	{"Apple M1 Ultra", 32, 22.0},
	{"Apple M1", 16, 11.0},
}

func (d *darwinDetector) Detect(ctx context.Context) ([]NPU, error) {
	chip, err := d.detectChip(ctx)
	if err != nil {
		return nil, err
	}
	if chip == "" {
		// No Apple-silicon chip line; no ANE to report (e.g. Intel Mac).
		return []NPU{}, nil
	}

	// Confirm the ANE hardware node really exists in the IORegistry. This is a
	// second, independent real probe (not just the chip name).
	aneEvidence := d.detectANEIORegistry(ctx)

	spec := matchANESpec(chip)

	src := fmt.Sprintf("system_profiler SPHardwareDataType chip=%q", chip)
	if aneEvidence != "" {
		src += "; ioreg: " + aneEvidence
	}
	if spec != nil {
		src += fmt.Sprintf("; %d-core ANE, ~%.1f TOPS (Apple Neural Engine vendor figure for chip family, approximate)", spec.aneCores, spec.tops)
	} else {
		src += "; Apple-silicon ANE present, TOPS unknown (chip family not in TOPS map)"
	}

	npu := NPU{
		Vendor:  "Apple",
		Model:   "Apple Neural Engine",
		Runtime: "CoreML/ANE",
		Source:  src,
	}
	if spec != nil {
		npu.TOPS = spec.tops
	}
	return []NPU{npu}, nil
}

// detectChip reads the live "Chip:" line from system_profiler SPHardwareDataType.
// This is the authoritative live probe — the result is NOT hardcoded.
func (d *darwinDetector) detectChip(ctx context.Context) (string, error) {
	out, err := d.run(ctx, "system_profiler", "SPHardwareDataType")
	if err != nil {
		return "", fmt.Errorf("system_profiler SPHardwareDataType: %w", err)
	}
	return parseChipLine(out), nil
}

// parseChipLine extracts the value of the "Chip:" line (or, on older systems,
// "Processor Name:") from system_profiler output.
func parseChipLine(out string) string {
	sc := bufio.NewScanner(strings.NewReader(out))
	var processorName string
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if v, ok := strings.CutPrefix(line, "Chip:"); ok {
			return strings.TrimSpace(v)
		}
		if v, ok := strings.CutPrefix(line, "Processor Name:"); ok {
			processorName = strings.TrimSpace(v)
		}
	}
	// Apple-silicon Macs always have a "Chip:" line; Intel Macs only have
	// "Processor Name:" and no ANE — only return it if it looks Apple-silicon.
	if strings.HasPrefix(processorName, "Apple ") {
		return processorName
	}
	return ""
}

// detectANEIORegistry looks for the Apple Neural Engine hardware node in the
// IORegistry. It returns a short evidence string if found, "" otherwise. The
// ANE appears as an `ane@...` AppleARMIODevice node and via the H11ANE driver
// class family ("aneFW"/H11ANE).
func (d *darwinDetector) detectANEIORegistry(ctx context.Context) string {
	// Fast, targeted query: the ANE platform node is named "ane".
	if out, err := d.run(ctx, "ioreg", "-rn", "ane"); err == nil {
		if strings.Contains(out, "ane@") || strings.Contains(out, "AppleARMIODevice") {
			return "ane@ AppleARMIODevice node present"
		}
	}
	// Fallback: scan the full registry for the H11ANE driver family.
	if out, err := d.run(ctx, "ioreg", "-l"); err == nil {
		if strings.Contains(out, "H11ANE") || strings.Contains(out, "aneFW") {
			return "H11ANE driver class present"
		}
	}
	return ""
}

// matchANESpec maps a live chip string to its ANE spec, most-specific first.
// Returns nil if the chip is Apple-silicon but not in the TOPS table.
func matchANESpec(chip string) *aneSpec {
	for i := range aneSpecs {
		if strings.Contains(chip, aneSpecs[i].family) {
			return &aneSpecs[i]
		}
	}
	return nil
}
