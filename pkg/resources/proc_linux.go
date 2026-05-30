//go:build linux

package resources

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ProcReader reads resource information from /proc on Linux.
type ProcReader struct{}

// NewProcReader creates a new ProcReader.
func NewProcReader() *ProcReader {
	return &ProcReader{}
}

// Read collects CPU and memory info for the given node ID.
func (p *ProcReader) Read(nodeID string) (NodeResources, error) {
	cpu, err := p.ReadCPUInfo()
	if err != nil {
		return NodeResources{}, fmt.Errorf("read cpuinfo: %w", err)
	}
	mem, err := p.ReadMemInfo()
	if err != nil {
		return NodeResources{}, fmt.Errorf("read meminfo: %w", err)
	}
	return NodeResources{
		NodeID: nodeID,
		CPU:    cpu,
		Memory: mem,
	}, nil
}

// ReadCPUInfo parses /proc/cpuinfo for core count, model, and frequency.
func (p *ProcReader) ReadCPUInfo() (CPUInfo, error) {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return CPUInfo{}, err
	}
	defer f.Close()

	var cores int
	var model string
	var freq float64
	seenModel := make(map[string]struct{})

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		switch key {
		case "processor":
			cores++
		case "model name":
			if _, ok := seenModel[val]; !ok {
				seenModel[val] = struct{}{}
				model = val
			}
		case "cpu MHz":
			if v, err := strconv.ParseFloat(val, 64); err == nil && v > freq {
				freq = v
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return CPUInfo{}, err
	}
	if cores == 0 {
		cores = 1
	}
	return CPUInfo{
		Cores:     cores,
		Model:     model,
		Frequency: freq,
	}, nil
}

// ReadMemInfo parses /proc/meminfo for total and available memory.
func (p *ProcReader) ReadMemInfo() (MemoryInfo, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return MemoryInfo{}, err
	}
	defer f.Close()

	var total, available int64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		key := strings.TrimSuffix(parts[0], ":")
		val, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			total = val
		case "MemAvailable":
			available = val
		}
	}
	if err := scanner.Err(); err != nil {
		return MemoryInfo{}, err
	}
	used := total - available
	if used < 0 {
		used = 0
	}
	return MemoryInfo{
		TotalKB:     total,
		AvailableKB: available,
		UsedKB:      used,
	}, nil
}
