package edgeheartbeat

import (
	"testing"
	"time"
)

// fakeClock is an injectable, advanceable clock.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestCollectorRejectsNonPositiveTimeout(t *testing.T) {
	if _, err := NewCollector(0, nil); err == nil {
		t.Fatal("expected error for zero timeout")
	}
	if _, err := NewCollector(-time.Second, nil); err == nil {
		t.Fatal("expected error for negative timeout")
	}
}

func TestCollectorIngestUpdatesLiveView(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c, err := NewCollector(10*time.Second, clk.now)
	if err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{pct: 80, charging: false, temp: 40, netType: NetworkWiFi}
	em := NewEmitter("dev-A", src, clk.now)

	if err := c.Ingest(em.Sample()); err != nil {
		t.Fatalf("ingest 1: %v", err)
	}
	v, ok := c.View("dev-A")
	if !ok || !v.Online {
		t.Fatalf("expected online device, got ok=%v view=%+v", ok, v)
	}
	if v.Last.BatteryPercent != 80 || v.Last.NetworkType != NetworkWiFi || v.Last.CPUTempC != 40 {
		t.Fatalf("view did not reflect first sample: %+v", v.Last)
	}

	// Change telemetry inputs; the collector's view must track them.
	src.pct = 55
	src.netType = NetworkCellular
	src.temp = 61
	clk.advance(2 * time.Second)
	if err := c.Ingest(em.Sample()); err != nil {
		t.Fatalf("ingest 2: %v", err)
	}
	v, _ = c.View("dev-A")
	if v.Last.BatteryPercent != 55 {
		t.Fatalf("battery not updated: got %d want 55", v.Last.BatteryPercent)
	}
	if v.Last.NetworkType != NetworkCellular {
		t.Fatalf("network not updated: got %s want cellular", v.Last.NetworkType)
	}
	if v.Last.CPUTempC != 61 {
		t.Fatalf("temp not updated: got %.1f want 61", v.Last.CPUTempC)
	}
	if v.Last.Seq != 2 {
		t.Fatalf("seq not incremented: got %d want 2", v.Last.Seq)
	}
}

func TestCollectorMarksOfflineAfterTimeout(t *testing.T) {
	clk := &fakeClock{t: time.Unix(2000, 0)}
	c, _ := NewCollector(5*time.Second, clk.now)
	src := &fakeSource{pct: 90, charging: true, temp: 30, netType: NetworkEthernet}
	em := NewEmitter("dev-B", src, clk.now)
	if err := c.Ingest(em.Sample()); err != nil {
		t.Fatal(err)
	}

	// Within timeout: still online.
	clk.advance(4 * time.Second)
	if !c.Online("dev-B") {
		t.Fatal("device should still be online within timeout")
	}

	// Past timeout: offline, and Reap reports it.
	clk.advance(2 * time.Second) // total 6s > 5s
	if c.Online("dev-B") {
		t.Fatal("device should be offline past timeout")
	}
	offline := c.Reap()
	if len(offline) != 1 || offline[0] != "dev-B" {
		t.Fatalf("Reap should list dev-B, got %v", offline)
	}
}

func TestCollectorRejectsStaleSeq(t *testing.T) {
	clk := &fakeClock{t: time.Unix(3000, 0)}
	c, _ := NewCollector(10*time.Second, clk.now)
	h2 := Heartbeat{DeviceID: "d", Seq: 2, BatteryPercent: 50, NetworkType: NetworkWiFi, CPUTempC: 30}
	h1 := Heartbeat{DeviceID: "d", Seq: 1, BatteryPercent: 10, NetworkType: NetworkWiFi, CPUTempC: 30}
	if err := c.Ingest(h2); err != nil {
		t.Fatal(err)
	}
	if err := c.Ingest(h1); err == nil {
		t.Fatal("expected rejection of stale seq 1 after seq 2")
	}
	v, _ := c.View("d")
	if v.Last.BatteryPercent != 50 {
		t.Fatalf("stale heartbeat must not overwrite view: got %d", v.Last.BatteryPercent)
	}
}

func TestCollectorReappearanceClearsOffline(t *testing.T) {
	clk := &fakeClock{t: time.Unix(4000, 0)}
	c, _ := NewCollector(5*time.Second, clk.now)
	src := &fakeSource{pct: 70, charging: false, temp: 35, netType: NetworkWiFi}
	em := NewEmitter("dev-C", src, clk.now)
	_ = c.Ingest(em.Sample())

	clk.advance(6 * time.Second)
	if c.Online("dev-C") {
		t.Fatal("expected offline after churn gap")
	}
	// Device churns back in.
	if err := c.Ingest(em.Sample()); err != nil {
		t.Fatal(err)
	}
	if !c.Online("dev-C") {
		t.Fatal("device should be online again after reappearing")
	}
}
