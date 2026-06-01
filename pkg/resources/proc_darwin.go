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
	// sysctlFn, vmStatFn and topFn are injectable for unit-test fixture replay.
	// In production they are left nil and the real system tools are called.
	sysctlFn func(key string) (string, error)
	vmStatFn func() (string, error)
	topFn    func() (string, error)
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

// errNoCPUSampler is returned by topCPU in fixture mode (sysctl faked but no
// top fake injected) so ReadCPUInfo leaves UsedCores at 0 without shelling to
// the real `top` — keeping fixture-based unit tests hermetic.
var errNoCPUSampler = fmt.Errorf("darwin: no CPU sampler in fixture mode")

// topCPU runs "top -l 2 -n 0" and returns the raw output. The second sample is
// computed over a real ~1s interval, so its "CPU usage:" line reflects actual
// utilization (the first sample is since-boot and useless for a point reading).
func (r *DarwinReader) topCPU() (string, error) {
	if r.topFn != nil {
		return r.topFn()
	}
	if r.sysctlFn != nil {
		// Fixture mode without an injected top sampler: do not touch real `top`.
		return "", errNoCPUSampler
	}
	out, err := exec.Command("top", "-l", "2", "-n", "0").Output()
	if err != nil {
		return "", fmt.Errorf("top -l 2 -n 0: %w", err)
	}
	return string(out), nil
}

// readUsedCores returns the number of busy logical cores (utilizationFraction *
// ncpu) sampled from `top`. Best-effort: callers treat an error as "utilization
// unavailable" and leave UsedCores at 0 rather than failing the whole read.
func (r *DarwinReader) readUsedCores(ncpu int) (float64, error) {
	out, err := r.topCPU()
	if err != nil {
		return 0, err
	}
	busyPct, err := parseTopCPUBusy(out)
	if err != nil {
		return 0, err
	}
	return busyPct / 100.0 * float64(ncpu), nil
}

// parseTopCPUBusy parses macOS `top -l 2` output and returns the busy CPU
// percentage (100 - idle) from the LAST "CPU usage:" line, e.g.
//
//	CPU usage: 6.66% user, 13.33% sys, 80.00% idle
//
// returns 19.99. Uses the last occurrence because `top -l 2` prints two
// samples and only the second reflects a real interval delta.
func parseTopCPUBusy(raw string) (float64, error) {
	var lastLine string
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "CPU usage:") {
			lastLine = line
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("top scan: %w", err)
	}
	if lastLine == "" {
		return 0, fmt.Errorf("top: no 'CPU usage:' line found")
	}
	// Find the token immediately preceding "% idle".
	idx := strings.Index(lastLine, "% idle")
	if idx < 0 {
		return 0, fmt.Errorf("top: no idle field in %q", lastLine)
	}
	prefix := strings.TrimSpace(lastLine[:idx])
	fields := strings.Fields(prefix)
	idleTok := fields[len(fields)-1]
	idle, err := strconv.ParseFloat(idleTok, 64)
	if err != nil {
		return 0, fmt.Errorf("top: parse idle %q: %w", idleTok, err)
	}
	if idle < 0 || idle > 100 {
		return 0, fmt.Errorf("top: idle %.2f out of range", idle)
	}
	return 100.0 - idle, nil
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

	info := CPUInfo{
		Cores: ncpu,
		Model: model,
	}
	// Best-effort live utilization via `top`. In fixture mode (sysctl faked, no
	// top fake) this returns errNoCPUSampler and UsedCores stays 0; in production
	// it reflects real busy cores so callers (metrics collector) see true CPU%.
	if used, err := r.readUsedCores(ncpu); err == nil {
		info.UsedCores = used
	}
	return info, nil
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
