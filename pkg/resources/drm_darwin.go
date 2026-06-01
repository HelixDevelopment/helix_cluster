//go:build darwin

package resources

import (
	"bufio"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// DarwinGPUReader probes GPU information on macOS.
//
// Two modes of operation:
//
//  1. Production (Base == ""): calls "system_profiler SPDisplaysDataType" via
//     IOKit to retrieve real GPU data reported by the OS. On Apple Silicon the
//     integrated GPU shows up here honestly (count=1, model="Apple M*",
//     memory=0 because unified memory is not a fixed VRAM allocation).
//
//  2. Test/fixture (Base != ""): uses the same portable DRM sysfs tree parser
//     as Linux (ParseDRMTree in drm.go) against the supplied base path. This
//     lets cross-OS unit tests in drm_test.go build a fake tree under
//     t.TempDir() and exercise real parsing logic on darwin without hardware.
//
// CLAUDE-2 compliance: replaces drm_other.go for darwin so macOS no longer
// returns a fabricated stub answer. spFn is injectable for unit tests.
type DarwinGPUReader struct {
	// Base, when non-empty, switches to the portable DRM tree parser instead
	// of system_profiler. Mirrors the Linux DRMGPUReader.Base field.
	Base string
	// spFn replaces the real system_profiler call in unit tests.
	spFn func() (string, error)
}

// NewDarwinGPUReader creates a DarwinGPUReader backed by real system_profiler.
func NewDarwinGPUReader() *DarwinGPUReader {
	return &DarwinGPUReader{}
}

// NewDRMGPUReader creates a DarwinGPUReader backed by real system_profiler.
// Name mirrors the Linux constructor for cross-platform caller compatibility.
func NewDRMGPUReader() *DarwinGPUReader {
	return &DarwinGPUReader{}
}

// NewDRMGPUReaderAt creates a DarwinGPUReader pinned to a base path.
//
// When base is non-empty the reader uses the portable ParseDRMTree parser
// (drm.go) against that path — identical to the Linux behaviour. This is what
// lets drm_test.go cross-OS tests work on darwin against a fake sysfs tree.
//
// When base is empty the reader uses real system_profiler output.
func NewDRMGPUReaderAt(base string) *DarwinGPUReader {
	return &DarwinGPUReader{Base: base}
}

// newDarwinGPUReaderWithFake creates a DarwinGPUReader with an injectable
// system_profiler output function for deterministic unit tests.
func newDarwinGPUReaderWithFake(spFn func() (string, error)) *DarwinGPUReader {
	return &DarwinGPUReader{spFn: spFn}
}

// systemProfiler runs system_profiler SPDisplaysDataType and returns output.
func (r *DarwinGPUReader) systemProfiler() (string, error) {
	if r.spFn != nil {
		return r.spFn()
	}
	out, err := exec.Command("system_profiler", "SPDisplaysDataType").Output()
	if err != nil {
		return "", fmt.Errorf("system_profiler SPDisplaysDataType: %w", err)
	}
	return string(out), nil
}

// Read implements Reader. It populates GPU using system_profiler (production)
// or the portable DRM tree parser (test/fixture mode). Other fields are left
// zero so the aggregator merges GPU data without clobbering CPU/memory.
func (r *DarwinGPUReader) Read(nodeID string) (NodeResources, error) {
	var gpu GPUInfo
	var err error

	if r.Base != "" {
		// Test/fixture mode: portable DRM tree parser over the fake sysfs tree.
		gpu, err = ReadGPUInfo(r.Base)
	} else {
		// Production mode: real system_profiler.
		gpu, err = r.ReadGPUInfo()
	}
	if err != nil {
		return NodeResources{}, fmt.Errorf("darwin gpu read: %w", err)
	}
	return NodeResources{NodeID: nodeID, GPU: gpu}, nil
}

// ReadGPUInfo parses real GPU information from system_profiler output.
func (r *DarwinGPUReader) ReadGPUInfo() (GPUInfo, error) {
	raw, err := r.systemProfiler()
	if err != nil {
		return GPUInfo{}, err
	}
	return parseSPDisplays(raw)
}

// parseSPDisplays parses the text output of "system_profiler SPDisplaysDataType"
// into GPUInfo. It counts each GPU entry (block starting with "Chipset Model:")
// and extracts the first entry's model name.
//
// The parser is deliberately lenient: unknown keys are ignored, and a missing
// "Total Number of Cores:" is not an error (discrete cards don't expose this).
// Memory stays 0 for integrated/Apple Silicon (unified memory has no fixed
// VRAM allocation to report — 0 is the honest value, not a fabrication).
func parseSPDisplays(raw string) (GPUInfo, error) {
	type gpuEntry struct {
		chipset string
		vendor  string
		cores   int
	}

	var entries []gpuEntry
	var cur *gpuEntry

	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// A GPU block starts with "Chipset Model:".
		if strings.HasPrefix(trimmed, "Chipset Model:") {
			model := strings.TrimSpace(strings.TrimPrefix(trimmed, "Chipset Model:"))
			entry := gpuEntry{chipset: model}
			entries = append(entries, entry)
			cur = &entries[len(entries)-1]
			continue
		}
		if cur == nil {
			continue
		}
		if strings.HasPrefix(trimmed, "Vendor:") {
			cur.vendor = strings.TrimSpace(strings.TrimPrefix(trimmed, "Vendor:"))
			continue
		}
		if strings.HasPrefix(trimmed, "Total Number of Cores:") {
			coresStr := strings.TrimSpace(strings.TrimPrefix(trimmed, "Total Number of Cores:"))
			c, err := strconv.Atoi(coresStr)
			if err != nil {
				return GPUInfo{}, fmt.Errorf("system_profiler GPU cores %q: %w", coresStr, err)
			}
			cur.cores = c
			continue
		}
	}
	if err := scanner.Err(); err != nil {
		return GPUInfo{}, fmt.Errorf("parse system_profiler: %w", err)
	}

	if len(entries) == 0 {
		// No GPU entries found — honest zero-GPU result (headless/no display).
		return GPUInfo{Count: 0}, nil
	}

	// Count = number of entries; Model = first chipset (most representative).
	// Memory = 0 (Apple Silicon/integrated GPUs use unified memory; no fixed VRAM).
	info := GPUInfo{
		Count: len(entries),
		Model: entries[0].chipset,
	}
	return info, nil
}

// Ensure DarwinGPUReader satisfies the Reader interface.
var _ Reader = (*DarwinGPUReader)(nil)
