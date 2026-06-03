# Helix Cluster OS

Helix Cluster OS is a next-generation distributed operating system for orchestrating compute workloads across heterogeneous nodes — from datacenter GPUs down to edge SBCs and handhelds. It unifies HPC scheduling, container orchestration, AI/ML inference, federated multi-cluster operation, and secure multi-tenant session management under a single control plane.

> **Engineering guarantee (CLAUDE-1 / CLAUDE-2):** every feature ships with tests that prove real end-user behaviour — never green tests over stubs — and every OS-specific capability uses a real native facility per platform (no Linux-only mocks). See [`CLAUDE.md`](CLAUDE.md) and [`Constitution.md`](Constitution.md).

## Features

- **Heterogeneous Node Management** — Register, monitor, and schedule across tiers T1–T8 (datacenter → microcontroller) with structured health scoring and capability negotiation.
- **Omega-model Scheduler** — Pluggable placement with optimistic concurrency, ClassAd matching, gang scheduling, value-multiplier preemption, multifactor (age/fairshare/size/QoS) priority queues, and constraint-based (location/colocation/order/stickiness) placement.
- **Distributed-Systems Foundation** — A large pure-Go library (`pkg/`) of consensus, membership (SWIM gossip), replication (CRDT, MVCC, anti-entropy), and federation primitives. See [Foundation Packages](#foundation-packages).
- **Federation & Multi-cluster** — Cross-cell trust, topology patterns, CRDT config sync, data-residency admission, split-brain detection, and quorum-based failure confirmation.
- **GPU & Cost Orchestration** — GPU pool management, TCO-aware local cost modelling, cost/latency-aware schedulers, burst-to-cloud autoscaling, N+K failover capacity reserve, and global budget caps.
- **Security & Attestation** — SPIFFE identity, JWT auth, ML-KEM-768 post-quantum E2EE, device attestation (challenge/response, proof-of-GPU-work, device sealing), and attestation-gated admission.
- **Deterministic Simulation Testing (DST)** — FoundationDB-style seeded simulation, BUGGIFY fault injection, Turmoil network simulation, and clock-fault injectors for reproducible distributed-systems testing.
- **Observability** — Built-in metrics, W3C distributed tracing, structured logging, and Grafana dashboard generation.
- **Multi-Protocol APIs** — gRPC services with Protocol Buffer definitions for all subsystems.

## Architecture

Helix is organised as a seven-layer stack (L0–L7) with 14 control-plane microservices, coordinated via SWIM gossip for membership and Raft consensus for strongly-consistent state. The scheduler is an Omega-model two-level design with optimistic concurrency.

The repository is a Go workspace combining a core module with git submodules for the larger services.

```
.
├── api/v1/            # Protocol Buffer definitions (NodeService, SessionService, SchedulerService, ...)
├── cmd/               # Service binaries and CLIs
├── internal/          # Private application packages (console, gateway, scheduler, node, health, policy, trust, ...)
├── pkg/               # Shared pure-Go foundation library (see "Foundation Packages")
├── web/               # React + TypeScript + Vite dashboard
├── docs/              # Documentation (see "Documentation")
├── data/              # HXC registry (SQLite) and runtime data
├── scripts/           # Build, test, and utility scripts
│
├── HelixConstitution/ # Governance & constitution (submodule)
├── security/          # Security service: identity, E2EE, attestation (submodule)
├── helixqa/           # HelixQA challenge/validation framework (submodule)
├── EventBus/          # Event bus (submodule)
├── Messaging/         # Messaging service (submodule)
├── discovery/         # Service discovery (submodule; pkg/discovery is the editable root)
├── containers/        # Container runtime (submodule)
├── recovery/          # Recovery service (submodule)
├── config/            # Configuration service (submodule)
├── challenges/        # Challenge platform (submodule)
├── DocProcessor/      # Document processing (submodule)
├── LLMOrchestrator/   # LLM orchestration (submodule)
├── Herald/            # Notification service (submodule)
└── docs_chain/        # Documentation-chain engine (submodule)
```

## Foundation Packages

The `pkg/` library provides the pure-Go, deterministic, well-tested primitives the control plane is built from. Highlights by domain:

| Domain | Packages |
|---|---|
| **Consensus & coordination** | `voting` (largest-subcluster quorum), `failconfirm` (SWIM two-phase PFAIL→FAIL), `leader`, `lock`, `splitbrain` / `splitbrainalert`, `heartbeatcoalescer` (Multi-Raft), `multiraft` (per-shard etcd-raft groups + `LeaseTracker` leaseholder local reads, throughput scales with shard count), `stonith` (STONITH fencing: IPMI/EC2/Azure/SBD + multi-level fallback), `kraft` (KRaft-style self-managed Raft metadata quorum, no ZooKeeper) |
| **Membership & discovery** | `swim` (+ phi-accrual, hierarchical), `discovery` (+ federated, + mDNS/DNS-SD `_helix-cluster._tcp` advertiser/browser: TXT cellid/nodeid/wgpubkey, reject-invalid; discovery-only, trust via SPIFFE), `nattraversal` (STUN), `ice`, `cellmesh`, `scan` (Oracle-SCAN stable virtual endpoint) |
| **Replication & state** | `crdt` (+ `merkle`, LWW/ORSet/G/PN-counters/vector-clock), `deltacrdt` (delta-state G/PN/OR-set/LWW-map), `mvcc` (B-tree time-travel store), `antientropy` (hinted-handoff + read-repair + Merkle diff), `watchmanager` (synced/unsynced/victim), `hlc`, `offlinesync`, `checkpoint_merge` |
| **Scheduling & placement** | `scheduler` (Omega/ClassAd/gang/preempt), `constraints` (Pacemaker 4-type), `preempt` (value-multiplier), `priorityqueue` (multifactor aging), `backfill` (SLURM), `admissioncontrol` (N+K reserve), `budgetcap`, `qos`, `suitability`, `ewmarank`, `workclaim` (SKIP LOCKED), `providerchain` (multi-tier fallback cascade), `modelrouter` (strategy→default model), `gepetto` (local-vs-Chutes arbitration), `llmfailover` (error-classified failover taxonomy), `carbonsched` (carbon-aware placement + per-job kWh/gCO2 metering), `workloadrouter` (UnifiedManager concurrent-pricing weighted composite routing + TEE multiplier) |
| **GPU & resource mgmt** | `pool`, `local` (TCO), `costsched`, `latencysched`, `healthmonitor`, `gpuattest` (attestation crypto), `capability`, `deviceprofile`, `device`, `devicecatalog` (machine-readable device taxonomy → tier/trust/compute-class lookup), `tierdef`, `tiersec`, `quantization`, `gpucatalog` (compute-multiplier catalog + attested scoring), `gputopo` (NUMA/NVLink topology-aware placement), `balancemonitor` (USD balance floor warning), `deviceplugin` (gRPC device-plugin / GRES fingerprinting + oversubscription rejection), `gpu` (ProviderAdapter registration hooks → pool-facing provider listing), `benchmark` (real repeatable on-host CPU score + GPU TFLOPS / NPU TOPS seams), `tierdetect` (cross-platform host-capability pre-provision gate → typed `MissingCapabilityError`), `internal/gpu` dual-workload reservation (Helix-PoW reserve + >0.80 starvation guard) |
| **Federation & multi-cluster** | `federation` (+ `suspicion`), `internal/federation` (Karmada PropagationPolicy/OverridePolicy engine: constraint-aware two-level cell selection + <60s failover reselect), `gitops` (ArgoCD ApplicationSet client: matrix per-cell generation + prune/self-heal + canary→tier-2→tier-1 rolling sync + drift/prune), `fedtopology`, `fedtrust`, `configsync`, `residency`, `raftprofile`, `spiffefed`, `doublecrypt` |
| **Messaging & flow** | `flowcontrol` (K8s APF), `workqueue` (rate-limited), `ratelimit`, `backoff`, `retry`, `idempotent` (exactly-once), `rebalance` (cooperative-sticky), `fiber`, `fallbackchain`, `pubsub`, `events` |
| **Routing & sessions** | `hashslot` (CRC16 + MOVED/ASK), `session`, `slotmigration` (atomic live migration), `edge`, `edgeregistry`, `edgeverify`, `edgefusion` |
| **Security & verification** | `crypto`, `jwt`, `hybridkex` (X25519 + ML-KEM-768 hybrid post-quantum key exchange), `e2eebench` (hybridkex handshake-latency benchmark, median <1ms), `modelintegrity` (SHA-256 gate), `redundantexec` (BOINC trust), `attestadmit`, `doublecrypt`, `spiffefed`, `gravaladmit` (HMAC GraVal admission), `gravalverify` (VRAM-ratio attestation + BatchVerify KPI), `gpuattest` (challenge/response + seal + multi-GPU node enumeration), `fsresidency` (per-range ReadAt + SHA-256 file-residency challenge), `exportcontrol` (country-tier KYC gate on controlled-GPU node onboarding), `compliancedoc` (EU AI Act model-card / provenance doc-gen from attestation logs) |
| **Burst & economics** | `burst` (hysteresis autoscaler), `bursthysteresis` (MONITOR→SPILL→RECOVER dead-band), `cloudspot` (AWS IMDSv2 / Azure scheduled-events / GCP preemption interruption pollers → drain/checkpoint/upload, httptest-proven), `marketplace`, `marketplaceadapter` (MarketplaceAdapter interface + Name()-dispatch registry + Chutes HTTP + Akash/AKT adapters), `revenueopt` (greedy GPU→marketplace revenue maximiser, TEE→Chutes bias), `economics` (multi-token RewardDistributor with treasury/reinvest conservation + participant ROI/break-even), `chutesaccount` (Chutes API model-list + balance client), `chutes` (inference client + attestation + E2EE envelope + `ChutesMinerConfig`/`ValidatorConfig` validation), `provider/chutes` (OpenAI-compatible /v1 provider + 429 retry/backoff + Retry-After), `provider/runpod` (serverless warm-pool GPUProvider), `provider/aws` (EC2 Spot GPUProvider, injectable client), `provider/ionet` (io.net REST Ray-cluster GPUProvider: DeployCluster/HealthCheck + capacity gate, httptest-proven), `llmadapter` (Claude/OpenAI request/response shape adapters) |
| **Testing & simulation** | `testing/dst` (+ BUGGIFY, chaos, turmoil), `dst` (standalone seeded-RNG deterministic sim harness + byte-for-byte replay), `porcupine` (WGL linearizability checker + recorder), `internal/chaos` (PodKill/partition/disk-stall/clock-skew injectors + canary rollback), `timefault`, `chaosexp`, `fmea`, `phasegate`, `qualitygate`, `phase7matrix`, `stats`, `covgate`, `sandbox` |
| **Observability** | `metrics` (+ tier/cost/provider-health series, + TAO-earnings/GraVal-status/token-throughput/gpu-utilization series), `tracing` (W3C), `health` (+ miner-api/GraVal DaemonSet dependency checks named in the rollup), `grafanadash`, `log` |

Each package is standard-library-only where possible, deterministic (injected clocks, seeded PRNGs), and proven by tests that fail under mutation of the logic they cover.

## Quick Start

### Prerequisites

- **Go 1.26+** (workspace uses `go.work`)
- Node.js 20+ (for the web UI)
- SQLite 3 (the HXC registry lives at `data/hxc_registry.db`)
- Docker & Docker Compose (for integration services)
- Protocol Buffer compiler + `protoc-gen-go` (optional, for API regeneration)

### Setup / Build / Test

```bash
./scripts/setup.sh     # initialise submodules and toolchains
./scripts/build.sh     # go build ./...
./scripts/test.sh      # go test ./...
./scripts/lint.sh      # go vet + linters
./scripts/format.sh    # gofmt
```

To run the full race suite for a package:

```bash
go test -race -count=1 ./pkg/<package>/...
```

## API

Protocol Buffer definitions live in `api/v1/`. Core services:

- `NodeService` — Node lifecycle management
- `SessionService` — Session CRUD operations
- `SchedulerService` — Job scheduling and monitoring
- `HealthService` — Health checks and reporting
- `AdvisoryService` — Distributed locks and advisory events
- `SecurityService` — Authentication and authorization
- `BuildService` — Build pipeline management

## Web UI

The web dashboard is built with React, TypeScript, and Vite.

```bash
cd web && npm install && npm run dev
```

## Documentation

- [`CLAUDE.md`](CLAUDE.md) — AI-agent engineering rules (end-user usability & cross-platform parity guarantees)
- [`Constitution.md`](Constitution.md) / [`HelixConstitution/`](HelixConstitution/) — project governance
- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — hardened L0–L7 architecture diagram + component map (lint-enforced by [`pkg/archlint`](pkg/archlint))
- [`docs/MVP_ARCHITECTURE.md`](docs/MVP_ARCHITECTURE.md) — living MVP seven-layer overview + service-communication Mermaid grounded in real packages, with a Go drift-validator that fails the build on doc/codebase drift (HXC-1145)
- [`docs/FOUNDATION_PACKAGES.md`](docs/FOUNDATION_PACKAGES.md) — full catalogue of `pkg/` packages
- [`docs/HXC_REGISTRY.md`](docs/HXC_REGISTRY.md) — the work-item registry model
- [`docs/NODE_PROVISIONING_BOUNDARY.md`](docs/NODE_PROVISIONING_BOUNDARY.md) — node-provisioning boundary: Helix provisions operator-controlled nodes; it does **not** jailbreak/root/unlock or bypass any device security (HXC-1146)
- [`docs/guides/phase_02_architecture.md`](docs/guides/phase_02_architecture.md) — Phase 02 architecture guide: operator console → WireGuard/SWIM mesh → discovery/scheduler → Linux node, with a traced job path and the no-jailbreak boundary (HXC-1164)
- [`docs/architecture/PHASE_8C_INTEGRATION.md`](docs/architecture/PHASE_8C_INTEGRATION.md) — code-grounded Phase 8C integration map: attestation→scheduler, e2ee→orchestrator, marketplace seams (implemented vs PLANNED) (HXC-1614)
- [`docs/PHASE_8C_EXIT_GATE_EVIDENCE.md`](docs/PHASE_8C_EXIT_GATE_EVIDENCE.md) — Phase 8C CLAUDE-1 exit-gate evidence matrix (PROVEN / PARTIAL / NOT-YET, each row tied to a real test or a Queued ticket) (HXC-1613)

### User documentation

- [`docs/USER_MANUAL.md`](docs/USER_MANUAL.md) — **operator manual**: prerequisites, build, configuration (`.env`), bringing services up, DB migrations, observability, SBOM/vuln scanning, host-safety; every command grounded in a real Make target / binary / script
- [`docs/USER_GUIDE.md`](docs/USER_GUIDE.md) — **end-user guide**: the request/session model, submitting work, E2EE confidential inference, and an honest single-host-vs-deployed-cluster capability split
- [`docs/guides/getting-started.md`](docs/guides/getting-started.md) · [`docs/guides/development.md`](docs/guides/development.md) · [`docs/guides/operations.md`](docs/guides/operations.md) · [`docs/guides/architecture.md`](docs/guides/architecture.md) — developer/operator guides

### Reference & quality

- [`docs/DATABASE_SCHEMA.md`](docs/DATABASE_SCHEMA.md) — **SQL schema reference**: every table/column/index/trigger from `migrations/postgresql/`, a Mermaid ER diagram, and the migrate-chain-vs-primary-schema reconciliation note
- [`docs/ARCHITECTURE_DIAGRAMS.md`](docs/ARCHITECTURE_DIAGRAMS.md) — **consolidated Mermaid diagrams**: L0–L7 stack, control-plane services, request/data flow (with E2EE/attestation seams), tier matrix
- [`docs/TEST_COVERAGE_REPORT.md`](docs/TEST_COVERAGE_REPORT.md) — **test coverage report**: real measured statement coverage (main 82.4% / security 87.8%) + per-test-type inventory (unit/integration/E2E/stress/chaos/benchmark/fuzz/race/security/challenges)
- [`docs/PRODUCTION_READINESS_REVIEW.md`](docs/PRODUCTION_READINESS_REVIEW.md) — honest 80-item production-readiness review (HXC-1286)

### Security

- [`docs/security/threat-model.md`](docs/security/threat-model.md) · [`docs/security/rbac.md`](docs/security/rbac.md) · [`docs/security/tls-setup.md`](docs/security/tls-setup.md) · [`docs/security/sbom.md`](docs/security/sbom.md) — threat model, RBAC, TLS/mTLS, SBOM

### Standards & process

- [`CODING_STANDARDS_GO.md`](CODING_STANDARDS_GO.md) / [`CODING_STANDARDS_C.md`](CODING_STANDARDS_C.md) / [`CODING_STANDARDS_ZIG.md`](CODING_STANDARDS_ZIG.md) — language standards
- [`DEVELOPMENT.md`](DEVELOPMENT.md) — development workflow
- [`CHANGELOG.md`](CHANGELOG.md) — release history

### Documentation Synchronization (CLAUDE-3 / §11.4.106)

Every change to code, services, components, architecture, or schema MUST update all affected materials — README, docs, user guides, manuals, websites, diagrams, and SQL/schema definitions — together with **all** of their exports. This is mechanically enforced out-of-the-box by the `docs_chain` engine via `.docs_chain/contexts/*.yaml` (Markdown → HTML/PDF/DOCX) and gated by `docs_chain verify`, with no escape hatch. The mandate restates and cites Constitution §11.4.106 and the project rules CLAUDE-3 / AGENT-2 / QWEN-2.

## Development Model

Work is tracked in an SQLite registry (`data/hxc_registry.db`) of `HXC-####` items across 11 phases (0–10). Items move `Queued → In progress → Completed` and are implemented in parallel "waves": disjoint new packages built via an implement → adversarial-review → fix pipeline, then gated with whole-tree `build`/`vet`, `-race` tests, and an independent mutation bite per item that must fail the item's named guard test. No item is marked Completed without that proof.

## Contributing

1. Create a feature branch
2. Implement with tests that prove real behaviour (and fail under mutation)
3. Run `go build ./... && go vet ./... && go test -race ./...`
4. Commit and open a Pull Request

## License

See [LICENSE](LICENSE) for details.
