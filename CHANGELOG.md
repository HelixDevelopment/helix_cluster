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
  - This session (12 pure-Go units):
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
