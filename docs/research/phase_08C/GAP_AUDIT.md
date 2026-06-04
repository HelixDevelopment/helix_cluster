# Phase 8C — Code-Grounded Gap Audit

> **Honest completion: ~45% of Phase 8C deliverables.**
> Crypto/trust primitives (PQ E2EE envelope, software proof-of-GPU, software TEE attestation) are genuinely implemented with real-behavior tests in the `vasic-digital/security` submodule; scheduler cost/preemption and a fiber transport exist. But the higher pillars — LLMOrchestrator serving (still a stub), GPU resource model, model-integrity gate, marketplace/audit, inferenceproxy, admission — are MISSING, and the implemented primitives lack the Chutes byte-interop, O(1) spot-check, streaming, and attestation-gated scheduling the roadmap specifies.

Audited 2026-06-01 against actual code in `pkg/`, `internal/`, `cmd/`, and the `security/` submodule (`vasic-digital/security`). A deliverable is **DONE** only if implemented AND covered by real-behavior tests; stub/partial/untested → PARTIAL/MISSING.

## 3-Axis Package Status (Refreshed 2026-06-04, HXC-939)

> **Why this section exists:** "exists" ≠ "used". `wired` is **measured** via `go list -deps ./cmd/... | grep -Fx <module-path>/<pkg>` (module = `github.com/HelixDevelopment/helix_cluster`), not assumed. **"Completed (registry) ≠ wired"** — Completed only means source+tests exist; it does NOT prove a shipped binary reaches it.
>
> **STALENESS CORRECTION:** the 2026-06-01 table below marked `pkg/modelintegrity` (then `hf_cache_verify`), `pkg/inferenceproxy`, and `pkg/marketplace` as MISSING. As of 2026-06-04 **all three EXIST and are TESTED** — but **all three are ORPHANED** (not reachable from any `cmd/` binary). The registry would show them "Completed"; the measured `wired` column shows they are exists-but-unused. The rows below override the older prose.

| Package | implemented | wired (reachable from `cmd/`) | tested |
|---|:---:|:---:|:---:|
| `pkg/modelintegrity` | yes | **NO (orphaned)** | yes |
| `pkg/inferenceproxy` | yes | **NO (orphaned)** | yes |
| `pkg/marketplace` | yes | **NO (orphaned)** | yes |
| `pkg/fiber` (miner↔validator transport) | yes | **NO (orphaned)** | yes |
| `pkg/scheduler` (cost_gpu + preempt) | yes | yes | yes |
| `pkg/resources` (GPU model — no attested/multiplier yet) | yes | yes | yes |
| `internal/llm` (router — stub `Inference`) | yes | yes | yes |
| `security/pkg/e2ee` (separate module) | yes | **NO (not in helix cmd graph)** | yes |
| `security/pkg/gpuattest` (separate module) | yes | **NO (not in helix cmd graph)** | yes |
| `security/pkg/attestation` (separate module) | yes | **NO (not in helix cmd graph)** | yes |
| `security/pkg/admission` / signed-image gate | **NO (absent)** | n/a | n/a |

**Orphan callouts:** `pkg/modelintegrity`, `pkg/inferenceproxy`, `pkg/marketplace`, `pkg/fiber` are implemented+tested but reachable from no binary. They satisfy "the package exists" and would pass `go test`, but deliver zero end-user value until a binary (e.g. `internal/llm`'s `LoadModel`, or the gateway) imports them — exactly the gap the 3-axis model exposes that "Completed" hid.

## Deliverable Status Table

| Deliverable (roadmap §) | Status | Evidence (file:line) | Notes |
|---|---|---|---|
| 8C.1 `pkg/security/e2ee` PQ envelope (ML-KEM-768 + HKDF-SHA256 + AEAD) | **PARTIAL** | `security/pkg/e2ee/package.go:103-326`; tests `security/pkg/e2ee/package_test.go:37-360` (14 tests: tamper/nonce-reuse/AAD/wrong-key rejection) | Real handshake + AEAD + replay rejection. **Gaps vs spec:** AEAD is AES-256-GCM, roadmap §4.1 demands **ChaCha20-Poly1305**; no streaming, no gzip, no nonce/instance **discovery** contract, and **NO Chutes byte-interop vectors** (§8 exit gate "byte-for-byte decrypt vs reference vectors" unmet). |
| 8C.1 E2EE framed transport | **DONE** | `security/pkg/e2ee/transport.go:31-77`; `package_test.go:286-336` | Length-prefixed record framing over `io.ReadWriter`, oversize guard, round-trip + concurrent tests. Solid but minimal. |
| 8C.2 `pkg/gpuattest` software proof-of-GPU (challenge/response + fingerprint) | **PARTIAL** | `security/pkg/gpuattest/package.go:104-323`; tests `package_test.go:42-380` (13 tests: expired/replay/tamper/wrong-key/forged-descriptor rejection) | Honest, clean-room HMAC challenge/response with replay+expiry protection and explicit self-documented scope note (no GPU kernel). **Gaps:** no **matmul PoVW**, no **O(1) spot-check**, no **device-sealed E2EE** (§4.1). Cannot reject a real oversubscribed/spoofed GPU — only proves secret possession. |
| 8C.3 `pkg/security/attestation` TEE (TDX/NVIDIA quote, measured boot, key release) | **PARTIAL** | `security/pkg/attestation/attestation.go:1-346`; tests `attestation_test.go` (18 tests) | Real ed25519-signed document construct/verify with nonce freshness, clock-skew, and measurement-**policy** evaluation; honest "software stands in for hardware quote" seam (`QuoteSigner`). **Gaps:** no real TDX/NVIDIA quote, no attested **key release**, no measured-boot binding. Roadmap §8 hardware exit gate unmet (needs real hardware — DEFERRED). |
| 8C.3 `pkg/security/admission` (OPA-style policy + signed-image/cosign) | **MISSING** | `security/pkg/policy/policy.go` is generic rule eval — no admission, no cosign, no signed-image verify; no `admission` dir anywhere | Only a generic policy-rule framework exists; the workload-admission + image-signature gate is absent. |
| 8C.4 `pkg/scheduler` cost-aware placement | **DONE** | `pkg/scheduler/cost_gpu.go:28-130`; `cost_gpu_test.go` | Cost-aware GPU scoring plugin (explicit price labels + proxy), implements Plugin interface, tested. |
| 8C.4 Scheduler preemption + optimistic claiming | **PARTIAL** | `pkg/scheduler/gang_preempt.go:66-279` (`ScheduleGangOptimistic`, `SchedulePreemptOptimistic`, version-conflict) | Priority-based preemption + optimistic-concurrency version bump present & tested. **Gaps:** preemption is **priority**-based, not the roadmap's **value-multiplier** model; no `SKIP LOCKED`-style DB claiming; **no attestation-gated admission predicate** (§5.1 integration unmet — scheduler never consults gpuattest/attestation). |
| 8C.5 `pkg/resources` GPU type (fingerprint + compute-multiplier catalog + attested/thermal state) | **MISSING** | `pkg/resources/types.go:23-28` `GPUInfo{Count,Model,Memory}` only | No device fingerprint, no `SUPPORTED_GPUS` multiplier catalog, no verified/attested flag, no thermal/readiness state. `internal/gpu/gpu.go` has status enum + /proc detection but not the attested resource model. |
| 8C.6 LLMOrchestrator WRAP vLLM/SGLang + ranking/failover + thermal pre-warm + trace audit | **MISSING** | `internal/llm/manager.go:142-158` — `Inference` returns `"[stub inference from %s]"` | Honest self-labeled **stub**. No vLLM/SGLang HTTP backend, no OpenAI passthrough, no ranking, no failover, no pre-warm, no trace trail. This is the largest functional gap. |
| 8C.7 Model-integrity gate (`hf_cache_verify` → Go, SHA/size) | **MISSING** | no match for `hf_cache`/`ModelIntegrity`/`VerifyModel` in `pkg/`,`internal/`,`security/pkg/`. `security/pkg/content/` is text content-filtering, not model integrity | Roadmap P1 #7 ("cheap, high-value") — entirely absent. |
| 8C.7 `pkg/marketplace` + `/audit` (registration→bounty→metering→payout, commit-then-prove) | **MISSING** | no match for `bounty`/`payout`/`metering`/`Marketplace` in `pkg`,`internal`,`cmd` | Economic layer entirely absent. |
| `pkg/inferenceproxy` (correlation-ID, trace unwrap, anonymization) | **MISSING** | no match for `correlation`/`anonymiz`/`inferenceproxy` in `pkg`/`internal` | Absent. |
| `pkg/fiber` miner↔validator transport | **PARTIAL** | `pkg/fiber/fiber.go:1-35+`; `fiber_test.go` | Clean-room length-prefixed framed transport with Ping/Pong keepalive + 1-reader/1-writer contract, tested. **Gaps:** no signed identity / stake-gated admission, no E2EE underlay wired (only documented as future). |
| Node identity: verify-then-pin (TOFU) TLS + header sanitization | **MISSING** | no `pin`/`TOFU`/`VerifyThenPin` in `pkg/security/tls.go` or `security/pkg/security/` | Existing `pkg/security/{tls,spiffe,vault}.go` cover standard TLS/SPIFFE/Vault, not keypair-rooted verify-then-pin. |

## Top Implementable Gaps (Go, no new infra)

1. **Model-integrity gate → `pkg/modelintegrity/`** *(roadmap P1 #7, 0.5wk, highest value/effort ratio)*
   Port `hf_cache_verify` semantics: given a model directory and a signed manifest of `{relpath → sha256, size}`, walk the cache and verify every file's SHA-256 and byte size before a model is marked servable; reject on any mismatch/missing/extra file. Pure stdlib (`crypto/sha256`, `io`, `os`). Wire into `internal/llm/manager.go LoadModel` as a precondition. **Use only V2 HMAC-SHA256 semantics — never MD5 (risk §7 V1).**
   *Mutation-pairable acceptance test:* build a temp model dir + manifest; assert `Verify` passes; then (a) flip one byte → expect `ErrHashMismatch`, (b) truncate a file → expect `ErrSizeMismatch`, (c) delete a file → expect `ErrMissing`. Mutation: invert the `subtle.ConstantTimeCompare`/size check and confirm a test fails.

2. **Attestation-gated scheduler admission predicate → `pkg/scheduler/` (extend)** *(roadmap §5.1, P1)*
   Add an `AdmissionPredicate` hook on `Scheduler` (and a `Plugin.Filter` participant) that rejects a node unless it carries a verified attestation token. Define an interface `NodeTrust interface{ Verified(nodeID string) bool }` satisfied by `gpuattest.Verifier` / `attestation.Verifier` results; nodes failing verification are filtered out before scoring. No new infra — operates on existing `Node` labels + an injected verifier.
   *Mutation-pairable test:* two nodes, one with a valid gpuattest response registered, one spoofed; assert only the attested node is placeable. Mutation: make the predicate always-return-true and confirm the spoofed-node-rejected test fails.

3. **GPU resource model → `pkg/resources/` (extend `GPUInfo`)** *(roadmap 8C.5, P1)*
   Extend `GPUInfo` with `DeviceFingerprint string`, `ComputeMultiplier float64` (from a static `SUPPORTED_GPUS` catalog keyed by `Model`), `Attested bool`, and `Thermal{TempC int; Ready bool}`. Provide `LookupMultiplier(model string)` against a hardcoded catalog (H100/H200/A100/etc.) with a conservative default. Feed `ComputeMultiplier`/`Attested` into the cost scorer (#2 and `cost_gpu.go`).
   *Mutation-pairable test:* assert `LookupMultiplier("H100")` > `LookupMultiplier("A100")` > default; assert an un-attested GPU is scored below an attested one. Mutation: swap two catalog entries → ordering test fails.

4. **E2EE Chutes byte-interop vectors + ChaCha20-Poly1305 cipher option → `security/pkg/e2ee/`** *(roadmap §8 exit gate, P0)*
   Add a `testdata/` corpus of fixed (encap-key, KEM-ct, nonce, AAD, plaintext, expected-ciphertext) vectors and a vector-driven test asserting byte-for-byte `Seal`/`Open` reproduction; add a ChaCha20-Poly1305 AEAD variant (`golang.org/x/crypto/chacha20poly1305`) selectable via `SessionConfig`, matching the roadmap's stated cipher. Closes the "byte-for-byte vs reference vectors" exit gate without a live worker.
   *Mutation-pairable test:* table of golden vectors; assert exact ciphertext bytes. Mutation: perturb one HKDF info-label/salt byte in derivation → all golden-vector tests fail.

5. **gpuattest O(1) spot-check + device-sealed encrypt → `security/pkg/gpuattest/`** *(roadmap 8C.2, P0)*
   Add `SpotCheck(resp, index)` that re-derives and compares a single deterministic challenge-indexed word of the proof (O(1) verification of a large software-PoVW response) and a `SealForDevice(deviceKey, plaintext)`/`OpenFromDevice` pair binding an AEAD key to the device secret. Stays software-only and clearly scoped (no GPU kernel).
   *Mutation-pairable test:* genuine response passes spot-check at random indices; a response with one tampered word fails at that index. Mutation: make `SpotCheck` ignore `index` → tampered-word test fails.

6. **fiber signed-identity + stake-gated admission → `pkg/fiber/` (extend)** *(roadmap §9 Phase 6 hook)*
   Add an ed25519 handshake frame type carrying `{nodeID, pubkey, signed-nonce}` and an `Admit(func(nodeID, stake) bool)` gate run before application frames flow; reject unsigned/under-staked peers (stake supplied by caller, no chain). Reuses existing frame machinery.
   *Mutation-pairable test:* peer with valid signature + sufficient stake admitted; bad signature or zero stake rejected. Mutation: skip signature verification → bad-signature test fails.

7. **inferenceproxy correlation/anonymization → `pkg/inferenceproxy/`** *(roadmap P2 #10)*
   An `http.Handler` middleware that injects/propagates a correlation-ID header, records which backend served a request (audit trail), and deterministically anonymizes configured request headers/fields (keyed hash, stable per-tenant). Pure stdlib.
   *Mutation-pairable test:* same input header → same anonymized token across calls; different tenant → different token; correlation-ID present on response. Mutation: make anonymizer return input unchanged → "anonymized ≠ original" test fails.

## DEFERRED (need external infra / hardware / GPU kernels / non-Go)

- **8C.3 real TEE attestation (Intel TDX + NVIDIA confidential-GPU quotes, measured boot, attested key release).** Needs real TDX + H100/H200-class confidential-GPU hardware and vendor CA chains (`go-tdx-guest`/DCAP/NRAS). Software `QuoteSigner` seam exists; genuine quotes cannot be produced or end-to-end validated in CI without the hardware (roadmap §7, §8 hardware gate).
- **8C.2 hardware-rooted proof-of-GPU (real GraVal CUDA/OpenCL matmul kernel).** Requires GPU + CUDA/OpenCL kernel code (not Go) and a GPU CI runner to prove a spoofed/oversubscribed GPU is rejected (roadmap §8 GPU gate). The software spot-check (#5) is the CPU-CI-testable subset; full hardware validation is deferred.
- **8C.6 vLLM/SGLang runtime WRAP (live serving exit gate).** The Go ranking/failover/passthrough orchestration (gap #6-adjacent) is implementable, but the §8 "live confidential instance / real worker" exit gate requires running upstream vLLM/SGLang GPU containers — deferred to a GPU integration environment.
- **8C.7 marketplace settlement over real economic flow.** The Go data model + commit-then-prove audit logic is buildable on existing Postgres/etcd, but end-to-end payout/reward reconciliation as an exit gate depends on the (still-missing) serving + metering data plane above; defer the e2e settlement proof until #1/#3/#6 land.
