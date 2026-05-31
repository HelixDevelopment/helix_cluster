# Phase 8B: Burst Computing & Load Spillover Architecture

## Executive Summary

This report designs HelixCluster's burst computing architecture -- a system that monitors local GPU utilization and automatically spills excess workloads to external GPU clouds when local capacity exceeds configurable thresholds. The architecture treats remote compute (Chutes AI, io.net, RunPod, AWS Spot) as an extension of the local cluster, not as separate infrastructure to which we submit jobs. We answer the fundamental question: **"How do we consume THESE networks as part of OUR cluster?"**

**Key findings:**
- Karpenter provisions GPU nodes in 45-60 seconds vs Cluster Autoscaler's 3-4 minutes [^3837^]
- Chutes AI provides inference at ~85% lower cost than AWS with OpenAI-compatible APIs [^3481^]
- AWS Spot instances offer 70-90% discounts with a 2-minute preemption warning [^2500^]
- Netflix achieves <3 minute time-to-recovery through predictive scaling and prioritized load shedding [^3861^]
- Loophole Labs' Architect enables seamless GPU checkpoint migration on preemption using CRIU [^2971^]

---

## 1. Auto-Scaling Patterns for GPU Workloads

### 1.1 Horizontal Pod Autoscaler (HPA)

The standard Kubernetes HPA scales pod replicas based on CPU, memory, or custom metrics. For GPU workloads, HPA is insufficient alone because it operates at the pod level, not the node level, and GPU metrics require NVIDIA DCGM exporter integration.

```yaml
# HPA for GPU workload with custom metrics
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: gpu-inference-hpa
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: llm-inference
  minReplicas: 1
  maxReplicas: 20
  metrics:
    - type: Pods
      pods:
        metric:
          name: nvidia_gpu_utilization
        target:
          type: AverageValue
          averageValue: "80"
    - type: External
      external:
        metric:
          name: queue_depth
        target:
          type: Value
          value: "100"
  behavior:
    scaleUp:
      stabilizationWindowSeconds: 60
      policies:
        - type: Pods
          value: 4
          periodSeconds: 30
    scaleDown:
      stabilizationWindowSeconds: 300
      policies:
        - type: Pods
          value: 2
          periodSeconds: 60
```

**Limitation:** HPA scales pods but cannot provision new GPU nodes. A pod requesting a GPU that doesn't exist will sit in "Pending" indefinitely without a cluster-level autoscaler [^3838^].

### 1.2 KEDA: Event-Driven Autoscaling

KEDA (Kubernetes Event-Driven Autoscaler) is a CNCF graduated project that enables scaling based on 70+ event sources including queue depth, Kafka lag, and custom metrics. For HelixCluster's burst manager, KEDA provides the critical bridge between queue-based workload demand and scaling decisions [^3844^].

```yaml
# KEDA ScaledObject for queue-based GPU workload
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: gpu-inference-keda
  namespace: helixcluster
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: inference-worker
  minReplicaCount: 0
  maxReplicaCount: 50
  triggers:
    # Scale on Redis queue depth
    - type: redis
      metadata:
        address: redis.helixcluster.svc:6379
        listName: inference:pending
        listLength: "10"
      authenticationRef:
        name: redis-auth
    # Scale on custom GPU utilization metric
    - type: prometheus
      metadata:
        serverAddress: http://prometheus.monitoring:9090
        metricName: gpu_utilization_ratio
        query: |
          avg(nvidia_gpu_utilization_gpu{namespace="helixcluster"}) / 100
        threshold: "0.85"
```

**Why KEDA matters for burst:** KEDA can scale to zero when no work exists and scale up rapidly when queue depth grows. Its 70+ built-in scalers include Redis, RabbitMQ, Kafka, AWS SQS, and PostgreSQL -- all viable backends for a multi-tier spillover queue [^3848^].

### 1.3 Cluster Autoscaler vs Karpenter

The choice of node autoscaler determines how quickly burst capacity materializes [^3837^]:

| Feature | Karpenter | Cluster Autoscaler |
|---------|-----------|-------------------|
| Provisioning speed | 45-60 seconds | 3-4 minutes |
| Scaling logic | Workload-aware (pod requirements) | Node-group-aware (predefined groups) |
| Cost efficiency | High (active bin-packing, consolidation) | Moderate (prone to over-provisioning) |
| Spot support | Native, declarative | Manual via ASGs |
| GPU node support | Direct EC2 API calls | Auto Scaling Group delays |

**Production insight:** GPU node provisioning for high-end SKUs (A100, H100) typically takes 5-10 minutes end-to-end including VM boot, GPU driver initialization, and pod scheduling. Karpenter's direct EC2 API calls cut this to under a minute for the VM itself, but GPU operator DaemonSet installation adds 1-2 minutes [^3838^] [^3850^].

### 1.4 Predictive Scaling

Predictive autoscaling uses ML models (Prophet, LSTM, ARIMA) to forecast demand and pre-scale before traffic arrives. Netflix reduced their time-to-recovery from 10+ minutes to under 3 minutes using predictive scaling [^3861^] [^3879^].

```python
# Predictive scaler using Prophet + KEDA (conceptual)
class PredictiveBurstScaler:
    def forecast_gpu_demand(self, historical_metrics):
        """Forecast GPU demand using Prophet time-series model"""
        model = Prophet(
            daily_seasonality=True,
            weekly_seasonality=True,
            changepoint_prior_scale=0.05
        )
        # Train on 2-4 weeks of GPU utilization data
        model.fit(historical_metrics)
        # Predict next 30 minutes
        future = model.make_future_dataframe(periods=6, freq='5min')
        forecast = model.predict(future)
        return forecast['yhat_upper'].max()  # Conservative estimate
    
    def pre_scale_nodes(self, predicted_utilization):
        """Pre-emptively provision burst capacity"""
        if predicted_utilization > 0.85:
            # Pre-warm external GPU nodes before local saturation
            self.burst_manager.provision_warm_pool(count=2)
```

**Key insight:** Combining predictive scaling with reactive burst creates a two-layer defense: prediction handles known patterns, while the burst manager handles unexpected spikes [^3882^].

---

## 2. Cloud Bursting Architectures

### 2.1 Virtual Kubelet: The Core Abstraction

Virtual Kubelet is the key enabling technology for transparent cloud bursting. It registers a "virtual node" in the Kubernetes cluster that delegates pod execution to external providers. When local capacity is exhausted, the scheduler transparently places pods on the virtual node, which provisions them via cloud APIs [^3841^].

**Architecture:**

```
+-----------------------------------------------------------+
|                    HelixCluster Control Plane               |
|  +------------------+    +-----------------------------+   |
|  |  Kubernetes      |    |  Burst Manager (Go)         |   |
|  |  Scheduler       |<---|  - Monitors GPU util         |   |
|  |                  |    |  - Routes to virtual nodes   |   |
|  +------------------+    +-----------------------------+   |
+-----------------------------------------------------------+
         |                           |
         | Local GPU nodes           | Virtual Node (Virtual Kubelet)
         v                           v
+------------------+      +-----------------------------+
| Physical GPU Node|      | Virtual Kubelet Provider    |
| (On-Prem/Owned)  |      | - Chutes Provider           |
| - H100 x8        |      | - io.net Provider           |
| - A100 x16       |      | - RunPod Provider           |
| - RTX 4090 x4    |      | - AWS Spot Provider         |
+------------------+      +-----------------------------+
                                   |
                                   v
                    +-----------------------------+
                    |   External GPU Clouds        |
                    |   - Chutes AI (decentralized)|
                    |   - io.net (DePIN)           |
                    |   - RunPod (serverless)      |
                    |   - AWS EC2 Spot             |
                    +-----------------------------+
```

**Key advantage:** Applications use the same Kubernetes APIs (Pods, Deployments, Jobs) regardless of whether workloads run locally or in the cloud. No code changes required [^3841^].

### 2.2 Existing Virtual Kubelet Implementations for GPU

ORCA (Orchestration for Research Cloud Access) is a Virtual Kubelet provider that specifically targets GPU-intensive AI/ML workloads, enabling research clusters to burst to AWS EC2 GPU instances including P5 (H100), P4d (A100), and Spot instances [^3842^].

A community-built Virtual Kubelet for RunPod enables Kubernetes to offload GPU jobs to RunPod's cloud, automatically extending clusters with on-demand GPUs [^3845^] [^3846^].

### 2.3 Kubernetes Federation for Multi-Cluster

For organizations operating multiple GPU clusters across regions or clouds, Kubernetes Federation provides unified management. Options include [^3873^]:

| Solution | Use Case | Complexity |
|----------|----------|------------|
| KubeFed v2 | Full federation | High |
| Liqo | Multi-cluster networking | Medium |
| Admiralty | Cross-cluster scheduling | Medium |
| Karmada | Geo-distributed GPU pools | Medium |

**Limitation:** Virtual Kubelet hits scalability limits during extreme bursts (10K to 400K+ cores), as the entry-point control plane must handle all pods. For HelixCluster's scale, this is unlikely to be a concern [^3870^].

---

## 3. Queue-Based Load Spillover

### 3.1 Multi-Tier Queue Architecture

The core of HelixCluster's burst strategy is a priority queue system with multiple backends. Workflows flow through tiers based on priority and local capacity:

```
                    +-------------------+
                    |  Workload Ingress  |
                    |  (Inference Jobs)  |
                    +--------+----------+
                             |
                    +--------v----------+
                    |  Priority Router  |
                    |  (QoS Classifier) |
                    +--------+----------+
                             |
            +----------------+----------------+
            |                |                |
   +--------v-------+ +------v------+ +------v---------+
   | REAL-TIME QoS  | | BATCH QoS   | | BEST-EFFORT QoS|
   | P99 < 100ms    | | P95 < 5s    | | No SLO         |
   +--------+-------+ +------+------+ +------+---------+
            |                |                |
            v                v                v
   +--------+-------+ +------v------+ +------v---------+
   | Local GPU Pool | | Local First | | Burst Queue    |
   | (Guaranteed)   | | -> Chutes   | | -> RunPod      |
   | Never spill    | | -> io.net   | | -> AWS Spot    |
   +----------------+ +-------------+ +----------------+
```

### 3.2 Queue Backend Implementation

```go
// Priority queue with spillover tiers
type QueueTier int

const (
    TierLocal       QueueTier = iota  // Local GPU only
    TierChutes                         // Chutes AI (low latency, low cost)
    TierIONet                          // io.net (cheapest on-demand)
    TierRunPod                         // RunPod serverless (auto-scale)
    TierAWSSpot                        // AWS Spot (cheapest, preemptible)
    TierDeadLetter                     // Failed jobs for retry/analysis
)

type SpilloverQueue struct {
    redis *redis.Client
    tiers map[QueueTier]string  // Redis list keys per tier
}

func (q *SpilloverQueue) Route(job *InferenceJob) error {
    // QoS-aware routing
    switch job.QoS {
    case QoSRealtime:
        // Real-time: local only, never spill
        return q.enqueue(TierLocal, job)
    
    case QoSBatch:
        // Batch: local first, spill when saturated
        if q.localCapacityAvailable() {
            return q.enqueue(TierLocal, job)
        }
        // Cost-aware selection: cheapest with acceptable latency
        provider := q.selectCheapestProvider(job.MaxAcceptableLatency)
        return q.enqueue(provider, job)
    
    case QoSBestEffort:
        // Best-effort: always to cheapest external
        return q.enqueue(q.cheapestAvailableTier(), job)
    }
    return nil
}
```

### 3.3 Back-Pressure and Dead Letter Queues

When external providers are also saturated, the system applies back-pressure:

1. **Provider saturation detected** --> increase queue wait timeout
2. **All providers at capacity** --> activate back-pressure (return 503 with Retry-After header)
3. **Job fails after max retries** --> move to dead letter queue for analysis
4. **DLQ threshold exceeded** --> alert operators, potentially degrade quality (smaller model, lower precision)

---

## 4. Spot/Preemptible Instance Strategy

### 4.1 Cloud Spot Instance Comparison

| Provider | Instance Type | On-Demand | Spot Price | Discount | Preemption Warning |
|----------|--------------|-----------|------------|----------|-------------------|
| AWS p5.48xlarge | 8x H100 | $55.04/hr | ~$22.78/hr | ~60% | 2 minutes [^3862^] |
| AWS g6e.48xlarge | 8x L40S | $30.13/hr | ~$9.00/hr | ~70% | 2 minutes [^3862^] |
| Azure ND H100 v5 | 8x H100 | ~$48/hr | ~$19/hr | ~60% | Variable [^3892^] |
| GCP a3-highgpu-8g | 8x H100 | ~$50/hr | ~$15/hr | ~70% | 30 seconds |

### 4.2 Handling Preemption: Checkpoint and Migrate

Loophole Labs' Architect demonstrates that GPU preemption can be made transparent through continuous checkpointing. Their approach [^2971^]:

1. **Normal Operation:** Continuously capture pod state and GPU memory using extended CRIU
2. **Preemption Notice:** On 2-minute AWS warning, begin live migration
3. **Migration:** Provision target node, stream checkpoint from passive storage + peer-to-peer from old node
4. **Rerouting:** Update routing layer transparently; clients cannot tell migration occurred

**Key insight:** For deep learning training, complete CUDA context and GPU memory state (model weights, optimizer states, gradient buffers) can be serialized. A training run at epoch 47 continues from epoch 47 on the new node [^2971^].

### 4.3 Spot Fleet Diversification

```yaml
# Karpenter NodePool with Spot diversification
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: gpu-spot-pool
spec:
  template:
    spec:
      requirements:
        - key: karpenter.k8s.aws/instance-family
          operator: In
          values: [p5, p4d, g6e]  # H100, A100, L40S
        - key: karpenter.sh/capacity-type
          operator: In
          values: ["spot", "on-demand"]
        - key: nvidia.com/gpu.memory
          operator: Gt
          values: ["40000"]  # 40GB+ VRAM
      nodeClassRef:
        group: karpenter.k8s.aws
        kind: EC2NodeClass
        name: gpu-class
  disruption:
    consolidationPolicy: WhenEmpty
    consolidateAfter: 30s
    expireAfter: 720h
  limits:
    nvidia.com/gpu: 100
```

---

## 5. Provider-Specific Integration Details

### 5.1 Chutes AI (Primary Burst Target)

Chutes AI is a decentralized serverless compute platform built on Bittensor Subnet 64, processing ~160 billion tokens daily for 400,000+ users at up to 90% lower costs than traditional providers [^3481^] [^3629^].

**API:** OpenAI-compatible (`https://llm.chutes.ai/v1`)

```go
// Chutes AI burst client
type ChutesClient struct {
    baseURL    string
    apiKey     string
    httpClient *http.Client
}

func NewChutesClient(apiKey string) *ChutesClient {
    return &ChutesClient{
        baseURL: "https://llm.chutes.ai/v1",
        apiKey:  apiKey,
        httpClient: &http.Client{
            Timeout: 30 * time.Second,
        },
    }
}

func (c *ChutesClient) Inference(ctx context.Context, req InferenceRequest) (*InferenceResponse, error) {
    body, _ := json.Marshal(map[string]interface{}{
        "model":    req.Model,  // e.g., "deepseek-ai/DeepSeek-V3-0324"
        "messages": req.Messages,
        "temperature": req.Temperature,
        "max_tokens":  req.MaxTokens,
    })
    
    httpReq, _ := http.NewRequestWithContext(ctx, "POST",
        c.baseURL+"/chat/completions", bytes.NewReader(body))
    httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
    httpReq.Header.Set("Content-Type", "application/json")
    
    resp, err := c.httpClient.Do(httpReq)
    // ... handle response
}

// Pricing: ~$0.30-$0.44 per 1M input tokens (DeepSeek V3)
// vs AWS Bedrock: ~$2.00 per 1M input tokens
// Cost reduction: ~85%
```

**Smart routing:** Chutes supports automatic model selection [^3629^]:
- `default` --> configured failover order
- `default:latency` --> lowest TTFT right now
- `default:throughput` --> highest TPS right now
- `modelA,modelB,modelC` --> inline failover across listed models

### 5.2 io.net (Scale Burst Target)

io.net is a DePIN (Decentralized Physical Infrastructure Network) aggregating GPUs from data centers worldwide. Pricing as of July 2025 [^3898^] [^3899^]:

| GPU Type | Ray Cluster | Container | Best For |
|----------|-------------|-----------|----------|
| RTX 4090 | $0.25/hr | $0.50/hr | Small models, dev |
| H100 PCIe | $0.89/hr | $1.70/hr | Training, inference |
| H100 SXM5 | $1.19/hr | $1.99/hr | Large-scale training |
| H200 | $2.39/hr | $2.49/hr | Biggest models |

**Key advantage:** 70% cheaper than AWS, instant deployment, no waitlists. io.net's pricing for H100 SXM ($1.19/hr) undercuts even RunPod's on-demand rate ($1.99/hr) [^3898^].

### 5.3 RunPod (Serverless Burst)

RunPod's serverless offering provides per-second billing with auto-scaling from zero. Pricing for Flex workers [^3859^]:

| GPU | Memory | Flex/second | Flex/hour | Best For |
|-----|--------|-------------|-----------|----------|
| RTX 4000 | 16GB | $0.00016 | $0.58/hr | Small models |
| A5000/3090 | 24GB | $0.00019 | $0.68/hr | Medium inference |
| A6000 | 48GB | $0.00034 | $1.22/hr | Big models |
| A100 | 80GB | $0.00076 | $2.74/hr | Large training |
| H100 | 80GB | $0.00116 | $4.18/hr | Big models |

**Key advantage:** Native serverless with scale-to-zero. Ideal for sporadic burst traffic.

### 5.4 AWS EC2 Spot (Cheapest Batch Target)

AWS EC2 Spot provides the deepest discounts for fault-tolerant batch workloads [^3862^] [^2500^]:

- **H100 (p5.48xlarge):** ~$22.78/hr Spot vs $55.04/hr On-Demand (~60% off)
- **L40S (g6e.48xlarge):** ~$9.00/hr Spot vs $30.13/hr On-Demand (~70% off)
- **Capacity Rebalancing:** Early warning (5-10 min) before preemption enables proactive migration

---

## 6. Latency vs Cost Trade-offs

### 6.1 End-to-End Latency Comparison

Based on benchmarks from local and cloud inference studies [^3895^] [^3897^] [^3894^]:

| Component | Local GPU | Chutes AI | io.net | RunPod | AWS Spot |
|-----------|-----------|-----------|--------|--------|----------|
| **Time to First Token** | 15-80ms | 100-400ms | 150-600ms | 100-500ms | 200-800ms |
| **Network RTT** | 0ms | 30-100ms | 50-150ms | 30-100ms | Variable |
| **Cold Start** | 3-8s (model load) | ~0 (always hot) | 2-5 min | 5-30s | 3-5 min |
| **Tokens/sec (throughput)** | 18-65 tok/s | 50-150 tok/s | 40-120 tok/s | 50-150 tok/s | 50-150 tok/s |
| **P99 Latency (short)** | 40-120ms | 200-600ms | 300-900ms | 250-700ms | 400-1200ms |
| **P99 Latency (long)** | 1.2-3.8s | 1.0-2.5s | 1.2-3.0s | 1.0-2.5s | 1.5-4.0s |

**Key insight:** For short outputs (<300 tokens), local GPU dominates due to zero network overhead. For longer outputs, cloud throughput can overcome TTFT penalty [^3895^].

### 6.2 Cost Comparison (Per GPU Hour, H100 Class)

| Provider | Price/Hour | Billing | Preemptible | Best For |
|----------|-----------|---------|-------------|----------|
| **Owned H100** | ~$1.20/hr (TCO) | Always on | No | Baseline capacity |
| **io.net H100 SXM** | $1.19/hr | Hourly | No | Cost-sensitive training |
| **RunPod H100** | $1.99/hr | Per-second | No | Serverless inference |
| **Chutes AI** | ~$0.50-1.00/hr equiv | Per-token | No | Token-based inference |
| **AWS Spot H100** | ~$22.78/8=$2.85/hr | Per-second | Yes (2-min warning) | Fault-tolerant batch |
| **AWS On-Demand H100** | $6.88/hr (1 of 8) | Per-second | No | Reserved capacity |

### 6.3 Quality of Service Tiers

| QoS Tier | Latency SLO | Routing | Degradation | Cost Premium |
|----------|-------------|---------|-------------|--------------|
| **Real-Time** | P99 < 100ms | Local only | Reduce context window | 0x |
| **Interactive** | P95 < 500ms | Local > Chutes > RunPod | Use smaller model | 0.3x |
| **Batch** | P95 < 30s | Cheapest available | Accept spot preemption | 0.5x |
| **Best-Effort** | Best effort | Always cheapest | Quantize to INT4 | 0.2x |

---

## 7. Existing Implementations: Lessons Learned

### 7.1 Netflix: Handling Sudden Load Spikes

Netflix's architecture for handling burst traffic provides the most relevant production blueprint [^3861^] [^3864^] [^3865^]:

**Key strategies:**
1. **Predictive Pre-scaling:** For known events (title launches), scale the entire fleet ahead of time using SPS-to-RPS mapping
2. **RPS-Based Scaling:** Switched from CPU-based to request-per-second based autoscaling -- CPU hits 100% at 2x and 10x load, providing no useful signal
3. **High-Resolution Metrics:** Reduced metric intervals from 5 minutes to 5 seconds via Atlas, cutting detection time by 3x
4. **Prioritized Load Shedding:** During saturation, shed by priority: Critical (playback) > Degraded (personalization) > Bulk (batch) > Best-Effort
5. **Success/Failure Buffers:** Every service operates with two headroom buffers -- success buffer (before errors) and failure buffer (before congestive collapse)

**Time-to-recovery breakdown:**
| Stage | Before Optimization | After Optimization |
|-------|-------------------|-------------------|
| Detection | 4 minutes | <1 minute |
| App Startup | 6 minutes | 2 minutes (parallel) |
| System Startup | 2 minutes | 1 minute |
| Hardware Provisioning | 2 minutes | <1 minute (EKS) |
| **Total TTR** | **10+ minutes** | **<3 minutes** |

### 7.2 Google Borg: Oversubscription

Google's Borg cluster manager optimizes resource use through job oversubscription. Autopilot configures task concurrency and CPU/memory limits using ML and historical data to reduce slack and task failures. The key insight: most tasks don't use their requested resources, allowing safe oversubscription up to measured utilization plus a safety margin [^3883^].

**Application to HelixCluster:** Monitor actual GPU utilization vs requested. If pods request 100% GPU but average 60% utilization, safe oversubscription allows running more pods before spilling to external clouds.

### 7.3 Ray: Elastic Execution

Ray's autoscaler dynamically adds and removes worker nodes based on task demands [^3869^] [^3868^]:

```python
# Ray elastic execution pattern
@ray.remote(num_gpus=1)
def inference_task(model_ref, batch):
    # Runs on auto-provisioned GPU
    return model_ref.forward(batch)

# Submit 100 tasks - Ray provisions GPUs as needed
futures = [inference_task.remote(model, batch) for batch in batches]
results = ray.get(futures)  # Auto-scales up, then back down
```

**KubeRay Federation** is actively being developed to enable RayCluster deployment across multiple Kubernetes clusters, with auto-scaling that dynamically balances workers across cloud providers [^3870^].

---

## 8. The HelixCluster Burst Manager

### 8.1 Architecture Overview

```
+---------------------------------------------------------------------+
|                    HELIXCLUSTER BURST MANAGER                        |
|                                                                      |
|  +------------------+  +------------------+  +------------------+   |
|  | Monitor          |  | Router           |  | Provisioner      |   |
|  | - GPU util scrape|  | - QoS classifier |  | - Chutes client  |   |
|  | - Queue depth    |  | - Cost optimizer |  | - io.net client  |   |
|  | - Provider health|  | - Fallback chain |  | - RunPod client  |   |
|  | - Predictor      |  | - Load balancer  |  | - AWS spot client|   |
|  +--------+---------+  +--------+---------+  +--------+---------+   |
|           |                     |                     |              |
|  +--------v---------+  +--------v---------+  +--------v---------+   |
|  | Decision Engine   |  | Cost Model       |  | Health Checker   |   |
|  | (State Machine)   |  | (Real-time)      |  | (Circuit Breaker)|   |
|  +--------+---------+  +------------------+  +------------------+   |
|           |                                                          |
+-----------|----------------------------------------------------------+
            |
    +-------v-----------------------------------------------------+
    |                    EXTERNAL GPU PROVIDERS                     |
    |  +----------+  +----------+  +----------+  +-------------+  |
    |  | Chutes   |  | io.net   |  | RunPod   |  | AWS Spot    |  |
    |  | $1.19/hr |  | $1.19/hr |  | $1.99/hr |  | $2.85/hr    |  |
    |  | <200ms   |  | <300ms   |  | <200ms   |  | Variable    |  |
    |  | 99.5%    |  | 98%      |  | 99%      |  | 90%         |  |
    |  +----------+  +----------+  +----------+  +-------------+  |
    +-------------------------------------------------------------+
```

### 8.2 Burst Manager: Go Implementation

```go
package burst

import (
    "context"
    "fmt"
    "sync"
    "time"
    
    "github.com/prometheus/client_golang/prometheus"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/client-go/kubernetes"
)

// --- Core Types ---

type QoSClass int

const (
    QoSRealtime QoSClass = iota     // P99 < 100ms, never spill
    QoSInteractive                   // P95 < 500ms, spill to fast external
    QoSBatch                         // P95 < 30s, spill to cheapest
    QoSBestEffort                    // No SLO, always cheapest
)

type BurstProvider int

const (
    ProviderLocal    BurstProvider = iota
    ProviderChutes
    ProviderIONet
    ProviderRunPod
    ProviderAWSSpot
    ProviderCount
)

func (p BurstProvider) String() string {
    return []string{"local", "chutes", "ionet", "runpod", "aws-spot"}[p]
}

type ProviderState struct {
    Provider      BurstProvider
    Available     bool
    CurrentCost   float64       // $/hr for H100 equiv
    AvgLatency    time.Duration // P95 latency
    ErrorRate     float64       // 0-1
    ActiveJobs    int
    MaxCapacity   int
    LastChecked   time.Time
}

type BurstConfig struct {
    UtilizationThreshold float64       // e.g., 0.90 (90%)
    ThresholdDuration    time.Duration // e.g., 60s
    CooldownDuration     time.Duration // e.g., 300s before scaling back
    MaxConcurrentBurst   int           // Max jobs in external clouds
    CostBudget           float64       // Max $/hr for burst capacity
    Providers            []ProviderConfig
}

type ProviderConfig struct {
    Provider    BurstProvider
    Weight      float64  // Preference weight
    APIKey      string
    APIEndpoint string
    MaxCost     float64  // Max $/hr acceptable
    Enabled     bool
}

// --- Burst Manager ---

type Manager struct {
    config       BurstConfig
    k8s          kubernetes.Interface
    providers    map[BurstProvider]ProviderClient
    providerStates map[BurstProvider]*ProviderState
    
    // Decision state
    localUtilization float64
    burstActive      bool
    burstJobs        map[string]*BurstJob
    
    // Control
    mu      sync.RWMutex
    ctx     context.Context
    cancel  context.CancelFunc
    
    // Metrics
    localUtilGauge   prometheus.Gauge
    burstActiveGauge prometheus.Gauge
    burstCostGauge   prometheus.Gauge
    jobsRoutedTotal  *prometheus.CounterVec
}

type BurstJob struct {
    ID           string
    QoS          QoSClass
    Provider     BurstProvider
    Model        string
    StartedAt    time.Time
    CostPerHour  float64
    CheckpointID string // For spot preemption
}

type ProviderClient interface {
    Submit(ctx context.Context, job *InferenceRequest) (*InferenceResponse, error)
    HealthCheck(ctx context.Context) (*ProviderState, error)
    Cancel(ctx context.Context, jobID string) error
    CostEstimate(model string, tokens int) float64
}

// --- Constructor ---

func NewManager(config BurstConfig, k8s kubernetes.Interface) *Manager {
    ctx, cancel := context.WithCancel(context.Background())
    m := &Manager{
        config:         config,
        k8s:            k8s,
        providers:      make(map[BurstProvider]ProviderClient),
        providerStates: make(map[BurstProvider]*ProviderState),
        burstJobs:      make(map[string]*BurstJob),
        ctx:            ctx,
        cancel:         cancel,
    }
    m.registerMetrics()
    return m
}

func (m *Manager) registerMetrics() {
    m.localUtilGauge = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "helixcluster_local_gpu_utilization",
        Help: "Current local GPU utilization ratio",
    })
    m.burstActiveGauge = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "helixcluster_burst_active",
        Help: "Whether burst mode is currently active",
    })
    m.burstCostGauge = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "helixcluster_burst_cost_per_hour",
        Help: "Current cost per hour of burst capacity",
    })
    m.jobsRoutedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
        Name: "helixcluster_jobs_routed_total",
        Help: "Total jobs routed by provider",
    }, []string{"provider", "qos", "result"})
}

// --- Main Control Loop ---

func (m *Manager) Start() error {
    // Initialize providers
    for _, pc := range m.config.Providers {
        if !pc.Enabled {
            continue
        }
        client, err := m.createProviderClient(pc)
        if err != nil {
            return fmt.Errorf("failed to create %s client: %w", pc.Provider, err)
        }
        m.providers[pc.Provider] = client
        m.providerStates[pc.Provider] = &ProviderState{
            Provider: pc.Provider,
        }
    }
    
    // Start background loops
    go m.monitoringLoop()
    go m.healthCheckLoop()
    go m.decisionLoop()
    go m.costOptimizationLoop()
    
    return nil
}

// monitoringLoop scrapes local GPU utilization from Prometheus/DCGM
func (m *Manager) monitoringLoop() {
    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()
    
    for {
        select {
        case <-m.ctx.Done():
            return
        case <-ticker.C:
            util := m.scrapeLocalGPUUtilization()
            m.mu.Lock()
            m.localUtilization = util
            m.localUtilGauge.Set(util)
            m.mu.Unlock()
        }
    }
}

func (m *Manager) scrapeLocalGPUUtilization() float64 {
    // Query Prometheus for average GPU utilization across all nodes
    // Query: avg(nvidia_gpu_utilization_gpu{cluster="helixcluster"}) / 100
    // Returns value 0.0 - 1.0
    // Stub: actual implementation queries Prometheus API
    return 0.0 // Placeholder
}

// decisionLoop evaluates whether to activate burst mode
func (m *Manager) decisionLoop() {
    overThresholdSince := time.Time{}
    ticker := time.NewTicker(10 * time.Second)
    defer ticker.Stop()
    
    for {
        select {
        case <-m.ctx.Done():
            return
        case <-ticker.C:
            m.mu.RLock()
            util := m.localUtilization
            burstActive := m.burstActive
            m.mu.RUnlock()
            
            if util >= m.config.UtilizationThreshold {
                if overThresholdSince.IsZero() {
                    overThresholdSince = time.Now()
                }
                // Check if we've been over threshold long enough
                if !burstActive && time.Since(overThresholdSince) >= m.config.ThresholdDuration {
                    m.activateBurst()
                }
            } else {
                overThresholdSince = time.Time{}
                if burstActive && util < m.config.UtilizationThreshold*0.7 {
                    // Deactivate when utilization drops to 70% of threshold
                    m.deactivateBurst()
                }
            }
        }
    }
}

// --- Burst Activation/Deactivation ---

func (m *Manager) activateBurst() {
    m.mu.Lock()
    defer m.mu.Unlock()
    
    if m.burstActive {
        return
    }
    
    m.burstActive = true
    m.burstActiveGauge.Set(1)
    
    // Pre-warm cheapest provider
    cheapest := m.selectCheapestProvider()
    if client, ok := m.providers[cheapest]; ok {
        // Send warm-up request to establish connection
        client.HealthCheck(m.ctx)
    }
    
    fmt.Printf("[BurstManager] Burst activated at %.0f%% utilization. "
        "Spilling to %s\n", m.localUtilization*100, cheapest)
}

func (m *Manager) deactivateBurst() {
    m.mu.Lock()
    defer m.mu.Unlock()
    
    if !m.burstActive {
        return
    }
    
    m.burstActive = false
    m.burstActiveGauge.Set(0)
    
    // Drain burst jobs back to local
    for id, job := range m.burstJobs {
        if job.Provider != ProviderLocal {
            fmt.Printf("[BurstManager] Draining job %s from %s back to local\n",
                id, job.Provider)
        }
    }
    
    fmt.Printf("[BurstManager] Burst deactivated. Utilization: %.0f%%\n",
        m.localUtilization*100)
}

// --- Job Routing ---

func (m *Manager) RouteJob(ctx context.Context, req *InferenceRequest) (*InferenceResponse, error) {
    m.mu.RLock()
    burstActive := m.burstActive
    m.mu.RUnlock()
    
    // QoS-aware routing decision
    provider := m.selectProvider(req.QoS, burstActive)
    
    if provider == ProviderLocal {
        // Route to local GPU
        resp, err := m.routeLocal(ctx, req)
        m.jobsRoutedTotal.WithLabelValues("local", req.QoS.String(), "success").Inc()
        return resp, err
    }
    
    // Route to external provider
    client, ok := m.providers[provider]
    if !ok {
        // Fallback to next provider
        provider = m.nextFallback(provider)
        client = m.providers[provider]
    }
    
    resp, err := client.Submit(ctx, req)
    if err != nil {
        m.jobsRoutedTotal.WithLabelValues(provider.String(), req.QoS.String(), "error").Inc()
        // Attempt fallback
        fallback := m.nextFallback(provider)
        if fallback != provider {
            return m.providers[fallback].Submit(ctx, req)
        }
        return nil, err
    }
    
    m.jobsRoutedTotal.WithLabelValues(provider.String(), req.QoS.String(), "success").Inc()
    return resp, nil
}

func (m *Manager) selectProvider(qos QoSClass, burstActive bool) BurstProvider {
    switch qos {
    case QoSRealtime:
        // Real-time: always local, never burst
        return ProviderLocal
        
    case QoSInteractive:
        if !burstActive {
            return ProviderLocal
        }
        // Prefer low-latency external: Chutes or RunPod
        return m.selectLowestLatency(ProviderChutes, ProviderRunPod)
        
    case QoSBatch:
        if !burstActive {
            return ProviderLocal
        }
        // Cost-optimized: io.net cheapest, then Chutes
        return m.selectCheapestAvailable(ProviderIONet, ProviderChutes, ProviderAWSSpot)
        
    case QoSBestEffort:
        // Always cheapest, local or external
        return m.selectCheapestAvailable(
            ProviderIONet, ProviderChutes, ProviderAWSSpot, ProviderRunPod,
        )
    }
    return ProviderLocal
}

// Fallback chain: local -> chutes -> ionet -> runpod -> aws-spot
func (m *Manager) nextFallback(current BurstProvider) BurstProvider {
    chain := []BurstProvider{
        ProviderLocal, ProviderChutes, ProviderIONet,
        ProviderRunPod, ProviderAWSSpot,
    }
    for i, p := range chain {
        if p == current && i+1 < len(chain) {
            return chain[i+1]
        }
    }
    return ProviderLocal // Ultimate fallback
}

func (m *Manager) selectCheapestAvailable(candidates ...BurstProvider) BurstProvider {
    var cheapest BurstProvider
    var minCost float64 = 1e9
    
    for _, p := range candidates {
        state, ok := m.providerStates[p]
        if !ok || !state.Available {
            continue
        }
        if state.CurrentCost < minCost {
            minCost = state.CurrentCost
            cheapest = p
        }
    }
    
    if cheapest == 0 {
        return ProviderLocal
    }
    return cheapest
}

func (m *Manager) selectLowestLatency(candidates ...BurstProvider) BurstProvider {
    var best BurstProvider
    var minLatency time.Duration = time.Hour
    
    for _, p := range candidates {
        state, ok := m.providerStates[p]
        if !ok || !state.Available {
            continue
        }
        if state.AvgLatency < minLatency {
            minLatency = state.AvgLatency
            best = p
        }
    }
    
    if best == 0 {
        return ProviderLocal
    }
    return best
}

// --- Health Checks ---

func (m *Manager) healthCheckLoop() {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    
    for {
        select {
        case <-m.ctx.Done():
            return
        case <-ticker.C:
            for provider, client := range m.providers {
                state, err := client.HealthCheck(m.ctx)
                m.mu.Lock()
                if err != nil {
                    m.providerStates[provider].Available = false
                    m.providerStates[provider].ErrorRate = 1.0
                } else {
                    m.providerStates[provider] = state
                }
                m.mu.Unlock()
            }
        }
    }
}

// --- Cost Optimization ---

func (m *Manager) costOptimizationLoop() {
    ticker := time.NewTicker(5 * time.Minute)
    defer ticker.Stop()
    
    for {
        select {
        case <-m.ctx.Done():
            return
        case <-ticker.C:
            m.rebalanceForCost()
        }
    }
}

func (m *Manager) rebalanceForCost() {
    m.mu.RLock()
    currentCost := 0.0
    for _, job := range m.burstJobs {
        currentCost += job.CostPerHour
    }
    m.mu.RUnlock()
    
    if currentCost > m.config.CostBudget {
        // Move jobs from expensive to cheaper providers
        cheapest := m.selectCheapestAvailable(ProviderIONet, ProviderChutes, ProviderAWSSpot)
        for id, job := range m.burstJobs {
            if job.Provider != cheapest && job.QoS != QoSRealtime {
                fmt.Printf("[BurstManager] Cost optimization: moving job %s "
                    "from %s to %s\n", id, job.Provider, cheapest)
                // Initiate migration
            }
        }
    }
    
    m.burstCostGauge.Set(currentCost)
}

// --- Graceful Degradation ---

func (m *Manager) HandleOverload(ctx context.Context) DegradationStrategy {
    // When all providers are saturated, degrade quality
    strategies := []DegradationStrategy{
        {Action: ReduceContextWindow, Factor: 0.5},   // Halve context
        {Action: UseSmallerModel, Factor: 0.7},       // Drop to 70% size model
        {Action: ReducePrecision, Factor: 0.5},       // FP16 -> INT8
        {Action: EnableCaching, Factor: 0.8},         // Aggressive caching
    }
    return strategies[0] // Apply most conservative first
}

type DegradationAction int

const (
    ReduceContextWindow DegradationAction = iota
    UseSmallerModel
    ReducePrecision
    EnableCaching
)

type DegradationStrategy struct {
    Action DegradationAction
    Factor float64
}

func (m *Manager) Stop() {
    m.cancel()
    // Gracefully drain burst jobs
    m.mu.Lock()
    for id, job := range m.burstJobs {
        if client, ok := m.providers[job.Provider]; ok {
            client.Cancel(context.Background(), id)
        }
    }
    m.mu.Unlock()
}

// InferenceRequest represents a GPU inference job
type InferenceRequest struct {
    ID              string
    QoS             QoSClass
    Model           string
    Messages        []Message
    MaxTokens       int
    Temperature     float64
    MaxAcceptableLatency time.Duration
}

type Message struct {
    Role    string `json:"role"`
    Content string `json:"content"`
}

type InferenceResponse struct {
    ID         string `json:"id"`
    Content    string `json:"content"`
    TokensUsed int    `json:"tokens_used"`
    Provider   string `json:"provider"`
    Latency    time.Duration
}

func (q QoSClass) String() string {
    return []string{"realtime", "interactive", "batch", "best-effort"}[q]
}

func (m *Manager) createProviderClient(pc ProviderConfig) (ProviderClient, error) {
    switch pc.Provider {
    case ProviderChutes:
        return NewChutesClient(pc.APIKey), nil
    case ProviderIONet:
        return NewIONetClient(pc.APIKey), nil
    case ProviderRunPod:
        return NewRunPodClient(pc.APIKey), nil
    case ProviderAWSSpot:
        return NewAWSSpotClient(pc.APIEndpoint), nil
    default:
        return nil, fmt.Errorf("unknown provider: %v", pc.Provider)
    }
}
```

### 8.3 Spot Preemption Handler

```go
package burst

// SpotPreemptionHandler manages checkpoint/restore for AWS Spot instances
type SpotPreemptionHandler struct {
    checkpointStore CheckpointStore  // S3 or persistent volume
    burstManager    *Manager
}

// HandlePreemptionNotice is called when AWS 2-minute warning is received
func (h *SpotPreemptionHandler) HandlePreemptionNotice(ctx context.Context, instanceID string) {
    log.Printf("[SpotHandler] Preemption notice for %s - 2 minutes to migrate", instanceID)
    
    // 1. Identify running jobs on this instance
    jobs := h.burstManager.GetJobsOnInstance(instanceID)
    
    // 2. For each job, trigger checkpoint
    for _, job := range jobs {
        go h.migrateJob(ctx, job)
    }
}

func (h *SpotPreemptionHandler) migrateJob(ctx context.Context, job *BurstJob) {
    // 3. Find replacement capacity
    replacement := h.burstManager.selectCheapestAvailable(
        ProviderIONet, ProviderChutes, ProviderRunPod,
    )
    
    // 4. Provision replacement
    client := h.burstManager.providers[replacement]
    
    // 5. Restore from checkpoint
    checkpoint, err := h.checkpointStore.Get(ctx, job.CheckpointID)
    if err != nil {
        // Fallback: restart job from beginning
        log.Printf("[SpotHandler] Checkpoint restore failed for %s, restarting", job.ID)
        client.Submit(ctx, &InferenceRequest{ID: job.ID})
        return
    }
    
    // 6. Submit restored job
    err = client.Restore(ctx, checkpoint)
    if err != nil {
        log.Printf("[SpotHandler] Migration failed for %s: %v", job.ID, err)
        return
    }
    
    log.Printf("[SpotHandler] Migrated job %s to %s", job.ID, replacement)
}
```

---

## 9. Complete Fallback Chain

```
+------------------------------------------------------------------+
|                    HELIXCLUSTER FALLBACK CHAIN                      |
+------------------------------------------------------------------+
|                                                                    |
|  LOCAL GPUs (owned)                                                |
|  +--> If utilization > 90% for 60s                                 |
|       |                                                            |
|       v                                                            |
|  CHUTES AI (decentralized, low latency)                            |
|  +--> If error rate > 5% or latency > 300ms                       |
|       |                                                            |
|       v                                                            |
|  io.net (cheapest on-demand, $0.89/hr H100)                       |
|  +--> If no capacity available                                     |
|       |                                                            |
|       v                                                            |
|  RunPod Serverless (per-second billing, auto-scale)                |
|  +--> If queue wait > 30s                                          |
|       |                                                            |
|       v                                                            |
|  AWS EC2 Spot (cheapest batch, ~$2.85/hr H100)                    |
|  +--> If preemption risk too high or no spot capacity              |
|       |                                                            |
|       v                                                            |
|  AWS EC2 On-Demand (guaranteed, $6.88/hr H100)                    |
|  +--> ULTIMATE FALLBACK                                            |
|                                                                    |
|  If ALL external fail:                                             |
|  +--> Back-pressure (return 503 + Retry-After)                    |
|  +--> Graceful degradation (smaller model, shorter context)        |
|  +--> Dead letter queue for later processing                       |
+------------------------------------------------------------------+
```

---

## 10. Cost-Latency Optimization Matrix

| Scenario | Primary | Fallback | Cost (H100 hr) | P99 Latency | Availability |
|----------|---------|----------|----------------|-------------|--------------|
| Real-time inference (<100ms) | Local GPU | Chutes (TEE) | $0 (owned) / $1.19 | 15-100ms | 99.99% |
| Interactive chat (500ms OK) | Local GPU | Chutes > RunPod | $0 / $1.19-1.99 | 100-500ms | 99.9% |
| Batch embedding | Local GPU | io.net > Chutes | $0 / $0.89-1.19 | 1-5s | 99.5% |
| Training checkpoint resume | Local GPU | io.net > AWS Spot | $0 / $0.89-2.85 | 5-30s | 98% |
| Large-scale fine-tuning | io.net cluster | AWS Spot fleet | $0.89-2.85 | N/A | 95% |
| Emergency overflow (all full) | AWS On-Demand | Back-pressure | $6.88 | Variable | 99.99% |

---

## 11. HelixCluster Integration

### 11.1 Kubernetes Deployment

```yaml
# burst-manager-deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: helixcluster-burst-manager
  namespace: helixcluster
spec:
  replicas: 1
  selector:
    matchLabels:
      app: burst-manager
  template:
    metadata:
      labels:
        app: burst-manager
    spec:
      serviceAccountName: burst-manager
      containers:
        - name: burst-manager
          image: helixcluster/burst-manager:latest
          ports:
            - containerPort: 8080
          env:
            - name: BURST_THRESHOLD
              value: "0.90"
            - name: BURST_THRESHOLD_DURATION
              value: "60s"
            - name: CHUTES_API_KEY
              valueFrom:
                secretKeyRef:
                  name: burst-secrets
                  key: chutes-api-key
            - name: IONET_API_KEY
              valueFrom:
                secretKeyRef:
                  name: burst-secrets
                  key: ionet-api-key
            - name: RUNPOD_API_KEY
              valueFrom:
                secretKeyRef:
                  name: burst-secrets
                  key: runpod-api-key
            - name: AWS_REGION
              value: "us-east-1"
          resources:
            requests:
              cpu: "500m"
              memory: "512Mi"
            limits:
              cpu: "1000m"
              memory: "1Gi"
          volumeMounts:
            - name: config
              mountPath: /etc/helixcluster
      volumes:
        - name: config
          configMap:
            name: burst-manager-config
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: burst-manager-config
  namespace: helixcluster
data:
  burst.yaml: |
    utilization_threshold: 0.90
    threshold_duration: 60s
    cooldown_duration: 300s
    max_concurrent_burst: 100
    cost_budget: 50.00  # $/hour max
    providers:
      - provider: chutes
        weight: 1.0
        max_cost: 2.00
        enabled: true
      - provider: ionet
        weight: 0.8
        max_cost: 1.50
        enabled: true
      - provider: runpod
        weight: 0.6
        max_cost: 3.00
        enabled: true
      - provider: aws-spot
        weight: 0.4
        max_cost: 5.00
        enabled: true
```

### 11.2 Integration with Virtual Kubelet

```yaml
# Virtual Kubelet providers for transparent bursting
apiVersion: apps/v1
kind: Deployment
metadata:
  name: virtual-kubelet-chutes
  namespace: helixcluster
spec:
  replicas: 1
  template:
    spec:
      containers:
        - name: chutes-provider
          image: helixcluster/virtual-kubelet-chutes:latest
          args:
            - --provider=chutes
            - --capacity=1000  # Advertised GPU capacity
            - --node-name=chutes-virtual-node
            - --api-key=$(CHUTES_API_KEY)
          env:
            - name: CHUTES_API_KEY
              valueFrom:
                secretKeyRef:
                  name: burst-secrets
                  key: chutes-api-key
```

### 11.3 KEDA Integration for Queue-Based Spillover

```yaml
# KEDA ScaledObject for burst queue
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: helixcluster-burst-scaler
  namespace: helixcluster
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: inference-worker
  minReplicaCount: 1
  maxReplicaCount: 200
  triggers:
    # Scale on local GPU utilization (from DCGM)
    - type: prometheus
      metadata:
        serverAddress: http://prometheus.monitoring:9090
        metricName: avg_gpu_utilization
        query: 'avg(nvidia_gpu_utilization_gpu{cluster="helixcluster"})'
        threshold: "85"
    # Scale on pending queue depth
    - type: redis
      metadata:
        address: redis.helixcluster.svc:6379
        listName: inference:pending
        listLength: "5"
    # Predictive: scale if forecast shows spike coming
    - type: kedify-predictive
      metadata:
        modelName: gpu-util-forecast
        modelMapeThreshold: "40"
        highMapeDefaultReturnValue: "50"
      targetValue: "80"
  advanced:
    scalingModifiers:
      formula: 'max(gpu_util, queue_depth * 10, predicted_load)'
      target: "80"
```

### 11.4 Helm Chart Values

```yaml
# values.yaml for Helm deployment
burstManager:
  enabled: true
  replicaCount: 2  # HA mode
  
  autoscaling:
    enabled: true
    threshold: 0.90
    thresholdDuration: 60s
    cooldownDuration: 300s
  
  providers:
    chutes:
      enabled: true
      apiKeySecret: chutes-api-key
      weight: 1.0
      maxCost: 2.00
    ioNet:
      enabled: true
      apiKeySecret: ionet-api-key
      weight: 0.8
      maxCost: 1.50
    runPod:
      enabled: true
      apiKeySecret: runpod-api-key
      weight: 0.6
      maxCost: 3.00
    awsSpot:
      enabled: true
      region: us-east-1
      weight: 0.4
      maxCost: 5.00
      instanceFamilies: ["p5", "p4d", "g6e"]
  
  qos:
    realtime:
      maxLatency: 100ms
      providers: [local]
    interactive:
      maxLatency: 500ms
      providers: [local, chutes, runpod]
    batch:
      maxLatency: 30s
      providers: [local, ionet, chutes, aws-spot]
    bestEffort:
      providers: [ionet, chutes, aws-spot, runpod]
  
  monitoring:
    prometheus:
      enabled: true
      scrapeInterval: 5s
    grafana:
      enabled: true
      dashboards:
        - burst-manager-overview
        - cost-analysis
        - provider-health
  
  costOptimization:
    enabled: true
    budget: 50.00  # $/hour
    rebalanceInterval: 5m
    spotDiversification: true
```

### 11.5 API Interface

The Burst Manager exposes a REST API for integration with HelixCluster's scheduler:

```go
// HTTP API handlers
func (m *Manager) RegisterRoutes(r *mux.Router) {
    r.HandleFunc("/v1/burst/status", m.handleStatus).Methods("GET")
    r.HandleFunc("/v1/burst/route", m.handleRoute).Methods("POST")
    r.HandleFunc("/v1/burst/providers", m.handleListProviders).Methods("GET")
    r.HandleFunc("/v1/burst/providers/{provider}/health", m.handleProviderHealth).Methods("GET")
    r.HandleFunc("/v1/burst/jobs", m.handleListJobs).Methods("GET")
    r.HandleFunc("/v1/burst/jobs/{id}/cancel", m.handleCancelJob).Methods("POST")
    r.HandleFunc("/v1/burst/cost", m.handleCostReport).Methods("GET")
    r.HandleFunc("/v1/burst/degradation", m.handleDegradation).Methods("GET", "POST")
    r.HandleFunc("/metrics", promhttp.Handler().ServeHTTP).Methods("GET")
}

// POST /v1/burst/route
// Request body: {"model": "llama-3-70b", "messages": [...], "qos": "interactive"}
// Response: {"provider": "chutes", "job_id": "abc-123", "estimated_cost": 0.05}
```

---

## 12. Key Answers

### Q1: What is the latency of spilling to Chutes vs local GPU?

| Metric | Local GPU (RTX 4090) | Chutes AI | Delta |
|--------|---------------------|-----------|-------|
| Time to First Token | 15-80ms | 100-400ms | +120-500ms |
| Full response (short) | 40-120ms | 200-600ms | +160-480ms |
| Throughput (tok/s) | 18-65 | 50-150 | Cloud higher for long outputs |

Chutes adds ~100-300ms of network overhead but provides competitive throughput for longer generations [^3895^] [^3897^].

### Q2: How fast can we provision a cloud GPU?

| Provider | Cold Start Time | Warm Start |
|----------|----------------|------------|
| Chutes AI | ~0 (always hot) | Instant |
| RunPod Serverless | 5-30s | Instant |
| io.net | 2-5 min | ~30s |
| AWS Spot (Karpenter) | 45-60s VM + 1-2min GPU operator | ~60s |
| AWS Spot (Cluster Autoscaler) | 3-4 min | ~3min |

### Q3: What is the optimal utilization threshold for spilling?

Based on analysis of Netflix's buffer model [^3861^] and Kubernetes autoscaling best practices:

- **Success buffer target:** 80% (normal operations)
- **Burst activation threshold:** 90% (spill to external)
- **Duration requirement:** 60 seconds (prevent flapping)
- **Scale-down threshold:** 63% (70% of 90%, with hysteresis)
- **Failure buffer limit:** 95% (emergency degradation)

### Q4: How to handle preemption of spot instances?

1. **Continuous checkpointing** using CRIU + GPU state serialization [^2971^]
2. **2-minute migration window** on AWS preemption notice
3. **Diversified spot fleet** across instance families (p5, p4d, g6e)
4. **Capacity Rebalancing** for early warning (5-10 min ahead)
5. **Automatic fallback** to on-demand if spot capacity unavailable

### Q5: What is the cost of burst vs always-on?

| Strategy | Monthly Cost (H100 equiv) | Waste | Risk |
|----------|--------------------------|-------|------|
| Always-on (owned) | $876 | 30-50% idle | Hardware failure |
| Karpenter auto-scale | $500-876 | 10-20% idle | 45-60s cold start |
| Burst to Chutes/io.net | $200-500 | Near-zero | Provider availability |
| Burst + Spot | $100-300 | Near-zero | Preemption |
| Predictive + Burst | $150-400 | 5-10% idle | Model inaccuracy |

**Recommendation:** Own local GPUs for baseline (60-70% of capacity) + burst to Chutes/io.net for peaks. This reduces total cost by 40-60% compared to always-on while maintaining <500ms latency for interactive workloads.

---

## References

[^3844^] KEDA - Kubernetes Event-driven Autoscaling. https://keda.sh/

[^3848^] Anil Singh, "KEDA (Kubernetes-based Event Driven Autoscaling)," Medium, 2024-10-03.

[^3841^] Plural, "What is Virtual Kubelet? An In-Depth Explainer," 2026-01-08.

[^3842^] ORCA - Orchestration for Research Cloud Access. https://orcapod.dev/

[^3845^] B.S. Vogler, "A k8s virtual kubelet that runs GPU jobs on RunPod," GitHub, 2025-02-28.

[^3846^] Benedikt Vogler, "Offloading GPU Workloads from Kubernetes to RunPod via Virtual Kubelet," 2025-03-06.

[^3837^] ScaleOps, "Karpenter vs Cluster Autoscaler: 2026 Comparison Guide," 2026-05-14.

[^3838^] Microsoft Q&A, "AKS GPU Node Autoscaling Delay for vLLM LLM Workloads," 2026-05-11.

[^3850^] GitHub Issue #9126, "Configure GPU node scale up delay," kubernetes/autoscaler.

[^3481^] SubnetAlpha, "Chutes (Subnet 64)," 2025-10-16.

[^3629^] Chutes AI Documentation. https://chutes.ai/llms.txt

[^3847^] TypingMind, "Connect and use Kimi K2.5 TEE from Chutes with API Key," 2026-01-27.

[^3859^] RunPod Documentation, "Serverless Pricing." https://docs.runpod.io/serverless/pricing

[^3729^] BuildMVPFast, "RunPod vs Lambda Labs vs CoreWeave: GPU Cloud Pricing," 2026-04-23.

[^3860^] AI Pricing Master, "Self-Hosting AI Models vs API Pricing," 2026-01-26.

[^2500^] nOps, "AWS EC2 Spot Instance Pricing Guide," 2025-09-11.

[^3862^] Tensorfuse, "Selecting Ideal EC2 Instances for GPU Workloads on AWS," 2025-09-05.

[^2971^] Loophole Labs, "Rethinking Spot Instances: Solving Preemption," 2025-09-08.

[^3886^] Cloud Ex Machina, "AWS Spot Instances Explained," 2025-08-19.

[^3861^] AWS re:Invent 2024, "How Netflix handles sudden load spikes in the cloud (NFX301)."

[^3864^] Ujjawal Poudel, "How Netflix Handles Sudden Load Spikes in the Cloud," Medium, 2025-04-07.

[^3865^] AntStack, "How Netflix handles sudden load spikes in the cloud (NFX301)," 2025-03-17.

[^3867^] Zenn.dev, "Netflix on Handling Sudden Cloud Load Spikes," 2024-01-01.

[^3869^] Anshad Ameenza, "Ray in 2024: Scaling AI and ML with Distributed Computing," 2026-02-10.

[^3870^] GitHub Issue #4561, "KubeRay Federation: Multi-Cluster RayCluster Deployment," ray-project/kuberay.

[^3868^] Ray Documentation, "Ray Autoscaler with Kueue."

[^3883^] Microsoft Research, "Coach: Exploiting Temporal Patterns for All-Resource Oversubscription."

[^3879^] OneUptime, "How to Implement Predictive Autoscaling with Kubernetes and ML Models," 2026-02-09.

[^3882^] Kedify, "Predictive Autoscaling for Kubernetes," 2025-10-23.

[^3895^] SitePoint, "Local AI Coding vs Cloud: Performance Analysis 2026," 2026-03-05.

[^3897^] LM-Kit Documentation, "Local vs Cloud: Latency Breakdown."

[^3894^] Local AI Master, "Cloud GPU vs Local Hardware Calculator 2026," 2026-03-16.

[^3898^] io.net, "The Open Source AI Infrastructure Platform." https://io.net/

[^3899^] io.net Blog, "IO vs CoreWeave and alternatives."

[^3892^] AuxilioBits, "Cost Optimization with Spot GPUs on AWS & Azure," 2026-04-24.

[^3893^] JarvisLabs, "NVIDIA H100 Price Guide 2026."

[^3873^] OneUptime, "How to Set Up Kubernetes Federation Across Clusters," 2026-01-19.

[^3305^] Introl, "Kubernetes for GPU Orchestration," 2026-02-23.

[^1077^] CloudOptimo, "Kubernetes AI Infrastructure in 2026," 2026-05-29.

[^3843^] DevZero, "Kubernetes Cluster Autoscaler," 2025-05-23.

[^3840^] OneUptime, "How to Tune Cluster Autoscaler Scale-Down Delay," 2026-02-09.

[^3875^] CoinGecko, "What Is io.net? A Decentralized GPU Network," 2025-09-01.

[^3849^] KeyPointt, "KEDA, Kubernetes based Event Driven Autoscaler," 2024-12-11.

[^3880^] Sneha Iyer, "Predictive Scaling in Kubernetes Using Machine Learning," IJSET.

[^3881^] ICITIES 2025, "Smarter Scaling: Predictive Autoscaling for Kubernetes."

[^3901^] Hugging Face, "Edge vs Cloud GPUs for Inference," 2026-01-19.

[^3896^] Cyfuture, "Cloud vs Local GPU The REAL Cost Comparison," 2026.

[^3872^] Spheron Network, "Kubernetes GPU Orchestration in 2026," 2026-04-14.

[^3874^] Medium, "Kubernetes 1.30 and Beyond: Multi-Cluster Federation," 2025-06-18.

[^3884^] YouTube, "Lightning Talk: Predictive Autoscaling in Kubernetes With KEDA and Prophet," KubeCon 2025.

[^3885^] Shashank Tavildar, "Proactive Kubernetes Scaling: How ML Optimizes HPA," Medium, 2025-01-25.
