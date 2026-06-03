//go:build darwin

package powergater

import (
	"bufio"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// darwinSource is the real macOS PowerSource. Battery percentage and the
// charging state are read from `pmset -g batt`, which is present on every macOS
// install and works unprivileged. CPU temperature is read best-effort from
// `powermetrics` (SMC sampler); when that is unavailable (it requires root, and
// Apple Silicon exposes no unprivileged thermal sysctl) CPUTempC returns
// TempUnknown and the gate treats the host as not-hot, as documented.
//
// CLAUDE-2: this returns real measured values on macOS — never a stub.
type darwinSource struct {
	// pmsetFn / powermetricsFn are injectable for the unit test that replays
	// captured fixture output. In production they are nil and the real tools
	// are invoked.
	pmsetFn        func() (string, error)
	powermetricsFn func() (string, error)
	nowFn          func() time.Time
}

// NewPowerSource returns the real PowerSource for this host (macOS).
func NewPowerSource() PowerSource {
	return &darwinSource{}
}

func (d *darwinSource) pmset() (string, error) {
	if d.pmsetFn != nil {
		return d.pmsetFn()
	}
	out, err := exec.Command("pmset", "-g", "batt").Output()
	if err != nil {
		return "", fmt.Errorf("pmset -g batt: %w", err)
	}
	return string(out), nil
}

func (d *darwinSource) Now() time.Time {
	if d.nowFn != nil {
		return d.nowFn()
	}
	return time.Now()
}

// pmsetBatteryRe matches the "NN%;" battery percentage token in pmset output.
var pmsetBatteryRe = regexp.MustCompile(`(\d+)%`)

// parsePmsetBattery extracts the battery percentage from `pmset -g batt`
// output. A host with no battery (desktop) yields no percentage token; callers
// treat that as 100 (can never be "low").
func parsePmsetBattery(out string) (pct int, found bool) {
	m := pmsetBatteryRe.FindStringSubmatch(out)
	if m == nil {
		return 0, false
	}
	v, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return v, true
}

// parsePmsetCharging reports whether pmset output indicates external power.
// pmset prints "Now drawing from 'AC Power'" when plugged in and
// "Now drawing from 'Battery Power'" when on battery. As a cross-check the
// per-battery line carries "charging"/"charged"/"AC attached" vs
// "discharging".
func parsePmsetCharging(out string) bool {
	lower := strings.ToLower(out)
	if strings.Contains(lower, "drawing from 'ac power'") {
		return true
	}
	if strings.Contains(lower, "drawing from 'battery power'") {
		return false
	}
	// Fall back to the per-battery status word.
	if strings.Contains(lower, "discharging") {
		return false
	}
	if strings.Contains(lower, "charging") || strings.Contains(lower, "charged") {
		return true
	}
	return false
}

func (d *darwinSource) Charging() bool {
	out, err := d.pmset()
	if err != nil {
		return false
	}
	return parsePmsetCharging(out)
}

func (d *darwinSource) BatteryPercent() int {
	out, err := d.pmset()
	if err != nil {
		return 100
	}
	pct, found := parsePmsetBattery(out)
	if !found {
		return 100
	}
	return pct
}

// powermetrics runs a single SMC sample. It requires elevated privileges; when
// it fails we return an error and CPUTempC falls back to TempUnknown.
func (d *darwinSource) powermetrics() (string, error) {
	if d.powermetricsFn != nil {
		return d.powermetricsFn()
	}
	out, err := exec.Command(
		"powermetrics", "--samplers", "smc", "-n", "1", "-i", "200",
	).Output()
	if err != nil {
		return "", fmt.Errorf("powermetrics smc: %w", err)
	}
	return string(out), nil
}

// powermetricsTempRe matches a "CPU die temperature: NN.N C" line.
var powermetricsTempRe = regexp.MustCompile(`(?i)CPU die temperature:\s*([0-9.]+)\s*C`)

// parsePowermetricsTemp extracts the CPU die temperature in Celsius from
// powermetrics SMC output, or returns (NaN/false) when absent.
func parsePowermetricsTemp(out string) (float64, bool) {
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		if m := powermetricsTempRe.FindStringSubmatch(scanner.Text()); m != nil {
			v, err := strconv.ParseFloat(m[1], 64)
			if err == nil {
				return v, true
			}
		}
	}
	return TempUnknown, false
}

func (d *darwinSource) CPUTempC() float64 {
	out, err := d.powermetrics()
	if err != nil {
		return TempUnknown
	}
	temp, ok := parsePowermetricsTemp(out)
	if !ok {
		return TempUnknown
	}
	return temp
}
