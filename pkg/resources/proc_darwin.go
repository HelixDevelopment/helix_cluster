//go:build darwin

package resources

import (
	"bufio"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// DarwinReader reads real resource information on macOS using sysctl and
// vm_stat. No cgo is used: the reader shells out to the standard macOS
// command-line tools that are always present on any macOS installation.
//
// CLAUDE-2 compliance: this replaces proc_mock.go for darwin so macOS no
// longer returns fabricated data. Every value returned reflects real hardware.
type DarwinReader struct {
	// sysctlFn and vmStatFn are injectable for unit-test fixture replay.
	// In production they are left nil and the real system tools are called.
	sysctlFn func(key string) (string, error)
	vmStatFn func() (string, error)
}

// NewDarwinReader creates a DarwinReader backed by real sysctl / vm_stat.
func NewDarwinReader() *DarwinReader {
	return &DarwinReader{}
}

// newDarwinReaderWithFakes creates a DarwinReader whose sysctl and vm_stat
// calls are replaced by caller-supplied fakes. Used in unit tests to inject
// fixture output without touching real hardware.
func newDarwinReaderWithFakes(
	sysctlFn func(key string) (string, error),
	vmStatFn func() (string, error),
) *DarwinReader {
	return &DarwinReader{
		sysctlFn: sysctlFn,
		vmStatFn: vmStatFn,
	}
}

// sysctl runs "sysctl -n <key>" and returns the trimmed output.
func (r *DarwinReader) sysctl(key string) (string, error) {
	if r.sysctlFn != nil {
		return r.sysctlFn(key)
	}
	out, err := exec.Command("sysctl", "-n", key).Output()
	if err != nil {
		return "", fmt.Errorf("sysctl -n %s: %w", key, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// vmStat runs "vm_stat" and returns the raw output.
func (r *DarwinReader) vmStat() (string, error) {
	if r.vmStatFn != nil {
		return r.vmStatFn()
	}
	out, err := exec.Command("vm_stat").Output()
	if err != nil {
		return "", fmt.Errorf("vm_stat: %w", err)
	}
	return string(out), nil
}

// Read implements Reader. It populates CPU and Memory from real macOS
// facilities. GPU is left zero (handled by DarwinGPUReader separately).
func (r *DarwinReader) Read(nodeID string) (NodeResources, error) {
	cpu, err := r.ReadCPUInfo()
	if err != nil {
		return NodeResources{}, fmt.Errorf("darwin read cpu: %w", err)
	}
	mem, err := r.ReadMemInfo()
	if err != nil {
		return NodeResources{}, fmt.Errorf("darwin read mem: %w", err)
	}
	return NodeResources{
		NodeID: nodeID,
		CPU:    cpu,
		Memory: mem,
	}, nil
}

// ReadCPUInfo reads the real CPU count and model from sysctl on macOS.
// hw.ncpu gives logical CPU count. machdep.cpu.brand_string gives the model
// (absent on Apple Silicon; hw.model is the fallback there).
func (r *DarwinReader) ReadCPUInfo() (CPUInfo, error) {
	ncpuStr, err := r.sysctl("hw.ncpu")
	if err != nil {
		return CPUInfo{}, fmt.Errorf("sysctl hw.ncpu: %w", err)
	}
	ncpu, err := strconv.Atoi(strings.TrimSpace(ncpuStr))
	if err != nil {
		return CPUInfo{}, fmt.Errorf("parse hw.ncpu %q: %w", ncpuStr, err)
	}
	if ncpu <= 0 {
		return CPUInfo{}, fmt.Errorf("hw.ncpu is %d: not a valid cpu count", ncpu)
	}

	// Try Intel brand string first; Apple Silicon does not expose this key.
	model, _ := r.sysctl("machdep.cpu.brand_string")
	if model == "" {
		// Apple Silicon: use hw.model as a human-readable fallback.
		model, _ = r.sysctl("hw.model")
	}

	return CPUInfo{
		Cores: ncpu,
		Model: model,
	}, nil
}

// ReadMemInfo reads real memory statistics from macOS.
// hw.memsize gives total physical RAM in bytes (exact, from kernel).
// vm_stat provides per-page free/inactive/speculative counts; page size is
// parsed from vm_stat's header line to avoid a second sysctl call.
func (r *DarwinReader) ReadMemInfo() (MemoryInfo, error) {
	// Total physical RAM: hw.memsize is in bytes.
	memsizeStr, err := r.sysctl("hw.memsize")
	if err != nil {
		return MemoryInfo{}, fmt.Errorf("sysctl hw.memsize: %w", err)
	}
	totalBytes, err := strconv.ParseInt(strings.TrimSpace(memsizeStr), 10, 64)
	if err != nil {
		return MemoryInfo{}, fmt.Errorf("parse hw.memsize %q: %w", memsizeStr, err)
	}
	if totalBytes <= 0 {
		return MemoryInfo{}, fmt.Errorf("hw.memsize %d: not a valid byte count", totalBytes)
	}

	// Available memory: sum "Pages free" + "Pages inactive" + "Pages speculative".
	// These are the pages that can be immediately reclaimed without swapping.
	raw, err := r.vmStat()
	if err != nil {
		return MemoryInfo{}, fmt.Errorf("vm_stat: %w", err)
	}
	pageSize, freePages, err := parseVMStat(raw)
	if err != nil {
		return MemoryInfo{}, fmt.Errorf("parse vm_stat: %w", err)
	}

	availableBytes := freePages * pageSize
	usedBytes := totalBytes - availableBytes
	if usedBytes < 0 {
		usedBytes = 0
	}
	return MemoryInfo{
		TotalKB:     totalBytes / 1024,
		AvailableKB: availableBytes / 1024,
		UsedKB:      usedBytes / 1024,
	}, nil
}

// parseVMStat parses "vm_stat" output and returns (pageSize, freePages, err).
// freePages is the sum of "Pages free", "Pages inactive", and
// "Pages speculative" — these are pages that can be used without eviction.
//
// The header line encodes the page size: "Mach Virtual Memory Statistics:
// (page size of N bytes)". All other relevant lines are "Pages <label>: N."
// format. Malformed lines for known keys are a hard error.
func parseVMStat(raw string) (pageSize int64, freePages int64, err error) {
	scanner := bufio.NewScanner(strings.NewReader(raw))
	gotPageSize := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// Header: "Mach Virtual Memory Statistics: (page size of N bytes)"
		if strings.HasPrefix(line, "Mach Virtual Memory Statistics:") {
			// Extract page size from the parenthesised suffix.
			if i := strings.Index(line, "(page size of "); i >= 0 {
				rest := line[i+len("(page size of "):]
				rest = strings.TrimSuffix(rest, ")")
				rest = strings.TrimSuffix(strings.TrimSpace(rest), " bytes")
				rest = strings.TrimSpace(rest)
				ps, e := strconv.ParseInt(rest, 10, 64)
				if e != nil {
					return 0, 0, fmt.Errorf("vm_stat header page size %q: %w", rest, e)
				}
				if ps <= 0 {
					return 0, 0, fmt.Errorf("vm_stat page size %d: not positive", ps)
				}
				pageSize = ps
				gotPageSize = true
			}
			continue
		}

		// Data lines: "Pages <label>:   N." — note trailing period.
		if !strings.HasPrefix(line, "Pages ") {
			continue
		}
		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			continue
		}
		label := strings.TrimSpace(line[len("Pages "):colonIdx])
		valStr := strings.TrimSpace(line[colonIdx+1:])
		valStr = strings.TrimSuffix(valStr, ".")
		valStr = strings.TrimSpace(valStr)

		switch label {
		case "free", "inactive", "speculative":
			v, e := strconv.ParseInt(valStr, 10, 64)
			if e != nil {
				return 0, 0, fmt.Errorf("vm_stat Pages %s: parse %q: %w", label, valStr, e)
			}
			if v < 0 {
				return 0, 0, fmt.Errorf("vm_stat Pages %s: negative value %d", label, v)
			}
			freePages += v
		}
	}
	if e := scanner.Err(); e != nil {
		return 0, 0, fmt.Errorf("vm_stat scan: %w", e)
	}
	if !gotPageSize {
		return 0, 0, fmt.Errorf("vm_stat: page size not found in header")
	}
	return pageSize, freePages, nil
}

// Ensure DarwinReader satisfies the Reader interface.
var _ Reader = (*DarwinReader)(nil)
