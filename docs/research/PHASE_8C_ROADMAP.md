# Helix Cluster OS — Phase 8C Roadmap: Porting Chutes AI Distributed-Computing Capabilities

> **Research Document** | Phase 8C Planning | 2026-05-31 (FINAL — corrected, complete picture, all 42 repos)
>
> This document defines how HelixCluster utilizes the `chutesai` open-source repositories to fill its distributed-compute gaps. It is grounded in the COMPLETE 42-repo survey — including the now-analyzed CORE/HIGH repos (`graval`, `chutes-miner`, `chutes-api`, `chutes`, `sek8s`, `fiber`, the E2EE transport stack) that the first pass missed. It sequences the actionable PORT/WRAP work by target module against Phase 8/8B (miner/marketplace), Phase 6 (federation), and the planned GPU/attestation modules.

---

## 1. Current State Summary

### Phases Completed (relevant lineage)

| Phase | Name | Status | Key Deliverables |
|-------|------|--------|------------------|
| **0–1** | Foundation + Core Infra | ✅ Complete | Submodules, CI/CD, docker-compose, pkg stubs, container orchestration |
| **2** | Console Nodes & Distributed Foundations | ✅ Complete | `pkg/swim`, `pkg/wireguard`, `pkg/discovery`, `pkg/leader`, `pkg/resources`, `pkg/scheduler`, `pkg/session` |
| **6** | Federation (planning) | 🔜 Roadmapped | Cross-cluster membership/scheduling |
| **8 / 8B** | Miner / Marketplace (planning) | 🔜 Roadmapped | Decentralized GPU marketplace |

### Phase 8C Research Artifacts (this set)

| Document | Location | Contents |
|----------|----------|----------|
| `CHUTESAI_REPO_ANALYSIS.md` | `docs/research/phase_08C/` | Per-repo analysis of all 42 chutesai repos (CORE→NONE) |
| `INCORPORATION_MATRIX.md` | `docs/research/phase_08C/` | Master decision table + per-gap rollup (9 gaps) |
| `dossiers/*.md` | `docs/research/phase_08C/dossiers/` | 20 deep-dive dossiers (the re-analyzed CORE/HIGH/MEDIUM repos) |

### Corrected Picture (vs the first partial pass)

The first synthesis (22 repos) wrongly concluded "no CORE repos." With the 20 failed agents now recovered, the CORE/HIGH substance is present and changes the plan materially:

| Capability | Canonical chutesai reference | Earlier (wrong) status |
|------------|-----------------------------|------------------------|
| Software GPU validation (proof-of-GPU) | **graval** + chutes + chutes-api | "out of scope / not in set" |
| Cost-aware GPU scheduler + preemption | **chutes-miner** (Gepetto) | "lives in repos not in this set" |
| Validator/orchestrator control plane | **chutes-api** | "documented only in chutes-docs" |
| Hardware TEE attestation (TDX+NVIDIA) | **sek8s** | "out of scope" |
| Secure miner↔validator p2p transport | **fiber** | "out of scope" |
| PQ E2EE inference transport | **chutes-e2ee-transport** / **e2ee-proxy** / e2ee-test | partial (e2ee-test only) |

---

## 2. Phase 8C Scope & Goals

### Primary Objective

Convert the Chutes AI distributed-GPU-compute blueprint into **native Go HelixCluster capabilities** — GPU validation/attestation, TEE-gated confidential serving, post-quantum E2EE inference transport, cost-aware GPU scheduling/preemption, and a decentralized miner/marketplace — without importing Python/CUDA/closed-`.so`/Bittensor dependencies.

### Three Pillars

| Pillar | Description | Outcome |
|--------|-------------|---------|
| **Trust the GPU** | Port GraVal-style software proof-of-GPU + sek8s-style TEE attestation | Schedule work onto untrusted/edge GPUs with cryptographic proof |
| **Confidential Serving** | Port the PQ E2EE inference envelope (ML-KEM-768 + AES-256-GCM default / ChaCha20-Poly1305 negotiable; see HXC-941) terminating inside attested enclaves | Tenant prompts unreadable by relay/miner operators |
| **Decentralized Marketplace** | Port Gepetto scheduling/preemption + chutes-api/chutes-audit economics into Phase 8/8B | Cost-aware placement + auditable, reproducible reward settlement |

### Decoupled Public Submodules (planned `vasic-digital` repos)

Several deliverables are general-purpose and will be created as **new PUBLIC `vasic-digital` GitHub repos**, consumed by Helix as submodules:

| Planned PUBLIC repo (vasic-digital) | Purpose |
|-------------------------------------|---------|
| `pkg/security/e2ee` **(planned)** | PQ E2EE inference transport (ML-KEM-768 + HKDF + AES-256-GCM default / ChaCha20-Poly1305 negotiable; see HXC-941), client + worker |
| `pkg/gpuattest` **(planned)** | Software GPU attestation (GraVal-style PoVW) — Go + CUDA/OpenCL kernels |
| `pkg/security/attestation` **(planned)** | Hardware TEE attestation (Intel TDX + NVIDIA), measured-boot/key-release |
| `pkg/security/admission` **(planned)** | Workload admission (OPA-style policy + signed-image verification) |
| `pkg/inferenceproxy` **(planned)** | Inference observability proxy (correlation-ID, trace-unwrap, anonymization) |

---

## 3. Phase 8C Sub-Phases

| Sub-Phase | Name | Weeks | Tasks | Goal |
|-----------|------|-------|-------|------|
| **8C.1** | E2EE Transport Port | 2 | 25 | `pkg/security/e2ee` — PQ envelope + discovery/nonce, byte-interop with Chutes |
| **8C.2** | GPU Attestation (software) | 3 | 30 | `pkg/gpuattest` — device-fingerprint challenge + matmul PoVW + spot-check + device-sealed E2EE |
| **8C.3** | TEE Attestation (hardware) | 3 | 30 | `pkg/security/attestation` + `admission` — TDX/NVIDIA quotes, measured boot, attested key release |
| **8C.4** | Scheduler Port (Gepetto) | 2 | 25 | Cost-aware placement + value-multiplier preemption + `SKIP LOCKED`-style optimistic claiming into `pkg/scheduler` |
| **8C.5** | GPU Resource Model | 1 | 15 | `pkg/resources` GPU type (device fingerprint, compute-multiplier catalog, attested/thermal state) |
| **8C.6** | LLMOrchestrator Routing/Serving | 2 | 20 | WRAP vLLM/SGLang + Go ranking/failover + thermal pre-warm + trace audit trail |
| **8C.7** | Miner / Marketplace + Audit | 2 | 25 | Phase 8/8B registration→bounty→metering→payout + commit-then-prove audit |

**Total: ~15 weeks | ~170 tasks | ~680 person-hours**

---

## 4. Package Breakdown

### 4.1 New pkg/ Packages (Phase 8C)

| Package | Purpose | Integration Point | Source Reference |
|---------|---------|-------------------|------------------|
| `pkg/security/e2ee` **(planned PUBLIC repo)** | PQ E2EE envelope (ML-KEM-768 + HKDF-SHA256 + AES-256-GCM default / ChaCha20-Poly1305 negotiable; see HXC-941) + streaming + nonce discovery | LLMOrchestrator, federation | chutes-e2ee-transport (PORT), e2ee-proxy, e2ee-test |
| `pkg/gpuattest` **(planned PUBLIC repo)** | Software proof-of-GPU: device-info challenge + matmul PoVW + O(1) spot-check + device-sealed encrypt | `pkg/scheduler`, `pkg/resources` | graval, chutes, chutes-api |
| `pkg/security/attestation` **(planned PUBLIC repo)** | Hardware TEE: TDX/NVIDIA quote gen+verify, measured boot, attested key release | scheduler placement gating, GPU | sek8s, chutes-api, chutes |
| `pkg/security/admission` **(planned PUBLIC repo)** | Policy admission + signed-image (cosign-style) + model-integrity gate | control-plane edge | sek8s (OPA/Rego), sglang (hf_cache_verify) |
| `pkg/attestation` (model integrity) | Port of `hf_cache_verify` — model-cache SHA/size verification before serving | LLMOrchestrator, marketplace | chutesai/sglang, cllmv (V2) |
| `pkg/inferenceproxy` **(planned PUBLIC repo)** | Transparent proxy: correlation-ID, trace-envelope unwrap, deterministic anonymization | LLMOrchestrator observability | research-data-opt-in-proxy |
| `pkg/marketplace` / `pkg/marketplace/audit` | Registration/bounty/metering/payout + commit-then-prove audit + reproducible reconciliation | Phase 8/8B | chutes-api, chutes-miner, chutes-audit, fiber |

### 4.2 Extended Existing Packages

| Package | Extension | Source Reference |
|---------|-----------|------------------|
| `pkg/scheduler` | Cost-aware placement (`hourly_cost ASC, free_gpus ASC`) + value-multiplier preemption + optimistic claiming + attestation-gated admission predicate | chutes-miner (Gepetto), chutes-api (autoscaler/bounty), genlayer-studio (`SKIP LOCKED`), squad-api (Kueue) |
| `pkg/resources` | GPU resource type: device fingerprint + `SUPPORTED_GPUS` compute-multiplier catalog + verified/attested flag + thermal/readiness state | chutes-api, chutes-miner, ai-sdk-provider-chutes |
| LLMOrchestrator | WRAP vLLM/SGLang; ranking + streaming-safe failover; per-task fallback; thermal pre-warm; passthrough/disconnect-abort | vllm, sglang, chutes-autopilot, model-router, chutes-search, chutes, ai-sdk-provider-chutes |
| security (node identity) | Keypair-rooted (no-CA) TLS verify-then-pin; managed-header sanitization | bittencert, research-data-opt-in-proxy |

### 4.3 WRAP-only Runtime Artifacts (not Go modules)

| Artifact | Role | Source |
|----------|------|--------|
| vLLM container (upstream) | Inference engine behind LLMOrchestrator (OpenAI HTTP) | chutesai/vllm |
| SGLang container | Inference engine + verification delta | chutesai/sglang |
| SageAttention wheels | GPU kernel acceleration inside the Python worker (data-plane) | chutesai/SageAttention |

---

## 5. Integration Points with Prior Phases

### 5.1 Scheduler ← GPU Attestation

```
pkg/gpuattest (8C.2) ──► pkg/scheduler (Phase 2 + 8C.4)
  software PoVW result    │   admission predicate (only attested GPUs schedulable)
pkg/security/attestation  │   node-trust label / reputation score
  (8C.3, TEE)             └── pkg/resources verified/attested flag
```

### 5.2 LLMOrchestrator ← E2EE Transport ← TEE

```
pkg/security/e2ee (8C.1) ── encryption terminates inside ──► pkg/security/attestation (8C.3)
  ML-KEM-768 envelope         the attested enclave              TDX/NVIDIA quote verify
        │
        └──► LLMOrchestrator (8C.6) routes confidential inference to verified instances
```

### 5.3 Marketplace ← all of the above (Phase 8/8B)

```
pkg/marketplace (8C.7)
  registration ─► pkg/gpuattest + pkg/security/attestation (prove capability)
  placement   ─► pkg/scheduler (cost-aware + preemption)
  metering    ─► pkg/marketplace/audit (commit-then-prove, reproducible payout)
  settlement  ─► Helix Raft reputation (replaces Bittensor weights; fiber as template)
```

---

## 6. Priority Ordering

### P0 — Critical Path (Trust + Confidentiality foundations)

| # | Task | Effort | Reason | Source |
|---|------|--------|--------|--------|
| 1 | `pkg/security/e2ee` PQ envelope (PORT) | 2 weeks | Confidential serving prerequisite; clean PORT target, Go 1.24 has all primitives | chutes-e2ee-transport, e2ee-test |
| 2 | `pkg/gpuattest` software proof-of-GPU | 3 weeks | "Trust the GPU" core; nothing else can gate untrusted GPUs without it | graval, chutes, chutes-api |
| 3 | `pkg/security/attestation` TEE (TDX+NVIDIA) | 3 weeks | Strong confidentiality root; binds E2EE to attested hardware | sek8s, chutes-api |

### P1 — Essential for Phase 8C MVP

| # | Task | Effort | Reason | Source |
|---|------|--------|--------|--------|
| 4 | `pkg/scheduler` cost-aware + preemption | 2 weeks | Decentralized placement; attestation-gated admission | chutes-miner (Gepetto), chutes-api |
| 5 | `pkg/resources` GPU type + catalog | 1 week | Scheduler needs attested GPU inventory | chutes-api, chutes-miner |
| 6 | LLMOrchestrator WRAP + routing/failover | 2 weeks | Real serving backends + production routing | vllm, sglang, autopilot, model-router |
| 7 | Model-integrity admission (`hf_cache_verify` → Go) | 0.5 week | Cheap, high-value marketplace trust gate | sglang, cllmv (V2) |

### P2 — Important but Can Be Deferred

| # | Task | Effort | Reason | Source |
|---|------|--------|--------|--------|
| 8 | `pkg/marketplace` + `audit` (Phase 8/8B) | 2 weeks | Economic layer; depends on P0/P1 | chutes-api, chutes-audit, fiber |
| 9 | `pkg/security/admission` (policy + cosign) | 1 week | Hardening; needs a k8s-compatible control plane | sek8s |
| 10 | `pkg/inferenceproxy` observability | 1 week | Audit trail (which node served the request) | research-data-opt-in-proxy |
| 11 | Federation E2EE/discovery hooks (Phase 6) | 1 week | Cross-cluster confidential routing | fiber, e2ee-proxy, chutes-miner (Karmada) |

---

## 7. Risk Analysis

| Risk | Probability | Impact | Mitigation |
|------|-------------|--------|------------|
| Opaque `.so` cores (graval/aegis/cllmv) cannot be ported | High | High | Reimplement transparently from observable protocols; never vendor unauditable attestation binaries (CLAUDE-1) |
| E2EE wire-format mismatch breaks Chutes interop | Medium | Medium | Capture e2ee-test vectors; byte-for-byte interop tests vs live `/e2e/invoke`; match KEM-ct/nonce/tag/gzip/HKDF-salt/info-strings exactly |
| TEE port needs real TDX + confidential-GPU hardware | High | High | Provision an Intel TDX + H100/H200-class test node; no mock-only validation (CLAUDE-1); graceful degrade where hardware absent |
| Bittensor coupling leaks into ports (SS58/weights/chain) | Medium | Medium | Strip to generic signed-nonce auth + Helix Raft reputation; keep only the validation/scoring math |
| GPU CI: no GPUs in pipeline | High | Medium | Software-attest paths gated behind a GPU job runner; CPU CI runs protocol/vector tests only |
| UNKNOWN-license repos contaminate the port | Medium | High | Clean-room reimplement from wire/spec for e2ee-test/autopilot/responses-proxy/etc; avoid GPL-3.0 SaintDurbin entirely |
| V1 (MD5) verification semantics copied by mistake | Low | High | Port ONLY cllmv V2 (HMAC-SHA256); V1 is cryptographically broken |
| `-TEE` name-suffix mistaken for attestation (PASS-bluff) | Medium | High | Never treat a model-name suffix as a trust signal; require a verified quote (CLAUDE-1) |

---

## 8. Success Criteria (Phase 8C Exit Gates)

| KPI | Target | Measurement |
|-----|--------|-------------|
| E2EE interop | Byte-for-byte decrypt success vs reference vectors AND a live confidential instance | Interop test (real worker, not mock) |
| GPU attestation | Spoofed/oversubscribed GPU rejected; genuine GPU passes; spot-check O(1) | End-to-end test on real GPU node |
| TEE attestation | Tampered node fails admission; genuine TDX+NVIDIA node releases key | Hardware integration test |
| Scheduling throughput | ≥10 placement decisions/sec with attestation gate | Benchmark |
| Confidential serving | Relay/proxy observes only ciphertext + routing metadata | Packet/log capture (sink-side evidence) |
| Model-integrity gate | Tampered model cache blocks serving; clean cache serves | E2E test |
| Test coverage (new pkg/) | >60% line coverage | Codecov |
| **CLAUDE-1 usability exit gate** | **Every feature proven end-to-end for an end user with sink-side evidence; real integration (no mock-only) for E2EE/attestation/scheduling; Challenge (HelixQA) PASS reflects genuine usability, not a PASS-bluff** | Captured screenshot/log/metrics per feature; integration tests against REAL services/hardware; no `-TEE`-suffix or other illusory trust signals accepted |

> **CLAUDE-1 emphasis:** Per `§7.1 + §11.4.39`, tests are necessary but NOT sufficient. For the attestation/E2EE/scheduler features in this phase: unit tests (with mutation), integration tests against REAL GPU/TEE hardware and live confidential instances, and end-to-end tests exercising the feature as an end user would, with captured sink-side evidence (the relay sees only ciphertext; the scheduler actually rejects a spoofed GPU; the tampered node actually fails to boot/admit). A passing test on a non-functional attestation path is a PASS-bluff of §7.1 severity.

---

## 9. Bridge to Phase 6 / 8 / 8B

### Phase 6: Federation

| Hook | Deliverable from 8C |
|------|---------------------|
| Cross-cluster confidential routing | `pkg/security/e2ee` + instance-discovery/nonce contract |
| Federation trust protocol | fiber-style signed identity + stake-gated admission (Helix identity, not SS58) |
| Multi-cluster aggregation | Karmada-pattern aggregation reimplemented over SWIM+Raft+etcd |

### Phase 8 / 8B: Miner / Marketplace

| Hook | Deliverable from 8C |
|------|---------------------|
| Provider-must-prove-capability gate | `pkg/gpuattest` + `pkg/security/attestation` |
| Cost-aware economic placement | `pkg/scheduler` (Gepetto port) + bounty/auction plugin |
| Auditable reward settlement | `pkg/marketplace/audit` (commit-then-prove, reproducible) over Helix Raft reputation |

Phase 8C provides the **trust, confidentiality, and scheduling primitives** that Phase 6 extends with **cross-cluster federation** and Phase 8/8B extends with **marketplace economics and settlement**.

---

## 10. References

1. `docs/research/phase_08C/CHUTESAI_REPO_ANALYSIS.md` — per-repo analysis (all 42)
2. `docs/research/phase_08C/INCORPORATION_MATRIX.md` — decision table + 9-gap rollup
3. `docs/research/phase_08C/dossiers/` — 20 deep-dive dossiers (CORE/HIGH/MEDIUM repos)
4. `docs/research/PHASE_2_ROADMAP.md` — format reference + Phase 2 packages (swim/wireguard/discovery/leader/resources/scheduler/session)
5. `docs/research/PHASE_6_ROADMAP.md` — federation (next-after) target for E2EE/discovery hooks
6. `docs/research/PHASE_8_ROADMAP.md`, `PHASE_8B_ROADMAP.md` — miner/marketplace targets
7. Source repos: chutesai/{graval, chutes-miner, chutes-api, chutes, sek8s, fiber, chutes-e2ee-transport, e2ee-proxy, e2ee-test, sglang, vllm, chutes-audit, cllmv, genlayer-studio, squad-api, chutes-autopilot, research-data-opt-in-proxy}
8. `CLAUDE.md` §CLAUDE-1 — End-User Usability Guarantee (usability exit gate above)
9. Go 1.24+ stdlib `crypto/mlkem` + `crypto/hkdf` + `crypto/aes`/`crypto/cipher` (AES-256-GCM default) + `golang.org/x/crypto/chacha20poly1305` (ChaCha20-Poly1305 negotiable; see HXC-941) — E2EE primitives
10. go-tdx-guest / Intel DCAP + NVIDIA NRAS — TEE attestation primitives for `pkg/security/attestation`
