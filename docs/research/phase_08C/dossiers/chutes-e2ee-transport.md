# chutes-e2ee-transport
- **Repo:** https://github.com/chutesai/chutes-e2ee-transport
- **Language:** Python
- **License:** MIT
- **Maturity:** active
- **Distributed-Computing Relevance:** high
- **Portability Verdict:** PORT
- **Target Helix Module:** security (new submodule: `pkg/security/e2ee` or `pkg/transport/e2ee`), feeding LLMOrchestrator inference path

## Purpose
A client-side, post-quantum end-to-end-encrypted transport that plugs in as a drop-in `httpx` transport for the OpenAI Python SDK. It transparently encrypts inference requests/responses with ML-KEM-768 + HKDF-SHA256 + ChaCha20-Poly1305 so that neither the Chutes API relay nor any intermediary hop can read the payload — only the target GPU instance (running inside a TEE chute) ever sees plaintext.

## Capabilities
- Drop-in `httpx.BaseTransport` / `httpx.AsyncBaseTransport` implementations (`ChutesE2EETransport`, `AsyncChutesE2EETransport`) that intercept requests at the HTTP layer, invisible to the OpenAI SDK above them.
- Hybrid post-quantum crypto envelope: ML-KEM-768 (Kyber, NIST FIPS 203) KEM encapsulation → HKDF-SHA256 key derivation (salt = first 16 bytes of the KEM ciphertext, domain-separated `info` per direction) → ChaCha20-Poly1305 AEAD.
- Bidirectional encryption with separate ephemeral keypairs: the client generates a *response* ML-KEM keypair per request and ships the public key inside the encrypted request body, so responses are encrypted back to a one-time key (`INFO_REQ` / `INFO_RESP` / `INFO_STREAM` domain separation).
- gzip compression of the JSON payload before encryption.
- Transparent streaming (SSE) support: decrypts an `e2e_init` key-exchange event to derive a stream key, then decrypts each `e2e` SSE chunk on the fly, re-emitting standard `data: {...}` lines the OpenAI SDK parses normally; passes through plaintext `usage` events and surfaces `e2e_error` events.
- Instance discovery + nonce management: prefetches single-use nonces from `/e2e/instances/{chute_id}` (pools per instance), caches them with TTL/expiry tracking, consumes one nonce per request (replay-proof), and refreshes when exhausted/expired.
- Model-name → `chute_id` resolution via the `/v1/models` listing endpoint, cached with a 5-minute TTL; UUID inputs pass through unresolved.
- Thread-safe `DiscoveryManager` with per-chute nonce pools, locks, and split base URLs (`api.chutes.ai` for E2EE invoke/instances, `llm.chutes.ai` for model listing).
- Rewrites requests to `POST /e2e/invoke` with a binary `application/octet-stream` body and routing headers (`X-Chute-Id`, `X-Instance-Id`, `X-E2E-Nonce`, `X-E2E-Stream`, `X-E2E-Path`).

## Distributed-Computing Notes
- **E2EE inference transport (core focus):** This is the client half of a confidential-inference protocol. It is the canonical reference for how to do *zero-trust inference routing* through an untrusted relay to a specific GPU instance — the relay sees only ciphertext and routing metadata, never the prompt/completion.
- **TEE/confidential compute linkage:** Targets `*-TEE` models (e.g. `zai-org/GLM-4.7-TEE`). The threat model assumes the GPU instance runs in a trusted execution environment and holds the static ML-KEM secret key; the public key is distributed via `/e2e/instances`. This is the network-transport complement to TEE/sek8s attestation — encryption terminates *inside* the enclave, not at the relay.
- **Instance discovery / placement signal:** `/e2e/instances/{chute_id}` returns a set of live E2EE-capable instances each with an `instance_id`, an `e2e_pubkey`, and a nonce pool. This is effectively a placement/availability fan-out: the client picks an instance and binds the request to it via a single-use nonce. This maps directly onto how a scheduler advertises ready compute endpoints.
- **Replay protection via single-use nonces:** Nonces are server-issued, pooled per instance, time-bounded (default ~55s server expiry, 60s client TTL), and consumed exactly once. This is a lightweight distributed anti-replay scheme worth porting for any request-binding-to-node protocol.
- **No GPU validation / GraVal / Bittensor / gossip here:** This repo is purely the transport/crypto client. It contains no miner/validator logic, no weights/consensus, no fiber p2p, and no GraVal attestation — those live in sibling Chutes repos. Its distributed relevance is the *confidential routing + node-bound request* pattern, not consensus or scheduling proper.

## HelixCluster Gaps Addressed
- **security / E2EE (primary):** Helix has no end-to-end payload-encryption layer for inter-service or inference traffic that survives an untrusted intermediary. This provides a complete, working hybrid-PQC envelope design (KEM + HKDF + AEAD with domain separation and per-request response keys) that can be ported to Go for confidential RPC.
- **LLMOrchestrator inference path:** When Helix routes inference through a broker/relay to a backend serving node, this is the blueprint for making that relay zero-knowledge — encryption terminates at the worker, not the broker.
- **discovery / placement:** The `/e2e/instances` fan-out + nonce-binding pattern complements Helix's scheduler/Omega by giving a concrete protocol for binding a request to a specific advertised instance with replay protection.
- **federation (Phase 6) / miner-marketplace (Phase 8/8B):** In an untrusted multi-tenant GPU marketplace, this is exactly the transport you need so the marketplace operator/relay cannot read tenant prompts — strengthens the trust story for renting third-party GPUs.
- Does **not** address: GPU attestation/GraVal, Raft/leader consensus, Bittensor weights, gossip/SWIM, or scheduler core — out of scope for this repo.

## Dependencies
- `httpx` >= 0.25 (transport interface)
- `cryptography` >= 42.0 (HKDF-SHA256, ChaCha20-Poly1305)
- `pqcrypto` >= 0.1.0 (ML-KEM-768 / Kyber-768 KEM)
- Optional: `openai` >= 1.0 (only for SDK integration), `pytest`/`pytest-asyncio`/`ruff` (dev)
- Pure-Python, no CUDA, no native build beyond the `pqcrypto`/`cryptography` wheels.

## Rationale
Verdict **PORT** (not WRAP) because the value is the *protocol and crypto envelope*, which is small, self-contained (~3 source modules, ~600 LOC total), well-specified, and trivially reimplementable in Go. Go has first-class equivalents for every primitive: ML-KEM-768 via `crypto/mlkem` (Go 1.24, which Helix already targets) or `cloudflare/circl`, HKDF via `golang.org/x/crypto/hkdf`, and ChaCha20-Poly1305 via `golang.org/x/crypto/chacha20poly1305`. A Go port (`pkg/security/e2ee`) plus a matching server-side decapsulation in the Helix worker gives Helix native confidential inference with no Python runtime in the data path. Wrapping the Python lib as a sidecar would add a language boundary in a hot, latency-sensitive path for no benefit. The MIT license permits direct reuse/derivation.

## Risks
- **Language mismatch:** Source is Python; Helix is Go. Mitigated by the small surface and the fact that Go 1.24 ships `crypto/mlkem` natively — but the port must exactly match the wire format (KEM-ct sizes: 1088-byte ML-KEM-768 ciphertext, 16-byte AEAD tag, 12-byte nonce, gzip framing, HKDF salt = `mlkem_ct[:16]`, and the `e2e-req-v1`/`e2e-resp-v1`/`e2e-stream-v1` info strings) or it will not interoperate with Chutes' relay.
- **Server side is opaque:** Only the client half is in this repo. To use the *Chutes* service you depend on their `/e2e/invoke` + `/e2e/instances` endpoints; to use the *pattern* inside Helix you must implement the worker-side enclave key custody and decapsulation yourself.
- **Crypto correctness / agility:** A hand-ported AEAD/KEM envelope is security-critical — needs constant-time review, test vectors captured from this reference impl, and adherence to CLAUDE-1 (real end-to-end decryption tests against a live worker, not mocks, before declaring complete). The salt-from-ciphertext HKDF choice and single-use-nonce semantics must be preserved exactly.
- **`pqcrypto` maturity:** the upstream ML-KEM-768 dependency is early-version (0.1.x); if mirroring behavior, prefer the standardized FIPS-203 Go implementation rather than reproducing any quirks.
- **Fork drift / protocol versioning:** versioned `info` strings imply Chutes may rev the protocol (`-v1`); a Helix port pinned to v1 could drift from the live service over time. Low risk if used only as an internal pattern.
