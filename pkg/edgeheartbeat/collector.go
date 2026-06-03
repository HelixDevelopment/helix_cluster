package edgeheartbeat

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// DeviceView is the collector's live, per-device picture of an edge device: the
// most recent heartbeat plus the (collector-clock) time it was received and
// whether the device is currently considered online.
type DeviceView struct {
	// Last is the most recently ingested heartbeat for this device.
	Last Heartbeat
	// LastSeenAt is the collector-clock instant the last heartbeat arrived.
	LastSeenAt time.Time
	// Online is true while LastSeenAt is within the churn timeout of "now".
	Online bool
}

// Collector ingests heartbeats over time and maintains a live view of every
// device it has seen. A device is marked OFFLINE when no heartbeat has arrived
// within Timeout (churn tolerance). The clock is injectable so liveness is
// deterministic in tests and decoupled from device-reported timestamps.
type Collector struct {
	mu      sync.RWMutex
	devices map[string]*DeviceView
	timeout time.Duration
	now     func() time.Time
}

// NewCollector creates a Collector with the given churn timeout. If now is nil,
// time.Now is used. A non-positive timeout is rejected so misconfiguration
// cannot silently disable churn detection.
func NewCollector(timeout time.Duration, now func() time.Time) (*Collector, error) {
	if timeout <= 0 {
		return nil, fmt.Errorf("collector: timeout must be > 0, got %v", timeout)
	}
	if now == nil {
		now = time.Now
	}
	return &Collector{
		devices: make(map[string]*DeviceView),
		timeout: timeout,
		now:     now,
	}, nil
}

// Ingest records a heartbeat, updating that device's live view. A heartbeat
// whose Seq is not strictly greater than the last seen Seq for the device is
// rejected as stale/reordered (returns an error and does not mutate state),
// preventing an out-of-order or replayed sample from overwriting newer data.
func (c *Collector) Ingest(h Heartbeat) error {
	if err := h.Validate(); err != nil {
		return fmt.Errorf("collector: reject invalid heartbeat: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if existing, ok := c.devices[h.DeviceID]; ok {
		if h.Seq <= existing.Last.Seq {
			return fmt.Errorf("collector: stale heartbeat for %s: seq %d <= %d",
				h.DeviceID, h.Seq, existing.Last.Seq)
		}
		existing.Last = h
		existing.LastSeenAt = now
		existing.Online = true
		return nil
	}
	c.devices[h.DeviceID] = &DeviceView{
		Last:       h,
		LastSeenAt: now,
		Online:     true,
	}
	return nil
}

// View returns the current live view for a single device. The bool is false if
// the device has never been seen. Online is recomputed against "now" so a
// device that has gone silent past the timeout reads as offline even without an
// explicit reap call.
func (c *Collector) View(deviceID string) (DeviceView, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	dv, ok := c.devices[deviceID]
	if !ok {
		return DeviceView{}, false
	}
	out := *dv
	out.Online = c.now().Sub(dv.LastSeenAt) < c.timeout
	return out, true
}

// Online reports whether the device is currently within its churn timeout.
func (c *Collector) Online(deviceID string) bool {
	dv, ok := c.View(deviceID)
	return ok && dv.Online
}

// Snapshot returns the live view of every known device, keyed by device ID,
// with Online recomputed against the current clock.
func (c *Collector) Snapshot() map[string]DeviceView {
	c.mu.RLock()
	defer c.mu.RUnlock()
	now := c.now()
	out := make(map[string]DeviceView, len(c.devices))
	for id, dv := range c.devices {
		v := *dv
		v.Online = now.Sub(dv.LastSeenAt) < c.timeout
		out[id] = v
	}
	return out
}

// Reap recomputes Online for every device against the current clock and returns
// the sorted IDs of devices that are now offline. It mutates the stored Online
// flag so a subsequent Snapshot reflects the reaped state even if the clock is
// later rewound (defensive against non-monotonic injected clocks in tests).
func (c *Collector) Reap() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	var offline []string
	for id, dv := range c.devices {
		dv.Online = now.Sub(dv.LastSeenAt) < c.timeout
		if !dv.Online {
			offline = append(offline, id)
		}
	}
	sort.Strings(offline)
	return offline
}
