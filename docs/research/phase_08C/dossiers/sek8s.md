# sek8s
- **Repo:** https://github.com/chutesai/sek8s
- **Language:** Python (with Bash host/guest tooling, Ansible, OPA/Rego, C TDX quote helper)
- **License:** MIT
- **Maturity:** active (production-targeted; v0.3.0, CalVer ops releases, CI, changelog discipline, security gates)
- **Distributed-Computing Relevance:** high
- **Portability Verdict:** REFERENCE
- **Target Helix Module:** security (NEW submodule: `pkg/security/attestation` + `pkg/security/admission`), with secondary touch points in `pkg/scheduler` (placement gating) and the GPU(planned) resource layer

## Purpose
sek8s is Chutes' confidential-compute stack that turns a bare-metal Intel TDX host with NVIDIA GPUs into a hardware-attested, tamper-evident, self-contained k3s node ("secure standalone k8s"). It bundles the host orchestration scripts, the encrypted guest-image builder (Ansible), in-guest Python services (TDX + GPU attestation, an OPA-backed admission controller, cosign image verification, a read-only system-status API), and an attestation reverse-proxy — so a remote Bittensor validator can cryptographically verify that a miner's GPU workload runs inside a genuine TEE before trusting it.

## Capabilities
- **Intel TDX quote generation** bound to the node's TLS certificate: `TdxQuoteProvider.get_quote(nonce)` shells out to `/usr/bin/tdx-quote-generator`, packing `report_data = nonce(64 hex) + cert_hash(64 hex)` (128 chars / 64 bytes) so the quote proves both freshness (anti-replay nonce) and key-possession (cert binding).
- **GPU attestation evidence** via NVIDIA nvTrust / `nv-attestation-sdk` (NRAS remote attestation) producing an ES384-signed JWT verifiable against NVIDIA public certs; GPU inventory via NVML/`pynvml` (UUID, compute capability, memory, clock, ECC).
- **Measured boot / RTMR3 access-config measurement**: every access-control file (`/etc/ssh`, PAM, `authorized_keys`, `/etc/passwd|shadow`, sudoers, grub cmdline) is hashed (SHA-384) into RTMR3 in initramfs; offline tamper → VM powers off at boot.
- **LUKS root-disk encryption with remote key release gated by attestation** — the decryption key is never on disk; the remote Chutes attestation/key service releases it only after MRTD/RTMR measurements match policy.
- **OPA/Rego admission controller** (FastAPI mutating+validating webhook) enforcing pod-security: no-root, no privileged, resource limits required, allowed registries only, volume-type/hostPath restrictions, SA-token mutation, seccomp, RBAC/CRD/namespace policies (11 production rego modules + extensive rego test suite).
- **cosign container-signature verification** (per-registry / per-org / per-repo config, Docker Hub digest pinning with TTLs) before images are admitted.
- **Bittensor SR25519 signed-request auth**: `verify_validator_signature` / `authorize()` validate `hotkey:nonce:payload_sha256` against allowlisted validator/miner SS58 keys with a 30s nonce window — the actual subnet trust binding.
- **Attestation reverse-proxy** (`attestation-proxy`): dual external (8443, validator-signature-gated) / internal (8444, NetworkPolicy-gated) FastAPI servers that proxy to the host attestation service over a Unix socket and to in-cluster services; signs every response body with the host RSA TLS key (`X-Signature`, PKCS1v15-SHA256) as a key-possession proof.
- **Read-only system-status API** exposing service health + GPU telemetry inside the otherwise SSH-less VM (TEE VMs have no SSH; management is via `chutes-miner-cli`).
- **Host/guest lifecycle tooling**: GPU/VFIO binding, bridge networking, LUKS volume creation, QEMU launch (`quick-launch.sh`), and a "TEE GPU VM" benchmark variant with partner-key SSH measured into RTMR3.

## Distributed-Computing Notes
- **GPU validation/attestation:** This is the deepest, most reusable asset — a complete dual-root-of-trust attestation pipeline (Intel TDX *and* NVIDIA GPU) with nonce-fresh quotes, cert-binding, and remote verification against Intel Tiber Trust Services + NVIDIA NRAS. Conceptually adjacent to GraVal (chutesai/graval) but TEE-based rather than challenge-response GPU proof-of-work.
- **Consensus/weights:** No subnet weight-setting or chain consensus here. The Bittensor coupling is *authentication only* — validator/miner identities are SS58 hotkeys and requests are SR25519-signed; sek8s is the trust-anchored data plane a validator probes, not the consensus engine.
- **Scheduling/placement:** No scheduler. Relevance to placement is indirect: attestation results are the signal an external scheduler/validator would consult to decide whether a node is trustworthy enough to receive confidential workloads.
- **TEE/confidential compute:** Core competency — measured boot, RTMR3 config measurement, attested LUKS key release, TDX quote + GPU evidence. This is the canonical reference for how to make a node *provably* confidential.
- **E2EE / serving / routing:** Not an inference transport or router. The attestation-proxy is a trust-gated request forwarder, not an E2EE inference channel; it provides authenticated reverse-proxying with response signing.
- **p2p/gossip / fault tolerance:** None. Single-node k3s; HA/gossip are out of scope. Fault handling is limited to proxy socket health-checks driving pod restarts.

## HelixCluster Gaps Addressed
- **security / attestation (largest fit):** Helix has no node attestation or confidential-compute trust root. sek8s is the blueprint for a Helix `pkg/security/attestation` submodule: TDX/SEV-SNP quote generation, nonce+cert binding, remote verification, and attestation-gated secret (LUKS/KMS) release. Directly relevant if Helix ever schedules sensitive workloads onto untrusted/edge GPU nodes.
- **GPU (planned) resource layer:** The NVML inventory + nvTrust evidence flow shows exactly how to enumerate and *prove* GPU identity/health — useful for the planned Helix GPU resource type and for an "attested GPU" admission gate.
- **scheduler / Omega:** Provides a clean pattern for a placement *precondition* — only admit confidential workloads onto nodes whose attestation policy passes. Could feed a node-trust label the Omega scheduler honors.
- **admission / policy:** The OPA/Rego admission-controller + cosign verification is a strong reference for a Helix workload-admission gate (signed-image enforcement, pod hardening) if Helix gains a k8s-compatible control plane.
- **miner/marketplace (Phase 8/8B):** This is the *miner-side TEE node* counterpart to Helix's marketplace — it defines what a trustworthy GPU miner must prove. Helix's federation (Phase 6) and miner work could adopt the SS58-signed, nonce-windowed request-auth scheme as a portable trust protocol.

## Dependencies
- Python 3.12+, FastAPI/Uvicorn, Flask, pydantic-settings, httpx, loguru, backoff, orjson.
- `bittensor` 9.x / `bittensor-wallet` / `substrate-interface` (SR25519 keypair verify), `cryptography` 45 (RSA signing).
- `nvidia-ml-py` / `pynvml` (GPU inventory), `nv-attestation-sdk` (GPU remote attestation), separate `chutes_nvevidence` package wrapping it.
- External system deps: Intel TDX firmware (`firmware/TDVF.fd`), `/usr/bin/tdx-quote-generator` (C helper), `tdx-quote-generator`/PCCS/PCK infra, NVIDIA NRAS, Intel Tiber Trust Services, cosign, OPA, k3s, QEMU, Ansible 2.x, LUKS2/cryptsetup.

## Rationale
REFERENCE, not PORT/WRAP, for three converging reasons. (1) **Hardware-bound:** the value is inseparable from Intel TDX + NVIDIA confidential-GPU hardware, an Intel PCCS/PCK chain, and NVIDIA NRAS — none of which Helix's Go control plane can call without that physical substrate. (2) **Language/stack mismatch:** the logic is Python/Bash/Ansible/Rego glue around platform binaries; the reusable parts are *protocols and measurement recipes* (report-data layout, RTMR3 hashing list, attested-key-release flow, SS58 nonce-windowed auth), which Helix should re-implement idiomatically in Go using the go-tdx-guest / Intel DCAP and NVIDIA attestation libraries rather than vendoring Python. (3) **WRAP is unattractive:** running these FastAPI services as sidecars would drag in bittensor + heavy CUDA/NVML deps and still require the TEE host. MIT licensing means liberal reuse of the *designs and even code snippets* is permitted — capture the attestation pipeline as a Helix spec and port natively into `pkg/security`.

## Risks
- **License:** MIT — low risk; permissive, attribution-only. Safe to study, adapt, and port code.
- **Language mismatch:** Python/FastAPI vs Helix Go — no direct linkage; everything must be re-implemented or sidecar-wrapped.
- **Heavy/hardware deps:** TDX-capable CPUs, confidential-compute-capable NVIDIA GPUs (H100/H200/B200/RTX-Pro class), NVSwitch for 8×H200, Intel PCCS + API key, NVIDIA NRAS account. Without this substrate the code is inert — a real adoption blocker unless Helix targets confidential GPU nodes.
- **External-service trust dependencies:** Remote verification leans on Intel Tiber Trust Services and NVIDIA NRAS (third-party, network-dependent, rate-limited) — must be factored into any Helix port's availability model.
- **Tight Bittensor coupling:** Auth is bound to SS58/SR25519 and an allowlist of subnet validators; reusing the auth pattern in Helix means stripping the Bittensor specifics down to a generic signed-nonce scheme.
- **Fork drift / opacity:** Not a fork (original Chutes work), but several pieces depend on Chutes' own attestation/key-release service and the chutes-miner control plane, which are not fully in-repo — porting requires reconstructing the server side of the LUKS key-release handshake.
- **Single-node scope:** No HA, gossip, or multi-node fault tolerance; do not look here for distributed-systems plumbing — only for the confidential-compute trust root.
