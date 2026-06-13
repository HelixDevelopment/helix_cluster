# HXC-1178 — Evidence Summary

**Verdict: DONE — real-broker QoS1 round-trip proven.**

## Broker

- Image: `docker.io/library/eclipse-mosquitto:2` (mosquitto 2.1.2), via podman.
- Started (no host bind-mount; config generated inside container because the
  podman machine has no host mounts):

  ```bash
  podman run -d --name hxc1178-mosq -p 18831:18831 \
    docker.io/library/eclipse-mosquitto:2 \
    sh -c 'printf "listener 18831 0.0.0.0\nallow_anonymous true\nlog_type all\n" > /tmp/m.conf && exec mosquitto -c /tmp/m.conf -v'
  ```

- Listener: TCP `0.0.0.0:18831`. Verified reachable (`nc -z` succeeded).

## Round-trip proven (from broker.log)

```
New client connected ... as it-edge-...        # edge node CONNECT
Received SUBSCRIBE from it-edge-...             # SUBSCRIBE helix/edge/it-node-1/dispatch
New client connected ... as it-disp-...         # dispatcher CONNECT
Received SUBSCRIBE from it-disp-...             # SUBSCRIBE helix/edge/+/status
Received PUBLISH from it-disp-... (q1, 'helix/edge/it-node-1/dispatch', 139 bytes)
Sending  PUBLISH to   it-edge-... (q1, 'helix/edge/it-node-1/dispatch', 139 bytes)
Received PUBACK from it-edge-...                # QoS1 ack (dispatch leg)
Received PUBLISH from it-edge-... (q1, 'helix/edge/it-node-1/status', 133 bytes)
Sending  PUBLISH to   it-disp-... (q1, 'helix/edge/it-node-1/status', 133 bytes)
Received PUBACK from it-disp-...                # QoS1 ack (status leg)
```

`q1` on every PUBLISH = QoS1 at-least-once, broker-mediated over TCP (no
in-memory fake). Topic routing confirmed: dispatch on
`helix/edge/it-node-1/dispatch`, status on `helix/edge/it-node-1/status`,
dispatcher matched via wildcard `helix/edge/+/status`.

## Tests

`go test ./... -race -v` (with broker) — 11/11 PASS:
- `TestRoundTripDispatchAndStatus` — real-broker dispatch + status round-trip,
  exact payload assertion.
- `TestConnectReconnect` — connect / IsConnected / graceful idempotent close.
- 9 unit tests (topics, encode/decode, validation) — run without a broker.

Without `MQTT_ADDR`, the two integration tests `t.Skip` with reason (no fake
pass).

Artifacts: `qa-results/hxc-1178/<timestamp>/{build.txt,integration-test.txt,broker.log}`.
