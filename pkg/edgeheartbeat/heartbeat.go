// Package edgeheartbeat implements an edge heartbeat protocol carrying
// battery %, charging state, thermal/CPU temperature and network type, so a
// power/thermal-aware scheduler can make placement decisions and tolerate
// device churn via a configurable offline timeout.
//
// The package is self-contained: it depends only on the Go standard library.
// Telemetry is read through a single TelemetrySource interface that has a REAL
// per-OS implementation behind build tags (reader_darwin.go / reader_linux.go),
// each returning real measured host values (CLAUDE-2). The source is injectable
// so tests can drive deterministic inputs.
package edgeheartbeat

import (
	"encoding/json"
	"fmt"
	"time"
)

// NetworkType enumerates the link types an edge device may report. The string
// values are stable wire values and MUST NOT change without a protocol bump.
const (
	NetworkWiFi     = "wifi"
	NetworkCellular = "cellular"
	NetworkEthernet = "ethernet"
	NetworkUnknown  = "unknown"
)

// CPUTempSentinel is the CPUTempC value reported when the host genuinely
// cannot expose a CPU temperature without elevated privileges or special
// hardware access (e.g. macOS without sudo/powermetrics). It is a clearly
// out-of-band negative value so a scheduler can detect "temperature unknown"
// rather than mistaking it for a real reading. Per CLAUDE-1 this is an honest
// "unavailable" marker, never a fabricated plausible temperature.
const CPUTempSentinel = -1.0

// validNetworkTypes is the closed set of acceptable NetworkType wire values.
var validNetworkTypes = map[string]struct{}{
	NetworkWiFi:     {},
	NetworkCellular: {},
	NetworkEthernet: {},
	NetworkUnknown:  {},
}

// Heartbeat is one telemetry sample emitted by an edge device. It is the wire
// message a device sends to the cluster collector.
type Heartbeat struct {
	// DeviceID uniquely identifies the emitting device within the cluster.
	DeviceID string `json:"device_id"`
	// Timestamp is the device-local wall-clock time the sample was taken,
	// in UTC. The collector uses its own injectable clock for liveness, so
	// this field is informational/telemetry, not the churn-timeout source.
	Timestamp time.Time `json:"timestamp"`
	// Seq is a per-device monotonically increasing sequence number. A
	// collector can use it to detect reordering or dropped heartbeats.
	Seq uint64 `json:"seq"`
	// BatteryPercent is the battery charge level in [0,100]. A device with no
	// battery (e.g. mains-only) reports 100 with Charging=true.
	BatteryPercent int `json:"battery_percent"`
	// Charging is true when the device is on external power / charging.
	Charging bool `json:"charging"`
	// CPUTempC is the CPU/package temperature in Celsius, or CPUTempSentinel
	// when the host cannot expose it.
	CPUTempC float64 `json:"cpu_temp_c"`
	// NetworkType is one of the Network* constants.
	NetworkType string `json:"network_type"`
}

// Validate checks that a Heartbeat is structurally well-formed. It is applied
// on decode so a collector never ingests a malformed/garbage sample.
func (h Heartbeat) Validate() error {
	if h.DeviceID == "" {
		return fmt.Errorf("heartbeat: empty device_id")
	}
	if h.BatteryPercent < 0 || h.BatteryPercent > 100 {
		return fmt.Errorf("heartbeat: battery_percent %d out of [0,100]", h.BatteryPercent)
	}
	if _, ok := validNetworkTypes[h.NetworkType]; !ok {
		return fmt.Errorf("heartbeat: invalid network_type %q", h.NetworkType)
	}
	// CPUTempC may be the sentinel (unavailable) or a plausible real value.
	// Reject only physically impossible readings that are not the sentinel.
	if h.CPUTempC != CPUTempSentinel && (h.CPUTempC < -50 || h.CPUTempC > 150) {
		return fmt.Errorf("heartbeat: cpu_temp_c %.2f implausible", h.CPUTempC)
	}
	return nil
}

// Encode serialises the heartbeat to a single JSON line (no trailing newline).
func (h Heartbeat) Encode() ([]byte, error) {
	if err := h.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(h)
}

// DecodeHeartbeat parses a JSON heartbeat and validates it.
func DecodeHeartbeat(data []byte) (Heartbeat, error) {
	var h Heartbeat
	if err := json.Unmarshal(data, &h); err != nil {
		return Heartbeat{}, fmt.Errorf("heartbeat: decode: %w", err)
	}
	if err := h.Validate(); err != nil {
		return Heartbeat{}, err
	}
	return h, nil
}

// TelemetrySource is the single interface every OS-specific reader implements
// (CLAUDE-2). Battery returns (percent, charging). CPUTempC returns degrees
// Celsius or CPUTempSentinel. NetworkType returns one of the Network* values.
type TelemetrySource interface {
	Battery() (percent int, charging bool)
	CPUTempC() float64
	NetworkType() string
}

// NewHostTelemetrySource returns the real telemetry source for the host OS.
// Its concrete type is selected at build time (reader_darwin.go / reader_linux.go).
func NewHostTelemetrySource() TelemetrySource {
	return newHostTelemetrySource()
}
