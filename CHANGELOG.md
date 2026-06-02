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
  - This session (29 pure-Go units):
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
