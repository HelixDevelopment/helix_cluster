# helix-raftd Observability (Prometheus `/metrics`) — HXC-987

| Field | Value |
|---|---|
| Status | active |
| Scope | `cmd/helix-raftd/metrics.go`, `cmd/helix-raftd/admin.go` |
| Materials | `observability/helix-raftd/prometheus.yml`, `observability/helix-raftd/grafana-dashboard.json` |
| Last reviewed against code | 2026-06-13 |

This document describes the metrics that the `helix-raftd` daemon exposes and how
to wire them into Prometheus + Grafana. Every metric listed below is emitted by
`cmd/helix-raftd/metrics.go` (`handleMetrics`) — nothing here is aspirational. If
a metric is not in the table below, the daemon does not emit it.

---

## 1. What the daemon exposes

`helix-raftd` serves a Prometheus text-exposition (`version=0.0.4`) endpoint at:

```
GET <admin-addr>/metrics
```

The endpoint is registered on the node's **admin** HTTP mux
(`cmd/helix-raftd/admin.go`, `newAdminServer`) — the same listener as `/status`,
`/get`, and `/put` — so it is served on the node's `--admin` host:port, **not**
the raft TCP `--bind` port. Only `GET` is accepted (any other method returns
`405`).

Each sample is read **live at scrape time** from the real raft node
(`node.Raft().Stats()`, `node.IsLeader()`, `node.LastIndex()`) and its FSM
(`node.FSM().Len()`, `node.FSM().AppliedCount()`). Nothing is cached or
hard-coded: a follower reports `helix_raftd_is_leader 0`, the leader reports `1`;
`helix_raftd_fsm_keys` rises after a committed PUT; and so on.

Every series carries a single label `node_id`, set to this node's `--id`
(e.g. `n1`).

---

## 2. Metric reference

All meanings below are copied verbatim from the `# HELP` text emitted by
`cmd/helix-raftd/metrics.go`.

| Metric | Type | Label | Meaning |
|---|---|---|---|
| `helix_raftd_is_leader` | gauge | `node_id` | Whether this node is the current raft leader (1) or not (0). |
| `helix_raftd_term` | gauge | `node_id` | Current raft term as seen by this node. |
| `helix_raftd_last_log_index` | gauge | `node_id` | Index of this node's last raft log entry. |
| `helix_raftd_commit_index` | gauge | `node_id` | Highest raft log index known to be committed on this node. |
| `helix_raftd_applied_index` | gauge | `node_id` | Highest raft log index applied to this node's FSM. |
| `helix_raftd_last_snapshot_index` | gauge | `node_id` | Index of this node's most recent raft snapshot (0 if none). |
| `helix_raftd_fsm_keys` | gauge | `node_id` | Number of keys currently stored in this node's replicated key/value FSM. |
| `helix_raftd_fsm_applied_total` | counter | `node_id` | Total number of commands this node's FSM has applied since process start. |

Notes on the underlying values (`metrics.go`):

- `term`, `commit_index`, `applied_index`, and `last_snapshot_index` are parsed
  from `node.Raft().Stats()`; a missing/unparseable key emits `0`, never a panic
  (`statUint`).
- `last_log_index` prefers `Stats()["last_log_index"]` and falls back to
  `node.LastIndex()` if that key is absent.

---

## 3. Wiring up Prometheus

Use the provided scrape config: `observability/helix-raftd/prometheus.yml`. It
defines a `helix-raftd` job with `metrics_path: /metrics` and one `static_configs`
target per node's **admin** address. Replace the `127.0.0.1:800x` placeholders
with your real admin addresses.

For a local three-node cluster the `--admin` flags map directly to the scrape
targets:

```
helix-raftd --id n1 --bind 127.0.0.1:7001 --admin 127.0.0.1:8001 \
    --data-dir ./data/n1 \
    --peers n1=127.0.0.1:7001,n2=127.0.0.1:7002,n3=127.0.0.1:7003
# n2 -> --admin 127.0.0.1:8002, n3 -> --admin 127.0.0.1:8003
```

→ scrape targets `127.0.0.1:8001`, `127.0.0.1:8002`, `127.0.0.1:8003`.

Run Prometheus against it:

```
prometheus --config.file=observability/helix-raftd/prometheus.yml
```

You can sanity-check a single node's exposition without Prometheus:

```
curl -s http://127.0.0.1:8001/metrics
```

---

## 4. Importing the Grafana dashboard

Import `observability/helix-raftd/grafana-dashboard.json` (Grafana → Dashboards →
New → Import → Upload JSON file). On import, select your Prometheus data source
for the `DS_PROMETHEUS` input. The dashboard has a multi-select `node_id`
template variable (populated by `label_values(helix_raftd_is_leader, node_id)`)
and these panels, all built only on the metrics in §2:

- **Cluster Leadership (is_leader by node)** — `helix_raftd_is_leader` per node.
- **Leader count (should be 1)** — `sum(helix_raftd_is_leader)`.
- **Raft Term** — `helix_raftd_term`.
- **Log vs Commit vs Applied index** — `helix_raftd_last_log_index`,
  `helix_raftd_commit_index`, `helix_raftd_applied_index`.
- **FSM keys** — `helix_raftd_fsm_keys`.
- **FSM applied rate (cmd/s)** — `rate(helix_raftd_fsm_applied_total[5m])`.
- **Last snapshot index** — `helix_raftd_last_snapshot_index`.

---

## 5. Example PromQL

**Alert: no leader** (election in progress or quorum lost) — fires when the sum
of the leadership gauge across the cluster is `0`:

```promql
sum(helix_raftd_is_leader) == 0
```

**Alert: split brain** — more than one node simultaneously claims leadership
(should never persist):

```promql
sum(helix_raftd_is_leader) > 1
```

**Apply/commit lag on a node** — last appended log entry is ahead of what has
been applied to the FSM:

```promql
helix_raftd_last_log_index - helix_raftd_applied_index
```

**FSM divergence between replicas** — in a converged cluster every node's key
count is identical, so this is `0`:

```promql
max(helix_raftd_fsm_keys) - min(helix_raftd_fsm_keys)
```

---

See also: [`docs/consensus.md`](consensus.md) for the Raft subsystem this
instruments.
