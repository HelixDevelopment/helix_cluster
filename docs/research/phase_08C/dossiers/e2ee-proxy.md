# e2ee-proxy
- **Repo:** https://github.com/chutesai/e2ee-proxy
- **Language:** Lua (OpenResty/LuaJIT) + C (native crypto lib) + a little Python (tests)
- **License:** MIT
- **Maturity:** active (production-intent; embedded cert/TEE-gating logic, not a fork)
- **Distributed-Computing Relevance:** high
- **Portability Verdict:** REFERENCE
- **Target Helix Module:** LLMOrchestrator (E2EE inference transport) + security (post-quantum AEAD envelope, TEE attestation gating); NEW submodule `pkg/llm/e2ee`

## Purpose
An OpenResty (nginx + LuaJIT) reverse proxy that gives clients drop-in OpenAI/Anthropic/Responses API compatibility while transparently end-to-end-encrypting each inference request so that only the target GPU instance (running in a TEE) can decrypt it. It performs format translation, per-request post-quantum key exchange (ML-KEM-768), AEAD encryption (ChaCha20-Poly1305), streaming SSE decryption, and refuses to talk to any model not flagged `confidential_compute: true`.

## Capabilities
- Drop-in API compatibility: `/v1/chat/completions`, `/v1/completions`, `/v1/messages` (Claude Messages), `/v1/responses` (OpenAI Responses), all translated to chat-completions before encryption; `/v1/models` is plain passthrough to `llm.chutes.ai`; `/health` and CORS preflight.
- Per-request E2EE envelope: resolve model→`chute_id` via `/v1/models`, fetch an available GPU instance + single-use nonce from `api.chutes.ai/e2e/instances/<chute_id>`, generate an ephemeral ML-KEM-768 keypair, encapsulate a shared secret against the instance's public key, derive symmetric keys via HKDF-SHA256, gzip-compress, ChaCha20-Poly1305 seal, POST the blob to `api.chutes.ai/e2e/invoke`.
- Forward secrecy: a fresh ephemeral ML-KEM keypair per request; the client's response secret key (`e2e_response_pk`) is injected into the encrypted payload so the instance can encrypt the reply back.
- Streaming decryption: parses SSE, handles an `e2e_init` event (decapsulates a per-stream key) then decrypts each `e2e` chunk independently with ChaCha20-Poly1305; passes through `usage` events and surfaces `e2e_error`.
- TEE enforcement: by default rejects any model whose `/v1/models` entry lacks `confidential_compute: true` (typically `-TEE`-suffixed models), with an `ALLOW_NON_CONFIDENTIAL=true` override for testing.
- Native crypto in a hardened, symbol-stripped shared library (`libe2ee_proxy.so`) loaded via LuaJIT FFI; exposes ML-KEM keygen/encap/decap, HKDF-SHA256, ChaCha20-Poly1305 seal/open, gzip, CSPRNG, and DER cert/key retrieval.
- Embedded TLS cert/key inside the obfuscated `.so` to evade Certificate Transparency scanners (key never touches disk); plus self-signed and bring-your-own-cert modes via env vars.
- Nonce/instance caching and retry: module-level nonce cache with TTL (`nonce_expires_in`, default 55s), single-use nonce consumption per instance, automatic invalidation + one retry on 403 nonce-rejection.

## Distributed-Computing Notes
- **E2EE inference transport (core artifact):** This is the client-edge of Chutes' confidential inference path. The protocol (ML-KEM-768 + HKDF-SHA256 + ChaCha20-Poly1305 + gzip, blob layout `mlkem_ct(1088) | nonce(12) | ciphertext(N) | tag(16)`) is the exact wire contract a GPU instance must implement to decrypt. Info strings `e2e-req-v1`/`e2e-resp-v1`/`e2e-stream-v1` namespace the three key-derivation contexts.
- **TEE / confidential compute gating:** Enforces that inference only flows to instances advertising `confidential_compute`. This is the policy hook that ties E2EE to attested hardware — without a TEE, an operator could dump decrypted memory. This connects conceptually to the sek8s/GraVal attestation story in the broader Chutes stack, though attestation itself is performed upstream (Chutes API decides which instances are confidential); this proxy only trusts the `confidential_compute` flag from `/v1/models`.
- **Instance discovery / placement (consumer side):** `e2e_discovery.lua` is a thin client of Chutes' placement service: it asks `api.chutes.ai/e2e/instances/<chute_id>` for the set of live instances, each with an `e2e_pubkey` and a pool of single-use nonces. The proxy does NOT do scheduling/placement itself — it consumes the result of upstream miner/validator scheduling. Useful as a model of how a routing edge negotiates with a decentralized GPU pool.
- **Post-quantum forward secrecy at scale:** ephemeral-per-request ML-KEM keypairs and single-use nonces are a concrete pattern for high-throughput confidential serving without long-lived session keys.
- **No p2p/gossip/consensus/weights here.** This repo is strictly the encryption + API-translation edge. Bittensor subnet logic, GraVal GPU validation, fiber gossip, and sek8s TEE orchestration live in sibling Chutes repos, not here.

## HelixCluster Gaps Addressed
- **LLMOrchestrator / inference serving:** Helix's LLM orchestration currently has no confidential-transport story. This supplies a complete, battle-tested protocol design for E2EE request/response and streaming over an untrusted network to attested compute — directly portable as a design spec for a Helix `pkg/llm/e2ee` envelope and a confidential-serving mode.
- **security / E2EE:** Provides a concrete post-quantum AEAD envelope (ML-KEM-768 + HKDF + ChaCha20-Poly1305) and a TEE-gating policy that Helix's security layer can adopt for any node-to-node confidential channel, not just LLM traffic.
- **federation (Phase 6) / discovery:** Models the consumer-side contract for negotiating with a remote, dynamically-discovered pool of compute instances (instance list + per-instance pubkey + single-use nonces). Informs how a Helix federation edge would talk to an external Chutes-style subnet.
- **miner/marketplace (Phase 8/8B):** Demonstrates the request path a Helix-hosted "buyer" edge would use to consume decentralized GPU compute from a Chutes subnet while keeping payloads private from the miner operator.
- Does NOT address: Omega scheduler/placement, GPU validation/attestation internals, leader/consensus, or EventBus/Messaging — those are out of scope for this proxy.

## Dependencies
- OpenResty / nginx + LuaJIT; Lua libs: `lua-resty-http`, `ngx.ssl`, `ngx.base64`, `cjson`/`cjson.safe`, LuaJIT `ffi`.
- Native: a custom C shared library `libe2ee_proxy.so` built from an ML-KEM (Kyber, `KYBER_K=3`) C implementation + `secure_crypto.c`, linked with `-lz -lpthread`; certs embedded via generated header.
- Build/protection toolchain: clang/LLVM `opt` with an out-of-tree obfuscation plugin (`xVMP`/`libxVMPPasses.so`: strvirt, vmp, autocff, region virtualization, anti-emu) and a custom packer (`xvmp_pack_so`). These tooling deps are NOT in the repo — the prebuilt `.so` is required, or the obfuscation step must be skipped.
- Docker (`parachutes/e2ee-proxy:latest`), `entrypoint.sh` templating nginx.conf (`__RESOLVERS__`, `__SERVER_NAME__`).
- Tests: Python (`tests/test_claude_api.py`, `tests/test_responses_api.py`) exercising the proxy via the OpenAI/Anthropic SDKs.

## Rationale
REFERENCE, not PORT/WRAP. The implementation language (LuaJIT FFI inside OpenResty) and the hardened/obfuscated native crypto blob are a poor fit for a Go cluster OS — Helix would not embed an OpenResty process or an xVMP-packed `.so`. However, the *protocol* is extremely valuable and cleanly specified: the exact KEM/KDF/AEAD choices, blob framing, streaming key-init handshake, info-string domain separation, single-use-nonce discovery contract, and TEE-gating policy are all directly reusable as the design for a native-Go Helix E2EE inference transport (Go has `crypto/cdsa`-grade ML-KEM via `crypto/mlkem` in Go 1.24+, plus `golang.org/x/crypto/chacha20poly1305` and `hkdf`). Re-implementing this envelope in Go is straightforward (S–M) and avoids dragging in OpenResty. WRAP (running the container as a sidecar) is a fallback if Helix needs Chutes interop quickly, but it inherits the obfuscation/cert-embedding operational burden. Per CLAUDE-1, any Helix adoption must be validated end-to-end against a real confidential instance, not mocked — a passing crypto unit test does not prove a real GPU instance can decrypt the envelope.

## Risks
- **Language mismatch:** Lua/OpenResty + LuaJIT FFI is alien to Helix's Go stack; porting means re-implementing, not linking.
- **Hardened native blob:** `libe2ee_proxy.so` is built with a proprietary, out-of-tree LLVM obfuscation plugin (xVMP) and packer that are NOT in the repo; you cannot reproduce the protected build from this repo alone, and the embedded TLS key makes the prebuilt artifact security-sensitive and non-redistributable in practice.
- **Tight coupling to Chutes endpoints:** Hardcoded `api.chutes.ai` / `llm.chutes.ai`, the `/e2e/instances` and `/e2e/invoke` contracts, the `chute_id`/`instance_id`/nonce headers, and the `confidential_compute` flag are all Chutes-specific; the server side of the protocol is not in this repo, so it cannot be exercised standalone without Chutes.
- **TEE trust is delegated, not verified here:** the proxy trusts the upstream `confidential_compute` flag; real attestation (GraVal/sek8s) happens elsewhere. Helix must not assume this proxy proves anything about hardware.
- **Crypto correctness on port:** ML-KEM sizes (pk 1184 / sk 2400 / ct 1088 / ss 32), HKDF salt = first 16 bytes of the KEM ciphertext, and per-context info strings must be matched exactly or interop silently fails — high-precision re-implementation required.
- **License is clean (MIT)** — no legal barrier to porting the protocol design.
