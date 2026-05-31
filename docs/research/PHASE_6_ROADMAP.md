# Helix Cluster OS — Phase 6 Roadmap: Multi-Cluster Federation

> **Research Document** | Phase 6 Planning | 2026-05-31
>
> This document defines the bridge from Phase 5 (Advanced & Exotic Devices) into Phase 6, covering
> the "block of blocks" cell federation model, encrypted mesh networking, hierarchical gossip, global
> control plane, and zero-trust cross-cluster security.

---

## 1. Current State Summary

### Phases 0–5 Completed

| Phase | Name | Status | Key Deliverables |
|-------|------|--------|------------------|
| **0** | Foundation | Complete | 29 submodules, CI/CD, 20-service docker-compose, 26 pkg stubs, go.work, buf proto pipeline |
| **1** | Core Infrastructure | Complete | Container orchestration (`pkg/infra`), VM testing framework, CLI skeleton (`helix_infra`) |
| **2** | Console Nodes & Distributed Foundations | Complete | `pkg/swim`, `pkg/wireguard`, `pkg/discovery`, `pkg/leader`, `pkg/scheduler`, `pkg/session`; PS4/PS5 agents |
| **3** | Edge & Mobile Devices | Complete | etcd integration, full Omega scheduler + ClassAds, mTLS/SPIFFE, OTel tracing, ARM64/Android/iOS agents |
| **4** | Virtual Testing Matrix | Complete | VM-based testing harness, fault injection, performance benchmarks, GPU passthrough |
| **5** | Advanced & Exotic Devices | Complete | GPU cluster management, LLM inference gateway, WASM sandbox, advanced policy engine |

### Phase 6 Research Artifacts

| Document | Location | Contents |
|----------|----------|----------|
| `HELIXCLUSTER_PHASE6_COMPLETE_REPORT.md` | `docs/research/phase_06/HelixCluster_Phase_06/` | 30 000-word complete report |
| `HELIXCLUSTER_PHASE6_FEDERATION_ARCHITECTURE.md` | `docs/research/phase_06/HelixCluster_Phase_06/` | Cell topology and federation patterns |
| `plan_phase6.md` | `docs/research/phase_06/HelixCluster_Phase_06/` | 7-dimension research plan |
| `helixcluster_phase6_sec01–09.md` | `docs/research/phase_06/HelixCluster_Phase_06/` | Per-chapter deep-dives (mesh, gossip, CRDT, security, DR, chaos, control plane, roadmap) |

---

## 2. Phase 6 Scope & Goals

### Primary Objective

Transform Helix Cluster OS from a **single-cluster OS** into a **recursive meta-cluster fabric** ("block of blocks"):
any cell presents itself to the federation as a single addressable node, and any node may expand into a nested sub-cluster — scaling from a single laptop to 100+ cells spanning 10 000+ nodes without re-architecture.

### Four Pillars

| Pillar | Description | Key Technology | Outcome |
|--------|-------------|---------------|---------|
| **Cross-Cluster Membership** | Two-tier SWIM: intra-cell direct probes + cross-cell gateway-relayed suspicion; Phi-accrual failure detection; constant ~5 KB/s gossip budget | Hierarchical SWIM, Phi-accrual, Merkle delta sync | Sub-1 s intra-cell + sub-10 s cross-cell failure detection |
| **Federated Scheduling** | Two-level placement: intra-cell Omega scheduler for node selection, inter-cell Karmada PropagationPolicy for workload distribution; latency-aware spot/preemptible bursting | Karmada, Omega scheduler, spot-aware placement | Cross-cell failover < 60 s; 40–60% cloud cost reduction |
| **Global Control Plane** | Federated API aggregation (`helixctl federation`), ArgoCD GitOps ApplicationSets, SPIFFE/SPIRE nested trust domains, OPA cross-cluster admission | Karmada / OCM, ArgoCD, SPIFFE/SPIRE, OPA/Gatekeeper | Single-commit deploy to all cells; unified `--all-cells` API |
| **Cross-Cluster Networking** | WireGuard kernel mesh (ChaCha20-Poly1305); ICE/STUN/TURN NAT traversal; QUIC fallback; mDNS local + DHT WAN discovery; Cilium Cluster Mesh eBPF data plane | WireGuard, libp2p, Cilium, QUIC | Cell join < 60 s; 3–5% CPU at 1 Gbps; >80% NAT hole-punch success |

### Key Architecture Decisions

- **Per-cell strong consistency, cross-cell eventual consistency.** Raft/etcd stays within each cell; cross-cell state uses HLC-tagged CRDTs (~60% of state types) + Merkle anti-entropy. Raft is never stretched across WAN.
- **Recursive self-similarity.** The same mDNS → DHT → bootstrap join protocol operates at every layer.
- **Cell sizing ~5 000 nodes** (Borg-proven) for fault-domain isolation and gossip scalability.
- **Five federation topology patterns:** full mesh, hub-and-spoke, tree, partitioned, super-cell.

---

## 3. Sub-Phases

| Sub-Phase | Name | Weeks | Tasks | Goal |
|-----------|------|-------|-------|------|
| **6a** | Core Mesh | 1–6 | 30 | Stable WireGuard mesh, NAT traversal, mDNS/DHT discovery, cell join/leave |
| **6b** | Gossip & State Sync | 7–12 | 35 | Hierarchical SWIM, CRDT primitives, Merkle anti-entropy, HLC clocks |
| **6c** | Federation Control Plane | 13–18 | 30 | SPIFFE/SPIRE, Cilium Cluster Mesh, global services, ArgoCD GitOps, Karmada scheduling |
| **6d** | Security & Production Hardening | 19–24 | 25 | OPA policies, Vault secrets, 12 chaos experiments, monitoring, DR runbooks |

**Total: 24 weeks | ~120 tasks | ~480 person-hours**

### Sub-Phase Dependency Gates

| Gate | Condition |
|------|-----------|
| 6a → 6b | Two cells sustain stable WireGuard mesh 72 h without intervention |
| 6b → 6c | 10-cell simulation survives 4 h WAN partition; CRDT converges post-heal < 120 s |
| 6c → 6d | 3-tier app deploys across 3 cells via ArgoCD; cross-cell failover < 60 s |
| 6d → Done | All 12 chaos experiments pass; split-brain alert < 30 s; DR restore < 15 min |

---

## 4. Package Breakdown

### 4.1 New pkg/ Packages (Phase 6, planned)

| Package | Purpose | Integration Point |
|---------|---------|-------------------|
| `pkg/federation` *(planned)* | Cell registry, lifecycle state machine (join/sync/evacuate/detach), federated API proxy | `internal/node`, `cmd/helix-federation` |
| `pkg/swim/hierarchical` *(planned)* | Two-tier SWIM: LAN memberlist pool + WAN delegate gateway pool | Extends `pkg/swim`; `internal/node` |
| `pkg/crdt` *(planned)* | G-Counter, LWW-Register, OR-Set with HLC timestamps; Merkle delta reconciliation | `pkg/federation`, state replication layer |
| `pkg/hlc` *(planned)* | Hybrid Logical Clock; NTP-drift tolerance < 10 ms | `pkg/crdt`, `pkg/swim/hierarchical` |
| `pkg/nattraversal` *(planned)* | ICE/STUN/TURN candidate gathering, UDP hole punch, QUIC fallback transport | `pkg/wireguard`, `pkg/federation` |
| `pkg/cilium` *(planned)* | Cilium Cluster Mesh client; CiliumIdentity propagation; global-service annotation | `internal/node`, federation control plane |
| `pkg/spiffe/federation` *(planned)* | SPIFFE/SPIRE nested topology; trust-bundle exchange across cells; SVID lifecycle | Extends `pkg/security`; all services |
| `pkg/gitops` *(planned)* | ArgoCD ApplicationSet client; drift detection; GitOps federation pipeline | `cmd/helix-federation` |

### 4.2 New internal/ Packages (Phase 6, planned)

| Package | Purpose |
|---------|---------|
| `internal/cell` *(planned)* | Cell abstraction: gateway node management, WireGuard inter-cell mesh wiring, SWIM delegate configuration |
| `internal/federation` *(planned)* | Karmada / OCM integration; PropagationPolicy engine; federated `kubectl` aggregation |
| `internal/chaos` *(planned)* | 12 automated chaos experiments (CE-01–CE-12); Chaos Mesh controller hooks; CI integration |

### 4.3 Existing Packages Extended

| Existing Package | Phase 6 Extension |
|-----------------|-------------------|
| `pkg/swim` | Hierarchical delegate pool, Phi-accrual tuning, gateway-relay suspicion |
| `pkg/wireguard` | Multi-cell peer management, automatic key rotation, inter-cell route advertising |
| `pkg/security` | SPIFFE/SPIRE trust-bundle federation, OPA/Gatekeeper cross-cluster policies |
| `pkg/scheduler` | Inter-cell Karmada propagation, latency-aware spot/preemptible scoring |
| `internal/node` | Cell agent: gossip tier selection, federation identity attestation |

---

## 5. Integration Points with Phase 5

```
Phase 5 Outputs                         Phase 6 Consumers
──────────────────────────────────────────────────────────────────────
pkg/gpu               ──► internal/cell  GPU resource advertisement across cells
internal/llm          ──► pkg/federation Federated LLM inference scheduling (Karmada policy)
pkg/wasm              ──► internal/chaos WASM policy plugins in chaos experiment harness
internal/policy       ──► pkg/spiffe/federation OPA rules applied at cell admission boundaries
pkg/security (mTLS)   ──► pkg/spiffe/federation Trust-bundle roots bridge Phase 5 SPIFFE to Phase 6 cross-cell identity
pkg/swim (intra-cell) ──► pkg/swim/hierarchical LAN pool reused; WAN delegate pool added on top
pkg/wireguard         ──► internal/cell  Per-cell gateway mesh; key rotation protocol extended
internal/scheduler    ──► internal/federation Two-level scheduling: intra-cell Omega + inter-cell Karmada
```

---

## 6. Priority Ordering

### P0 — Critical Path (Federation Cannot Form Without These)

| # | Task | Effort | Reason |
|---|------|--------|--------|
| 1 | `pkg/nattraversal` STUN/TURN/ICE + UDP hole punch | 2 weeks | Cross-cell WireGuard tunnels blocked by NAT without this |
| 2 | `internal/cell` WireGuard inter-cell mesh manager | 2 weeks | Encrypted data plane prerequisite for all federation traffic |
| 3 | `pkg/swim/hierarchical` WAN delegate gossip | 2 weeks | Cross-cell failure detection and membership |
| 4 | `pkg/crdt` + `pkg/hlc` primitives | 2 weeks | Cross-cell state replication without WAN Raft |
| 5 | `pkg/federation` cell lifecycle state machine | 1 week | Join/leave/evacuate protocol |

### P1 — Essential for Phase 6 MVP

| # | Task | Effort | Reason |
|---|------|--------|--------|
| 6 | `pkg/spiffe/federation` trust-bundle exchange | 2 weeks | mTLS and workload identity across cell boundaries |
| 7 | `pkg/cilium` Cluster Mesh data plane | 2 weeks | Pod-to-pod connectivity and global service DNS |
| 8 | `internal/federation` Karmada integration | 2 weeks | Inter-cell workload placement and failover |
| 9 | `pkg/gitops` ArgoCD ApplicationSet federation | 1 week | Operational delivery model for all cells |

### P2 — Important but Can Be Deferred to 6d

| # | Task | Effort | Reason |
|---|------|--------|--------|
| 10 | `internal/chaos` 12-experiment suite | 2 weeks | Required for production gate; not for alpha |
| 11 | OPA/Gatekeeper cross-cluster admission | 1 week | Hardens security; not blocking for functional demo |
| 12 | Velero DR + cross-region snapshot replication | 1 week | RPO target; can use manual backup initially |
| 13 | Prometheus federation + split-brain alerts | 1 week | Observability; debug-mode logging covers alpha |

---

## 7. Risk Analysis

| Risk | Probability | Impact | Mitigation |
|------|-------------|--------|------------|
| Symmetric NAT penetration fails for 5–15% of residential nodes | Medium | High | TURN relay embedded on every gateway node; TCP-443 fallback; libp2p multi-hop circuit relay |
| CRDT state explosion as cell count grows (> 5 KB/s gossip budget) | Medium | High | Merkle-tree delta sync (divergent keys only); per-key TTL + GC; namespace sharding |
| Key engineer attrition (CRDTs / SWIM / SPIFFE are niche skills) | Medium | Medium | Mandatory pair programming on gossip + CRDT code; ADR for every non-obvious decision; Turmoil simulation as executable spec |
| Cilium Cluster Mesh API stability between releases | Low | Medium | Pin Cilium version; contract tests against Cilium API surface; vendor support contract |
| WireGuard key rotation causing mesh flap during rotation | Low | High | Zero-downtime rotation protocol: new key added before old removed; 200 ms overlap window |
| CRDT convergence latency exceeds 120 s after large partition | Low | High | Bounded Merkle delta batch size; prioritize control-plane CRDT types in anti-entropy order |
| etcd stretched across WAN by mistake | Low | Critical | Static analysis lint rule forbidding etcd endpoints outside cell CIDR; CI gate enforces |

---

## 8. Success Criteria / Phase 6 Exit Gates

| KPI | Target | Measurement |
|-----|--------|-------------|
| Cell join time | < 60 seconds | Automated end-to-end test: `helixctl cell join` → cell visible in federation |
| Intra-cell failure detection | < 1 second | SWIM probe latency benchmark; 100-node pool, 24-hour soak |
| Cross-cell failure detection | < 10 seconds | Gateway-relay suspicion; 10-cell testbed |
| Gossip bandwidth per gateway | < 5 KB/s | Prometheus metric `helix_gossip_tx_bytes_per_sec`; 10-cell load test |
| WireGuard CPU overhead | < 5% at 1 Gbps | `iperf3` + `perf stat` on gateway nodes |
| NAT traversal success rate | > 80% non-symmetric NAT | Automated test matrix across 4 NAT types |
| CRDT convergence after partition | < 120 seconds | Chaos experiment CE-06; 10-cell partition 4 h |
| Cross-cell workload failover | < 60 seconds | Karmada PropagationPolicy; gateway node killed |
| Cloud bursting cost reduction | 40–60% | Spot vs on-demand billing comparison over 30-day trial |
| Disaster recovery RPO | < 15 minutes | Velero restore drill: full namespace restore to secondary cell |
| Chaos experiment pass rate | 12 / 12 | `internal/chaos` CI suite; quarterly Game Day |
| Split-brain alert latency | < 30 seconds | Prometheus alerting rule; partition injected via Chaos Mesh |
| Test coverage `pkg/federation`, `pkg/crdt`, `pkg/swim/hierarchical` | > 70% line coverage | Codecov gate on PR |
| **CLAUDE-1 End-User Usability Gate** | **ALL sub-gates pass** | **See below** |

### CLAUDE-1 End-User Usability Sub-Gates (mandatory, non-waivable)

| Sub-Gate | Requirement | Evidence Required |
|----------|-------------|-------------------|
| Real integration tests | Every federated feature tested against live multi-cell testbed (no mock-only) | CI job `integration/federation` with real WireGuard + real etcd |
| End-to-end user flows | `helix cell join`, `helixctl get pods --all-cells`, cross-cell service DNS, GitOps deploy — all exercised as an operator would run them | Test log + terminal recording |
| HelixQA Challenge validation | All Phase 6 Challenges pass against a real running federation (not unit stubs) | HelixQA report with PASS on each Challenge |
| Sink-side evidence | Before declaring any feature complete: captured metrics (Grafana screenshot), log excerpt, or `helixctl` output proving end-user-visible operation | Attached artifact in PR description |
| No mock-only claims | Mocks permitted only in unit tests (`pkg/crdt`, `pkg/hlc`); integration and e2e tests use real services | Code review checklist item enforced in CI |

---

## 9. Bridge to Phase 7 (Industry Benchmarking & Hardening)

Phase 6 delivers a manually operated but production-hardened federation. Phase 7 targets autonomous operation built on top of Phase 6d's telemetry, chaos runbooks, and operational maturity.

| Phase 7 Sub-Phase | Builds On (Phase 6) | Deliverable |
|-------------------|---------------------|-------------|
| 7.1 Self-healing mesh | `internal/cell` WireGuard manager | Latency-aware gateway re-routing without human intervention |
| 7.2 Predictive scaling | Karmada + Prometheus federation | Federated HPA with demand-pattern forecasting across cells |
| 7.3 Zero-trust service mesh (L7) | `pkg/spiffe/federation` + Cilium | mTLS + SPIFFE + OPA at eBPF level; sidecar-free |
| 7.4 Federated storage | `pkg/crdt` block-layer CRDTs | CRDT-backed volume replication for stateful workloads |
| 7.5 Governance automation | `pkg/gitops` + `pkg/wasm` | Policy-as-code (data residency, cost budgets, carbon-aware scheduling) via WASM edge plugins |

Phase 7 is estimated at 32–40 weeks and requires Phase 6d production-readiness review as a hard prerequisite.

---

## 10. References

1. `docs/research/phase_06/HelixCluster_Phase_06/HELIXCLUSTER_PHASE6_COMPLETE_REPORT.md` — 30 000-word Phase 6 report
2. `docs/research/phase_06/HelixCluster_Phase_06/plan_phase6.md` — 7-dimension research plan
3. `docs/research/phase_06/HelixCluster_Phase_06/HELIXCLUSTER_PHASE6_FEDERATION_ARCHITECTURE.md` — Cell topology and federation patterns
4. `docs/research/phase_06/HelixCluster_Phase_06/helixcluster_phase6_sec01.md` — Chapter 1: Cell topology & federation patterns
5. `docs/research/phase_06/HelixCluster_Phase_06/helixcluster_phase6_sec02.md` — Chapter 2: Encrypted mesh networking
6. `docs/research/phase_06/HelixCluster_Phase_06/helixcluster_phase6_sec03.md` — Chapter 3: Hierarchical membership & gossip
7. `docs/research/phase_06/HelixCluster_Phase_06/helixcluster_phase6_sec04.md` — Chapter 4: Consistency model & state classification
8. `docs/research/phase_06/HelixCluster_Phase_06/helixcluster_phase6_sec05.md` — Chapter 5: Zero-trust security & FMEA
9. `docs/research/phase_06/HelixCluster_Phase_06/helixcluster_phase6_sec06.md` — Chapter 6: Cloud bursting & disaster recovery
10. `docs/research/phase_06/HelixCluster_Phase_06/helixcluster_phase6_sec07.md` — Chapter 7: Resilience engineering & chaos
11. `docs/research/phase_06/HelixCluster_Phase_06/helixcluster_phase6_sec08.md` — Chapter 8: Federated control plane
12. `docs/research/phase_06/HelixCluster_Phase_06/helixcluster_phase6_sec09.md` — Chapter 9: Implementation roadmap
13. `docs/research/PHASE_5_ROADMAP.md` — Phase 5 (prior phase)
14. `docs/research/PHASE_7_ROADMAP.md` — Phase 7 (next phase)
15. `pkg/swim/`, `pkg/wireguard/`, `pkg/security/`, `pkg/scheduler/` — Phase 5 packages extended in Phase 6
