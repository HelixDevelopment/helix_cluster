// Package console provides console hardware detection and adaptation for Helix Cluster OS.
//
// This package detects PlayStation 4/5 and other console hardware signatures,
// monitors thermal zones, queries GPU compute capabilities, and manages
// trust levels for console nodes joining the cluster.
//
// On non-Linux platforms, the package operates in NoOp mode and returns
// generic fallback values.
package console

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ConsoleType identifies the detected console hardware.
type ConsoleType string

const (
	TypeUnknown ConsoleType = "unknown"
	TypePS4     ConsoleType = "ps4"
	TypePS4Pro  ConsoleType = "ps4pro"
	TypePS5     ConsoleType = "ps5"
	TypePS5Pro  ConsoleType = "ps5pro"
)

// Detector holds configuration for console hardware detection.
type Detector struct {
	procCPUInfoPath string
	deviceTreePath  string
}

// NewDetector creates a new console hardware detector.
func NewDetector() *Detector {
	return &Detector{
		procCPUInfoPath: "/proc/cpuinfo",
		deviceTreePath:  "/proc/device-tree",
	}
}

// Detect returns the detected console type and a confidence score (0.0-1.0).
// On non-Linux platforms it always returns TypeUnknown with 0 confidence.
func (d *Detector) Detect() (ConsoleType, float64, error) {
	if runtime.GOOS != "linux" {
		return TypeUnknown, 0.0, nil
	}

	ct, conf, err := d.detectLinux()
	if err != nil {
		return TypeUnknown, 0.0, err
	}
	return ct, conf, nil
}

func (d *Detector) detectLinux() (ConsoleType, float64, error) {
	// Check /proc/cpuinfo for known console CPU signatures.
	cpuType, cpuConf, err := d.checkCPUInfo()
	if err != nil && !os.IsNotExist(err) {
		return TypeUnknown, 0.0, fmt.Errorf("cpuinfo check: %w", err)
	}
	if cpuConf >= 0.8 {
		return cpuType, cpuConf, nil
	}

	// Check device tree for console-compatible strings.
	dtType, dtConf, err := d.checkDeviceTree()
	if err != nil && !os.IsNotExist(err) {
		return TypeUnknown, 0.0, fmt.Errorf("device-tree check: %w", err)
	}
	if dtConf >= 0.8 {
		return dtType, dtConf, nil
	}

	// Merge confidence if both partial matches exist.
	if cpuConf > 0 && dtConf > 0 {
		merged := cpuConf + dtConf*(1-cpuConf)
		if merged >= 0.8 {
			if cpuType != TypeUnknown {
				return cpuType, merged, nil
			}
			return dtType, merged, nil
		}
	}
	if cpuConf > dtConf {
		return cpuType, cpuConf, nil
	}
	return dtType, dtConf, nil
}

func (d *Detector) checkCPUInfo() (ConsoleType, float64, error) {
	f, err := os.Open(d.procCPUInfoPath)
	if err != nil {
		return TypeUnknown, 0.0, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, strings.ToLower(scanner.Text()))
	}
	if err := scanner.Err(); err != nil {
		return TypeUnknown, 0.0, err
	}

	score := 0.0
	var detected ConsoleType = TypeUnknown

	for _, line := range lines {
		// AMD Jaguar (PS4 / PS4 Pro)
		if strings.Contains(line, "amd") && strings.Contains(line, "jaguar") {
			score += 0.4
			detected = TypePS4
		}
		if strings.Contains(line, "amd") && strings.Contains(line, "zen 2") {
			score += 0.4
			detected = TypePS5
		}
		// Specific model name hints.
		if strings.Contains(line, "playstation") || strings.Contains(line, "ps4") || strings.Contains(line, "ps5") {
			score += 0.6
			if strings.Contains(line, "ps5") {
				detected = TypePS5
			} else if strings.Contains(line, "ps4") {
				detected = TypePS4
			}
		}
	}

	if score > 1.0 {
		score = 1.0
	}
	return detected, score, nil
}

func (d *Detector) checkDeviceTree() (ConsoleType, float64, error) {
	compatPath := filepath.Join(d.deviceTreePath, "compatible")
	data, err := os.ReadFile(compatPath)
	if err != nil {
		return TypeUnknown, 0.0, err
	}

	s := strings.ToLower(string(data))
	score := 0.0
	var detected ConsoleType = TypeUnknown

	if strings.Contains(s, "ps5") || strings.Contains(s, "playstation5") {
		score = 1.0
		detected = TypePS5
	} else if strings.Contains(s, "ps4") || strings.Contains(s, "playstation4") {
		score = 1.0
		detected = TypePS4
	} else if strings.Contains(s, "sony") && strings.Contains(s, "console") {
		score = 0.5
	}

	return detected, score, nil
}

// IsConsole returns true if the detector believes it is running on console hardware.
func (d *Detector) IsConsole() bool {
	ct, conf, _ := d.Detect()
	return ct != TypeUnknown && conf >= 0.5
}
