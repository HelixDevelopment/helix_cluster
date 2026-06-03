// Package powergater implements a charging-gated scheduling guard (HXC-1175).
//
// An agent that runs on a battery-powered or thermally constrained host must
// not blindly accept compute work: doing so can drain a laptop battery, run a
// device hot, or violate a "only work while plugged in / only at night" policy.
// PowerGater consults a real per-OS PowerSource (battery %, charging state, CPU
// temperature, wall-clock time) and returns an accept/refuse decision together
// with a machine-parseable reason string the scheduler can log and act on.
//
// CLAUDE-2 compliance: the PowerSource interface has REAL per-OS
// implementations (reader_darwin.go via pmset/powermetrics, reader_linux.go via
// /sys/class/power_supply and /sys/class/thermal). No OS runs on a mock for
// real operation; the fake source here exists ONLY for exhaustive unit tests.
package powergater

import (
	"fmt"
	"math"
	"time"
)

// TempUnknown is the sentinel CPUTempC value returned by a PowerSource when the
// host exposes no unprivileged way to read CPU temperature (e.g. Apple Silicon
// without root powermetrics). The gate treats an unknown temperature as
// "not hot" rather than refusing work — refusing on missing telemetry would
// make the guard unusable on such hosts. This is the documented behaviour
// referenced by HXC-1175.
var TempUnknown = math.NaN()

// PowerSource provides the real, measured power/thermal/clock state of the host.
// Implementations are build-tag split per OS (reader_darwin.go / reader_linux.go)
// and each returns real values for its platform.
type PowerSource interface {
	// Charging reports whether the host is currently running on external power.
	Charging() bool
	// BatteryPercent reports the current battery charge in the range [0,100].
	// On a host with no battery (desktop/server) implementations return 100,
	// since "no battery" can never be "battery low".
	BatteryPercent() int
	// CPUTempC reports the current CPU package temperature in degrees Celsius,
	// or TempUnknown (NaN) when the host exposes no readable temperature.
	CPUTempC() float64
	// Now reports the current wall-clock time, used for the night-mode window.
	Now() time.Time
}

// NightWindow is a wall-clock hour window [StartHour, EndHour) on a 24h clock,
// used by NightModeOnly. When StartHour > EndHour the window wraps across
// midnight (e.g. Start=22, End=6 means 22:00..06:00). A window where
// StartHour == EndHour is treated as "the whole day" (always night).
type NightWindow struct {
	StartHour int
	EndHour   int
}

// contains reports whether the given hour [0,23] falls inside the window.
func (w NightWindow) contains(hour int) bool {
	if w.StartHour == w.EndHour {
		return true
	}
	if w.StartHour < w.EndHour {
		return hour >= w.StartHour && hour < w.EndHour
	}
	// Wrapping window across midnight.
	return hour >= w.StartHour || hour < w.EndHour
}

// PowerConfig is the operator-supplied policy for accepting work.
type PowerConfig struct {
	// OnlyWhenCharging refuses work unless the host is on external power.
	OnlyWhenCharging bool
	// MinBatteryPercent refuses work when the battery is strictly below this
	// level (a guard against draining the host). 0 disables the check.
	MinBatteryPercent int
	// NightModeOnly, when true, restricts work to the NightWindow hours.
	NightModeOnly bool
	// NightWindow is the permitted wall-clock window when NightModeOnly is set.
	NightWindow NightWindow
	// MaxCpuTempC refuses work when the measured CPU temperature is strictly
	// above this threshold. 0 disables the check. An unknown (NaN) temperature
	// never trips this check.
	MaxCpuTempC float64
}

// PowerGater is the scheduling guard. It is constructed with a policy and a
// real PowerSource and consulted via CanAcceptWork before each work unit.
type PowerGater struct {
	cfg PowerConfig
	src PowerSource
}

// New constructs a PowerGater from a policy and a power source. The source must
// be non-nil; callers on a real host pass NewPowerSource(), tests pass a fake.
func New(cfg PowerConfig, src PowerSource) *PowerGater {
	return &PowerGater{cfg: cfg, src: src}
}

// Reason string constants returned by CanAcceptWork. The empty string means
// "accept". Battery and temperature reasons embed the live measured value.
const (
	ReasonAccept      = ""
	ReasonNotCharging = "not_charging"
	ReasonDaytime     = "daytime"
)

// CanAcceptWork reports whether the agent may accept a work unit right now.
// It returns (true, "") to accept, or (false, reason) to refuse, where reason
// is one of:
//
//	"not_charging"     OnlyWhenCharging is set and the host is on battery.
//	"battery_low_X%"   battery (X%) is below MinBatteryPercent.
//	"daytime"          NightModeOnly is set and now is outside the NightWindow.
//	"cpu_hot_XC"       CPU temperature (X°C) is above MaxCpuTempC.
//
// Checks are evaluated in a fixed priority order so the reason is deterministic
// when several would trip: charging, battery, night-window, temperature.
func (g *PowerGater) CanAcceptWork() (bool, string) {
	if g.cfg.OnlyWhenCharging && !g.src.Charging() {
		return false, ReasonNotCharging
	}

	if g.cfg.MinBatteryPercent > 0 {
		if pct := g.src.BatteryPercent(); pct < g.cfg.MinBatteryPercent {
			return false, fmt.Sprintf("battery_low_%d%%", pct)
		}
	}

	if g.cfg.NightModeOnly {
		hour := g.src.Now().Hour()
		if !g.cfg.NightWindow.contains(hour) {
			return false, ReasonDaytime
		}
	}

	if g.cfg.MaxCpuTempC > 0 {
		temp := g.src.CPUTempC()
		// An unknown (NaN) temperature is treated as not-hot, by design.
		if !math.IsNaN(temp) && temp > g.cfg.MaxCpuTempC {
			return false, fmt.Sprintf("cpu_hot_%gC", temp)
		}
	}

	return true, ReasonAccept
}
