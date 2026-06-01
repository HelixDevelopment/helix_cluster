# Continuation Document

**Revision:** 57
**Last modified:** 2026-06-01T18:10:21Z
**Description:** Sacred invariant resumption document for Helix Cluster OS
**Authority:** Constitution §12.10
**Maintainer:** Operator + AI loop

---

## §0: How to use this document

Any CLI agent resuming work on this project MUST read this file first.

## §1: Recently Completed Work (last 10 commits)

| Commit | Message |
|--------|---------|
| `546c68d` | Foundation wave 10: mark 3 items Completed (HXC-1131/1134/1144) — integration-proven vs real vault/etcd/postgres |
| `467a6dc` | Foundation wave 10: Vault secret injection + rotation (KV v2, no-downtime refresh), WireGuard mesh segmentation policy engine (DENY-wins label selectors + enforcement ruleset), Backup service (etcd clientv3 snapshot + pg_dump/restore state-match) — integration-proven vs real vault/etcd/postgres. GATE: fixed 2 non-compiling integration files (unused ctx, unused require import) |
| `f1e920b` | Foundation wave 9: mark 4 items Completed (HXC-1104/1116/1127/1132) — integration-proven vs real redis/mTLS/opa/HTTP; CLAUDE-2 macOS CPU utilization now real |
| `7634fbb` | Foundation wave 9: Redis store (routing TTL + NodeEvent pub/sub, go-redis/v9), SPIFFE SVID issuance + mTLS identity (go-spiffe/v2), OPA policy engine + HelixConstitution scheduling (open-policy-agent/opa), metrics collector scrape endpoint — integration-proven vs real redis/mTLS/opa/HTTP. GATE FIXES: tmux ControlModeAttach EOF-before-drain race; pkg/resources DarwinReader now samples REAL macOS CPU utilization via top (CLAUDE-2: no more 0%% on macOS); redis test mutex-copy vet violation |
| `51cea15` | Foundation wave 8: mark 5 items Completed (HXC-1108/1109/1118/1120/1121) — integration-proven vs real NATS/Kafka/HTTP |
| `6c825df` | Foundation wave 8: NATS JetStream stream-config (5 streams + EnsureStreams), Kafka topic-config (partitions/replication/retention + EnsureTopics), HelixQA challenge runner + DST chaos/session-recovery harness, gateway REST surface (/v1/sessions, /v1/pool/utilization) + OpenAPI 3.0 — integration-proven vs real NATS/Kafka/HTTP |
| `479efb5` | Foundation wave 7: mark 9 items Completed (HXC-1101/1102/1103/1105/1106/1107/1114/1115/1133) — integration-proven vs real tmux/etcd/postgres |
| `e12003a` | Foundation wave 7: session WS-vertical (htmux attach+PTY-over-WS+msgpack envelope), e2ee ML-KEM record + sw-attestation, RBAC scopes, ratelimit token-bucket + etcd key namespace, postgres 15-table primary schema — integration-proven (real tmux/etcd/postgres); gate fixed tmux-CC subscribe race + pg applier host-path-in-container bug |
| `6882b02` | Foundation wave 6: mark 5 items Completed (HXC-1084/1085/1090/1095/1099) |
| `1367dca` | Foundation wave 6: session(tmux -CC + CRDT) / wireguard STUN-NAT / postgres registry / build orchestrator — integration-proven |

## §2: Environment Snapshot

| Property | Value |
|----------|-------|
| **Branch** | `main` |
| **Commit** | `546c68d` |
| **Timestamp** | 2026-06-01T18:10:21Z |

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
