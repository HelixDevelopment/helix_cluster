# 2. Remote GPU Node Abstraction

The central engineering challenge in HelixCluster's reverse-integration architecture is making a GPU located two thousand miles away appear as `/dev/nvidia0` on a local cluster node. This chapter examines the virtualization patterns, network protocols, and Kubernetes machinery that transform remote GPUs into first-class cluster citizens. The abstraction must be transparent enough that unmodified CUDA applications consume remote GPUs without awareness that the silicon is not physically present, yet performant enough that latency does not negate the 50-80% cost savings that multi-provider pooling delivers.

The **HelixCluster Virtual GPU** intercepts CUDA API calls at the runtime layer, forwards them to provider-specific adapters over gRPC, and manages memory staging through pinned local buffers. Running inside Kubernetes as a DaemonSet that registers virtual `nvidia.com/gpu` resources, it aggregates Chutes, io.net, RunPod, CoreWeave, and hyperscaler GPUs into a single logical pool, with the Pool Manager selecting the best provider per workload based on real-time cost, latency, and availability.

---

## 2.1 The Virtual GPU Pattern

### 2.1.1 Virtual `/dev/nvidia*` Proxying to Remote GPUs over gRPC

Modern GPU computing follows a strict stack from application to silicon. The user's PyTorch script calls into `libcudart.so`, which dispatches to `libcuda.so`, which talks to the NVIDIA kernel driver `nvidia.ko`, which ultimately commands the physical GPU. The Virtual GPU Pattern inserts a transparent interception layer between the CUDA Runtime and the CUDA Driver, replacing local driver calls with gRPC requests to a remote GPU agent.

The architecture creates virtual device files—`/dev/helixcluster-nvidia0`, `/dev/helixcluster-nvidia1`, and so on—that stand in for physical GPUs. These are not mere symlinks or stubs; they are fully operational virtual devices backed by a Go-based proxy service that maintains the entire CUDA context state on behalf of the application.

```
+-------------------------------------------------------------+
|                    LOCAL NODE (HelixCluster)                  |
|                                                             |
|  +------------------+    +---------------------------+       |
|  | CUDA Application |    | HelixCluster GPU Proxy    |       |
|  |                  |    |                           |       |
|  | libcuda.so    +------>+ CUDA API Interceptor      |       |
|  | (interceptor)  |    |  (LD_PRELOAD replacement)   |       |
|  +------------------+    +-----------+---------------+       |
|                                      |                      |
|  +------------------+                | gRPC/REST            |
|  | /dev/nvidia0     |<---------------+ (virtual device)     |
|  | (virtual,        |                |                      |
|  |  created by      |    +-----------v---------------+       |
|  |  proxy)          |    | GPU Pool Manager          |       |
|  +------------------+    | - Tracks remote GPUs      |       |
|                          | - Load balances           |       |
|                          | - Handles failover        |       |
|                          +-----------+---------------+       |
+--------------------------------------|----------------------+
                                       |
                           +-----------v---------------+
                           |    NETWORK (Internet)     |
                           |   10-100 Gbps typical     |
                           +-----------+---------------+
                                       |
+----------------------+---------------v------------------------+
|              REMOTE GPU PROVIDERS                            |
|                                                              |
|  +----------------+  +----------------+  +----------------+ |
|  | Chutes Node    |  | io.net Worker  |  | RunPod GPU     | |
|  | (Bittensor)    |  | (Ray cluster)  |  | (serverless)   | |
|  |                |  |                |  |                | |
|  | GPU Proxy      |  | GPU Proxy      |  | GPU Proxy      | |
|  | Agent          |  | Agent          |  | Agent          | |
|  +--------+-------+  +--------+-------+  +--------+-------+ |
|           |                   |                   |          |
|  +--------v-------+  +--------v-------+  +--------v-------+ |
|  | Physical GPU   |  | Physical GPU   |  | Physical GPU   | |
|  | (A100/H100)    |  | (A100/H100)    |  | (A100/H100)    | |
|  +----------------+  +----------------+  +----------------+ |
+--------------------------------------------------------------+
```

**Figure 2.1 — HelixCluster Virtual GPU Architecture.** Local CUDA applications interact with a virtual `/dev/nvidia*` device. The GPU Proxy intercepts all CUDA API calls, forwards them over gRPC to provider-specific agents, and manages memory staging through local pinned buffers. Multiple remote providers are aggregated behind a single virtual device interface by the Pool Manager.

The proxy creates these virtual devices through a Go binary that manages device node files and permissions; a future iteration will use a lightweight kernel driver for full `libcuda.so` compatibility. The virtual device presents the same `ioctl` interface as NVIDIA's driver, but every call is translated into a protobuf message and dispatched over the network.

### 2.1.2 CUDA API Interception: Local Calls Forwarded to Chutes/io.net Miners

The interception mechanism operates through `LD_PRELOAD`, a Linux dynamic linker feature that allows the GPU Proxy to substitute its own implementation of CUDA Runtime functions before the real `libcuda.so` is loaded. When the application calls `cudaMalloc()`, it executes the proxy's version. The proxy serializes the request, sends it via gRPC to the remote GPU agent, and returns a virtual address that the application uses transparently.

The following Go code shows the core `VirtualGPU` struct and its implementation of the three most critical CUDA operations: memory allocation, memory copy, and kernel launch.

```go
// pkg/interceptor/cuda_interceptor.go
package interceptor

import (
    "context"
    "fmt"
    "sync"
    "unsafe"

    "google.golang.org/grpc"
    pb "helixcluster/gpu-proxy/proto"
)

// VirtualGPU represents a local virtual GPU that proxies to a remote GPU
type VirtualGPU struct {
    deviceID    int
    provider    ProviderAdapter
    memoryPool  *MemoryPool
    streamMap   map[uintptr]uint64  // local stream -> remote stream
    eventMap    map[uintptr]uint64  // local event -> remote event
    mu          sync.RWMutex
    ctx         context.Context
}

// NewVirtualGPU creates a new virtual GPU backed by a remote provider
func NewVirtualGPU(ctx context.Context, deviceID int, provider ProviderAdapter) (*VirtualGPU, error) {
    memPool, err := NewMemoryPool(1 << 30) // 1GB staging buffer
    if err != nil {
        return nil, fmt.Errorf("failed to create memory pool: %w", err)
    }

    return &VirtualGPU{
        deviceID:   deviceID,
        provider:   provider,
        memoryPool: memPool,
        streamMap:  make(map[uintptr]uint64),
        eventMap:   make(map[uintptr]uint64),
        ctx:        ctx,
    }, nil
}

// CUDAMalloc intercepts cudaMalloc and forwards to remote GPU
func (vg *VirtualGPU) CUDAMalloc(size uint64) (uintptr, error) {
    localPtr, err := vg.memoryPool.Allocate(size)
    if err != nil {
        return 0, err
    }

    resp, err := vg.provider.AllocateMemory(vg.ctx, &pb.AllocRequest{
        Size:     size,
        DeviceId: uint32(vg.deviceID),
    })
    if err != nil {
        vg.memoryPool.Free(localPtr)
        return 0, fmt.Errorf("remote alloc failed: %w", err)
    }

    vg.memoryPool.RegisterRemoteMapping(localPtr, resp.RemoteHandle, resp.RemoteAddress)
    return localPtr, nil
}

// CUDAFree intercepts cudaFree and forwards to remote GPU
func (vg *VirtualGPU) CUDAFree(devPtr uintptr) error {
    mapping := vg.memoryPool.GetMapping(devPtr)
    if mapping == nil {
        return fmt.Errorf("invalid device pointer: %x", devPtr)
    }

    _, err := vg.provider.FreeMemory(vg.ctx, &pb.FreeRequest{
        RemoteHandle: mapping.remoteHandle,
    })
    if err != nil {
        return fmt.Errorf("remote free failed: %w", err)
    }

    return vg.memoryPool.Free(devPtr)
}

// CUDAMemcpy intercepts cudaMemcpy (H2D, D2H, D2D)
func (vg *VirtualGPU) CUDAMemcpy(dst, src uintptr, size uint64, kind uint32) error {
    switch kind {
    case CUDAMemcpyHostToDevice:
        return vg.memcpyH2D(dst, src, size)
    case CUDAMemcpyDeviceToHost:
        return vg.memcpyD2H(dst, src, size)
    case CUDAMemcpyDeviceToDevice:
        return vg.memcpyD2D(dst, src, size)
    default:
        return fmt.Errorf("unsupported memcpy kind: %d", kind)
    }
}

func (vg *VirtualGPU) memcpyH2D(dst, src uintptr, size uint64) error {
    mapping := vg.memoryPool.GetMapping(dst)
    if mapping == nil {
        return fmt.Errorf("invalid device pointer: %x", dst)
    }

    _, err := vg.provider.CopyHostToDevice(vg.ctx, &pb.H2DRequest{
        RemoteHandle: mapping.remoteHandle,
        Data:         unsafe.Slice((*byte)(unsafe.Pointer(src)), size),
        Size:         size,
    })
    return err
}

// CUDALaunchKernel intercepts cudaLaunchKernel
func (vg *VirtualGPU) CUDALaunchKernel(
    kernelName string,
    gridDim, blockDim Dim3,
    args []byte,
    sharedMem uint64,
    stream uintptr,
) error {
    vg.mu.RLock()
    remoteStream := vg.streamMap[stream]
    vg.mu.RUnlock()

    _, err := vg.provider.LaunchKernel(vg.ctx, &pb.KernelLaunchRequest{
        KernelName:   kernelName,
        GridDimX:     gridDim.X,
        GridDimY:     gridDim.Y,
        GridDimZ:     gridDim.Z,
        BlockDimX:    blockDim.X,
        BlockDimY:    blockDim.Y,
        BlockDimZ:    blockDim.Z,
        KernelArgs:   args,
        SharedMem:    sharedMem,
        StreamHandle: remoteStream,
        DeviceId:     uint32(vg.deviceID),
    })
    return err
}

type Dim3 struct{ X, Y, Z uint32 }

const (
    CUDAMemcpyHostToDevice   = 1
    CUDAMemcpyDeviceToHost   = 2
    CUDAMemcpyDeviceToDevice = 3
)
```

The interceptor handles six categories of CUDA API calls, each with a specialized forwarding strategy summarized below.

| CUDA API Category | Examples | Interception Strategy |
|---|---|---|
| Memory Management | `cudaMalloc`, `cudaFree`, `cudaMemcpy` | Forward to remote; maintain local staging buffer mapping |
| Kernel Launch | `cudaLaunchKernel` | Serialize arguments, forward to remote agent for execution |
| Streams | `cudaStreamCreate`, `cudaStreamSynchronize` | Virtual stream ID mapping; sequence numbers preserve ordering |
| Events | `cudaEventCreate`, `cudaEventRecord` | Remote event proxy with batched status polling |
| Context | `cudaSetDevice`, `cudaGetDevice` | Return virtual device IDs from proxy registry |
| Peer Access | `cudaDeviceEnablePeerAccess` | Remote peer setup via provider adapter if supported |

**Table 2.1 — CUDA API Interception Strategies by Category.** Each category of CUDA call requires a different forwarding approach. Memory operations use local staging buffers to batch and pipeline transfers; kernel launches are fully serialized and dispatched; streams and events use virtual ID mapping to preserve CUDA semantics across the network boundary.

### 2.1.3 Memory Staging: Local Buffer → Network Transfer → Remote GPU VRAM

The performance-critical path in any remote GPU system is data movement. When an application calls `cudaMemcpy` with `cudaMemcpyHostToDevice`, data traverses three stages: (1) from application memory to a local pinned staging buffer, (2) across the network via gRPC or Apache Arrow Flight, and (3) from the remote agent's host memory into GPU VRAM. The proxy allocates a 1 GB pinned host memory staging buffer at startup; small transfers (under 64 KB) serialize inline within gRPC, while larger transfers use Arrow Flight streaming at up to 6,000 MB/s over InfiniBand or 1,650 MB/s across standard datacenter networks. Persistent HTTP/2 connections eliminate TCP handshake overhead.

When the local node has RDMA-capable NICs, the proxy bypasses the staging buffer entirely and uses GPUDirect RDMA for 1-5 microsecond latency—this "fast path" requires Mellanox ConnectX adapters available within single-provider networks. For the general cross-provider case, staging buffers remain the practical default.

```
Data Flow (Host -> Remote GPU):
1. App writes data to local pinned memory (staging buffer)
2. GPU Proxy serializes and transfers via gRPC/Arrow Flight
3. Remote agent receives and executes cudaMemcpy (host->device)
4. Virtual address returned to application

Optimizations: persistent connections, chunked streaming,
GPUDirect RDMA fast path, batched small transfers
```

---

## 2.2 CUDA over Network Technologies

The HelixCluster GPU Proxy synthesizes lessons from over a decade of academic and industrial research into CUDA remoting. The key differentiator is practicality: the system must work with today's cloud providers and networks without requiring custom hardware or kernel patches.

### 2.2.1 rCUDA: Transparent Remote CUDA (Academic, Limited Availability)

The Remote CUDA (rCUDA) framework, developed at Universitat Politecnica de Valencia, was the seminal implementation of transparent CUDA remoting. It intercepts CUDA Runtime API calls via `LD_PRELOAD`, serializes them into a custom wire protocol, and forwards them to an `rCUDAd` daemon on the remote GPU host, supporting both TCP and InfiniBand transports with RDMA acceleration. rCUDA demonstrates that transparent remoting is technically feasible at 10-100 microseconds kernel launch overhead. However, the project is no longer actively maintained, requires exact CUDA version matching, does not support unified memory or graph capture, and memory transfers remain the dominant bottleneck. HelixCluster borrows rCUDA's interception philosophy but replaces its custom protocol with gRPC and its single-server design with a multi-provider pool manager.

### 2.2.2 NVSHMEM: GPU-Initiated RDMA (1-5µs Latency, Datacenter Only)

NVSHMEM implements the OpenSHMEM API for GPU clusters, providing a partitioned global address space that allows CUDA kernels to directly access remote GPU memory through `nvshmem_putmem()` and `nvshmem_getmem()` calls. The runtime achieves GPU-initiated RDMA via dedicated progress threads over InfiniBand verbs, delivering sub-microsecond latency and near line-rate bandwidth. However, NVSHMEM requires NVLink or InfiniBand connectivity available only within a single datacenter, and cannot bridge across providers. Within a single provider's network it serves as an optimized transport; across providers, the gRPC proxy remains the universal fallback.

### 2.2.3 Ray/Dask: Distributed Execution with GPU Support (Practical Approach)

Apache Ray is the most production-proven framework for distributed GPU execution—OpenAI uses Ray to coordinate ChatGPT training across thousands of GPUs. Ray's GPU support is explicit: developers annotate functions with `@ray.remote(num_gpus=1)`, and the Ray scheduler places tasks on GPU-equipped workers, handling placement groups, fractional GPU support (`num_gpus=0.5`), and zero-copy data sharing through its object store. For HelixCluster, Ray serves as the distributed execution backend for training workloads spanning multiple remote providers; the GPU Proxy registers each virtual GPU as a Ray resource, and Ray tasks are routed to the proxy for provider selection. Dask-CUDA provides a similar model, with benchmarks showing 8 GPUs completing a 2-terabyte array computation in 19 seconds versus 2 hours 39 minutes on a single CPU core.

### 2.2.4 gRPC GPU Kernel Dispatch: 100-500µs Overhead per Call

The HelixCluster GPU Proxy uses gRPC as its primary CUDA transport. gRPC provides strong typing via Protocol Buffers, efficient HTTP/2 multiplexing, streaming for large transfers, and excellent Go implementations. Research shows gRPC achieves 0.43 million RPCs per second on TCP with 8 threads, scaling to 6.5 million on RDMA-accelerated transports. For GPU kernel dispatch, measured overhead is 100-500 microseconds per call—higher than rCUDA's custom protocol but acceptable for batched workloads. The key optimization is **operation batching**: the proxy accumulates a sequence of CUDA operations and dispatches them as a single RPC, amortizing the network round-trip and reducing effective per-kernel overhead to under 10 microseconds for inference workloads.

| Technology | Interception Layer | Transport | Kernel Launch Latency | Maturity | HelixCluster Role |
|---|---|---|---|---|---|
| GPUDirect RDMA | Driver | InfiniBand/NVLink | 1-5 µs | Production (NVIDIA) | Fast-path optimization within single provider |
| NVSHMEM | Kernel | InfiniBand | <1 µs (GPU-initiated) | Production (NVIDIA) | Intra-cluster GPU-to-GPU when IB available |
| rCUDA | Runtime | TCP/RDMA | 10-100 µs | Academic (unmaintained) | Architectural inspiration for interceptor |
| gVirtuS | Runtime | TCP/RDMA | 50-500 µs | Open source (limited) | Cross-architecture reference |
| **gRPC GPU Proxy** | **Runtime** | **gRPC/HTTP/2** | **100-500 µs** | **Production (HelixCluster)** | **Primary cross-provider transport** |
| Ray/Dask | Application | TCP (custom) | Variable (task-level) | Production | **Distributed training orchestration** |
| NVIDIA vGPU/GRID | Hypervisor | Internal | ~5-10% perf loss | Production (licensed) | VM-based multi-tenancy (not API remoting) |

**Table 2.2 — CUDA over Network Technology Comparison.** The HelixCluster gRPC GPU Proxy occupies the middle ground: higher latency than RDMA-based solutions but universally deployable across any cloud provider with standard networking. Ray provides higher-level distributed execution for training workloads. For inference, the 100-500 µs dispatch overhead is hidden by request batching.

---

## 2.3 Kubernetes GPU Proxy

### 2.3.1 GPU Proxy as DaemonSet: Registers Virtual `nvidia.com/gpu` Resources

The Kubernetes integration transforms the GPU Proxy from a standalone binary into a cluster-wide service. Deployed as a DaemonSet, the proxy runs on every node that needs access to remote GPUs. It implements the Kubernetes Device Plugin API, communicating with kubelet via gRPC over the Unix socket `/var/lib/kubelet/device-plugins/nvidia.sock`. Through this mechanism, the proxy registers virtual `nvidia.com/gpu` resources that appear to the Kubernetes scheduler as standard GPU allocations.

When a pod requests `nvidia.com/gpu: 1`, the scheduler may assign it to a node where the only available GPUs are virtual ones backed by the proxy. The proxy intercepts the pod's GPU requests and binds the appropriate virtual device. From the pod's perspective, it receives a standard GPU device mount and can run unmodified CUDA containers. The proxy handles all translation transparently.

```yaml
# configs/gpu-proxy-daemonset.yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: helixcluster-gpu-proxy
  namespace: helixcluster
  labels:
    app: gpu-proxy
    helixcluster.io/component: gpu-proxy
spec:
  selector:
    matchLabels:
      app: gpu-proxy
  template:
    metadata:
      labels:
        app: gpu-proxy
        helixcluster.io/tier: remote-proxy
        helixcluster.io/component: gpu-proxy
    spec:
      serviceAccountName: gpu-proxy
      hostNetwork: true
      nodeSelector:
        helixcluster.io/gpu-proxy: "enabled"
      containers:
      - name: gpu-proxy
        image: helixcluster/gpu-proxy:v0.8.0
        command: ["/bin/gpu-proxy"]
        args:
        - --device-plugin=true
        - --resource-name=nvidia.com/gpu
        - --virtual-devices=4
        - --staging-buffer=1Gi
        - --grpc-port=9333
        - --provider-config=/etc/helixcluster/providers.yaml
        resources:
          limits:
            memory: "4Gi"
            cpu: "2000m"
          requests:
            memory: "1Gi"
            cpu: "500m"
        securityContext:
          privileged: true
        volumeMounts:
        - name: device-plugin
          mountPath: /var/lib/kubelet/device-plugins
        - name: providers-config
          mountPath: /etc/helixcluster
          readOnly: true
        - name: grpc-tls
          mountPath: /etc/helixcluster/tls
          readOnly: true
        env:
        - name: NODE_NAME
          valueFrom:
            fieldRef:
              fieldPath: spec.nodeName
        - name: POD_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        - name: HELIXCLUSTER_LOG_LEVEL
          value: "info"
        - name: HELIXCLUSTER_METRICS_PORT
          value: "9090"
        livenessProbe:
          grpc:
            port: 9333
          initialDelaySeconds: 30
          periodSeconds: 15
        readinessProbe:
          grpc:
            port: 9333
          initialDelaySeconds: 5
          periodSeconds: 10
      - name: metrics-exporter
        image: helixcluster/gpu-metrics-exporter:v0.8.0
        ports:
        - containerPort: 9090
          name: metrics
        env:
        - name: PROXY_ENDPOINT
          value: "localhost:9333"
        - name: SCRAPE_INTERVAL
          value: "15"
      volumes:
      - name: device-plugin
        hostPath:
          path: /var/lib/kubelet/device-plugins
          type: Directory
      - name: providers-config
        secret:
          secretName: gpu-proxy-providers
      - name: grpc-tls
        secret:
          secretName: gpu-proxy-tls
```

The DaemonSet requires privileged access for device node creation and kubelet device plugin communication. `hostNetwork: true` ensures the proxy can reach remote providers without NAT complications. Each instance registers up to `--virtual-devices` virtual GPUs (default 4) per node, mapped to actual remote GPUs through a provider configuration mounted as a Kubernetes secret.

### 2.3.2 Provider Adapters: Chutes, io.net, RunPod, AWS Each with Own Adapter

Each GPU provider exposes a different API, uses different authentication, and offers different capabilities. The HelixCluster Provider Adapter Interface abstracts these differences behind a uniform Go interface, allowing the Pool Manager to treat all GPUs as fungible resources.

```go
// pkg/adapter/provider.go
package adapter

import (
    "context"
    pb "helixcluster/gpu-proxy/proto"
)

// ProviderAdapter abstracts different GPU cloud providers
type ProviderAdapter interface {
    // AllocateMemory allocates GPU memory on the remote device
    AllocateMemory(ctx context.Context, req *pb.AllocRequest) (*pb.AllocResponse, error)

    // FreeMemory deallocates GPU memory
    FreeMemory(ctx context.Context, req *pb.FreeRequest) (*pb.FreeResponse, error)

    // CopyHostToDevice transfers data from host to remote GPU
    CopyHostToDevice(ctx context.Context, req *pb.H2DRequest) (*pb.H2DResponse, error)

    // CopyDeviceToHost transfers data from remote GPU to host
    CopyDeviceToHost(ctx context.Context, req *pb.D2HRequest) (*pb.D2HResponse, error)

    // LaunchKernel executes a CUDA kernel on the remote GPU
    LaunchKernel(ctx context.Context, req *pb.KernelLaunchRequest) (*pb.KernelLaunchResponse, error)

    // GetDeviceInfo returns information about the remote GPU
    GetDeviceInfo(ctx context.Context) (*pb.DeviceInfo, error)

    // HealthCheck verifies connectivity to the remote GPU
    HealthCheck(ctx context.Context) error

    // CostPerHour returns the hourly cost in USD
    CostPerHour() float64

    // Bandwidth returns the available network bandwidth in Gbps
    Bandwidth() float64
}

// ProviderType identifies the GPU cloud provider
type ProviderType string

const (
    ProviderChutes     ProviderType = "chutes"
    ProviderIonet      ProviderType = "io.net"
    ProviderRunPod     ProviderType = "runpod"
    ProviderCoreWeave  ProviderType = "coreweave"
    ProviderLambda     ProviderType = "lambda"
    ProviderAWS        ProviderType = "aws"
    ProviderGCP        ProviderType = "gcp"
    ProviderGeneric    ProviderType = "generic"
)
```

Each adapter translates `ProviderAdapter` calls into provider-specific APIs: RunPod uses gRPC to serverless endpoints with API key authentication; Chutes translates inference workloads into OpenAI-compatible REST calls; io.net uses the Ray client for distributed task submission; CoreWeave communicates via the Kubernetes API to provision bare-metal GPU pods; AWS, GCP, and Azure adapters use their respective cloud SDKs. New providers can be added without modifying the proxy core—only a new adapter implementation is required. The Pool Manager routes workloads to any provider based on real-time cost and availability, achieving true multi-cloud GPU portability.

| Provider | GPU Type | Cost/Hour | Adapter Transport | Best For |
|---|---|---|---|---|
| Chutes | H100 (shared) | $1.50-$3.00 | OpenAI-compatible REST API | LLM inference burst, privacy-sensitive workloads |
| io.net | A100/H100 | $1.00-$2.50 | Ray distributed client | Training clusters, highest elasticity |
| RunPod | A100 80GB | $1.99-$3.99 | gRPC / REST API | Serverless inference, fast cold-start |
| CoreWeave | H100 SXM | $2.06-$6.15 | Kubernetes native | Production training, InfiniBand networking |
| Lambda | H100 PCIe | $1.99-$3.99 | Cloud API | Development/testing, research workloads |
| AWS (spot) | H100 | $3.69-$12.29 | AWS SDK (EC2) | Compliance, reserved capacity, geographic presence |

**Table 2.3 — Remote GPU Provider Cost and Capability Matrix.** The blended cost across all providers typically ranges from $1.50 to $3.00 per GPU-hour for H100-equivalent compute, representing 50-80% savings versus AWS on-demand. Each provider is accessed through a dedicated adapter that translates the uniform `ProviderAdapter` interface into provider-specific API calls.

### 2.3.3 Pool Manager: Aggregate Multiple Remote GPUs Behind Single Virtual Device

The GPU Pool Manager maintains a real-time view of all virtual GPU resources across providers and makes routing decisions based on workload requirements, cost constraints, and SLA policies. Its pluggable scheduler selects backing providers using round-robin, least-load, or cost-aware policies. Health checks every 30 seconds trigger automatic migration if a provider fails.

From the application's perspective, it sees a fixed number of local virtual devices regardless of how many physical GPUs back them. A virtual device might map to a Chutes GPU in one moment and an io.net GPU the next, with the Pool Manager handling context switching transparently. For model-parallel workloads, the Pool Manager uses Ray to shard execution across multiple providers, coordinating through NCCL when intra-provider networking allows.

---

## 2.4 What Works vs What Doesn't

The 100-500 microsecond gRPC dispatch overhead and 50-200 millisecond gigabyte-transfer latency shape which workloads suit the remote GPU proxy. **Latency tolerance depends on the ratio of computation to communication**. Workloads with large computation per byte transferred amortize the network penalty; those with millions of tiny kernels and frequent synchronization do not.

### 2.4.1 Fine-Grained HPC: Too Much Latency (Not Suitable)

High-performance computing simulations—molecular dynamics, computational fluid dynamics, weather modeling—decompose problems across GPUs using domain decomposition, where each GPU handles a spatial subdomain and exchanges "halo" data with neighbors after every time step. These exchanges are small but extremely frequent. A 100-microsecond kernel launch overhead multiplied by millions of launches per hour becomes a 10-100x slowdown. The Pool Manager's workload classifier detects HPC patterns (high kernel launch frequency, dense `cudaStreamSynchronize` calls) and routes them to local physical GPUs or CoreWeave clusters with InfiniBand, bypassing the cross-provider proxy.

### 2.4.2 LLM Inference: Excellent Fit (Batch Requests, Hide Latency)

Large language model inference is the ideal workload for remote GPU proxying. Model weights load once into GPU memory and are amortized over thousands of requests; inference consists of batched matrix multiplications that keep the GPU compute-bound for 10-50 milliseconds at a time. A 100-microsecond dispatch overhead is negligible against such compute phases. The vLLM and SGLang serving stacks further optimize by batching multiple client requests into a single kernel launch, achieving hundreds of tokens per second per GPU. The proxy's operation batching groups multiple CUDA calls into a single RPC, reducing effective overhead to under 10 microseconds per call in batched scenarios.

### 2.4.3 Training: Good Fit with Checkpointing

Distributed training alternates between forward/backward passes (compute-bound) and gradient synchronization (communication-bound). Compute phases map well to remote GPUs; synchronization requires more bandwidth than gRPC provides for large models, but several mitigations make training practical. Gradient compression reduces synchronization volume by 100-1000x with minimal accuracy impact. Asynchronous algorithms such as local SGD reduce synchronization frequency from every step to every N steps. Ray coordinates multi-node training, with the proxy handling intra-step CUDA dispatch while Ray's built-in all-reduce handles gradient exchange. Checkpointing every few steps ensures provider failures lose no more than minutes of progress.

### 2.4.4 Rendering: Perfect Fit (Embarrassingly Parallel)

GPU rendering—frame generation, batch image processing, video transcoding—is "embarrassingly parallel": each frame processes independently with no inter-GPU communication. The proxy distributes frames across remote GPUs and collects results. The only network traffic is input upload and output download through Arrow Flight; a 4K frame at 30 FPS requires approximately 1.5 Gbps, well within modern cloud networking capacity.

| Workload Type | Computation to Communication | Proxy Suitability | Bandwidth Required | Acceptable Latency | Key Mitigation | Provider |
|---|---|---|---|---|---|---|
| Fine-grained HPC (MD, CFD) | Very low | **Not suitable** | 40-100 Gbps | 5-50 µs | Route to local GPUs or IB clusters | CoreWeave |
| LLM Inference (batched) | High | **Excellent fit** | 1-10 Gbps | 10-100 ms | Batching hides dispatch overhead | Chutes, RunPod, io.net |
| LLM Training (data-parallel) | Medium | **Good fit** | 10-25 Gbps | 1-10 ms | Gradient compression, async algorithms | io.net, CoreWeave |
| Fine-tuning (LoRA/QLoRA) | High | **Excellent fit** | 1 Gbps | 10-100 ms | Small adapter weights, minimal sync | Any provider |
| GPU Rendering (4K30) | Very high | **Perfect fit** | 1-10 Gbps | 33 ms/frame | Frame-level parallelism, streaming I/O | RunPod, io.net |
| Small model inference | High | **Good fit** | 100 Mbps-1 Gbps | 100-500 ms | Minimal data transfer | Chutes, RunPod |
| Real-time gaming / streaming | N/A | **Not suitable** | N/A | <16 ms | Hard latency bound | Local GPU only |

**Table 2.4 — Workload Suitability Matrix for Remote GPU Proxy.** Suitability depends on the ratio of computation to communication. Bandwidth requirements span three orders of magnitude, from 100 Mbps for small inference to 100 Gbps for fine-grained HPC. The gRPC GPU Proxy at 10 Gbps satisfies all workloads except HPC simulations, which need InfiniBand. Batched inference and rendering are ideal; real-time applications with hard latency bounds should use local physical GPUs.

The HelixCluster GPU Proxy represents a pragmatic engineering tradeoff. It does not achieve the sub-microsecond latency of GPUDirect RDMA, nor the full transparency of rCUDA, nor the VM-level isolation of NVIDIA vGPU. Instead, it delivers what the others cannot: a unified abstraction across every major GPU provider that integrates with Kubernetes scheduling and reduces compute costs by 50-80% while maintaining acceptable performance for inference, training, and rendering. The Pool Manager's workload-aware routing ensures each job lands on the right tier: local GPUs for latency-sensitive tasks, proxied remote GPUs for cost-optimized batch work, and InfiniBand-connected clusters for cases demanding maximum inter-GPU bandwidth.
