# chutes
- **Repo:** https://github.com/chutesai/chutes
- **Language:** Python
- **License:** MIT
- **Maturity:** production
- **Distributed-Computing Relevance:** core
- **Portability Verdict:** REFERENCE
- **Target Helix Module:** NEW submodule `pkg/gpuattest` (GraVal/TEE attestation port) + `LLMOrchestrator`/`pkg/scheduler` (NodeSelector + serving model) + `security` (E2EE transport patterns); miner/marketplace (Phase8/8B)

## Purpose
`chutes` is the client SDK, CLI, and in-pod runtime for the Chutes.ai decentralized GPU inference platform (a Bittensor subnet). It lets developers package an inference app ("chute" = a FastAPI app; "cord" = a route; "job" = a non-API rental) onto a CUDA Docker image, declare GPU requirements via a `NodeSelector`, deploy it to miner-owned GPUs, and — critically — it embeds the runtime middleware that performs GPU validation/attestation and end-to-end-encrypted request/response transport between validators and the workload running on untrusted miner hardware. The miner scheduler and validator/consensus code live in separate repos (`rayonlabs/chutes-miner`, `rayonlabs/chutes-api`); this repo is the SDK + the trust/serving runtime that runs *inside* each GPU pod.

## Capabilities
- **App/route model:** `Chute(FastAPI)` + `@chute.cord()` decorators that auto-generate OpenAPI schemas from type hints; supports streaming, passthrough proxying (to a local vLLM/SGLang/ComfyUI daemon), and arbitrary webservers. SGLang passthrough wires per-request IDs (`rid`) and `abort_request` propagation on client disconnect.
- **Declarative GPU placement constraints:** `NodeSelector(gpu_count 1–8, min_vram_gb_per_gpu 16–140, max_hourly_price_per_gpu, include/exclude GPU model lists)` — the scheduling *intent* contract consumed by the off-repo placement engine.
- **Autoscaling/billing policy on the workload:** `concurrency`, `max_instances`, `scaling_threshold` (in-flight ratio trigger), `shutdown_after_seconds` (scale-to-zero), per-GPU hourly billing.
- **GPU validation/attestation (GraVal):** Proof-of-Valid-Work — validator seeds a challenge, runtime runs seeded matrix multiplications on each GPU, VRAM-capacity checks, device-info challenges, and extracts a symmetric key by GPU-bound decryption to prove the claimed GPUs are real.
- **TEE / confidential compute:** Intel TDX attestation path — an evidence server (`/verify`, `/evidence`) binds a validator nonce + E2E pubkey, fetches a TDX quote (hash of service pubkey) from an attestation service, returns it for verification before releasing the symmetric key.
- **Layered E2E encryption transport ("Aegis", shipped as `chutes-aegis.so`):** x25519 ECDH session key derivation → AES-256-GCM session encryption; plus a post-quantum **ML-KEM (Kyber)** end-to-end channel for request decryption / response encryption with a streaming mode (`e2e_stream_begin/chunk/end`, 1088-byte ML-KEM ciphertext header). Generates TLS/mTLS certs in-enclave.
- **Filesystem attestation (`cfsv`, C lib via ctypes):** Merkle-style `cfsv_challenge` over the container filesystem (salted, sparse), `cfsv_sizetest` for VRAM/disk-capacity proofs, bytecode cleanup, versioned challenge binaries (cfsv_v2..v4).
- **Bittensor identity auth:** request signing with SS58 hotkey keypairs (`sign_request`, nonce + sha256 payload signature) — decentralized identity instead of API-server-issued tokens (API keys layered on top).
- **Anti-noisy-neighbor runtime hardening:** all user code runs in a dedicated thread pool with per-request cancel handles + disconnect watchers so a blocking CUDA call can't starve health-check pings; module locking (`lock_modules`), egress firewalling after startup (`allow_external_egress`), encrypted FS option.
- **Image build DSL:** `Image` builder with Dockerfile-like directives (from_base, run_command, env, add, apt, workdir, entrypoint); standard templates for vllm/sglang/diffusion/embedding.

## Distributed-Computing Notes
- **GPU validation/attestation:** Two interchangeable verifier strategies behind one interface (`GpuVerifier.create` → `GravalGpuVerifier` | `TeeGpuVerifier`). GraVal is challenge-response PoVW seeded by validator; TEE is hardware quote attestation. Both culminate in the validator releasing a symmetric key only after proof — this is the trust root for running workloads on adversarial, unverified GPU suppliers.
- **Scheduling/placement:** *Intent* lives here (`NodeSelector`); the *solver* (Omega-style optimistic scheduler in Helix terms) lives in `chutes-api`/`chutes-miner` (not in this repo). So this is the placement-constraint schema, not the scheduler.
- **p2p/gossip & consensus/weights:** NOT in this repo. Chutes consensus, validator weight-setting, and subnet gossip are Bittensor-substrate + the off-repo validator. This SDK only signs with the hotkey and talks HTTP to a validator URL. ("fiber" p2p / SWIM-style gossip is not present here.)
- **TEE/confidential compute:** Full Intel TDX flow with nonce binding to the E2E pubkey (prevents quote replay/relay), SSL verification deliberately disabled because authenticity is proven via the TDX quote embedding the service pubkey hash.
- **E2EE inference transport:** Best-in-class reference — combines classical ECDH+AES-GCM with post-quantum ML-KEM, including a streaming AEAD framing protocol. Decryption/encryption happens in middleware (`GraValMiddleware`) so user inference code never sees ciphertext; `request.state._encrypt`/`decrypted` abstract it.
- **Serving/routing:** Passthrough cords + disconnect-aware upstream abort give production-grade streaming LLM serving with proper backpressure/cancellation semantics.
- **Fault tolerance:** Provisioning backoff (`StillProvisioning` 503 retry), scale-to-zero, disconnect → 499 conversion to keep H2 connections healthy, inner-coroutine cancellation on timeout/disconnect.

## HelixCluster Gaps Addressed
- **GPU (planned Helix module):** This is the single most valuable reference Helix has for *trustless* GPU verification. Helix's planned GPU resource layer can port the GraVal PoVW pattern (seeded matmul + VRAM check + device-info challenge) and the TDX attestation evidence flow to validate GPU nodes that aren't fully trusted.
- **scheduler/Omega + resources:** `NodeSelector` is a clean, portable placement-constraint contract (gpu_count, min_vram, price cap, include/exclude) that maps directly onto Helix's Omega scheduler input and resource model.
- **security / E2EE:** The Aegis layered transport (ECDH+AES-GCM session + ML-KEM PQ E2E + streaming AEAD) is a strong reference for Helix's E2EE inference transport and confidential-compute story; the design (decrypt-in-middleware, key release gated on attestation) is portable even though the `.so` itself is not.
- **miner/marketplace (Phase8/8B):** The end-to-end economic model — node selection → attestation → symmetric-key release → metered hot/scale-to-zero billing → chute sharing — is a near-complete blueprint for Helix's GPU marketplace/miner phases.
- **leader/consensus & discovery:** NOT addressed here (lives off-repo in Bittensor + chutes-api/chutes-miner). Helix should look at those repos for consensus/weights, not this SDK.
- **LLMOrchestrator:** Passthrough+streaming+disconnect-abort serving patterns inform Helix's inference routing/orchestration.

## Dependencies
- Python 3.10+, FastAPI/Starlette + Uvicorn + Hypercorn[h2] (HTTP/2 serving), aiohttp[speedups], pydantic v2, orjson.
- **`graval>=0.2.6`** (CUDA GPU validation library — the heart of GravalGpuVerifier; CUDA/native).
- **Bittensor:** `bittensor-wallet`, `async-substrate-interface` (SS58 hotkey signing).
- `cryptography` (AES-CBC/GCM, PKCS7), `pyjwt` (launch tokens), `rbcl` (Ristretto/libsodium bindings), `pybase64`.
- Native blobs shipped in-repo: `chutes-aegis.so` (E2E/x25519/ML-KEM), `chutes-aegis-verify.so`, `chutes-bcm.so`, `chutes-inspecto.so`, `envdump.so`, `cfsv`/`cfsv_v2..v4` (filesystem challenge C binaries) — **closed/opaque**, ctypes-loaded.
- `huggingface_hub`, `hf_transfer` (model weights), `typer`+`rich`+`textual` (CLI/TUI), `prometheus-client`, `cllmv==0.1.3`, `netifaces-plus`, `pyudev`, `psutil`.

## Rationale
REFERENCE (not PORT/WRAP) because: (1) it is Python and Helix is Go — no shared runtime; (2) the most valuable pieces (GraVal GPU proofs, Aegis E2E/ML-KEM, cfsv filesystem challenge, inspecto/bcm/aegis-verify) ship as **opaque precompiled `.so`/binary blobs** with no source in this repo, so they cannot be ported, only re-implemented from their observable protocols; (3) it is deeply coupled to Bittensor subnet identity and CUDA. However, the *architecture and protocols* are extremely high-value: this is a battle-tested, production blueprint for trustless GPU compute, attestation-gated key release, and post-quantum E2EE inference — exactly Helix's primary interest. Helix should mine it for design (GPU attestation handshake, NodeSelector schema, billing/scale-to-zero state machine, decrypt-in-middleware E2EE) and reference the sibling `chutes-miner`/`chutes-api` repos for the scheduler/consensus pieces that are absent here. Wrapping is technically possible (call the SDK from a Go orchestrator over HTTP) but ties Helix to Bittensor and the closed blobs, so it is not recommended.

## Risks
- **Language mismatch:** Pure Python; Helix core is Go/Zig — any reuse is a clean-room re-implementation, not a port.
- **Opaque native blobs:** GraVal, Aegis (E2E/ML-KEM), cfsv, inspecto, bcm, aegis-verify are closed `.so`/binaries — license on the *blobs* is effectively UNKNOWN/proprietary even though the Python wrapper is MIT; the actual crypto/attestation logic is not auditable from this repo.
- **Heavy CUDA/Python coupling:** `graval` requires real NVIDIA GPUs/CUDA; cannot run or test attestation paths on Helix's Go test infra.
- **Bittensor lock-in:** Identity, payment, and (off-repo) consensus assume a Bittensor subnet; porting the trust model without Bittensor requires replacing the whole hotkey/weights substrate.
- **Split codebase / fork-drift-like risk:** Scheduler, validator, and consensus are in separate fast-moving repos; analyzing only this SDK gives an incomplete view and any design Helix copies must track three repos.
- **TLS-verification-disabled pattern:** The `verify_mode = CERT_NONE` approach is safe *only* with the TDX-quote-binds-pubkey invariant; naively copying it without the attestation binding would be a security hole.
