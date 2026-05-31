# Chutes.ai Security Architecture: E2EE, Post-Quantum Cryptography, and TEE Deep Dive

> **Research Date:** 2026-07-07 | **Phase 8, Dimension 2** | **Classification:** Technical Architecture Analysis
>
> This report provides a comprehensive analysis of Chutes.ai's groundbreaking security architecture, covering end-to-end encryption with post-quantum cryptography, GPU hardware attestation, Trusted Execution Environments, and their implications for distributed AI compute platforms.

---

## Table of Contents

1. [Executive Summary](#1-executive-summary)
2. [End-to-End Encryption (E2EE) Architecture](#2-end-to-end-encryption-e2ee-architecture)
3. [Post-Quantum Cryptography: ML-KEM-768](#3-post-quantum-cryptography-ml-kem-768)
4. [Symmetric Encryption: ChaCha20-Poly1305](#4-symmetric-encryption-chacha20-poly1305)
5. [Key Derivation: HKDF-SHA256](#5-key-derivation-hkdf-sha256)
6. [Intel TDX: Trust Domain Extensions](#6-intel-tdx-trust-domain-extensions)
7. [NVIDIA Confidential Computing (CC Mode)](#7-nvidia-confidential-computing-cc-mode)
8. [GPU Attestation: GraVal Approach](#8-gpu-attestation-graval-approach)
9. [TEE Remote Attestation Flow](#9-tee-remote-attestation-flow)
10. [Source Code Analysis](#10-source-code-analysis)
11. [Performance Impact Analysis](#11-performance-impact-analysis)
12. [Comparison with Other Platforms](#12-comparison-with-other-platforms)
13. [Attack Vectors and Mitigations](#13-attack-vectors-and-mitigations)
14. [HelixCluster Integration Recommendations](#14-helixcluster-integration-recommendations)
15. [References](#15-references)

---

## 1. Executive Summary

Chutes.ai has implemented what may be the most advanced security model in the distributed AI compute space. Its architecture combines **post-quantum end-to-end encryption**, **hardware-based Trusted Execution Environments (Intel TDX + NVIDIA CC)**, and **remote attestation** to create a verifiable zero-trust inference platform. [^3463^]

The core innovation is a cryptographic guarantee that **no intermediary -- including Chutes itself -- can read user prompts or model responses**. This is achieved through a multi-layer defense-in-depth strategy:

| Layer | Technology | Protection |
|---|---|---|
| **Transport** | TLS 1.3 | Network-level confidentiality |
| **E2EE Payload** | ML-KEM-768 + ChaCha20-Poly1305 | Application-level confidentiality |
| **TEE Memory** | Intel TDX (MKTME) | Hardware memory encryption |
| **GPU VRAM** | NVIDIA CC Mode (AES-256-GCM) | Hardware VRAM encryption |
| **Code Integrity** | Cosign + OPA Admission Controller | Only signed images execute |
| **Attestation** | Intel DCAP + NVIDIA NRAS | Hardware authenticity verification |
| **Runtime** | Aegis cryptographic library | Key isolation and zeroization |

The entire stack is **open source** and independently auditable, with evidence endpoints that allow third-party verification without trusting Chutes. [^3468^] [^3470^]

---

## 2. End-to-End Encryption (E2EE) Architecture

### 2.1 E2EE Flow Diagram

```
+-------------------------------------------------------------+
|  CLIENT SIDE                                                |
|  +------------------+    +---------------------------+      |
|  | OpenAI/Anthropic |    | E2EE Transport (Python)   |      |
|  | SDK              |--->| or E2EE Proxy (OpenResty) |      |
|  +------------------+    +---------------------------+      |
|           |                           |                     |
|           v                           v                     |
|    Plaintext request      ML-KEM-768 keypair generation     |
|                           Encapsulate shared secret         |
|                           HKDF-SHA256 derive sym key        |
|                           Gzip compress                     |
|                           ChaCha20-Poly1305 encrypt         |
|                           +---------------------------------|
|                           | Encrypted Blob Structure:       |
|                           | [ML-KEM CT][nonce][cipher][tag] |
+-------------------------------------------------------------+
                              | HTTPS + E2EE envelope
                              v
+-------------------------------------------------------------+
|  CHUTES API (Zero Trust)                                    |
|  +-----------------------------------------------------+    |
|  | Nonce validation (atomic Redis Lua script)          |    |
|  | - Check nonce exists & matches instance_id          |    |
|  | - Delete nonce atomically (single-use enforcement)  |    |
|  | - Reject with 403 if invalid/reused                 |    |
|  +-----------------------------------------------------+    |
|  | Re-encrypt for mTLS transport to instance           |    |
|  | API CANNOT read E2EE payload (opaque ciphertext)    |    |
|  +-----------------------------------------------------+    |
+-------------------------------------------------------------+
                              | mTLS tunnel
                              v
+-------------------------------------------------------------+
|  GPU INSTANCE (Intel TDX Trust Domain)                      |
|  +-----------------------------------------------------+    |
|  | Strip transport encryption                          |    |
|  | ML-KEM Decapsulate with instance private key        |    |
|  | ChaCha20-Poly1305 decrypt + verify auth tag         |    |
|  | Extract client's ephemeral response public key      |    |
|  +-----------------------------------------------------+    |
|  | RUN INFERENCE (model executes on decrypted prompt)  |    |
|  +-----------------------------------------------------+    |
|  | Encrypt response:                                   |    |
|  | - ML-KEM Encapsulate using client response_pk       |    |
|  | - HKDF derive response key                          |    |
|  | - ChaCha20-Poly1305 encrypt                         |    |
|  +-----------------------------------------------------+    |
+-------------------------------------------------------------+
                              | Encrypted response
                              v
+-------------------------------------------------------------+
|  CLIENT SIDE                                                |
|  +-----------------------------------------------------+    |
|  | Extract ML-KEM ciphertext                           |    |
|  | Decapsulate shared_secret with response_sk          |    |
|  | Derive symmetric key via HKDF                       |    |
|  | Decrypt + Gzip decompress                           |    |
|  | Plaintext response                                  |    |
|  +-----------------------------------------------------+    |
+-------------------------------------------------------------+
```

### 2.2 E2EE Protocol Steps (Detailed)

**Step 1: Instance Discovery**
```
GET /e2e/instances/{chute_id}

Response: {
  instance_ids: [...],
  ml_kem_pubkeys: [...],  // Base64-encoded ML-KEM-768 public keys
  nonces: [...]           // Single-use nonces for replay prevention
}
```

**Step 2: Optional TEE Attestation Verification**
```
GET /instances/{id}/attestation?nonce={random_32byte_hex}

Response: {
  tdx_quote: "...",           // Intel TDX quote signed by CPU
  gpu_evidence: "...",        // NVIDIA attestation report
  e2e_pubkey: "...",          // ML-KEM public key
  certificate: "..."          // TLS certificate
}

// Verification:
// 1. Verify TDX quote via Intel DCAP
// 2. Verify GPU evidence via NVIDIA Attestation SDK
// 3. Check report_data contains SHA256(nonce || e2e_pubkey)
// 4. This proves the key was generated INSIDE a genuine TEE
```

**Step 3: Request Encryption (Per Request)**
1. Generate ephemeral ML-KEM-768 keypair (response_pk, response_sk)
2. Encapsulate shared_secret using instance's public key -> ML-KEM ciphertext
3. Derive request_key = HKDF-SHA256(shared_secret, salt=CT[:16], info="e2e-req-v1")
4. Inject client's ephemeral response_pk into JSON payload
5. Gzip compress JSON
6. Generate random 12-byte nonce
7. ChaCha20-Poly1305 encrypt with request_key
8. Assemble blob: `[ML-KEM CT (1088b)][nonce (12b)][ciphertext][auth tag (16b)]`

**Step 4: Streaming Protocol**
- Non-streaming: full ML-KEM encapsulation for response
- Streaming: single `e2e_init` SSE event with ML-KEM ciphertext, then per-chunk ChaCha20-Poly1305 encryption with derived stream key
- Stream end: all key material explicitly wiped [^3463^]

### 2.3 Trust Boundaries

| Component | Can See Plaintext? | What It Sees |
|---|---|---|
| **Your machine** | **Yes** | Your prompt and the response |
| **Chutes API** | **No** | Opaque ciphertext, routing headers, nonce tokens, usage metadata |
| **Network intermediaries** | **No** | TLS-encrypted ciphertext containing E2EE-encrypted ciphertext |
| **GPU instance (TEE)** | **Yes** | Decrypted prompt + response inside hardware-isolated enclave |
| **Host OS / hypervisor** | **No** | Hardware-encrypted memory; cannot inspect TEE contents |
| **Chutes engineers** | **No** | No access to TEE memory; no logging of plaintext |

The API sees only `{"usage": {"prompt_tokens": 50, "completion_tokens": 200}}` metadata extracted from within the TEE for billing. [^3463^]

---

## 3. Post-Quantum Cryptography: ML-KEM-768

### 3.1 ML-KEM-768 Specification (FIPS 203)

ML-KEM-768 (formerly CRYSTALS-Kyber) is a lattice-based key encapsulation mechanism standardized by NIST in August 2024 as FIPS 203. Its security rests on the hardness of the **Module Learning With Errors (MLWE)** problem, believed to be resistant to both classical and quantum attacks. [^3504^] [^3507^]

**Why Post-Quantum Matters:** The "harvest now, decrypt later" threat means adversaries could record encrypted traffic today and decrypt it once quantum computers capable of running Shor's algorithm become available. By using ML-KEM-768 today, traffic captured in 2025 remains secure even against future quantum attacks. [^3463^]

### 3.2 ML-KEM Parameter Sets

| Parameter | ML-KEM-512 | ML-KEM-768 | ML-KEM-1024 |
|---|---|---|---|
| **NIST Security Level** | Level 1 | Level 3 | Level 5 |
| **Equivalent Classical** | AES-128 | AES-192 | AES-256 |
| **Public Key Size** | 800 bytes | **1,184 bytes** | 1,568 bytes |
| **Private Key Size** | 1,632 bytes | **2,400 bytes** | 3,168 bytes |
| **Ciphertext Size** | 768 bytes | **1,088 bytes** | 1,568 bytes |
| **Shared Secret** | 32 bytes | **32 bytes** | 32 bytes |
| **Required RBG Strength** | 128-bit | **192-bit** | 256-bit |

Chutes uses **ML-KEM-768** (NIST Level 3), providing approximately 192 bits of classical security equivalent to AES-192. [^3504^] [^3505^]

### 3.3 Comparison: ML-KEM-768 vs. Traditional Key Exchange

| Feature | ML-KEM-768 | RSA-2048 | ECDH (X25519) |
|---|---|---|---|
| **Mathematical Foundation** | Module-LWE (lattices) | Integer factorization | Elliptic curve DLP |
| **Quantum Resistance** | **Yes** | No | No |
| **Public Key Size** | 1,184 bytes | 256 bytes | 32 bytes |
| **Ciphertext Size** | 1,088 bytes | 256 bytes | 32 bytes |
| **KeyGen Performance** | ~142,000 ops/s | ~1,000 ops/s | ~50,000 ops/s |
| **Encaps Performance** | ~103,000 ops/s | ~500 ops/s | ~50,000 ops/s |
| **Decaps Performance** | ~134,000 ops/s | ~15,000 ops/s | ~50,000 ops/s |
| **Constant-Time** | **Yes (by design)** | Implementation-dependent | **Yes (by design)** |
| **Side-Channel Resistance** | Strong | Vulnerable to timing attacks | Strong |
| **NIST Standard** | FIPS 203 (Aug 2024) | FIPS 186-5 | SP 800-186 |

*Performance data from AMD Ryzen 7 7700, single-core operations per second. [^3507^]*

**Key Insight:** ML-KEM-768 is actually **faster** than RSA-2048 for both encapsulation and decapsulation, despite larger key sizes. Compared to ECDH, it trades larger keys (37x) for quantum resistance while maintaining comparable performance. The 1,184-byte public key still fits within a single TCP packet (MTU ~1,500 bytes), avoiding fragmentation issues. [^3506^]

### 3.4 ML-KEM-768 in Chutes: Implementation Details

Every E2EE request uses a **fresh ephemeral ML-KEM-768 keypair** on the client side. This provides:
- **Forward secrecy:** Compromising one exchange reveals nothing about others
- **Independent key material:** Each request-response pair uses entirely independent keys
- **Double key exchange:** Request uses client->instance encapsulation; response uses instance->client encapsulation via embedded response_pk [^3463^]

---

## 4. Symmetric Encryption: ChaCha20-Poly1305

### 4.1 Why ChaCha20-Poly1305 Over AES-GCM

Chutes chose ChaCha20-Poly1305 as the authenticated encryption cipher for several reasons: [^3463^] [^3509^]

| Feature | ChaCha20-Poly1305 | AES-256-GCM |
|---|---|---|
| **Hardware dependency** | Fast in software (no AES-NI needed) | Requires AES-NI for optimal performance |
| **Timing side-channels** | **Resistant by design** (ARX operations) | Vulnerable in software implementations |
| **TEE context performance** | Consistent across TEE types | Variable without hardware acceleration |
| **Nonce size** | 96 bits (12 bytes) | 96 bits (12 bytes) |
| **Tag size** | 128 bits (16 bytes) | 128 bits (16 bytes) |
| **Key size** | 256 bits (32 bytes) | 256 bits (32 bytes) |
| **Mobile/ARM performance** | **3x faster** than AES-128-GCM | Slower without dedicated hardware |
| **RFC Standard** | RFC 8439 | NIST SP 800-38D |

**Cloudflare benchmark:** Decrypting a 1MB file on Galaxy Nexus: AES-128-GCM took 41.6ms vs. ChaCha20-Poly1305 at 13.2ms -- a **3x speedup** on mobile devices. [^3509^]

### 4.2 Encrypted Payload Structure

```
[ML-KEM Ciphertext: 1088 bytes]
[Nonce: 12 bytes]
[Ciphertext: variable length]
[Authentication Tag: 16 bytes]
--------------------------------
Total overhead per request: ~1,116 bytes + ciphertext
```

The encryption uses a random 12-byte nonce for each operation. Gzip compression is applied before encryption to reduce bandwidth and eliminate information leakage from ciphertext length variations. [^3463^]

---

## 5. Key Derivation: HKDF-SHA256

### 5.1 HKDF Specification (RFC 5869)

HKDF (HMAC-based Extract-and-Expand Key Derivation Function) follows a two-phase paradigm: [^3520^]

**Phase 1: Extract**
```
PRK = HMAC-Hash(salt, IKM)
```
Concentrates dispersed entropy from the input keying material (the ML-KEM shared secret) into a short, cryptographically strong pseudorandom key.

**Phase 2: Expand**
```
T(1) = HMAC-Hash(PRK, "" | info | 0x01)
T(2) = HMAC-Hash(PRK, T(1) | info | 0x02)
...
OKM = first L octets of T(1) | T(2) | ...
```
Stretches the PRK into the desired output length using domain-specific info strings.

### 5.2 Chutes Key Derivation Scheme

```python
request_key  = HKDF-SHA256(shared_secret, salt=mlkem_ct[:16], info="e2e-req-v1")
response_key = HKDF-SHA256(shared_secret, salt=mlkem_ct[:16], info="e2e-resp-v1")
stream_key   = HKDF-SHA256(shared_secret, salt=mlkem_ct[:16], info="e2e-stream-v1")
```

**Design rationale:**
- **Salt = first 16 bytes of ML-KEM ciphertext:** Binds the derived key to the specific encapsulation, providing context separation
- **Domain separation via info strings:** Even if the same shared secret were somehow reused, the derived keys are cryptographically independent across request/response/stream purposes
- **32-byte output:** Matches ChaCha20-Poly1305 key size [^3463^] [^3518^]

---

## 6. Intel TDX: Trust Domain Extensions

### 6.1 TDX Architecture Overview

Intel Trust Domain Extensions (TDX) is an architectural extension available in 4th Generation Intel Xeon Scalable Processors and later. It creates **Trust Domains (TDs)** -- virtual machines that run in Secure-Arbitration Mode (SEAM) with encrypted CPU state and memory. [^3503^]

**Key components:**
- **MKTME (Multi-Key Total Memory Encryption):** Hardware memory encryption using AES-XTS-128, with unique encryption keys per Trust Domain
- **TDX Module:** Intel-signed software module that manages TD lifecycle, memory isolation, and attestation
- **RTMRs (Runtime Measurement Registers):** Cryptographic measurements of firmware, bootloader, kernel stored in CPU registers
- **SEAM range:** Protected memory region inaccessible to hypervisor

### 6.2 Memory Protection Model

```
Normal Mode                          TDX Mode
+---------------+                   +------------------+
| Host OS       |                   | Host OS/Hypervisor|
| (untrusted)   |                   | (untrusted)      |
|               |                   | Cannot access TD |
+---------------+                   | memory/registers |
| App memory    |                   +------------------+
| (plaintext    |                   | Trust Domain     |
|  in RAM)      |                   | (encrypted mem)  |
+---------------+                   | - CPU state      |
                                    | - RAM contents   |
                                    | - Registers      |
                                    +------------------+
```

**Critical property:** Even physical access to the server's RAM cannot extract key material from a Trust Domain. Memory is encrypted with AES-XTS-128 using keys known only to the CPU. [^3503^] [^3471^]

### 6.3 TDX Attestation Flow

1. **Measurement during boot:** The TDX module measures firmware, bootloader, kernel, and other critical components into RTMR registers
2. **Quote generation:** The CPU generates a TD Quote cryptographically signed by a **private key fused into the CPU itself**
3. **Nonce binding:** The validator provides a random nonce included in the quote (prevents replay attacks)
4. **Verification:** The validator checks the signature using Intel's public keys and compares RTMR values against known-good "golden" configurations
5. **LUKS key release:** Only after successful attestation is the disk decryption key released, enabling boot [^3467^] [^3503^]

**Supported CPUs for Chutes TEE:**
- Intel Emerald Rapids (5th Gen Xeon Scalable)
- Intel Granite Rapids (6th Gen Xeon Scalable) [^3525^]

---

## 7. NVIDIA Confidential Computing (CC Mode)

### 7.1 CC Mode Architecture

NVIDIA Confidential Computing on H100/H200/B200 GPUs provides hardware-level protection for AI workloads through three key mechanisms: [^3515^] [^3566^]

**1. VRAM Encryption (AES-256-GCM)**
- Every write to HBM is encrypted using AES-256-GCM by a dedicated **Confidential Computing Engine (CCE)** integrated on the GPU die
- The encryption key is generated inside the GPU security processor during initialization and **never leaves the chip**
- Host software (hypervisor, CUDA driver, management plane) cannot access the key or read plaintext VRAM
- Hardware-accelerated encryption/decryption is decoupled from compute pipeline (tensor cores, SMs)

**2. PCIe Bus Encryption (Protected PCIe / PPCIE)**
- All CPU-GPU data transfers over PCIe are encrypted
- On Intel systems: uses TDX memory encryption engine
- On AMD systems: uses SEV-SNP
- Prevents cold-boot attacks and DMA analyzers on the PCIe bus

**3. GPU Remote Attestation**
- GPU produces cryptographically signed attestation reports
- Verified via NVIDIA Remote Attestation Service (NRAS) or local SDK
- Reports contain GPU identity, firmware version, CC mode status

### 7.2 GPU Support Matrix

| GPU | Architecture | VRAM Encryption | PCIe Encryption | NVLink Encryption | Attestation |
|---|---|---|---|---|---|
| H100 SXM5/PCIe | Hopper | AES-256-GCM | Yes (TDX/SEV-SNP) | No | NRAS / Local SDK |
| H200 SXM5 | Hopper | AES-256-GCM | Yes (TDX/SEV-SNP) | No | NRAS / Local SDK |
| B200 SXM6 | Blackwell | AES-256-GCM | Yes (PCIe 5.0 enc.) | **Yes** | NRAS / Local SDK |
| GB200 | Grace-Hopper | AES-256-GCM | Yes (unified memory) | **Yes** | NRAS + CPU TEE |
| A100/Ampere | Ampere | **No** | **No** | **No** | Not supported |

**Key insight:** CC mode requires Hopper architecture or newer. A100 and older GPUs lack the on-die security processor and cannot run CC mode. [^3515^] [^3525^]

### 7.3 Performance Overhead of CC Mode

| Workload Type | CC Mode Overhead | Notes |
|---|---|---|
| **LLM inference (large compute/I/O ratio)** | **2-5%** | Encryption parallel to compute pipeline |
| **Model loading/swap** | **20-30%** latency increase | Additional encryption for data transfer |
| **CPU-GPU interconnect** | ~4 GB/s limit | CPU encryption performance bottleneck |
| **Multi-GPU NVLink (B200+)** | Minimal with HW encryption | NVLink encryption in hardware |
| **BERT LLM Inference** | Near-zero | High compute-to-data ratio [^3565^] |

NVIDIA benchmarks show CC mode overhead is **under 3%** for large matrix operations typical of transformer inference. The overhead reduces toward zero as model size grows because compute dominates over I/O. [^3565^] [^3572^]

A comprehensive study (ICDCS 2025) found that in model-swapping scenarios, throughput in No-CC mode is 45-70% higher than CC mode, primarily due to model loading overhead. However, the actual inference processing rate remains consistent -- the bottleneck is loading, not computation. [^3561^] [^3562^]

---

## 8. GPU Attestation: GraVal Approach

### 8.1 GraVal: "Proof of Consecutive VRAM Work"

GraVal (graval-priv) is Chutes' proprietary GPU attestation scheme that provides **Proof of Consecutive VRAM Work** to cryptographically attest to GPU physical properties. It uses OpenCL and the clBLAS library for broad compatibility with GPUs from different manufacturers (NVIDIA and AMD). [^3467^] [^3471^]

**How it works:**
1. The validator issues a challenge to perform a series of consecutive matrix multiplications on the GPU
2. The GPU performs the operations using diagonal memory slices from the matrices
3. This drastically reduces data transfer overhead while retaining cryptographic proof that the full multiplication occurred
4. The time taken, combined with memory access patterns, provides a hardware-level signature of the GPU's processing speed and available VRAM
5. The process also creates a **unique AES-256 encryption key** based on the GPU's UUID and a random challenge, tying the secure communication channel to verified physical hardware

**Purpose:** Prevents miners from fraudulently claiming more powerful GPUs than they actually possess. A T4 cannot fake the performance signature of an H100 because the matrix multiplication time and VRAM access patterns are hardware-specific. [^3467^]

### 8.2 GraVal vs. NVIDIA Hardware Attestation

| Aspect | GraVal (Software) | NVIDIA CC Attestation (Hardware) |
|---|---|---|
| **Verification type** | Performance benchmark | Cryptographic signed report |
| **GPU support** | NVIDIA + AMD | NVIDIA Hopper+ only |
| **Tamper resistance** | Medium (can be emulated with effort) | **High** (signed by GPU security processor) |
| **VRAM verification** | Indirect (via computation time) | **Direct** (hardware-encrypted) |
| **Identity proof** | Performance fingerprint | **Cryptographic identity** |
| **Use in Chutes** | All chutes (baseline) | TEE-enabled chutes (enhanced) |

In TEE mode, GraVal is augmented by NVIDIA's signed attestation report, providing dual verification: performance-based + hardware-cryptographic. [^3467^]

---

## 9. TEE Remote Attestation Flow

### 9.1 Complete Attestation Sequence

```
  Validator (Chutes)                      Miner Node (sek8s)
        |                                         |
        |  1. Generate random nonce              |
        |---------------------------------------->|
        |                                         |
        |  2. Request TD Quote + GPU evidence    |
        |     (nonce included in quote)          |
        |                                         |-- CPU generates TD Quote
        |                                         |   (signed by CPU-fused key)
        |                                         |   containing RTMR measurements
        |                                         |   + SHA256(nonce || e2e_pubkey)
        |                                         |
        |                                         |-- GPU generates attestation
        |                                         |   report via NVIDIA SDK
        |                                         |
        |  3. Return evidence                    |
        |<----------------------------------------|
        |                                         |
        |  4. Verify TDX Quote:                  |
        |     - Check Intel signature            |
        |     - Verify nonce binding             |
        |     - Compare RTMRs to golden config   |
        |                                         |
        |  5. Verify GPU Evidence:               |
        |     - Check via NVIDIA NRAS/SDK        |
        |     - Confirm GPU identity (H100 etc.) |
        |     - Validate CC mode enabled         |
        |                                         |
        |  6. ALL PASS -> Issue launch token     |
        |---------------------------------------->|
        |                                         |
        |  7. LUKS key released                  |
        |     (VM can now decrypt root fs)       |
```

### 9.2 Third-Party Independent Verification

Chutes provides public endpoints for independent verification: [^3468^] [^3470^]

```bash
# Get accepted measurements for all hardware configurations
curl https://api.chutes.ai/servers/tee/measurements

# Returns: accepted RTMR values and expected GPU configs per hardware profile
```

The verification process:
1. Caller generates random 32-byte nonce (hex-encoded)
2. Requests evidence from Chutes API with nonce
3. Receives TDX quote + GPU evidence with `report_data` containing `SHA256(nonce + e2e_pubkey)`
4. Verifies TDX quote via Intel DCAP (independent of Chutes)
5. Verifies GPU evidence via NVIDIA Attestation SDK (independent of Chutes)
6. Confirms the E2E public key was generated inside the attested TEE

This allows **any third party to independently verify TEE integrity without trusting Chutes**. [^3468^]

---

## 10. Source Code Analysis

### 10.1 chutes-e2ee-transport (Python) -- crypto.py

The Python transport uses the `pqcrypto` library for ML-KEM-768 operations and `cryptography` (hazmat) for HKDF and ChaCha20-Poly1305. [^3551^]

```python
# Core constants
MLKEM_CT_SIZE = 1088       # ML-KEM-768 ciphertext size
TAG_SIZE = 16               # Poly1305 auth tag
INFO_REQ = b"e2e-req-v1"    # Domain separation for request key
INFO_RESP = b"e2e-resp-v1"  # Domain separation for response key
INFO_STREAM = b"e2e-stream-v1"  # Domain separation for stream key

# Key derivation using HKDF-SHA256
def derive_key(shared_secret, mlkem_ct, info):
    return HKDF(
        algorithm=hashes.SHA256(),
        length=32,
        salt=mlkem_ct[:16],    # First 16 bytes of ciphertext as salt
        info=info,              # Domain-specific info string
    ).derive(shared_secret)

# Request encryption flow
def build_e2ee_request(e2e_pubkey_b64, payload):
    # 1. Generate ephemeral keypair for response
    response_pk, response_sk = mlkem_generate_keypair()
    
    # 2. Encapsulate shared secret with instance's public key
    e2e_pubkey = base64.b64decode(e2e_pubkey_b64)
    mlkem_ct, shared_secret = mlkem_encrypt(e2e_pubkey)
    
    # 3. Derive symmetric key via HKDF-SHA256
    sym_key = derive_key(shared_secret, mlkem_ct, INFO_REQ)
    
    # 4. Embed response public key in payload
    payload_with_pk = {**payload, "e2e_response_pk": base64.b64encode(response_pk)}
    
    # 5. Gzip compress -> encrypt
    compressed = gzip.compress(json.dumps(payload_with_pk).encode())
    nonce = os.urandom(12)
    ciphertext, tag = chacha_encrypt(sym_key, nonce, compressed)
    
    # 6. Assemble blob: [CT][nonce][cipher][tag]
    blob = mlkem_ct + nonce + ciphertext + tag
    return E2EERequestResult(blob=blob, response_sk=response_sk)
```

**Code Quality Assessment:**
- Clean separation of concerns (key derivation, encryption, request building)
- Proper use of domain separation via HKDF info strings
- Secure random nonce generation via `os.urandom(12)`
- Ephemeral keypairs per request (forward secrecy)
- Response public key embedded in encrypted payload (prevents MITM on response)

### 10.2 e2ee-proxy (OpenResty/Lua) -- e2ee_crypto.lua

The proxy uses FFI bindings to a native C library (`libe2ee_proxy.so`) with xVMP obfuscation for key material protection: [^3568^]

```lua
-- FFI bindings to native C crypto library
ffi.cdef[[
    int e2ee_mlkem_keygen(uint8_t *pk, uint8_t *sk);
    int e2ee_mlkem_encapsulate(const uint8_t *pk, uint8_t *ct, uint8_t *ss);
    int e2ee_mlkem_decapsulate(const uint8_t *sk, const uint8_t *ct, uint8_t *ss);
    int e2ee_hkdf_sha256(const uint8_t *ikm, size_t ikm_len, 
                         const uint8_t *salt, size_t salt_len,
                         const uint8_t *info, size_t info_len,
                         uint8_t *okm, size_t okm_len);
    int e2ee_chacha20_seal(const uint8_t key[32], const uint8_t nonce[12],
                           const uint8_t *plaintext, size_t pt_len,
                           uint8_t *ciphertext, uint8_t tag[16]);
    int e2ee_chacha20_open(const uint8_t key[32], const uint8_t nonce[12],
                           const uint8_t *ciphertext, size_t ct_len,
                           const uint8_t tag[16], uint8_t *plaintext);
]]
```

**Key design choices:**
- Native C implementation with xVMP (virtual machine protection) obfuscation
- Key material never exposed as plaintext in memory outside protected code paths
- Same cryptographic primitives as Python transport (interoperability guaranteed)
- OpenResty handles high-throughput concurrent connections efficiently

### 10.3 E2EE Proxy Architecture

The proxy (OpenResty-based) provides: [^3469^]
- Transparent interception of OpenAI/Anthropic API requests
- Automatic key exchange, encryption, nonce management
- Format translation between OpenAI Chat Completions, OpenAI Responses, and Anthropic Messages APIs
- Streaming decryption on-the-fly
- Automatic retry on nonce expiry
- Drop-in replacement: just change `base_url` to `https://e2ee-local-proxy.chutes.dev:8443`

---

## 11. Performance Impact Analysis

### 11.1 E2EE Cryptographic Overhead

| Operation | Latency | Notes |
|---|---|---|
| ML-KEM-768 key generation | ~7 microseconds | On AMD Ryzen 7 7700 |
| ML-KEM-768 encapsulation | ~10 microseconds | Per-request |
| ML-KEM-768 decapsulation | ~7 microseconds | Inside TEE |
| Full hybrid handshake (X25519+ML-KEM-768) | **243 microseconds** | Python implementation [^3559^] |
| HKDF-SHA256 key derivation | ~5 microseconds | Negligible |
| ChaCha20-Poly1305 encryption | ~1 microsec/1KB | Data-size dependent |
| Gzip compression | Variable | Prompt-size dependent |

**Total E2EE overhead per request: ~0.3-0.5ms** for the cryptographic handshake, plus streaming encryption/decryption proportional to payload size. For LLM inference where TTFT is typically 50-500ms, this overhead is **negligible (< 1%)**. [^3559^]

### 11.2 TEE Performance Overhead Summary

| Scenario | CC Mode Overhead | Key Bottleneck |
|---|---|---|
| **Single-model inference (steady-state)** | 2-5% throughput | VRAM encryption (parallel to compute) |
| **Model swapping (multi-tenant)** | 20-30% latency, 45-70% throughput | Model loading encryption |
| **Short sequences (< 100 tokens)** | 5-7% | I/O overhead more visible |
| **Long sequences (500+ tokens)** | 2-3% | Compute dominates |
| **Large models (70B+)** | Near-zero | Compute fully dominates I/O |
| **Multi-GPU training (DDP)** | 1.6-16.8x | All-reduce encryption overhead [^3573^] |

### 11.3 Practical Impact on Inference Latency

For a typical Chutes E2EE inference request:
```
Component                    Latency
---------------------------------------------
E2EE handshake (ML-KEM+HKDF)  ~0.3 ms
Network round-trip            ~20-100 ms
TEE processing/decryption     ~1-2 ms
Model inference (TTFT)        ~50-500 ms
E2EE response encryption      ~0.1 ms per chunk
---------------------------------------------
Total E2EE overhead:           < 1% of total latency
```

The E2EE cryptographic overhead is dominated by network latency and model inference time. For streaming responses, per-chunk ChaCha20-Poly1305 encryption adds negligible overhead (~1 microsecond per KB of output tokens).

---

## 12. Comparison with Other Platforms

### 12.1 Security Feature Comparison

| Feature | Chutes.ai | Akash Network | Spheron | Phala Network | Traditional Cloud |
|---|---|---|---|---|---|
| **End-to-End Encryption** | **ML-KEM-768 + ChaCha20** | TLS only | TLS + optional CC | TLS + SGX | TLS only |
| **Post-Quantum Crypto** | **Yes (ML-KEM-768)** | No | No | No | No |
| **CPU TEE** | **Intel TDX** | No | Intel TDX/AMD SEV | Intel SGX | Azure/GCP TEE |
| **GPU TEE** | **NVIDIA CC Mode** | No | NVIDIA CC Mode | No | Limited |
| **GPU VRAM Encryption** | **Yes (AES-256-GCM)** | No | Yes | N/A | Limited |
| **Remote Attestation** | **Intel DCAP + NVIDIA NRAS** | Basic | Intel + NVIDIA | SGX attestation | Cloud-managed |
| **Independent Verification** | **Yes (public endpoints)** | No | Partial | Yes | No |
| **Open Source TEE Stack** | **Yes (sek8s)** | Partial | No | Yes | No |
| **Code Signing** | **Cosign (Sigstore)** | No | No | No | Varies |
| **Continuous Monitoring** | **Watchtower** | No | No | No | Varies |
| **Anonymous Miners** | **Yes** | Yes | No | Yes | N/A |

### 12.2 Key Differentiators

1. **Only platform with post-quantum E2EE for AI inference** -- Chutes is unique in deploying ML-KEM-768 for production AI workloads
2. **Independent third-party verification** -- Public attestation endpoints allow anyone to verify without trusting Chutes
3. **Full hardware chain of trust** -- Intel TDX (CPU) + NVIDIA CC (GPU) + Cosign (code) creates end-to-end hardware verification
4. **Open source entire stack** -- Both sek8s (TEE infrastructure) and chutes-api (verification) are open source [^3470^]
5. **LUKS-encrypted root FS with attestation-gated decryption** -- VM cannot boot if modified [^3471^]

---

## 13. Attack Vectors and Mitigations

### 13.1 Comprehensive Attack Matrix

| Attack Vector | Description | Standard Mitigation | TEE Mitigation |
|---|---|---|---|
| **Eavesdropping (network)** | Intercept traffic between client and API | TLS 1.3 | E2EE (API sees only ciphertext) |
| **API compromise** | Chutes API server compromised | Rate limiting, auth | E2EE payload remains encrypted |
| **Host OS compromise** | Miner's host OS rooted | Container isolation | **Hardware-blocked** (TDX memory encryption) |
| **Hypervisor attack** | Malicious hypervisor inspects VM | None (by design) | **Hardware-blocked** (TDX removes hypervisor from trust boundary) |
| **VRAM extraction** | Physical attack to read GPU memory | None | **Hardware-blocked** (CC mode AES-256-GCM encryption) |
| **PCIe bus sniffing** | Cold-boot / DMA attack on PCIe | None | **Hardware-blocked** (Protected PCIe encryption) |
| **GPU fraud** | Claim H100, run T4 | GraVal performance benchmark | NVIDIA hardware attestation report |
| **Attestation forgery** | Fake TEE evidence | N/A | **Hardware-signed** quotes (CPU-fused key) |
| **Replay attack** | Reuse old nonce/quote | None | Atomic Redis Lua script + nonce freshness |
| **Code tampering** | Modify chute source code | `inspecto` bytecode hash | Cosign admission controller + immutable FS |
| **Model substitution** | Use cheaper/quantized model | `watchtower` random weight slice hash | TEE isolation prevents tampering |
| **Malicious chute** | Chute logs/exfiltrates data | cfsv filesystem validation | Egress control (net-nanny) + TEE isolation |
| **Rollback attack** | Force old vulnerable version | Validator source of truth | Admission controller blocks old images |
| **Side-channel attack** | Timing analysis of crypto | Constant-time implementations | TEE performance counter restrictions |

### 13.2 Nonce Replay Prevention (Atomic Redis Lua)

The critical anti-replay mechanism: [^3463^]

```
1. API receives request with X-E2E-Nonce header
2. Atomic Redis Lua script executes:
   a. Check nonce exists in Redis
   b. Verify nonce matches instance_id
   c. Verify nonce hasn't been used
   d. DELETE nonce atomically (single-use enforcement)
3. If any check fails: reject with HTTP 403
4. If valid: proceed with request forwarding
```

This ensures each nonce can only be used once, preventing replay attacks where an adversary resends a captured encrypted request.

---

## 14. HelixCluster Integration Recommendations

### 14.1 Cryptographic Stack Integration

**Recommendation: Adopt Chutes' ML-KEM-768 + ChaCha20-Poly1305 E2EE pattern**

```
HelixCluster E2EE Integration:

1. Client SDK (Python/JS/Rust)
   - ML-KEM-768 keypair generation (liboqs or pqcrypto)
   - ChaCha20-Poly1305 encryption (libsodium or ring)
   - HKDF-SHA256 key derivation
   
2. API Gateway
   - Nonce validation (Redis/Valkey with Lua scripts)
   - Instance discovery endpoint
   - Rate limiting on encrypted endpoints
   
3. Worker Node (TEE)
   - ML-KEM-768 decapsulation inside TDX
   - ChaCha20-Poly1305 decryption
   - Inference execution
   - Response re-encryption
```

**Implementation priority:**
| Priority | Component | Effort | Impact |
|---|---|---|---|
| P0 | E2EE request/response encryption | 2-3 weeks | Critical privacy |
| P0 | Atomic nonce validation | 3-5 days | Replay prevention |
| P1 | TEE instance attestation | 3-4 weeks | Hardware trust |
| P1 | Streaming E2EE protocol | 1-2 weeks | UX parity |
| P2 | GPU attestation (GraVal-style) | 2-3 weeks | GPU fraud prevention |
| P2 | Post-quantum migration tooling | 1 week | Future-proofing |

### 14.2 TEE Infrastructure Recommendations

**Option A: Integrate sek8s (Chutes' open-source TEE stack)**
```
Benefits:
- Battle-tested for GPU inference workloads
- Intel TDX + NVIDIA CC mode support
- LUKS-encrypted root FS with attestation-gated boot
- Cosign admission controller
- k3s Kubernetes distribution inside TEE
- Public attestation verification endpoints

Integration: Deploy sek8s for TEE-capable worker nodes,
connect to HelixCluster scheduler via Kubernetes API
```

**Option B: Build custom TEE integration**
```
Components needed:
- Intel TDX host setup (emerald/granite rapids CPUs)
- NVIDIA CC mode GPU configuration (H100/H200/B200)
- KVM/QEMU TD launcher
- Custom attestation service (Intel DCAP + NVIDIA NRAS)
- LUKS key management service
- Kubernetes admission controller (OPA/Gatekeeper)

Effort: 3-6 months for production-ready stack
```

**Recommendation: Start with Option A (sek8s) for speed-to-market,** then evaluate custom build as the platform matures.

### 14.3 GPU Verification for HelixCluster

**Adapt GraVal's approach for HelixCluster worker verification:**

```python
class GPUAttestation:
    """
    Hybrid GPU verification combining performance benchmarking
    with hardware attestation (when available).
    """
    
    def verify_gpu(self, worker_id, gpu_info):
        # Phase 1: GraVal-style performance benchmark
        challenge = generate_random_challenge()
        result = run_vram_benchmark(gpu_info, challenge)
        
        # Verify computation time matches expected for claimed GPU model
        if not verify_performance_signature(result, gpu_info.model):
            return False  # GPU fraud detected
            
        # Phase 2: Hardware attestation (if CC-capable GPU)
        if gpu_info.cc_mode_supported:
            attestation = get_gpu_attestation_report(worker_id)
            if not verify_via_nvidia_nras(attestation):
                return False
                
        # Phase 3: Derive session key from GPU UUID + challenge
        session_key = derive_aes256_key(gpu_info.uuid, challenge)
        return True, session_key
```

### 14.4 Security Architecture for HelixCluster

**Proposed HelixCluster Security Layers:**

```
Layer 7: Application (E2EE via ML-KEM-768 + ChaCha20-Poly1305)
Layer 6: Presentation (TLS 1.3 for transport)
Layer 5: Session (Per-request ephemeral keys, nonce validation)
Layer 4: Transport (mTLS between API and workers)
Layer 3: Network (Egress control via net-nanny, DNS verification)
Layer 2: Data (GPU VRAM encryption via NVIDIA CC mode)
Layer 1: Physical (Intel TDX memory encryption, CPU-fused attestation keys)
Layer 0: Supply Chain (Cosign image signing, forge build verification)
```

### 14.5 Key Implementation Insights

1. **ML-KEM-768 performance is NOT a bottleneck:** At ~243 microseconds for a full handshake, the cryptographic overhead is < 1% of typical inference latency. The real production challenge is memory bandwidth, not CPU. [^3559^]

2. **CC mode inference overhead is acceptable:** 2-5% for steady-state inference, 20-30% for model-swapping scenarios. For privacy-sensitive workloads, this is a reasonable trade-off. [^3565^]

3. **Start with software attestation, upgrade to hardware:** GraVal-style performance benchmarks can be deployed immediately on any GPU. Hardware attestation (NVIDIA CC) requires Hopper+ GPUs but provides stronger guarantees.

4. **Open source the verification layer:** Following Chutes' model, publish attestation evidence endpoints and golden measurements so third parties can verify independently.

5. **The nonce management system is critical:** The atomic Redis Lua script for single-use nonce enforcement is a small but essential component for replay attack prevention.

6. **Streaming requires a different protocol:** The per-chunk ChaCha20-Poly1305 with a pre-derived stream key (via `e2e_init` event) avoids the overhead of full ML-KEM operations per token.

---

## 15. References

[^3463^]: Chutes.ai, "End-to-End Encrypted AI Inference with Post-Quantum Cryptography," March 2026. https://chutes.ai/news/end-to-end-encrypted-ai-inference-with-post-quantum-cryptography

[^3467^]: Chutes.ai Documentation, "Security Architecture / TEE Verification." https://chutes.ai/docs/core-concepts/security-architecture

[^3468^]: Chutes API GitHub, "TEE Evidence Verification." https://github.com/chutesai/chutes-api/blob/main/docs/tee-verification.md

[^3469^]: Chutes GitHub, "e2ee-proxy: OpenResty-based E2EE proxy." https://github.com/chutesai/e2ee-proxy

[^3470^]: Chutes.ai, "I built an open-source TEE stack for confidential GPU compute," May 2026. https://chutes.ai/news/i-built-an-open-source-tee-stack-for-confidential-gpu-compute

[^3471^]: Chutes.ai, "Confidential Compute for AI Inference," December 2025. https://chutes.ai/news/confidential-compute-for-ai-inference-how-chutes-delivers-verifiable-privacy-with-trusted-execution-environments

[^3503^]: arXiv, "Intel TDX Demystified: A Top-Down Approach," 2023. https://ar5iv.labs.arxiv.org/html/2303.15540

[^3504^]: Encryption Consulting, "In-Depth Overview Of FIPS 203," January 2026. https://www.encryptionconsulting.com/overview-of-fips-203/

[^3505^]: QuantumSequrity, "Understanding ML-KEM: The Future of Key Encapsulation," 2026. https://quantumsequrity.com/blog/ml-kem-explained

[^3507^]: IETF, "ML-KEM Security Considerations," RFC draft, November 2025. https://www.ietf.org/archive/id/draft-sfluhrer-cfrg-ml-kem-security-considerations-04.html

[^3509^]: Cloudflare, "Do the ChaCha: better mobile performance with cryptography," 2015. https://blog.cloudflare.com/do-the-chacha-better-mobile-performance-with-cryptography/

[^3515^]: Spheron Network, "Confidential GPU Computing on Cloud: Deploy LLMs with NVIDIA TEE," April 2026. https://www.spheron.network/blog/confidential-gpu-computing-nvidia-tee-encrypted-vram/

[^3517^]: Intel Trust Authority, "GPU Remote Attestation With Intel Trust Authority," February 2026. https://docs.trustauthority.intel.com/main/articles/articles/ita/concept-gpu-attestation.html

[^3520^]: IETF RFC 5869, "HMAC-based Extract-and-Expand Key Derivation Function (HKDF)," May 2010. https://datatracker.ietf.org/doc/html/rfc5869

[^3525^]: NVIDIA Developer Blog, "Announcing NVIDIA Secure AI General Availability," May 2025. https://developer.nvidia.com/blog/announcing-nvidia-secure-ai-general-availability/

[^3551^]: Chutes GitHub, "chutes-e2ee-transport: Python E2EE transport," 2026. https://github.com/chutesai/chutes-e2ee-transport/blob/main/src/chutes_e2ee/crypto.py

[^3556^]: Chutes GitHub, "sek8s: Secure, standalone k8s setup with TEE." https://github.com/chutesai/sek8s

[^3559^]: arXiv, "Bridging the Post-Quantum Production Gap with a Hybrid-by-Default Python Cryptography Library," May 2026. https://arxiv.org/html/2605.17061v1

[^3561^]: arXiv, "Performance of Confidential Computing GPUs (ICDCS 2025)," May 2025. https://ui.adsabs.harvard.edu/abs/2025arXiv250516501M/abstract

[^3565^]: NVIDIA, "Confidential Compute on NVIDIA Hopper H100 Whitepaper." https://images.nvidia.com/aem-dam/en-zz/Solutions/data-center/HCC-Whitepaper-v1.0.pdf

[^3566^]: NVIDIA Developer Blog, "Confidential Computing on NVIDIA H100 GPUs for Secure and Trustworthy AI," August 2023. https://developer.nvidia.com/blog/confidential-computing-on-h100-gpus-for-secure-and-trustworthy-ai/

[^3572^]: arXiv, "Confidential Computing on nVIDIA H100 GPU," September 2024. https://arxiv.org/html/2409.03992v2

[^3573^]: arXiv, "Characterization of GPU TEE Overheads in Distributed Data Parallel ML Training," January 2025. https://arxiv.org/html/2501.11771v1

---

*Report generated: 2026-07-07 | Words: ~4,200 | Sources: 20+ primary sources*
*Classification: Technical Architecture Analysis for HelixCluster Integration Planning*
