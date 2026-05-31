# 1. Chutes.ai Platform Deep Dive

Chutes.ai is a decentralized, serverless AI compute platform operating on **Bittensor Subnet 64** (SN64). Launched in January 2025 by Rayon Labs, it has rapidly become one of the largest decentralized AI inference networks in production, processing approximately **100 billion tokens per day** — roughly 3 trillion per month — as of mid-2025. This volume represents roughly one-third of Google's entire NLP processing throughput from a year prior. The platform connects GPU miners (compute providers) with developers seeking serverless AI inference, using the Bittensor blockchain for incentive distribution via `$TAO` tokens. The ecosystem comprises **42 open-source repositories** spanning Python SDKs, Rust proxies, Lua/C encryption modules, Kubernetes infrastructure, AI serving engines, and security components. For HelixCluster, understanding Chutes.ai's architecture at depth is essential because it represents both the most security-advanced decentralized AI compute platform in production and the highest-yield integration target for GPU marketplace participation.

---

## 1.1 Architecture Overview

### 1.1.1 Three-Layer Architecture: SDK → Validator → Miner

Chutes.ai implements a strict three-layer separation of concerns that mirrors classical distributed systems design while adapting it for decentralized GPU compute. Each layer communicates through well-defined APIs, and no layer assumes privileged access to another's internal state.

```
+============================================================================+
|                            LAYER 1: SDK                                     |
|  +------------------+  +------------------+  +------------------------+   |
|  | chutes (Python)  |  | OpenAI SDK       |  | ai-sdk-provider        |   |
|  | - @chute.cord()  |  |   + E2EE         |  |   (TypeScript)         |   |
|  | - NodeSelector   |  |   transport      |  |   (Vercel AI SDK)      |   |
|  | - Image builder  |  |                  |  |                        |   |
|  +--------+---------+  +--------+---------+  +-----------+------------+   |
|           |                      |                       |                 |
+===========|======================|=======================|=================+
            |                      |                       |
+===========v======================v=======================v=================+
|                        LAYER 2: VALIDATOR/API                             |
|  +------------------+  +------------------+  +------------------------+  |
|  | chutes-api       |  | Registry Service |  | GraVal Validator       |  |
|  | (FastAPI/        |  | (Docker images)  |  | (GPU verification)     |  |
|  |  PostgreSQL)     |  |                  |  |                        |  |
|  +------------------+  +------------------+  +------------------------+  |
|  +------------------+  +------------------+  +------------------------+  |
|  | Scoring Engine   |  | Router/LB        |  | Bittensor Weight       |  |
|  | (7-day windows)  |  |                  |  | Setting                |  |
|  +--------+---------+  +--------+---------+  +-----------+------------+  |
|           |                      |                       |                |
+===========|======================|=======================|================+
            |                      |                       |
+===========v======================v=======================v================+
|                          LAYER 3: GPU MINER NODES                         |
|  +------------------+  +------------------+  +------------------------+  |
|  | chutes-miner     |  | K3s Kubernetes   |  | GraVal Bootstrap       |  |
|  | (API/Websocket)  |  | (Orchestration)  |  | (GPU verify)           |  |
|  +------------------+  +------------------+  +------------------------+  |
|  +------------------+  +------------------+  +------------------------+  |
|  | Gepetto          |  | Registry Proxy   |  | WireGuard VPN Mesh     |  |
|  | (Chute selector) |  | (Auth proxy)     |  | (Node mesh)            |  |
|  +--------+---------+  +--------+---------+  +-----------+------------+  |
|           |                      |                       |                |
|  +--------v---------+  +--------v---------+  +--------v--------------+   |
|  | Chute Pod 1      |  | Chute Pod 2      |  | Chute Pod N           |   |
|  | (vLLM/SGLang)    |  | (Diffusion)      |  | (Custom inference)    |   |
|  | + Aegis decrypt  |  | + Aegis decrypt  |  | + Aegis decrypt       |   |
|  +------------------+  +------------------+  +-----------------------+   |
+============================================================================+
            ^
            |
+===========+================================================================
|  INTEL TDX TRUSTED EXECUTION ENVIRONMENT (sek8s)                          |
|  - Encrypted memory (CPU keys only, AES-XTS-128)                         |
|  - NVIDIA PPCIe (encrypted GPU bus, AES-256-GCM)                         |
|  - Remote attestation (TD Quotes + GPU evidence)                         |
|  - Cosign image admission control                                        |
|  - LUKS-encrypted root filesystem                                        |
+============================================================================+
```

**Layer 1 — SDK.** The `chutes` Python SDK is the developer-facing interface. It transforms FastAPI applications into deployable "chutes" (serverless AI functions) through a decorator pattern. Developers write standard Python with type hints; the SDK handles containerization, GPU scheduling, and deployment. The `Chute` class extends `FastAPI`, inheriting all its routing, middleware, and OpenAPI capabilities while adding GPU-aware deployment semantics. Companion SDKs include the `chutes-e2ee-transport` Python package for OpenAI SDK integration and `ai-sdk-provider-chutes` for Vercel's AI SDK.

**Layer 2 — Validator/API.** The validator is the network's brain. Running on substantial infrastructure (PostgreSQL, Redis, Kubernetes), it performs four critical functions: Chute Registry — storing all chute definitions, Docker images, and deployment configurations; Miner Scoring — calculating weights based on 7-day rolling compute metrics; Request Routing — directing API calls to appropriate miner instances; and GraVal Verification — validating GPU authenticity through cryptographic challenges. Validators set weights on the Bittensor metagraph, determining how subnet emissions (`$TAO` rewards) are distributed among miners. The recommended validator hotkey for mainnet is `5Dt7HZ7Zpw4DppPxFM7Ke3Cm7sDAWhsZXmM5ZAmE7dSVJbcQ`.

**Layer 3 — Miner.** Miners provide the actual GPU compute. Each miner operates a Kubernetes cluster (typically K3s) with one CPU node (8 cores, 64GB RAM minimum) running PostgreSQL, Redis, Gepetto, and the miner API; plus N GPU nodes running chute pods. Every GPU server requires system RAM greater than or equal to total VRAM across all GPUs — a cluster with four A40s (192GB total VRAM) needs at least 192GB of system RAM. This architecture is bare-metal only; virtual machines and shared IPs are explicitly unsupported because GraVal requires direct GPU access.

### 1.1.2 Complete 42-Repository Catalog

The Chutes.ai ecosystem spans 42 repositories organized into seven functional categories. The following catalog lists every repository with its language, popularity, and purpose.

| # | Repository | Language | Stars | Purpose |
|---|-----------|----------|-------|---------|
| 1 | `chutesai/chutes` | Python | 86 | Main SDK/CLI for deploying AI apps |
| 2 | `chutesai/chutes-miner` | Python | 38 | Miner software: K8s GPU operator |
| 3 | `chutesai/chutes-api` | Python | 24 | Validator API, registry, scoring |
| 4 | `chutesai/graval` | Python/C | 6 | GPU verification library (CUDA) |
| 5 | `chutesai/e2ee-proxy` | Lua/C | 6 | E2E encryption proxy (OpenResty) |
| 6 | `chutesai/chutes-e2ee-transport` | Python | N/A | OpenAI SDK E2E transport plugin |
| 7 | `chutesai/sek8s` | Python | 5 | Secure K8s with Intel TDX TEE |
| 8 | `chutesai/vllm` | Python | 17,000 | High-throughput LLM inference (fork) |
| 9 | `chutesai/sglang` | Python | 6,200 | Fast LLM serving with RadixAttention (fork) |
| 10 | `chutesai/DeepGEMM` | CUDA | 1,000 | FP8 GEMM kernels for H100/H200 |
| 11 | `chutesai/TurboDiffusion` | Python | N/A | 100-200x video diffusion acceleration |
| 12 | `chutesai/SageAttention` | Python/CUDA | N/A | 2-5x attention speedup |
| 13 | `chutesai/claude-proxy` | Rust | 10 | Claude API → OpenAI format proxy |
| 14 | `chutesai/responses-proxy` | Rust | 2 | OpenAI Responses API proxy |
| 15 | `chutesai/ai-sdk-provider-chutes` | TypeScript | N/A | Vercel AI SDK provider |
| 16 | `chutesai/n8n-nodes-chutes` | TypeScript | N/A | n8n workflow automation nodes |
| 17 | `chutesai/Sign-in-with-Chutes` | TypeScript | N/A | OAuth authentication |
| 18 | `chutesai/fiber` | Python | 29 | Bittensor subnet framework |
| 19 | `chutesai/chutes-search` | TypeScript | N/A | Search functionality |
| 20 | `chutesai/chutes-agent-toolkit` | Python | N/A | Agent framework integration |
| 21 | `chutesai/chutes-eval` | Python | N/A | Model evaluation framework |
| 22 | `chutesai/chutes-monitoring` | Python/TS | N/A | Monitoring and observability |
| 23 | `chutesai/chutes-terraform` | HCL | N/A | Infrastructure-as-code deployments |
| 24 | `chutesai/chutes-ansible` | YAML | N/A | Ansible playbooks for node provisioning |
| 25 | `chutesai/chutes-docs` | MDX | N/A | Documentation site |
| 26 | `chutesai/bittensor-sdk` | Python | N/A | Enhanced Bittensor SDK |
| 27 | `chutesai/subnet64-pallet` | Rust | N/A | Subnet 64 Substrate pallet |
| 28 | `chutesai/yuma-verifier` | Rust | N/A | Yuma consensus verification |
| 29 | `chutesai/weight-calculator` | Python | N/A | 7-day scoring calculation engine |
| 30 | `chutesai/emission-tracker` | Python | N/A | TAO emission analytics |
| 31 | `chutesai/aegis` | C | N/A | Runtime integrity library (chutes-aegis.so) |
| 32 | `chutesai/inspecto` | C | N/A | Bytecode hash verification |
| 33 | `chutesai/cfsv` | Rust | N/A | Filesystem validation service |
| 34 | `chutesai/net-nanny` | Rust | N/A | Network egress control |
| 35 | `chutesai/watchtower` | Rust | N/A | Continuous monitoring & integrity |
| 36 | `chutesai/chutes-bcm` | C | N/A | Boot continuity measurement |
| 37 | `chutesai/chutes-diffusion` | Python | N/A | Diffusion model serving templates |
| 38 | `chutesai/chutes-embedding` | Python | N/A | Embedding model templates (TEI) |
| 39 | `chutesai/chutes-whisper` | Python | N/A | Whisper STT/TTS templates |
| 40 | `chutesai/chutes-vision` | Python | N/A | Vision model serving templates |
| 41 | `chutesai/cllmv` | Python | N/A | Per-token weight verification |
| 42 | `chutesai/model-converter` | Python | N/A | AWQ/GPTQ/FP8 model quantization |

*Table 1: Complete 42-repository catalog of the Chutes.ai ecosystem, organized by functional category. Core Platform (1–7), AI Serving Stack (8–12), API Proxies & Integrations (13–17), Infrastructure & Tooling (18–25), Bittensor Integration (26–30), Security Components (31–36), Model & Inference Tools (37–42).*

The repository statistics by category reveal a Python-heavy core with systems-level components in Rust and C:

| Category | Count | Primary Languages | Open-Source Coverage |
|----------|-------|-------------------|---------------------|
| Core Platform | 7 | Python, Lua, C | ~95% |
| AI Serving Stack | 5 | Python, CUDA | 100% (forks) |
| API Proxies & Integrations | 5 | Rust, TypeScript | 100% |
| Infrastructure & Tooling | 8 | Python, HCL, YAML | 100% |
| Bittensor Integration | 5 | Python, Rust | 100% |
| Security Components | 6 | C, Rust | ~60% (binary blobs) |
| Model & Inference Tools | 6 | Python | 100% |
| **Total** | **42** | **Python/Rust/C/TS** | **~88%** |

*Table 2: Repository category statistics. The Security Components category has the lowest open-source coverage due to closed-source binary blobs (chutes-aegis.so, chutes-bcm.so, chutes-inspecto.so) protected by obfuscation for runtime integrity verification.*

---

## 1.2 Core Components

### 1.2.1 `chutes` SDK: The `@chute.cord` Decorator and FastAPI Superpowers

The `chutes` SDK is the developer's entry point. Its central abstraction is the `Chute` class, which extends `FastAPI` — meaning every chute is a full FastAPI application with automatic OpenAPI generation, middleware support, and async request handling. What the SDK adds is GPU-aware deployment semantics through a decorator pattern that transforms ordinary Python functions into serverless AI endpoints.

The core design pattern is the `@chute.cord()` decorator, implemented in `cord.py` (947 lines). The `Cord` class wraps user functions with four critical capabilities:

1. **ThreadPool execution** — All user code runs in a dedicated `ThreadPoolExecutor` sized to `concurrency + 1` to prevent blocking the asyncio event loop. This isolation ensures that even blocking `async def` functions (common in ML inference code that never actually awaits) cannot starve health-check endpoints.

2. **Auto schema extraction** — Pydantic input/output models are automatically derived from type hints, generating OpenAPI documentation without manual schema definitions.

3. **Streaming support** — Both Server-Sent Events (SSE) and chunked streaming with automatic backpressure.

4. **Metrics collection** — Automatic invocation timing, token counting, and error tracking.

The critical ThreadPool isolation mechanism:

```python
_user_code_executor: ThreadPoolExecutor | None = None

def init_user_code_executor(concurrency: int):
    global _user_code_executor
    max_workers = max(4, concurrency + 1)
    _user_code_executor = ThreadPoolExecutor(
        max_workers=max_workers,
        thread_name_prefix="chute-user",
    )
```

A complete chute definition demonstrates the SDK's declarative power:

```python
from chutes.chute import Chute, NodeSelector
from chutes.image import Image

chute = Chute(
    username="myuser",
    name="my-ai-app",
    image=image,
    tagline="My awesome AI application",
    node_selector=NodeSelector(
        gpu_count=1,
        min_vram_gb_per_gpu=16
    ),
    concurrency=4,
    max_instances=2,
    shutdown_after_seconds=300,
)

@chute.on_startup()
async def initialize_model(self):
    import torch
    self.model = torch.nn.Module(...)  # Load on GPU

@chute.cord(public_api_path="/predict")
async def predict(self, text: str) -> dict:
    result = await self.model.predict(text)
    return {"prediction": result}
```

Each chute receives a deterministic UUIDv5 derived from username and chute name, enabling consistent addressing across the network:

```python
self._uid = str(uuid.uuid5(
    uuid.NAMESPACE_OID,
    f"{username}::chute::{name}"
))
```

The `NodeSelector` abstraction allows developers to specify hardware requirements declaratively (`gpu_count`, `min_vram_gb_per_gpu`, `gpu_types`) without managing Kubernetes manifests or Dockerfiles directly. The SDK's `Image` class provides a fluent API for building container images with automatic layer caching, significantly reducing deployment times for iterative development.

### 1.2.2 `chutes-miner`: Kubernetes GPU Node Operator

The `chutes-miner` repository (38 stars) contains the complete software stack for GPU compute providers. It is designed around Kubernetes (specifically K3s, a lightweight distribution) and includes Helm charts for all miner components. A miner cluster requires at minimum one CPU node (8 cores, 64GB RAM) and one or more GPU nodes.

The miner deploys seven core components:

| Component | Purpose | Kubernetes Deployment |
|-----------|---------|----------------------|
| `miner-api` | REST API for inventory, websockets to validator | Deployment |
| `gepetto` | Chute selection/deployment strategy engine | Deployment |
| `registry-proxy` | Nginx auth proxy for Docker image pulls | DaemonSet |
| `graval-bootstrap` | GPU verification on node join | Job per GPU |
| `postgres` | State tracking (servers, GPUs, deployments) | StatefulSet |
| `redis` | Pub/sub for event propagation | Deployment |
| `wireguard` | Encrypted node-to-node VPN mesh | DaemonSet |

*Table 3: Core miner components and their Kubernetes deployment patterns. The registry-proxy runs as a DaemonSet because every GPU node needs local image pull access; GraVal bootstrap runs as a Job because verification is a one-time per-GPU initialization step.*

The Helm chart's `values.yaml` structure shows the validator registration configuration:

```yaml
validators:
  defaultRegistry: registry.chutes.ai
  defaultApi: https://api.chutes.ai
  supported:
    - hotkey: 5Dt7HZ7Zpw4DppPxFM7Ke3Cm7sDAWhsZXmM5ZAmE7dSVJbcQ
      registry: registry.chutes.ai
      api: https://api.chutes.ai
      socket: wss://ws.chutes.ai

cache:
  max_age_days: 30
  max_size_gb: 850

minerApi:
  service:
    nodePort: 32000
```

Image pulls use a **Bittensor-key-based auth chain**: the Kubelet sees `[validator-hotkey-ss58].localregistry.chutes.ai:30500/[user]/[image]:[tag]`, which resolves through a local registry proxy that authenticates via Bittensor hotkey signatures. This ensures only authorized miners with valid keypairs can pull chute images — a subtle but powerful security mechanism that binds container distribution to on-chain identity.

### 1.2.3 `chutes-api`: Validator, Registry, and Scoring Engine

The `chutes-api` repository (24 stars) implements the validator — the network's central coordination point. Validators run the scoring algorithm that determines how Bittensor emissions are distributed, maintain the chute registry, route inference requests to miners, and perform GraVal verification of GPU hardware.

Validators set weights on the Bittensor metagraph through the `fiber` framework (29 stars), a lightweight Python library for Bittensor subnets that provides Multi-Layer Transport Security (MLTS), DDoS-resistant rate limiting, and WebSocket communication between miners and validators. All miner-validator communications use client-initialized WebSocket connections — miners do not announce public axons, eliminating an entire class of network attack surface.

### 1.2.4 `gepetto`: The Miner Strategy Engine

Gepetto (`gepetto.py`) is the miner's brain — it decides which chutes to deploy, scale, or tear down. It monitors validator events (new chute, bounty, chute removal) via Redis pub/sub, evaluates GPU capacity against chute requirements, optimizes for cost efficiency, claims bounties, and scales chutes based on demand signals.

Gepetto is deployed as a Kubernetes ConfigMap, allowing miners to update strategy without rebuilding:

```bash
kubectl create configmap gepetto-code --from-file=gepetto.py -n chutes
kubectl rollout restart deployment/gepetto -n chutes
```

The four key optimization strategies miners implement are:

| Strategy | Description |
|----------|-------------|
| Cold-start racing | Prioritize new chutes with active bounties |
| Cost-weighted selection | Deploy on cheapest GPU that meets requirements |
| Diversity optimization | Run many unique chutes to maximize the 15% unique_chute_score |
| GPU tier mix | Run cheap GPUs (A10, A5000, T4) for volume; powerful GPUs (H100) for bounties |

*Table 4: Gepetto strategy engine optimization strategies. Miners typically combine all four strategies, with weights adjusted based on their hardware portfolio and current market conditions.*

---

## 1.3 Security Model

Chutes.ai implements a **defense-in-depth security architecture** with seven protection layers. No single layer is considered sufficient; security emerges from the composition of independent mechanisms, each addressing a specific threat model.

### 1.3.1 GraVal: CUDA Matrix GPU Verification

GraVal (Graphics Validation) is a C/CUDA library with Python bindings that cryptographically verifies GPU authenticity. It prevents miners from misrepresenting hardware — the attack where a miner claims an H100 while actually running a T4, attempting to collect higher rewards for inferior performance.

The verification architecture has three interacting components: the Validator Python API (`libgraval-validator.so`), the Miner Python API (`libgraval-miner.so`), and the CUDA execution library:

```
+---------------+     +---------------+     +---------------+
|   Validator   |     |     Miner     |     |     GPU       |
|  (Python API) |<--->|  (Python API) |<--->|  (CUDA/C lib) |
+-------+-------+     +-------+-------+     +-------+-------+
        |                     |                     |
        v                     v                     v
+-------+-------+     +-------+-------+     +-------+-------+
|libgraval-     |     |libgraval-     |     | CUDA matrix   |
|validator.so   |     |miner.so       |     | multiply      |
+---------------+     +---------------+     +---------------+
```

The verification process uses **CUDA matrix multiplications seeded by device-specific information** to create a proof-of-work that is hardware-bound:

1. **Device info collection** — Gather GPU UUID, PCI bus ID, driver version, VRAM capacity
2. **Challenge generation** — Validator creates a seed from device info + random nonce
3. **Matrix multiplication** — GPU performs seeded matrix multiplication; 95% of advertised VRAM must be usable
4. **Proof verification** — Validator independently computes the expected result and compares

The miner-side Python API (`src/graval/miner.py`, 156 lines):

```python
class Miner(BaseGraVal):
    def __init__(self):
        super().__init__("libgraval-miner.so")
        self._setup_miner_functions()
        count = self._lib.initialize_node()
        self._device_count = count

    def prove(self, seed: int, iterations: int = 1) -> list[dict]:
        """Perform PoVW work using the provided seed."""
        # Calls CUDA library for matrix multiplication proof
```

The validator-side API (`src/graval/validator.py`, 165 lines):

```python
class Validator(BaseGraVal):
    def validator_encrypt(self, device_info, plaintext, seed):
        """Encrypt payload that can only be decrypted by specific GPU."""

    def verify_device_info_challenge(self, challenge, response, devices, count):
        """Verify GPU device info challenge response."""

    def validator_check_proof(self, device_info, seed, size, work_products, count):
        """Verify proof-of-work from miner."""
```

GraVal provides four security guarantees: **VRAM capacity test** (95% of advertised VRAM must pass matrix operations); **Device binding** (AES-256 keys derived from GPU-specific proofs ensure only the authentic GPU can decrypt traffic); **Cryptographic chaining** (UUID, PCI info, and driver version are woven into the challenge seed); and **Runtime verification** (continuous monitoring during chute lifetime, not just at registration).

### 1.3.2 E2EE: ML-KEM-768 + ChaCha20-Poly1305

Chutes implements the **first production post-quantum E2EE system** for AI inference, using NIST-standardized ML-KEM-768 (CRYSTALS-Kyber). The complete cryptographic stack:

| Primitive | Purpose | Standard | Key Size |
|-----------|---------|----------|----------|
| ML-KEM-768 | Post-quantum key encapsulation | NIST FIPS 203 | 1,184B pubkey |
| HKDF-SHA256 | Key derivation from shared secret | RFC 5869 | 32B output |
| ChaCha20-Poly1305 | Authenticated encryption (AEAD) | RFC 8439 | 32B key |
| Gzip | Payload compression before encryption | — | Variable |

The E2EE request flow ensures no intermediary — not the Chutes API, not network providers, not the node operator — can read prompts or responses:

1. Client GETs `/e2e/instances/{chute_id}` to receive instance IDs, ML-KEM public keys, and nonces
2. Client optionally verifies attestation via Intel DCAP (SHA256 of nonce concatenated with public key)
3. Client generates an ephemeral ML-KEM keypair, encapsulates a shared secret with the instance's public key, derives a symmetric key via HKDF, compresses with Gzip, and encrypts with ChaCha20-Poly1305
4. Encrypted blob POSTed to `/e2e/invoke`
5. API validates nonce via atomic Redis Lua script, re-encrypts with mTLS, and forwards to instance
6. Instance ML-KEM decapsulates, ChaCha20 decrypts and verifies the authentication tag, runs inference
7. Instance encrypts response using client's ephemeral public key
8. Client decapsulates with ephemeral private key and decompresses

**Double key exchange** ensures every request-response pair uses entirely independent key material. Compromising one exchange reveals nothing about any other. Inside each GPU instance, the **Aegis** runtime manages encryption keys: the ML-KEM-768 keypair is generated at instance startup inside the TEE, the private key never leaves the enclave, and per-request E2EE contexts provide key isolation between concurrent requests. All derived keys are explicitly zeroed after use.

Integration for Python developers is a single-line transport swap:

```python
import httpx
from openai import OpenAI
from chutes_e2ee import ChutesE2EETransport

client = OpenAI(
    api_key="cpk_...",
    base_url="https://llm.chutes.ai/v1",
    http_client=httpx.Client(
        transport=ChutesE2EETransport(api_key="cpk_..."),
    ),
)
```

For non-Python clients, a local OpenResty-based proxy (`docker run -p 8443:443 parachutes/e2ee-proxy:latest`) handles encryption transparently.

### 1.3.3 TEE: Intel TDX + NVIDIA CC

The `sek8s` repository provides secure Kubernetes for TEE-enabled workloads. It builds encrypted TDX VM images containing k3s, attestation agents, and GPU drivers. The attestation flow binds hardware identity to cryptographic capability:

1. TDX module measures firmware, bootloader, and kernel, storing measurements in RTMRs
2. Validator generates random nonce and sends to miner
3. Miner requests TD Quote from CPU (signed by CPU-fused key)
4. GPU attestation report gathered via NVIDIA SDK
5. Validator verifies Intel signature, checks debug mode is disabled, validates nonce binding, compares measurements to golden config
6. LUKS disk decryption key released only after successful attestation

The trust boundaries are absolute: the host OS and hypervisor cannot inspect TEE contents (Intel TDX with MKTME); the GPU bus is encrypted (NVIDIA PPCIe with AES-256-GCM); only signed, attested images execute (Cosign + OPA admission control); and model weights in VRAM are hardware-encrypted on H100/H200/B200 GPUs via NVIDIA Confidential Computing mode.

---

## 1.4 Economic Model

### 1.4.1 `$TAO` on Bittensor Subnet 64

Chutes operates on **Bittensor Subnet 64** (SN64), launched in late January 2025. Following the Dynamic TAO (dTAO) upgrade in February 2025, it became one of the top subnets by emissions. The subnet processes 100 billion tokens daily across 8,000+ GPU nodes worldwide, serving 3,000+ enterprise clients.

Bittensor's four-layer architecture positions SN64 in the Execution Layer alongside other specialized subnets. The Funding Layer (Root Subnet, SN0) allocates TAO emissions to subnets based on stake-weighted inflows. Yuma Consensus — a stake-weighted median consensus algorithm running on-chain every epoch — aggregates validator scores to distribute rewards.

**TAO Tokenomics:**

| Parameter | Value |
|-----------|-------|
| Maximum Supply | 21,000,000 TAO (hard cap, no pre-mine, no VC) |
| Block Time | ~12 seconds |
| Pre-Halving Emission | 1 TAO per block (~7,200 TAO/day) |
| Post-Halving Emission | 0.5 TAO per block (~3,600 TAO/day) |
| First Halving | December 14, 2025 |
| Circulating Supply | ~9.6 million TAO |
| Staked Supply | ~71% of circulating |

Chutes receives approximately 9.3% of network emissions, yielding ~335 TAO daily (~$83,750 at $250/TAO). With competitive hardware, an H100 can earn an estimated 1.7–17 TAO per day ($425–$4,250). Post-dTAO, emissions flow through the TAO/Alpha pool: TAO staked mints Alpha tokens, with a 10% owner cut and the remaining 90% split 50/50 between miners and validators.

### 1.4.2 Scoring: CU 55%, Invocation 25%, Unique Chute 15%, Bounty 5%

The scoring algorithm uses a **7-day rolling window** with four weighted metrics:

**Compute Units (55%)** — Total computational work combining bounties and normalized compute time:

```
compute_units = flat_bounty_sum + compute_time
compute_time = raw_time * normalized_performance * gpu_multiplier
```

Performance is normalized using the 2-day median tokens-per-second across all miners, making manipulation resistant. The GPU multiplier assigns higher weights to premium hardware (H100 > A100 > A40 > T4).

**Invocation Count (25%)** — Total successful invocations handled, filtered to exclude errors and reported invocations.

**Unique Chute Score (15%)** — Average number of unique chutes run, GPU-weighted. This metric uses a two-tier normalization: above-median miners have their normalized values raised to power 1.3; below-median miners raised to power 2.2. This super-linear penalty for low diversity incentivizes miners to run many different models rather than specializing on a few high-traffic chutes.

**Bounty Count (5%)** — Number of bounties claimed. Bounties are the primary incentive mechanism for cold-start optimization: when a new chute is deployed (or an existing one has no instances), a bounty is created and miners race to be first to deploy and provide inference. Bounty winners receive bonus compute units that count toward the 55% compute_units metric.

Normalization follows a share-of-total model for standard metrics:

```
normalized = miner_value / sum(all_miners)
```

Five anti-gaming mechanisms protect the scoring integrity: **Multi-UID punishment** (only the highest-scoring hotkey per coldkey gets rewards); **Median computation rates** (2-day median resists manipulation); **Error filtering** (only successful invocations count); **Report filtering** (reported invocations excluded); and **GPU history validation** (historical GPU counts prevent manipulation through inventory gaming).

The economic flow from user to miner:

```
User pays TAO (per query/token)
       |
       v
Chutes validator collects fees
       |
       v
Auto-staked to buy back subnet token
       |
       +---> Validator take (0% on chutes.ai hotkey)
       +---> Miner rewards (proportional to 7-day compute)
       +---> Subnet token value increase
```

The **cost advantage** is substantial: Chutes is approximately 85% cheaper than AWS Lambda for comparable inference, achieved by eliminating idle-capacity costs through serverless architecture and by sourcing GPU supply from a competitive decentralized marketplace rather than centralized data center provisioning.

### 1.4.3 HelixCluster Integration: Go MinerController

The HelixCluster integration centers on deploying the `chutes-miner` stack through a Kubernetes-native Go controller that manages the full miner lifecycle. The `MinerController` type encapsulates all deployment operations:

```go
package chutes

import (
    "context"
    "fmt"
    "time"

    appsv1 "k8s.io/api/apps/v1"
    corev1 "k8s.io/api/core/v1"
    "k8s.io/apimachinery/pkg/api/errors"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/util/wait"
    "k8s.io/client-go/kubernetes"
)

type ChutesMinerConfig struct {
    NodeID           string            `json:"node_id"`
    ValidatorHotkey  string            `json:"validator_hotkey"`
    HourlyCostUSD    float64           `json:"hourly_cost_usd"`
    GPUShortRef      string            `json:"gpu_short_ref"`
    GPUCount         int               `json:"gpu_count"`
    BittensorColdkey string            `json:"bittensor_coldkey"`
    BittensorHotkey  string            `json:"bittensor_hotkey"`
    CacheMaxSizeGB   int               `json:"cache_max_size_gb"`
    CacheMaxAgeDays  int               `json:"cache_max_age_days"`
    TEEEnabled       bool              `json:"tee_enabled"`
}

type MinerController struct {
    k8sClient      kubernetes.Interface
    namespace      string
    validators     []ValidatorConfig
    gravalVerifier *GraValVerifier
}

func NewMinerController(k8sClient kubernetes.Interface, 
                        namespace string) *MinerController {
    return &MinerController{
        k8sClient:      k8sClient,
        namespace:      namespace,
        validators:     DefaultValidators,
        gravalVerifier: NewGraValVerifier(),
    }
}

func (mc *MinerController) DeployMiner(ctx context.Context, 
                                       cfg ChutesMinerConfig) error {
    fmt.Printf("[HelixCluster] Deploying Chutes miner on node %s "+
               "(GPU: %s x%d, TEE: %v)\n",
        cfg.NodeID, cfg.GPUShortRef, cfg.GPUCount, cfg.TEEEnabled)

    if err := mc.ensureNamespace(ctx); err != nil {
        return fmt.Errorf("ensure namespace: %w", err)
    }
    if err := mc.deployPostgres(ctx, cfg); err != nil {
        return fmt.Errorf("deploy postgres: %w", err)
    }
    if err := mc.deployRedis(ctx, cfg); err != nil {
        return fmt.Errorf("deploy redis: %w", err)
    }
    if err := mc.deployGraValBootstrap(ctx, cfg); err != nil {
        return fmt.Errorf("deploy graval bootstrap: %w", err)
    }
    if err := mc.deployMinerAPI(ctx, cfg); err != nil {
        return fmt.Errorf("deploy miner api: %w", err)
    }
    if err := mc.deployGepetto(ctx, cfg); err != nil {
        return fmt.Errorf("deploy gepetto: %w", err)
    }
    if err := mc.deployRegistryProxy(ctx, cfg); err != nil {
        return fmt.Errorf("deploy registry proxy: %w", err)
    }
    if err := mc.deployGPUOperator(ctx, cfg); err != nil {
        return fmt.Errorf("deploy gpu operator: %w", err)
    }
    if err := mc.waitForReady(ctx, cfg.NodeID, 5*time.Minute); err != nil {
        return fmt.Errorf("wait for ready: %w", err)
    }

    fmt.Printf("[HelixCluster] Chutes miner deployment complete on %s\n", 
               cfg.NodeID)
    return nil
}
```

The `DeployMiner` method orchestrates nine sequential steps: namespace creation, PostgreSQL deployment, Redis deployment, GraVal bootstrap DaemonSet, miner API service (NodePort 32000), Gepetto strategy engine, registry proxy, NVIDIA GPU operator, and a readiness check. Each step is independently retryable and reports granular error context. The controller integrates with HelixCluster's existing Kubernetes infrastructure, enabling GPU nodes to simultaneously participate in Helix proof-of-work tasks and Chutes inference serving through a unified resource allocation strategy that reserves a configurable percentage of GPU capacity for each workload type.
