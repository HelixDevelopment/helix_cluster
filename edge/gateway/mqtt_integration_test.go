package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	mqttclient "github.com/HelixDevelopment/helix_cluster/edge/mqttclient"
)

// mqttBroker returns the broker endpoint to test against. It honors the
// HELIX_MQTT_BROKER env var (e.g. "tcp://127.0.0.1:18831") and otherwise probes
// a small set of common local endpoints. If none is reachable the caller SKIPs
// honestly with the exact reason (CLAUDE-1: no fake PASS without a real broker).
func mqttBroker(t *testing.T) string {
	t.Helper()
	candidates := []string{}
	if v := os.Getenv("HELIX_MQTT_BROKER"); v != "" {
		candidates = append(candidates, v)
	}
	candidates = append(candidates,
		"tcp://127.0.0.1:18831", // podman eclipse-mosquitto mapped port (this run)
		"tcp://127.0.0.1:1883",  // default mosquitto port
	)
	for _, b := range candidates {
		hostport := b
		if i := len("tcp://"); len(b) > i && b[:i] == "tcp://" {
			hostport = b[i:]
		}
		conn, err := net.DialTimeout("tcp", hostport, 1500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return b
		}
	}
	t.Skipf("MQTT integration SKIPPED: no broker reachable. Tried %v. "+
		"Start one (e.g. `podman run -d -p 18831:1883 eclipse-mosquitto:2` with anonymous enabled) "+
		"and/or set HELIX_MQTT_BROKER, then re-run.", candidates)
	return ""
}

// TestCrossTransport_MQTTStatusToWS proves the MQTT -> gateway -> WS path end to
// end over a REAL broker: an MQTT-attached worker publishes a Status; the MQTT
// bridge transport converts it to a KindStatus envelope and routes it; a real
// WebSocket observer of that node receives the byte-exact status snapshot.
//
// This exercises heterogeneous routing into the gateway FROM MQTT and out over
// a different wire protocol (WebSocket), which is the cross-transport guarantee.
func TestCrossTransport_MQTTStatusToWS(t *testing.T) {
	broker := mqttBroker(t)

	gw := New()
	defer gw.Close()

	// WS observer side.
	wst := NewWebSocketTransport(gw)
	srv := httptest.NewServer(wst.Handler())
	defer srv.Close()
	wsBase := wsURL(srv.URL, "/edge")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// MQTT bridge transport (subscribes wildcard status into the gateway).
	mt, err := NewMQTTTransport(ctx, gw, broker, "helix-gw-bridge-status")
	if err != nil {
		t.Fatalf("start MQTT transport: %v", err)
	}
	if !mt.Client().IsConnected() {
		t.Fatalf("MQTT bridge reports not connected to %s", broker)
	}
	t.Logf("MQTT bridge connected to real broker %s", broker)

	// Real WS observer of node-M's status. (A status with explicit Dest is
	// delivered to that node's registered conns; we register a "node-M"
	// observer over WS so it receives node-M's fan-back.)
	observer, err := DialWS(ctx, wsBase, "controller")
	if err != nil {
		t.Fatalf("dial WS controller: %v", err)
	}
	defer observer.Close()
	waitForRoutes(t, gw, map[string]int{"controller": 1})

	// A real MQTT worker publishes its status to the broker.
	worker, err := mqttclient.New(mqttclient.Config{Broker: broker, NodeID: "node-M", ClientID: "node-M-worker"})
	if err != nil {
		t.Fatalf("new MQTT worker: %v", err)
	}
	if err := worker.Connect(ctx); err != nil {
		t.Fatalf("connect MQTT worker: %v", err)
	}
	defer worker.Close(time.Second)

	// Give the bridge's wildcard subscription a moment to be active on the broker.
	time.Sleep(300 * time.Millisecond)

	st := mqttclient.Status{
		NodeID:     "node-M",
		State:      mqttclient.StateBusy,
		ActiveJobs: 3,
		Timestamp:  time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := worker.PublishStatus(ctx, st); err != nil {
		t.Fatalf("worker.PublishStatus: %v", err)
	}

	// The status fans back to the "controller" observer over WS (it is the only
	// other registered conn, and the status source is node-M).
	got, err := observer.Recv(8 * time.Second)
	if err != nil {
		t.Fatalf("WS controller did not receive MQTT-originated status: %v", err)
	}
	if got.Kind != KindStatus || got.Source != "node-M" {
		t.Fatalf("routing error: got kind=%q source=%q", got.Kind, got.Source)
	}
	var decoded mqttclient.Status
	if err := json.Unmarshal(got.Payload, &decoded); err != nil {
		t.Fatalf("status payload not decodable: %v", err)
	}
	if decoded.NodeID != "node-M" || decoded.State != mqttclient.StateBusy || decoded.ActiveJobs != 3 {
		t.Fatalf("status content mismatch: %+v", decoded)
	}
	t.Logf("CROSS-TRANSPORT OK: MQTT->gateway->WS delivered status source=%s state=%s jobs=%d (real broker %s)",
		got.Source, decoded.State, decoded.ActiveJobs, broker)
}

// TestCrossTransport_QUICtoMQTTDispatch proves the gateway -> MQTT path: a real
// QUIC client dispatches work to a node attached over the MQTT bridge, and a
// real MQTT worker subscribed to that node's dispatch topic receives the
// byte-exact work item (QUIC -> gateway -> MQTT). This is heterogeneous routing
// OUT of the gateway onto the broker.
func TestCrossTransport_QUICtoMQTTDispatch(t *testing.T) {
	broker := mqttBroker(t)

	gw := New()
	defer gw.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	// MQTT bridge + register node-M as an MQTT-reachable destination.
	mt, err := NewMQTTTransport(ctx, gw, broker, "helix-gw-bridge-dispatch")
	if err != nil {
		t.Fatalf("start MQTT transport: %v", err)
	}
	if _, err := mt.RegisterNode("node-M"); err != nil {
		t.Fatalf("register MQTT node-M: %v", err)
	}

	// Real MQTT worker subscribes to its dispatch topic to receive the work.
	worker, err := mqttclient.New(mqttclient.Config{Broker: broker, NodeID: "node-M", ClientID: "node-M-dispatch-worker"})
	if err != nil {
		t.Fatalf("new MQTT worker: %v", err)
	}
	if err := worker.Connect(ctx); err != nil {
		t.Fatalf("connect MQTT worker: %v", err)
	}
	defer worker.Close(time.Second)

	recvCh := make(chan mqttclient.WorkItem, 1)
	if err := worker.SubscribeDispatch(ctx, func(w mqttclient.WorkItem) error {
		select {
		case recvCh <- w:
		default:
		}
		return nil
	}); err != nil {
		t.Fatalf("worker.SubscribeDispatch: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	// QUIC sender.
	srvCfg, cliCfg := quicTestConfigs(t)
	qt, err := NewQUICTransport(gw, "127.0.0.1:0", srvCfg)
	if err != nil {
		t.Fatalf("start quic transport: %v", err)
	}
	sender, err := DialQUIC(ctx, qt.Addr(), "scheduler", cliCfg)
	if err != nil {
		t.Fatalf("dial QUIC scheduler: %v", err)
	}
	defer sender.Close()
	waitForRoutes(t, gw, map[string]int{"scheduler": 1, "node-M": 1})

	payload := json.RawMessage(`{"task":"build","target":"helixd","flags":["-race"]}`)
	dispatch := Envelope{
		ID:       "xt-quic-mqtt-1",
		Kind:     KindDispatch,
		Source:   "scheduler",
		Dest:     "node-M",
		Payload:  payload,
		IssuedAt: time.Now().UTC(),
	}
	if err := sender.Send(dispatch); err != nil {
		t.Fatalf("QUIC sender.Send: %v", err)
	}

	select {
	case w := <-recvCh:
		if w.ID != "xt-quic-mqtt-1" {
			t.Fatalf("routing error: MQTT worker got id %q want xt-quic-mqtt-1", w.ID)
		}
		if !bytes.Equal(w.Payload, payload) {
			t.Fatalf("payload not byte-equal across QUIC->MQTT: got %q want %q", w.Payload, payload)
		}
		t.Logf("CROSS-TRANSPORT OK: QUIC->gateway->MQTT delivered id=%s byte-exact payload=%s (real broker %s)",
			w.ID, string(w.Payload), broker)
	case <-time.After(10 * time.Second):
		t.Fatalf("MQTT worker did not receive QUIC-originated dispatch within deadline")
	}
}
