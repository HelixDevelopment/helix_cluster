# chutes-api
- **Repo:** https://github.com/chutesai/chutes-api
- **Language:** Python
- **License:** MIT
- **Maturity:** production
- **Distributed-Computing Relevance:** core
- **Portability Verdict:** REFERENCE
- **Target Helix Module:** pkg/scheduler (Omega) + pkg/resources/GPU(planned) + security/E2EE + a NEW `minermarket` submodule (Phase8/8B); GraVal/TEE attestation → a NEW `pkg/attestation` reference design

## Purpose
chutes-api is the production control plane for the chutes.ai decentralized GPU compute platform — a Bittensor subnet validator/orchestrator. It accepts user-defined "chutes" (containerized inference apps), places them onto miner-provided GPU servers, cryptographically validates/attests those GPUs (GraVal + NVIDIA nvattest + Intel TDX), routes E2EE inference traffic to verified instances, meters invocations, and translates utilization/quality into on-chain Bittensor weight settings that pay miners.

## Capabilities
- FastAPI + Uvicorn async control plane (`api/main.py`) mounting per-domain routers: users, chutes, images, instances, invocations, payments, miner, node, server, jobs, registry, secrets.
- Decentralized GPU marketplace: miners register `servers` → `nodes` (one row per physical GPU, with full device fingerprint: UUID, VRAM, SM major/minor, processor count, clock, ECC, SXM) via `api/node/schemas.py`.
- **GraVal GPU validation/attestation**: `api/graval_server.py` (challenge/cipher generator using the `graval` library + Bittensor `Keypair`) and `api/graval_worker.py` (taskiq Redis-queue worker) issue per-GPU memory-bound cryptographic challenges. Each `Node` carries a `seed`; GraVal proves a specific physical GPU with claimed VRAM actually holds the data, defeating spoofed/oversubscribed GPUs.
- **Hardware attestation**: `nv-attest/` subpackage wraps NVIDIA `nv-attestation-sdk` / `nv-local-gpu-verifier` / `nv-ppcie-verifier` for remote GPU attestation; `api/server/quote.py` + `service.py` parse and verify **Intel TDX** confidential-VM quotes (MRTD + RTMR0-3 measurements) against expected `TeeMeasurementConfig`, with boot-time and runtime measurement migrations.
- **Autoscaler / scheduler** (`chute_autoscaler.py`, ~147KB): utilization-driven scale up/down with a Redis distributed lock (`autoscaler:lock`, SET NX + TTL), demand-based instance calculation, scale-down lookback/drop-ratio guards, and a **bounty** mechanism (`api/bounty/`) that incentivizes miners to deploy under-served chutes (capacity placement via economic signal rather than central bin-packing).
- **NodeSelector scheduling constraints** (`api/chute/schemas.py`): `gpu_count` (1-8), `min_vram_gb_per_gpu`, `supported_gpus` derived from a `SUPPORTED_GPUS` catalog (`api/gpu.py`) with per-GPU compute multipliers and hourly rate basis — i.e. a GPU-aware placement/affinity model.
- **E2EE inference transport** (`api/util.py`): X25519 ECDH session-key derivation (`derive_x25519_session_key`) + ChaCha20-Poly1305 AEAD (`encrypt_instance_request` / `decrypt_instance_response`) per instance, so the validator↔miner inference path is end-to-end encrypted with a per-instance `rint_session_key`.
- **Signed miner/validator RPC** (`api/miner_client.py`): every validator→miner request is signed with the Bittensor hotkey (ss58 + nonce + sha256(payload)); ed25519-zebra / sr25519 signatures, replay-protected by nonce headers.
- **Bittensor consensus integration** (`metasync/`): `sync_metagraph.py` pulls subnet metagraph via `async-substrate-interface`; `set_weights_on_metagraph.py` normalizes/quantizes per-miner scores to U16 and sets on-chain weights every scoring period; `shared.get_scoring_data` derives scores from invocation metrics.
- **Watchtower** (`watchtower.py`, ~66KB): continuous liveness/integrity prober — parses miner host TCP state tables, verifies expected container command (`get_expected_command`/`verify_expected_command`), env dumps, and purges instances that fail integrity.
- Real-time bi-directional comms: Socket.IO server (`api/socket_server.py`, `event_socket_server.py`) + Redis pub/sub fan-out (`api/redis_pubsub.py`) for validator↔miner streaming.
- Per-chute subdomain HTTP routing/serving (slug-based, wildcard TLS), vLLM/SGLang chute templates, registry proxy with hotkey-signature docker auth, encrypted invocation log export, fault-tolerant instance purge/reconcile loops.
- Code-safety sandboxing of user-submitted chute code via AST inspection (`api/affine.py`): blocks dangerous builtins, bounds AST depth/size.

## Distributed-Computing Notes
- **GPU validation/attestation is the crown jewel.** Three independent layers: (1) GraVal memory-hard proof-of-GPU (a physical GPU with claimed VRAM must compute a seeded challenge), (2) NVIDIA hardware attestation via nvattest SDK, (3) Intel TDX confidential-VM quote verification (MRTD/RTMR measurement matching). This is a complete trust-establishment pipeline for *untrusted, third-party* GPUs — exactly what a decentralized cluster OS lacks.
- **Scheduling/placement is economic, not bin-packing.** Placement is driven by a bounty/auction loop + utilization autoscaler with a Redis distributed lock for single-writer safety; miners self-select work. NodeSelector provides the hard constraints (GPU type, VRAM, count) and compute multipliers provide cost-weighting.
- **p2p/gossip**: Not classic SWIM/gossip — topology is validator-hub ↔ miners over signed HTTPS + Socket.IO + Redis pub/sub. Membership/consensus is delegated to the Bittensor subtensor chain (the README's "fiber" p2p is upstream Bittensor, not in this repo).
- **Consensus/weights**: full Bittensor Yuma-consensus participation — scores → normalized U16 weights → `set_weights` extrinsic. This is reputation/incentive consensus layered on Substrate, distinct from Helix's Raft/SWIM.
- **TEE/confidential compute**: first-class Intel TDX quote parsing + RTMR measurement allow-listing, plus NVIDIA confidential-compute (PPCIE) verifier. Boot and runtime measurement versioning via SQL migrations.
- **E2EE serving**: X25519 + ChaCha20-Poly1305 per-instance session keys — production-grade confidential inference transport.
- **Fault tolerance**: watchtower integrity prober + autoscaler reconcile/purge + Redis-lock idempotency; relies on Postgres (Aurora/AlloyDB) + Redis + Kubernetes, not on a custom consensus core.

## HelixCluster Gaps Addressed
- **GPU (planned pkg/resources/GPU):** Provides a battle-tested *adversarial* GPU verification design (GraVal proof-of-GPU + NVIDIA/TDX attestation) — Helix currently has no way to trust a GPU it did not provision. This is the single highest-value transfer.
- **scheduler/Omega:** Demand/utilization autoscaling with a distributed-lock single-writer pattern and economic placement is a reference contrast to Helix's optimistic-concurrency Omega scheduler; the NodeSelector constraint model (gpu_count/min_vram/supported_gpus/compute-multiplier) maps directly onto Helix resource requests.
- **security/E2EE (Phase6 federation):** X25519+ChaCha20-Poly1305 per-instance transport and hotkey-signed, nonce-replay-protected RPC are a clean blueprint for Helix cross-node/federation confidential transport and signed control messages.
- **miner/marketplace (Phase8/8B):** End-to-end design for a decentralized compute marketplace — registration, bounties, metering, payment, and incentive-weighted payout — directly informs Helix's Phase8/8B miner/marketplace work.
- **leader/consensus:** Demonstrates delegating membership/incentive consensus to an external chain (Bittensor) while keeping orchestration centralized — an alternative to baking everything into Raft.
- **Messaging/EventBus & discovery:** Socket.IO + Redis pub/sub fan-out and slug-subdomain instance routing offer concrete patterns for Helix EventBus/discovery, though Helix already targets NATS.
- **TEE (NEW pkg/attestation):** TDX/nvattest quote verification is a ready reference for a confidential-compute module Helix does not yet have.

## Dependencies
fastapi, uvicorn, SQLAlchemy 2.x + asyncpg (Postgres), redis + taskiq-redis (queues/locks), `graval` (GPU challenge lib), `bittensor-wallet` + `async-substrate-interface` + `bittensor-drand` (subnet/weights), `py-ed25519-zebra-bindings` / `rbcl` / `cryptography` (X25519, ChaCha20-Poly1305, signatures), `dcap-qvl` (Intel TDX quote verification), `nv-attestation-sdk` / `nv-local-gpu-verifier` / `nv-ppcie-verifier` (NVIDIA attestation, in nv-attest), `chutes` SDK (git pin), python-socketio, aioboto3 (S3), transformers/tokenizers/huggingface-hub. Runtime: Kubernetes (microk8s), Ansible bootstrap, Helm charts, S3-compatible object store, wildcard TLS.

## Rationale
REFERENCE (not PORT/WRAP) because: (1) Language mismatch — this is ~10k+ lines of async Python tightly bound to FastAPI/SQLAlchemy/Bittensor/CUDA; a Go cluster OS cannot import it. (2) The deepest value — GraVal proof-of-GPU, TDX/NVIDIA attestation flows, X25519+ChaCha20 E2EE handshake, U16 weight quantization, bounty-based placement, and the NodeSelector constraint model — are *algorithms and protocols* that Helix should re-implement in Go, not binaries to wrap. (3) It is architecturally coupled to the Bittensor chain for consensus/payment, which Helix does not use. The MIT license makes verbatim porting of specific functions (e.g. the attestation/E2EE handshake) fully permissible, so this is a high-fidelity blueprint for Helix's planned GPU, attestation, E2EE-federation, and miner-marketplace modules. A thin WRAP of the `graval` and `nv-attest` worker binaries via gRPC/HTTP could be a tactical option if Helix wants attestation before a Go re-implementation exists.

## Risks
- **License:** MIT — low risk; attribution required. Note the upstream `graval`, `chutes` SDK, and `nv-*` SDKs are separate packages with their own (mostly MIT/Apache, but NVIDIA SDKs may carry NVIDIA license terms) — verify each before porting attestation code.
- **Language mismatch:** Pure Python; no reusable Go artifacts — everything must be re-implemented or wrapped as a sidecar.
- **Heavy/specialized deps:** CUDA + `graval` GPU kernels, NVIDIA attestation SDKs (require NVIDIA tooling/keys), Intel TDX hardware/quoting service, Bittensor substrate chain — porting attestation requires the corresponding hardware and vendor services.
- **Bittensor coupling:** Consensus, identity (ss58 hotkeys), and payments assume a Substrate subnet; not directly usable without that chain. Helix would borrow the *scoring→weight* and *signed-RPC* patterns, not the chain dependency.
- **Operational weight:** Requires Postgres (Aurora/AlloyDB), Redis cluster, Kubernetes, Ansible, Helm, wildcard TLS, S3 — a large operational surface; reference only the algorithms, not the deployment.
- **Fork drift / pins:** Depends on git-pinned `chutes` SDK and a forked `taskiq-redis` commit; fast-moving production repo (depth-1 clone), so re-validate any ported logic against upstream.
