# cllmv
- **Repo:** https://github.com/chutesai/cllmv
- **Language:** Python
- **License:** MIT
- **Maturity:** experimental
- **Distributed-Computing Relevance:** medium
- **Portability Verdict:** REFERENCE
- **Target Helix Module:** LLMOrchestrator (inference verification submodule) / security (attestation handshake)

## Purpose
`cllmv` (Chutes LLM Verification) is a thin Python `ctypes` shim that loads a proprietary native shared library, `/usr/local/lib/chutes-aegis.so`, and exposes inference-verification primitives used by the Chutes/Bittensor subnet to cryptographically prove that a miner actually ran the claimed LLM model/revision rather than faking responses. It provides token generation (miner side), token validation (validator side), an X25519-based session-key handshake, and build-time hashing of the installed inference engine (sglang/vllm).

## Capabilities
- **`generate(id, created, value) -> 32-hex token`**: miner-side computation of a per-completion verification token bound to the request id, creation timestamp, and the response value.
- **`validate(...) -> bool` (V1)**: validator-side check of a 32-hex MD5 "interleaving" hash over `(id, created, value, salt, model, revision)`.
- **`validate_v2(...) -> bool` (V2)**: validator-side check of a 32-hex HMAC-SHA256 token computed with a per-session key, binding `(id, created, value, sub, model, revision)`.
- **Session handshake**: `get_session_init() -> 312-hex blob` (miner emits an init blob carrying an encrypted ephemeral HMAC key; empty on validator); `decrypt_session_key(blob_hex, x25519_private_key_hex) -> 64-hex key` (validator decrypts the miner's ephemeral key using its X25519 private key); `is_v2_session(blob)` checks the 312-hex length.
- **`pkg_hash` module** (`python -m cllmv.pkg_hash`): locates the installed `sglang` or `vllm` package (handling editable installs via `direct_url.json` and regular installs via distribution file lists), then calls native `compute_package_hash(name, path, ...)` to emit a JSON `{package, path, hash}` record for build-time attestation of the inference engine.
- **Stub/graceful-degradation mode**: if `chutes-aegis.so` is absent, every function returns empty/`False`/`None` and logs a warning rather than crashing.

## Distributed-Computing Notes
- **Inference validation / anti-cheat for a decentralized GPU subnet.** This is the verifier half of a miner↔validator trust protocol: validators issue/observe completions and confirm via these tokens that the response was produced by the declared model and revision. This is the "did the GPU node actually do the work it claims" problem central to decentralized compute marketplaces.
- **E2EE-adjacent session bootstrap.** The V2 flow uses an X25519 ephemeral key exchange to establish a per-session HMAC key that is decrypted only by the holder of the validator private key — a confidential handshake for binding verification tokens to a session.
- **Attestation of the runtime stack.** `pkg_hash` provides software attestation of the exact inference engine binary/package the miner runs (sglang/vllm), complementing GPU hardware attestation (GraVal) found elsewhere in the Chutes stack.
- **No scheduling, p2p/gossip, Raft/weights, or TEE code here.** All actual cryptography, token derivation, and hashing live inside the closed-source `chutes-aegis.so`; this repo is purely the FFI binding surface and contract documentation (argtypes/restypes, blob sizes, V1-vs-V2 semantics).

## HelixCluster Gaps Addressed
- **LLMOrchestrator inference integrity (planned):** Helix's inference serving path lacks any "prove the served model is the claimed model/revision" guarantee. The token-generate/validate contract here is a reference design for a verification layer over serving nodes.
- **miner/marketplace trust (Phase 8 / 8B):** Helix's decentralized-compute marketplace work needs anti-spoofing for compute results. This repo documents a concrete miner/validator verification handshake shape (init blob → X25519 decrypt → per-session HMAC token).
- **security / E2EE handshake:** the X25519 ephemeral-key-to-session-HMAC pattern is a portable design idea for Helix's confidential session bootstrap, independent of the proprietary blob.
- It does **not** address scheduler/Omega, resources, GPU hardware validation, federation transport, discovery, or leader/consensus — those need GraVal/fiber/sek8s-class repos, not this one.

## Dependencies
- Python 3.10–3.12; runtime install requires only `setuptools>=0.75` (dev extras: `ruff`, `wheel`).
- **Hard runtime dependency on a proprietary native library** `/usr/local/lib/chutes-aegis.so` (not in this repo; closed source). Without it the package is inert.
- Build-time attestation path optionally inspects installed `sglang` or `vllm` distributions (not declared deps; discovered via `importlib.metadata`).

## Rationale
REFERENCE, not PORT/WRAP. The only open code is ~388 lines of `ctypes` glue plus package-path discovery; 100% of the security value is inside the closed-source `chutes-aegis.so`, which is unavailable and not redistributable. Helix cannot link or port that binary, and wrapping a Go binding to a proprietary `.so` it cannot obtain provides no value. What IS reusable is the *protocol design*: the V1/V2 token contract, the 312-hex init blob + X25519 session-key handshake, and the build-time engine-hash attestation pattern. Helix should re-implement an equivalent open verification scheme in Go (LLMOrchestrator/security) using this repo as the documented reference for message shapes and semantics.

## Risks
- **Proprietary core unavailable:** the actual algorithms live in `chutes-aegis.so`; this repo alone is non-functional and cannot be made functional outside Chutes infrastructure.
- **Language mismatch:** Python `ctypes` FFI vs Helix Go; would require cgo or a re-implementation regardless.
- **Weak/legacy crypto in V1:** MD5-based "interleaving" hash is cryptographically broken; only the V2 HMAC-SHA256 path is sound — do not port V1 semantics.
- **Opaque contract:** blob sizes (312/65/32 hex) and field-binding order are asserted by Python comments only; exact derivation is unverifiable without the binary, so any clean-room re-implementation cannot be wire-compatible with Chutes.
- **Maturity:** single squashed commit (Feb 2026), version 0.1.2, "Development Status :: 3 - Alpha", empty README — minimal, evolving, no tests in repo.
