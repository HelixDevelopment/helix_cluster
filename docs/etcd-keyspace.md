# Helix Cluster OS — etcd Keyspace Design

## Overview

etcd serves as the cluster-wide consistent state store for Helix Cluster OS. It uses multi-version concurrency control (MVCC) and Raft consensus to provide strong consistency for critical cluster metadata.

All keys are prefixed with `/clusteros/`.

## Key Hierarchy

```
/clusteros/
├── nodes/
│   ├── {node_id}              → Node (JSON)
│   ├── {node_id}/status       → NodeStatus (ACTIVE, SUSPECT, LEFT, FAILED)
│   ├── {node_id}/heartbeat    → Timestamp (lease-bound)
│   ├── {node_id}/leases/      → Resource leases (session-scoped)
│   │   ├── {lease_id}
│   │   └── ...
│   └── {node_id}/metadata     → Static node metadata
├── sessions/
│   ├── {session_id}           → Session (JSON)
│   ├── {session_id}/status    → SessionStatus (CREATING, RUNNING, MIGRATING, TERMINATED)
│   ├── {session_id}/routing   → I/O routing table (node → PTY mapping)
│   ├── {session_id}/bindings  → Node bindings for this session
│   ├── {session_id}/windows   → Window list (ordered)
│   └── {session_id}/panes     → Pane list (ordered)
├── scheduler/
│   ├── pool/                  → ResourcePool (JSON)
│   │   ├── total              → Aggregated cluster resources
│   │   ├── available          → Currently free resources
│   │   └── by_node/{node_id}  → Per-node resource slice
│   ├── queue/                 → Pending scheduling requests
│   │   ├── {request_id}       → SchedulingRequest (JSON)
│   │   └── ...
│   ├── reservations/          → Active reservations
│   │   ├── {reservation_id}   → Reservation (JSON)
│   │   └── ...
│   └── bindings/              → Session → Node bindings
│       ├── {session_id}       → NodeBinding (JSON)
│       └── ...
├── security/
│   ├── spiffe_ids/            → SPIFFE ID → Node mapping
│   │   ├── {spiffe_id_hash}   → Node ID
│   │   └── ...
│   ├── wireguard/
│   │   ├── peers/             → Allowed IPs and pubkeys
│   │   │   ├── {node_id}      → WireGuardPeerConfig (JSON)
│   │   │   └── ...
│   │   └── subnets/           → Allocated WireGuard subnets
│   │       ├── {subnet_cidr}  → Assignment metadata
│   │       └── ...
│   └── acl/                   → Access control policies
│       ├── {policy_id}        → OPA/Rego policy bundle reference
│       └── ...
├── config/
│   ├── cluster/               → Cluster-wide settings
│   │   ├── name               → Cluster name
│   │   ├── version            → Cluster version
│   │   └── settings           → Generic config JSON
│   ├── scheduler/             → Scheduler configuration
│   │   ├── policy             → Scheduling policy JSON
│   │   └── weights            → Resource weighting
│   └── limits/                → Resource quotas
│       ├── {user_id}          → UserQuota (JSON)
│       └── default            → Default quota
├── builds/
│   ├── {job_id}               → BuildJob (JSON)
│   ├── {job_id}/status        → BuildStatus
│   └── {job_id}/artifacts     → Artifact list
├── locks/
│   ├── scheduler/             → Scheduling mutex (lease-bound)
│   ├── migrations/            → Migration mutex (lease-bound)
│   ├── config/                → Config changes mutex (lease-bound)
│   └── builds/                → Build orchestration mutex
├── leader/
│   ├── scheduler              → Current scheduler leader (lease-bound)
│   ├── health_monitor         → Current health monitor leader
│   └── build_coordinator      → Current build coordinator
└── version/
    └── schema                 → Current schema version (for migration coordination)
```

## Lease Usage

etcd leases are used for ephemeral keys:

| Key Pattern | Lease TTL | Purpose |
|-------------|-----------|---------|
| `/clusteros/nodes/{id}/heartbeat` | 10s | Node liveness |
| `/clusteros/locks/*` | 30s | Distributed locks (auto-release on failure) |
| `/clusteros/leader/*` | 15s | Service leadership election |
| `/clusteros/nodes/{id}/leases/*` | Session lifetime | Resource lease binding |

## Watch Patterns

Components watch these prefixes for changes:

| Watcher | Prefix | Purpose |
|---------|--------|---------|
| Node Agent | `/clusteros/nodes/{self_id}/` | Receive commands |
| Session Manager | `/clusteros/sessions/` | Track all session changes |
| Scheduler | `/clusteros/scheduler/queue/` | New scheduling requests |
| Health Monitor | `/clusteros/nodes/{id}/heartbeat` | Node failure detection |
| API Gateway | `/clusteros/config/cluster/` | Config reloads |
| Build Coordinator | `/clusteros/builds/` | Build job events |

## Concurrency Control

1. **Optimistic locking**: All updates include the previous `mod_revision` for compare-and-swap.
2. **Transactions**: Multi-key updates use etcd transactions for atomicity.
3. **Leases**: Ephemeral keys automatically expire if the holder fails.
4. **Election**: Leaders use `clientv3/concurrency` for fair leader election.

## Data Size Guidelines

- Individual values should remain < 1 MB (etcd default limit).
- Large lists (e.g., all sessions) are paginated via range queries.
- Binary data (CRDT states, large JSON) is stored in PostgreSQL; etcd holds references.
