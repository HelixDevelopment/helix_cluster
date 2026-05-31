# chutes-audit
- **Repo:** https://github.com/chutesai/chutes-audit
- **Language:** Python
- **License:** MIT
- **Maturity:** active
- **Distributed-Computing Relevance:** medium
- **Portability Verdict:** REFERENCE
- **Target Helix Module:** miner/marketplace (Phase8/8B) — audit & weight/scoring submodule (e.g. `pkg/marketplace/audit`)

## Purpose
A standalone, single-file (`audit.py`) "lite validator" that independently reproduces and verifies Chutes (Bittensor subnet 64) reward distribution: it downloads validator/miner audit reports, verifies their integrity against on-chain `set_commitment` SHA-256 checksums, recomputes per-miner incentives from compute-unit lifetime data in Postgres, and (optionally) sets subnet weights on-chain. It exists to prove the validator is distributing requests/rewards to miners fairly, and to let third parties audit subnet activity without running the full (expensive) validator stack.

## Capabilities
- Independent verification of audit reports: pulls a list of audit entries from `api.chutes.ai/audit/`, downloads each JSON report plus the associated jobs CSV exports, and verifies SHA-256 of report content against the validator's on-chain commitment (`Commitments.CommitmentOf` storage on subtensor). (Note: the chain commitment cross-check is currently commented out / "temporarily disabling commitment checks" in `download_and_check_one`.)
- Reproducible incentive calculation: recomputes each miner's score purely from time-weighted compute units (`SUM(overlap_seconds * compute_multiplier)`) over a 1-day scoring interval, normalizes to a distribution, and compares the locally calculated incentive against the live metagraph incentive (deltas typically <0.2%).
- On-chain weight setting: when configured as a registered validator, composes a `SubtensorModule.set_weights` extrinsic (U16-quantized weights, version_key 69420) signed with a Bittensor `Keypair` and submits it via `async-substrate-interface`.
- Metagraph sync: fetches the full subnet-64 metagraph via `SubnetInfoRuntimeApi.get_metagraph`, SS58-encodes hotkeys/coldkeys, and upserts node records (stake split tao/alpha, vtrust/consensus, axon ip/port) into Postgres with checksum-based change detection.
- Miner self-report cross-audit: compares validator-reported instance lifetimes against miner Prometheus-exported metrics, computing per-miner agreement ratios and flagging discrepancies (timestamp mismatches, multiplier mismatches, instances claimed by only one side).
- Data reconciliation: rebuilds `instance_compute_history` and `instance_audits` from authoritative CSV feeds (`reconciliation_csv`, `compute_history_csv`), with safety guards against truncating on empty feeds and path-traversal guards on downloaded files.
- Operational tooling: Docker Compose (Postgres 16 + auditor), a pm2-driven git-pull autoupdater (`utils/autoupdater.py` does `git reset --hard origin/<branch>` then re-launches), and blacklisted-hotkey exclusion.

## Distributed-Computing Notes
- **Consensus/weights (Bittensor):** This is the core. It demonstrates the full lite-validator weight-setting path: metagraph read → scoring → U16 normalization/quantization → signed `set_weights` extrinsic on subtensor. Useful as a concrete reference for how a decentralized-compute subnet translates measured work into on-chain consensus weights.
- **Integrity/attestation (chain-anchored):** Reports are anchored by SHA-256 commitments on-chain; the auditor re-derives the hash and compares — a lightweight, verifiable-audit-log pattern (commit-then-prove) that is reusable independent of Bittensor.
- **Scheduling/placement:** None directly. The repo consumes the *outputs* of placement (instance lifetimes, compute multipliers) but contains no scheduler, placement, or bin-packing logic.
- **GPU validation/attestation (GraVal), TEE/sek8s, E2EE, p2p/gossip (fiber), inference routing/serving:** None present. GPU enters only as a per-chute `gpu_count` sync from a REST endpoint and as bonus factors already "baked into" `compute_multiplier`. No GraVal, no confidential compute, no transport.
- **Fault tolerance:** Modest — `backoff` retry decorators, transactional Postgres reconciliation, restart-always containers, and an outer retry loop around the integrity checker. This is operational resilience, not distributed fault tolerance.
- **Scoring model:** Pure compute-unit lifetime accounting (instance active seconds × time-varying multiplier) with all bonuses (bounty, urgency, TEE, private) pre-folded into the multiplier. Heavy reliance on Postgres window/lateral-join SQL for the math.

## HelixCluster Gaps Addressed
- **miner/marketplace (Phase 8/8B):** Strongest fit. If Helix builds a decentralized-compute marketplace with miners/validators, this is a reference design for the *audit & reward-reconciliation* half: how to (a) measure miner contribution as time-weighted compute units, (b) publish tamper-evident audit reports anchored to a checksum, (c) let independent parties reproduce the reward distribution, and (d) detect miner self-report fraud via cross-source agreement ratios.
- **leader/consensus:** Tangential reference only — shows a real on-chain weight/consensus-write path, but Helix's consensus is Raft/SWIM, not Bittensor, so this does not map directly.
- **security/E2EE, GPU(planned), scheduler/Omega, federation(Phase6), LLMOrchestrator, Messaging/EventBus, discovery:** Not addressed. The repo contains no scheduling, GPU attestation, E2EE, messaging, or service-discovery code.

## Dependencies
- `async-substrate-interface` (>=1.0.0) and `bittensor-wallet` (>=4.0.1) — Bittensor/Substrate chain access and keypair signing (the load-bearing, non-portable deps).
- `sqlalchemy` (2.x, async) + `asyncpg` + Postgres 16 — all scoring math runs in SQL; requires the external `psql` client for COPY-based reconciliation.
- `aiohttp`, `orjson`/`pyyaml`, `pydantic`, `loguru`, `munch`, `backoff`, `tqdm`, `cryptography<44`.
- Runtime: Python 3.10–3.12, `uv`/poetry, Docker Compose; pm2 + git for the autoupdater.

## Rationale
REFERENCE, not PORT/WRAP. The repo is tightly coupled to three things Helix does not have and likely will not adopt: (1) the Bittensor/Substrate subnet-64 consensus mechanism, (2) a specific Chutes API (`api.chutes.ai/...`) as the source of truth, and (3) a Postgres-centric scoring model expressed as bespoke SQL. There is no reusable distributed-systems primitive (no scheduler, gossip, attestation, transport) to lift. Its enduring value to Helix is *conceptual*: the commit-then-verify audit-log pattern and the independent, reproducible reward-reconciliation design are directly applicable to a future Helix compute marketplace and can be re-implemented natively in Go. Porting the code itself would import the entire Bittensor stack for little gain.

## Risks
- **Language mismatch:** Pure async Python (SQLAlchemy/asyncpg); Helix is Go. No shared runtime; any reuse is a re-implementation, not a binding.
- **License:** MIT — permissive, compatible with porting/derivation; no license risk.
- **Hard Bittensor coupling:** `async-substrate-interface` + `bittensor-wallet` + subtensor RPC are foundational here and irrelevant to a Raft/SWIM cluster OS; can't be excised without rewriting the scoring/weight path.
- **External-API dependence:** Correctness depends on `api.chutes.ai` endpoints and on-chain commitments; not self-contained — the "audit" trusts a central API for CSV feeds even as it verifies on-chain hashes.
- **Operational sharp edges:** Autoupdater runs `git reset --hard origin/<branch>` and relaunches automatically (supply-chain/foot-gun risk); the on-chain commitment cross-check is currently commented out, weakening the integrity guarantee versus what the README claims; scoring is heavy on a single Postgres node (README demands NVMe, 64GB+ RAM, 8h+ initial sync). Fork-drift risk is low (single active repo, not a fork) but the autoupdate-from-HEAD model means deployments silently track upstream.
