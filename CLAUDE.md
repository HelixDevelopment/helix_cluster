# Helix Cluster OS — CLAUDE.md

| Field | Value |
|---|---|
| Revision | 1 |
| Created | 2026-05-30 |
| Last modified | 2026-05-30T20:43:00Z |
| Status | active |

## INHERITED FROM HelixConstitution/CLAUDE.md

All rules in `HelixConstitution/CLAUDE.md` (and the `HelixConstitution/Constitution.md` it references) apply unconditionally. The project-specific rules below extend them.

## Project-Specific Extensions

### Technology Stack
- Go 1.24+ for microservices
- Zig 0.14+ for system primitives
- C/C++ for GPU kernels
- PostgreSQL 16+, Redis Cluster 7+, etcd, NATS, Kafka

### Architecture
- Seven-layer stack (L0-L7)
- 14 control plane microservices
- SWIM gossip + Raft consensus
- Omega-model scheduler with optimistic concurrency

### Applied Fixes Table
| Fix | Date | Commit | Release | Description |
|-----|------|--------|---------|-------------|
