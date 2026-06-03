package edgeheartbeat

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestEndToEndLoopbackChurn exercises the full path over a REAL local transport
// (stdlib net, loopback TCP): an emitter serialises heartbeats and sends them
// line-delimited to a server that decodes and ingests into a Collector. As the
// fake telemetry inputs change, the collector's live view must track them each
// heartbeat; advancing the injectable clock past the timeout marks the device
// offline. A time-series of (input -> observed) values is captured to
// qa-results/edgeheartbeat/<run-id>/timeseries.json as sink-side evidence.
func TestEndToEndLoopbackChurn(t *testing.T) {
	clk := &fakeClock{t: time.Unix(5000, 0)}
	collector, err := NewCollector(3*time.Second, clk.now)
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	// Server goroutine: read newline-delimited heartbeats, decode, ingest.
	ingested := make(chan Heartbeat, 16)
	var srvWG sync.WaitGroup
	srvWG.Add(1)
	go func() {
		defer srvWG.Done()
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		defer conn.Close()
		sc := bufio.NewScanner(conn)
		for sc.Scan() {
			hb, derr := DecodeHeartbeat(sc.Bytes())
			if derr != nil {
				t.Errorf("server decode: %v", derr)
				return
			}
			if ierr := collector.Ingest(hb); ierr != nil {
				t.Errorf("server ingest: %v", ierr)
				return
			}
			ingested <- hb
		}
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	src := &fakeSource{pct: 95, charging: true, temp: 38.0, netType: NetworkWiFi}
	em := NewEmitter("edge-e2e", src, clk.now)

	// Drive a sequence of distinct telemetry states; the collector's observed
	// view must equal each emitted input after it is sent.
	type step struct {
		pct      int
		charging bool
		temp     float64
		netType  string
	}
	steps := []step{
		{95, true, 38.0, NetworkWiFi},
		{72, false, 51.5, NetworkCellular},
		{60, false, 66.0, NetworkEthernet},
		{40, false, 70.5, NetworkUnknown},
	}

	type record struct {
		Seq         uint64  `json:"seq"`
		InPct       int     `json:"in_battery"`
		OutPct      int     `json:"out_battery"`
		InTemp      float64 `json:"in_temp"`
		OutTemp     float64 `json:"out_temp"`
		InNet       string  `json:"in_net"`
		OutNet      string  `json:"out_net"`
		OnlineAfter bool    `json:"online_after"`
	}
	var series []record

	for i, s := range steps {
		src.pct, src.charging, src.temp, src.netType = s.pct, s.charging, s.temp, s.netType
		clk.advance(500 * time.Millisecond) // stay within the 3s timeout
		hb := em.Sample()
		line, eerr := hb.Encode()
		if eerr != nil {
			t.Fatalf("encode step %d: %v", i, eerr)
		}
		if _, werr := conn.Write(append(line, '\n')); werr != nil {
			t.Fatalf("write step %d: %v", i, werr)
		}

		// Wait for the server to ingest this heartbeat.
		select {
		case got := <-ingested:
			if got.Seq != hb.Seq {
				t.Fatalf("seq mismatch: got %d want %d", got.Seq, hb.Seq)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timeout waiting for ingest of step %d", i)
		}

		v, ok := collector.View("edge-e2e")
		if !ok {
			t.Fatalf("device not in collector after step %d", i)
		}
		// The cluster's view MUST reflect the just-emitted inputs.
		if v.Last.BatteryPercent != s.pct || v.Last.NetworkType != s.netType || v.Last.CPUTempC != s.temp {
			t.Fatalf("step %d view mismatch: got bat=%d net=%s temp=%.1f want bat=%d net=%s temp=%.1f",
				i, v.Last.BatteryPercent, v.Last.NetworkType, v.Last.CPUTempC,
				s.pct, s.netType, s.temp)
		}
		series = append(series, record{
			Seq: hb.Seq, InPct: s.pct, OutPct: v.Last.BatteryPercent,
			InTemp: s.temp, OutTemp: v.Last.CPUTempC,
			InNet: s.netType, OutNet: v.Last.NetworkType, OnlineAfter: v.Online,
		})
	}

	// Churn: stop emitting and advance the clock past the timeout.
	if !collector.Online("edge-e2e") {
		t.Fatal("device should be online before churn gap")
	}
	clk.advance(4 * time.Second) // > 3s timeout
	if collector.Online("edge-e2e") {
		t.Fatal("device should be OFFLINE after churn gap")
	}
	if off := collector.Reap(); len(off) != 1 || off[0] != "edge-e2e" {
		t.Fatalf("Reap should report edge-e2e offline, got %v", off)
	}

	conn.Close()
	ln.Close()
	srvWG.Wait()

	// Sink-side evidence: write the time-series with a run id.
	runID := fmt.Sprintf("run-%d", time.Now().UnixNano())
	dir := filepath.Join(repoRoot(t), "qa-results", "edgeheartbeat", runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir evidence: %v", err)
	}
	evidence := map[string]any{
		"run_id":         runID,
		"device_id":      "edge-e2e",
		"transport":      "loopback-tcp",
		"timeout_secs":   3,
		"final_offline":  !collector.Online("edge-e2e"),
		"timeseries":     series,
	}
	b, _ := json.MarshalIndent(evidence, "", "  ")
	path := filepath.Join(dir, "timeseries.json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write evidence: %v", err)
	}
	t.Logf("sink-side evidence written: %s", path)
}

// repoRoot walks up from the test's CWD to find the module root (go.mod).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Fall back to CWD if go.mod not found.
			wd, _ := os.Getwd()
			return wd
		}
		dir = parent
	}
}
