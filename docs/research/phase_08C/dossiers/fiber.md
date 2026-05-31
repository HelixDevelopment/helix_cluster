# fiber
- **Repo:** https://github.com/chutesai/fiber
- **Language:** Python
- **License:** MIT
- **Maturity:** production
- **Distributed-Computing Relevance:** high
- **Portability Verdict:** REFERENCE
- **Target Helix Module:** security (E2EE transport patterns) + a NEW federation/miner submodule (Phase6/Phase8); LLMOrchestrator transport reference

## Purpose
Fiber is a lightweight Python framework (originally by Rayon Labs, here the chutesai fork) for building Bittensor subnets — it provides the secure miner↔validator communication layer plus on-chain (Subtensor/substrate) interactions used to read the metagraph and set consensus weights. It is the production networking/comms substrate that Chutes' decentralized GPU-inference subnet runs on.

## Capabilities
- Encrypted HTTP transport between validators (clients) and miners (servers) over FastAPI/httpx, with both a plaintext path (`fiber/validator`, `fiber/miner`) and an end-to-end encrypted path (`fiber/encrypted/*`).
- Handshake + hybrid encryption: validator fetches the miner's RSA-2048 public key, generates a random 32-byte symmetric key, encrypts it with RSA-OAEP(SHA-256), and posts it; subsequent request/response bodies are encrypted with Fernet (AES-128-CBC + HMAC) keyed by that symmetric key (this is the "Multi-Layer Transport Security / MLTS" the README references).
- Request authentication via substrate (sr25519) keypair signatures over a constructed signing message (nonce + miner hotkey + symmetric-key-uuid / payload hash), verified server-side against the on-chain hotkey.
- Replay protection: a `NonceManager` enforcing single-use, time-windowed nonces (`NONCE_WINDOW_NS`, 2-min TTL) with a background cleanup thread.
- Symmetric-key lifecycle: per-(validator-hotkey, uuid) Fernet keys, persisted encrypted-at-rest, with expiry and periodic background cleanup (`EncryptionKeysHandler`).
- Streamed and non-streamed POST/GET helpers (`make_streamed_post`, `make_non_streamed_post/get`) — the streaming path is what carries token-by-token LLM inference output.
- On-chain (`fiber/chain`): `Metagraph` sync of nodes (miners+validators) from Subtensor, weight normalization/quantization to U16 and `set_weights` (incl. commit-reveal via `bittensor_commit_reveal`), commitments, IP posting to chain, node fetching.
- DDoS/sybil resistance hook: `blacklist_low_stake` dependency rejecting requests from hotkeys whose on-chain stake is below a configurable threshold.

## Distributed-Computing Notes
- **p2p/gossip:** Despite the "p2p transport" one-liner, fiber is NOT a gossip/DHT mesh. It is a directed validator→miner HTTP client/server model; peer discovery is done out-of-band via the on-chain metagraph (each node posts its IP/port to the Subtensor chain), not via SWIM-style gossip. This contrasts with HelixCluster's SWIM layer.
- **Consensus/weights:** Consensus is delegated entirely to Bittensor/Subtensor (Yuma consensus). Fiber's role is to (a) read the metagraph and (b) submit validator weight vectors on-chain, optionally with commit-reveal to hide weights until a reveal window — directly relevant to any "scoring/reputation → on-chain settlement" design.
- **E2EE inference transport:** The `encrypted` package is a clean, dependency-light reference implementation of authenticated hybrid-encrypted RPC (RSA key exchange + Fernet payload encryption + signed nonce headers + stake gating) — exactly the shape needed for confidential inference routing.
- **GPU validation/attestation (GraVal), TEE/sek8s:** NONE present. This repo is pure comms+chain; no GPU probing, attestation, scheduling, or placement logic. Those live in sibling chutesai repos.
- **Scheduling/placement:** None. Fiber gives the transport + identity + stake primitives; the subnet's own validator code decides which miner to query.
- **Fault tolerance:** Light — `tenacity` retry/backoff around substrate queries; metagraph is periodically re-synced (5-min loop) and cached to disk. No replication/quorum of its own.
- **chutesai fork delta:** Tracks upstream `rayonlabs/fiber` closely (README still points pip installs at `rayonlabs/fiber`). Visible fork-specific work is incremental (e.g. merged PR "trust-removal" adjusting metagraph trust fields); no architectural divergence observed. Effectively fork-tracking with minor patches.

## HelixCluster Gaps Addressed
- **security / E2EE (real):** Provides a battle-tested pattern for authenticated, encrypted node-to-node RPC with replay protection and key rotation that Helix's E2EE inference transport goal can mirror in Go.
- **federation (Phase6) & miner/marketplace (Phase8/8B):** The validator/miner identity model, on-chain stake-weighted admission (`blacklist_low_stake`), and weight-setting/commit-reveal flow are a concrete reference for a decentralized compute marketplace with reputation settlement.
- **LLMOrchestrator:** The streamed encrypted POST path is a direct reference for confidential streaming inference between an orchestrator and remote GPU workers.
- **discovery / leader-consensus:** Only partially — fiber outsources discovery and consensus to a blockchain, which is orthogonal to Helix's SWIM+Raft; useful as a contrast/architecture reference, not a drop-in.

## Dependencies
- Chain: `substrate-interface==1.7.10`, `async-substrate-interface==1.1.1`, `bittensor-commit-reveal==0.1.0`, `eth-typing`, `netaddr`, `tenacity`, `pydantic<=2.9.2`.
- Networking (`full` extra): `fastapi==0.112.0`, `uvicorn==0.30.5`, `httpx==0.27.2`, `cryptography>=43,<43.1` (RSA-OAEP + Fernet).
- No CUDA/GPU/ML dependencies at all — it is a thin comms+chain library.

## Rationale
REFERENCE (not PORT/WRAP): The code is small, MIT-licensed, and conceptually valuable, but it is (1) tightly coupled to Bittensor/Subtensor for identity, discovery, and consensus — Helix uses its own SWIM+Raft+etcd stack, so the chain half is not reusable; and (2) pure Python with no GPU/compute logic, so there is nothing performance-critical to wrap via FFI. The highest-value use is to PORT the *patterns* of the `encrypted` package (RSA→Fernet hybrid handshake, signed single-use nonces, stake-gated admission, encrypted streaming) into Helix's Go security/transport layer, and to study its weight-setting/commit-reveal flow for the Phase8 miner-marketplace settlement design. Wrapping the live Python (embedding it as a service) is not warranted given the small surface and the chain coupling.

## Risks
- **Language mismatch:** Pure Python (FastAPI/asyncio); Helix is Go/Zig. No clean library boundary to wrap — porting means re-implementing in Go.
- **Bittensor lock-in:** Identity, peer discovery, stake, and consensus all assume a live Subtensor chain and substrate keypairs; not usable standalone without that ecosystem.
- **Crypto details to scrutinize if ported:** RSA-2048 asymmetric keys are regenerated in-memory on each miner start (no persisted/attested key, so no long-term identity binding for the encryption key); Fernet is AES-128 (acceptable but not AES-256-GCM); these are fine for the threat model but worth hardening in a Helix port.
- **Fork drift:** This is a fork of `rayonlabs/fiber` and explicitly pip-installs from upstream; chutes-specific changes are minimal, so upstream drift / divergence tracking is a maintenance consideration if vendored.
- **No GPU/attestation here:** Anyone expecting GraVal/TEE content will not find it — relevance is the secure-transport + chain-settlement layer only.
