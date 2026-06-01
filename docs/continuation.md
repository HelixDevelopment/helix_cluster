# Continuation Document

**Revision:** 52
**Last modified:** 2026-06-01T16:33:29Z
**Description:** Sacred invariant resumption document for Helix Cluster OS
**Authority:** Constitution §12.10
**Maintainer:** Operator + AI loop

---

## §0: How to use this document

Any CLI agent resuming work on this project MUST read this file first.

## §1: Recently Completed Work (last 10 commits)

| Commit | Message |
|--------|---------|
| `6c825df` | Foundation wave 8: NATS JetStream stream-config (5 streams + EnsureStreams), Kafka topic-config (partitions/replication/retention + EnsureTopics), HelixQA challenge runner + DST chaos/session-recovery harness, gateway REST surface (/v1/sessions, /v1/pool/utilization) + OpenAPI 3.0 — integration-proven vs real NATS/Kafka/HTTP |
| `479efb5` | Foundation wave 7: mark 9 items Completed (HXC-1101/1102/1103/1105/1106/1107/1114/1115/1133) — integration-proven vs real tmux/etcd/postgres |
| `e12003a` | Foundation wave 7: session WS-vertical (htmux attach+PTY-over-WS+msgpack envelope), e2ee ML-KEM record + sw-attestation, RBAC scopes, ratelimit token-bucket + etcd key namespace, postgres 15-table primary schema — integration-proven (real tmux/etcd/postgres); gate fixed tmux-CC subscribe race + pg applier host-path-in-container bug |
| `6882b02` | Foundation wave 6: mark 5 items Completed (HXC-1084/1085/1090/1095/1099) |
| `1367dca` | Foundation wave 6: session(tmux -CC + CRDT) / wireguard STUN-NAT / postgres registry / build orchestrator — integration-proven |
| `e0d3938` | Wave 5 deferred resolved: mark HXC-1086/1088/1093 Completed (integration-proven) |
| `4d0ac66` | fix(swim,leader,node): resolve Wave 5 deferred — real-dep integration now green |
| `8184725` | Foundation wave 5: mark 5 items Completed (HXC-1087/1089/1091/1092/1094); 1086/1088/1093 stay Queued (integration WIP) |
| `2f27ac9` | Foundation wave 5: 5 control-plane streams (gateway/lock/wireguard/node-registry proven; leader+swim+node-watch WIP) |
| `8f570cb` | fix(pkg/infra): isolate VMSpawn integration tests against deterministic vm-1 collision |

## §2: Environment Snapshot

| Property | Value |
|----------|-------|
| **Branch** | `main` |
| **Commit** | `6c825df` |
| **Timestamp** | 2026-06-01T16:33:29Z |

## §3: Active Work

| HXC | Title | Status |
|-----|-------|--------|
| HXC-922 | Node service, advisory locks, chaos tests, docs | ✅ Done |
| HXC-921 | Web UI, K8s/Helm, integration, E2E, benchmarks | ✅ Done |
| HXC-920 | Health, security, build, wireguard services | ✅ Done |
| HXC-919 | htmux, messaging, GPU, WASM, storage, build manifest | ✅ Done |
| HXC-918 | Wire scheduler/session gRPC to real backends, etcd discovery | ✅ Done |
| HXC-916 | Stub expansion (config, retry, ratelimit, validator, build cache) | ✅ Done |
| HXC-914 | LLM, policy, setup, distributed lock | ✅ Done |
| HXC-913 | Health, security, ClassAds parser | ✅ Done |
| HXC-912 | Gateway, helixd, helix-agent | ✅ Done |
| HXC-911 | Scheduler, Session gRPC services | ✅ Done |
| HXC-910 | Console, security, testing infra | ✅ Done |

## §4: Next Planned Work

All MVP, Phase 2, Phase 3, and Phase 4 are **COMPLETE**. Potential future enhancements:

1. **Performance optimization** — Profile and optimize hot paths
2. **Multi-region support** — Cross-region cluster federation
3. **GPU scheduling v2** — Multi-GPU, fractional GPU allocation
4. **Web UI real-time** — WebSocket integration for live updates
5. **Operator pattern** — Kubernetes operator for cluster management

## §5: Known Issues / Blockers

- None

## §6: Quick Commands

```bash
# Run all tests (requires GOWORK due to workspace modules)
GOWORK=$(pwd)/go.work go test ./... -race -count=1

# Build all binaries
GOWORK=$(pwd)/go.work go build ./cmd/...

# Build web UI
cd web && npm run build

# Render Helm chart
helm template helix-cluster deploy/helm/

# Run benchmarks
go test -bench=. ./test/benchmark

# Run chaos tests
go test -race -count=1 ./test/chaos
```
