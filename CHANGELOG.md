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
- **Security fix (HIGH):** closed a TOCTOU replay-bypass in `pkg/gpuattest.Verify` — the nonce check and consume are now atomic, with a concurrent-replay `-race` regression test.
- Every foundation package ships with tests that fail under mutation of the logic they cover (CLAUDE-1 enforcement) and are gated by whole-tree `build`/`vet` + `-race`.
- Initial project scaffold with Go workspace.
- 29 submodules integrated at project root.
- Directory structure: `cmd/`, `pkg/`, `internal/`, `api/`, `web/`, `scripts/`, `deploy/`, `test/`.

## [0.1.0-dev] - 2026-05-30

### Added
- Phase 0 bootstrap: submodule cloning and workspace initialization.
