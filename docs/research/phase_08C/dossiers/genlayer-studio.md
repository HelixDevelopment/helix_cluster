# genlayer-studio
- **Repo:** https://github.com/chutesai/genlayer-studio
- **Language:** Python
- **License:** MIT
- **Maturity:** fork-tracking
- **Distributed-Computing Relevance:** medium
- **Portability Verdict:** REFERENCE
- **Target Helix Module:** pkg/scheduler (Omega) + LLMOrchestrator (consensus-pattern reference); NOT a direct port target

## Purpose
GenLayer Studio is a local, dockerized sandbox that replicates the GenLayer L1 protocol's execution environment and consensus algorithm so developers can write and test "Intelligent Contracts" — smart contracts whose logic invokes LLMs and the web. It is a single-host SIMULATOR of a decentralized network (leader + N stake-weighted validators reaching consensus over non-deterministic LLM outputs), not the production network itself.

## Capabilities
- Stake-weighted (VRF-style) validator-set selection per transaction via `numpy` weighted sampling over validator stake (`backend/consensus/vrf.py`).
- Leader-and-validators consensus state machine with a rich appeal/rotation protocol: AGREE / DISAGREE / TIMEOUT / NO_MAJORITY / leader-rotation, plus validator/leader appeal rounds (`backend/consensus/types.py`, `base.py` ~4000 LOC).
- Distributed work-claiming across consensus workers using Postgres `SELECT ... FOR UPDATE SKIP LOCKED` to atomically claim pending transactions, appeals, and finalization tasks (`backend/consensus/worker.py`).
- Redis-backed worker message handler + FastAPI consensus-worker service with crash/restart tracking and health probes (`backend/consensus/worker_service.py`).
- Pluggable LLM validators: per-validator model/provider config, fallback validator selection (different provider class / different model) for fault tolerance (`backend/validators/__init__.py`); 40+ provider presets (OpenAI, Anthropic, Google, OpenRouter, io.net, Ollama, xAI, Heurist).
- GenVM: a WASM-based deterministic contract VM with host bindings, calldata ABI, retry, and execution-timeout gating (`backend/node/genvm/`).
- Rollup bridge: posts consensus results to an EVM chain (Hardhat/Web3 pool, `backend/rollup/consensus_service.py`).
- JSON-RPC protocol endpoints, finality window with appeal-failed decay, transaction lifecycle persistence in Postgres.

## Distributed-Computing Notes
- **Consensus/weights:** Real, working stake-weighted validator sampling and a multi-round leader/appeal BFT-style protocol — but executed in a SINGLE process/host simulation; there is no real p2p, no gossip, no networked Raft/SWIM. "Distribution" is emulated via DB rows + Redis queue + multiple worker processes sharing one Postgres.
- **Fault tolerance:** Concrete patterns worth studying — `FOR UPDATE SKIP LOCKED` lease-free work stealing, idle-validator replacement (`MAX_IDLE_REPLACEMENTS`), leader-crash retry, validator exec timeouts, fallback-provider failover.
- **GPU validation/attestation (GraVal):** NONE. No GraVal, no GPU attestation, no TEE/confidential compute, no E2EE inference transport.
- **p2p/gossip (fiber), Bittensor subnet/weights, sek8s:** NONE. Despite being in the chutesai org, this repo contains zero Chutes-specific code.
- **Inference serving/routing:** LLM calls are plain HTTPS to external provider APIs (or local Ollama); no GPU scheduling/placement, no miner marketplace.

## HelixCluster Gaps Addressed
- **scheduler/Omega (optimistic concurrency):** Strong reference. The Postgres `SKIP LOCKED` work-claiming + optimistic appeal/retry loop is a clean, battle-tested pattern for Omega-style optimistic-concurrency scheduling and idempotent task claiming.
- **leader/consensus:** Reference design for a leader+validator quorum with explicit rotation/appeal states and timeout handling — useful as a design comparison for Helix's SWIM+Raft layer (conceptual only; Helix already has stronger real consensus).
- **LLMOrchestrator:** Useful pattern for multi-provider LLM validator pools with weighted selection and provider/model fallback failover — directly relevant to routing/redundancy in LLMOrchestrator.
- Does NOT address: GPU compute, federation (Phase 6), E2EE/security, miner/marketplace (Phase 8/8B), resources, discovery — none of those exist here.

## Dependencies
Python 3 / FastAPI / uvicorn / asyncio, SQLAlchemy + Postgres 16, Redis, numpy, aiohttp/requests, loguru, web3 (EVM rollup), Hardhat (Node.js), GenVM (WASM runtime), Vector (log shipping), Vue/TS frontend + explorer. Docker Compose orchestrates ~13 services.

## Rationale
REFERENCE, not PORT/WRAP. (1) It is a passive mirror of upstream `genlayerlabs/genlayer-studio` — `chutesai/main` is 0 commits ahead and 99 behind upstream (origin is a strict ancestor of upstream); there are NO Chutes additions, so the "GPU/Bittensor/GraVal" interest does not apply to this repo at all. (2) It is a single-host SIMULATOR of a decentralized network, not real distributed infrastructure. (3) It is Python+WASM+EVM, fundamentally mismatched with Helix's Go 7-layer stack. (4) Its genuinely valuable assets are PATTERNS — stake-weighted selection, leader/appeal consensus state machine, and `SKIP LOCKED` optimistic work-claiming — which Helix should learn from and re-implement idiomatically in Go, not import.

## Risks
- **Fork drift:** Repo is already 99 commits behind upstream; no Chutes value-add, so tracking it is pointless — read upstream instead.
- **Language mismatch:** Pure Python/asyncio + WASM GenVM + Solidity/EVM rollup; nothing reusable as a Go library.
- **Domain mismatch:** Tightly coupled to GenLayer's blockchain/intelligent-contract domain (calldata ABI, rollup posting, finality windows); the consensus is about LLM-output agreement, not general compute scheduling.
- **Simulation only:** "Validators" are DB rows; there is no real networking, so its consensus cannot be lifted into a real distributed system without redesign.
- **License is permissive (MIT)** — safe to copy/adapt patterns, but there is little code worth copying verbatim into Go.
