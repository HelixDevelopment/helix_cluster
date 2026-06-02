# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **Foundation distributed-systems library (`pkg/`)** — a large, growing pure-Go, deterministic, mutation-tested package set. Recent additions include:
  - Consensus/coordination: `voting`, `failconfirm`, `heartbeatcoalescer`, `splitbrainalert`.
  - Replication/state: `mvcc` (B-tree time-travel store), `antientropy` (hinted-handoff + read-repair + Merkle diff), `watchmanager` (synced/unsynced/victim watch groups), `crdt`/`merkle`.
  - Scheduling/placement: `constraints` (Pacemaker 4-type), `preempt` (value-multiplier), `priorityqueue` (multifactor aging), `workclaim` (SKIP-LOCKED), `admissioncontrol` (N+K reserve), `budgetcap`, `qos`, `suitability`, `ewmarank`, `fallbackchain`.
  - GPU/resource & cost: `costsched`, `latencysched`, `healthmonitor`, `gpuattest` (attestation crypto: challenge/response, proof-of-GPU-work, O(1) spot-check, device-sealing), `attestadmit`, `quantization`, `local` (TCO), `pool`, `burst`.
  - Messaging/flow & edge: `flowcontrol` (K8s APF), `idempotent` (exactly-once), `rebalance` (cooperative-sticky), `informer` (list-watch cache), `redundantexec` (BOINC trust), `edgeregistry`, `edgeverify`, `slotmigration`, `hashslot`, `healthprobe`.
  - Testing/simulation: `timefault` (clock skew/freeze/monotonic injectors), BUGGIFY in `testing/dst`, `phase7matrix` (gap-matrix verifier).
  - This session (34 pure-Go units):
    - `bursthysteresis` — `BurstController` with a MONITOR→SPILL→RECOVER two-threshold hysteresis dead-band (HXC-1504).
    - `providerchain` — ordered multi-tier provider fallback cascade with retriable/terminal error classification and injected-clock failover SLA (HXC-1508).
    - `gpucatalog` — `SUPPORTED_GPUS` compute-multiplier catalog, `LookupMultiplier`, and attestation-aware `Score` (attested above un-attested) (HXC-1592).
    - `gravaladmit` — HMAC-SHA256 GraVal challenge-response provider admission gate with single-use nonce and `graval.verified` label (HXC-1523).
    - `modelrouter` — strategy-to-default-model resolution (latency/throughput/quality/cost) with a typed unknown-strategy error (HXC-1430).
    - `gravalverify` — GraVal GPU attestation with a VRAM-ratio `>= 0.95` gate and a concurrent, race-clean `BatchVerify` pass-rate KPI (HXC-1435, HXC-1436).
    - `gepetto` — HelixGepetto dual-resource local-vs-Chutes arbitration with a monotonic reserve schedule and a strict `>0.80` high-water anti-starvation zero-capacity cut-off (HXC-1446).
    - `chutesaccount` — Chutes API model-list (TEE/price fields) and `/users/me` account-balance client over HTTP with `Authorization: Bearer` (HXC-1429).
    - `cmd/burst-controller` — utilization-driven binary driving `bursthysteresis`; emits a "burst allocation request" on SPILL and a RECOVER line, runID-stamped (HXC-1531).
    - `tieredcache` — hot(memory)/warm(NVMe)/cold(SSD) tiered cache with injected backends + injected clock, idle-demotion + promotion, and per-tier hit stats (HXC-1418).
    - `smartrouter` — intelligent ModelRouter (latency/throughput/cost/TEE/balanced) that always excludes unhealthy models, with a typed no-eligible-model error (HXC-1539).
    - `epochresolve` — config-epoch failover slot-ownership conflict resolution (highest-epoch wins, deterministic lexicographic equal-epoch tiebreak, order-independent convergence) (HXC-1397).
    - `balancemonitor` — USD account-balance monitor with a strict low-balance floor warning, over HTTP with `Authorization: Bearer` (HXC-1512).
    - `scan` — Oracle-SCAN-style stable virtual client endpoint: least-loaded healthy-backend routing with a membership-stable name and injected clock (HXC-1403).
    - `llmfailover` — error-classified LLM failover taxonomy (deterministic per-class fallback paths) plus a sandbox scheduling-hint contract (HXC-1600).
    - `llmadapter` — Claude Messages / OpenAI Responses-API request and response shape adapters to OpenAI chat-completions, preserving tool-call order (HXC-1599).
    - `deltacrdt` — standalone delta-state CRDTs (G-counter / PN-counter / observed-remove OR-set / LWW-map) with delta-mutators and a convergent (commutative/associative/idempotent) merge (HXC-1409).
    - `gputopo` — topology-aware NUMA/NVLink GPU placement scoring that prefers NVLink-connected GPU pairs and falls back to PCIe (HXC-1406).
    - `fsresidency` — filesystem residency challenge: per-byte-range `ReadAt` + SHA-256 verification proving a model file is really present on disk; truncated/missing/mismatch all fail (HXC-1572).
    - `economics` — `RewardDistributor` multi-token distribution with treasury/reinvest splits and a conservation invariant (`sum(ledger)+treasury+reinvest == total`), plus `GetParticipantROI` (ROI% + break-even days, typed unknown-participant error) (HXC-1460, HXC-1461).
    - `revenueopt` — `RevenueOptimizer.OptimizeAllocation` greedy GPU→marketplace assignment maximising expected revenue, with a TEE→Chutes revenue bias; self-contained types (HXC-1456).
    - `exportcontrol` — export-control / country-code KYC gate: a controlled GPU (`H100`/`A100`) from a Tier-3 (embargoed) or unknown jurisdiction is rejected with `ErrExportDenied`; Tier-1 is allowed (HXC-1482).
    - `devicecatalog` — machine-readable 64-device taxonomy with case-insensitive substring `Lookup` resolving a discovered CPU model (e.g. `Rockchip RK3588`) to its tier / trust-level / compute-class (HXC-1331).
    - `compliancedoc` — EU AI Act compliance-documentation pipeline: generates a model card + technical-documentation artifact from attestation-log records, with a SHA-256 provenance hash bound to the actual record content (HXC-1481).
    - `hybridkex` — hybrid post-quantum key exchange combining X25519 (`crypto/ecdh`) and ML-KEM-768 (`crypto/mlkem`) shared secrets via HKDF-SHA256 (group `X25519MLKEM768`); a tampered ML-KEM ciphertext yields a non-matching key (HXC-1476).
    - `carbonsched` — carbon-aware job placement: chooses the lowest-carbon-intensity latency-eligible region (deterministic name tiebreak) with per-job kWh / gCO2 metering (HXC-1480).
    - `gpuattest` (additive `multigpu.go`) — multi-GPU node enumeration: an N-GPU node descriptor yields N distinct, independently-verifiable per-device attestation proofs keyed by GPU UUID; rejects duplicate/empty descriptors (HXC-1573).
    - `workloadrouter` — `UnifiedManager.RouteWorkload`: concurrent per-marketplace price probes + a weighted composite score (inverse price/latency, health) with a TEE multiplier that re-ranks confidential offers when the workload requires a TEE (HXC-1455).
    - `metrics` (additive `tiermetrics.go`) — `TierMetrics` exposes `gpu_tier_utilization`, `gpu_cost_per_hour`, and `provider_health` Prometheus-text series with deterministic ordering and concurrent-safe setters (HXC-1535).
    - `cmd/gpu-pool-manager` — GPU pool manager HTTP front-end: `POST /allocate` returns the tier-correct (lowest-tier healthy, name tiebreak) provider as JSON (`503` when none healthy) and `GET /providers` lists providers with health; httptest-gated, self-contained (HXC-1530).
    - `marketplaceadapter` — `MarketplaceAdapter` interface (`Name`/`GetCurrentPricing`/`SubmitWork`) with a `Name()`-keyed dispatch `Registry` and a `ChutesAdapter` over HTTP (httptest fixture, non-2xx rejected) (HXC-1454).
    - `cmd/e2ee-proxy` — transparent ML-KEM-768 (`crypto/mlkem`) + AES-256-GCM encrypting proxy: `EncryptingTransport` seals request bodies and `DecryptingHandler` opens them, so the on-wire payload between proxy and upstream is ciphertext-only while the client receives the correct plaintext; tampered ciphertext is rejected (HXC-1532).
    - `e2eebench` — `MeasureHandshakeLatency` benchmarks the `hybridkex` full handshake (median + P95), proving median < 1ms on host and enforcing per-iteration key agreement (`ErrKeyDisagreement`) (HXC-1556).
    - `archlint` + `docs/ARCHITECTURE.md` — the hardened L0–L7 architecture diagram and component map, mechanically enforced: `archlint` parses the component-map table and fails the build if any documented component maps to a package path that does not exist on disk (HXC-1424).
  - Wave 67 (5 units, subagent-driven, 5 parallel streams in disjoint packages):
    - `multiraft` — `MultiRaftManager` partitioning cluster state into independent per-shard `go.etcd.io/raft/v3` groups, each electing its own leader and committing independently via per-shard `Propose` routing over an in-process `RaftTransport`; write throughput scales with shard count and a shard re-elects on leader loss while others keep committing (HXC-1387).
    - `stonith` — STONITH fencing: a `FencingAgent` interface with IPMI / AWS-EC2 / Azure-ARM / SBD-shared-disk / NoOp drivers and a `MultiLevelFencer` multi-level fallback, with sink-side fence confirmation; the IPMI real-binary path is an honest SKIP-with-reason when `ipmitool` is absent (HXC-1401).
    - `deviceplugin` — Nomad/K8s-style gRPC device-plugin / GRES fingerprinting framework over real gRPC: plugins register full device fingerprints, the registry parses GRES descriptors, atomically applies fingerprints, allocates by request, and rejects oversubscription (HXC-1407).
    - `internal/gpu` (additive `manager_helixpow.go`) — dual-workload capacity reservation: `ReserveForHelixPoW(fraction)` + `ChutesCapacity()` reporting only the non-reserved remainder, with a strict `>0.80` Helix-load Gepetto starvation guard that zeroes Chutes capacity (HXC-1447).
    - `chutes` (additive `config.go`) — `ChutesMinerConfig` and `ValidatorConfig` deployment descriptors with `Validate()` invariants (non-empty IDs/hotkeys, `GPUCount>0`, non-negative cost, positive cache size, TEE consistency) (HXC-1439).
  - Wave 68 (5 units, subagent-driven, 5 parallel streams in disjoint packages, run concurrently with wave 69):
    - `dst` — standalone FoundationDB-style deterministic simulation harness: single-threaded seeded-RNG event loop with injectable network/disk/logical-clock; same seed => byte-for-byte identical trace + exact failure replay; 1000+ seeded sims (HXC-1419).
    - `porcupine` — self-contained WGL linearizability checker + concurrent history recorder: PASS on linearizable, FAIL (with violating op) on a seeded non-linearizable history (HXC-1421).
    - `kraft` — KRaft-style self-managed Raft metadata quorum (`go.etcd.io/raft/v3`, no ZooKeeper): replicated `CreateTopic`/`ListTopics` + partition-leader assignment, controller election + controller-loss failover (HXC-1417).
    - `internal/chaos` — fault-injection library (PodKill/NetworkPartition/DiskStall/ClockSkew) + canary-guarded runner with automatic rollback on health<SLO; deterministic via injected clock + seeded selection (HXC-1422).
    - `multiraft` (additive `lease.go`) — `LeaseTracker` CockroachDB-style leaseholder local reads (no Raft round-trip), follower routing via `RaftTransport.SendRead`, lease-expiry re-route against an injected clock (HXC-1389).
  - Wave 69 (5 units, subagent-driven, 5 parallel streams in disjoint packages, run concurrently with wave 68):
    - `stonith` — IPMI credential hardening: BMC password delivered via the `IPMI_PASSWORD` env + `-E` (never on the argv / process table) with redacted argv in error strings; tests assert the password appears in neither argv nor errors (HXC-908, closing a wave-67 follow-up).
    - `deviceplugin` — concurrency test driving `ApplyFingerprint`/`Allocate`/`Release` from 50+ goroutines under `-race`, proving the Registry never oversubscribes (mutex removal trips the race detector) (HXC-910, closing a wave-67 follow-up).
    - `internal/llm` — replaced the stub `Inference` with a real strategy-based router (latency/throughput/quality/cost) over the live model registry, excluding unhealthy/unloaded models with a typed no-eligible-model error and a real injectable `Backend` seam (HXC-1470).
    - `metrics` (additive `earningsmetrics.go`) — `EarningsMetrics` exposing `tao_earnings_total` / `graval_attestation_status` / `token_throughput` / `gpu_utilization` Prometheus-text series with deterministic (sorted) ordering + concurrent-safe setters (HXC-1483).
    - `internal/gpu` (additive `attesthook.go`) — GraVal attestation hook: `AllocateForChutes` refuses a GPU that has not passed attestation (`ErrAttestationRequired`), even on the idempotent re-allocation path; injectable `ChutesAttestor` seam (HXC-1448).
  - Wave 70 (5 units, subagent-driven, 5 parallel streams in disjoint packages, run concurrently with wave 71):
    - `provider/chutes` — OpenAI-compatible `/v1/chat/completions` provider (`cpk_` Bearer) with HTTP-429 retry+backoff; httptest proves retry-then-success, Bearer header, and typed errors (HXC-1510).
    - `pool` (additive `model.go`) — `GPUDevice`/`WorkloadSpec`/`GPUAllocation`/`PoolStatus` data model with snake_case JSON tags + round-trip/concurrency tests (HXC-1495).
    - `quantization` (additive `awq.go`) — AWQ 4-bit default serving format + `EstimateVRAM` (~25% of fp16) + budget-aware `SelectFormat` preferring AWQ when fp16 won't fit (HXC-1473).
    - `internal/gateway` (additive `inference.go`) — inference route forwarding to an injectable Chutes client; `TEERequired` => `X-E2EE-Enabled` routing marker; client failure => 502 (HXC-1469).
    - `internal/gpu` (additive `mig.go`) — MIG profile management: standardized vGPU tiers (1g.10gb/2g.20gb/3g.40gb/7g.80gb), in-memory partitioning with oversubscription rejection + fixed-at-creation immutability (HXC-1449).
- **Security fix (HIGH):** closed a TOCTOU replay-bypass in `pkg/gpuattest.Verify` — the nonce check and consume are now atomic, with a concurrent-replay `-race` regression test.
- Every foundation package ships with tests that fail under mutation of the logic they cover (CLAUDE-1 enforcement) and are gated by whole-tree `build`/`vet` + `-race`.
- Initial project scaffold with Go workspace.
- 29 submodules integrated at project root.
- Directory structure: `cmd/`, `pkg/`, `internal/`, `api/`, `web/`, `scripts/`, `deploy/`, `test/`.

### Changed
- **Governance:** CLAUDE-3 / AGENT-2 / QWEN-2 documentation continuous-sync guarantee added (restates and cites Constitution §11.4.106 docs-chain); `.docs_chain/contexts/tracked_docs.yaml` extended to enforce README, CLAUDE/AGENTS/QWEN, CHANGELOG, FOUNDATION_PACKAGES, and CODEGRAPH export-sync.

## [0.1.0-dev] - 2026-05-30

### Added
- Phase 0 bootstrap: submodule cloning and workspace initialization.
