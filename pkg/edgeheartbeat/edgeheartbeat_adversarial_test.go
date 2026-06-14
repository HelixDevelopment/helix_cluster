package edgeheartbeat

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// hb is a terse helper to build a structurally valid Heartbeat for a device/seq.
func hb(dev string, seq uint64) Heartbeat {
	return Heartbeat{
		DeviceID:       dev,
		Timestamp:      time.Unix(0, int64(seq)).UTC(),
		Seq:            seq,
		BatteryPercent: 50,
		Charging:       false,
		CPUTempC:       30,
		NetworkType:    NetworkWiFi,
	}
}

// TestOfflineBoundaryExact pins the inclusive/exclusive edge of the offline
// rule with a frozen clock at exactly timeout-1ns, timeout, and timeout+1ns
// after the last heartbeat. The production rule is online <=> now-lastSeen <
// timeout (strict), so:
//   - now-lastSeen == timeout-1ns  -> ONLINE
//   - now-lastSeen == timeout      -> OFFLINE (boundary is exclusive of timeout)
//   - now-lastSeen == timeout+1ns  -> OFFLINE
//
// This is the centerpiece: the existing suite only checks timeout-1s and
// timeout+1s, never the exact tick. A drift of the comparison to <= would flip
// the == timeout case and is caught here.
func TestOfflineBoundaryExact(t *testing.T) {
	const timeout = 5 * time.Second
	base := time.Unix(10_000, 0)

	cases := []struct {
		name       string
		elapsed    time.Duration
		wantOnline bool
	}{
		{"oneTickBeforeTimeout", timeout - time.Nanosecond, true},
		{"exactlyTimeout", timeout, false},
		{"oneTickAfterTimeout", timeout + time.Nanosecond, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clk := &fakeClock{t: base}
			c, err := NewCollector(timeout, clk.now)
			if err != nil {
				t.Fatal(err)
			}
			if err := c.Ingest(hb("dev", 1)); err != nil {
				t.Fatalf("ingest: %v", err)
			}
			clk.t = base.Add(tc.elapsed)

			// All three read paths must agree on liveness at the boundary.
			if got := c.Online("dev"); got != tc.wantOnline {
				t.Fatalf("Online()=%v want %v (elapsed=%v timeout=%v)",
					got, tc.wantOnline, tc.elapsed, timeout)
			}
			v, ok := c.View("dev")
			if !ok {
				t.Fatal("View: device missing")
			}
			if v.Online != tc.wantOnline {
				t.Fatalf("View().Online=%v want %v", v.Online, tc.wantOnline)
			}
			snap := c.Snapshot()["dev"]
			if snap.Online != tc.wantOnline {
				t.Fatalf("Snapshot().Online=%v want %v", snap.Online, tc.wantOnline)
			}
			offline := c.Reap()
			reapedOffline := len(offline) == 1 && offline[0] == "dev"
			if reapedOffline == tc.wantOnline {
				t.Fatalf("Reap offline=%v but wantOnline=%v (must be opposite)",
					reapedOffline, tc.wantOnline)
			}
		})
	}
}

// TestStaleSeqDoesNotRegressLastSeen pins the monotonicity contract: a
// reordered/replayed heartbeat (Seq <= last) is rejected and must NOT update
// Last, LastSeenAt, or revive a device. A strictly-greater Seq does update.
func TestStaleSeqDoesNotRegressLastSeen(t *testing.T) {
	const timeout = 5 * time.Second
	base := time.Unix(20_000, 0)
	clk := &fakeClock{t: base}
	c, _ := NewCollector(timeout, clk.now)

	// Fresh sample at seq 5, battery 80, recorded at base.
	fresh := hb("dev", 5)
	fresh.BatteryPercent = 80
	if err := c.Ingest(fresh); err != nil {
		t.Fatalf("ingest fresh: %v", err)
	}

	// Advance the collector clock, then replay an OLDER seq with different
	// battery. It must be rejected and must not touch LastSeenAt/Last.
	clk.t = base.Add(2 * time.Second)
	for _, staleSeq := range []uint64{1, 4, 5} { // < and == last seq
		stale := hb("dev", staleSeq)
		stale.BatteryPercent = 10
		if err := c.Ingest(stale); err == nil {
			t.Fatalf("stale seq %d accepted; monotonic guard missing", staleSeq)
		}
	}
	v, _ := c.View("dev")
	if v.Last.BatteryPercent != 80 || v.Last.Seq != 5 {
		t.Fatalf("stale heartbeat regressed Last: %+v", v.Last)
	}
	if !v.LastSeenAt.Equal(base) {
		t.Fatalf("stale heartbeat regressed LastSeenAt: got %v want %v",
			v.LastSeenAt, base)
	}

	// A strictly-greater seq DOES advance LastSeenAt to the current clock.
	newer := hb("dev", 6)
	newer.BatteryPercent = 33
	if err := c.Ingest(newer); err != nil {
		t.Fatalf("ingest newer: %v", err)
	}
	v, _ = c.View("dev")
	if v.Last.BatteryPercent != 33 || v.Last.Seq != 6 {
		t.Fatalf("newer heartbeat not applied: %+v", v.Last)
	}
	if !v.LastSeenAt.Equal(base.Add(2 * time.Second)) {
		t.Fatalf("newer heartbeat did not advance LastSeenAt: %v", v.LastSeenAt)
	}
}

// TestStaleSeqCannotReviveOfflineDevice proves a replayed old heartbeat cannot
// flap an offline device back online (it is rejected before any state change).
func TestStaleSeqCannotReviveOfflineDevice(t *testing.T) {
	const timeout = 3 * time.Second
	base := time.Unix(30_000, 0)
	clk := &fakeClock{t: base}
	c, _ := NewCollector(timeout, clk.now)

	if err := c.Ingest(hb("dev", 10)); err != nil {
		t.Fatal(err)
	}
	clk.t = base.Add(timeout) // now offline (== boundary)
	if c.Online("dev") {
		t.Fatal("precondition: device must be offline")
	}
	// Replay an old seq: rejected, stays offline.
	if err := c.Ingest(hb("dev", 4)); err == nil {
		t.Fatal("stale replay accepted")
	}
	if c.Online("dev") {
		t.Fatal("stale replay must not revive an offline device")
	}
	// A fresh, higher seq DOES revive it (recovery).
	if err := c.Ingest(hb("dev", 11)); err != nil {
		t.Fatal(err)
	}
	if !c.Online("dev") {
		t.Fatal("fresh heartbeat must bring device back online")
	}
}

// TestConcurrentIngestVsQueryNoRace is the concurrency centerpiece. Many
// goroutines hammer Ingest across many devices while other goroutines hammer
// every read accessor (View, Online, Snapshot, Reap). Under -race this fails if
// any per-node-map access is unguarded. It also asserts no panic and a
// consistent final state (every device present with its highest seq).
func TestConcurrentIngestVsQueryNoRace(t *testing.T) {
	const (
		devices   = 16
		perDevice = 400
		readers   = 8
	)
	clk := &fakeClock{t: time.Unix(40_000, 0)}
	c, err := NewCollector(time.Second, clk.now)
	if err != nil {
		t.Fatal(err)
	}

	var stop atomic.Bool
	var wg sync.WaitGroup

	// Writers: one goroutine per device, monotonically increasing seq.
	for d := 0; d < devices; d++ {
		wg.Add(1)
		go func(d int) {
			defer wg.Done()
			id := fmt.Sprintf("dev-%02d", d)
			for s := uint64(1); s <= perDevice; s++ {
				if err := c.Ingest(hb(id, s)); err != nil {
					t.Errorf("ingest %s seq %d: %v", id, s, err)
					return
				}
			}
		}(d)
	}

	// Readers: hammer every accessor concurrently with the writers.
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			for !stop.Load() {
				id := fmt.Sprintf("dev-%02d", r%devices)
				_ = c.Online(id)
				_, _ = c.View(id)
				_ = c.Snapshot()
				_ = c.Reap()
			}
		}(r)
	}

	// Let writers finish, then stop readers.
	doneWriters := make(chan struct{})
	go func() {
		// Wait only for writer goroutines via a separate counter.
		// Simpler: spin until every device has reached perDevice.
		for {
			snap := c.Snapshot()
			done := len(snap) == devices
			for _, v := range snap {
				if v.Last.Seq < perDevice {
					done = false
					break
				}
			}
			if done {
				close(doneWriters)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	select {
	case <-doneWriters:
	case <-time.After(30 * time.Second):
		t.Fatal("writers did not converge")
	}
	stop.Store(true)
	wg.Wait()

	// Consistent final state: every device present with its max seq.
	snap := c.Snapshot()
	if len(snap) != devices {
		t.Fatalf("expected %d devices, got %d", devices, len(snap))
	}
	for d := 0; d < devices; d++ {
		id := fmt.Sprintf("dev-%02d", d)
		v, ok := snap[id]
		if !ok {
			t.Fatalf("device %s missing from final snapshot", id)
		}
		if v.Last.Seq != perDevice {
			t.Fatalf("device %s final seq=%d want %d", id, v.Last.Seq, perDevice)
		}
	}
}

// TestBurstNoDropMonotonicSeq sends a tight burst of in-order heartbeats and
// asserts every one is accepted (none dropped) and the final seq is exact.
func TestBurstNoDropMonotonicSeq(t *testing.T) {
	clk := &fakeClock{t: time.Unix(50_000, 0)}
	c, _ := NewCollector(time.Minute, clk.now)
	const n = 1000
	for s := uint64(1); s <= n; s++ {
		if err := c.Ingest(hb("burst", s)); err != nil {
			t.Fatalf("burst dropped seq %d: %v", s, err)
		}
	}
	v, ok := c.View("burst")
	if !ok || v.Last.Seq != n {
		t.Fatalf("burst final seq=%d ok=%v want %d", v.Last.Seq, ok, n)
	}
}

// TestUnknownAndEmptyEdges covers querying a never-seen device and an empty
// collector across every accessor.
func TestUnknownAndEmptyEdges(t *testing.T) {
	clk := &fakeClock{t: time.Unix(60_000, 0)}
	c, _ := NewCollector(time.Second, clk.now)

	if _, ok := c.View("ghost"); ok {
		t.Fatal("View of unknown device returned ok=true")
	}
	if c.Online("ghost") {
		t.Fatal("unknown device reported online")
	}
	if snap := c.Snapshot(); len(snap) != 0 {
		t.Fatalf("empty collector snapshot non-empty: %v", snap)
	}
	if off := c.Reap(); len(off) != 0 {
		t.Fatalf("empty collector Reap non-empty: %v", off)
	}
}

// TestReapMixedFleet pins Reap's behavior on a mixed fleet at the exact
// boundary: a device whose gap == timeout is reaped offline, a device just
// inside (timeout-1ns) is not, and the returned IDs are sorted.
func TestReapMixedFleet(t *testing.T) {
	const timeout = 10 * time.Second
	base := time.Unix(70_000, 0)
	clk := &fakeClock{t: base}
	c, _ := NewCollector(timeout, clk.now)

	// online-z seen latest (just inside), offline-a oldest (at boundary).
	if err := c.Ingest(hb("offline-a", 1)); err != nil {
		t.Fatal(err)
	}
	clk.t = base.Add(timeout - time.Nanosecond) // online-z within window
	if err := c.Ingest(hb("online-z", 1)); err != nil {
		t.Fatal(err)
	}
	// Move now so offline-a's gap == timeout exactly, online-z's gap == 1ns.
	clk.t = base.Add(timeout)

	off := c.Reap()
	if len(off) != 1 || off[0] != "offline-a" {
		t.Fatalf("Reap=%v want [offline-a]", off)
	}
	// Reap mutated stored Online; Snapshot must reflect it.
	snap := c.Snapshot()
	if snap["offline-a"].Online {
		t.Fatal("offline-a should be offline after Reap")
	}
	if !snap["online-z"].Online {
		t.Fatal("online-z should remain online")
	}
}
