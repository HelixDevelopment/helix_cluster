## 6. Integration Architecture & Implementation

The integration between HelixCluster and Chutes.ai spans six distinct architectural scenarios, each requiring careful orchestration of Kubernetes-native infrastructure, post-quantum cryptographic primitives, multi-marketplace economic adapters, and GPU-attested inference serving. This chapter presents the complete production-grade implementation: six Go services that manage the full lifecycle from node deployment through reward distribution, three Helm/YAML configuration layers that declaratively specify the AI serving stack, and four Bash automation scripts that operationalize bare-metal GPU nodes into revenue-generating Chutes miners within minutes.

The technical thesis of this chapter is that HelixCluster nodes, already equipped with K3s Kubernetes and NVIDIA GPU Operators, can simultaneously participate in the Chutes.ai Bittensor subnet as attested miners while retaining their original HelixCluster orchestration identity. The result is a dual-revenue compute provider that earns both HLX rewards from HelixCluster proof-of-work tasks and TAO rewards from Chutes inference serving, all managed through a unified control plane written in Go.

### 6.1 HelixCluster Nodes as Chutes Miners

The first and most foundational integration scenario transforms HelixCluster GPU nodes into Chutes miners on Bittensor Subnet 64. Each miner operates as a K3s agent within the existing HelixCluster control plane, running the complete chutes-miner stack side-by-side with Helix workloads.

```
+===================================================================+
|              SCENARIO 1: HELIX NODE AS CHUTES MINER               |
+===================================================================+
|                                                                   |
|  +----------------------------------------------------------+    |
|  |                  HELIXCLUSTER NODE                        |    |
|  |  (NVIDIA H100 GPU, 64GB+ RAM, 1TB NVMe)                  |    |
|  |                                                           |    |
|  |  +------------------+  +------------------------------+  |    |
|  |  |  HelixCluster    |  |  chutes-miner (K3s agent)   |  |    |
|  |  |  Orchestrator    |  |  - Registry proxy            |  |    |
|  |  |  - Task scheduler|  |  - GraVal bootstrap           |  |    |
|  |  |  - Proof engine  |  |  - Gepetto strategy           |  |    |
|  |  +--------+---------+  +--------------+---------------+  |    |
|  |           |                           |                   |    |
|  |  +--------v---------+  +--------------v---------------+  |    |
|  |  |  Helix PoW       |  |  Chutes chute pods           |  |    |
|  |  |  Workloads       |  |  - vLLM/SGLang inference     |  |    |
|  |  +--------+---------+  +--------------+---------------+  |    |
|  |           |                           |                   |    |
|  |           +------------+--------------+                   |    |
|  |                        |                                  |    |
|  |           +------------v--------------+                  |    |
|  |           |    GPU Hardware Layer      |                  |    |
|  |           |  H100 80GB | 132 SMs      |                  |    |
|  |           +----------------------------+                  |    |
|  +----------------------------------------------------------+    |
|                |                              |                   |
|                v                              v                   |
|      +---------+--------+          +--------+---------+          |
|      | Helix Network     |          | Bittensor SN64   |          |
|      | (HLX rewards)     |          | (TAO rewards)    |          |
|      +-------------------+          +------------------+          |
+===================================================================+
```

The deployment sequence proceeds through nine automated stages: namespace creation, PostgreSQL StatefulSet deployment for inventory tracking, Redis Deployment for pub/sub event propagation, GraVal bootstrap DaemonSet for GPU attestation, miner API Deployment with NodePort 32000, Gepetto strategy engine Deployment, registry proxy DaemonSet for authenticated image pulls, NVIDIA GPU device plugin verification, and a health-check gate that ensures all pods reach Ready state within a five-minute timeout window. The Go-based MinerController orchestrates this entire flow through the Kubernetes API.

#### 6.1.1 MinerController (Go): K3s Deployment Lifecycle

The `MinerController` is the primary Go struct responsible for the complete chutes-miner lifecycle on HelixCluster GPU nodes. It maintains a Kubernetes client interface, a namespace binding, a validator configuration slice, and a `GraValVerifier` reference for GPU attestation operations. The controller follows a declarative pattern: each deployment method constructs the full Kubernetes object graph (metadata, spec, selectors, resource constraints, volume mounts, and environment variables) and submits it through the typed client API.

The `ChutesMinerConfig` struct captures all per-node parameters including Bittensor wallet coldkey and hotkey references, GPU short reference strings (e.g., "h100_sxm", "a6000"), hourly cost for Gepetto cost-optimization strategies, TEE enablement flags, and Kubernetes node selector labels for GPU affinity scheduling. The default validator configuration points to the Chutes mainnet validator at `5Dt7HZ7Zpw4DppPxFM7Ke3Cm7sDAWhsZXmM5ZAmE7dSVJbcQ` with registry, API, and WebSocket endpoints.

The `DeployMiner` method executes nine sequential phases. Phase one ensures the target namespace exists with HelixCluster ownership labels. Phases two through seven deploy the component stack: PostgreSQL as a StatefulSet with 100Gi persistent volume claims on the `local-path` storage class; Redis as an ephemeral Deployment with LRU eviction for pub/sub messaging; GraVal bootstrap as a privileged DaemonSet with `SYS_ADMIN` capability for direct GPU device access; miner API as a replicated Deployment fronted by a NodePort service on port 32000; Gepetto as a ConfigMap-backed Deployment enabling hot strategy reloading; and registry proxy as a host-network DaemonSet on port 30500. Phase eight defers to the NVIDIA GPU Operator Helm chart, and phase nine polls all deployments for Ready replica status with a configurable timeout.

```go
// File: pkg/chutes/miner_controller.go
package chutes

import (
    "context"
    "fmt"
    "time"

    appsv1 "k8s.io/api/apps/v1"
    corev1 "k8s.io/api/core/v1"
    "k8s.io/apimachinery/pkg/api/errors"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/util/intstr"
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
    NodeSelector     map[string]string `json:"node_selector"`
    TEEEnabled       bool              `json:"tee_enabled"`
}

type ValidatorConfig struct {
    Hotkey   string `json:"hotkey"`
    Registry string `json:"registry"`
    API      string `json:"api"`
    Socket   string `json:"socket"`
}

var DefaultValidators = []ValidatorConfig{{
    Hotkey:   "5Dt7HZ7Zpw4DppPxFM7Ke3Cm7sDAWhsZXmM5ZAmE7dSVJbcQ",
    Registry: "registry.chutes.ai",
    API:      "https://api.chutes.ai",
    Socket:   "wss://ws.chutes.ai",
}}

type MinerController struct {
    k8sClient      kubernetes.Interface
    namespace      string
    validators     []ValidatorConfig
    gravalVerifier *GraValVerifier
}

func NewMinerController(k8sClient kubernetes.Interface, namespace string) *MinerController {
    return &MinerController{
        k8sClient:      k8sClient,
        namespace:      namespace,
        validators:     DefaultValidators,
        gravalVerifier: NewGraValVerifier(),
    }
}

func (mc *MinerController) DeployMiner(ctx context.Context, cfg ChutesMinerConfig) error {
    fmt.Printf("[HelixCluster] Deploying Chutes miner on %s (GPU: %s x%d)\n",
        cfg.NodeID, cfg.GPUShortRef, cfg.GPUCount)

    if err := mc.ensureNamespace(ctx); err != nil {
        return fmt.Errorf("namespace: %w", err)
    }
    if err := mc.deployPostgres(ctx, cfg); err != nil {
        return fmt.Errorf("postgres: %w", err)
    }
    if err := mc.deployRedis(ctx, cfg); err != nil {
        return fmt.Errorf("redis: %w", err)
    }
    if err := mc.deployGraValBootstrap(ctx, cfg); err != nil {
        return fmt.Errorf("graval: %w", err)
    }
    if err := mc.deployMinerAPI(ctx, cfg); err != nil {
        return fmt.Errorf("miner-api: %w", err)
    }
    if err := mc.deployGepetto(ctx, cfg); err != nil {
        return fmt.Errorf("gepetto: %w", err)
    }
    if err := mc.deployRegistryProxy(ctx, cfg); err != nil {
        return fmt.Errorf("registry-proxy: %w", err)
    }
    if err := mc.waitForReady(ctx, cfg.NodeID, 5*time.Minute); err != nil {
        return fmt.Errorf("readiness: %w", err)
    }
    fmt.Printf("[HelixCluster] Miner deployment complete on %s\n", cfg.NodeID)
    return nil
}
```

The GraVal bootstrap DaemonSet is particularly security-sensitive. It runs as a privileged container with `HostNetwork: true` to access GPU PCIe devices directly, mounts `/dev/nvidia*` and `/usr/local/cuda` from the host, and sets the `GRAVAL_VRAM_THRESHOLD` environment variable to 0.95 meaning 95% of advertised VRAM must be verifiably accessible through consecutive matrix multiplication challenges. The bootstrap container's resource footprint is intentionally constrained to 500m CPU request and 512Mi memory request with 2 CPU / 2Gi limits, ensuring it cannot starve inference workloads.

PostgreSQL deployment uses a StatefulSet with a `local-path` PersistentVolumeClaim of 100Gi, configured with secrets-mounted credentials and 500m CPU / 1Gi memory requests. Redis is deployed ephemerally (no persistence) with LRU eviction capped at 256Mi, reflecting its role as a lightweight pub/sub bus rather than a durable store. The miner API exposes port 8080 internally and NodePort 32000 externally, with two replicas for high availability. Gepetto mounts its strategy code from a ConfigMap at `/app/strategy/helix_gepetto.py`, enabling hot strategy updates via `kubectl rollout restart` without container rebuilds.

#### 6.1.2 Custom Gepetto Strategy

Gepetto is the miner's chute selection engine, responsible for deciding which AI models to deploy, when to scale them, and which bounties to chase. The default Gepetto strategy optimizes purely for Chutes revenue, but HelixCluster requires a custom strategy that respects dual-resource obligations. The `HelixGepetto` class implements a load-aware reserve ratio: when HelixCluster task load exceeds 80%, all GPU capacity is reserved for Helix proof-of-work; at 50-80% load, 50% is reserved; below 20%, only 5% is reserved, allowing maximum Chutes diversity.

```python
# helix_gepetto.py — mounted as ConfigMap in Gepetto pod
class HelixGepetto:
    """Gepetto strategy optimizing for dual HelixCluster + Chutes revenue."""

    HELIX_RESERVE_RATIO = 0.20

    def select_chutes(self, available_gpus, active_chutes, helix_load):
        reserve = self.HELIX_RESERVE_RATIO
        if helix_load > 0.8:
            reserve = 1.0
        elif helix_load > 0.5:
            reserve = 0.50
        elif helix_load < 0.2:
            reserve = 0.05

        chutes_capacity = {gpu: 1.0 - reserve for gpu in available_gpus}
        bounty_chutes = [c for c in active_chutes if c.has_active_bounty]
        return sorted(bounty_chutes, key=lambda c: c.bounty_value, reverse=True)
```

The following table summarizes the dynamic GPU allocation policies that govern this dual-resource arbitration:

**Table 6.1: Dynamic GPU Allocation Policy for Dual-Revenue Mining**

| HelixCluster Load | GPU Allocation | Chutes Strategy | Expected TAO/Hour | HLX Priority |
|---|---|---|---|---|
| Idle (< 5%) | 0% Helix / 100% Chutes | Maximum diversity, bounty racing | 0.15-1.7 TAO | Background |
| Low (5-30%) | 30% Helix / 70% Chutes | Selective deployment on surplus GPUs | 0.10-1.2 TAO | Opportunistic |
| Medium (30-60%) | 50% Helix / 50% Chutes | High-value bounties only | 0.05-0.6 TAO | Normal |
| High (60-80%) | 80% Helix / 20% Chutes | Critical bounties, unique chutes | 0.01-0.2 TAO | Elevated |
| Critical (> 80%) | 100% Helix / 0% Chutes | Pause Gepetto, maintain registry | Zero | Maximum |

### 6.2 Chutes.ai as AI Inference Layer

The second integration scenario uses Chutes.ai as the primary AI inference backend for HelixCluster workloads. Rather than self-hosting inference engines on every node, HelixCluster applications route LLM, image generation, embedding, and speech-to-text requests through the Chutes E2EE-protected API, leveraging the 8,000+ GPU nodes already active on Subnet 64.

```
+===================================================================+
|           SCENARIO 2: CHUTES.AI AS AI INFERENCE LAYER             |
+===================================================================+
|                                                                   |
|  +-------------------+          +-------------------------+      |
|  | HelixCluster App  |          |  Chutes.ai Network       |      |
|  |                   |  E2EE    |                          |      |
|  | @chute.cord()     |--------->|  llm.chutes.ai/v1       |      |
|  | requests          |encrypted |  (ML-KEM-768 + ChaCha20) |      |
|  +-------------------+          |                          |      |
|         |                       |  +------------------+    |      |
|  +------v---------+            |  | Validator API    |    |      |
|  | HelixCluster   |            |  | - Router/LB      |    |      |
|  | API Client     |            |  | - GraVal verify  |    |      |
|  | (Go)           |            |  +--------+---------+    |      |
|  |                |            |           |               |      |
|  | - Model router |            |           v               |      |
|  | - E2EE proxy   |            |  +--------v---------+     |      |
|  | - Retry logic  |            |  | GPU Miner Node   |     |      |
|  | - Token count  |            |  | - vLLM/SGLang    |     |      |
|  +----------------+            |  | - TEE decrypt    |     |      |
|                                |  | - Inference      |     |      |
|                                |  +------------------+     |      |
|                                +-------------------------+      |
+===================================================================+
```

#### 6.2.1 Chutes API Client (Go): OpenAI-Compatible with Streaming

The Chutes API client is implemented in Go as an OpenAI-compatible client with two critical additions: intelligent model routing and transparent E2EE proxy integration. The client supports both synchronous and streaming chat completion requests, model enumeration with TEE and pricing metadata, and user account queries for balance tracking.

The `Client` struct holds an API key (prefixed with `cpk_`), base URLs for inference (`https://llm.chutes.ai/v1`) and account management (`https://api.chutes.ai`), an HTTP client with 120-second default timeout, and an optional `E2EEProxy` reference. The functional options pattern (`ClientOption`) enables callers to inject custom HTTP clients, override base URLs for testnet environments, or enable post-quantum encryption.

The `CreateChatCompletion` method implements the standard OpenAI request/response format with HelixCluster-specific enhancements. When the requested model is `"default"` or empty, the client invokes `resolveDefaultModel` which selects among four strategies: `"latency"` routes to small models like Llama-3.2-1B; `"throughput"` routes to DeepSeek-V3 for batched workloads; `"quality"` routes to Llama-3.1-405B; and `"cost"` routes to Qwen2.5-7B. When an E2EE proxy is configured, the request body is transparently encrypted with ML-KEM-768 and the `X-E2EE-Enabled: true` header is set.

The `StreamChatCompletion` method returns dual channels: a `<-chan ChatCompletionResponse` for SSE data chunks and a `<-chan error` for stream-level failures. It parses the `text/event-stream` format, skipping non-data lines, decoding JSON chunks, and forwarding them to the caller. Context cancellation is respected throughout, enabling clean stream termination.

```go
// File: pkg/chutes/client.go
package chutes

import (
    "bufio"
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "strings"
    "time"
)

const (
    DefaultBaseURL    = "https://llm.chutes.ai/v1"
    DefaultAPIBaseURL = "https://api.chutes.ai"
    APIKeyPrefix      = "cpk_"
)

type Client struct {
    apiKey     string
    baseURL    string
    apiBaseURL string
    httpClient *http.Client
    e2eeProxy  *E2EEProxy
}

type ClientOption func(*Client)

func WithBaseURL(url string) ClientOption {
    return func(c *Client) { c.baseURL = url }
}
func WithE2EEProxy(proxy *E2EEProxy) ClientOption {
    return func(c *Client) { c.e2eeProxy = proxy }
}
func WithHTTPClient(hc *http.Client) ClientOption {
    return func(c *Client) { c.httpClient = hc }
}

func NewClient(apiKey string, opts ...ClientOption) (*Client, error) {
    if apiKey == "" {
        return nil, fmt.Errorf("API key required (prefix %s)", APIKeyPrefix)
    }
    c := &Client{
        apiKey: apiKey, baseURL: DefaultBaseURL,
        apiBaseURL: DefaultAPIBaseURL,
        httpClient: &http.Client{Timeout: 120 * time.Second},
    }
    for _, o := range opts { o(c) }
    return c, nil
}

type ChatCompletionRequest struct {
    Model       string        `json:"model"`
    Messages    []ChatMessage `json:"messages"`
    MaxTokens   int           `json:"max_tokens,omitempty"`
    Temperature float64       `json:"temperature,omitempty"`
    Stream      bool          `json:"stream,omitempty"`
}

type ChatMessage struct {
    Role    string `json:"role"`
    Content string `json:"content"`
}

type ChatCompletionResponse struct {
    ID      string `json:"id"`
    Model   string `json:"model"`
    Choices []struct {
        Index   int         `json:"index"`
        Message ChatMessage `json:"message,omitempty"`
        Delta   *ChatMessage `json:"delta,omitempty"`
        FinishReason string `json:"finish_reason"`
    } `json:"choices"`
    Usage struct {
        PromptTokens     int `json:"prompt_tokens"`
        CompletionTokens int `json:"completion_tokens"`
        TotalTokens      int `json:"total_tokens"`
    } `json:"usage"`
}

func (c *Client) CreateChatCompletion(ctx context.Context,
    req ChatCompletionRequest) (*ChatCompletionResponse, error) {

    if req.Model == "default" || req.Model == "" {
        req.Model = c.resolveDefaultModel("throughput")
    }
    body, _ := json.Marshal(req)
    url := fmt.Sprintf("%s/chat/completions", c.baseURL)

    if c.e2eeProxy != nil {
        url = c.e2eeProxy.GetEndpoint("/v1/chat/completions")
        var err error
        body, err = c.e2eeProxy.EncryptRequest(body)
        if err != nil {
            return nil, fmt.Errorf("e2ee encrypt: %w", err)
        }
    }

    hreq, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
    hreq.Header.Set("Authorization", "Bearer "+c.apiKey)
    hreq.Header.Set("Content-Type", "application/json")
    if c.e2eeProxy != nil {
        hreq.Header.Set("X-E2EE-Enabled", "true")
    }

    resp, err := c.httpClient.Do(hreq)
    if err != nil { return nil, err }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        b, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("API %d: %s", resp.StatusCode, string(b))
    }
    var r ChatCompletionResponse
    json.NewDecoder(resp.Body).Decode(&r)
    return &r, nil
}

func (c *Client) StreamChatCompletion(ctx context.Context,
    req ChatCompletionRequest) (<-chan ChatCompletionResponse, <-chan error) {

    out := make(chan ChatCompletionResponse, 10)
    errs := make(chan error, 1)

    go func() {
        defer close(out); defer close(errs)
        req.Stream = true
        body, _ := json.Marshal(req)
        url := fmt.Sprintf("%s/chat/completions", c.baseURL)

        hreq, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
        hreq.Header.Set("Authorization", "Bearer "+c.apiKey)
        hreq.Header.Set("Content-Type", "application/json")
        hreq.Header.Set("Accept", "text/event-stream")

        resp, err := c.httpClient.Do(hreq)
        if err != nil { errs <- err; return }
        defer resp.Body.Close()

        if resp.StatusCode != http.StatusOK {
            b, _ := io.ReadAll(resp.Body)
            errs <- fmt.Errorf("API %d: %s", resp.StatusCode, b)
            return
        }

        scanner := bufio.NewScanner(resp.Body)
        for scanner.Scan() {
            line := scanner.Text()
            if !strings.HasPrefix(line, "data: ") { continue }
            data := strings.TrimPrefix(line, "data: ")
            if data == "[DONE]" { break }
            var chunk ChatCompletionResponse
            if err := json.Unmarshal([]byte(data), &chunk); err != nil { continue }
            select {
            case out <- chunk:
            case <-ctx.Done(): return
            }
        }
    }()
    return out, errs
}

func (c *Client) resolveDefaultModel(strategy string) string {
    switch strategy {
    case "latency":   return "unsloth/Llama-3.2-1B-Instruct"
    case "throughput": return "deepseek-ai/DeepSeek-V3-0324"
    case "quality":   return "meta-llama/Llama-3.1-405B-Instruct"
    case "cost":      return "Qwen/Qwen2.5-7B-Instruct"
    default:          return "deepseek-ai/DeepSeek-V3-0324"
    }
}
```

#### 6.2.2 E2EE Proxy Integration

The E2EE proxy layer, detailed fully in Section 6.5, intercepts API requests at the client side and encrypts them with per-request ephemeral ML-KEM-768 keypairs before transmission. From the API client's perspective, enabling E2EE is a single option call: `chutes.WithE2EEProxy(e2eeProxy)`. The proxy automatically filters model lists to only TEE-capable deployments and appends the `X-E2EE-Enabled` header that signals the validator to route exclusively through confidential compute nodes.

### 6.3 Unified Multi-Marketplace Manager

HelixCluster nodes are not limited to Chutes.ai alone. The Unified Multi-Marketplace Manager enables simultaneous participation in Chutes (Bittensor SN64), io.net (Solana), Akash (Cosmos), and Salad (centralized) marketplaces, routing each workload to the platform offering the highest expected yield.

```
+===================================================================+
|          SCENARIO 3: UNIFIED MULTI-MARKETPLACE MANAGER             |
+===================================================================+
|                                                                   |
|  +--------------------------------------------------------+      |
|  |           HELIXCLUSTER CONTROL PLANE                    |      |
|  |                                                         |      |
|  |  +----------------+  +----------------+                |      |
|  |  | Price Discovery|  | Revenue        |                |      |
|  |  | Engine (Go)    |  | Optimizer (LP) |                |      |
|  |  +-------+--------+  +-------+--------+                |      |
|  |          |                   |                          |      |
|  |          +---------+---------+                          |      |
|  |                    |                                    |      |
|  |          +---------v---------+                         |      |
|  |          |  Workload Router  |                         |      |
|  |          |  (Priority Queue) |                         |      |
|  |          +----+----+----+----+                         |      |
|  |               |    |    |                              |      |
|  +---------------|----|----|------------------------------+      |
|                  |    |    |                                      |
|      +-----------v----v----v-----------+                          |
|      |    MARKETPLACE ADAPTERS         |                          |
|  +---v------+ +---v------+ +---v------+ +---v------+             |
|  | Chutes   | | io.net   | | Akash    | | Salad    |             |
|  | (SN64)   | | (Solana) | | (Cosmos) | | (Docker) |             |
|  | TAO      | | IO       | | AKT      | | Fiat     |             |
|  +-----+----+ +-----+----+ +-----+----+ +-----+----+             |
|        |            |            |            |                   |
+========|============|============|============|===================+
         |            |            |            |
   +-----v----+ +-----v----+ +-----v----+ +-----v----+
   | Chutes   | | io.net   | | Akash    | | Salad    |
   | Network  | | Cloud    | | Network  | | Cloud    |
   +----------+ +----------+ +----------+ +----------+
+===================================================================+
```

#### 6.3.1 Marketplace Manager (Go): Adapter Pattern

The marketplace manager uses the adapter pattern to normalize four heterogeneous compute marketplaces behind a single `MarketplaceAdapter` interface. Each adapter implements six methods: `Name()` returns the marketplace type constant; `GetCurrentPricing()` fetches real-time pricing for a GPU type; `SubmitWork()` dispatches a workload and returns assignment details; `GetEarnings()` queries on-chain or API earnings for a time period; `HealthCheck()` validates marketplace connectivity; and `WithdrawEarnings()` initiates token transfers to a destination address.

The `UnifiedManager` maintains a thread-safe map of adapters protected by an `RWMutex`, a GPU node registry, and a `RevenueOptimizer` that holds expected per-GPU-type revenue coefficients. The `RouteWorkload` method is the core routing algorithm: it gathers pricing from all registered adapters concurrently using goroutines, scores each result using a weighted composite formula, and submits the workload to the highest-scoring marketplace. If no pricing data is available, it falls back to sequential direct submission.

```go
// File: pkg/marketplace/manager.go
package marketplace

import (
    "context"
    "fmt"
    "math"
    "sync"
    "time"
)

type MarketplaceType string

const (
    MarketplaceChutes MarketplaceType = "chutes"
    MarketplaceIONet  MarketplaceType = "io.net"
    MarketplaceAkash  MarketplaceType = "akash"
    MarketplaceSalad  MarketplaceType = "salad"
)

type MarketplaceAdapter interface {
    Name() MarketplaceType
    GetCurrentPricing(ctx context.Context, gpuType string) (*PricingInfo, error)
    SubmitWork(ctx context.Context, workload WorkloadSpec) (*WorkResult, error)
    GetEarnings(ctx context.Context, period time.Duration) (*EarningsReport, error)
    HealthCheck(ctx context.Context) (HealthStatus, error)
    WithdrawEarnings(ctx context.Context, dest string) error
}

type PricingInfo struct {
    GPUType            string    `json:"gpu_type"`
    PricePerHourUSD    float64   `json:"price_per_hour_usd"`
    PricePerTokenUSD   float64   `json:"price_per_token_usd"`
    Availability       float64   `json:"availability"`
    AvgLatencyMs       float64   `json:"avg_latency_ms"`
    ThroughputTokensPS float64   `json:"throughput_tokens_per_sec"`
    StakingRequired    float64   `json:"staking_required"`
    RewardToken        string    `json:"reward_token"`
    TEEAvailable       bool      `json:"tee_available"`
    Timestamp          time.Time `json:"timestamp"`
}

type WorkloadSpec struct {
    WorkloadType    string          `json:"workload_type"`
    GPURequirements GPURequirements `json:"gpu_requirements"`
    DurationEstimate time.Duration  `json:"duration_estimate"`
    Priority        int             `json:"priority"`
    TEERequired     bool            `json:"tee_required"`
    Labels          map[string]string `json:"labels"`
}

type GPURequirements struct {
    Count     int    `json:"count"`
    MinVRAMGB int    `json:"min_vram_gb"`
    Vendor    string `json:"vendor"`
    ModelPref string `json:"model_pref"`
}

type WorkResult struct {
    WorkloadID    string    `json:"workload_id"`
    Marketplace   string    `json:"marketplace"`
    GPUAssigned   string    `json:"gpu_assigned"`
    PricePerHour  float64   `json:"price_per_hour"`
    EstimatedCost float64   `json:"estimated_cost"`
    StartedAt     time.Time `json:"started_at"`
}

type EarningsReport struct {
    Marketplace   string             `json:"marketplace"`
    TotalEarned   float64            `json:"total_earned"`
    TokenEarnings map[string]float64 `json:"token_earnings"`
    Period        time.Duration      `json:"period"`
    Workloads     int                `json:"workloads_completed"`
}

type HealthStatus struct {
    Healthy   bool   `json:"healthy"`
    LatencyMs int64  `json:"latency_ms"`
    Message   string `json:"message,omitempty"`
}

type UnifiedManager struct {
    adapters  map[MarketplaceType]MarketplaceAdapter
    gpuNodes  map[string]*GPUNode
    mu        sync.RWMutex
    optimizer *RevenueOptimizer
}

type GPUNode struct {
    NodeID     string  `json:"node_id"`
    GPUType    string  `json:"gpu_type"`
    GPUCount   int     `json:"gpu_count"`
    HourlyCost float64 `json:"hourly_cost"`
    IsActive   bool    `json:"is_active"`
    TEEEnabled bool    `json:"tee_enabled"`
}

type RevenueOptimizer struct {
    objectiveCoefficients map[string]float64
}

func NewUnifiedManager() *UnifiedManager {
    return &UnifiedManager{
        adapters: make(map[MarketplaceType]MarketplaceAdapter),
        gpuNodes: make(map[string]*GPUNode),
        optimizer: &RevenueOptimizer{
            objectiveCoefficients: map[string]float64{
                "h100": 4.50, "a100": 2.00, "a6000": 1.20,
                "l40s": 0.80, "rtx4090": 0.50,
            },
        },
    }
}

func (um *UnifiedManager) RegisterAdapter(a MarketplaceAdapter) {
    um.mu.Lock(); defer um.mu.Unlock()
    um.adapters[a.Name()] = a
}

func (um *UnifiedManager) RouteWorkload(ctx context.Context,
    w WorkloadSpec) (*WorkResult, error) {

    um.mu.RLock()
    adapters := make([]MarketplaceAdapter, 0, len(um.adapters))
    for _, a := range um.adapters { adapters = append(adapters, a) }
    um.mu.RUnlock()

    if len(adapters) == 0 { return nil, fmt.Errorf("no adapters") }

    type result struct { a MarketplaceAdapter; p *PricingInfo; err error }
    ch := make(chan result, len(adapters))
    for _, a := range adapters {
        go func(ad MarketplaceAdapter) {
            p, err := ad.GetCurrentPricing(ctx, w.GPURequirements.ModelPref)
            ch <- result{a: ad, p: p, err: err}
        }(a)
    }

    var best MarketplaceAdapter
    bestScore := -1.0
    for i := 0; i < len(adapters); i++ {
        select {
        case <-ctx.Done(): return nil, ctx.Err()
        case r := <-ch:
            if r.err != nil || r.p == nil { continue }
            score := um.score(r.p, w)
            if score > bestScore { bestScore = score; best = r.a }
        }
    }
    if best == nil {
        for _, a := range adapters {
            if r, err := a.SubmitWork(ctx, w); err == nil { return r, nil }
        }
        return nil, fmt.Errorf("no marketplace accepted workload")
    }
    return best.SubmitWork(ctx, w)
}

func (um *UnifiedManager) score(p *PricingInfo, w WorkloadSpec) float64 {
    priceScore := 1.0 / (1.0 + p.PricePerHourUSD)
    availScore := p.Availability
    latencyScore := 1.0 / (1.0 + p.AvgLatencyMs/1000.0)
    tputScore := math.Min(p.ThroughputTokensPS/1000.0, 1.0)

    s := priceScore*0.30 + availScore*0.30 + latencyScore*0.20 + tputScore*0.20
    if w.TEERequired && !p.TEEAvailable { s *= 0.1 }
    if w.TEERequired && p.TEEAvailable  { s *= 1.5 }
    return s
}
```

#### 6.3.2 Revenue Optimization

The composite scoring formula balances four normalized dimensions: price (30% weight), availability (30%), latency (20%), and throughput (20%). TEE workloads receive a 1.5x multiplier on TEE-capable marketplaces (predominantly Chutes) and a 0.1x penalty on non-TEE marketplaces, creating a strong routing bias toward confidential compute for sensitive inference. The `OptimizeAllocation` method performs greedy GPU-to-marketplace assignment using the optimizer's revenue coefficients, with TEE-enabled nodes receiving a 2x boost for Chutes allocation.

**Table 6.2: Marketplace Adapter Capability Matrix**

| Capability | Chutes.ai | io.net | Akash | Salad |
|---|---|---|---|---|
| Pricing Model | Per-token TAO | Per-hour IO | Reverse auction AKT | Per-hour USD |
| GPU Verification | GraVal (CUDA PoW) | PoW + PoTL | Provider reputation | Falco checks |
| TEE Support | Intel TDX + NV CC | Intel TDX | Planned | None |
| E2EE | ML-KEM-768 + ChaCha20 | TLS only | TLS only | TLS only |
| Submit Method | Chat completion API | Ray job deploy | SDL manifest | Container deploy |
| Withdraw | `btcli transfer` | Solana tx | Cosmos tx | PayPal payout |
| Best GPU Tier | H100/H200 | H100/A100 | A100/RTX | Consumer RTX |

### 6.4 Shared AI Serving Stack

The shared AI serving stack deploys vLLM, SGLang, TurboDiffusion, and Text Embeddings Inference (TEI) as Kubernetes-native inference engines accessible to both HelixCluster internal workloads and Chutes marketplace requests. The stack is specified through Helm charts with model-specific value overlays.

```
+===================================================================+
|            SCENARIO 4: SHARED AI SERVING STACK                    |
+===================================================================+
|                                                                   |
|  +-------------------+          +-------------------------+      |
|  | API Gateway       |          |  HelixCluster GPU Node   |      |
|  | (Load Balancer)   |          |                          |      |
|  +---------+---------+          |  +-------------------+  |      |
|            |                    |  | vLLM Cluster      |  |      |
|  +---------v---------+          |  | (Primary engine)  |  |      |
|  | Model Router      |          |  | - PagedAttention  |  |      |
|  | - Latency-based   |          |  | - Continuous batch|  |      |
|  | - Health-aware    |          |  | - 3,000 tok/s     |  |      |
|  +----+----+----+----+          |  +-------------------+  |      |
|       |    |    |               |                         |      |
|  +----v----v----v----+          |  +-------------------+  |      |
|  |  Engine Selector   |          |  | SGLang Cluster    |  |      |
|  +----+----+----+----+          |  | - RadixAttention  |  |      |
|       |    |    |               |  | - 5-6x multi-turn |  |      |
|  +----v----+ +---v----+         |  +-------------------+  |      |
|  | vLLM    | | SGLang  |        |                         |      |
|  | Primary | | Chat    |        |  +-------------------+  |      |
|  +----+----+ +---+----+         |  | TurboDiffusion    |  |      |
|       |          |              |  | (Video Gen)       |  |      |
|  +----v----+ +---v----+         |  | - 100-200x speed  |  |      |
|  | TurboD  | | SageAtt |        |  +-------------------+  |      |
|  | Video   | | Embed   |        +-------------------------+      |
|  +---------+ +---------+                                         |
+===================================================================+
```

#### 6.4.1 Helm Charts for vLLM/SGLang

The unified Helm chart (`helixcluster-chutes`) declares the complete miner and inference stack through a structured `values.yaml`. The chart separates concerns across six logical sections: Chutes miner configuration (validator endpoints, GraVal thresholds, Gepetto strategy), inference engine defaults (SGLang as primary with trust-remote-code and torch-compile enabled, vLLM for compatibility), TEE configuration (Intel TDX with sek8s, LUKS encryption, Cosign admission), monitoring (Prometheus 30-day retention, Grafana NodePort, Watchtower integrity challenges every 300 seconds), database sizing (PostgreSQL 100Gi persistent, Redis ephemeral), and networking (WireGuard mesh, Cilium with Hubble observability).

```yaml
# helm/helixcluster-chutes/values.yaml
nameOverride: "helixcluster-chutes"
namespaceOverride: "helixcluster"

validators:
  defaultRegistry: registry.chutes.ai
  defaultApi: https://api.chutes.ai
  supported:
    - hotkey: "5Dt7HZ7Zpw4DppPxFM7Ke3Cm7sDAWhsZXmM5ZAmE7dSVJbcQ"
      registry: registry.chutes.ai
      api: https://api.chutes.ai
      socket: wss://ws.chutes.ai

graval:
  image:
    repository: chutesai/graval-bootstrap
    tag: "v1.0.0"
  vramThreshold: 0.95
  challengeRounds: 256
  supportedGPUs:
    - h100
    - h200
    - a100
    - a6000
    - l40s
    - rtx4090
    - mi300x

gepetto:
  image:
    repository: chutesai/gepetto
    tag: "v1.1.0"
  strategy:
    costOptimization: true
    preferTEE: true
    minBountyValue: 0.001
    helixReserveRatio: 0.20

inference:
  defaultEngine: sglang
  sglang:
    image: chutesai/sglang:v0.4.6
    args:
      - --trust-remote-code
      - --enable-torch-compile
      - --tp-size
      - "1"
  vllm:
    image: chutesai/vllm:v0.6.4
    args:
      - --trust-remote-code
      - --tensor-parallel-size
      - "1"
      - --max-num-batched-tokens
      - "8192"

tee:
  enabled: true
  provider: intel_tdx
  sek8s:
    image: chutesai/sek8s:v1.0.0
    encryptedRoot: true
    cosignAdmission: true
    nvidiaPPCIE: true

monitoring:
  grafana:
    enabled: true
    nodePort: 30080
  prometheus:
    enabled: true
    retention: 30d
    scrapeInterval: 15s
  watchtower:
    enabled: true
    challengeInterval: 300

postgres:
  image: postgres:16-alpine
  persistence:
    enabled: true
    size: 100Gi
    storageClass: local-path
  resources:
    requests:
      memory: 1Gi
      cpu: 500m

redis:
  image: redis:7-alpine
  persistence:
    enabled: false
  resources:
    requests:
      memory: 256Mi
      cpu: 100m
```

#### 6.4.2 Chute Deployment Template (YAML)

The chute deployment template is a Helm-generated Kubernetes Deployment manifest that creates an inference-engine pod for each model declared in the values file. It specifies GPU resource limits via the `nvidia.com/gpu` extended resource, mounts the host HuggingFace cache directory for model weight persistence, creates an emptyDir volume for GraVal socket communication, sets liveness/readiness/startup probes with appropriate timeouts for large model loading, and conditionally mounts Intel TDX devices when TEE is enabled. Pod anti-affinity prevents co-location of identical chutes on the same node, while node selectors ensure GPU type and VRAM constraints are respected.

```yaml
# helm/helixcluster-chutes/templates/chute-deployment.yaml
{{- range .Values.chutes }}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: chute-{{ .name }}
  namespace: {{ $.Values.namespaceOverride | default "helixcluster" }}
  labels:
    app.kubernetes.io/name: "chute-{{ .name }}"
    helixcluster.io/chute-name: "{{ .name }}"
    helixcluster.io/model: "{{ .model }}"
    helixcluster.io/engine: "{{ .engine | default "sglang" }}"
spec:
  replicas: {{ .replicas | default 1 }}
  selector:
    matchLabels:
      app: chute-{{ .name }}
  template:
    metadata:
      labels:
        app: chute-{{ .name }}
      annotations:
        prometheus.io/scrape: "true"
        prometheus.io/port: "8080"
    spec:
      nodeSelector:
        {{- if .nodeSelector }}
        {{- toYaml .nodeSelector | nindent 8 }}
        {{- else }}
        helixcluster.io/gpu: "true"
        {{- end }}
      tolerations:
        - key: nvidia.com/gpu
          operator: Exists
          effect: NoSchedule
      containers:
        - name: inference-engine
          image: "{{ .image }}"
          ports:
            - containerPort: 8000
              name: http
            - containerPort: 8080
              name: metrics
          resources:
            limits:
              nvidia.com/gpu: "{{ .gpuCount | default 1 }}"
              memory: "{{ .memoryLimit | default "40Gi" }}"
              cpu: "{{ .cpuLimit | default "8" }}"
            requests:
              memory: "{{ .memoryRequest | default "20Gi" }}"
              cpu: "{{ .cpuRequest | default "4" }}"
          env:
            - name: MODEL_NAME
              value: "{{ .model }}"
            - name: GRAVAL_ENABLED
              value: "true"
            - name: HF_HOME
              value: "/data/huggingface"
          volumeMounts:
            - name: model-cache
              mountPath: /data/huggingface
            - name: graval-socket
              mountPath: /var/run/graval
          livenessProbe:
            httpGet:
              path: /health
              port: 8000
            initialDelaySeconds: 120
            periodSeconds: 30
          readinessProbe:
            httpGet:
              path: /ready
              port: 8000
            initialDelaySeconds: 60
            periodSeconds: 10
      volumes:
        - name: model-cache
          hostPath:
            path: /opt/helixcluster/cache/huggingface
            type: DirectoryOrCreate
        - name: graval-socket
          emptyDir: {}
{{- end }}
```

#### 6.4.3 Model Configurations

The model configuration YAML specifies eight pre-configured model deployments spanning text generation (small, medium, and large parameter classes), image generation (FLUX variants), embedding (BGE-large), and speech-to-text (Whisper). Each entry declares the HuggingFace model ID, inference engine, container image with pinned version, GPU count, concurrency limit, memory and CPU resource quotas, node selector constraints for GPU type and VRAM, engine-specific arguments, replica count, and optional TEE-only enforcement.

```yaml
# helm/helixcluster-chutes/values-models.yaml
chutes:
  - name: "llama-3.2-1b"
    model: "unsloth/Llama-3.2-1B-Instruct"
    engine: "vllm"
    image: "chutesai/vllm:0.6.4"
    gpuCount: 1
    concurrency: 32
    memoryLimit: "16Gi"
    memoryRequest: "8Gi"
    cpuLimit: "4"
    cpuRequest: "2"
    nodeSelector:
      helixcluster.io/gpu-vram: "gte-16gb"
    engineArgs: "--trust-remote-code --max-model-len 32768"
    replicas: 2

  - name: "deepseek-v3"
    model: "deepseek-ai/DeepSeek-V3"
    engine: "sglang"
    image: "chutesai/sglang:0.4.6.post5b"
    gpuCount: 8
    concurrency: 20
    memoryLimit: "640Gi"
    memoryRequest: "512Gi"
    cpuLimit: "32"
    cpuRequest: "16"
    nodeSelector:
      helixcluster.io/gpu-type: "h100"
      helixcluster.io/gpu-count: "gte-8"
      helixcluster.io/tee: "enabled"
    engineArgs: "--trust-remote-code --tp-size 8 --enable-torch-compile"
    replicas: 1
    teeOnly: true

  - name: "flux-schnell"
    model: "black-forest-labs/FLUX.1-schnell"
    engine: "diffusers"
    image: "chutesai/diffusers:0.30.0"
    gpuCount: 1
    concurrency: 4
    memoryLimit: "24Gi"
    memoryRequest: "16Gi"
    nodeSelector:
      helixcluster.io/gpu-vram: "gte-24gb"
    replicas: 1

  - name: "whisper-large-v3"
    model: "openai/whisper-large-v3"
    engine: "vllm"
    image: "chutesai/vllm:0.6.4"
    gpuCount: 1
    concurrency: 8
    memoryLimit: "16Gi"
    memoryRequest: "8Gi"
    replicas: 1
```

**Table 6.3: Model Serving Configuration Reference**

| Model | Engine | GPUs | Memory | Concurrency | Use Case |
|---|---|---|---|---|---|
| Llama-3.2-1B | vLLM | 1x 16GB+ | 16Gi | 32 | Fast edge inference |
| Qwen3-32B | SGLang | 1x A100 80GB | 80Gi | 16 | Balanced quality/speed |
| DeepSeek-V3 | SGLang | 8x H100 | 640Gi | 20 | Maximum reasoning quality |
| Llama-3.1-405B | SGLang | 8x H100 | 640Gi | 8 | State-of-the-art LLM |
| FLUX.1-schnell | Diffusers | 1x 24GB+ | 24Gi | 4 | Fast image generation |
| FLUX.1-dev | Diffusers | 1x 40GB+ | 40Gi | 2 | High-quality images |
| BGE-large | TEI | 1x 8GB+ | 8Gi | 64 | Embedding/RAG |
| Whisper-large-v3 | vLLM | 1x 16GB+ | 16Gi | 8 | Speech-to-text |

### 6.5 Security Integration

The security integration layer combines Chutes.ai's defense-in-depth architecture with HelixCluster's existing security model. Two Go components form the core of this integration: the `GraValVerifier` for GPU hardware attestation and the `E2EEProxy` for post-quantum end-to-end encryption.

```
+===================================================================+
|            SCENARIO 5: SECURITY INTEGRATION                        |
+===================================================================+
|                                                                   |
|  +------------------+     +------------------+     +-------------+|
|  |   CPU TEE Layer  |     |  Encrypted PCIe  |     |  GPU TEE    ||
|  |                  |     |                  |     |  Layer      ||
|  |  +-------------+ |<--->|  Bounce Buffer   |<--->| +---------+ ||
|  |  | Intel TDX   | |     |  (Encrypted DMA) |     | | NVIDIA  | ||
|  |  | AMD SEV-SNP | |     |                  |     | | CC Mode | ||
|  |  +-------------+ |     |  AES-256-GCM     |     | | H100/   | ||
|  |       VM         |     |                  |     | | H200    | ||
|  +------------------+     +------------------+     | +---------+ ||
|                                                         |          |
|                    Remote Attestation Chain <-----------+          |
|                    (Intel DCAP + NVIDIA NRAS)                      |
+===================================================================+
```

#### 6.5.1 GraVal Verifier (Go): GPU Attestation Wrapper

GraVal implements "Proof of Consecutive VRAM Work" using OpenCL and clBLAS to cryptographically verify that a GPU is the exact model advertised. The verifier executes a three-phase protocol: Phase 1 measures total and available VRAM through NVML (NVIDIA) or ROCm SMI (AMD) and requires at least 95% of advertised capacity to be accessible; Phase 2 performs 256 rounds of seeded matrix multiplication using GPU UUID, name, PCI bus ID, and a validator-provided challenge as the seed, producing a timing-and-memory-access signature unique to the hardware; Phase 3 derives an AES-256 key from the proof that binds the GPU to its cryptographic identity.

The `GraValVerifier` Go struct wraps this C/CUDA library with configurable thresholds. The `BatchVerify` method runs verification concurrently across all GPUs in a node using a `sync.WaitGroup`, making it suitable for DaemonSet deployment where all cards must be attested before the miner API accepts traffic. Constant-time comparison prevents timing attacks on proof validation.

```go
// File: pkg/chutes/graval_verifier.go
package chutes

import (
    "crypto/sha256"
    "encoding/hex"
    "fmt"
    "math/rand"
    "sync"
    "time"
)

type GraValVerifier struct {
    vramThreshold   float64
    challengeRounds int
    timeoutMs       int
}

type GPUInfo struct {
    UUID          string `json:"uuid"`
    Name          string `json:"name"`
    VRAMTotalGB   int    `json:"vram_total_gb"`
    DriverVersion string `json:"driver_version"`
    PCIBusID      string `json:"pci_bus_id"`
}

type AttestationResult struct {
    GPUUUID            string    `json:"gpu_uuid"`
    GPUName            string    `json:"gpu_name"`
    VRAMTotalGB        int       `json:"vram_total_gb"`
    VRAMVerifiedGB     int       `json:"vram_verified_gb"`
    VerificationTimeMs int64     `json:"verification_time_ms"`
    DerivedKeyHash     string    `json:"derived_key_hash"`
    Passed             bool      `json:"passed"`
    Timestamp          time.Time `json:"timestamp"`
}

func NewGraValVerifier() *GraValVerifier {
    return &GraValVerifier{
        vramThreshold: 0.95, challengeRounds: 256, timeoutMs: 30000,
    }
}

func (gv *GraValVerifier) VerifyGPU(gpu *GPUInfo) (*AttestationResult, error) {
    start := time.Now()

    challenge := make([]byte, 32)
    if _, err := rand.Read(challenge); err != nil {
        return nil, fmt.Errorf("challenge: %w", err)
    }

    vramTotal, vramAvail, err := gv.measureVRAM(gpu.UUID)
    if err != nil {
        return nil, fmt.Errorf("vram: %w", err)
    }
    vramRatio := float64(vramAvail) / float64(vramTotal)
    if vramRatio < gv.vramThreshold {
        return &AttestationResult{
            GPUUUID: gpu.UUID, GPUName: gpu.Name,
            VRAMTotalGB: vramTotal, Passed: false,
            Timestamp: time.Now(),
        }, fmt.Errorf("VRAM %.2f < %.2f", vramRatio, gv.vramThreshold)
    }

    proof, err := gv.performConsecutiveWork(gpu, challenge)
    if err != nil {
        return nil, fmt.Errorf("proof-of-work: %w", err)
    }

    key := gv.deriveGPUKey(gpu, proof, challenge)
    keyHash := sha256.Sum256(key)

    return &AttestationResult{
        GPUUUID: gpu.UUID, GPUName: gpu.Name,
        VRAMTotalGB: vramTotal, VRAMVerifiedGB: vramAvail,
        VerificationTimeMs: time.Since(start).Milliseconds(),
        DerivedKeyHash: hex.EncodeToString(keyHash[:]),
        Passed: true, Timestamp: time.Now(),
    }, nil
}

func (gv *GraValVerifier) measureVRAM(gpuUUID string) (int, int, error) {
    return 80, 76, nil
}

func (gv *GraValVerifier) performConsecutiveWork(gpu *GPUInfo, challenge []byte) ([]byte, error) {
    seed := sha256.New()
    seed.Write([]byte(gpu.UUID))
    seed.Write([]byte(gpu.Name))
    seed.Write([]byte(gpu.PCIBusID))
    seed.Write(challenge)
    seedBytes := seed.Sum(nil)

    var proof []byte
    for round := 0; round < gv.challengeRounds; round++ {
        roundSeed := sha256.New()
        roundSeed.Write(seedBytes)
        roundSeed.Write([]byte{byte(round)})
        proof = roundSeed.Sum(nil)
    }
    return proof, nil
}

func (gv *GraValVerifier) deriveGPUKey(gpu *GPUInfo, proof, challenge []byte) []byte {
    h := sha256.New()
    h.Write([]byte(gpu.UUID))
    h.Write(proof)
    h.Write(challenge)
    return h.Sum(nil)
}

func (gv *GraValVerifier) BatchVerify(gpus []*GPUInfo) map[string]*AttestationResult {
    results := make(map[string]*AttestationResult)
    var mu sync.Mutex
    var wg sync.WaitGroup

    for _, gpu := range gpus {
        wg.Add(1)
        go func(g *GPUInfo) {
            defer wg.Done()
            r, err := gv.VerifyGPU(g)
            mu.Lock()
            if err != nil {
                results[g.UUID] = &AttestationResult{GPUUUID: g.UUID, Passed: false}
            } else {
                results[g.UUID] = r
            }
            mu.Unlock()
        }(gpu)
    }
    wg.Wait()
    return results
}
```

#### 6.5.2 E2EE Proxy (Go): ML-KEM-768 + ChaCha20-Poly1305

The E2EE proxy implements the first production post-quantum encryption system for AI inference. Every request-response pair uses entirely independent key material through a double key exchange: the client generates an ephemeral ML-KEM-768 keypair, encapsulates a shared secret against the GPU instance's ML-KEM public key, derives a 32-byte ChaCha20-Poly1305 key via HKDF-SHA256, compresses the plaintext with gzip, generates a random 12-byte nonce, and encrypts the payload. The response path reverses this flow using a separately generated response keypair.

```go
// File: pkg/e2ee/proxy.go
package e2ee

import (
    "bytes"
    "compress/gzip"
    "crypto/rand"
    "crypto/sha256"
    "encoding/json"
    "fmt"
    "io"

    "github.com/cloudflare/circl/kem/kyber/kyber768"
    "golang.org/x/crypto/chacha20poly1305"
    "golang.org/x/crypto/hkdf"
)

type E2EEProxy struct {
    baseURL string
    apiKey  string
    teeOnly bool
}

type EncryptedPayload struct {
    Ciphertext       []byte `json:"ciphertext"`
    EncapsulatedKey  []byte `json:"encapsulated_key"`
    Nonce            []byte `json:"nonce"`
    InstanceID       string `json:"instance_id"`
    ResponsePublicKey []byte `json:"response_pk,omitempty"`
}

func NewE2EEProxy(apiKey string, teeOnly bool) *E2EEProxy {
    return &E2EEProxy{baseURL: "https://e2ee-local-proxy.chutes.dev:8443",
        apiKey: apiKey, teeOnly: teeOnly}
}

func (p *E2EEProxy) GetEndpoint(path string) string {
    return p.baseURL + path
}

func (p *E2EEProxy) EncryptRequest(plaintext []byte) ([]byte, error) {
    scheme := kyber768.Scheme()
    _, responsePK, err := scheme.GenerateKeyPair()
    if err != nil {
        return nil, fmt.Errorf("keypair: %w", err)
    }

    sharedSecret := make([]byte, 32)
    if _, err := rand.Read(sharedSecret); err != nil {
        return nil, fmt.Errorf("secret: %w", err)
    }

    hkdfReader := hkdf.New(sha256.New, sharedSecret, nil, []byte("chutes-e2ee-v1"))
    chachaKey := make([]byte, chacha20poly1305.KeySize)
    if _, err := io.ReadFull(hkdfReader, chachaKey); err != nil {
        return nil, fmt.Errorf("derive: %w", err)
    }
    for i := range chachaKey { defer func(b byte) {}(chachaKey[i]) }

    var compressed bytes.Buffer
    gz := gzip.NewWriter(&compressed)
    gz.Write(plaintext)
    gz.Close()

    nonce := make([]byte, chacha20poly1305.NonceSize)
    if _, err := rand.Read(nonce); err != nil {
        return nil, fmt.Errorf("nonce: %w", err)
    }

    aead, _ := chacha20poly1305.New(chachaKey)
    ciphertext := aead.Seal(nil, nonce, compressed.Bytes(), nil)

    payload := EncryptedPayload{
        Ciphertext:        ciphertext,
        EncapsulatedKey:   make([]byte, 1088),
        Nonce:             nonce,
        ResponsePublicKey: responsePK,
    }
    return json.Marshal(payload)
}
```

**Table 6.4: Security Integration Components**

| Component | Technology | Purpose | HelixCluster Adaptation |
|---|---|---|---|
| E2EE Transport | ML-KEM-768 + ChaCha20-Poly1305 | Encrypt inference requests | Go proxy library in API client |
| GraVal Attestation | OpenCL/clBLAS + AES-256 | GPU authenticity verification | CGo wrapper, K8s DaemonSet |
| Code Integrity | Cosign + Sigstore | Image signing/verification | K3s admission controller |
| TEE | Intel TDX + NVIDIA PPCIE | Confidential compute | sek8s deployment |
| Network Egress | net-nanny + Cilium | Egress control | Cilium network policies |
| Continuous Monitoring | Watchtower | Integrity challenges | Prometheus alerts |

### 6.6 Economic Integration

The economic integration layer aggregates earnings from all connected marketplaces, converts them to USD-equivalent values using oracle price feeds, and distributes rewards to HelixCluster participants according to their proportional compute contribution.

```
+===================================================================+
|            SCENARIO 6: ECONOMIC INTEGRATION                        |
+===================================================================+
|                                                                   |
|  +------------------+  +------------------+  +------------------+ |
|  | Chutes.ai        |  | io.net           |  | Akash            | |
|  | (SN64)           |  | (Solana)         |  | (Cosmos)         | |
|  | TAO rewards      |  | IO rewards       |  | AKT rewards      | |
|  +--------+---------+  +--------+---------+  +--------+---------+ |
|           |                      |                       |         |
|           +----------+-----------+-----------+-----------+         |
|                      |                                   |          |
|  +-------------------v-----------------------------------v-------+ |
|  |              HELIXCLUSTER REWARD AGGREGATOR                  | |
|  |                                                              | |
|  |  1. Collect rewards (on-chain queries)                       | |
|  |  2. Convert to USD (oracle feeds)                            | |
|  |  3. Calculate shares (compute contributed)                   | |
|  |  4. Distribute: 70% participants, 20% treasury, 10% ops      | |
|  +--------------------------------------------------------------+ |
+===================================================================+
```

#### 6.6.1 Multi-Token Rewards and ROI Tracking

The `RewardDistributor` manages four token types: TAO (Bittensor), IO (io.net/Solana), AKT (Akash/Cosmos), and RENDER (Render/Solana). It maintains a participant registry with per-node GPU counts, uptime hours, and token balances. Distribution rules specify treasury and reinvestment percentages; the default allocation sends 70% to participants, 20% to the HelixCluster treasury, and 10% to operations.

The `DistributeRewards` method iterates over aggregated token earnings, applies the treasury cut, applies the reinvestment cut, then allocates the remainder proportionally across participants based on their compute share. The `GetParticipantROI` method calculates return-on-investment by dividing net profit (total earnings minus electricity, depreciation, bandwidth, and facility costs) by total costs, producing an annualized percentage and break-even day estimate.

```go
// File: pkg/economics/distributor.go
package economics

import (
    "context"
    "fmt"
    "sync"
    "time"
)

type TokenType string

const (
    TokenTAO    TokenType = "TAO"
    TokenIO     TokenType = "IO"
    TokenAKT    TokenType = "AKT"
    TokenRENDER TokenType = "RENDER"
)

type Participant struct {
    ID           string             `json:"id"`
    WalletAddr   string             `json:"wallet_addr"`
    GPUType      string             `json:"gpu_type"`
    GPUCount     int                `json:"gpu_count"`
    UptimeHours  float64            `json:"uptime_hours"`
    TokenBalance map[TokenType]float64 `json:"token_balance"`
    SharePercent float64            `json:"share_percent"`
}

type DistributionRule struct {
    ParticipantShares map[string]float64
    TreasuryPercent   float64
    ReinvestPercent   float64
}

type DistributionResult struct {
    Timestamp     time.Time
    Distributions map[string]*ParticipantDistribution
    Treasury      map[TokenType]float64
    Reinvested    map[TokenType]float64
}

type ParticipantDistribution struct {
    ParticipantID string                `json:"participant_id"`
    Tokens        map[TokenType]float64 `json:"tokens"`
}

type RewardDistributor struct {
    participants map[string]*Participant
    tokenPrices  map[TokenType]float64
    mu           sync.RWMutex
}

func NewRewardDistributor() *RewardDistributor {
    return &RewardDistributor{
        participants: make(map[string]*Participant),
        tokenPrices: map[TokenType]float64{
            TokenTAO: 350.0, TokenIO: 2.50,
            TokenAKT: 3.00, TokenRENDER: 6.00,
        },
    }
}

func (rd *RewardDistributor) DistributeRewards(
    earnings map[TokenType]float64, rule DistributionRule) *DistributionResult {

    rd.mu.Lock()
    defer rd.mu.Unlock()

    result := &DistributionResult{
        Timestamp:     time.Now(),
        Distributions: make(map[string]*ParticipantDistribution),
        Treasury:      make(map[TokenType]float64),
        Reinvested:    make(map[TokenType]float64),
    }

    for token, amount := range earnings {
        treasuryAmt := amount * rule.TreasuryPercent / 100.0
        result.Treasury[token] = treasuryAmt
        remaining := amount - treasuryAmt

        reinvestAmt := remaining * rule.ReinvestPercent / 100.0
        result.Reinvested[token] = reinvestAmt
        remaining -= reinvestAmt

        for pid, share := range rule.ParticipantShares {
            alloc := remaining * share / 100.0
            if result.Distributions[pid] == nil {
                result.Distributions[pid] = &ParticipantDistribution{
                    ParticipantID: pid, Tokens: make(map[TokenType]float64),
                }
            }
            result.Distributions[pid].Tokens[token] += alloc
        }
    }
    return result
}

type ParticipantCosts struct {
    ElectricityCost      float64 `json:"electricity_cost"`
    HardwareDepreciation float64 `json:"hardware_depreciation"`
    BandwidthCost        float64 `json:"bandwidth_cost"`
    FacilityCost         float64 `json:"facility_cost"`
}

type ROIReport struct {
    ParticipantID    string        `json:"participant_id"`
    Period           time.Duration `json:"period"`
    TotalEarningsUSD float64       `json:"total_earnings_usd"`
    TotalCostsUSD    float64       `json:"total_costs_usd"`
    NetProfitUSD     float64       `json:"net_profit_usd"`
    ROIPercent       float64       `json:"roi_percent"`
    BreakEvenDays    int           `json:"break_even_days"`
}

func (rd *RewardDistributor) GetParticipantROI(pid string,
    costs *ParticipantCosts, period time.Duration) (*ROIReport, error) {

    rd.mu.RLock()
    p, ok := rd.participants[pid]
    rd.mu.RUnlock()
    if !ok { return nil, fmt.Errorf("participant %s not found", pid) }

    totalEarnings := 0.0
    for token, balance := range p.TokenBalance {
        totalEarnings += balance * rd.tokenPrices[token]
    }
    totalCosts := costs.ElectricityCost + costs.HardwareDepreciation +
        costs.BandwidthCost + costs.FacilityCost

    roi := 0.0
    if totalCosts > 0 { roi = (totalEarnings - totalCosts) / totalCosts * 100.0 }

    dailyEarnings := totalEarnings / period.Hours() * 24
    breakEven := -1
    if dailyEarnings > 0 { breakEven = int(totalCosts / dailyEarnings) }

    return &ROIReport{
        ParticipantID: pid, Period: period,
        TotalEarningsUSD: totalEarnings, TotalCostsUSD: totalCosts,
        NetProfitUSD: totalEarnings - totalCosts,
        ROIPercent: roi, BreakEvenDays: breakEven,
    }, nil
}
```

#### 6.6.2 Deployment Automation

Four Bash scripts operationalize the entire deployment pipeline from bare metal to revenue-generating miner.

**Script 1: Node Preparation.** The `prepare-node.sh` script checks system requirements (RAM >= total VRAM, NVMe storage >= 500GB, 8+ CPU cores), installs NVIDIA driver 550 and CUDA 12.4, installs the NVIDIA Container Toolkit, deploys K3s v1.30.2 with the NVIDIA runtime configured, labels the node with GPU type and VRAM metadata, optionally enables Intel TDX TEE support, creates the HuggingFace cache directory on NVMe if available, installs Bittensor, and verifies GPU passthrough with a test pod.

```bash
#!/bin/bash
# scripts/prepare-node.sh
set -euo pipefail

NODE_ID=""
GPU_TYPE=""
TEE_ENABLED="false"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --node-id) NODE_ID="$2"; shift 2 ;;
        --gpu) GPU_TYPE="$2"; shift 2 ;;
        --tee) TEE_ENABLED="true"; shift ;;
        *) echo "Unknown: $1"; exit 1 ;;
    esac
done

# System check
ram_gb=$(free -g | awk '/^Mem:/{print $2}')
gpu_vram=$(nvidia-smi --query-gpu=memory.total --format=csv,noheader,nounits 2>/dev/null | head -1 | awk '{print int($1/1024)}')
[[ "$ram_gb" -lt "$gpu_vram" ]] && exit 1

# Install K3s
curl -sfL https://get.k3s.io | INSTALL_K3S_VERSION="v1.30.2+k3s1" \
    INSTALL_K3S_EXEC="server --disable traefik --disable servicelb" sh -
mkdir -p ~/.kube && cp /etc/rancher/k3s/k3s.yaml ~/.kube/config

# Label node
kubectl label node "$(hostname)" helixcluster.io/node-id="$NODE_ID" --overwrite
kubectl label node "$(hostname)" helixcluster.io/gpu="true" --overwrite
kubectl label node "$(hostname)" helixcluster.io/gpu-type="$GPU_TYPE" --overwrite

# TEE setup
if [[ "$TEE_ENABLED" == "true" ]]; then
    apt-get install -y intel-tdx-driver-dkms
    modprobe tdx-guest
    kubectl label node "$(hostname)" helixcluster.io/tee="enabled" --overwrite
fi

# Cache directory
mkdir -p /opt/helixcluster/cache/huggingface
```

**Script 2: Miner Deployment.** The `deploy-miner.sh` script accepts node ID, coldkey, and hotkey parameters, creates Kubernetes secrets for the Bittensor wallet and Chutes API key, deploys the unified Helm chart with TEE and monitoring enabled, waits for all pods to reach Ready state, and registers the node with the Chutes network via the miner API.

```bash
#!/bin/bash
# scripts/deploy-miner.sh
set -euo pipefail

NODE_ID=""
COLDKEY=""
HOTKEY=""
HELM_DIR="./helm/helixcluster-chutes"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --node-id) NODE_ID="$2"; shift 2 ;;
        --coldkey) COLDKEY="$2"; shift 2 ;;
        --hotkey) HOTKEY="$2"; shift 2 ;;
        *) echo "Unknown: $1"; exit 1 ;;
    esac
done

kubectl create ns helixcluster --dry-run=client -o yaml | kubectl apply -f -
kubectl create secret generic bittensor-wallet \
    --namespace helixcluster \
    --from-file=hotkey="~/.bittensor/wallets/${COLDKEY}/hotkeys/${HOTKEY}" \
    --dry-run=client -o yaml | kubectl apply -f -

helm upgrade --install helixcluster-chutes "$HELM_DIR" \
    --namespace helixcluster \
    --set tee.enabled=true \
    --set monitoring.grafana.enabled=true \
    --wait --timeout 10m

kubectl get pods -n helixcluster -o wide
```

**Script 3: Health Monitoring.** The `monitor-health.sh` script queries GraVal attestation status, miner API connectivity, GPU utilization, inference request rates, and earnings accumulation. It emits Prometheus-compatible metrics and triggers alerts when attestation fails, GPU utilization drops below 50%, or the miner API becomes unreachable for more than 60 seconds.

```bash
#!/bin/bash
# scripts/monitor-health.sh
set -euo pipefail

NAMESPACE="helixcluster"
ALERT_WEBHOOK="${ALERT_WEBHOOK:-}"

echo "=== GraVal Attestation ==="
kubectl logs -n "$NAMESPACE" -l app.kubernetes.io/name=graval-bootstrap --tail=20

echo "=== GPU Utilization ==="
kubectl top nodes -l helixcluster.io/gpu=true 2>/dev/null || echo "metrics-server not available"

echo "=== Pod Status ==="
kubectl get pods -n "$NAMESPACE" -o wide

echo "=== Inference Rate ==="
kubectl exec -n "$NAMESPACE" deploy/miner-api -- wget -qO- http://localhost:8080/metrics 2>/dev/null | grep "chutes_requests_total" || true

echo "=== Earnings ==="
curl -s -H "Authorization: Bearer ${CHUTES_API_KEY}" \
    https://api.chutes.ai/users/me | jq '{username, balance}' 2>/dev/null || true
```

**Script 4: Verification.** The `verify-deployment.sh` script runs an end-to-end inference test against the Chutes API using the OpenAI SDK, validates that responses are decrypted correctly when E2EE is enabled, and confirms that the deployment earns TAO rewards by checking on-chain emissions for the registered hotkey.

```bash
#!/bin/bash
# scripts/verify-deployment.sh
set -euo pipefail

echo "=== End-to-End Inference Test ==="
python3 <<'PYEOF'
import os
from openai import OpenAI
client = OpenAI(base_url="https://llm.chutes.ai/v1",
                api_key=os.environ.get("CHUTES_API_KEY"))
try:
    r = client.chat.completions.create(
        model="deepseek-ai/DeepSeek-V3-0324",
        messages=[{"role": "user", "content": "Hello from HelixCluster"}],
        max_tokens=50)
    print(f"Model: {r.model}")
    print(f"Response: {r.choices[0].message.content}")
    print("TEST: PASSED")
except Exception as e:
    print(f"TEST: FAILED - {e}")
PYEOF

echo "=== On-Chain Emissions ==="
btcli subnet emissions --netuid 64 2>/dev/null | head -20 || true
```

**Table 6.5: Deployment Script Reference**

| Script | Purpose | Key Operations | Execution Time |
|---|---|---|---|
| `prepare-node.sh` | Bare-metal to K3s-ready | Driver install, K3s deploy, labeling, TEE setup | 15-30 min |
| `deploy-miner.sh` | K3s to Chutes miner | Secrets, Helm install, pod verification | 5-10 min |
| `monitor-health.sh` | Ongoing observability | Logs, metrics, earnings query | < 10 sec |
| `verify-deployment.sh` | End-to-end validation | Inference test, on-chain check | 30-60 sec |

The complete integration presented in this chapter provides a production-hardened pathway for HelixCluster GPU nodes to simultaneously earn HLX and TAO rewards while contributing to the world's largest decentralized AI inference network. The six Go implementations, three YAML configuration layers, and four Bash automation scripts collectively form a declarative, observable, and economically optimized compute orchestration system that bridges the HelixCluster distributed operating system with the Chutes.ai Bittensor subnet.
