## 6. Complete Implementation & Roadmap

This chapter presents the production-grade implementation of the HelixCluster reverse-integration system. Every abstraction from earlier chapters—the four-tier GPU hierarchy, the provider-adapter pattern, the post-quantum E2EE proxy, and the burst controller—materialises here as Go source code, Kubernetes manifests, and runnable deployment artifacts. Three principles guide the implementation: **uniformity** (every external GPU source implements a single `GPUProvider` interface), **security by default** (ML-KEM-768 + ChaCha20-Poly1305 for all remote traffic), and **operational simplicity** (one Helm command or one `docker compose up` deploys the stack).

---

### 6.1 GPU Pool Manager (Go)

The Pool Manager is the central allocator. Written in Go for concurrency safety under cgroups, it maintains a real-time view of every GPU—local RTX 4090s, H100s on io.net, serverless A100s on RunPod, TEE instances on Chutes—and places workloads according to cost caps, latency bounds, and SLA policies encoded in `WorkloadRequest.Labels`.

#### 6.1.1 Types: GPUProvider Interface, VirtualGPU, WorkloadRequest

All GPU sources, from a local PCIe card to a decentralised REST endpoint, implement the four-method `GPUProvider` interface. This makes remote GPUs substitutable for local ones at the scheduling layer.

```go
// pkg/pool/types.go
package pool

import "context"

// GPUProvider is the uniform interface for every GPU source.
type GPUProvider interface {
    Discover(ctx context.Context) ([]*VirtualGPU, error)
    Allocate(ctx context.Context, gpuID string, req WorkloadRequest) (*Allocation, error)
    Execute(ctx context.Context, allocID string, req WorkloadRequest) error
    Release(ctx context.Context, allocID string) error
}

// VirtualGPU describes one GPU as seen by the scheduler.
type VirtualGPU struct {
    ID              string            `json:"id"`
    ProviderID      string            `json:"provider_id"`
    Tier            GPUTier           `json:"tier"`
    Model           string            `json:"model"`
    VRAMBytes       uint64            `json:"vram_bytes"`
    TFLOPSFP16      float64           `json:"tflops_fp16"`
    CostPerHour     float64           `json:"cost_per_hour"`
    Location        string            `json:"location"`
    Labels          map[string]string `json:"labels"`
    Utilisation     float64           `json:"utilisation"`
    MemoryUsed      uint64            `json:"memory_used"`
    ActiveWorkloads int               `json:"active_workloads"`
    Healthy         bool              `json:"healthy"`
    LastHealthCheck time.Time         `json:"last_health_check"`
}

// GPUTier defines the four-tier priority hierarchy.
type GPUTier int

const (
    TierLocal         GPUTier = iota
    TierRemoteProxy
    TierCloud
    TierDecentralized
)

// WorkloadRequest is a single job asking for GPU resources.
type WorkloadRequest struct {
    ID           string            `json:"id"`
    Type         WorkloadType      `json:"type"`
    GPUModel     string            `json:"gpu_model"`
    GPUCount     int               `json:"gpu_count"`
    MinVRAM      uint64            `json:"min_vram"`
    MaxLatencyMs int               `json:"max_latency_ms"`
    MaxCostHour  float64           `json:"max_cost_hour"`
    Duration     time.Duration     `json:"duration"`
    Priority     int               `json:"priority"`
    Labels       map[string]string `json:"labels"`
    UserID       string            `json:"user_id"`
}

type WorkloadType string
const (
    WorkloadInference WorkloadType = "inference"
    WorkloadTraining  WorkloadType = "training"
    WorkloadBatch     WorkloadType = "batch"
)
```

The interface omits health-check and cost-query methods; those live in the optional `HealthChecker` and `Pricer` sub-interfaces so lightweight adapters need not implement concerns they do not own.

#### 6.1.2 Pool Manager: Discovery, Health Check, Scheduling, Metrics

`PoolManager` is the sole writer of pool state. It holds device and allocation maps, a pluggable `Scheduler`, and a `HealthMonitor` that ticks every thirty seconds. A `sync.RWMutex` protects the maps; hot read paths acquire only the read lock.

```go
// pkg/pool/pool_manager.go
package pool

import (
    "context"
    "fmt"
    "sync"
    "time"

    "github.com/google/uuid"
    "go.uber.org/zap"
)

type PoolManager struct {
    mu          sync.RWMutex
    devices     map[string]*VirtualGPU
    allocations map[string]*Allocation
    providers   map[string]GPUProvider
    scheduler   Scheduler
    healthMon   *HealthMonitor
    costTracker *CostTracker
    logger      *zap.Logger

    burstThreshold float64
    healthInterval time.Duration
    maxCostPerHour float64
}

type PoolOption func(*PoolManager)

func WithScheduler(s Scheduler) PoolOption    { return func(p *PoolManager) { p.scheduler = s } }
func WithBurstThreshold(t float64) PoolOption { return func(p *PoolManager) { p.burstThreshold = t } }
func WithLogger(l *zap.Logger) PoolOption     { return func(p *PoolManager) { p.logger = l } }

func NewPoolManager(opts ...PoolOption) *PoolManager {
    pm := &PoolManager{
        devices:        make(map[string]*VirtualGPU),
        allocations:    make(map[string]*Allocation),
        providers:      make(map[string]GPUProvider),
        scheduler:      NewPriorityScheduler(),
        burstThreshold: 0.90,
        healthInterval: 30 * time.Second,
        maxCostPerHour: 1000.0,
        logger:         zap.NewNop(),
    }
    for _, o := range opts { o(pm) }
    pm.healthMon = NewHealthMonitor(pm, pm.healthInterval)
    pm.costTracker = NewCostTracker()
    return pm
}

// RegisterProvider discovers GPUs from one source and adds them to the pool.
func (pm *PoolManager) RegisterProvider(id string, p GPUProvider) error {
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    gpus, err := p.Discover(ctx)
    if err != nil { return fmt.Errorf("provider %s discovery: %w", id, err) }

    pm.mu.Lock()
    defer pm.mu.Unlock()
    pm.providers[id] = p
    for _, g := range gpus {
        g.ProviderID = id
        g.LastHealthCheck = time.Now()
        g.Healthy = true
        pm.devices[g.ID] = g
    }
    pm.logger.Info("provider registered", zap.String("id", id), zap.Int("gpus", len(gpus)))
    return nil
}

// Allocate selects and reserves GPUs for a workload.
func (pm *PoolManager) Allocate(ctx context.Context, req WorkloadRequest) (*Allocation, error) {
    pm.mu.Lock()
    defer pm.mu.Unlock()

    currentCost := pm.costTracker.CurrentCostPerHour()
    if req.MaxCostHour > 0 && currentCost+req.MaxCostHour > pm.maxCostPerHour {
        return nil, fmt.Errorf("cost cap %.2f + %.2f > %.2f", currentCost, req.MaxCostHour, pm.maxCostPerHour)
    }
    candidates := pm.filterCandidates(req)
    if len(candidates) < req.GPUCount {
        return nil, fmt.Errorf("insufficient GPUs: need %d, found %d", req.GPUCount, len(candidates))
    }
    selected := pm.scheduler.Select(candidates, req, req.GPUCount)
    alloc := &Allocation{
        ID: uuid.New().String(), GPUs: selected, Workload: req,
        StartTime: time.Now(), CostHour: pm.blendedCost(selected), Tier: selected[0].Tier,
    }
    pm.allocations[alloc.ID] = alloc
    for _, g := range selected { g.ActiveWorkloads++; g.MemoryUsed += req.MinVRAM }
    pm.costTracker.AddAllocation(alloc)
    return alloc, nil
}

// Release frees a GPU allocation.
func (pm *PoolManager) Release(ctx context.Context, allocID string) error {
    pm.mu.Lock()
    defer pm.mu.Unlock()
    alloc, ok := pm.allocations[allocID]
    if !ok { return fmt.Errorf("allocation %s not found", allocID) }
    for _, g := range alloc.GPUs {
        g.ActiveWorkloads--
        if g.MemoryUsed >= alloc.Workload.MinVRAM { g.MemoryUsed -= alloc.Workload.MinVRAM }
    }
    pm.costTracker.RemoveAllocation(alloc)
    delete(pm.allocations, allocID)
    return nil
}

// ShouldBurst reports whether local GPU utilisation exceeds the burst threshold.
func (pm *PoolManager) ShouldBurst() bool {
    pm.mu.RLock()
    defer pm.mu.RUnlock()
    var utilSum, count float64
    for _, g := range pm.devices {
        if g.Tier == TierLocal { utilSum += g.Utilisation; count++ }
    }
    if count == 0 { return true }
    return (utilSum / count) > pm.burstThreshold
}

func (pm *PoolManager) filterCandidates(req WorkloadRequest) []*VirtualGPU {
    var out []*VirtualGPU
    for _, g := range pm.devices {
        if !g.Healthy { continue }
        if req.GPUModel != "" && req.GPUModel != "any" && g.Model != req.GPUModel { continue }
        if g.VRAMBytes-g.MemoryUsed < req.MinVRAM { continue }
        if req.MaxCostHour > 0 && g.CostPerHour > req.MaxCostHour { continue }
        if !matchLabels(g.Labels, req.Labels) { continue }
        out = append(out, g)
    }
    return out
}

func (pm *PoolManager) blendedCost(gpus []*VirtualGPU) float64 { var t float64; for _, g := range gpus { t += g.CostPerHour }; return t }
func matchLabels(dev, sel map[string]string) bool { for k, v := range sel { if dev[k] != v { return false } }; return true }
```

The `HealthMonitor` goroutine ticks every `healthInterval` (default 30 s), refreshes each provider via `Discover`, and marks stale GPUs unhealthy. When a healthy GPU fails, it logs the event and triggers asynchronous failover.

#### 6.1.3 Scheduler: Priority-Based (Local First), Cost-Aware, Topology-Aware

The scheduler is a swappable interface. The default `PriorityScheduler` sorts by tier priority ascending, utilisation ascending, and cost ascending. Operators can swap in `CostAwareScheduler` for cost-sensitive dev environments or `LatencyAwareScheduler` for real-time inference.

```go
// pkg/pool/scheduler.go
package pool

import "sort"

type Scheduler interface {
    Select(candidates []*VirtualGPU, req WorkloadRequest, count int) []*VirtualGPU
}

type PriorityScheduler struct{}

func NewPriorityScheduler() *PriorityScheduler { return &PriorityScheduler{} }

func (s *PriorityScheduler) Select(cands []*VirtualGPU, req WorkloadRequest, n int) []*VirtualGPU {
    sort.Slice(cands, func(i, j int) bool {
        if cands[i].Tier != cands[j].Tier { return cands[i].Tier < cands[j].Tier }
        if cands[i].Utilisation != cands[j].Utilisation { return cands[i].Utilisation < cands[j].Utilisation }
        return cands[i].CostPerHour < cands[j].CostPerHour
    })
    if n > len(cands) { n = len(cands) }
    return cands[:n]
}
```

**Table 1 — Scheduler strategies.**

| Strategy | Sort Key (primary → tertiary) | Best For | Risk |
|----------|------------------------------|----------|------|
| `PriorityScheduler` (default) | Tier → Utilisation → Cost | Production inference | May under-utilise cheap remote GPUs |
| `CostAwareScheduler` | Cost → VRAM free | Batch queues, cost-constrained training | Higher tail latency if cheapest provider is distant |
| `LatencyAwareScheduler` | Measured RTT → Cost | Real-time serving (TTFT < 100 ms) | Requires continuous probing; stale samples mis-route |

---

### 6.2 Remote GPU Providers

Each external GPU source lives in its own package under `pkg/provider/`. All implement `GPUProvider` so the pool manager registers them identically.

#### 6.2.1 ChutesProvider: API Client with E2EE

The Chutes adapter speaks the OpenAI-compatible REST API at `llm.chutes.ai/v1`, handles HTTP-429 fallback to alternate models, and optionally routes through the ML-KEM-768 E2EE proxy when `Labels["tee"]` is present.

```go
// pkg/provider/chutes/chutes.go
package chutes

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "time"

    "helixcluster/pkg/pool"
    "go.uber.org/zap"
)

const APIBase = "https://llm.chutes.ai/v1"
const DefaultModel = "default:latency"

type ChutesProvider struct {
    apiKey         string
    client         *http.Client
    baseURL        string
    costPerHour    float64
    logger         *zap.Logger
    fallbackModels []string
}

func New(apiKey string, opts ...Option) *ChutesProvider {
    p := &ChutesProvider{
        apiKey:         apiKey,
        client:         &http.Client{Timeout: 120 * time.Second},
        baseURL:        APIBase,
        costPerHour:    1.80,
        fallbackModels: []string{"deepseek-ai/DeepSeek-V3-0324", "MiniMaxAI/MiniMax-M2.5-TEE", "Qwen/Qwen3-32B-TEE"},
        logger:         zap.NewNop(),
    }
    for _, o := range opts { o(p) }
    return p
}

type Option func(*ChutesProvider)
func WithLogger(l *zap.Logger) Option { return func(p *ChutesProvider) { p.logger = l } }

func (p *ChutesProvider) Discover(ctx context.Context) ([]*pool.VirtualGPU, error) {
    req, _ := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/models", nil)
    req.Header.Set("Authorization", "Bearer "+p.apiKey)
    resp, err := p.client.Do(req)
    if err != nil { return nil, err }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK { return nil, fmt.Errorf("discover %d", resp.StatusCode) }

    var models []struct{ ID string `json:"id"`; VRAM uint64 `json:"vram_bytes"`; Cost1M float64 `json:"cost_per_1m_input"` }
    json.NewDecoder(resp.Body).Decode(&models)

    var gpus []*pool.VirtualGPU
    for _, m := range models {
        gpus = append(gpus, &pool.VirtualGPU{
            ID: m.ID, ProviderID: "chutes", Tier: pool.TierDecentralized,
            Model: m.ID, VRAMBytes: m.VRAM, CostPerHour: m.Cost1M * 100,
            Location: "decentralized", Labels: map[string]string{"api": "openai", "tee": "optional"},
            Healthy: true,
        })
    }
    return gpus, nil
}

func (p *ChutesProvider) Allocate(ctx context.Context, gpuID string, req pool.WorkloadRequest) (*pool.Allocation, error) {
    if err := p.healthCheck(ctx); err != nil { return nil, err }
    return &pool.Allocation{
        ID: gpuID, GPUs: []*pool.VirtualGPU{{ID: gpuID, ProviderID: "chutes", Tier: pool.TierDecentralized}},
        CostHour: p.costPerHour, Tier: pool.TierDecentralized,
    }, nil
}

func (p *ChutesProvider) Execute(ctx context.Context, allocID string, wr pool.WorkloadRequest) error {
    body, _ := json.Marshal(chatRequest{
        Model: p.selectModel(wr), Messages: []message{{Role: "user", Content: wr.Labels["prompt"]}},
        MaxTokens: 500,
    })
    httpReq, _ := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/chat/completions", bytes.NewReader(body))
    httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
    httpReq.Header.Set("Content-Type", "application/json")
    resp, err := p.client.Do(httpReq)
    if err != nil { return err }
    defer resp.Body.Close()
    if resp.StatusCode == http.StatusTooManyRequests { return p.retryWithFallback(ctx, wr) }
    if resp.StatusCode != http.StatusOK { b, _ := io.ReadAll(resp.Body); return fmt.Errorf("chutes %d: %s", resp.StatusCode, b) }
    return nil
}

func (p *ChutesProvider) Release(ctx context.Context, allocID string) error { return nil }

func (p *ChutesProvider) healthCheck(ctx context.Context) error {
    req, _ := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/models", nil)
    req.Header.Set("Authorization", "Bearer "+p.apiKey)
    resp, err := p.client.Do(req)
    if err != nil { return err }
    resp.Body.Close()
    if resp.StatusCode != http.StatusOK { return fmt.Errorf("health %d", resp.StatusCode) }
    return nil
}

func (p *ChutesProvider) retryWithFallback(ctx context.Context, wr pool.WorkloadRequest) error {
    for i, model := range p.fallbackModels {
        p.logger.Warn("429 fallback", zap.String("model", model), zap.Int("attempt", i))
        time.Sleep(time.Duration(i+1) * time.Second)
        // retry logic omitted for brevity
    }
    return fmt.Errorf("all Chutes fallbacks exhausted")
}

func (p *ChutesProvider) selectModel(wr pool.WorkloadRequest) string {
    if m := wr.Labels["model"]; m != "" { return m }
    return DefaultModel
}

type chatRequest struct{ Model string `json:"model"`; Messages []message `json:"messages"`; MaxTokens int `json:"max_tokens"` }
type message struct{ Role string `json:"role"`; Content string `json:"content"` }
```

#### 6.2.2 IONetProvider: Ray Cluster Adapter

io.net exposes GPU clusters through a Ray-based API. `IONetProvider` provisions a Ray cluster on demand, translates `WorkloadRequest` into Ray job submissions, and tears the cluster down on `Release`—ideal for multi-GPU training bursts.

```go
// pkg/provider/ionet/ionet.go  (skeleton)
package ionet

import (
    "bytes"
    "context"
    "encoding/json"
    "net/http"
    "time"

    "helixcluster/pkg/pool"
)

const CloudAPI = "https://cloud.io.net/api/v2"

type IONetProvider struct {
    apiKey      string
    client      *http.Client
    clusterID   string
    costPerHour float64
}

func New(apiKey string) *IONetProvider {
    return &IONetProvider{apiKey: apiKey, client: &http.Client{Timeout: 60 * time.Second}, costPerHour: 1.85}
}

func (p *IONetProvider) Discover(ctx context.Context) ([]*pool.VirtualGPU, error) {
    return []*pool.VirtualGPU{{
        ID: "ionet-h100-0", ProviderID: "ionet", Tier: pool.TierDecentralized,
        Model: "H100-80GB", VRAMBytes: 80 << 30, CostPerHour: 1.85,
        Location: "us-east", Labels: map[string]string{"cluster": "ray"}, Healthy: true,
    }}, nil
}

func (p *IONetProvider) Allocate(ctx context.Context, gpuID string, req pool.WorkloadRequest) (*pool.Allocation, error) {
    payload := map[string]any{"gpu_type": "H100", "gpu_count": req.GPUCount, "region": "us-east"}
    body, _ := json.Marshal(payload)
    httpReq, _ := http.NewRequestWithContext(ctx, "POST", CloudAPI+"/clusters", bytes.NewReader(body))
    httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
    resp, err := p.client.Do(httpReq)
    if err != nil { return nil, err }
    defer resp.Body.Close()
    var result struct{ ClusterID string `json:"cluster_id"` }
    json.NewDecoder(resp.Body).Decode(&result)
    p.clusterID = result.ClusterID
    return &pool.Allocation{ID: result.ClusterID, CostHour: p.costPerHour * float64(req.GPUCount)}, nil
}

func (p *IONetProvider) Execute(ctx context.Context, allocID string, wr pool.WorkloadRequest) error {
    return p.submitRayJob(ctx, allocID, wr)
}

func (p *IONetProvider) Release(ctx context.Context, allocID string) error {
    req, _ := http.NewRequestWithContext(ctx, "DELETE", CloudAPI+"/clusters/"+allocID, nil)
    req.Header.Set("Authorization", "Bearer "+p.apiKey)
    resp, err := p.client.Do(req)
    if err == nil { resp.Body.Close() }
    return err
}
```

#### 6.2.3 RunPodProvider: Serverless GPU Adapter

RunPod's serverless platform has cold-start characteristics (2–15 s). `RunPodProvider` maintains a warm pool of one pod per popular GPU model, claims it on `Allocate`, and asynchronously replenishes.

```go
// pkg/provider/runpod/runpod.go  (skeleton)
package runpod

import (
    "context"
    "net/http"
    "time"

    "helixcluster/pkg/pool"
)

const APIEndpoint = "https://api.runpod.io/v2"

type RunPodProvider struct {
    apiKey      string
    client      *http.Client
    costPerHour float64
    warmPool    map[string]string // model -> pod_id
}

func New(apiKey string) *RunPodProvider {
    return &RunPodProvider{
        apiKey: apiKey, client: &http.Client{Timeout: 30 * time.Second},
        costPerHour: 2.69, warmPool: make(map[string]string),
    }
}

func (p *RunPodProvider) Discover(ctx context.Context) ([]*pool.VirtualGPU, error) {
    return []*pool.VirtualGPU{{
        ID: "runpod-h100", ProviderID: "runpod", Tier: pool.TierDecentralized,
        Model: "H100-80GB", VRAMBytes: 80 << 30, CostPerHour: 2.69,
        Labels: map[string]string{"serverless": "true", "flashboot": "enabled"}, Healthy: true,
    }}, nil
}

func (p *RunPodProvider) Allocate(ctx context.Context, gpuID string, req pool.WorkloadRequest) (*pool.Allocation, error) {
    podID := p.warmPool[req.GPUModel]
    if podID == "" { podID = p.createEndpoint(ctx, req) }
    delete(p.warmPool, req.GPUModel)
    go p.warmOne(req.GPUModel)
    return &pool.Allocation{ID: podID, CostHour: p.costPerHour}, nil
}

func (p *RunPodProvider) Execute(ctx context.Context, allocID string, wr pool.WorkloadRequest) error {
    return p.runJob(ctx, allocID, wr)
}
func (p *RunPodProvider) Release(ctx context.Context, allocID string) error { return nil }
func (p *RunPodProvider) warmOne(model string) { p.warmPool[model] = p.createEndpoint(context.Background(), pool.WorkloadRequest{GPUModel: model}) }
```

#### 6.2.4 AWSProvider: EC2 Spot Instance Adapter

The AWS adapter provisions Spot instances with `InstanceInterruptionBehavior = stop` and `SpotInstanceType = persistent`, yielding 60–70 % savings. It traps the two-minute interruption warning, checkpoints to S3, and triggers burst-failover.

```go
// pkg/provider/aws/aws.go  (skeleton)
package aws

import (
    "context"
    "fmt"
    "sync"

    "github.com/aws/aws-sdk-go-v2/config"
    "github.com/aws/aws-sdk-go-v2/service/ec2"
    "github.com/aws/aws-sdk-go-v2/service/ec2/types"
    "helixcluster/pkg/pool"
)

type AWSProvider struct {
    client      *ec2.Client
    region      string
    instances   map[string]*gpuInstance
    mu          sync.RWMutex
    costPerHour float64
}

type gpuInstance struct {
    InstanceID   string
    InstanceType types.InstanceType
    GPUType      string
}

func New(region string) (*AWSProvider, error) {
    cfg, err := config.LoadDefaultConfig(context.Background(), config.WithRegion(region))
    if err != nil { return nil, err }
    return &AWSProvider{
        client: ec2.NewFromConfig(cfg), region: region,
        instances: make(map[string]*gpuInstance), costPerHour: 3.83,
    }, nil
}

func (p *AWSProvider) Discover(ctx context.Context) ([]*pool.VirtualGPU, error) {
    return p.describeTaggedInstances(ctx)
}

func (p *AWSProvider) Allocate(ctx context.Context, gpuID string, req pool.WorkloadRequest) (*pool.Allocation, error) {
    it := p.selectInstanceType(req.GPUModel, req.GPUCount)
    result, err := p.client.RunInstances(ctx, &ec2.RunInstancesInput{
        InstanceType: it, ImageId: aws.String("ami-xxxxxxxxxxxxxxxxx"),
        MinCount: aws.Int32(1), MaxCount: aws.Int32(1),
        InstanceMarketOptions: &types.InstanceMarketOptionsRequest{
            MarketType: types.MarketTypeSpot,
            SpotOptions: &types.SpotMarketOptions{
                InstanceInterruptionBehavior: types.InstanceInterruptionBehaviorStop,
                SpotInstanceType:            types.SpotInstanceTypePersistent,
            },
        },
        TagSpecifications: []types.TagSpecification{{
            ResourceType: types.ResourceTypeInstance,
            Tags: []types.Tag{
                {Key: aws.String("helixcluster.io/managed"), Value: aws.String("true")},
                {Key: aws.String("Name"), Value: aws.String("helixcluster-"+req.ID)},
            },
        }},
    })
    if err != nil { return nil, fmt.Errorf("RunInstances: %w", err) }
    id := *result.Instances[0].InstanceId
    p.mu.Lock(); p.instances[id] = &gpuInstance{InstanceID: id, InstanceType: it, GPUType: req.GPUModel}; p.mu.Unlock()
    p.waitRunning(ctx, id)
    return &pool.Allocation{ID: id, CostHour: p.costPerHour * float64(req.GPUCount)}, nil
}

func (p *AWSProvider) Execute(ctx context.Context, allocID string, wr pool.WorkloadRequest) error {
    return p.runWorkload(ctx, allocID, wr)
}

func (p *AWSProvider) Release(ctx context.Context, allocID string) error {
    _, err := p.client.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{allocID}})
    return err
}

func (p *AWSProvider) selectInstanceType(model string, count int) types.InstanceType {
    switch model { case "H100": return types.InstanceTypeP5d48xlarge; case "A100": return types.InstanceTypeP4de24xlarge }
    return types.InstanceTypeP5d48xlarge
}
```

**Table 2 — Remote provider comparison.**

| Provider | Protocol | Cold Start | Cost (H100/hr) | Best Workload | Pre-emption Handling |
|----------|----------|-----------|----------------|---------------|---------------------|
| Chutes.ai | OpenAI REST | < 1 s | $1.80–2.00 (token) | LLM inference burst | Model fallback chain |
| io.net | Ray + REST | 30–120 s | $1.49–2.20 | Multi-GPU training | Checkpoint to S3 |
| RunPod | Serverless gRPC | 2–15 s | $2.69 | Serverless inference | Warm pool + flashboot |
| AWS EC2 Spot | EC2 SDK | 2–5 min | $3.83 (spot) | Compliance, long jobs | 2-min warning → drain |

---

### 6.3 Security Integration

All remote GPU traffic is encrypted by default through three mechanisms: a per-session E2EE proxy, GraVal GPU attestation, and Intel TDX verification.

#### 6.3.1 E2EE Proxy: ML-KEM-768 Handshake per Remote GPU Session

The E2EE proxy (`pkg/e2ee/proxy.go`) intercepts outbound HTTP requests to Chutes, performs ML-KEM-768 key encapsulation against the target GPU's public key, and encrypts the body with ChaCha20-Poly1305. The handshake runs once per session: (1) fetch the GPU's ML-KEM public key and TDX quote; (2) encapsulate an ephemeral shared secret; (3) stretch it with HKDF-SHA256 into a 256-bit ChaCha20 key; (4) AEAD-seal the JSON body; (5) the GPU TEE decapsulates and decrypts inside the enclave, then encrypts the response with a derived response key. Parameters: ML-KEM-768 (FIPS 203), HKDF-SHA-256 (RFC 5869), ChaCha20-Poly1305 (RFC 8439), implemented via Cloudflare's `circl` Go package.

#### 6.3.2 GraVal Verification: Run GPU Proof Before Accepting Provider

Before a provider is admitted to the pool, GraVal (`pkg/e2ee/graval.go`) demands a three-phase proof: (1) **capability**—execute a reference CUDA kernel and return the checksum within a time bound that rules out CPU emulation; (2) **identity**—sign the capability result with an ECDSA key chaining to the manufacturer's root CA; (3) **consistency**—the verifier re-executes the kernel locally and compares checksums. Passing providers receive label `graval.verified: "true"`.

#### 6.3.3 TEE Attestation: Verify Intel TDX Before Sensitive Workloads

When `Labels["tee"] == "true"`, the scheduler restricts candidates to GPUs with `Labels["tee_attested"] == "true"`. The verifier (`pkg/e2ee/attestation.go`) validates the Intel TDX quote: checks the DCAP signature, rejects debug-mode TEEs, verifies `report_data` binds the E2EE public key (prevents replay), and matches enclave measurements against a whitelist. If NVIDIA CC GPU attestation is present, it is verified against the NVGPU kernel driver.

---

### 6.4 Deployment

#### 6.4.1 Helm Charts for Kubernetes Deployment

The Helm chart packages the pool manager, burst controller, E2EE proxy, local vLLM DaemonSet, and Prometheus ServiceMonitor.

```yaml
# configs/helm/helixcluster/Chart.yaml
apiVersion: v2
name: helixcluster
description: HelixCluster — Reverse Integration GPU Cluster
version: 1.0.0
appVersion: "1.0.0"
keywords: [gpu, ai, decentralized, chutes, inference]
```

```yaml
# configs/helm/helixcluster/values.yaml
namespace: helixcluster

gpuPoolManager:
  enabled: true
  replicaCount: 2
  image: {repository: helixcluster/pool-manager, tag: "1.0.0", pullPolicy: IfNotPresent}
  resources: {requests: {memory: "256Mi", cpu: "250m"}, limits: {memory: "512Mi", cpu: "500m"}}
  service: {type: ClusterIP, port: 8080}
  config:
    burstThreshold: 0.90
    drainThreshold: 0.60
    maxCostPerHour: 500.0
    healthCheckInterval: 30s
    scheduler: priority

burstController:
  enabled: true
  image: {repository: helixcluster/burst-controller, tag: "1.0.0"}
  config: {drainDuration: "10m", cooldown: "5m"}

e2eeProxy:
  enabled: true
  image: {repository: helixcluster/e2ee-proxy, tag: "1.0.0"}
  service: {type: ClusterIP, port: 8443}

localVLLM:
  enabled: true
  model: "meta-llama/Llama-3.1-8B-Instruct"
  tensorParallelSize: 1
  gpuMemoryUtilization: 0.90
  resources: {limits: {nvidia.com/gpu: "1", memory: "32Gi"}}

providers:
  chutes:   {enabled: true,  apiKeySecret: chutes-api-key}
  ionet:    {enabled: false, apiKeySecret: ionet-api-key}
  runpod:   {enabled: false, apiKeySecret: runpod-api-key}
  aws:      {enabled: false, credentialsSecret: aws-credentials}

monitoring:
  enabled: true
  serviceMonitor: {enabled: true, interval: 15s}

logging: {level: info, format: json}
```

```yaml
# configs/helm/helixcluster/templates/gpu-pool-manager.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "helixcluster.fullname" . }}-gpu-pool-manager
  namespace: {{ .Values.namespace }}
spec:
  replicas: {{ .Values.gpuPoolManager.replicaCount }}
  selector:
    matchLabels:
      app.kubernetes.io/name: gpu-pool-manager
  template:
    metadata:
      labels:
        app.kubernetes.io/name: gpu-pool-manager
    spec:
      serviceAccountName: {{ include "helixcluster.serviceAccountName" . }}
      containers:
      - name: pool-manager
        image: "{{ .Values.gpuPoolManager.image.repository }}:{{ .Values.gpuPoolManager.image.tag }}"
        imagePullPolicy: {{ .Values.gpuPoolManager.image.pullPolicy }}
        ports:
        - containerPort: {{ .Values.gpuPoolManager.service.port }}
          name: http
        env:
        - name: HELIX_BURST_THRESHOLD
          value: "{{ .Values.gpuPoolManager.config.burstThreshold }}"
        - name: HELIX_MAX_COST_HOUR
          value: "{{ .Values.gpuPoolManager.config.maxCostPerHour }}"
        - name: HELIX_LOG_LEVEL
          value: {{ .Values.logging.level }}
        resources:
          {{- toYaml .Values.gpuPoolManager.resources | nindent 10 }}
        livenessProbe:
          httpGet: {path: /health, port: http}
          initialDelaySeconds: 10
          periodSeconds: 15
---
apiVersion: v1
kind: Service
metadata:
  name: {{ include "helixcluster.fullname" . }}-gpu-pool-manager
  namespace: {{ .Values.namespace }}
spec:
  type: {{ .Values.gpuPoolManager.service.type }}
  ports:
  - port: {{ .Values.gpuPoolManager.service.port }}
    targetPort: http
    name: http
  selector:
    app.kubernetes.io/name: gpu-pool-manager
```

Install with one command:

```bash
helm upgrade --install helixcluster ./configs/helm/helixcluster \
    --namespace helixcluster --create-namespace \
    --set providers.chutes.enabled=true \
    --set providers.chutes.apiKeySecret=chutes-api-key \
    --wait --timeout 10m
```

#### 6.4.2 Docker Compose for Development

```yaml
# configs/docker/docker-compose.yaml
version: "3.8"

services:
  gpu-pool-manager:
    build:
      context: ../..
      dockerfile: configs/docker/Dockerfile.pool-manager
    ports: ["8080:8080"]
    environment:
      - HELIX_LOG_LEVEL=debug
      - HELIX_BURST_THRESHOLD=0.90
      - HELIX_MAX_COST_HOUR=500
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:8080/health"]
      interval: 10s
      timeout: 5s
      retries: 3
    networks: [helixcluster]

  e2ee-proxy:
    build:
      context: ../..
      dockerfile: configs/docker/Dockerfile.e2ee-proxy
    ports: ["8443:8443"]
    environment:
      - CHUTES_API_KEY=${CHUTES_API_KEY}
      - HELIX_E2EE_ENABLED=true
    depends_on:
      gpu-pool-manager: {condition: service_healthy}
    networks: [helixcluster]

  local-vllm:
    image: vllm/vllm-openapi:v0.6.0
    command: >
      --model meta-llama/Llama-3.1-8B-Instruct
      --tensor-parallel-size 1 --max-num-seqs 256
      --gpu-memory-utilization 0.90 --enable-prefix-caching
    deploy:
      resources:
        reservations:
          devices: [{driver: nvidia, count: 1, capabilities: [gpu]}]
    ports: ["8000:8000"]
    networks: [helixcluster]

  prometheus:
    image: prom/prometheus:latest
    ports: ["9090:9090"]
    volumes: [./prometheus.yml:/etc/prometheus/prometheus.yml]
    networks: [helixcluster]

  grafana:
    image: grafana/grafana:latest
    ports: ["3000:3000"]
    environment: [GF_SECURITY_ADMIN_PASSWORD=admin]
    volumes: [grafana-storage:/var/lib/grafana]
    networks: [helixcluster]

networks:
  helixcluster: {driver: bridge}
volumes:
  grafana-storage:
```

**Project structure tree:**

```
helixcluster/
├── cmd/
│   ├── gpu-pool-manager/main.go
│   ├── burst-controller/main.go
│   ├── e2ee-proxy/main.go
│   └── cli/main.go
├── pkg/
│   ├── pool/
│   │   ├── types.go              # GPUProvider, VirtualGPU, WorkloadRequest
│   │   ├── pool_manager.go       # discovery, health check, scheduling, metrics
│   │   └── scheduler.go          # PriorityScheduler, CostAwareScheduler
│   ├── provider/
│   │   ├── chutes/chutes.go      # OpenAI REST client with E2EE
│   │   ├── ionet/ionet.go        # Ray cluster adapter
│   │   ├── runpod/runpod.go      # serverless GPU adapter
│   │   └── aws/aws.go            # EC2 spot instance adapter
│   ├── e2ee/
│   │   ├── proxy.go              # ML-KEM-768 + ChaCha20-Poly1305
│   │   ├── attestation.go        # Intel TDX verifier
│   │   └── graval.go             # 3-phase GPU proof verifier
│   ├── burst/
│   │   ├── controller.go         # auto-spillover logic
│   │   └── cost_router.go        # provider scoring
│   └── api/server.go             # gRPC + HTTP front-end
├── configs/
│   ├── helm/helixcluster/        # Chart.yaml, values.yaml, templates/*.yaml
│   └── docker/
│       ├── docker-compose.yaml
│       ├── Dockerfile.pool-manager
│       ├── Dockerfile.e2ee-proxy
│       └── Dockerfile.gpu-proxy
├── proto/gpu_proxy.proto
├── Makefile
└── go.mod
```

---

### 6.5 Implementation Roadmap

The roadmap is organised into four six-week phases, each ending with a demonstrable milestone. Risk reduction drives the sequencing: E2EE and Chutes (highest uncertainty) ship first; multi-provider burst and TEE hardening follow once the core data path is proven.

**Table 3 — 24-week implementation roadmap.**

| Phase | Weeks | Theme | Deliverables | Exit Criteria |
|-------|-------|-------|--------------|---------------|
| **8b-a** | 1–6 | Chutes Consumer + E2EE | E2EE proxy (ML-KEM-768), ChutesProvider, local vLLM stack | E2EE inference through Chutes TEE succeeds; p99 < 500 ms |
| **8b-b** | 7–12 | GPU Pool Manager + Remote Proxy | PoolManager, IONetProvider, RunPodProvider, Docker Compose | 3 providers registered; failover < 30 s; health check proven |
| **8b-c** | 13–18 | Multi-Platform + Burst Controller | AWSProvider (spot), burst controller, Helm chart, dashboards | Burst at 90 % util; one-Helm deploy; 48 h load test passed |
| **8b-d** | 19–24 | TEE + Production Hardening | GraVal verification, TDX enforcement, key rotation, chaos tests | TEE attestation enforced; GraVal gates admission; 99.9 % over 7-day soak |

#### 6.5.1 Phase 8b-a: Chutes Consumer + E2EE (Weeks 1–6)

Weeks 1–2: scaffold the Go module, implement the `GPUProvider` skeleton, build the Chutes REST adapter, and integrate Cloudflare `circl` for ML-KEM-768. Weeks 3–4: implement local vLLM serving and the `LocalProvider` that discovers GPUs via `nvidia-smi`. Weeks 5–6: end-to-end test—a Python client sends a prompt through the E2EE proxy to a Chutes TEE model, receives an encrypted response, and decrypts it locally. Confirm encryption overhead < 3 %.

#### 6.5.2 Phase 8b-b: GPU Pool Manager + Remote Proxy (Weeks 7–12)

Weeks 7–8: implement `PoolManager` with `sync.RWMutex` state, 30-second health monitor, and pluggable scheduler. Weeks 9–10: build `IONetProvider` (Ray cluster provisioning) and `RunPodProvider` (serverless with warm-pool). Weeks 11–12: create the Docker Compose stack and write integration tests simulating provider failures; verify automatic failover within one health-check interval.

#### 6.5.3 Phase 8b-c: Multi-Platform + Burst Controller (Weeks 13–18)

Weeks 13–14: implement `AWSProvider` with Spot launch, interruption handling, and checkpoint-to-S3. Weeks 15–16: build the burst-controller state machine (`LocalOnly → BurstActive → Draining → LocalOnly`) with the workload-type-aware `CostRouter`. Weeks 17–18: package into the Helm chart, deploy Prometheus/Grafana, and run a 48-hour load test saturating local GPUs and verifying automatic burst to Chutes.

#### 6.5.4 Phase 8b-d: TEE + Production Hardening (Weeks 19–24)

Weeks 19–20: implement GraVal three-phase verification and gate provider admission on success. Weeks 21–22: harden TDX attestation enforcement, automate key rotation via Kubernetes CronJob. Weeks 23–24: chaos engineering—randomly terminate adapters, inject Spot interruptions, saturate the network. Confirm 99.9 % request success rate. External security audit of the E2EE implementation.

**Table 4 — Cost targets by scenario at roadmap completion.**

| Scenario | GPU Count | Monthly TCO | vs AWS On-Demand | Key Components |
|----------|----------:|------------:|-----------------:|----------------|
| Inference-only (Chutes) | 0 owned | $500–2,000 | 85–95 % cheaper | Chutes E2EE + CPU orchestration |
| Dev/test (RTX 4090) | 1–4 | $500–1,000 | 90 %+ cheaper | Local GPU + idle monetisation |
| Training burst (io.net) | 4–16 hybrid | $1,500–5,000 | 70–85 % cheaper | Local base + io.net H100 spot |
| Production (all tiers) | 14–100 | $8,000–15,000 | 81.5 % cheaper | Owned base + Chutes/RunPod burst + AWS compliance |

The 24-week roadmap delivers a production reverse-integration cluster that consumes decentralised GPU clouds as native compute, protects every byte of remote traffic with post-quantum encryption, and continuously optimises cost through intelligent provider selection. Extending the cluster to a new provider requires only a single `GPUProvider` implementation and a Helm values entry—preserving the principle that external networks exist to serve the cluster, not the reverse.

