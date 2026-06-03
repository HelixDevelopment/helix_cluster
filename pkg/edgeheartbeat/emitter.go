package edgeheartbeat

import "time"

// Emitter builds successive Heartbeats for one device. Each call to Sample
// reads the current telemetry from the injected TelemetrySource, stamps the
// current time from the injected clock, and assigns the next monotonically
// increasing Seq. This makes the emitter fully deterministic in tests while
// using a REAL telemetry source in production.
type Emitter struct {
	deviceID string
	src      TelemetrySource
	now      func() time.Time
	seq      uint64
}

// NewEmitter creates an Emitter for deviceID reading from src. If now is nil,
// time.Now (UTC) is used.
func NewEmitter(deviceID string, src TelemetrySource, now func() time.Time) *Emitter {
	if now == nil {
		now = time.Now
	}
	return &Emitter{deviceID: deviceID, src: src, now: now}
}

// Sample reads current telemetry and returns the next Heartbeat. Seq increments
// by one on every call, starting at 1.
func (e *Emitter) Sample() Heartbeat {
	e.seq++
	pct, charging := e.src.Battery()
	return Heartbeat{
		DeviceID:       e.deviceID,
		Timestamp:      e.now().UTC(),
		Seq:            e.seq,
		BatteryPercent: pct,
		Charging:       charging,
		CPUTempC:       e.src.CPUTempC(),
		NetworkType:    e.src.NetworkType(),
	}
}
