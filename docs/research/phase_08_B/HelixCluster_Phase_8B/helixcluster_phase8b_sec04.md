## 4. Chutes Technology Stack Adoption

Chutes.ai operates a decentralized AI compute marketplace on Bittensor subnet 64, built on a deeply integrated open-source technology stack representing one of the most production-hardened collections of decentralized GPU infrastructure primitives available. This chapter evaluates ten critical components from the Chutes ecosystem for adoption as foundational HelixCluster infrastructure, applying **reverse integration**: consuming Chutes' hardened MIT-licensed primitives as our own, accelerating the security and attestation roadmap by an estimated 6--12 months while preserving protocol compatibility with the broader Bittensor ecosystem.

Each component is assessed on four dimensions: direct code reusability, Go rewrite necessity, integration effort in engineering weeks, and strategic value to HelixCluster's decentralized compute objectives. Table 4.1 presents the complete adoption matrix.

**Table 4.1 --- Ten-Component Adoption Matrix**

| # | Component | Reusability | Go Rewrite Effort | Weeks | Strategic Value | Priority |
|---|-----------|:-----------:|:-----------------:|:-----:|:---------------:|:--------:|
| 1 | `e2ee-proxy` (ML-KEM-768 + ChaCha20) | C lib via CGO | Proxy core only | 2--3 | **Critical** --- post-quantum encryption | P0 |
| 2 | `graval` (GPU attestation) | C/CUDA libs | Go bindings via CGO | 4--6 | **Critical** --- eliminates fake GPU fraud | P0 |
| 3 | `sek8s` (TEE Kubernetes) | Guest/host tools | Scheduler integration | 6--8 | **Very High** --- production TEE K8s is rare | P0 |
| 4 | `model-router` (intelligent routing) | Classification logic | Full Go rewrite | 3--4 | **Critical** --- scheduler brain | P0 |
| 5 | `@chute.cord` SDK pattern | Pattern/concept only | `@helix.task` from scratch | 8--10 | **Very High** --- developer experience | P1 |
| 6 | `bittencert` (blockchain identity) | Protocol + concept | Go port | 1--2 | **High** --- no CA dependency | P1 |
| 7 | `SageAttention` (low-bit attention) | C/CUDA kernels | Go bindings | 2 | **High** --- 2--5x attention speedup | P1 |
| 8 | `sglang` / `vllm` (serving stack) | Container images | Config generation | 2--3 | **Very High** --- inference backbone | P1 |
| 9 | `TurboDiffusion` (video acceleration) | Python pipeline | Go wrapper | 3--4 | **High** --- media processing | P2 |
| 10 | Sign-in-with-Chutes (OAuth) | SDK directly | Gateway integration | 1 | **Medium-High** --- auth bridge | P2 |

P0 components form the security and scheduling foundation and must land first. P1 components deliver developer experience and performance optimization. P2 components extend specialized capabilities.

---

### 4.1 E2EE Proxy for Cluster Security

#### 4.1.1 Adapting `e2ee-proxy`: Go Rewrite with CGO

Chutes' `e2ee-proxy` is an OpenResty-based reverse proxy providing end-to-end encryption for AI inference APIs. It transparently intercepts OpenAI-compatible requests and encrypts them with **ML-KEM-768 + ChaCha20-Poly1305**. The native C library (`libe2ee_proxy.so`) loads via LuaJIT FFI bindings, with critical paths protected by xVMP obfuscation.

HelixCluster adopts the exact cryptographic protocol for Chutes API compatibility while rewriting the proxy core in Go. The rewrite eliminates the OpenResty/LuaJIT dependency and enables direct embedding into HelixCluster's control plane.

**Table 4.2 --- Cryptographic Primitive Comparison**

| Primitive | Chutes (C/Lua) | HelixCluster (Go) | Standard | Purpose |
|-----------|---------------|-------------------|----------|---------|
| Key Encapsulation | ML-KEM-768 | ML-KEM-768 (CIRCL) | NIST FIPS 203 | Post-quantum shared secret |
| Key Derivation | HKDF-SHA256 | HKDF-SHA256 (x/crypto) | RFC 5869 | Symmetric key derivation |
| AEAD | ChaCha20-Poly1305 | ChaCha20-Poly1305 (x/crypto) | RFC 8439 | Payload encryption |
| Compression | Gzip | Gzip (compress/gzip) | --- | Payload compression |
| Forward Secrecy | Ephemeral keypair/request | Same protocol | --- | Compromise resilience |

The Go implementation uses Cloudflare's CIRCL for ML-KEM-768 (~243 microseconds encapsulation on x86_64) and `golang.org/x/crypto` for ChaCha20-Poly1305 with AVX-2/AVX-512 paths.

```go
// pkg/e2ee/e2ee_context.go
package e2ee

import (
    "crypto/rand"
    "crypto/sha256"
    "fmt"
    "io"

    "github.com/cloudflare/circl/kem/kyber/kyber768"
    "golang.org/x/crypto/chacha20poly1305"
    "golang.org/x/crypto/hkdf"
)

const InfoReq = "e2e-req-v1"

type E2EEContext struct {
    InstancePK   kyber768.EncapsulationKey
    SharedSecret []byte
    SymmetricKey []byte
}

func (ctx *E2EEContext) Encapsulate() ([]byte, error) {
    ct, ss, err := ctx.InstancePK.Encapsulate()
    if err != nil {
        return nil, fmt.Errorf("ml-kem-768 encaps failed: %w", err)
    }
    ctx.SharedSecret = ss
    kdf := hkdf.New(sha256.New, ss, nil, []byte(InfoReq))
    ctx.SymmetricKey = make([]byte, chacha20poly1305.KeySize)
    if _, err = io.ReadFull(kdf, ctx.SymmetricKey); err != nil {
        return nil, fmt.Errorf("hkdf failed: %w", err)
    }
    return ct, nil
}

func (ctx *E2EEContext) Seal(plaintext []byte) ([]byte, error) {
    aead, err := chacha20poly1305.New(ctx.SymmetricKey)
    if err != nil {
        return nil, err
    }
    nonce := make([]byte, aead.NonceSize())
    if _, err = rand.Read(nonce); err != nil {
        return nil, err
    }
    return aead.Seal(nonce, nonce, plaintext, nil), nil
}

func (ctx *E2EEContext) Open(ciphertext []byte) ([]byte, error) {
    aead, err := chacha20poly1305.New(ctx.SymmetricKey)
    if err != nil {
        return nil, err
    }
    if len(ciphertext) < aead.NonceSize() {
        return nil, fmt.Errorf("ciphertext too short")
    }
    nonce, ct := ciphertext[:aead.NonceSize()], ciphertext[aead.NonceSize():]
    return aead.Open(nil, nonce, ct, nil)
}
```

The `E2EEContext` manages encapsulation, HKDF key derivation with protocol info strings, authenticated encryption, and transparent gzip compression of JSON payloads.

#### 4.1.2 Post-Quantum Security for Node-to-Node Encryption

ML-KEM-768 bases its hardness on the Module Learning With Errors (MLWE) problem --- unlike classical ECDH, it has no known efficient quantum attacks via Shor's algorithm. For HelixCluster, where traffic traverses untrusted decentralized networks, this is a material differentiator, not speculation.

The proxy integrates at HelixCluster's mesh transport layer. Every inter-node request and every inference payload to decentralized providers traverses an E2EE tunnel with a fresh ephemeral keypair per request, providing forward secrecy. HTTP bodies are encrypted; headers remain in cleartext over underlying TLS for routing. The total encryption overhead is estimated below 3% of throughput on modern hardware with AES-NI and AVX-512 acceleration.

---

### 4.2 GraVal for Node Attestation

#### 4.2.1 Proof of Consecutive VRAM Work: Verifying GPU Authenticity

`graval` is Chutes' C/CUDA graphics card validation library --- the foundation of trust in their network, preventing GPU fraud (claiming H100 while running T4, fabricated PCI IDs). Its **Proof of Consecutive VRAM Work (PoVW)** is a computationally binding proof that a specific GPU performed deterministic matrix multiplications seeded by hardware identifiers.

Verification has four phases: (1) **VRAM Capacity Test** allocates 95% of reported VRAM for GEMM --- insufficient memory fails immediately; (2) **PoVW Challenge** generates a seed from GPU UUID and PCI info, requiring deterministic GEMM operations; (3) **Device Info Challenge** queries GPU properties against manufacturer databases; (4) **Filesystem Challenge** validates runtime integrity against build baselines. The C/CUDA libraries complete verification in under 2 seconds.

For HelixCluster, GraVal integrates via CGO into the node join protocol:

```go
// pkg/attest/graval_integration.go
package attest

/*
#cgo LDFLAGS: -lgraval-miner -lgraval-validator -lcudart
#include <graval/miner.h>
#include <graval/validator.h>
*/
import "C"
import (
    "fmt"
    "time"
    "unsafe"
)

type GraValAttestor struct {
    vctx    unsafe.Pointer
    timeout time.Duration
}

type GPUProof struct {
    DeviceID   string `json:"device_id"`
    DeviceName string `json:"device_name"`
    VRAMBytes  uint64 `json:"vram_bytes"`
    BusID      string `json:"pci_bus_id"`
    PoVWHash   []byte `json:"povw_hash"`
    Timestamp  int64  `json:"timestamp"`
    Valid      bool   `json:"valid"`
}

func NewGraValAttestor() (*GraValAttestor, error) {
    ctx := C.graval_validator_create()
    if ctx == nil {
        return nil, fmt.Errorf("graval_validator_create failed")
    }
    return &GraValAttestor{vctx: ctx, timeout: 30 * time.Second}, nil
}

func (ga *GraValAttestor) VerifyGPU(gpuIndex int) (*GPUProof, error) {
    cIdx := C.int(gpuIndex)
    if C.graval_test_vram_capacity(ga.vctx, cIdx) != 0 {
        return nil, fmt.Errorf("VRAM capacity test failed gpu %d", gpuIndex)
    }
    var povw C.uchar
    var hlen C.size_t
    if C.graval_verify_povw(ga.vctx, cIdx, &povw, &hlen) != 0 {
        return nil, fmt.Errorf("PoVW failed gpu %d", gpuIndex)
    }
    var dev C.graval_device_info
    if C.graval_verify_device_info(ga.vctx, cIdx, &dev) != 0 {
        return nil, fmt.Errorf("device info failed gpu %d", gpuIndex)
    }
    return &GPUProof{
        DeviceID:   C.GoString(dev.uuid),
        DeviceName: C.GoString(dev.name),
        VRAMBytes:  uint64(dev.vram_bytes),
        BusID:      C.GoString(dev.pci_bus_id),
        PoVWHash:   C.GoBytes(unsafe.Pointer(&povw), C.int(hlen)),
        Timestamp:  time.Now().Unix(),
        Valid:      true,
    }, nil
}

func (ga *GraValAttestor) Close() {
    if ga.vctx != nil {
        C.graval_validator_destroy(ga.vctx)
        ga.vctx = nil
    }
}
```

The `GPUProof` binds hardware identity to the node certificate, creating an unforgeable attestation chain the pool manager verifies before admitting any GPU.

#### 4.2.2 Detecting Fake and Misrepresented GPUs in Semi-Trusted Tiers

Semi-trusted decentralized providers present the highest GPU misrepresentation risk. A provider may substitute a slower GPU while charging premium pricing, or virtualize a single physical GPU across multiple customers while advertising dedicated access. GraVal eliminates these attack vectors: the validator generates challenges only answerable by the claimed hardware, with proofs cryptographically bound to unique device identity through the device UUID and PCI bus fingerprint.

For CPU-only nodes where GraVal's CUDA-based verification cannot run, HelixCluster implements a **Proof-of-Compute (PoC)** fallback using AVX-512 matrix multiplication seeded by CPU feature flags and microcode version. The PoC generates a deterministic hash from the computation result that validators can verify without re-executing the full workload. The composite `VerifyIdentity` function checks bittencert signature, GraVal or PoC proof, and economic stake --- a three-factor trust model stronger than any single factor and resilient to the absence of GPU hardware.

---

### 4.3 TEE for Sensitive Workloads

#### 4.3.1 Adapting `sek8s`: Intel TDX + NVIDIA CC

`sek8s` is Chutes' security-hardened Kubernetes distribution for **Intel TDX confidential VMs** --- one of the few open-source TEE-enabled K8s stacks for GPU workloads in production. It comprises guest tools for encrypted VM images with k3s and attestation agents, host tools for GPU binding and VM launch, Ansible automation, Python FastAPI admission control services, and NVIDIA attestation SDK wrappers.

**Table 4.3 --- TEE Platform Comparison**

| TEE Technology | Hardware | GPU Passthrough | Status | sek8s Adaptation | HelixCluster Tier |
|:--------------|:---------|:---------------|:-------|:----------------|:-----------------|
| Intel TDX | Xeon Scalable, Core Ultra | Yes (NVIDIA CC) | Production | Native | Cloud, Local |
| AMD SEV-SNP | EPYC 9004+ | Yes | Production | Guest image adapt | Cloud |
| NVIDIA CC | H100, H200 | Native | Production | Native via PPCIE | All GPU tiers |
| ARM TrustZone | Cortex-A | Limited | Development | Significant rework | Edge (future) |
| Intel SGX | Xeon (legacy) | No | Deprecated | N/A | --- |

HelixCluster adopts sek8s as its TEE foundation. The encrypted volume handling, attestation flow, OPA admission controller, and cosign image verification are directly reusable.

The security model has six layers: (1) Intel TDX encrypts VM memory with CPU-fused keys, removing the hypervisor from the trust boundary; (2) NVIDIA Protected PCIe encrypts the CPU-GPU channel, making VRAM inaccessible to the host; (3) Remote attestation generates a TD Quote signed by the CPU-fused key, bound to a validator nonce; (4) LUKS disk encryption protects the guest root filesystem, decrypted only after attestation; (5) cosign admission control ensures only signed images execute; (6) OPA enforces no-root, no-privileged, no-host-mount policies.

#### 4.3.2 Remote Attestation via Intel DCAP

Intel DCAP verifies TDX attestation quotes through a multi-step protocol. When a TEE pod launches, the sek8s agent running inside the confidential VM generates a TD Quote containing three critical fields: the TDX measurement register (MRTD) representing the hash of the VM's initial trusted state, a nonce provided by the HelixCluster validator to prevent replay attacks, and the TDX module's report signed by the CPU's Provisioning Certification Key (PCK). The validator submits this quote to Intel's Provisioning Certification Service (PCS) via a cached Provisioning Certification Key Certificate Chain (PCK Cert Chain), avoiding network dependencies during the critical path. PCS verifies the PCK signature against Intel's root of trust and returns the attestation result. Only after successful verification does the validator release the LUKS disk encryption key to the guest, enabling the boot process to complete. This architecture ensures that compromised or counterfeit TDX hardware cannot execute sensitive HelixCluster workloads.

The `sek8s-tdx` RuntimeClass enables transparent TEE scheduling:

```yaml
# configs/helix-tee-pod.yaml
apiVersion: v1
kind: Pod
metadata:
  name: sensitive-inference
  namespace: helixcluster
  annotations:
    helix.chutes.io/tee: "required"
    helix.chutes.io/attestation-nonce: "${NONCE}"
    helix.chutes.io/gpu-attestation: "required"
spec:
  runtimeClassName: sek8s-tdx
  containers:
  - name: inference
    image: helix/inference:v1.2.3
    resources:
      limits:
        nvidia.com/gpu: "1"
        intel.com/tdx: "1"
        memory: "64Gi"
    securityContext:
      allowPrivilegeEscalation: false
      readOnlyRootFilesystem: true
      runAsNonRoot: true
    volumeMounts:
    - name: tmp
      mountPath: /tmp
  affinity:
    nodeAffinity:
      requiredDuringSchedulingIgnoredDuringExecution:
        nodeSelectorTerms:
        - matchExpressions:
          - key: node.helix.chutes.io/tee-capable
            operator: In
            values: ["tdx", "sev-snp"]
        - matchExpressions:
          - key: node.helix.chutes.io/gpu-tee-capable
            operator: In
            values: ["nvidia-cc"]
  volumes:
  - name: tmp
    emptyDir:
      medium: Memory
      sizeLimit: 10Gi
```

Pods annotated `helix.chutes.io/tee: required` schedule onto TDX-capable nodes. The admission controller enforces image signature and policy compliance before launch. Node affinity ensures TEE GPU workloads land only on nodes supporting both Intel TDX and NVIDIA CC. The memory-backed `emptyDir` provides encrypted scratch space never touching the host filesystem.

---

### 4.4 AI Serving Stack

#### 4.4.1 vLLM + SGLang + SageAttention + TurboDiffusion

Chutes maintains production forks of high-performance inference engines adopted as HelixCluster's serving backbone. **vLLM** provides baseline throughput with PagedAttention. **SGLang** adds RadixAttention for 5x structured generation speedup via KV-cache reuse. **SageAttention** implements INT8/FP4 low-bit attention for 2--5x speedup over FlashAttention. **TurboDiffusion** accelerates video diffusion 100--200x through kernel fusion, positioning HelixCluster for media processing.

These deploy as container images via k3s, with `@helix.task` generating configurations. SageAttention is the default attention backend, with automatic FlashAttention fallback when precision demands. The stack auto-detects GPU capabilities at startup --- INT8 on H100/H200 (FP8 support), FP4 on Blackwell, FlashAttention on older hardware.

#### 4.4.2 Model Router as Intelligent Workload Scheduler

The `model-router` classifies requests and routes to optimal models by task type. It is strategically significant as HelixCluster's workload scheduler brain.

```
+---------------------------------------------------------------------+
|                    HELIXCLUSTER MODEL ROUTER                         |
|                                                                     |
|  +--------------+    +--------------+    +------------------+      |
|  |   Ingress    |--->|  Task        |--->|  Model Selection |      |
|  |   Request    |    |  Classifier  |    |  Engine          |      |
|  +--------------+    +------+-------+    +--------+---------+      |
|                             |                      |                |
|                    +--------v---------+   +--------v---------+      |
|                    | Task Categories: |   | Routing Logic:   |      |
|                    | * general_text   |   | * latency-first  |      |
|                    | * math_reasoning |   | * cost-first     |      |
|                    | * programming    |   | * tee-required   |      |
|                    | * creative       |   | * quality-first  |      |
|                    | * vision         |   | * balanced       |      |
|                    +--------+---------+   +--------+---------+      |
|                             |                      |                |
|                    +--------v----------------------v---------+      |
|                    |      Self-Answer Optimization            |      |
|                    |  (confidence >= 0.95 -> direct response)  |      |
|                    +------------------+-----------------------+      |
|                                       |                             |
|  +------------------------------------v----------------------+      |
|  |              Backend Selection & Failover                  |      |
|  |  +----------+  +----------+  +----------+  +----------+  |      |
|  |  | Local    |  | Chutes   |  | io.net   |  | Cloud    |  |      |
|  |  | vLLM     |  | API      |  | Ray      |  | Hyperscr |  |      |
|  |  +----------+  +----------+  +----------+  +----------+  |      |
|  +-----------------------------------------------------------+      |
+---------------------------------------------------------------------+
```

The **Task Classifier** determines task category from request content. For trivial queries with classifier confidence >= 0.95, **Self-Answer Optimization** responds directly without backend inference. The **Model Selection Engine** picks the best backend by strategy: latency-first for real-time applications, cost-first for batch processing, TEE-required for sensitive data, quality-first for research workloads, or balanced for general use.

The Go rewrite (`helix-router`) integrates directly with the GPU Pool Manager, subscribing to real-time health metrics from all backends. The router maintains exponentially weighted moving averages for TTFT (Time-To-First-Token), TPS (Tokens-Per-Second), error rate, and cost-per-1M-tokens, re-ranking preferences every 5 seconds. A backend is removed from rotation when its error rate exceeds 20% or TTFT exceeds 30 seconds. Automatic failover switches to the next-best alternative without client-visible interruption, with retry logic applying exponential backoff and circuit breaker patterns for transient failures.

---

### 4.5 @helix.task SDK (from @chute.cord)

#### 4.5.1 Go Decorator Pattern for Task Deployment

The Chutes SDK provides a decorator-based Python framework for serverless AI applications. `@chute.cord` creates HTTP endpoints with auto-generated OpenAPI schemas; `@chute.on_startup`/`@chute.on_shutdown` manage lifecycle hooks; `NodeSelector` specifies hardware requirements; auto-scaling adjusts instances by utilization.

HelixCluster adapts this as **`@helix.task`** --- Go-native with fluent API and functional options substituting for Python decorators.

**Table 4.4 --- SDK Feature Parity: `@chute.cord` vs. `@helix.task`**

| Feature | `@chute.cord` (Python) | `@helix.task` (Go) | Status |
|:--------|:-----------------------|:-------------------|:-------|
| Endpoint | `@chute.cord()` decorator | `Task.Cord()` fluent API | Implemented |
| Startup hooks | `@chute.on_startup(priority)` | `Task.OnStartup(priority, fn)` | Implemented |
| Shutdown hooks | `@chute.on_shutdown()` | `Task.OnShutdown(fn)` | Implemented |
| Error handlers | Exception catch | `Task.OnError(fn)` | **Added** |
| Node selector | `NodeSelector(...)` | `NodeSelector` struct | Implemented |
| Auto-scaling | `scaling_threshold`, `max_instances` | `AutoScaler` struct | Implemented |
| Concurrency | `concurrency` int | `WithConcurrency(n)` option | Implemented |
| Passthrough | Passthrough cords | `CordOption.Proxy` flag | Implemented |
| OpenAPI | Auto from Pydantic | Auto from Go struct tags | Implemented |
| Packaging | Docker auto-build | Ko + Dockerfile gen | Adapted |

```go
// pkg/sdk/task.go --- @helix.task Go implementation
package sdk

import (
    "context"
    "sort"
    "time"

    "github.com/gin-gonic/gin"
)

type HandlerFunc func(ctx context.Context, req interface{}) (interface{}, error)
type StartupFunc func(ctx context.Context) error
type ShutdownFunc func(ctx context.Context) error
type ErrorFunc func(ctx context.Context, err error) error

type NodeSelector struct {
    GPUCount      int    `json:"gpu_count"`
    MinVRAMPerGPU int    `json:"min_vram_gb_per_gpu"`
    GPUModel      string `json:"gpu_model,omitempty"`
    TEE           bool   `json:"tee,omitempty"`
}

type StartupHook struct {
    Priority int
    Func     StartupFunc
}

type HandlerRegistration struct {
    Handler      HandlerFunc
    InputSchema  interface{}
    OutputSchema interface{}
}

type Task struct {
    Name             string
    Image            string
    NodeSelector     NodeSelector
    Concurrency      int
    MaxInstances     int
    ShutdownAfter    time.Duration
    ScalingThreshold float64

    startupHooks  []StartupHook
    shutdownHooks []ShutdownFunc
    errorHandlers []ErrorFunc
    handlers      map[string]HandlerRegistration
    router        *gin.Engine
    state         map[string]interface{}
}

type TaskOption func(*Task)

func WithNodeSelector(ns NodeSelector) TaskOption {
    return func(t *Task) { t.NodeSelector = ns }
}
func WithConcurrency(n int) TaskOption {
    return func(t *Task) { t.Concurrency = n }
}
func WithMaxInstances(n int) TaskOption {
    return func(t *Task) { t.MaxInstances = n }
}
func WithScalingThreshold(th float64) TaskOption {
    return func(t *Task) { t.ScalingThreshold = th }
}

func NewTask(name string, opts ...TaskOption) *Task {
    t := &Task{
        Name:             name,
        Concurrency:      4,
        MaxInstances:     5,
        ShutdownAfter:    300 * time.Second,
        ScalingThreshold: 0.75,
        handlers:         make(map[string]HandlerRegistration),
        router:           gin.New(),
        state:            make(map[string]interface{}),
    }
    for _, opt := range opts {
        opt(t)
    }
    return t
}

// Cord registers an HTTP handler, equivalent to @chute.cord().
func (t *Task) Cord(path string, method string, handler HandlerFunc,
                     opts ...CordOption) {
    reg := HandlerRegistration{Handler: handler}
    for _, opt := range opts {
        opt(&reg)
    }
    t.handlers[method+":"+path] = reg
    t.router.Handle(method, path, func(c *gin.Context) {
        handler(c.Request.Context(), c)
    })
}

type CordOption func(*HandlerRegistration)

func WithInputSchema(schema interface{}) CordOption {
    return func(r *HandlerRegistration) { r.InputSchema = schema }
}
func WithOutputSchema(schema interface{}) CordOption {
    return func(r *HandlerRegistration) { r.OutputSchema = schema }
}

// OnStartup registers a prioritized startup hook (@chute.on_startup).
func (t *Task) OnStartup(priority int, fn StartupFunc) {
    t.startupHooks = append(t.startupHooks,
        StartupHook{Priority: priority, Func: fn})
    sort.Slice(t.startupHooks, func(i, j int) bool {
        return t.startupHooks[i].Priority < t.startupHooks[j].Priority
    })
}

// OnShutdown registers a shutdown hook (@chute.on_shutdown).
func (t *Task) OnShutdown(fn ShutdownFunc) {
    t.shutdownHooks = append(t.shutdownHooks, fn)
}

// OnError registers an error handler --- added beyond @chute.cord.
func (t *Task) OnError(fn ErrorFunc) {
    t.errorHandlers = append(t.errorHandlers, fn)
}

func (t *Task) SetState(key string, value interface{}) {
    t.state[key] = value
}

func (t *Task) GetState(key string) (interface{}, bool) {
    v, ok := t.state[key]
    return v, ok
}
```

#### 4.5.2 Lifecycle Hooks: on_startup, on_shutdown, on_error

Startup hooks run sequentially by priority (lower first), each gating the next. If any fails, the task does not enter service and prior hooks' shutdown logic runs for cleanup --- preventing partially-initialized tasks from accepting traffic.

Shutdown hooks execute in reverse priority order on termination or scale-down, with a configurable grace period (default 30s). For GPU tasks, shutdown releases CUDA context, unloads model weights from VRAM, and persists cached state to the distributed cache.

Error hooks execute asynchronously, decoupled from the request path. They receive structured error objects with type, stack context, redacted request metadata, and task state snapshot. The default handler emits structured logs; custom handlers integrate with alerting or trigger automatic migration.

Full `@helix.task` parity is estimated at **8--10 engineering weeks**, reflecting the gap between Python's dynamic typing (runtime schema introspection) and Go's static typing (struct tag parsing and reflection). The investment is justified by the significantly improved developer experience for HelixCluster task authors.

---

### 4.6 bittencert for Identity

#### 4.6.1 Blockchain-Backed X.509 Certificates

`bittencert` creates X.509 certificates signed with Bittensor keypairs, enabling certificate authentication without a traditional CA. It bridges blockchain identity (ss58 addresses) with TLS infrastructure --- essential for decentralized systems with no central trust anchor.

The certificate's CN contains the hostname, OU the ss58 address, and O the hex-encoded signature of the verification string `serial_number:cn:ou:not_before:not_after`, signed by the Bittensor keypair. Verification reconstructs this string and validates the signature against the claimed ss58 address.

For HelixCluster, each node generates a bittencert on registration, binding Bittensor identity to its network endpoint. The composite `VerifyIdentity` checks bittencert signature, GraVal GPU proof (or CPU PoC fallback), and minimum economic stake for the requested tier. This three-factor identity --- cryptographic key ownership, hardware attestation, and economic collateral --- creates a trust model far stronger than any single factor and eliminates the need for a centralized certificate authority in a decentralized compute network.

The Go port is **1--2 engineering weeks** given Go's mature `crypto/x509` and `crypto/ecdsa`. Protocol compatibility with Python bittencert ensures mutual verification between HelixCluster nodes and Chutes miners. Combined with GraVal attestation and the E2EE proxy, bittencert completes the cryptographic trust foundation that enables decentralized GPU compute at production scale without compromising on security, verifiability, or decentralization principles.
