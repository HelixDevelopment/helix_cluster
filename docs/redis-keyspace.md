# Helix Cluster OS — Redis Keyspace Design

## Overview

Redis Cluster serves as the distributed cache and real-time state store for Helix Cluster OS. All keys are prefixed with `clusteros:` to avoid collisions in shared Redis instances.

## Key Patterns

### Session State (CRDT-synced, short TTL)

| Key Pattern | Type | TTL | Description |
|-------------|------|-----|-------------|
| `clusteros:session:{id}:state` | Hash / JSON | 60s | Session state with vector clock |
| `clusteros:session:{id}:routing` | Hash | 60s | Node routing table for I/O |
| `clusteros:session:{id}:windows` | List | 60s | Ordered window list |
| `clusteros:session:{id}:panes` | List | 60s | Ordered pane list |
| `clusteros:session:{id}:active_window` | String | 60s | Currently focused window UUID |
| `clusteros:session:{id}:crdt_clock` | String | 60s | Vector clock JSON |

### Node Hot Data

| Key Pattern | Type | TTL | Description |
|-------------|------|-----|-------------|
| `clusteros:node:{id}:resources` | Hash | 30s | Current resource availability |
| `clusteros:node:{id}:health` | Hash | 30s | Latest health snapshot |
| `clusteros:node:{id}:metrics` | Sorted Set | 300s | Last 5 minutes of metrics (timestamp → value) |
| `clusteros:node:{id}:heartbeat` | String | 15s | Last heartbeat timestamp |
| `clusteros:node:{id}:capabilities` | Set | 300s | Capability strings |

### GPU Status

| Key Pattern | Type | TTL | Description |
|-------------|------|-----|-------------|
| `clusteros:gpu:{id}:status` | String | 30s | `AVAILABLE`, `ALLOCATED`, `UNHEALTHY` |
| `clusteros:gpu:{id}:metrics` | Hash | 30s | Temperature, utilization, memory usage |
| `clusteros:gpu:{id}:allocated_to` | String | 60s | Session ID if allocated |

### Cache Aggregates

| Key Pattern | Type | TTL | Description |
|-------------|------|-----|-------------|
| `clusteros:cache:sessions` | Sorted Set | 60s | Session list sorted by last activity (score = timestamp) |
| `clusteros:cache:pool` | Hash | 30s | Aggregated resource pool snapshot |
| `clusteros:cache:capabilities` | Set | 300s | All cluster capabilities |
| `clusteros:cache:nodes:active` | Set | 30s | Set of active node IDs |
| `clusteros:cache:nodes:by_region:{region}` | Set | 60s | Node IDs per region |

### Rate Limiting

| Key Pattern | Type | TTL | Description |
|-------------|------|-----|-------------|
| `clusteros:ratelimit:{user_id}` | Hash | 60s | Token bucket counters |
| `clusteros:ratelimit:global` | Hash | 60s | Global rate limit state |
| `clusteros:ratelimit:burst:{resource}` | String | 60s | Burst allowance tracker |

### Pub/Sub Channels

| Channel | Description |
|---------|-------------|
| `clusteros:events:nodes` | Node join / leave / fail events |
| `clusteros:events:sessions` | Session create / terminate / migrate |
| `clusteros:events:scheduler` | Scheduling decisions and queue changes |
| `clusteros:events:alerts` | Health alerts and predictions |
| `clusteros:events:builds` | Build job state changes |
| `clusteros:events:advisories` | LLM advisory generation |

### Scheduler State

| Key Pattern | Type | TTL | Description |
|-------------|------|-----|-------------|
| `clusteros:scheduler:queue` | Sorted Set | 0 | Pending scheduling requests (score = priority) |
| `clusteros:scheduler:bindings` | Hash | 60s | Session → Node bindings |
| `clusteros:scheduler:reservations` | Hash | 60s | Active reservation IDs |

### Build System

| Key Pattern | Type | TTL | Description |
|-------------|------|-----|-------------|
| `clusteros:build:{id}:status` | String | 300s | Current build status |
| `clusteros:build:{id}:progress` | Hash | 300s | Progress metrics |
| `clusteros:build:{id}:artifacts` | List | 0 | Artifact IDs produced |
| `clusteros:build:queue` | Sorted Set | 0 | Pending build jobs |

## TTL Strategy

- **Heartbeat data**: 15s (fast failure detection)
- **Resource snapshots**: 30s (frequently refreshed)
- **Session state**: 60s (CRDT sync interval)
- **Metrics history**: 300s (5-minute sliding window)
- **Persistent aggregates**: No TTL (refreshed by writers)

## Consistency Notes

1. Redis is **eventually consistent** — authoritative state lives in PostgreSQL and etcd.
2. Session CRDT state uses vector clocks for conflict resolution.
3. Node heartbeats are written with `NX` (only if not exists) to detect stale entries.
4. Rate limiters use Redis Lua scripts for atomic token bucket operations.
