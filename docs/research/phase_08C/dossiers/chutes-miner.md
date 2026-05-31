# chutes-miner
- **Repo:** https://github.com/chutesai/chutes-miner
- **Language:** Python
- **License:** MIT
- **Maturity:** production
- **Distributed-Computing Relevance:** core
- **Portability Verdict:** REFERENCE
- **Target Helix Module:** pkg/scheduler (Omega) + pkg/resources (GPU) + a NEW `miner`/`marketplace` submodule (Phase8/8B) + security/E2EE

## Purpose
Miner-side node software for the Chutes.ai permissionless, serverless GPU-compute platform on Bittensor subnet 64. It provisions/verifies GPU servers, federates them as standalone Kubernetes (K3s) clusters under Karmada, and runs an optimistic scheduler ("Gepetto") that deploys, autoscales, and preempts containerized AI inference workloads ("chutes") to maximize a miner's share of validator-measured compute time.

## Capabilities
- **Cost/placement-aware GPU scheduler (`gepetto.py`, ~2280 LOC):** event-driven control loop with `activator`, `autoscaler`, and `reconciler` async tasks; `optimal_scale_up_server` picks the cheapest server (`ORDER BY hourly_cost ASC, free_gpus ASC`) with enough verified free GPUs matching the chute's `supported_gpus` and TEE flag, gated by node disk availability.
- **Preemption engine (`preempting_deploy`, `scale_chute`):** preempts the least-valuable running instance ranked by `compute_multiplier`; never preempts non-preemptible/private deployments, the only global instance of a chute, or instances whose multiplier ≥ the new chute's effective multiplier. Uses a global active-instance view fetched from the validator for cross-miner preemption decisions.
- **GPU attestation/verification (GraVal):** bootstrap FastAPI service per node verifies each GPU via matrix-multiplication proofs seeded by device info, asserts ≥95% of advertised VRAM is usable, and derives per-GPU AES decryption keys.
- **GPU bootstrap & lifecycle (`api/server/verification.py`, ~860 LOC):** creates K8s Jobs/Services to run GraVal (and TEE) bootstrap, watches verification, persists verified GPUs into Postgres.
- **Multi-cluster federation:** each GPU node is its own K3s cluster; the control plane aggregates them via Karmada search-cache APIs (`/apis/search.karmada.io/.../nodes|pods|services|deployments`) and federates metrics into a central Prometheus.
- **Resource collector agent (`chutes-agent`):** in-cluster `kubernetes_asyncio` collector snapshots nodes/deployments/pods/services/jobs across watched namespaces and exposes them to the control plane.
- **Validator websocket + Redis pubsub event bus:** `socket.io` client authenticates to the validator with a Bittensor signature and relays `miner_broadcast` events (gpu_verified, chute_created/deleted, bounty_change, instance_*, rolling_update, job_*) into internal Redis pubsub handlers.
- **Authenticated registry proxy (`chutes-registry`):** nginx + miner-API subrequest injects Bittensor key-signature auth so private chute images load as `[validator-hotkey].localregistry.chutes.ai:30500/...`.
- **TEE / confidential-VM management (`chutes-miner-cli/tee*.py`):** signed CLI to a per-VM "system-manager" on port 8080 for TEE node status, image cache, and service log streaming; servers carry an `is_tee` flag enforced end-to-end in scheduling.
- **HuggingFace model cache management** (`chutes-cache-cleaner`) with size/age eviction to optimize cold-start times; bounty-claiming for first-to-serve.

## Distributed-Computing Notes
- **GPU validation/attestation (GraVal):** proof-of-GPU via deterministic CUDA matrix multiplications keyed on real device info (UUID, VRAM), plus a VRAM-capacity gate. `Challenge{seed, iterations, ciphertext}` → `/prove` returns device proofs and decrypts validator-supplied ciphertext on the exact advertised GPU. This binds workload encryption keys to physical silicon — a strong hardware-rooted attestation primitive. Requires the external `graval` C/CUDA package (v0.2.6) — not portable to Go without an FFI/sidecar.
- **E2EE inference transport:** all traffic to instances is encrypted with keys only the advertised GPU can derive (per-device AES via GraVal `decrypt`), so the GPU itself is the decryption root — confidential transport without trusting the host.
- **Scheduling/placement:** optimistic, cost-minimizing, multiplier-aware bin-packing with preemption — directly analogous to Helix's Omega scheduler with optimistic concurrency.
- **Consensus/weights:** Chutes runs on Bittensor subnet 64; weights are 7-day compute-time sums computed by validators (not in this repo). Miner trusts an explicitly-configured validator allow-list (not the metagraph) — SR25519 signed requests both directions.
- **p2p/gossip:** none of the classic SWIM/fiber gossip here — coordination is hub-and-spoke (validator websocket + Redis pubsub) and cluster federation is via Karmada, not gossip.
- **TEE/confidential compute:** first-class `is_tee` server class with a dedicated confidential-VM system-manager and signed management plane (the sek8s-style path), though hardware attestation/quote verification logic lives outside this repo.
- **Fault tolerance:** reconciler loop continuously reconciles desired vs. actual deployments; server locks (`Server.locked`), pending-deployment guards, and idempotent event handlers.

## HelixCluster Gaps Addressed
- **Scheduler/Omega:** a battle-tested, production reference for cost-aware GPU placement + preemption by value multiplier and optimistic reconciliation — concrete patterns to port into `pkg/scheduler`.
- **Resources / GPU (planned):** the GPU inventory model (verified flag, model_short_ref, per-server free/used GPU accounting, VRAM/RAM ratio rules) is a ready blueprint for Helix `pkg/resources` GPU support.
- **Federation (Phase 6):** Karmada multi-cluster aggregation + Prometheus metric federation is a direct analog for Helix federation across member clusters.
- **Security / E2EE:** GraVal's GPU-bound encryption and SR25519 signed request envelopes inform Helix `security` and confidential-inference transport.
- **Miner / marketplace (Phase 8 / 8B):** end-to-end reference for a decentralized GPU marketplace participant — registration, validator allow-list, hourly-cost economics, bounty claiming, preemptible vs. private tiers.
- **Messaging / EventBus:** validator-broadcast → Redis pubsub fan-out is a concrete event-bus topology to compare against Helix NATS EventBus.
- **LLMOrchestrator:** chute (container) lifecycle, autoscaling, and cold-start cache optimization map onto Helix inference orchestration.

## Dependencies
Python 3.12+; FastAPI/uvicorn, SQLAlchemy 2 + asyncpg (Postgres), Redis (pubsub), `python-socketio` (asyncio client), aiohttp, `substrate-interface` + `py-bip39-bindings` (Bittensor SR25519 keys), `kubernetes`/`kubernetes_asyncio`, loguru, typer/rich (CLI), pydantic-settings. **External heavy dep:** `graval==0.2.6` (C/CUDA GPU-attestation library). Infra: K3s + **Karmada** (multi-cluster), Helm, Ansible, nginx registry proxy, Prometheus/Grafana.

## Rationale
REFERENCE (not PORT/WRAP). The system is Python + CUDA + Bittensor-substrate + Kubernetes/Karmada — architecturally rich and exactly on-target for Helix's distributed-compute thesis, but mechanically un-portable into a Go cluster OS: Gepetto is tightly coupled to SQLAlchemy/Postgres, Redis pubsub, the validator websocket protocol, and the `graval` CUDA native lib. The durable value is the *design*: the scheduler/preemption algorithm, GPU verification+inventory model, GPU-bound E2EE, TEE node class, and Karmada federation pattern should be re-implemented natively in Go. WRAP is conceivable only for the GraVal attestation step (run it as a Python/CUDA sidecar that Helix calls over gRPC/HTTP) — worth a follow-up, but the broader miner is best mined for ideas.

## Risks
- **Language mismatch:** core logic is Python/async + SQLAlchemy; no Go reuse — re-implementation effort, not port.
- **Heavy CUDA/native dep:** `graval` is closed-detail C/CUDA tied to NVIDIA GPUs; can't run on CPU CI and can't be transpiled — must be a sidecar if used at all.
- **Bittensor coupling:** auth, identity (SR58 hotkeys), and the economic/weights model assume subnet 64; the consensus/weights themselves live in `chutes-api`/validator, not here — porting the marketplace requires those too.
- **Karmada/K3s assumption:** the whole control plane assumes Kubernetes + Karmada federation; Helix's own 7-layer Go stack would diverge from this substrate.
- **License:** MIT declared in every `pyproject.toml` but **no top-level `LICENSE` file present** — confirm before copying any code verbatim; design/patterns are safe to reference.
- **Fork drift / external truth:** key contracts (GPU list, weights, validator endpoints) live in sibling repos (`chutes-api`, `graval`), so this repo alone is an incomplete spec.
- **Effort to realize value:** L (re-architecting scheduler + GPU model + federation in Go); a GraVal sidecar WRAP alone would be M.

**Effort:** L
