# edge/mqttclient — Helix Edge MQTT Client (HXC-1178)

A real MQTT client for Helix **edge worker nodes**. It lets an edge node:

- **receive work-dispatch** messages on `helix/edge/<node_id>/dispatch`, and
- **publish status/heartbeat** messages on `helix/edge/<node_id>/status`.

It wraps the maintained Eclipse Paho client
(`github.com/eclipse/paho.mqtt.golang`, MQTT 3.1.1) and uses **QoS1**
(at-least-once) for all dispatch and status traffic so work items and
heartbeats are never silently dropped.

This is a **self-contained Go module** with its own `go.mod` (module path
`github.com/HelixDevelopment/helix_cluster/edge/mqttclient`). It does not
participate in the parent `go.work`; a local `go.work` pins just this module
so it builds and tests standalone with no environment overrides.

## Features

- `Connect(ctx)` with **auto-reconnect** (Paho `SetAutoReconnect`, capped
  backoff) and an `OnConnect` hook for resubscribing after reconnects.
- `SubscribeDispatch(ctx, handler)` — QoS1 subscription on the node's
  dispatch topic; decodes each `WorkItem` and routes it to `handler`.
  Malformed payloads are logged and skipped (a poison message cannot stall
  the subscription).
- `SubscribeStatus(ctx, topic, handler)` — QoS1 subscription used by a
  dispatcher/controller (e.g. on `AllStatusTopic()` = `helix/edge/+/status`).
- `PublishStatus(ctx, Status)` — QoS1 publish of a heartbeat to the node's
  own status topic.
- `PublishDispatch(ctx, targetNodeID, WorkItem)` — dispatcher-side QoS1
  publish of a work item to a node's dispatch topic.
- `Close(quiesce)` — graceful, idempotent disconnect that flushes in-flight
  messages.

Topic construction / parsing (`DispatchTopic`, `StatusTopic`,
`AllStatusTopic`, `NodeIDFromTopic`) is pure and unit-tested without a broker.

## Usage

```go
edge, _ := mqttclient.New(mqttclient.Config{
    Broker: "tcp://127.0.0.1:1883",
    NodeID: "node-7",
})
_ = edge.Connect(ctx)
_ = edge.SubscribeDispatch(ctx, func(w mqttclient.WorkItem) error {
    // run the work item ...
    return nil
})
_ = edge.PublishStatus(ctx, mqttclient.Status{State: mqttclient.StateOnline})
defer edge.Close(time.Second)
```

## Tests

```bash
# Unit tests only (no broker) — integration test SKIPs honestly:
cd edge/mqttclient && go test ./...

# Full suite against a REAL broker (round-trip proof):
podman run -d --name mosq -p 18831:18831 docker.io/library/eclipse-mosquitto:2 \
  sh -c 'printf "listener 18831 0.0.0.0\nallow_anonymous true\nlog_type all\n" > /tmp/m.conf && exec mosquitto -c /tmp/m.conf -v'
cd edge/mqttclient && MQTT_ADDR=127.0.0.1:18831 go test ./... -race -v
```

The integration test (`integration_test.go`) is gated on `MQTT_ADDR`: it
connects two real clients (a dispatcher and an edge node) over TCP, asserts
the dispatched `WorkItem` payload round-trips exactly, and asserts the edge
node's heartbeat is received by the dispatcher — all at QoS1. When
`MQTT_ADDR` is unset it `t.Skip`s with the reason (never a fake pass).

## DEFERRED

Running this on real SBC / Android edge hardware over a cellular/edge
network is device-gated and **DEFERRED** to the device integration
environment. The client logic and the real-broker round-trip are fully
proven here (see `qa-results/hxc-1178/`).
