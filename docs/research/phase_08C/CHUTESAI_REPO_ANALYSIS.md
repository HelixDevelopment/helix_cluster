# Chutes AI Org — Repository Analysis (Phase 8C, COMPLETE)

> **Research Document** | Phase 8C — Distributed-Computing Capability Survey | 2026-05-31 (FINAL)
>
> Structured analysis of **all 42 repositories** in the `chutesai` GitHub org, focused on DISTRIBUTED COMPUTING capabilities and how HelixCluster can utilize/port them. Repositories are grouped by `distributed_computing_relevance` (core → high → medium → low → none).
>
> **Revision note (FINAL pass):** The first synthesis covered only 22 repos because 20 analysis agents had failed — including the most important distributed-compute repos. Those 20 (`graval`, `chutes-miner`, `chutes-api`, `chutes`, `fiber`, `sek8s`, `chutes-e2ee-transport`, `e2ee-proxy`, `chutes-audit`, `cllmv`, `genlayer-studio`, `research-data-opt-in-proxy`, `squad-api`, `chutes-autopilot`, `claude-proxy`, `codex`, `cpp-oasvalidator`, `responses-proxy`, `DeepGEMM`, `lua-oasvalidator`) have now been re-analyzed and merged here. **This corrects the earlier note that claimed no CORE repos existed** — the actual Chutes distributed-compute substance (GraVal GPU attestation, the Gepetto scheduler, the chutes-api validator/orchestrator, the in-pod trust runtime, sek8s TEE, fiber transport) lives in these now-analyzed repos and is given prominent CORE/HIGH treatment below.
>
> **License flag:** Any repo marked `UNKNOWN` license is treated as **all-rights-reserved by default** and is a **hard blocker for PORT/WRAP of code**. Only clean-room reimplementation from public wire/spec descriptions is permitted for those. (Affects: `chutes-autopilot`, `responses-proxy`, `claude-proxy`, `research-data-opt-in-proxy`, plus the previously-noted `chutes-docs`, `e2ee-test`, `chutes-jumpmaster`, `model-router`, `chutes-dropzone`, `chutes-n8n-local`, `chutes-style`, `chutes-agent-toolkit`.)

---

## Relevance Tiers — Index (all 42)

| Tier | Repos |
|------|-------|
| **CORE** | chutes-api, chutes-miner, graval, chutes |
| **HIGH** | sek8s, fiber, chutes-e2ee-transport, e2ee-proxy, chutes-docs, sglang, vllm |
| **MEDIUM** | chutes-audit, cllmv, genlayer-studio, research-data-opt-in-proxy, squad-api, chutes-autopilot, e2ee-test |
| **LOW** | claude-proxy, codex, cpp-oasvalidator, responses-proxy, DeepGEMM, chutes-jumpmaster, model-router, bittencert, SageAttention, SageAttention-1, chutes-dropzone, chutes-n8n-local, SaintDurbin (st_durbin), chutes-search, ai-sdk-provider-chutes |
| **NONE** | lua-oasvalidator, hermes-agent, TurboDiffusion, chutes-style, n8n-nodes-chutes, openclaw, moltbot, Sign-in-with-Chutes, chutes-agent-toolkit |

> **Correction to prior note on CORE:** The earlier draft claimed "no single repo is core distributed-compute infrastructure." That was an artifact of the 20 failed agents. The CORE tier is now populated: `chutes-api` (validator/orchestrator control plane), `chutes-miner` (Gepetto cost-aware GPU scheduler + preemption), `graval` (software GPU attestation / proof-of-GPU-work), and `chutes` (in-pod trust runtime: GraVal/TEE verification + Aegis E2EE). Together these are the canonical reference for trustless GPU compute. None are PORT targets (Python/CUDA/closed `.so` + Bittensor coupling), but all are high-fidelity blueprints for Helix's planned GPU/attestation/scheduler/marketplace modules under MIT licensing.

---

# TIER: CORE

## chutes-api

- **Repo:** https://github.com/chutesai/chutes-api
- **Purpose:** Production control plane for the chutes.ai decentralized GPU compute platform — a Bittensor subnet validator/orchestrator. Accepts user-defined "chutes" (containerized inference apps), places them onto miner-provided GPU servers, cryptographically validates/attests those GPUs (GraVal + NVIDIA nvattest + Intel TDX), routes E2EE inference traffic to verified instances, meters invocations, and translates utilization/quality into on-chain Bittensor weight settings that pay miners.
- **Language:** Python · **License:** MIT · **Maturity:** production
- **Capabilities:**
  - FastAPI async control plane mounting per-domain routers (users, chutes, images, instances, invocations, payments, miner, node, server, jobs, registry, secrets).
  - Decentralized GPU marketplace: miners register `servers` → `nodes` (one row per physical GPU with full device fingerprint: UUID, VRAM, SM major/minor, processor count, clock, ECC, SXM).
  - **GraVal GPU validation/attestation:** `graval_server.py` + `graval_worker.py` (taskiq Redis worker) issue per-GPU memory-bound cryptographic challenges; each `Node` carries a `seed`; proves a specific physical GPU with claimed VRAM holds the data, defeating spoofed/oversubscribed GPUs.
  - **Hardware attestation:** `nv-attest/` wraps NVIDIA nv-attestation-sdk / nv-local-gpu-verifier / nv-ppcie-verifier; `server/quote.py` parses & verifies **Intel TDX** confidential-VM quotes (MRTD + RTMR0-3) against `TeeMeasurementConfig`.
  - **Autoscaler/scheduler** (`chute_autoscaler.py`, ~147KB): utilization-driven scale up/down with a Redis distributed lock (`autoscaler:lock`, SET NX + TTL), demand-based instance calculation, scale-down lookback/drop-ratio guards, and a **bounty** mechanism incentivizing miners to deploy under-served chutes (economic placement, not central bin-packing).
  - **NodeSelector scheduling constraints:** `gpu_count` (1-8), `min_vram_gb_per_gpu`, `supported_gpus` from a `SUPPORTED_GPUS` catalog with per-GPU compute multipliers + hourly rate basis.
  - **E2EE inference transport:** X25519 ECDH session-key derivation + ChaCha20-Poly1305 AEAD per instance (`encrypt_instance_request`/`decrypt_instance_response`).
  - **Signed miner/validator RPC:** Bittensor hotkey (ss58 + nonce + sha256(payload)); ed25519-zebra / sr25519 signatures, nonce replay protection.
  - **Bittensor consensus integration** (`metasync/`): metagraph sync + `set_weights_on_metagraph.py` normalizing/quantizing per-miner scores to U16 and setting on-chain weights each scoring period.
  - **Watchtower** (~66KB): continuous liveness/integrity prober — parses miner host TCP state tables, verifies expected container command, env dumps, purges instances failing integrity.
  - Socket.IO server + Redis pub/sub fan-out; per-chute subdomain HTTP routing; vLLM/SGLang chute templates; registry proxy with hotkey-signature docker auth; AST-based code-safety sandboxing of user chute code.
- **Distributed-computing relevance:** CORE — GPU validation/attestation is the crown jewel (three independent layers: GraVal memory-hard proof-of-GPU, NVIDIA hardware attestation, Intel TDX quote verification). Economic/bounty placement; Bittensor Yuma-consensus weights; production-grade confidential serving.
- **Portability verdict:** REFERENCE
- **Target Helix module:** `pkg/scheduler` (Omega) + `pkg/resources`/GPU(planned) + security/E2EE + a NEW `minermarket` submodule (Phase 8/8B); GraVal/TEE attestation → NEW `pkg/attestation` reference design
- **Effort:** L
- **Rationale:** ~10k+ lines of async Python tightly bound to FastAPI/SQLAlchemy/Bittensor/CUDA; a Go cluster OS cannot import it. The deepest value (GraVal proof-of-GPU, TDX/NVIDIA attestation flows, X25519+ChaCha20 E2EE handshake, U16 weight quantization, bounty placement, NodeSelector constraint model) are *algorithms/protocols* to re-implement in Go. MIT permits verbatim porting of specific functions. A thin WRAP of the `graval`/`nv-attest` workers via gRPC/HTTP is a tactical option before a Go re-implementation exists.
- **Risks:** Language mismatch; heavy CUDA + NVIDIA/Intel attestation hardware/vendor services; Bittensor consensus/identity coupling; large operational surface (Postgres/Aurora, Redis cluster, K8s, Helm, S3, wildcard TLS); fork drift / git-pinned `chutes` SDK & forked taskiq-redis.

## chutes-miner

- **Repo:** https://github.com/chutesai/chutes-miner
- **Purpose:** Miner-side node software for the chutes.ai permissionless serverless GPU platform (Bittensor SN64). Provisions/verifies GPU servers, federates them as standalone K3s clusters under Karmada, and runs the optimistic **Gepetto** scheduler that deploys, autoscales, and preempts containerized AI inference workloads ("chutes") to maximize a miner's share of validator-measured compute time.
- **Language:** Python · **License:** MIT · **Maturity:** production
- **Capabilities:**
  - **Cost/placement-aware GPU scheduler (`gepetto.py`, ~2280 LOC):** event-driven control loop with `activator`/`autoscaler`/`reconciler` async tasks; `optimal_scale_up_server` picks the cheapest server (`ORDER BY hourly_cost ASC, free_gpus ASC`) with enough verified free GPUs matching the chute's `supported_gpus` and TEE flag, gated by node disk.
  - **Preemption engine:** preempts the least-valuable running instance ranked by `compute_multiplier`; never preempts non-preemptible/private/sole-global instances or instances whose multiplier ≥ the new chute's. Cross-miner preemption uses a global active-instance view from the validator.
  - **GPU attestation/verification (GraVal):** per-node bootstrap FastAPI service verifies each GPU via matrix-multiplication proofs seeded by device info, asserts ≥95% advertised VRAM usable, derives per-GPU AES decryption keys.
  - **GPU bootstrap/lifecycle** (`api/server/verification.py`, ~860 LOC): K8s Jobs/Services run GraVal (and TEE) bootstrap; verified GPUs persisted to Postgres.
  - **Multi-cluster federation:** each GPU node is its own K3s cluster; control plane aggregates via Karmada search-cache APIs; metrics federated into central Prometheus.
  - **Validator websocket + Redis pubsub bus:** `socket.io` client (Bittensor-signed) relays `miner_broadcast` events into internal Redis pubsub handlers.
  - **Authenticated registry proxy** (nginx + miner-API subrequest injecting hotkey-signature auth); **TEE/confidential-VM management** (signed CLI to a per-VM system-manager on port 8080); HuggingFace model-cache eviction; bounty claiming.
- **Distributed-computing relevance:** CORE — production reference for cost-aware GPU placement + value-multiplier preemption + optimistic reconciliation; GPU-bound E2EE; first-class TEE node class; Karmada multi-cluster federation.
- **Portability verdict:** REFERENCE
- **Target Helix module:** `pkg/scheduler` (Omega) + `pkg/resources` (GPU) + NEW `miner`/`marketplace` submodule (Phase 8/8B) + security/E2EE
- **Effort:** L
- **Rationale:** Python + CUDA + Bittensor-substrate + K8s/Karmada — architecturally rich, exactly on-target, but mechanically un-portable to Go. Durable value is the *design*: scheduler/preemption algorithm, GPU verification+inventory model, GPU-bound E2EE, TEE node class, Karmada federation pattern — re-implement natively in Go. WRAP conceivable only for the GraVal step (Python/CUDA sidecar over gRPC/HTTP).
- **Risks:** Language mismatch; heavy CUDA native dep (`graval==0.2.6`); Bittensor coupling (weights live in chutes-api, not here); Karmada/K3s substrate assumption; no top-level LICENSE file (MIT in pyproject only); incomplete spec (key contracts live in sibling repos).

## graval

- **Repo:** https://github.com/chutesai/graval
- **Purpose:** GraVal ("Graphics card Validation") — a cryptographic GPU attestation and proof-of-GPU-work library that proves a remote miner ACTUALLY possesses the specific GPU it claims and binds encrypted inference payloads to that exact device so only the genuine GPU can decrypt them. The trust anchor that lets validators verify untrusted miners' compute **without** a TEE.
- **Language:** Python (thin ctypes wrapper over prebuilt C/OpenCL `.so` libraries) · **License:** MIT (declared in `setup.py`; NO standalone LICENSE file in repo snapshot) · **Maturity:** active (v0.2.6, Alpha; deployed in production SN64 stack)
- **Capabilities:**
  - **Device-info attestation challenge/response:** validator issues a challenge keyed to a claimed device roster (name, UUID, memory, SM/processor count, clock, max threads/proc); miner must compute a GPU response only that exact hardware can produce.
  - **Proof-of-GPU-work (PoVW):** `generate_challenge_matrices(seed, iterations)` produces matrix-multiply work products with SHA256 intermediate hashes; validator spot-checks any single iteration index cheaply via `validator_check_proof` — succinct probabilistic verification (O(1) per challenge, 200+ challenges per test).
  - **Hardware-bound encryption (device-sealed E2EE):** `encrypt(device_info, plaintext, iterations, seed)` → ciphertext + IV bound to a GPU's properties; only the matching GPU can `miner_decrypt(...)`.
  - **Filesystem challenge:** SHA256 of an arbitrary (offset,length) byte range of a miner file to prove model/weight residency.
  - Multi-GPU node enumeration (`initialize_node()`); ships as a standalone FastAPI microservice with Bittensor SS58 signature auth (30s nonce window), internal-IP filtering, async GPU lock; Helm runs 8 GPU-pinned replicas.
- **Distributed-computing relevance:** CORE — precisely the GPU-attestation primitive HelixCluster lacks. *Software* attestation (OpenCL/CUDA matmul work + device fingerprinting) for an untrusted-miner adversary model, using probabilistic spot-checks rather than full re-execution. Distinct from (and complementary to) hardware TEE attestation (sek8s).
- **Portability verdict:** REFERENCE
- **Target Helix module:** NEW submodule `pkg/gpuattest` feeding `pkg/scheduler` (Omega) + `pkg/resources`; secondary tie-in to security (E2EE payload sealing) and the Phase 8/8B miner/marketplace plane
- **Effort:** L
- **Rationale:** Scientific value extremely high and on-target, BUT the entire crypto/GPU algorithm is a **closed prebuilt `.so` blob** (`libgraval-miner.so`/`libgraval-validator.so`) — no portable source, the Python is a trivial FFI shim. WRAP (cgo to the `.so`) couples Helix to opaque NVIDIA/OpenCL binaries of unknown provenance + a Bittensor deployment model and, per CLAUDE-1, cannot be end-to-end validated. Correct use: study GraVal's *design* (device-fingerprint challenge/response + probabilistic PoVW spot-checks + device-bound encryption) and implement an equivalent transparent native primitive in `pkg/gpuattest` with Go + CUDA/OpenCL kernels under Helix's own consensus/reputation.
- **Risks:** License ambiguity (MIT declared, no LICENSE file in snapshot; `.so` provenance separately unverified); opaque unauditable binary core in a trust-critical path; Python+ctypes, zero Go; hard NVIDIA/OpenCL+GPU dependency (untestable in CI without GPUs); Bittensor coupling (SS58/on-chain weights) must be stripped; fork drift vs `rayonlabs/graval` (ABI version-locked); software (not hardware) attestation — weaker than TEE, must combine with sek8s for strong confidentiality.

## chutes

- **Repo:** https://github.com/chutesai/chutes
- **Purpose:** Client SDK, CLI, and **in-pod runtime** for the chutes.ai platform. Packages an inference app ("chute" = FastAPI app; "cord" = route; "job" = non-API rental) onto a CUDA Docker image, declares GPU requirements via `NodeSelector`, deploys to miner-owned GPUs, and embeds the runtime middleware that performs GPU validation/attestation and E2EE request/response transport between validators and the workload on untrusted miner hardware.
- **Language:** Python · **License:** MIT · **Maturity:** production
- **Capabilities:**
  - **App/route model:** `Chute(FastAPI)` + `@chute.cord()` auto-generating OpenAPI schemas; streaming, passthrough proxying (local vLLM/SGLang/ComfyUI), disconnect-aware upstream abort (`rid` + `abort_request`).
  - **Declarative GPU placement constraints:** `NodeSelector(gpu_count 1-8, min_vram_gb_per_gpu 16-140, max_hourly_price_per_gpu, include/exclude GPU model lists)`.
  - Autoscaling/billing policy on the workload: `concurrency`, `max_instances`, `scaling_threshold`, `shutdown_after_seconds` (scale-to-zero), per-GPU hourly billing.
  - **GPU validation/attestation (GraVal):** seeded matmul + VRAM checks + device-info challenges; symmetric key released by GPU-bound decryption.
  - **TEE/confidential compute:** Intel TDX evidence server (`/verify`, `/evidence`) binding a validator nonce + E2E pubkey to a TDX quote before releasing the symmetric key.
  - **Layered E2E transport "Aegis"** (shipped as `chutes-aegis.so`): x25519 ECDH → AES-256-GCM session encryption + post-quantum **ML-KEM (Kyber)** E2E channel with streaming mode (`e2e_stream_begin/chunk/end`, 1088-byte ML-KEM ct header). In-enclave TLS/mTLS cert gen.
  - **Filesystem attestation (`cfsv`, C lib):** Merkle-style salted/sparse challenge over the container FS; capacity proofs; versioned challenge binaries (cfsv_v2..v4).
  - Bittensor SS58 request signing; anti-noisy-neighbor runtime hardening (dedicated thread pool, per-request cancel handles, egress firewalling, module locking); `Image` build DSL.
- **Distributed-computing relevance:** CORE — best-in-class reference for trustless GPU compute: two interchangeable verifiers (`GravalGpuVerifier` | `TeeGpuVerifier`) culminating in attestation-gated symmetric-key release; decrypt-in-middleware E2EE so user code never sees ciphertext.
- **Portability verdict:** REFERENCE
- **Target Helix module:** NEW `pkg/gpuattest` (GraVal/TEE port) + LLMOrchestrator/`pkg/scheduler` (NodeSelector + serving) + security (E2EE transport); miner/marketplace (Phase 8/8B)
- **Effort:** L
- **Rationale:** Python (Helix is Go); the most valuable pieces (GraVal, Aegis E2E/ML-KEM, cfsv, inspecto/bcm) ship as **opaque precompiled blobs** with no source here — re-implement from observable protocols, don't port. Deeply Bittensor/CUDA-coupled. But the *architecture/protocols* (attestation handshake, NodeSelector schema, billing/scale-to-zero state machine, decrypt-in-middleware E2EE) are extremely high-value. WRAP ties Helix to Bittensor + closed blobs → not recommended.
- **Risks:** Language mismatch; opaque proprietary `.so` blobs (license effectively UNKNOWN on the blobs even though Python wrapper is MIT); heavy CUDA/Python coupling (untestable attestation in CI); Bittensor lock-in; split codebase (scheduler/consensus in sibling repos); `verify_mode=CERT_NONE` pattern safe ONLY with the TDX-quote-binds-pubkey invariant.

---

# TIER: HIGH

## sek8s

- **Repo:** https://github.com/chutesai/sek8s
- **Purpose:** Chutes' confidential-compute stack turning a bare-metal Intel TDX host with NVIDIA GPUs into a hardware-attested, tamper-evident, self-contained k3s node ("secure standalone k8s"), so a remote validator can cryptographically verify a miner's GPU workload runs inside a genuine TEE before trusting it.
- **Language:** Python (+ Bash, Ansible, OPA/Rego, C TDX quote helper) · **License:** MIT · **Maturity:** active (production-targeted, v0.3.0)
- **Capabilities:**
  - **Intel TDX quote generation** bound to the node TLS cert: `report_data = nonce(64 hex) + cert_hash(64 hex)` proving freshness (anti-replay) + key-possession (cert binding).
  - **GPU attestation evidence** via NVIDIA nvTrust / `nv-attestation-sdk` (NRAS) producing an ES384-signed JWT verifiable against NVIDIA certs; GPU inventory via NVML/pynvml.
  - **Measured boot / RTMR3 access-config measurement:** every access-control file (ssh, PAM, authorized_keys, passwd/shadow, sudoers, grub cmdline) SHA-384-hashed into RTMR3 in initramfs; offline tamper → VM powers off at boot.
  - **LUKS root-disk encryption with remote, attestation-gated key release** — key never on disk; released only after MRTD/RTMR measurements match policy.
  - **OPA/Rego admission controller** (mutating+validating webhook): no-root, no-privileged, resource limits, allowed registries, hostPath restrictions, seccomp, RBAC/CRD/namespace policies (11 rego modules + tests).
  - **cosign container-signature verification** before admission; **Bittensor SR25519 signed-request auth** (`hotkey:nonce:payload_sha256`, 30s window); **attestation reverse-proxy** (dual 8443/8444, response-body RSA signing); read-only system-status API; host/guest lifecycle tooling (VFIO, LUKS, QEMU launch).
- **Distributed-computing relevance:** HIGH — the canonical dual-root-of-trust attestation pipeline (Intel TDX *and* NVIDIA GPU) with nonce-fresh quotes, cert-binding, and remote verification. The TEE counterpart to GraVal's challenge-response.
- **Portability verdict:** REFERENCE
- **Target Helix module:** security (NEW `pkg/security/attestation` + `pkg/security/admission`); secondary `pkg/scheduler` (placement gating) + GPU(planned) resource layer
- **Effort:** L
- **Rationale:** Value is inseparable from Intel TDX + NVIDIA confidential-GPU hardware + Intel PCCS/PCK + NVIDIA NRAS — none callable from Go without the substrate. Logic is Python/Bash/Ansible/Rego glue around platform binaries; reusable parts are *protocols and measurement recipes* (report-data layout, RTMR3 hashing list, attested-key-release flow, SS58 nonce-windowed auth) to re-implement in Go via go-tdx-guest / Intel DCAP + NVIDIA attestation libs. MIT permits liberal reuse of designs/snippets.
- **Risks:** Heavy hardware deps (TDX CPUs, H100/H200/B200-class confidential GPUs, NVSwitch, PCCS, NRAS); external-service trust (Intel Tiber, NVIDIA NRAS); Bittensor auth coupling (strip to generic signed-nonce); server side of LUKS key-release handshake not fully in-repo; single-node scope (no HA/gossip).

## fiber

- **Repo:** https://github.com/chutesai/fiber
- **Purpose:** Lightweight Python framework (Rayon Labs, chutesai fork) for building Bittensor subnets — the secure miner↔validator communication layer + on-chain (Subtensor) interactions to read the metagraph and set consensus weights. The production networking/comms substrate the Chutes subnet runs on.
- **Language:** Python · **License:** MIT · **Maturity:** production
- **Capabilities:**
  - Encrypted HTTP transport between validators (clients) and miners (servers) over FastAPI/httpx, with plaintext and end-to-end-encrypted paths.
  - **Handshake + hybrid encryption ("MLTS"):** validator fetches miner RSA-2048 pubkey → encrypts a random 32-byte symmetric key with RSA-OAEP(SHA-256) → posts it; subsequent bodies encrypted with Fernet (AES-128-CBC + HMAC).
  - Request auth via substrate (sr25519) signatures over nonce + miner hotkey + key-uuid/payload hash, verified against the on-chain hotkey.
  - **Replay protection:** `NonceManager` single-use, time-windowed nonces (2-min TTL) + background cleanup; per-(validator,uuid) Fernet key lifecycle with expiry.
  - Streamed/non-streamed POST/GET helpers (streaming carries token-by-token LLM output); on-chain `Metagraph` sync, U16 weight normalization + `set_weights` (incl. commit-reveal); **DDoS/sybil hook** `blacklist_low_stake`.
- **Distributed-computing relevance:** HIGH — clean dependency-light reference implementation of authenticated hybrid-encrypted RPC (RSA exchange + Fernet payload + signed nonce headers + stake gating). NOT a gossip/DHT mesh: directed validator→miner HTTP; peer discovery via on-chain metagraph (contrast with Helix SWIM); consensus delegated to Bittensor Yuma.
- **Portability verdict:** REFERENCE
- **Target Helix module:** security (E2EE transport patterns) + NEW federation/miner submodule (Phase 6/8); LLMOrchestrator transport reference
- **Effort:** M
- **Rationale:** Small, MIT, conceptually valuable, but (1) tightly coupled to Bittensor/Subtensor for identity/discovery/consensus — the chain half is not reusable under Helix's SWIM+Raft+etcd; (2) pure Python with no GPU/compute logic to FFI-wrap. Highest value: PORT the *patterns* of the `encrypted` package (RSA→Fernet hybrid handshake, signed single-use nonces, stake-gated admission, encrypted streaming) into Helix Go security/transport; study weight-setting/commit-reveal for Phase 8 marketplace settlement.
- **Risks:** Language mismatch; Bittensor lock-in (identity/discovery/stake/consensus all assume a live Subtensor chain); crypto to harden if ported (RSA-2048 regenerated per start, Fernet AES-128 vs AES-256-GCM); fork drift vs `rayonlabs/fiber`; no GPU/attestation content here.

## chutes-e2ee-transport

- **Repo:** https://github.com/chutesai/chutes-e2ee-transport
- **Purpose:** Client-side, post-quantum E2EE transport that plugs in as a drop-in `httpx` transport for the OpenAI Python SDK, transparently encrypting inference requests/responses with ML-KEM-768 + HKDF-SHA256 + ChaCha20-Poly1305 so neither the Chutes relay nor any intermediary can read the payload — only the target TEE GPU instance sees plaintext.
- **Language:** Python · **License:** MIT · **Maturity:** active
- **Capabilities:**
  - Drop-in `httpx.BaseTransport`/`AsyncBaseTransport` intercepting at the HTTP layer, invisible to the OpenAI SDK above.
  - Hybrid PQ envelope: ML-KEM-768 (FIPS 203) KEM → HKDF-SHA256 (salt = first 16 bytes of KEM ciphertext, domain-separated `info`) → ChaCha20-Poly1305 AEAD; gzip before encryption.
  - Bidirectional with separate ephemeral keypairs (client ships a one-time *response* ML-KEM pubkey inside the encrypted request; `INFO_REQ`/`INFO_RESP`/`INFO_STREAM` domain separation).
  - Transparent SSE streaming: `e2e_init` derives a stream key, each `e2e` chunk decrypted on the fly and re-emitted as standard `data:` lines; passes through `usage`, surfaces `e2e_error`.
  - Instance discovery + single-use nonce mgmt (`/e2e/instances/{chute_id}`, per-instance pools, TTL/expiry, one nonce/request, refresh on exhaust); model→chute_id resolution via `/v1/models` (5-min TTL); thread-safe `DiscoveryManager`; rewrites to `POST /e2e/invoke` (octet-stream + routing headers).
- **Distributed-computing relevance:** HIGH — canonical reference for zero-trust inference routing through an untrusted relay to a specific GPU instance; the network-transport complement to TEE/sek8s attestation (encryption terminates *inside* the enclave). Single-use-nonce request-binding is a lightweight distributed anti-replay scheme.
- **Portability verdict:** **PORT**
- **Target Helix module:** security (NEW `pkg/security/e2ee` or `pkg/transport/e2ee`), feeding the LLMOrchestrator inference path
- **Effort:** M
- **Rationale:** Value is the protocol + crypto envelope — small, self-contained (~600 LOC, 3 modules), well-specified, trivially reimplementable in Go. Go has first-class equivalents: `crypto/mlkem` (Go 1.24, already targeted), `x/crypto/hkdf`, `x/crypto/chacha20poly1305`. A Go port plus matching server-side decapsulation in the Helix worker gives native confidential inference with no Python in the data path.
- **Risks:** Must match the wire format exactly (1088-byte ML-KEM-768 ct, 16-byte tag, 12-byte nonce, gzip framing, HKDF salt = `mlkem_ct[:16]`, `e2e-{req,resp,stream}-v1` info strings) or interop fails; server side is opaque (only the client half here); security-critical hand-port needs constant-time review + captured test vectors + real end-to-end decryption tests (CLAUDE-1); `pqcrypto` dep is early (0.1.x) — prefer FIPS-203 Go impl; protocol versioning drift.

## e2ee-proxy

- **Repo:** https://github.com/chutesai/e2ee-proxy
- **Purpose:** An OpenResty (nginx + LuaJIT) reverse proxy giving clients drop-in OpenAI/Anthropic/Responses compatibility while transparently E2E-encrypting each inference request so only the target TEE GPU instance can decrypt it — format translation, per-request ML-KEM-768 key exchange, ChaCha20-Poly1305 AEAD, streaming SSE decryption, and refusal to talk to any model not flagged `confidential_compute: true`.
- **Language:** Lua (OpenResty/LuaJIT) + C (native crypto) + a little Python (tests) · **License:** MIT · **Maturity:** active
- **Capabilities:**
  - Drop-in API compatibility (`/v1/chat/completions`, `/v1/completions`, `/v1/messages`, `/v1/responses` all translated to chat-completions before encryption; `/v1/models` passthrough).
  - Per-request E2EE envelope: resolve model→chute_id, fetch instance + nonce, ephemeral ML-KEM-768 keypair, encapsulate against instance pubkey, HKDF-SHA256, gzip, ChaCha20-Poly1305 seal, POST to `/e2e/invoke`. Blob layout `mlkem_ct(1088) | nonce(12) | ciphertext(N) | tag(16)`; info strings `e2e-{req,resp,stream}-v1`.
  - Forward secrecy (fresh ephemeral keypair per request); streaming decryption (`e2e_init` per-stream key); **TEE enforcement** (rejects non-`confidential_compute` models, `ALLOW_NON_CONFIDENTIAL` override).
  - Native crypto in a hardened symbol-stripped `libe2ee_proxy.so` via LuaJIT FFI (ML-KEM keygen/encap/decap, HKDF, ChaCha20-Poly1305, gzip, CSPRNG, DER cert retrieval); TLS cert/key embedded in the obfuscated `.so` to evade CT scanners; nonce/instance caching + 403-retry.
- **Distributed-computing relevance:** HIGH — the client-edge of Chutes' confidential inference path; the exact wire contract a GPU instance must implement. TEE-gating policy ties E2EE to attested hardware. Consumer-side placement negotiation with a decentralized GPU pool.
- **Portability verdict:** REFERENCE
- **Target Helix module:** LLMOrchestrator (E2EE inference transport) + security (PQ AEAD envelope, TEE gating); NEW submodule `pkg/llm/e2ee`
- **Effort:** M
- **Rationale:** LuaJIT FFI inside OpenResty + a hardened/obfuscated native blob is a poor fit for a Go cluster OS (Helix would not embed OpenResty or an xVMP-packed `.so`). But the *protocol* is extremely valuable and cleanly specified — re-implement the envelope natively in Go (Go 1.24 `crypto/mlkem` + `x/crypto/chacha20poly1305`/`hkdf`). WRAP (running the container as a sidecar) is a fallback for fast Chutes interop but inherits the obfuscation/cert-embedding burden. Per CLAUDE-1, any adoption must be validated against a real confidential instance, not mocked.
- **Risks:** Language mismatch; hardened native blob not reproducible from repo (proprietary xVMP plugin/packer + embedded TLS key → non-redistributable); hardcoded `api.chutes.ai`/`llm.chutes.ai` + Chutes-specific contracts; TEE trust delegated (trusts upstream `confidential_compute` flag, not verified here); exact crypto sizes must match or interop silently fails. License clean (MIT).

## chutes-docs

- **Repo:** https://github.com/chutesai/chutes-docs
- **Purpose:** Official Markdown documentation for the Chutes platform (decentralized serverless GPU compute on Bittensor SN64). No executable code; canonical architecture reference for validator/miner mechanics, GraVal attestation, TEE confidential compute, the gepetto scheduler, the 7-day scoring algorithm, E2E encryption, and the validator REST API.
- **Language:** Markdown · **License:** UNKNOWN (no LICENSE; license API 404) — **blocking for verbatim copy** · **Maturity:** active
- **Capabilities:** GraVal GPU attestation via consecutive matmuls proving ≥95% VRAM + GPU-misrepresentation detection; per-GPU AES-256 key from GPU UUID + challenge; miner verification suite (cfsv, inspecto, envdump, cllmv) + watchtower; sek8s on Intel TDX with NVIDIA PPCIE, TD Quote attestation, LUKS attestation-gated root FS, cosign admission; gepetto miner scheduler; scoring weights (compute 55/invocations 25/unique-chute 15/bounty 5) with 7-day anti-gaming window; NodeSelector resource model; WireGuard multi-provider K3s mesh.
- **Distributed-computing relevance:** HIGH — most complete public description of Chutes mechanics; the authoritative architecture map cross-referencing the now-analyzed CORE repos.
- **Portability verdict:** REFERENCE
- **Target Helix module:** REFERENCE for GPU(planned), security/E2EE, miner/marketplace (Phase 8/8B), federation (Phase 6); no code lands
- **Effort:** S
- **Rationale:** Pure Markdown (84 .md, zero code, no LICENSE, auto-syncs to chutes-web). Nothing to wrap/port; value is entirely as the authoritative design reference. Actual algorithms must be implemented against the real code repos (now available: chutes-api/chutes-miner/graval/sek8s/chutes).
- **Risks:** No LICENSE (all-rights-reserved default); closed components are black boxes; docs may drift from implementation; deeply coupled to Bittensor + K3s; per CLAUDE-1, inspired features still need real integration/E2E validation.

## chutesai/sglang

- **Repo:** https://github.com/chutesai/sglang
- **Purpose:** Fork (chutes branch) of sgl-project/sglang, a high-throughput LLM/VLM serving engine. Chutes adds a decentralized-GPU trust layer: per-response `chutes_verification` attestation injected into every OpenAI chunk via external `cllmv`; mandatory HF model-cache integrity verification at startup (`os._exit(99)` on tampering); SHA256 prompt/chat-template provenance hashes.
- **Language:** Python (with CUDA/C++/Triton kernels) · **License:** Apache-2.0 · **Maturity:** fork-tracking
- **Capabilities:** RadixAttention prefix caching, continuous batching, chunked prefill, CUDA graphs; tensor/data/pipeline/expert parallelism; OpenAI-compatible API; constrained/structured + speculative decoding; quantization; **CHUTES delta:** per-response inference attestation via `cllmv`; mandatory HF model-cache integrity verification (SHA256/git-blob-SHA1 + size vs HF Hub, hard `os._exit(99)` on mismatch); prompt/template provenance (`template_sha256`/`prompt_sha256`).
- **Distributed-computing relevance:** HIGH (verification delta is the value; no scheduler/p2p/consensus/TEE here).
- **Portability verdict:** REFERENCE (engine WRAP-able; verification delta portable)
- **Target Helix module:** LLMOrchestrator (run SGLang as a serving backend) + NEW `pkg/attestation` modeled on the verification pattern (port hf_cache_verify.py logic to Go for the GPU/marketplace admission gate)
- **Effort:** M
- **Rationale:** Engine is Python+CUDA → WRAP via OpenAI API. Portable IP is the CHUTES delta: hf_cache_verify.py is pure-Python (resolve HF snapshot, compare symlink-target SHA256/git-blob-SHA1 + size vs authoritative metadata, hard-fail) — trivially reimplementable in Go as a model-integrity admission gate, filling a marketplace trust gap. `chutes_verification`/`cllmv` crypto is in a closed external module → study as PATTERN.
- **Risks:** Python + CUDA/Triton (WRAP-only); `cllmv` closed; fork drift; reaches proxy.chutes.ai + uses `os._exit(99)` (Go port must use Helix's own metadata source + graceful failure); Apache-2.0 OK for verification logic, NVIDIA/`cllmv` separate.

## chutesai/vllm

- **Repo:** https://github.com/chutesai/vllm
- **Purpose:** Hard fork of vllm-project/vllm, the high-throughput LLM inference engine. Chutes maintains it purely as a pinned base dependency; **ZERO source changes** (ahead_by:0, behind_by:3484).
- **Language:** Python (CUDA/C++/C kernels) · **License:** Apache-2.0 · **Maturity:** fork-tracking
- **Capabilities:** PagedAttention KV-cache; continuous batching; tensor parallelism + multi-node pipeline parallelism; Ray + native MP executor; OpenAI-compatible server; prefix caching; speculative decoding; KV-cache transfer / disaggregated prefill-decode connectors; broad backends; NCCL/custom all-reduce.
- **Distributed-computing relevance:** HIGH (upstream engine; this fork adds nothing).
- **Portability verdict:** WRAP
- **Target Helix module:** LLMOrchestrator (deploy vanilla upstream vLLM in a container via OpenAI HTTP API); NOT a code-port target
- **Effort:** M
- **Rationale:** No Chutes IP. Track vllm-project/vllm directly (fork is stale + zero custom commits) and WRAP as the inference engine. Study its distributed executor, tensor+pipeline placement, and disaggregated P/D KV transfer as reference patterns.
- **Risks:** ~3484 commits behind + zero custom commits (pin upstream); Python+CUDA (WRAP only); heavy runtime deps; Apache-2.0 OK; the miner/validator/attestation IP is NOT here.

---

# TIER: MEDIUM

## chutes-audit

- **Repo:** https://github.com/chutesai/chutes-audit
- **Purpose:** Standalone single-file "lite validator" that independently reproduces and verifies Chutes (SN64) reward distribution — downloads validator/miner audit reports, verifies integrity against on-chain `set_commitment` SHA-256 checksums, recomputes per-miner incentives from compute-unit lifetime data in Postgres, and optionally sets subnet weights on-chain.
- **Language:** Python · **License:** MIT · **Maturity:** active
- **Capabilities:** independent audit-report verification (SHA-256 vs on-chain commitment; cross-check currently commented out); reproducible incentive calc (`SUM(overlap_seconds * compute_multiplier)`, normalize, compare to live metagraph, deltas <0.2%); on-chain `set_weights` (U16, version_key 69420); metagraph sync; miner self-report cross-audit (validator vs miner Prometheus, agreement ratios, discrepancy flagging); CSV-based data reconciliation; pm2 git-pull autoupdater.
- **Distributed-computing relevance:** MEDIUM — full lite-validator weight-setting path + chain-anchored commit-then-prove audit-log pattern; no scheduler/GPU/E2EE/p2p/transport.
- **Portability verdict:** REFERENCE
- **Target Helix module:** miner/marketplace (Phase 8/8B) — audit & weight/scoring submodule (`pkg/marketplace/audit`)
- **Effort:** M
- **Rationale:** Tightly coupled to Bittensor/Substrate consensus, a specific Chutes API source of truth, and a Postgres-SQL scoring model. No reusable distributed primitive. Enduring value is conceptual: commit-then-verify audit-log + independent reproducible reward-reconciliation, re-implementable natively in Go.
- **Risks:** Language mismatch; hard Bittensor coupling; external-API dependence; autoupdater `git reset --hard` foot-gun; commitment cross-check currently disabled; heavy single-Postgres scoring (NVMe, 64GB+ RAM, 8h+ sync).

## cllmv

- **Repo:** https://github.com/chutesai/cllmv
- **Purpose:** "Chutes LLM Verification" — a thin Python ctypes shim over the proprietary `/usr/local/lib/chutes-aegis.so`, exposing inference-verification primitives to prove a miner actually ran the claimed LLM model/revision rather than faking responses (token generate/validate, X25519 session handshake, build-time engine hashing).
- **Language:** Python · **License:** MIT · **Maturity:** experimental
- **Capabilities:** `generate(id, created, value)→32-hex token` (miner); `validate(...)` V1 (MD5 interleaving over id/created/value/salt/model/revision); `validate_v2(...)` V2 (HMAC-SHA256 with per-session key); session handshake (`get_session_init()→312-hex`, `decrypt_session_key(blob, x25519_priv)→64-hex`); `pkg_hash` build-time attestation of installed sglang/vllm; graceful stub mode if `.so` absent.
- **Distributed-computing relevance:** MEDIUM — verifier half of a miner↔validator anti-cheat protocol ("did the GPU node actually run the declared model"); X25519 ephemeral→session-HMAC bootstrap; runtime-stack attestation via pkg_hash.
- **Portability verdict:** REFERENCE
- **Target Helix module:** LLMOrchestrator (inference verification submodule) / security (attestation handshake)
- **Effort:** M
- **Rationale:** Only open code is ~388 lines of ctypes glue + path discovery; 100% of security value is in the closed unavailable `chutes-aegis.so`. Reusable: the *protocol design* (V1/V2 token contract, 312-hex init blob + X25519 session-key handshake, build-time engine-hash attestation). Re-implement an equivalent open scheme in Go.
- **Risks:** Proprietary core unavailable (repo inert without `.so`); language mismatch; **V1 MD5 cryptographically broken** (port only V2 HMAC-SHA256); opaque contract (blob sizes asserted by comments only, not wire-compatible without the binary); single squashed commit, empty README.

## genlayer-studio

- **Repo:** https://github.com/chutesai/genlayer-studio
- **Purpose:** Local dockerized sandbox replicating the GenLayer L1 protocol's execution + consensus (leader + N stake-weighted validators reaching consensus over non-deterministic LLM outputs) so devs can test "Intelligent Contracts." A single-host SIMULATOR of a decentralized network, not the production network.
- **Language:** Python · **License:** MIT · **Maturity:** fork-tracking (0 ahead / 99 behind upstream; no Chutes additions)
- **Capabilities:** stake-weighted (VRF-style) validator-set selection (`numpy` weighted sampling); leader+validators consensus state machine with appeal/rotation (AGREE/DISAGREE/TIMEOUT/NO_MAJORITY, ~4000 LOC); distributed work-claiming via Postgres `SELECT ... FOR UPDATE SKIP LOCKED`; Redis worker + FastAPI consensus-worker service; pluggable LLM validators with provider/model fallback; WASM GenVM; EVM rollup bridge.
- **Distributed-computing relevance:** MEDIUM — real stake-weighted sampling + multi-round leader/appeal BFT, but executed in a single-process simulation (no real p2p/gossip/networked Raft); `SKIP LOCKED` work-stealing + fallback-provider failover are the reusable FT patterns.
- **Portability verdict:** REFERENCE
- **Target Helix module:** `pkg/scheduler` (Omega) + LLMOrchestrator (consensus-pattern reference); NOT a direct port target
- **Effort:** M
- **Rationale:** Passive mirror of `genlayerlabs/genlayer-studio` (no Chutes value-add); single-host simulator; Python+WASM+EVM mismatched with Go. Valuable assets are PATTERNS — stake-weighted selection, leader/appeal consensus state machine, `SKIP LOCKED` optimistic work-claiming — re-implement idiomatically in Go.
- **Risks:** Fork drift (99 behind; read upstream instead); language mismatch; domain mismatch (blockchain intelligent-contracts); simulation only ("validators" are DB rows). MIT permissive.

## research-data-opt-in-proxy

- **Repo:** https://github.com/chutesai/research-data-opt-in-proxy
- **Purpose:** Standalone FastAPI reverse proxy that transparently forwards OpenAI-compatible LLM traffic to `llm.chutes.ai` while opt-in recording per-request research traces for a Harvard prefix-caching study. An observability/data-capture sidecar in front of the inference plane — NOT a scheduler/miner/validator.
- **Language:** Python · **License:** UNKNOWN (no LICENSE; API 404; no SPDX) — **all-rights-reserved** · **Maturity:** active (Vercel-deployed)
- **Capabilities:** transparent OpenAI passthrough (SSE-preserving); RFC 7230 hop-by-hop header stripping; spoof-proof managed-header injection (`X-Chutes-Research-OptIn`, `X-Chutes-Trace`, `X-Chutes-Correlation-Id`, `X-Chutes-RealIP`); trace-envelope unwrapping (`TraceSSEUnwrapper` + parallel `StreamingTraceMetadataBuilder`); dual recording (raw HTTP + anonymized Qwen-style usage trace); deterministic prompt anonymization (tiktoken cl100k_base → 16-token blocks → Blake2b-keyed SipHash-2-4 → sequential IDs); S3/Vercel Blob archival w/ SHA-256; secret-gated internal export/archive endpoints (constant-time compare); token-bucket rate limiter.
- **Distributed-computing relevance:** MEDIUM — passive observer of subnet inference placement: `chutes_trace.py` regex-extracts `target=<instance> uid=<int> hotkey/coldkey` per routing attempt, reconstructs `attempts`, selects winning target. A clean reference for miner-selection telemetry over the wire. Does NOT route/schedule.
- **Portability verdict:** REFERENCE
- **Target Helix module:** LLMOrchestrator (inference routing/recording adjunct); NEW `pkg/inferenceproxy` observability submodule + security (header sanitization, deterministic anonymization)
- **Effort:** M
- **Rationale:** License UNKNOWN/proprietary (no copy); Python/FastAPI/Vercel/tiktoken/Postgres mismatched with Go; an observability adjunct, not a DC primitive. High-value ideas (trace-envelope unwrap to recover miner UID/hotkey/coldkey placement; deterministic salted-SipHash prefix-block anonymization; spoof-proof managed-header injection; cron-drainable archival with constant-time-authed endpoints) → re-implement natively in Go.
- **Risks:** License blocking; tiktoken/cl100k_base has no first-class Go equivalent (block-hash semantics may diverge); brittle Chutes-specific trace-envelope regexes; GDPR/opt-in + salt-management obligations; Vercel/Hetzner-specific ops; tests validate recording fidelity, not compute placement.

## squad-api

- **Repo:** https://github.com/chutesai/squad-api
- **Purpose:** FastAPI control plane ("Agents, on chutes.ai", by Rayon Labs) for defining and running sandboxed AI agents on the Chutes decentralized-GPU platform. Each invocation is packaged as code + inputs and executed as an isolated Kubernetes Job; the agent is a `smolagents` `CodeAgent` whose LLM/VLM/image/TTS/embedding + web/X calls hit remote Chutes endpoints. A *consumer* of decentralized GPU compute.
- **Language:** Python · **License:** MIT · **Maturity:** active
- **Capabilities:** agent definition/config store (Postgres); invocation engine (SQLAlchemy `after_insert` → S3 tarball → namespaced `batch/v1` Job); **Kueue-gated job admission** (`suspend=True` + `kueue.x-k8s.io/queue-name`, free vs paid queues map to different CPU/mem requests); sandboxed worker under an egress proxy with strict `NO_PROXY` allowlist (`*.chutes.ai`); built-in tools (llm/vlm/image/tts/transcribe/apex_search/web/memory/x/agent_caller); AES-encrypted BYOK secret store + per-invocation RS256 JWTs.
- **Distributed-computing relevance:** MEDIUM — consumer not provider; scheduling delegated to K8s + **Kueue** (gang/quota queueing); the reusable idea is queue-name-routed, suspend-then-admit batch jobs with per-tier resource shapes; egress-proxy + scoped-JWT sandboxing.
- **Portability verdict:** REFERENCE
- **Target Helix module:** LLMOrchestrator (agent invocation/sandboxing) + `pkg/scheduler` (Kueue-style job admission pattern)
- **Effort:** M
- **Rationale:** Valuable DC content is two *patterns*: (1) Kueue-gated, queue-routed, suspend-then-admit batch execution with tiered resource shapes; (2) sandboxed agent-invocation lifecycle with proxy-confined egress + scoped per-job JWTs. Study and re-implement natively in Helix scheduler/Omega + LLMOrchestrator. Bulk is FastAPI CRUD + Chutes-specific tool/X plumbing.
- **Risks:** Language mismatch; K8s+Kueue coupling (only the pattern transfers); tight Chutes endpoint/auth coupling; heavy deps + separate worker image; agent runs arbitrary generated Python (isolation relies on pod + egress proxy, not a hard sandbox); low maturity (`version 0.0.1`, no README).

## chutes-autopilot

- **Repo:** https://github.com/chutesai/chutes-autopilot
- **Purpose:** Single-binary OpenAI-compatible HTTP router/reverse-proxy in front of `llm.chutes.ai`. Picks one chute per request via live-utilization ranking, an explicit preference list, or direct passthrough; rewrites the `model` field; streams responses unbuffered with retry-based failover. Selection + proxy only — no product logic.
- **Language:** Rust · **License:** UNKNOWN (no LICENSE/`license` field; API `license: null`) — **all-rights-reserved** · **Maturity:** active
- **Capabilities:** OpenAI-compatible `/v1/chat/completions` reverse proxy with true streaming passthrough; three routing modes (`chutesai/AutoPilot` ranked alias, comma-separated ordered failover, direct passthrough); ~5s utilization refresh + ~5min model-catalog allowlist; **deterministic ranking** (score → instance count → utilization → rate-limit → name; `free_capacity = active_instance_count*(1-util)` + `scale_bonus` − rate-limit penalty, EWMA over 5m/15m/1h); sticky per-client selection (TTL, bounded); **streaming-safe failover** (retry only before first byte; pass 429 through); readiness gating; Prometheus metrics; offline smoke harness.
- **Distributed-computing relevance:** MEDIUM — the ranking engine is a lightweight deterministic utilization-aware placement heuristic with hysteresis + tie-break determinism (conceptually adjacent to Omega scoring, far simpler — single resource dimension, no bidding). The "retry only before first byte" rule is a reusable streaming-proxy FT pattern. GraVal/TEE here is stubbed/heuristic (`-TEE` suffix only; live evidence probes return HTTP 400 / require chutes_version ≥ 0.6.0).
- **Portability verdict:** REFERENCE
- **Target Helix module:** LLMOrchestrator (inference routing/failover); secondary `pkg/scheduler` ranking heuristics
- **Effort:** S
- **Rationale:** License UNKNOWN/proprietary (no copy); Rust vs Go; value is the routing/failover/ranking *design* — small, self-contained, easily reimplemented in Go (Effort S). README concedes the feature is superseded by native Chutes work (PR #103). Headline DC areas (GraVal/p2p/consensus/TEE/E2EE) absent or stubbed.
- **Risks:** License blocking for code reuse; Rust→Go reimplementation; superseded upstream; hardcoded Chutes endpoints; per-replica sticky state (needs shared store for multi-replica); **`-TEE` suffix as attestation signal is a CLAUDE-1 PASS-bluff risk** — proves nothing about confidential compute.

## chutesai/e2ee-test

- **Repo:** https://github.com/chutesai/e2ee-test
- **Purpose:** Browser-native reference client + test harness for the Chutes E2EE confidential-compute inference protocol. A TypeScript/Vite UI drives the request lifecycle; all protocol crypto lives in a Rust/WASM module. Discovers TEE instances, leases one-time nonces, PQ-encrypts a chat-completions request to a specific instance's public key, incrementally decrypts the streamed TEE response.
- **Language:** TypeScript (UI, ~500 LOC) + Rust→WASM (crypto, ~340 LOC) · **License:** UNKNOWN (no LICENSE; no license field) — **blocking for code copy** · **Maturity:** experimental
- **Capabilities:** PQ hybrid encryption (ML-KEM-768 KEM to instance e2e_pubkey, 1184B PK/1088B CT/2400B expanded SK); ChaCha20-Poly1305 AEAD over gzip JSON; HKDF-SHA256 (salt = first 16B of KEM ct, labels `e2e-{req,resp,stream}-v1`); request framing `[mlkem_ct(1088)||nonce(12)||ct+tag]` with embedded response pubkey; streaming decryption from `e2e_init`; TEE instance discovery + lease/nonce (`GET /e2e/instances/{chute_id}`, `POST /e2e/invoke`); one-time nonce lifecycle; per-key/model cache scoping + zeroization; `-TEE` model filtering; hardened browser deployment recipe.
- **Distributed-computing relevance:** MEDIUM — self-contained crypto-portable spec of a PQ E2EE inference envelope + documented HTTP/SSE discovery+nonce+invoke protocol. Every primitive has a mature pure-Go equivalent. (Same protocol family as chutes-e2ee-transport/e2ee-proxy.)
- **Portability verdict:** **PORT** (clean-room reimplementation; do not copy source under UNKNOWN license)
- **Target Helix module:** security/e2ee (NEW `pkg/e2ee`) consumed by LLMOrchestrator; protocol notes feed GPU(planned) confidential-serving + federation (Phase 6)
- **Effort:** M
- **Rationale:** High-value content is small, self-contained, crypto-portable. Go has native equivalents (`crypto/mlkem` in 1.24, `x/crypto` chacha20poly1305 + hkdf, compress/gzip). Rust/WASM module is browser-specific → REFERENCE only. lib.rs test vectors can validate a Go port byte-for-byte (interop tests against real Chutes endpoints satisfy CLAUDE-1).
- **Risks:** LICENSE UNKNOWN/absent (reimplement from public wire description); **no remote attestation in client** (trusts discovery; a zero-trust Helix port MUST ADD TEE attestation binding the pubkey to a genuine quote); protocol unversioned beyond v1; crypto pinned to RC deps; precompiled .wasm must NOT be vendored.

---

# TIER: LOW

## claude-proxy

- **Repo:** https://github.com/chutesai/claude-proxy
- **Purpose:** Stateless HTTP proxy exposing the Anthropic Claude Messages API and translating to/from an OpenAI-compatible chat-completions backend, so Claude clients can drive OpenAI-shaped inference (Chutes' `llm.chutes.ai`).
- **Language:** Rust · **License:** UNKNOWN — **blocking for code copy** · **Maturity:** active
- **Capabilities:** bidirectional Claude↔OpenAI schema translation; Claude-style SSE; tiktoken-rs token counting; model discovery + case correction; bearer pass-through (forwards `cpk_*`, rejects `sk-ant-*`); optional in-process **circuit breaker**; Cloudflare Workers WASM variant.
- **Distributed-computing relevance:** LOW — single-process stateless L7 gateway; only DC-adjacent features are the local circuit breaker and horizontal statelessness.
- **Portability verdict:** REFERENCE
- **Target Helix module:** LLMOrchestrator (inference routing / API translation shim)
- **Effort:** S
- **Rationale:** Pure Rust (~7k LOC), solves an API-shape adaptation problem tangential to Helix's DC mission. Extractable: translation/SSE/token-count logic + circuit-breaker pattern → re-implement in Go if a Claude-compatible endpoint is needed.
- **Risks:** License UNKNOWN (blocks copy/port); Rust vs Go; zero overlap with GPU/miner/validator/consensus/TEE/E2EE; pre-1.0 churn; incomplete feature surface (no prompt caching/citations/server tools).

## codex

- **Repo:** https://github.com/chutesai/codex
- **Purpose:** Thin fork of `openai/codex` (local terminal coding agent). ONLY Chutes value-add is a small Rust compat patch letting the Codex CLI drive non-OpenAI models through the Chutes Responses proxy (`responses.chutes.ai/v1`).
- **Language:** Rust (+ TS/Node CLI wrapper, Bazel) · **License:** Apache-2.0 · **Maturity:** fork-tracking
- **Capabilities:** local agentic coding loop (TUI, exec, apply-patch, MCP, sandboxing); OpenAI Responses API client (WS + HTTP, SSE); **Chutes proxy compat shim** (detects `responses.chutes.ai`, downgrades `developer` role, folds `system` into instructions, filters to `function` tools, injects `<function=TOOL_NAME>` text protocol + re-parses tool calls from plain text); rebase automation.
- **Distributed-computing relevance:** LOW — single DC touchpoint is client-side inference transport; the transferable idea is the text-protocol tool-call bridge for heterogeneous backends.
- **Portability verdict:** REFERENCE
- **Target Helix module:** LLMOrchestrator (inference-provider/proxy adapter pattern)
- **Effort:** S
- **Rationale:** Entire interactive Rust/Node coding-agent app, DC surface effectively nil; only IP is a ~50KB single-file patch. Sole transferable artifact: provider-aware request rewriting + text-based tool-call fallback grammar → reimplement in Go inside LLMOrchestrator. Apache-2.0 clean.
- **Risks:** Rust+Node vs Go; fork drift (targets specific upstream SHAs); brittle `<function=...>` interop (needs hardening per CLAUDE-1); inert without live `responses.chutes.ai`; heavy Bazel+Cargo+pnpm toolchain.

## cpp-oasvalidator

- **Repo:** https://github.com/chutesai/cpp-oasvalidator
- **Purpose:** C++11 thread-safe library validating inbound HTTP requests against an OpenAPI 3.x spec (method, route, JSON body, path/query/header params) so only spec-compliant requests reach backends.
- **Language:** C++ (C++11) · **License:** MIT · **Maturity:** fork-tracking (identical mirror of upstream v1.1.1)
- **Capabilities:** short-circuit validation pipeline (method→route→body→path→query→header); RapidJSON JSON-Schema body validation; `path_trie` radix router with templated segments; full param style/explode deserialization matrix; lazy deserialization; thread-safe immutable parsed structures; rich `ValidationError` + JSON-Pointer error model; method aliasing.
- **Distributed-computing relevance:** LOW (none present) — single-host request-validation library; only tangential relevance is as an edge admission-control / API-gateway component.
- **Portability verdict:** REFERENCE
- **Target Helix module:** API gateway / request-validation middleware (NEW thin L6/L7 edge layer)
- **Effort:** M (Go reimpl) / S (CGO wrap, not recommended)
- **Rationale:** Exact unmodified upstream mirror (Chutes added nothing); zero overlap with Helix DC core; Go already has mature native OAS validators (kin-openapi, ogen). Justified use: conceptual reference for short-circuit ordering, path-trie router, JSON-Pointer error model if Helix builds a Go OpenAPI admission layer.
- **Risks:** Fork drift / no provenance value; C++11 vs Go (CGO burden unwarranted); MIT (attribution); request-only/single-host scope.

## responses-proxy

- **Repo:** https://github.com/chutesai/responses-proxy
- **Purpose:** Small stateless Rust (axum+tokio) service translating the OpenAI Responses API (`POST /v1/responses`, SSE) into backend Chat Completions, so Codex-style `wire_api="responses"` clients can drive Chutes backends unchanged.
- **Language:** Rust · **License:** UNKNOWN — **blocking for copy** · **Maturity:** active
- **Capabilities:** bidirectional Responses↔Chat-Completions translation; fragmentation-safe tool calling (buffers arg deltas before name/header); dual event emission (modern + legacy); XML-style tool-call salvage; reasoning-model support (`<think>`); MCP tool-result ingestion; circuit breaker (5 fail → 30s); 60s model cache; stateless auth pass-through.
- **Distributed-computing relevance:** LOW — stateless protocol shim; only "routing" is L7 forwarding to one `BACKEND_URL`; FT limited to local circuit breaker.
- **Portability verdict:** REFERENCE
- **Target Helix module:** LLMOrchestrator (API-surface adapter / inference-routing front door)
- **Effort:** S
- **Rationale:** Valuable but narrow + OpenAI-protocol-specific, and Rust vs Go. If Helix needs Responses compatibility, reimplement the converter/streaming state machine in Go using this repo's event-ordering/tool-call-buffering invariants as spec.
- **Risks:** License UNKNOWN (hard blocker for copy); Rust vs Go; protocol coupling/drift (OpenAI Responses + Codex client); zero GPU/consensus/p2p/TEE coverage.

## DeepGEMM

- **Repo:** https://github.com/chutesai/DeepGEMM
- **Purpose:** Single-node JIT-compiled CUDA tensor-core kernel library providing modern LLM compute primitives (FP8/FP4/BF16 GEMMs with fine-grained scaling, fused MoE "Mega MoE", MQA scoring kernels, HyperConnection). The GPU math backend inside an inference engine — not a service/scheduler/network layer.
- **Language:** CUDA (C++/CUDA + Python/PyTorch bindings) · **License:** MIT · **Maturity:** fork-tracking (single "fixes" commit atop upstream)
- **Capabilities:** FP8 GEMM with 1D1D/1D2D scaling; grouped GEMMs (contiguous/masked) for MoE; K-axis-grouped GEMM for MoE backward; BF16/TF32/FP8xFP4; Mega MoE comm/compute overlap; lightweight runtime JIT; **Chutes addition** `warmup_kernels` (~417 lines) pre-warming the JIT cache to eliminate first-request cold-start compile latency.
- **Distributed-computing relevance:** LOW — no GraVal/p2p/consensus/TEE/E2EE/routing/miner logic; only "scheduling" is intra-GPU tile scheduling; only "communication" is intra-node MoE overlap (relies on DeepEP).
- **Portability verdict:** SKIP
- **Target Helix module:** none (single-node GPU compute primitive; at most a build/runtime dep of an inference worker)
- **Effort:** XL
- **Rationale:** CUDA/PyTorch kernel library with zero distributed surface; cannot be ported to Go; relevant only as a deep transitive dependency inside a GPU inference worker image (opaque container artifact). Chutes' delta is a JIT-warmup helper, not a distributed feature.
- **Risks:** Language mismatch; Hopper/Blackwell GPU + CUDA 12.3-12.9 lock-in; heavy build deps (CUTLASS/fmt/NVRTC/PyTorch); fork drift; misclassification risk (must NOT be modeled as a Helix distributed component).

## chutes-jumpmaster

- **Repo:** https://github.com/chutesai/chutes-jumpmaster
- **Purpose:** Client-side developer/ops toolkit for operating on the Chutes platform — wraps upstream Docker images into "chutes," auto-discovers HTTP/OpenAPI routes to generate passthrough cords, and provides a 2162-line interactive bash hub for build/deploy/warmup/logs. NOT platform infrastructure.
- **Language:** Python (tools) + Bash (bulk) · **License:** UNKNOWN — **blocking for code copy** · **Maturity:** experimental
- **Capabilities:** image-wrapping via `chutes.image.Image`; OpenAPI route auto-discovery → routes.json; passthrough cord generation; `register_service_launcher` on_startup hook; client-side NodeSelector GPU hints; account/wallet ops; multi-repo git sync.
- **Distributed-computing relevance:** LOW — operator tooling that talks to Chutes as a client; DC logic is in sibling repos.
- **Portability verdict:** REFERENCE (bordering SKIP)
- **Target Helix module:** LLMOrchestrator (cord/passthrough serving contract as reference) + `pkg/resources` (NodeSelector-style GPU request schema)
- **Effort:** S
- **Rationale:** Bash menus + Python image-wrapping/route-discovery that sit OUTSIDE the Chutes system. Conceptual takeaways only (cord passthrough-routing contract, NodeSelector shape, OpenAPI-probe-then-generate onboarding). License UNKNOWN forbids copy; language mismatch compounds.
- **Risks:** UNKNOWN license; Python+heavy Bash vs Go; very low maturity; tightly coupled to proprietary Chutes SDK + Bittensor wallet; misleading "orchestration" naming.

## model-router

- **Repo:** https://github.com/chutesai/model-router
- **Purpose:** Application-layer LLM request router (FastAPI on Vercel) in front of Chutes' LLM API. A tool-calling classifier buckets each request into 6 task types and forwards to a task-specific primary model with ordered fallback on 429/5xx/empty-response. Exposes OpenAI + Anthropic surfaces with a self-answer fast path.
- **Language:** Python · **License:** UNKNOWN — **blocking for code copy** · **Maturity:** active
- **Capabilities:** LLM task classification via forced tool-call (4-model classifier fallback); per-task ordered fallback chains (dedup, 8-deep cap, capacity demotion); empty-response detection; Anthropic↔OpenAI translation; self-answer; per-model registry; multi-tenant token pass-through billing; in-process metrics + 503 attempt-trace.
- **Distributed-computing relevance:** LOW — inference-routing proxy; no scheduler/GPU/p2p/consensus/miner/TEE.
- **Portability verdict:** REFERENCE
- **Target Helix module:** LLMOrchestrator
- **Effort:** S
- **Rationale:** Clean production-shaped routing pattern worth studying, not porting. ~50-100 lines a Go LLMOrchestrator could absorb (ordered per-task fallback + dedup+cap; empty/partial detection; 503 attempt-trace; capacity-aware demotion). Does not address hard DC gaps.
- **Risks:** No LICENSE (proprietary by default); Python/FastAPI/Vercel vs Go; tight Chutes coupling; thin serving proxy; module-global mutable state; forwards bearer tokens verbatim + CORS * (diverges from zero-trust).

## bittencert

- **Repo:** https://github.com/chutesai/bittencert
- **Purpose:** ~300-line Python library binding self-signed TLS certs to a Bittensor (Substrate) hotkey identity — generates a P-256 TLS keypair, signs cert metadata with the node's sr25519/ed25519 keypair, embeds the signature in the cert Organization field; a companion aiohttp connector verify-then-pins. PKI-less, identity-rooted mutual auth between subnet nodes.
- **Language:** Python · **License:** MIT · **Maturity:** experimental
- **Capabilities:** identity-signed self-signed P-256 certs; verify cert issued by holder of an ss58; identity obfuscation (OU = uuid5(ss58)); verify-then-pin aiohttp connector; pluggable async CertificateStore (LRU/TTL); Typer CLI.
- **Distributed-computing relevance:** LOW — node-identity/transport-auth primitive (prove an HTTPS endpoint is controlled by a specific on-chain hotkey without a CA; TOFU-with-identity-pinning). Signed payload omits SANs + pubkey (weak channel binding).
- **Portability verdict:** REFERENCE
- **Target Helix module:** security (node identity / mTLS), thin Go adapter only if Bittensor-subnet federation is pursued
- **Effort:** S
- **Rationale:** Tiny, clean, MIT. Core idea (root TLS trust in a node's signing keypair, not a CA, then pin-after-verify) is a useful E2EE/identity pattern, trivially reimplemented in Go (1-2 days). Porting the Python makes no sense (substrate-interface dependency). Adopt the PATTERN.
- **Risks:** Python (reimplement); heavy substrate-interface dep; weak/partial crypto binding (omits SAN + pubkey); connector disables standard TLS validation during fetch; experimental; scope-creep if Bittensor federation isn't a real goal.

## chutesai/SageAttention

- **Repo:** https://github.com/chutesai/SageAttention
- **Purpose:** CUDA/Triton GPU kernel library accelerating transformer attention by quantizing Q·Kᵀ to INT8 and P·V to FP8/FP16 (SA2/2++) or microscaling FP4 (SA3 on Blackwell). Drop-in SDPA/FlashAttention replacement. Single-GPU compute primitive.
- **Language:** CUDA/C++ + Python (PyTorch) + Triton · **License:** Apache-2.0 · **Maturity:** active
- **Capabilities:** INT8 per-block/warp/thread quant with smoothing; FP8 PV two-level accumulation; FP4 (Blackwell); arch-specialized kernels + Triton fallback; drop-in SDPA/FA2/3; torch.compile compat; benchmark harness.
- **Distributed-computing relevance:** LOW — no distributed logic of substance; chutesai fork byte-identical to upstream thu-ml.
- **Portability verdict:** WRAP
- **Target Helix module:** GPU(planned) node-local inference runtime under LLMOrchestrator; never linked into Go control plane
- **Effort:** M
- **Rationale:** CUDA/Python single-GPU kernel; nothing distributed to PORT. Realistic integration is WRAP-at-the-edge inside a Python inference server. Fork adds nothing over upstream thu-ml.
- **Risks:** CUDA+Python only; per-arch + per-CUDA wheel coupling; SA3 hardware-limited; quantization may degrade quality (sink-side e2e validation per CLAUDE-1); stale mirror.

## chutesai/SageAttention-1

- **Repo:** https://github.com/chutesai/SageAttention-1
- **Purpose:** Same family — CUDA/Triton quantized (INT8 QKᵀ + FP8/FP16 PV) attention kernels, drop-in FlashAttention/SDPA replacement, single-GPU primitive.
- **Language:** CUDA, Python, C++ (Triton) · **License:** Apache-2.0 · **Maturity:** fork-tracking
- **Capabilities:** INT8 Q/K quant with smoothing; FP8/FP16 PV; arch kernels + Blackwell FP4; drop-in `sageattn(q,k,v)`; benchmark harness.
- **Distributed-computing relevance:** LOW — essentially no distributed content; verbatim mirror of upstream thu-ml (empty diff, single 'Update README' commit).
- **Portability verdict:** SKIP
- **Target Helix module:** GPU(planned) inference worker — runtime dependency only
- **Effort:** S
- **Rationale:** Verbatim mirror with ZERO chutesai customizations; hand-written CUDA/Triton + thin Python wrapper, un-portable into Go. Belongs to the inference data-plane. SKIP.
- **Risks:** Verbatim mirror (overcounting risk); CUDA+Python+Triton incompatible with Go; hard GPU/driver coupling; near-lossless but not bit-exact (attestation tolerance); pinned to one upstream commit.

## chutes-dropzone

- **Repo:** https://github.com/chutesai/chutes-dropzone
- **Purpose:** Self-hosted "AI workspace" packaging project bundling OpenWebUI + n8n behind a TLS edge, wiring Chutes OAuth/OIDC SSO, routing OpenAI-compatible LLM traffic to `llm.chutes.ai` or through a shared e2ee-proxy sidecar that restricts the catalog to confidential-compute (TEE) chutes. Single-container/compose.
- **Language:** Shell + Lua/OpenResty + Python + TS/Vue + Dockerfiles · **License:** UNKNOWN (package.json private:true) — **blocking for code copy** · **Maturity:** active
- **Capabilities:** multi-app deploy behind one edge; Chutes OAuth2/OIDC SSO with PKCE; s6-rc single-container; inference reverse-proxy; **TEE gating** (model_catalog.lua filters `/v1/models` to confidential_compute==true); confidential-inference instance discovery (e2ee_discovery.lua → per-instance pubkeys + short-TTL nonces); per-service Postgres isolation; smoke + Playwright e2e.
- **Distributed-computing relevance:** LOW — app-packaging repo; only distributed-adjacent content is the two Lua files (TEE-only routing policy + nonce-based handshake); actual crypto in upstream parachutes/e2ee-proxy image.
- **Portability verdict:** REFERENCE
- **Target Helix module:** LLMOrchestrator / security (E2EE) — reference patterns only
- **Effort:** S
- **Rationale:** Single-host app launcher with Chutes SSO; zero scheduling/placement/GPU/p2p/consensus. Transferable ideas: TEE-only catalog filter + nonce/pubkey instance-discovery cache → reimplement in Go.
- **Risks:** No LICENSE + private:true (blocks PORT/WRAP); core crypto absent (external image); tight Chutes coupling; Shell/Lua/Python/TS vs Go; explicitly single-host (contradicts Helix multi-node).

## chutes-n8n-local

- **Repo:** https://github.com/chutesai/chutes-n8n-local
- **Purpose:** Deployment/packaging repo for self-hosting n8n CE wired to Chutes — adds "Login with Chutes" OAuth2/PKCE SSO, bundled n8n-nodes-chutes, and an optional e2ee-proxy sidecar routing LLM text only to TEE instances. A consumer/client of Chutes' decentralized GPU compute.
- **Language:** Shell + TS + Lua/OpenResty + Dockerfiles + Python · **License:** UNKNOWN (API 404; n8n Sustainable Use) — **blocking for code copy** · **Maturity:** active
- **Capabilities:** single-container (s6) + compose; OAuth2 Auth Code + PKCE (S256) against Chutes IdP; managed-credential lifecycle (encrypted tokens, refresh, scope chutes:read/invoke); **TEE gating** (e2ee_discovery.lua); E2E attestation client (`/e2e/instances/{chute_id}` → e2e_pubkey + ~55s nonces); traffic-mode routing (direct vs e2ee-proxy); resource-type→subdomain routing; retry+backoff.
- **Distributed-computing relevance:** LOW — thin client; the one distributed-relevant artifact is the TEE attestation+routing CLIENT in e2ee_discovery.lua; actual E2E crypto in upstream parachutes/e2ee-proxy image.
- **Portability verdict:** REFERENCE
- **Target Helix module:** LLMOrchestrator (inference routing) + security/E2EE (TEE-gated routing); secondary future GPU attestation submodule
- **Effort:** S
- **Rationale:** Deployment/packaging product, not infrastructure. Transferable IP is conceptual: e2ee_discovery.lua pattern (require confidential_compute before routing; discover instances + pubkeys + short-TTL nonces; OAuth scope introspection to gate invocation).
- **Risks:** UNKNOWN license + n8n Sustainable Use obligations; Go vs Lua/TS; real crypto in external image; tight Chutes coupling; chatty ~55s nonce TTL discovery.

## SaintDurbin (chutesai/st_durbin)

- **Repo:** https://github.com/chutesai/st_durbin
- **Purpose:** A single Solidity contract for the Bittensor Subtensor EVM that autonomously distributes TAO staking yield to 16 hard-coded recipients by fixed basis points while protecting principal, and auto-switches the staking validator when the current one loses permit. Off-chain Node.js + GitHub Actions cron triggers daily `executeTransfer()`.
- **Language:** Solidity 0.8.20 + JavaScript · **License:** **GPL-3.0-only (copyleft — incompatible with permissive Go stack; avoid copying)** · **Maturity:** active
- **Capabilities:** read metagraph via 0x802; validator-selection scoring (score = stake*(1+dividend/65535), pick highest, moveStake with rollback); proportional payout to 16 recipients (10,000 bps); principal-protection heuristics; time-locked emergency drain; permissionless reentrancy-guarded trigger.
- **Distributed-computing relevance:** LOW — on-chain financial/treasury contract; only contact with DC is Bittensor subnet mechanics at the consumer level (validator-failover pattern). chutesai is 0 ahead / 0 behind source.
- **Portability verdict:** SKIP
- **Target Helix module:** none (at most REFERENCE for a future Bittensor settlement/marketplace adapter)
- **Effort:** S
- **Rationale:** Pristine unmodified fork of a 567-line Solidity yield-distribution treasury; ZERO DC customizations; wrong language/domain. Only reusable idea (metagraph validator-data + stake-weighted selection) is generic and only matters if Helix integrates a Bittensor settlement layer.
- **Risks:** GPL-3.0 copyleft (incompatible) + SPDX/LICENSE ambiguity; Solidity/EVM can't run in Go; tight precompile coupling; domain mismatch; single cron SPOF.

## chutes-search

- **Repo:** https://github.com/chutesai/chutes-search
- **Purpose:** AI-powered "answer engine" (Perplexica fork), Next.js 15/TypeScript. Web search + retrieve/rank + streamed LLM-synthesized answers. Chutes additions: Chutes OpenAI-compatible inference with multi-model fallback + model-router, OAuth2 PKCE SSO, IP-hashed rate limiting, AES-256-GCM per-user field encryption, and "Deep Research" orchestrating ephemeral Sandy sandboxes.
- **Language:** TypeScript · **License:** MIT · **Maturity:** active
- **Capabilities:** Chutes LLM client; multi-candidate fallback (`runWithLlmCandidates`) with typed error classification → deterministic chain ending in model-router; optimization-mode→model routing; ephemeral sandbox client (Sandy: create/terminate/status/exec/files) with priority/preemptable/flavor scheduling hints; OAuth PKCE + sealed cookies; AES-256-GCM per-user field encryption; IP-hashed daily quota; SSE generation.
- **Distributed-computing relevance:** LOW — consumer of distributed compute; touch-points are inference failover, an ephemeral-compute scheduling client passing hints to a remote scheduler (Sandy), surface-level TEE.
- **Portability verdict:** REFERENCE
- **Target Helix module:** LLMOrchestrator (fallback/routing); secondary reference for scheduler scheduling-hint contract + security field encryption
- **Effort:** S
- **Rationale:** Whole product out of scope. Two small modules worth reimplementing in Go: fallbacks.ts (error-classified LLM failover) and sandy.ts (ephemeral GPU/agent sandbox scheduler contract incl. priority/preemptable/flavor — useful for Helix scheduler API + marketplace job-submission). MIT.
- **Risks:** TS/Next.js vs Go; end-user web app; low maturity; -TEE unverified; brittle substring error matching; heavy LangChain/JS deps.

## ai-sdk-provider-chutes

- **Repo:** https://github.com/chutesai/ai-sdk-provider-chutes
- **Purpose:** Client-side TypeScript npm package implementing a Vercel AI SDK v4/v5 provider for Chutes hosted inference. Maps AI SDK calls to per-model subdomain endpoints, adds dynamic chute discovery and a "Therm" cold-start warmup client. Pure API client.
- **Language:** TypeScript · **License:** MIT · **Maturity:** active
- **Capabilities:** Vercel AI SDK provider; OpenAI-compatible conversion targeting per-chute subdomains; dynamic discovery (`GET /chutes/`); capability inference heuristics; **Therm warmup** (`GET /chutes/warmup/{chuteId}` → hot/warming/cold + instanceCount); non-blocking ThermalMonitor (polling, waitUntilHot, reheat); availability probe via error-string classification; async custom-inference (priority queue, webhooks); 327+ tests.
- **Distributed-computing relevance:** LOW — thin HTTP client; all scheduling/placement/validation server-side. Distributed-adjacent signals: cold-start/autoscaling visibility (Therm), capacity/health-probe, service discovery, async job offload.
- **Portability verdict:** REFERENCE
- **Target Helix module:** LLMOrchestrator (+ optional `pkg/inference/providers/chutes` adapter if Helix consumes Chutes upstream)
- **Effort:** S
- **Rationale:** TS SaaS client; zero server-side distributed logic. Value is conceptual: Therm warmup + ThermalMonitor is a clean tested reference for how LLMOrchestrator should pre-warm scale-from-zero GPU backends and expose {cold/warming/hot, instanceCount} before dispatch — mirror in Go.
- **Risks:** MIT but thin distributed substance; TS vs Go; brittle log-string regex + error-string sniffing; real mechanics server-side/absent; heuristic capability inference; small project.

---

# TIER: NONE

## lua-oasvalidator

- **Repo:** https://github.com/chutesai/lua-oasvalidator
- **Purpose:** Thin Lua C-API binding over the C++11 `cpp-oasvalidator` library that validates inbound HTTP requests against an OpenAPI 3.x spec, intended for Lua-scriptable API gateways (Kong, OpenResty).
- **Language:** Lua (binding over C++) · **License:** MIT · **Maturity:** stale (identical to upstream, no Chutes commits)
- **Capabilities:** load OAS spec; sequential request validation (`ValidateRoute`/`ValidateBody`/`ValidatePathParam`/`ValidateQueryParam`/`ValidateHeaders`/`ValidateRequest`); param-style deserialization; numeric error codes + JSON-pointer errors; lazy deserialization; method aliasing.
- **Distributed-computing relevance:** NONE — OpenAPI schema-conformance validator for single HTTP requests.
- **Portability verdict:** SKIP
- **Target Helix module:** N/A (closest neighbor would be an API-gateway/admission layer)
- **Effort:** S
- **Rationale:** Zero DC relevance; Lua-over-C++ requires a Lua VM + CMake/C++ build alien to Helix's Go stack; byte-identical to upstream (Chutes added nothing); any equivalent capability is trivially obtained from a native Go OpenAPI validator. Nothing to port/wrap/reference for distributed-systems design.
- **Risks:** Language/runtime mismatch; fork drift (no Chutes changes; upstream dormant); single-author hobby project; MIT (moot given SKIP).

## chutesai/hermes-agent

- **Purpose:** Single-user self-improving AI agent framework (TUI/CLI + messaging gateway) with skills/cron/MCP. "Chutes edition" only points base_url at llm.chutes.ai/v1. An LLM application, not infrastructure.
- **Language:** Python · **License:** MIT · **Maturity:** active
- **Capabilities:** agent loop with 40+ tools; pluggable providers; messaging gateway; procedural-memory skills; FTS5 session search; cron; pluggable terminal backends (local/Docker/SSH/Daytona/Modal); subagent spawning; RL/research tooling; MCP client.
- **Distributed-computing relevance:** NONE — no scheduler/GPU/miner/validator/subnet/p2p/consensus/TEE; terminal "backends" are single-tenant remote-exec; Chutes delta is provider base_url config.
- **Portability verdict:** SKIP · **Target Helix module:** none · **Effort:** XL
- **Rationale:** End-user LLM assistant; advances no Helix gap; massive monolithic Python coupled to LLM SDKs. SKIP.
- **Risks:** Large monolithic Python vs Go/Zig; single-user; XL effort low value; fork-tracking burden; heavy optional dep surface.

## TurboDiffusion (chutesai fork)

- **Purpose:** CUDA/PyTorch framework accelerating video diffusion (Wan2.1/2.2) 100-200x on one GPU via SLA/SageAttention kernels, INT8 quant, rCM distillation. FSDP+context-parallel training + single-process TUI serving.
- **Language:** Python + CUDA/C++ (CUTLASS) · **License:** Apache-2.0 · **Maturity:** fork-tracking
- **Capabilities:** few-step video diffusion; SLA/SageAttention kernels; INT8 linear + custom norms/GEMM; single-process TUI; FSDP + sequence/context-parallel training; WebDataset synthetic-data gen.
- **Distributed-computing relevance:** NONE — chutesai fork byte-identical to upstream thu-ml; only multi-GPU code is intra-job training parallelism; 'serve' is single-process TUI. A leaf compute workload.
- **Portability verdict:** SKIP · **Target Helix module:** none (optionally a containerized workload behind LLMOrchestrator/GPU scheduler) · **Effort:** XL
- **Rationale:** Nothing to port; single-GPU CUDA/PyTorch with zero decentralized-compute logic; fork adds zero over upstream. Realistic relationship is consumption (containerize checkpoints+script).
- **Risks:** Python+CUDA/CUTLASS; heavy hardware coupling; checkpoints on external HF; identical mirror; Apache-2.0 irrelevant.

## chutes-style

- **Purpose:** Self-contained frontend brand/UI style kit distilled from chutes-web (design tokens, brand guide, logos/fonts, static HTML preview board, zero-dep Node static server, ~80 Svelte SVG icons). Purely cosmetic.
- **Language:** Svelte/CSS/JS · **License:** UNKNOWN · **Maturity:** experimental
- **Capabilities:** design token system; brand docs; ~80 Svelte SVG icons; static overview board; zero-dependency Node static server; packaged brand assets.
- **Distributed-computing relevance:** NONE
- **Portability verdict:** SKIP · **Target Helix module:** none (at most a future cosmetic operator console) · **Effort:** S
- **Rationale:** Brand/UI kit, not infrastructure; zero distributed/GPU/scheduling/security primitives; irrelevant to a Go cluster OS; blocked by unknown license + Chutes trademark.
- **Risks:** No LICENSE; trademark/brand encumbrance; Svelte/CSS vs Go; bundled fonts may carry separate licenses.

## n8n-nodes-chutes

- **Purpose:** n8n community node package (TS) exposing Chutes.ai services to n8n workflows — three nodes + credential type; a pure HTTP client SDK against Chutes' public REST API.
- **Language:** TypeScript · **License:** MIT · **Maturity:** active
- **Capabilities:** HTTP client w/ Bearer auth; per-resource base-URL routing; OpenAPI capability discovery (per-chute `/openapi.json`, 1h cache); dynamic param mapping; 429 backoff; LangChain chat-model wrapper; dynamic model dropdown.
- **Distributed-computing relevance:** NONE — strictly a client integration; only 429 backoff-retry. Architecturally informative: per-chute subdomain serving + `/openapi.json` self-description.
- **Portability verdict:** SKIP · **Target Helix module:** none (at most REFERENCE for LLMOrchestrator discovery/routing) · **Effort:** S
- **Rationale:** n8n plugin + thin HTTP client; no distributed/scheduling/GPU/consensus to port/wrap; the per-chute `/openapi.json` discovery is trivial to reimplement and not worth a dependency. SKIP.
- **Risks:** TS/Node + n8n + LangChain vs Go; SaaS client; v0.1.0 single-author; heuristic schema fallbacks brittle; MIT (only wasted-effort risk).

## chutesai/openclaw

- **Purpose:** Fork of openclaw/openclaw (375K-star TS agentic coding-assistant CLI). chutesai's only addition is a bundled Chutes model-provider plugin routing inference to llm.chutes.ai/v1 via OAuth (PKCE) or API key, defaulting to TEE models.
- **Language:** TypeScript · **License:** MIT (parent NOASSERTION) · **Maturity:** fork-tracking
- **Capabilities:** agentic CLI (tools/skills/MCP/ACP); pluggable provider architecture; Chutes provider plugin (OAuth2 PKCE + API-key, token refresh); inference routing; TEE-model selection; containerized sandboxes; model aliasing/fallback.
- **Distributed-computing relevance:** NONE (in-repo) — purely a CLIENT; only signal is the default `-TEE` catalog (attestation server-side).
- **Portability verdict:** REFERENCE · **Target Helix module:** LLMOrchestrator (+ security for OAuth-PKCE) · **Effort:** S
- **Rationale:** TS agentic fork whose chutes delta is a thin provider plugin; zero scheduler/miner/validator/consensus/attestation. Reference-only: the Chutes inference surface + TEE catalog with pricing (ready upstream a Go LLMOrchestrator could call) and the OAuth2-PKCE onboarding pattern.
- **Risks:** TS/Node vs Go; no distributed substance in-repo; fast-moving upstream (drift); Chutes IdP/endpoint coupling; TEE = name suffix (no cryptographic attestation).

## chutesai/moltbot

- **Purpose:** Local-first single-user "personal AI assistant" gateway bridging consumer messaging channels to LLM-backed agents. Central "Gateway" control plane manages sessions/channels/tools/cron. Fork of openclaw/openclaw.
- **Language:** TypeScript · **License:** MIT · **Maturity:** active
- **Capabilities:** local-first single-host Gateway; multi-channel messaging adapters; LLM agent orchestration with OAuth subscription auth + auth-profile rotation/failover; Sign-in-with-Chutes OAuth (PKCE); tool/plugin SDK + MCP-style extensions; daemonized service; DM pairing/allowlist.
- **Distributed-computing relevance:** NONE — single-host single-user control plane fanning messaging events to an LLM agent loop; a CONSUMER of remote inference; Chutes delta is an OAuth IdP client.
- **Portability verdict:** SKIP · **Target Helix module:** none · **Effort:** XL
- **Rationale:** Consumer AI chat gateway, single-host/single-user, TS/Node mismatched with Go/Zig; only chutes delta is a client OAuth shim; fills no Helix gap. At most chutes-oauth.ts PKCE is a tiny REFERENCE. SKIP.
- **Risks:** MIT OK but fast-moving fork; TS/Node vs Go/Zig; consumer single-user not cluster; unclear maintenance; broad messaging attack surface.

## chutesai/Sign-in-with-Chutes

- **Purpose:** Copy-paste TS/Next.js SDK adding "Sign in with Chutes" OAuth 2.0 Auth-Code + PKCE to web apps. Token used as Bearer to call Chutes inference on the user's behalf.
- **Language:** TypeScript · **License:** MIT · **Maturity:** experimental
- **Capabilities:** OAuth 2.0 Auth Code + PKCE (S256) against api.chutes.ai/idp/*; PKCE + CSRF state; code↔token exchange + refresh rotation; OIDC userinfo; HttpOnly cookie sessions; Next.js route handlers + useChutesSession hook; OAuth app registration; user-delegated inference pattern.
- **Distributed-computing relevance:** NONE — pure OAuth client; only adjacency is documenting the Chutes IDP OAuth surface + fine-grained `chutes:invoke:{chute_id}` scope.
- **Portability verdict:** SKIP · **Target Helix module:** none (at most reference notes for security/auth + LLMOrchestrator) · **Effort:** S
- **Rationale:** Single-purpose MIT OAuth client SDK, no tests, no distributed content. Helix is Go/Zig; reimplementing browser OAuth-with-cookies in Go is trivial stdlib work. Only durable value is documentary (IDP endpoint map, scope taxonomy). Capture as reference notes; otherwise skip.
- **Risks:** TS/Next.js vs Go/Zig; no distributed capability; tight Chutes IDP coupling; 1 star/no tests/incomplete; tokens-in-cookies + 30-day refresh need hardening.

## chutesai/chutes-agent-toolkit

- **Purpose:** Empty placeholder GitHub repository. No commits, branches, README, code, or license (size=0).
- **Language:** none · **License:** UNKNOWN (empty repo) · **Maturity:** unknown
- **Capabilities:** None. API confirms size=0, language=null, license=null; clone returns empty-repository warning.
- **Distributed-computing relevance:** NONE
- **Portability verdict:** SKIP · **Target Helix module:** none · **Effort:** S
- **Rationale:** Nothing to port, wrap, or study. Every avenue confirms an empty placeholder. Skip now; optionally re-survey if Chutes pushes code.
- **Risks:** Empty repo; UNKNOWN license; misleading one-liner; likely-future Python (mismatch); short shelf life.
