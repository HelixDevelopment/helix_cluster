# graval
- **Repo:** https://github.com/chutesai/graval
- **Language:** Python (thin ctypes wrapper over prebuilt C/OpenCL `.so` libraries)
- **License:** MIT (declared via `setup.py` `license_expression="MIT"`; NO standalone LICENSE file present in repo snapshot — see Risks)
- **Maturity:** active (v0.2.6, `Development Status :: 3 - Alpha`; deployed in production Chutes/Bittensor SN64 stack via Helm + Dockerfile)
- **Distributed-Computing Relevance:** core
- **Portability Verdict:** REFERENCE
- **Target Helix Module:** NEW submodule `pkg/gpuattest` (GPU validation/attestation) feeding `pkg/scheduler` (Omega) + `pkg/resources`; secondary tie-in to `security` (E2EE payload sealing) and the Phase 8/8B miner/marketplace plane

## Purpose
GraVal ("Graphics card Validation") is a cryptographic GPU attestation and proof-of-GPU-work library used by the Chutes decentralized inference subnet to prove that a remote miner ACTUALLY possesses the specific GPU hardware it claims, and to bind encrypted inference payloads to that exact device so only the genuine GPU can decrypt them. It is the trust anchor that lets validators verify untrusted miners' compute without a TEE.

## Capabilities
- **Device-info attestation challenge/response:** validator issues a challenge keyed to a claimed device roster (name, UUID, memory, SM/processor count, clock rate, max threads/processor); the miner must compute a response on the GPU that only a node with that exact hardware profile can produce (`generate_device_info_challenge` / `process_device_info_challenge` / `verify_device_info_challenge`).
- **Proof-of-GPU-work (PoVW):** miner runs `generate_challenge_matrices(seed, iterations)` producing matrix-multiply work products with SHA256 intermediate hashes per iteration/matrix; validator spot-checks any single iteration index cheaply via `validator_check_proof` without re-running the whole workload (succinct probabilistic verification).
- **Hardware-bound encryption (device-sealed E2EE):** validator `encrypt(device_info, plaintext, iterations, seed)` produces ciphertext + IV bound to a GPU's properties; only the matching GPU can `miner_decrypt(...)` it. This is the transport-sealing primitive for confidential inference routing.
- **Filesystem challenge:** validator can demand SHA256 of an arbitrary (offset,length) byte range of a file on the miner (`miner_filesystem_challenge`) to prove a model/weight file is genuinely resident on disk.
- **Multi-GPU node enumeration:** `initialize_node()` discovers all local accelerators; per-device work products keyed by GPU UUID.
- **Ships as a standalone FastAPI microservice** (`api.py`) with Bittensor SS58 signature auth (`X-Validator`/`X-Nonce`/`X-Signature`, 30s nonce window), internal-only IP filtering, and an async GPU lock; Helm chart runs 8 replicas pinned to `nvidia.com/gpu` nodes with pod anti-affinity and 32Gi/1-GPU requests.

## Distributed-Computing Notes
- **GPU validation/attestation (the core):** GraVal is precisely the GPU-attestation primitive HelixCluster lacks. It is a *software* attestation scheme (OpenCL/CUDA-backed matrix work + device fingerprinting), distinct from hardware TEE attestation (sek8s/confidential compute). It assumes an untrusted-miner adversary model and uses probabilistic spot-checks rather than full re-execution — directly relevant to a scheduler that places work on heterogeneous, partially-trusted GPU nodes.
- **Miner/validator topology:** maps cleanly onto Helix's planned miner/marketplace plane (Phase 8/8B). Validators = trusted control-plane verifiers; miners = untrusted compute providers proving capability before being scheduled.
- **Consensus/weights coupling:** designed for Bittensor subnet economics — validators use challenge results to set on-chain weights (reward genuine GPUs, penalize spoofers). The signature auth uses `bittensor_wallet` SS58 keypairs. Helix would replace Bittensor weight-setting with its own Raft/SWIM-based reputation, keeping only the validation math.
- **E2EE inference transport:** the device-bound encrypt/decrypt is a real end-to-end-encryption mechanism for getting an inference payload to a specific GPU such that no intermediary (or even a spoofing miner) can read it.
- **Fault tolerance / anti-fraud:** spot-check verification means a validator can audit a miner at O(1) cost per challenge across 200+ challenges (per test), enabling continuous lightweight integrity sampling rather than expensive full verification.
- **Opaque core:** the actual cryptographic + GPU logic lives entirely in prebuilt `libgraval-miner.so` / `libgraval-validator.so` binaries — NO source for the algorithm is in this repo. The Python here is purely ctypes FFI marshalling.

## HelixCluster Gaps Addressed
- **GPU (planned) + `pkg/resources`:** gives Helix a concrete protocol to *prove* a node's advertised GPU inventory is real before trusting it as a schedulable resource — closing the "node lies about its GPUs" gap.
- **`pkg/scheduler` (Omega):** attestation results become a placement gate / admission predicate; spot-check pass-rate becomes a node health/reputation signal feeding scoring.
- **Miner/marketplace (Phase 8/8B):** the validator↔miner challenge economy is a direct template for a decentralized compute marketplace where providers must prove capability to earn placement/reward.
- **`security` / E2EE:** device-bound payload sealing is a candidate for confidential workload delivery; complements (does not replace) TEE-based confidential compute from sek8s.
- **federation (Phase 6) / discovery / leader-consensus:** validation outcomes are a trust input to cross-cluster federation and to weight/reputation aggregation that Helix would run over Raft instead of Bittensor.

## Dependencies
- **Runtime:** Python 3.10–3.12, `ctypes` (stdlib) → prebuilt `.so` libs (OpenCL/CUDA; struct fields reference `context/queue/program/tanh_kernel/downsample_kernel/opencl_initialized` → OpenCL backend). Requires NVIDIA GPU (`nvidia.com/gpu`).
- **Service layer (`api.py`):** `fastapi`, `uvicorn`, `loguru`, `bittensor-wallet` (SS58 keypair signature verification).
- **Deploy:** Dockerfile on `parachutes/python:3.12.9`; Helm chart; Ansible playbooks for host prep.
- **Packaging:** `setuptools` only; the `.so` binaries are vendored in `src/graval/lib/`.

## Rationale
REFERENCE, not PORT/WRAP. The scientific value is extremely high and directly on-target for Helix's distributed-GPU focus, BUT the entire crypto/GPU algorithm is a closed prebuilt `.so` blob — there is no portable source to translate to Go, and the Python is a trivial FFI shim. WRAP (shelling out to / linking the `.so` from Go via cgo) is technically possible but couples Helix to opaque NVIDIA/OpenCL binaries of unknown provenance built for a specific ABI, and to a Bittensor-specific deployment model. The correct use is to study GraVal's *design* — software GPU attestation via device-fingerprint challenge/response + probabilistic PoVW spot-checks + device-bound encryption — and implement an equivalent native primitive in `pkg/gpuattest` with Go + CUDA/OpenCL kernels under Helix's own consensus/reputation layer. Per CLAUDE-1, a wrapped opaque binary cannot be end-to-end validated for genuine end-user GPU-attestation behavior, which further favors a transparent reimplementation over WRAP.

## Risks
- **License ambiguity:** MIT is declared in `setup.py` (and upstream `rayonlabs/graval` is MIT), but THIS repo snapshot contains NO `LICENSE` file — confirm the LICENSE ships before relying on it; treat the `.so` binaries' license/provenance as separately unverified.
- **Opaque binary core:** all real logic is in unauditable prebuilt `.so` files; security-sensitive (attestation) code that cannot be reviewed is unacceptable to vendor directly into a trust-critical path. No reproducible build is provided.
- **Language mismatch:** Python+ctypes; zero Go. Any reuse is a clean-room reimplementation, not a port.
- **Hard NVIDIA/OpenCL + GPU dependency:** untestable in Helix CI without GPU hardware; heavy CUDA/OpenCL runtime coupling.
- **Bittensor coupling:** auth and incentive model assume a Bittensor subnet (SS58 wallets, on-chain weights); must be stripped and replaced with Helix-native identity/consensus.
- **Fork drift:** Chutes (`chutesai`) tracks/rebrands `rayonlabs/graval`; upstream `.so` and ABI bump together (version-locked to `0.2.6` in Docker/Helm), so any vendored binary risks silent drift.
- **Attestation is software, not hardware:** GraVal's guarantees are weaker than TEE attestation — a sufficiently capable adversary emulating GPU device-info + work could attempt spoofing; design must combine it with hardware roots of trust (sek8s) for strong confidentiality.
