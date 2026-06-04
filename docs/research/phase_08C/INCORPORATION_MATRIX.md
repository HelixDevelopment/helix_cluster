# Chutes AI → HelixCluster Incorporation Matrix (Phase 8C, COMPLETE)

> **Research Document** | Phase 8C — Incorporation Decisions | 2026-05-31 (FINAL, all 42 repos)
>
> Master decision table for every repo in the `chutesai` org, sorted by value (CORE PORT/WRAP first → REFERENCE → SKIP). Then a per-gap rollup mapping Helix's distributed-compute gaps (scheduler, resources, GPU validation/attestation, federation, E2EE/security, LLM routing/serving, miner/marketplace, consensus/discovery, TEE) to contributing repos + recommended action.
>
> **License legend:** MIT/Apache-2.0 = permissive (port/snippet OK with attribution). UNKNOWN = no LICENSE → all-rights-reserved → **clean-room reimplementation only, no code copy.** GPL-3.0 = copyleft → **incompatible with Helix's permissive Go stack → avoid.**
>
> **Verdict legend:** **PORT** = reimplement protocol/algorithm natively in Go. **WRAP** = run as-is (container/sidecar) and integrate over an API. **REFERENCE** = study the design, no code lands. **SKIP** = no actionable value.
>
> **Dist-Compute (DC) legend:** core / high / medium / low / none.

---

## Master Table (sorted by value: CORE PORT/WRAP → REFERENCE → SKIP)

| Repo | License | DC | Verdict | Target Helix Module | Effort | Gap Filled |
|------|---------|----|---------|---------------------|--------|------------|
| **chutes-e2ee-transport** | MIT | high | **PORT** | `pkg/security/e2ee` → LLMOrchestrator | M | E2EE/security — PQ confidential inference transport (client+worker) |
| **e2ee-test** | UNKNOWN | medium | **PORT** (clean-room) | `pkg/e2ee` → LLMOrchestrator | M | E2EE/security — PQ inference envelope + discovery/nonce protocol (byte-validatable) |
| **chutesai/vllm** | Apache-2.0 | high | **WRAP** | LLMOrchestrator (container, OpenAI API) | M | LLM serving — inference engine backend |
| **chutesai/SageAttention** | Apache-2.0 | low | **WRAP** | GPU(planned) worker runtime (Python edge) | M | GPU compute — attention kernel acceleration (data-plane) |
| **chutes-api** | MIT | core | REFERENCE | `pkg/scheduler` + GPU + security/E2EE + `minermarket` + `pkg/attestation` | L | GPU validation/attestation; scheduler; E2EE; miner/marketplace; consensus/weights |
| **chutes-miner** | MIT | core | REFERENCE | `pkg/scheduler` (Gepetto) + `pkg/resources` + `miner`/`marketplace` + security | L | Scheduler (cost-aware + preemption); GPU inventory; federation; miner economics |
| **graval** | MIT* | core | REFERENCE | NEW `pkg/gpuattest` → scheduler + resources | L | GPU validation/attestation — software proof-of-GPU + device-bound E2EE |
| **chutes** | MIT | core | REFERENCE | NEW `pkg/gpuattest` + LLMOrchestrator/scheduler + security | L | GPU/TEE attestation; NodeSelector; Aegis E2EE; scale-to-zero billing |
| **sek8s** | MIT | high | REFERENCE | `pkg/security/attestation` + `pkg/security/admission` | L | TEE — dual-root (TDX+NVIDIA) attestation, measured boot, attested key release, admission |
| **fiber** | MIT | high | REFERENCE | security (E2EE) + federation/miner submodule | M | E2EE transport; federation; stake-gated admission; weight/commit-reveal settlement |
| **e2ee-proxy** | MIT | high | REFERENCE | `pkg/llm/e2ee` + LLMOrchestrator + security | M | E2EE/security — PQ AEAD envelope wire-contract + TEE gating policy |
| **chutes-docs** | UNKNOWN | high | REFERENCE | GPU + security + miner/marketplace + federation (docs) | S | Architecture map for all of the above (no code lands) |
| **chutesai/sglang** | Apache-2.0 | high | REFERENCE (engine WRAP-able) | LLMOrchestrator + NEW `pkg/attestation` | M | LLM serving + model-integrity admission gate (hf_cache_verify → Go) |
| **chutes-audit** | MIT | medium | REFERENCE | `pkg/marketplace/audit` (Phase 8/8B) | M | Miner/marketplace — commit-then-prove audit log + reproducible reward reconciliation |
| **cllmv** | MIT | medium | REFERENCE | LLMOrchestrator (inference verification) / security | M | LLM serving integrity — model/revision anti-cheat + X25519 session handshake |
| **genlayer-studio** | MIT | medium | REFERENCE | `pkg/scheduler` + LLMOrchestrator | M | Scheduler/consensus — `SKIP LOCKED` work-claiming + leader/appeal + provider failover |
| **research-data-opt-in-proxy** | UNKNOWN | medium | REFERENCE | LLMOrchestrator + NEW `pkg/inferenceproxy` + security | M | LLM routing/observability — miner-selection telemetry + deterministic anonymization |
| **squad-api** | MIT | medium | REFERENCE | LLMOrchestrator + `pkg/scheduler` | M | Scheduler — Kueue-style suspend-then-admit tiered job admission; agent sandboxing |
| **chutes-autopilot** | UNKNOWN | medium | REFERENCE | LLMOrchestrator + scheduler ranking | S | LLM routing — utilization-aware ranking + streaming-safe failover |
| **chutes-jumpmaster** | UNKNOWN | low | REFERENCE | LLMOrchestrator + `pkg/resources` | S | Resources — NodeSelector request schema; cord passthrough-routing contract |
| **model-router** | UNKNOWN | low | REFERENCE | LLMOrchestrator | S | LLM routing — per-task ordered fallback + empty-response detection |
| **bittencert** | MIT | low | REFERENCE | security (node identity / mTLS) | S | Security — keypair-rooted TLS (no CA) verify-then-pin identity binding |
| **chutes-dropzone** | UNKNOWN | low | REFERENCE | LLMOrchestrator / security (E2EE) | S | E2EE/security — TEE-only catalog filter + nonce/pubkey discovery cache |
| **chutes-n8n-local** | UNKNOWN | low | REFERENCE | LLMOrchestrator + security/E2EE | S | E2EE/security — TEE-gated routing client + OAuth scope introspection |
| **chutes-search** | MIT | low | REFERENCE | LLMOrchestrator + scheduler hint contract | S | LLM routing + scheduler — ephemeral sandbox scheduling-hint contract (priority/preemptable/flavor) |
| **ai-sdk-provider-chutes** | MIT | low | REFERENCE | LLMOrchestrator (+ chutes provider adapter) | S | LLM routing — cold-start warmup/thermal state machine (scale-from-zero pre-warm) |
| **claude-proxy** | UNKNOWN | low | REFERENCE | LLMOrchestrator (API translation shim) | S | LLM routing — Claude↔OpenAI translation + circuit breaker |
| **codex** | Apache-2.0 | low | REFERENCE | LLMOrchestrator (provider adapter) | S | LLM routing — provider-aware rewriting + text tool-call fallback grammar |
| **responses-proxy** | UNKNOWN | low | REFERENCE | LLMOrchestrator (Responses↔Chat adapter) | S | LLM routing — Responses↔Chat-Completions streaming converter |
| **cpp-oasvalidator** | MIT | low | REFERENCE | NEW API-gateway request-validation middleware | M | Security/edge — OpenAPI admission (short-circuit ordering, path-trie, JSON-Pointer errors) |
| **openclaw** | MIT | none | REFERENCE | LLMOrchestrator (OAuth-PKCE) | S | Security/routing — OAuth2-PKCE onboarding pattern (in-repo DC: none) |
| **chutesai/SageAttention-1** | Apache-2.0 | low | SKIP | GPU(planned) worker (runtime dep only) | S | (verbatim upstream mirror; no Chutes delta) |
| **DeepGEMM** | MIT | low | SKIP | none (transitive worker dep) | XL | (single-node CUDA kernels; no distributed surface) |
| **SaintDurbin (st_durbin)** | GPL-3.0 | low | SKIP | none (Bittensor settlement reference only) | S | (Solidity treasury; copyleft; domain mismatch) |
| **lua-oasvalidator** | MIT | none | SKIP | N/A | S | (Lua-over-C++ OAS validator; identical to upstream) |
| **hermes-agent** | MIT | none | SKIP | none | XL | (end-user agent app; no infra) |
| **TurboDiffusion** | Apache-2.0 | none | SKIP | none (containerized workload) | XL | (single-GPU CUDA diffusion; identical mirror) |
| **chutes-style** | UNKNOWN | none | SKIP | none | S | (brand/UI kit) |
| **n8n-nodes-chutes** | MIT | none | SKIP | none | S | (n8n client plugin) |
| **moltbot** | MIT | none | SKIP | none | XL | (single-user messaging gateway) |
| **Sign-in-with-Chutes** | MIT | none | SKIP | none (reference notes) | S | (OAuth client SDK; IDP/scope documentation) |
| **chutes-agent-toolkit** | UNKNOWN | none | SKIP | none | S | (empty placeholder repo) |

\* `graval`: MIT declared in `setup.py` but no standalone LICENSE file in the repo snapshot; treat as MIT-intent but confirm; the vendored `.so` binaries' provenance is separately unverified.

**Verdict counts (all 42):** PORT 2 · WRAP 2 · REFERENCE 27 · SKIP 11.
(One repo, `openclaw`, is DC=none but verdict REFERENCE for its OAuth-PKCE pattern; all other NONE-tier repos are SKIP.)

---

## Per-Gap Rollup

For each HelixCluster distributed-compute gap: the contributing chutesai repos (ranked by fidelity) and the recommended Helix action.

### Gap 1 — Scheduler / Placement (Omega, optimistic concurrency)

| Repo | Contribution |
|------|--------------|
| **chutes-miner** (Gepetto) | Production cost-aware GPU scheduler (`ORDER BY hourly_cost ASC, free_gpus ASC`) + value-multiplier preemption + optimistic reconcile loop |
| **chutes-api** (autoscaler) | Utilization/demand autoscaling with Redis distributed-lock single-writer + **bounty/auction economic placement** + NodeSelector hard constraints |
| **genlayer-studio** | Postgres `SELECT ... FOR UPDATE SKIP LOCKED` lease-free work-claiming + leader/appeal retry (optimistic-concurrency pattern) |
| **squad-api** | Kueue `suspend=True` + queue-label, suspend-then-admit, per-tier (free/paid) resource shapes |
| **chutes-autopilot** | Deterministic utilization-aware ranking with EWMA smoothing + tie-break determinism + stickiness (single-dimension) |
| **chutes** / **chutes-jumpmaster** | `NodeSelector` placement-intent schema (gpu_count, min_vram, price cap, include/exclude) |

**Recommended action:** PORT the Gepetto cost-minimizing + preemption-by-multiplier algorithm and the NodeSelector constraint model into `pkg/scheduler`/`pkg/resources` natively in Go; adopt `SKIP LOCKED`-equivalent optimistic claiming (etcd CAS / Postgres advisory) for Omega's optimistic-concurrency core; layer the bounty/auction signal as an optional economic-placement plugin for Phase 8/8B; borrow Kueue's suspend-then-admit + tiered-quota pattern for batch/agent jobs. Real integration + benchmark exit per CLAUDE-1.

### Gap 2 — Resources (node/GPU inventory)

| Repo | Contribution |
|------|--------------|
| **chutes-miner** | GPU inventory model (verified flag, model_short_ref, per-server free/used accounting, VRAM/RAM ratio rules) |
| **chutes-api** | One-row-per-physical-GPU device fingerprint (UUID, VRAM, SM major/minor, proc count, clock, ECC, SXM) + `SUPPORTED_GPUS` catalog with compute multipliers + hourly basis |
| **sek8s** | NVML/pynvml GPU enumeration + nvTrust evidence as the "attested GPU" resource shape |
| **ai-sdk-provider-chutes** | Cold-start/thermal visibility ({cold/warming/hot, instanceCount}) as a resource-readiness signal |

**Recommended action:** Implement a Go GPU resource type in `pkg/resources` using the chutes-api device-fingerprint schema + the compute-multiplier/hourly-cost catalog; gate "schedulable" on a verified/attested flag (see Gap 3); expose a thermal/readiness state so the scheduler pre-warms scale-from-zero backends.

### Gap 3 — GPU Validation / Attestation (software proof-of-GPU)

| Repo | Contribution |
|------|--------------|
| **graval** | The core software-attestation design: device-info challenge/response + matmul PoVW with cheap probabilistic spot-check + device-bound encrypt/decrypt + filesystem challenge (algorithm is a closed `.so`) |
| **chutes** | In-pod `GravalGpuVerifier` flow culminating in attestation-gated symmetric-key release; cfsv Merkle filesystem challenge + capacity proofs |
| **chutes-api** | Validator-side challenge/cipher generation (`graval_server.py`/`graval_worker.py`); per-Node seed; ≥95% VRAM gate |
| **chutes-miner** | Bootstrap verification (K8s Job runs GraVal, persists verified GPUs); per-GPU AES key derivation |
| **chutesai/sglang** | `hf_cache_verify.py` model-cache integrity (pure-Python, portable) as a software model-integrity admission gate |
| **cllmv** | Model/revision anti-cheat token contract (V2 HMAC-SHA256) + build-time engine hashing |

**Recommended action:** Build a NEW transparent `pkg/gpuattest` in Go + CUDA/OpenCL kernels that re-implements GraVal's design (device-fingerprint challenge → seeded matmul PoVW → O(1) spot-check verification → device-bound payload sealing) under Helix's own reputation/consensus — do NOT vendor the opaque `.so` (unauditable, fails CLAUDE-1). Port `hf_cache_verify` logic to Go as a model-integrity admission gate. Adopt cllmv's V2 (NOT V1/MD5) verification-token shape. Pair with hardware TEE (Gap 9) for strong confidentiality.

### Gap 4 — Federation (Phase 6, cross-cluster)

| Repo | Contribution |
|------|--------------|
| **chutes-miner** | Karmada multi-cluster aggregation (search-cache APIs) + Prometheus metric federation across per-node K3s clusters |
| **fiber** | Validator/miner identity model + on-chain stake-weighted admission + weight-setting/commit-reveal as a federation-settlement reference |
| **chutes-api** | Pattern of delegating membership/incentive consensus to an external layer while keeping orchestration centralized |
| **e2ee-proxy / chutes-e2ee-transport** | Consumer-side contract for negotiating with a remote dynamically-discovered compute pool (instance list + per-instance pubkey + single-use nonces) |

**Recommended action:** For Phase 6, model cross-cluster aggregation on Karmada's search-cache + metric-federation pattern but implement over Helix's SWIM+Raft+etcd rather than Karmada/K3s; use fiber's signed-identity + stake-gated admission as the federation trust protocol (replace Bittensor SS58 with Helix identity). Keep the instance-discovery + nonce-binding contract for talking to external Chutes-style subnets.

### Gap 5 — E2EE / Security (confidential transport, identity)

| Repo | Contribution |
|------|--------------|
| **chutes-e2ee-transport** | **PORT target** — complete client-side PQ envelope (ML-KEM-768 + HKDF-SHA256 + ChaCha20-Poly1305) with domain-separated info strings + per-request response keys + streaming |
| **e2ee-proxy** | The exact wire contract a GPU instance must decrypt (blob framing, info strings, single-use-nonce discovery, TEE gating) |
| **e2ee-test** | **PORT target (clean-room)** — byte-validatable reference (test vectors) for the same envelope + discovery/nonce protocol |
| **chutes-api** | Production X25519 ECDH + ChaCha20-Poly1305 per-instance session keys + hotkey-signed nonce-replay-protected RPC |
| **chutes** (Aegis) | Layered ECDH+AES-256-GCM session + post-quantum ML-KEM E2E + streaming AEAD; decrypt-in-middleware |
| **fiber** | RSA→Fernet hybrid handshake + signed single-use nonces + stake-gated admission (dependency-light reference) |
| **bittencert** | Keypair-rooted (no-CA) TLS verify-then-pin node-identity binding |
| **research-data-opt-in-proxy** | Spoof-proof managed-header sanitization + deterministic salted-SipHash anonymization + constant-time secret auth |
| **chutes-dropzone / chutes-n8n-local** | TEE-only catalog filter + nonce/pubkey discovery cache patterns |

**Recommended action:** PORT the PQ E2EE envelope into a single canonical `pkg/security/e2ee` (Go 1.24 `crypto/mlkem` + stdlib `crypto/hkdf` + stdlib `crypto/aes`/`crypto/cipher` + `x/crypto/chacha20poly1305`), validated byte-for-byte against e2ee-test vectors and a real confidential worker (CLAUDE-1: no mock-only). Implement the Helix worker-side decapsulation + enclave key custody. Add bittencert-style keypair-rooted identity for node mTLS; adopt the managed-header sanitization + anonymization primitives for any recording edge.

**AEAD-suite reconciliation (HXC-941, decided 2026-06-04).** This matrix originally specified ChaCha20-Poly1305 as the sole record AEAD (mirroring `chutes-api` + `chutes-e2ee-transport`). The implemented `security/pkg/e2ee` instead supports **both** AEAD suites behind one negotiable `SessionConfig.Suite` switch, and makes **AES-256-GCM the canonical default** with **ChaCha20-Poly1305 a fully-supported negotiable alternative**:

- **AES-256-GCM (default, `SuiteAES256GCM`, the zero value).** Hardware-accelerated on AES-NI / ARMv8-Crypto hardware, FIPS 197 / SP 800-38D standard, and already the wire AEAD used by the `chutes` (Aegis) layer's ECDH+AES-256-GCM session — so AES-GCM is *not* a deviation from the Chutes corpus, it matches the Aegis layer.
- **ChaCha20-Poly1305 (negotiable, `SuiteChaCha20Poly1305`, RFC 8439 IETF variant via `x/crypto/chacha20poly1305`).** Selected for non-AES-NI peers (cache-timing-resistant in software) and for byte-compatibility with the `chutes-api` / `chutes-e2ee-transport` ChaCha vectors. Both peers MUST select the same suite for Open to succeed.

Rationale: both ciphers use a 32-byte key + 12-byte nonce + 16-byte tag, so the suite choice is invisible to the wire framing. Keeping AES-GCM as default (rather than ripping it out for ChaCha-only) maximises interoperability + hardware acceleration on the common path while leaving the ChaCha path available for Chutes wire vectors and software-only peers. Both suites carry Known-Answer Tests against published vectors — AES-256-GCM against GCM-spec Test Case 16 (NIST SP 800-38D), ChaCha20-Poly1305 against RFC 8439 §2.8.2 — in `security/pkg/e2ee/kat_test.go` (`go test -run KAT ./pkg/e2ee/...`). **Follow-up:** when authoritative Chutes published wire vectors are imported into the repo (e2ee-test PORT), add them as additional KATs alongside the self-consistent RFC/NIST vectors.

### Gap 6 — LLM Routing / Serving (LLMOrchestrator)

| Repo | Contribution |
|------|--------------|
| **chutesai/vllm** | **WRAP** — inference engine backend (vanilla upstream) |
| **chutesai/sglang** | **WRAP** engine + portable model-integrity verification delta |
| **chutes-autopilot** | Utilization-aware ranking + streaming-safe "retry only before first byte" failover |
| **model-router** | Per-task ordered fallback + dedup/cap + empty/partial-response detection |
| **chutes-search** | Error-classified LLM failover taxonomy + ephemeral sandbox scheduling-hint contract |
| **research-data-opt-in-proxy** | Transparent proxy + per-request correlation-ID + trace-envelope unwrap (which node served this) |
| **claude-proxy / responses-proxy / codex** | API-shape adapters (Claude↔OpenAI, Responses↔Chat, provider rewriting + text tool-call fallback) + circuit breaker |
| **ai-sdk-provider-chutes** | Therm cold-start warmup + thermal monitor (pre-warm scale-from-zero) |
| **chutes** | Passthrough cords + disconnect-aware upstream abort (production streaming serving) |
| **SageAttention / DeepGEMM / TurboDiffusion** | Data-plane kernel acceleration (opaque worker deps, never in Go control plane) |

**Recommended action:** WRAP vLLM/SGLang as serving backends behind LLMOrchestrator (OpenAI HTTP). Reimplement in Go the ranking+streaming-safe-failover (autopilot), per-task fallback (model-router), thermal pre-warm (ai-sdk-provider), and correlation-ID/trace-unwrap audit trail (research-data-opt-in-proxy). Add API-shape adapters (Claude/Responses) only if those front doors are needed. Sink-side evidence (which node served the request) per CLAUDE-1.

### Gap 7 — Miner / Marketplace (Phase 8 / 8B)

| Repo | Contribution |
|------|--------------|
| **chutes-api** | End-to-end marketplace: registration, bounties, metering, payment, incentive-weighted payout |
| **chutes-miner** | Marketplace participant: validator allow-list, hourly-cost economics, bounty claiming, preemptible vs private tiers |
| **chutes** | Economic model: node selection → attestation → key release → metered hot/scale-to-zero billing → chute sharing |
| **chutes-audit** | Audit & reward-reconciliation half: time-weighted compute units, tamper-evident audit reports, independent reproducibility, miner self-report fraud detection |
| **fiber** | Stake-gated admission + weight-setting/commit-reveal settlement flow |
| **graval / cllmv** | Provider-must-prove-capability gate (GPU PoVW + model/revision verification) feeding placement/reward |

**Recommended action:** Design the Phase 8/8B `minermarket` submodule on chutes-api's registration→bounty→metering→payout loop; gate placement on `pkg/gpuattest` proofs (Gap 3) + TEE attestation (Gap 9); implement chutes-audit's commit-then-prove audit log + reproducible reconciliation + self-report cross-audit natively in Go; settle reputation over Helix Raft (replacing Bittensor weights) while keeping fiber's stake/commit-reveal as a design template.

### Gap 8 — Consensus / Discovery

| Repo | Contribution |
|------|--------------|
| **genlayer-studio** | Stake-weighted (VRF-style) validator selection + leader/appeal BFT state machine + `SKIP LOCKED` optimistic claiming (single-host simulation) |
| **chutes-api** / **fiber** / **chutes-audit** | Delegated on-chain consensus (Bittensor Yuma): score → U16 weight quantization → `set_weights` (contrast/reference, not a drop-in for Raft/SWIM) |
| **chutes-e2ee-transport / e2ee-proxy** | Instance-discovery fan-out (`/e2e/instances`) + single-use nonce binding as a placement/availability discovery pattern |
| **bittencert** | Identity-rooted discovery (prove an endpoint belongs to a specific keypair without a CA) |

**Recommended action:** Helix already has stronger real consensus (SWIM+Raft+etcd). Use genlayer-studio's leader/appeal + stake-weighted-selection as a *conceptual* comparison and lift its `SKIP LOCKED` optimistic-claiming idiom into the scheduler. Treat the Bittensor weight/commit-reveal path as the marketplace-settlement reference (Gap 7), not as a consensus engine. Adopt the instance-discovery + nonce-binding contract for endpoint discovery in confidential routing.

### Gap 9 — TEE / Confidential Compute

| Repo | Contribution |
|------|--------------|
| **sek8s** | The canonical reference: dual-root (Intel TDX + NVIDIA) attestation, nonce+cert-bound quotes, measured boot / RTMR3, attested LUKS key release, OPA/Rego admission + cosign |
| **chutes-api** | TDX quote parsing + RTMR measurement allow-listing + NVIDIA PPCIE verifier (validator side) |
| **chutes** | In-pod TDX evidence server binding validator nonce + E2E pubkey to a TDX quote before key release |
| **e2ee-proxy / chutes-e2ee-transport / e2ee-test** | The transport that terminates *inside* the enclave (TEE-gating policy + encryption-to-instance-pubkey) |
| **cllmv** | Software runtime-stack attestation (engine pkg hash) complementing hardware TEE |

**Recommended action:** Build a NEW `pkg/security/attestation` in Go re-implementing sek8s's TDX+NVIDIA quote generation/verification (report-data layout, RTMR3 measurement list, attested-key-release handshake, SS58→generic-signed-nonce auth) via go-tdx-guest / Intel DCAP + NVIDIA NRAS; add an admission gate (sek8s OPA/Rego + cosign pattern). Bind the E2EE envelope (Gap 5) to a verified quote so encryption terminates only inside attested enclaves. CLAUDE-1: validate against real TDX+confidential-GPU hardware, not mocks.

---

## Cross-Cutting Notes

- **The four CORE repos (chutes-api, chutes-miner, graval, chutes) are a single system viewed from four angles** — validator/orchestrator, miner/scheduler, attestation library, and in-pod runtime. They must be read together; no one of them is a complete spec. Helix should treat them as the master blueprint for the GPU-validation + scheduler + marketplace + E2EE quartet.
- **Two true PORT targets** (`chutes-e2ee-transport`, `e2ee-test`) plus the **e2ee-proxy / chutes-api / chutes** references converge on ONE protocol — implement it once in `pkg/security/e2ee` and reuse everywhere.
- **License blockers:** `e2ee-test`, `chutes-autopilot`, `responses-proxy`, `claude-proxy`, `research-data-opt-in-proxy`, `chutes-docs`, `chutes-jumpmaster`, `model-router`, `chutes-dropzone`, `chutes-n8n-local`, `chutes-style`, `chutes-agent-toolkit` are UNKNOWN-license → clean-room reimplementation only. `SaintDurbin` is GPL-3.0 → avoid entirely.
- **Opaque-binary caveat:** `graval`, `chutes` (Aegis/cfsv), and `cllmv` ship their security logic as closed `.so` blobs. Per CLAUDE-1, a wrapped opaque attestation binary cannot be end-to-end validated — Helix MUST reimplement transparently, not vendor.
