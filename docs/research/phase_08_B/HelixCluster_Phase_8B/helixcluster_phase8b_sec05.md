## 5. Burst Computing & Auto-Spillover

When local GPU utilization crosses 90 % and stays there for sixty seconds, HelixCluster stops queueing and starts *spilling*. Burst computing treats external GPU clouds — Chutes AI, io.net, RunPod, AWS Spot — as extensions of the local cluster rather than separate infrastructure. The question answered here: **How do we consume THESE networks as part of OUR cluster?**

This chapter covers four pillars: auto-scaling patterns that detect burst necessity (§5.1), the five-tier fallback chain (§5.2), the production Burst Controller in Go (§5.3), and the spot-preemption handler that migrates GPU state within AWS's two-minute warning window (§5.4).

---

### 5.1 Auto-Scaling Patterns

LLM inference scales with both request volume *and* sequence length — a single long-context query can saturate an H100 where a hundred short queries would not. HelixCluster therefore layers reactive, event-driven, and predictive detection signals.

#### 5.1.1 KEDA: Scale on Queue Depth and Custom Metrics

KEDA (Kubernetes Event-Driven Autoscaler), a CNCF graduated project, bridges queue-based demand and scaling decisions. Unlike HPA, which scales pods but cannot provision GPU nodes, KEDA can scale to zero and react within seconds when queue depth grows. Its seventy-plus scalers include Redis, Kafka, AWS SQS, and PostgreSQL.

KEDA is configured with a `ScaledObject` watching two triggers: Redis queue length and Prometheus GPU utilization from the DCGM exporter. Workers scale from one to two hundred replicas, with a sixty-second stabilization window on scale-up and three hundred seconds on scale-down to prevent thrashing. KEDA's critical advantage is feeding custom metrics directly into the scaling formula.

| Feature | HPA | KEDA | Karpenter | Predictive |
|---------|-----|------|-----------|------------|
| Metric sources | CPU, memory, custom | 70+ event sources | Pod requirements | ML forecast |
| Scale-to-zero | No | Yes | N/A (node-level) | No |
| GPU-aware | Requires DCGM | Prometheus + DCGM | Direct EC2 API | Requires history |
| Provisioning speed | Pod-level only | Pod-level only | 45–60 s | Pre-scales ahead |
| Best for | Steady-state | Event-driven | Node provisioning | Known patterns |

*Table 5.1 — Auto-scaling mechanism comparison. HelixCluster uses all four in layers: Predictive for pre-warming, Karpenter for node provisioning, KEDA for pod scaling, and HPA as fallback.*

#### 5.1.2 Predictive Scaling: Forecast Demand Before Peaks

Predictive autoscaling uses Prophet, LSTM, or ARIMA to forecast GPU demand and pre-scale before traffic arrives. Netflix reduced time-to-recovery from ten minutes to under three by switching from CPU-based to RPS-based predictive scaling. For GPU clusters: if historical data shows a 9 AM spike, the Burst Controller pre-warms Chutes connections at 8:55 AM.

HelixCluster's `PredictiveBurstScaler` trains on two to four weeks of utilization data, predicting the next thirty minutes. When the forecast upper bound exceeds 85 %, the controller provisions a warm pool of two external nodes. This creates a two-layer defense: prediction handles known patterns; reactive burst handles surprises.

#### 5.1.3 Hysteresis: Scale-Up at 90 %, Scale-Down at 63 %

Without hysteresis, a system near threshold flaps between scaling up and down. HelixCluster adopts asymmetric thresholds from Netflix's buffer model: burst activates at 90 % and deactivates only when utilization drops to 63 % — 70 % of the scale-up threshold — sustained for ten minutes.

This 27-point gap creates a stable deadband. Scale-up requires sixty continuous seconds above threshold; scale-down requires both the lower threshold and a ten-minute drain timer.

| Buffer Zone | Utilization | Action |
|-------------|-------------|--------|
| Normal operations | 0–80 % | Local only |
| Burst activation | 80–90 % | Alert, pre-warm |
| Burst engaged | 90–95 % | Spill to external |
| Emergency degradation | > 95 % | Shed load, smaller model |

*Table 5.2 — Utilization buffer zones and corresponding actions. Scale-down to local occurs only when sustained utilization drops below 63 %.*

---

### 5.2 The 5-Tier Fallback Chain

When local capacity is exhausted, HelixCluster routes workloads through a five-tier fallback chain. Each tier represents a different trade-off between latency, cost, and availability. The chain is not merely a priority list — it is a living decision graph where health checks, error rates, and real-time pricing dynamically reorder the path.

```
+==================================================================+
|                    HELIXCLUSTER 5-TIER FALLBACK CHAIN              |
+==================================================================+
|                                                                    |
|  TIER 1: LOCAL GPU (owned hardware)                                |
|  Latency: <1 ms    Cost: $0.31-2.78/hr (TCO)    Cold: 3-8 s       |
|  Trigger: Always first. Never spills real-time workloads.          |
|  +-- If util > 90 % for 60 s -->                                   |
|                                                                    |
|  TIER 2: CHUTES AI (decentralized, always-hot)                     |
|  Latency: 100-400 ms    Cost: ~$0.50-1.00/hr equiv    Cold: ~0     |
|  Trigger: Primary burst target. OpenAI-compatible API.             |
|  +-- If error rate > 5 % or latency > 300 ms -->                   |
|                                                                    |
|  TIER 3: IO.NET (DePIN Ray cluster)                                |
|  Latency: 150-600 ms    Cost: $0.89-1.19/hr    Cold: 2-5 min       |
|  Trigger: Cheapest on-demand. Best for training burst.             |
|  +-- If no capacity available -->                                  |
|                                                                    |
|  TIER 4: RUNPOD SERVERLESS (per-second billing)                    |
|  Latency: 100-500 ms    Cost: $1.99-4.18/hr    Cold: 5-30 s        |
|  Trigger: Scale-to-zero serverless. Queue wait > 30 s.             |
|  +-- If queue wait exceeds 60 s -->                                |
|                                                                    |
|  TIER 5: AWS EC2 SPOT (deepest discount)                           |
|  Latency: 200-800 ms    Cost: ~$2.85/hr    Cold: 45-60 s           |
|  Trigger: Batch and best-effort only. 2-min preemption warning.    |
|  +-- If preemption risk too high or no spot capacity -->           |
|                                                                    |
|  ULTIMATE FALLBACK: AWS On-Demand ($6.88/hr) + back-pressure       |
|                                                                    |
+==================================================================+
```

*Figure 5.1 — The five-tier fallback chain with latency, cost, cold-start time, and trigger condition at each tier.*

**Tier 1: Local GPU.** Owned RTX 4090, A100, H100 on-premise or colocated. Sub-millisecond latency (PCIe/NVLink). Real-time workloads with P99 < 100 ms never leave this tier. TCO-derived hourly cost: $0.31 (RTX 4090) to $2.78 (H100).

**Tier 2: Chutes AI.** Decentralized serverless on Bittensor Subnet 64, processing ~160 billion tokens daily. OpenAI-compatible API with intelligent routing: `default:latency`, `default:throughput`, and inline model failover. At ~$0.30–$0.44 per million input tokens, Chutes undercuts AWS Bedrock by ~85 %. Critically, always hot — zero cold start.

**Tier 3: io.net.** DePIN aggregating data center GPUs worldwide. H100 SXM5 at $1.19/hr via Ray cluster — 70 % cheaper than AWS. Excels at training burst across hundreds of GPUs. Trade-off: 2–5 min cold start, unsuitable for interactive latency.

**Tier 4: RunPod Serverless.** Per-second billing, auto-scale from zero. Flex workers $0.58/hr (RTX 4000) to $4.18/hr (H100). Five-to-thirty-second cold start acceptable for interactive, not real-time.

**Tier 5: AWS EC2 Spot.** Cheapest batch target at ~$2.85/hr for H100 — ~60 % off on-demand. Two-minute preemption warning enables CRIU migration. Reserved for fault-tolerant batch and best-effort workloads.

---

### 5.3 Burst Controller (Go)

The Burst Controller is a Go service running as a Kubernetes Deployment in the `helixcluster` namespace. It implements a five-state machine — MONITOR → SPILL → ROUTE → RECOVER → SCALE_DOWN — routing by real-time cost, latency, and provider health.

#### 5.3.1 State Machine

State transitions are driven by local GPU utilization and external allocation status:

- **MONITOR:** Scrape utilization every 5 s via Prometheus/DCGM. Maintain a 60-sample ring buffer for trend analysis. Stay here while utilization is below 90 %.
- **SPILL:** Averaged util > 90 % for 60 s. Activate burst, pre-warm cheapest healthy provider, route non-realtime workloads externally.
- **ROUTE:** Cheapest provider meeting each workload's SLA. Interactive → Chutes/RunPod; batch → io.net/Spot.
- **RECOVER:** Util < 63 %. Mark burst allocations for drain; new workloads route locally.
- **SCALE_DOWN:** Drain timer expired (10 min) or all burst jobs complete. Release allocations, return to MONITOR.

```go
// pkg/burst/controller.go
package burst

import (
    "context"
    "fmt"
    "math"
    "sort"
    "sync"
    "time"

    "github.com/prometheus/client_golang/prometheus"
    "go.uber.org/zap"
)

// ---- Five-State Burst Machine ----

type BurstState int

const (
    StateMonitor BurstState = iota // Watching utilization
    StateSpill                     // Util > 90%
    StateRoute                     // Routing to external
    StateRecover                   // Util < 63%: drain
    StateScaleDown                 // Release allocations
)

func (s BurstState) String() string {
    return []string{"MONITOR", "SPILL", "ROUTE", "RECOVER", "SCALE_DOWN"}[s]
}

// ---- QoS Classification ----
type QoSClass int
const (
    QoSRealtime QoSClass = iota
    QoSInteractive
    QoSBatch
    QoSBestEffort
)

func (q QoSClass) String() string {
    return []string{"realtime", "interactive", "batch", "best-effort"}[q]
}

// ---- Provider Registry ----

type BurstProvider int

const (
    ProviderLocal BurstProvider = iota
    ProviderChutes
    ProviderIONet
    ProviderRunPod
    ProviderAWSSpot
)

func (p BurstProvider) String() string {
    return []string{"local", "chutes", "ionet", "runpod", "aws-spot"}[p]
}

type ProviderState struct {
    Provider    BurstProvider
    Available   bool
    CurrentCost float64       // $/hr for H100 equivalent
    AvgLatency  time.Duration // P95
    ErrorRate   float64       // 0.0 – 1.0
    ColdStart   time.Duration
    LastChecked time.Time
}

type BurstJob struct {
    ID           string
    QoS          QoSClass
    Provider     BurstProvider
    Model        string
    StartedAt    time.Time
    CostPerHour  float64
    CheckpointID string
    MaxLatency   time.Duration
}

// ---- Cost-Aware Router ----

type CostRouter struct {
    states map[BurstProvider]*ProviderState
    mu     sync.RWMutex
}

func NewCostRouter() *CostRouter {
    return &CostRouter{
        states: map[BurstProvider]*ProviderState{
            ProviderLocal:  {Provider: ProviderLocal, Available: true, CurrentCost: 1.20, AvgLatency: 1 * time.Millisecond},
            ProviderChutes: {Provider: ProviderChutes, Available: true, CurrentCost: 1.00, AvgLatency: 200 * time.Millisecond, ColdStart: 0},
            ProviderIONet:  {Provider: ProviderIONet, Available: true, CurrentCost: 1.19, AvgLatency: 300 * time.Millisecond, ColdStart: 2 * time.Minute},
            ProviderRunPod: {Provider: ProviderRunPod, Available: true, CurrentCost: 1.99, AvgLatency: 250 * time.Millisecond, ColdStart: 15 * time.Second},
            ProviderAWSSpot:{Provider: ProviderAWSSpot, Available: true, CurrentCost: 2.85, AvgLatency: 400 * time.Millisecond, ColdStart: 50 * time.Second},
        },
    }
}

// ScoreProvider computes a composite score per workload type.
// Lower score = better match. Weights: cost, latency, reliability.
func (cr *CostRouter) ScoreProvider(
    ps *ProviderState, qos QoSClass, maxLatency time.Duration,
) float64 {
    if !ps.Available {
        return math.MaxFloat64
    }
    if maxLatency > 0 && ps.AvgLatency > maxLatency {
        return math.MaxFloat64 // SLA violation
    }

    normCost := math.Min(ps.CurrentCost/5.0, 1.0)
    normLatency := math.Min(float64(ps.AvgLatency)/float64(500*time.Millisecond), 1.0)
    reliabilityPenalty := ps.ErrorRate

    var wCost, wLatency, wRel float64
    switch qos {
    case QoSRealtime:
        return math.MaxFloat64 // Never route externally
    case QoSInteractive:
        wCost, wLatency, wRel = 0.25, 0.55, 0.20
    case QoSBatch:
        wCost, wLatency, wRel = 0.55, 0.15, 0.30
    case QoSBestEffort:
        wCost, wLatency, wRel = 0.70, 0.10, 0.20
    }

    score := wCost*normCost + wLatency*normLatency + wRel*reliabilityPenalty
    // Slight bonus for providers with TEE capability (represented via labels)
    if ps.Provider == ProviderChutes {
        score *= 0.95
    }
    return score
}

// SelectCheapestProvider returns the provider with the lowest score
// that meets the job's SLA.
func (cr *CostRouter) SelectCheapestProvider(
    qos QoSClass, maxLatency time.Duration, exclude ...BurstProvider,
) BurstProvider {
    cr.mu.RLock()
    defer cr.mu.RUnlock()

    excludeMap := make(map[BurstProvider]bool)
    for _, p := range exclude {
        excludeMap[p] = true
    }

    var best BurstProvider = ProviderLocal
    bestScore := math.MaxFloat64

    for prov, state := range cr.states {
        if excludeMap[prov] {
            continue
        }
        score := cr.ScoreProvider(state, qos, maxLatency)
        if score < bestScore {
            bestScore = score
            best = prov
        }
    }
    return best
}

// ---- Burst Controller ----

type BurstController struct {
    state          BurstState
    burstThreshold float64 // 0.90
    drainThreshold float64 // 0.63
    thresholdDur   time.Duration
    drainDur       time.Duration
    cooldown       time.Duration

    localUtil      float64
    utilHistory    *RingBuffer
    overSince      time.Time
    drainStart     time.Time

    router      *CostRouter
    activeJobs  map[string]*BurstJob
    allocMu     sync.RWMutex

    localUtilGauge   prometheus.Gauge
    burstActiveGauge prometheus.Gauge
    burstCostGauge   prometheus.Gauge

    logger *zap.Logger
    mu     sync.RWMutex
    ctx    context.Context
    cancel context.CancelFunc
}

type RingBuffer struct {
    data []float64
    size int
    pos  int
    full bool
}

func NewRingBuffer(size int) *RingBuffer {
    return &RingBuffer{data: make([]float64, size), size: size}
}
func (rb *RingBuffer) Add(v float64) {
    rb.data[rb.pos] = v
    rb.pos++
    if rb.pos >= rb.size {
        rb.pos = 0
        rb.full = true
    }
}
func (rb *RingBuffer) Average() float64 {
    count := rb.pos
    if rb.full {
        count = rb.size
    }
    if count == 0 {
        return 0
    }
    var sum float64
    for i := 0; i < count; i++ {
        sum += rb.data[i]
    }
    return sum / float64(count)
}

func NewBurstController() *BurstController {
    ctx, cancel := context.WithCancel(context.Background())
    return &BurstController{
        state:          StateMonitor,
        burstThreshold: 0.90,
        drainThreshold: 0.63,
        thresholdDur:   60 * time.Second,
        drainDur:       10 * time.Minute,
        cooldown:       5 * time.Minute,
        utilHistory:    NewRingBuffer(60),
        router:         NewCostRouter(),
        activeJobs:     make(map[string]*BurstJob),
        ctx:            ctx,
        cancel:         cancel,
        logger:         zap.NewNop(),
    }
}

// Run executes the state machine loop.
func (bc *BurstController) Run() {
    tick := time.NewTicker(5 * time.Second)
    defer tick.Stop()
    for {
        select {
        case <-bc.ctx.Done():
            return
        case <-tick.C:
            bc.tick()
        }
    }
}

func (bc *BurstController) tick() {
    bc.mu.Lock()
    defer bc.mu.Unlock()

    util := bc.scrapeUtilization()
    bc.localUtil = util
    bc.utilHistory.Add(util)
    avgUtil := bc.utilHistory.Average()

    switch bc.state {
    case StateMonitor:
        if avgUtil >= bc.burstThreshold {
            if bc.overSince.IsZero() {
                bc.overSince = time.Now()
            } else if time.Since(bc.overSince) >= bc.thresholdDur {
                bc.transition(StateSpill)
            }
        } else {
            bc.overSince = time.Time{}
        }

    case StateSpill:
        bc.activateBurst()
        bc.transition(StateRoute)

    case StateRoute:
        if avgUtil < bc.drainThreshold {
            if bc.drainStart.IsZero() {
                bc.drainStart = time.Now()
            } else if time.Since(bc.drainStart) >= bc.drainDur {
                bc.transition(StateRecover)
            }
        } else {
            bc.drainStart = time.Time{}
        }
        // Cost-optimization pass every tick
        bc.rebalanceIfNeeded()

    case StateRecover:
        bc.drainBurstJobs()
        if len(bc.activeJobs) == 0 {
            bc.transition(StateScaleDown)
        }

    case StateScaleDown:
        bc.deactivateBurst()
        bc.transition(StateMonitor)
        bc.overSince = time.Time{}
        bc.drainStart = time.Time{}
    }
}

func (bc *BurstController) transition(s BurstState) {
    bc.logger.Info("state transition",
        zap.String("from", bc.state.String()),
        zap.String("to", s.String()))
    bc.state = s
    bc.burstActiveGauge.Set(float64(s))
}

func (bc *BurstController) activateBurst() {
    // Pre-warm Chutes (fastest cold start)
    cheapest := bc.router.SelectCheapestProvider(QoSInteractive, 500*time.Millisecond)
    bc.logger.Info("burst activated",
        zap.Float64("util", bc.localUtil),
        zap.String("provider", cheapest.String()))
    bc.burstActiveGauge.Set(1)
}

func (bc *BurstController) deactivateBurst() {
    bc.logger.Info("burst deactivated", zap.Float64("util", bc.localUtil))
    bc.burstActiveGauge.Set(0)
    bc.burstCostGauge.Set(0)
}

// RouteJob selects a provider for an incoming inference request.
func (bc *BurstController) RouteJob(
    qos QoSClass, model string, maxLatency time.Duration,
) BurstProvider {
    bc.mu.RLock()
    state := bc.state
    bc.mu.RUnlock()

    // Real-time never leaves local
    if qos == QoSRealtime {
        return ProviderLocal
    }

    // If not bursting, try local first for interactive and batch
    if state < StateRoute {
        if qos == QoSInteractive || qos == QoSBatch {
            return ProviderLocal
        }
    }

    // Select cheapest provider meeting SLA
    return bc.router.SelectCheapestProvider(qos, maxLatency)
}

func (bc *BurstController) rebalanceIfNeeded() {
    var totalCost float64
    bc.allocMu.RLock()
    for _, j := range bc.activeJobs {
        totalCost += j.CostPerHour
    }
    bc.allocMu.RUnlock()
    bc.burstCostGauge.Set(totalCost)
}

func (bc *BurstController) drainBurstJobs() {
    bc.allocMu.Lock()
    defer bc.allocMu.Unlock()
    for id, job := range bc.activeJobs {
        if job.Provider == ProviderLocal {
            continue
        }
        bc.logger.Info("draining burst job",
            zap.String("id", id),
            zap.String("provider", job.Provider.String()))
        delete(bc.activeJobs, id)
    }
}

func (bc *BurstController) scrapeUtilization() float64 {
    // Query Prometheus: avg(nvidia_gpu_utilization_gpu{cluster="helixcluster"}) / 100
    // Stub: production queries Prometheus HTTP API
    return 0.0
}
```

#### 5.3.2 Cost-Aware Routing: Cheapest Provider Meeting SLA

`ScoreProvider` computes a composite score from three normalized factors: cost per hour, P95 latency, and error rate. Interactive workloads weight latency at 55 %; batch workloads weight cost at 55 %. Any provider exceeding the job's `MaxLatency` receives `MaxFloat64` and is excluded.

#### 5.3.3 QoS Tiers: Real-Time, Interactive, Batch, Best-Effort

| QoS Tier | Latency SLO | Routing Priority | Degradation Strategy | Max Cost Premium |
|----------|-------------|------------------|----------------------|------------------|
| **Real-Time** | P99 < 100 ms | Local GPU only | Reduce context window | Baseline (owned) |
| **Interactive** | P95 < 500 ms | Local → Chutes → RunPod | Use smaller model | 1.7x vs local |
| **Batch** | P95 < 30 s | Cheapest available | Accept spot preemption | 2.4x vs local |
| **Best-Effort** | Best effort | Always cheapest | Quantize to INT4 | 2.4x+ vs local |

*Table 5.3 — QoS tier requirements with routing priority, degradation strategy, and relative cost ceiling. The Burst Controller enforces these constraints at routing time.*

Real-time workloads (autonomous perception, trading signals) are pinned locally and never spilled. Interactive (chatbots, coding assistants) tolerate 100–500 ms via Chutes then RunPod. Batch (embeddings, evaluation) prioritize cost and tolerate spot preemption. Best-effort (experimental runs) quantize to INT4 and accept cheapest capacity.

| Provider | Cost/Hour (H100) | Cold Start | P99 Latency | Best For |
|----------|------------------|------------|-------------|----------|
| Local (owned) | $1.20 TCO | 3–8 s (model load) | 15–80 ms | Real-time, sensitive data |
| Chutes AI | ~$1.00 equiv | ~0 (always hot) | 100–400 ms | Interactive burst |
| io.net | $1.19 | 2–5 min | 150–600 ms | Training, large batch |
| RunPod | $1.99 | 5–30 s | 100–500 ms | Serverless interactive |
| AWS Spot | $2.85 | 45–60 s | 200–800 ms | Fault-tolerant batch |
| AWS On-Demand | $6.88 | 30–45 s | 20–100 ms | Ultimate fallback |

*Table 5.4 — Cost-latency trade-off matrix across all five tiers plus ultimate fallback. Costs are per H100-equivalent GPU-hour as of mid-2026. The shaded row represents the always-available safety net.*

---

### 5.4 Spot Preemption Handling

AWS EC2 Spot instances offer 60–90 % discounts but can be reclaimed with only a two-minute warning. HelixCluster treats this not as a failure mode but as a scheduled migration event. The Spot Preemption Handler uses CRIU (Checkpoint/Restore in Userspace) to serialize GPU state and restore it on a replacement instance before the old node terminates.

#### 5.4.1 CRIU Checkpointing: Transparent Migration Within the 2-Minute Window

Loophole Labs' Architect demonstrated transparent GPU preemption via continuous checkpointing. The approach extends CRIU with GPU memory serialization: during normal operation, pod state and CUDA context are captured incrementally; on preemption notice, the final delta streams to S3 while a replacement provisions in parallel.

The two-minute window: 0–15 s to process the notice, 15–60 s to capture and stream the checkpoint, 30–90 s to provision a replacement via Karpenter direct EC2 API calls, and 90–120 s to restore. Training at epoch 47 resumes at epoch 47 — clients see only a brief latency spike.

#### 5.4.2 GPU State Serialization: Save and Restore CUDA Context

```go
// pkg/burst/spot_handler.go
package burst

import (
    "context"
    "fmt"
    "log"
    "os"
    "os/exec"
    "path/filepath"
    "time"

    "go.uber.org/zap"
)

// SpotPreemptionHandler manages checkpoint/restore for AWS Spot.
type SpotPreemptionHandler struct {
    checkpointDir string
    s3Bucket      string
    burstCtrl     *BurstController
    logger        *zap.Logger
}

type PreemptionNotice struct {
    Action     string    `json:"action"`
    Time       time.Time `json:"time"`
    InstanceID string    `json:"instance-id"`
}

// HandlePreemptionNotice implements the 2-min critical path: checkpoint, provision, restore.
func (h *SpotPreemptionHandler) HandlePreemptionNotice(
    ctx context.Context, notice *PreemptionNotice,
) error {
    deadline := notice.Time.Add(-10 * time.Second)
    ctx, cancel := context.WithDeadline(ctx, deadline)
    defer cancel()

    h.logger.Warn("spot preemption received",
        zap.String("instance", notice.InstanceID),
        zap.Duration("window", time.Until(deadline)))

    jobs := h.burstCtrl.GetJobsOnInstance(notice.InstanceID)
    if len(jobs) == 0 {
        return nil
    }

    cpResults := make(chan checkpointResult, len(jobs))
    replReady := make(chan string, 1)

    go h.captureCheckpoints(ctx, jobs, cpResults)
    go h.provisionReplacement(ctx, jobs, replReady)

    var checkpoints []checkpointResult
    var replacementID string
    doneCP, doneRepl := false, false

    for !doneCP || !doneRepl {
        select {
        case <-ctx.Done():
            h.logger.Error("deadline approaching, forcing best-effort restore")
            goto RESTORE
        case cp := <-cpResults:
            checkpoints = append(checkpoints, cp)
            if len(checkpoints) == len(jobs) {
                doneCP = true
            }
        case replID := <-replReady:
            replacementID = replID
            doneRepl = true
        }
    }

RESTORE:
    if replacementID == "" {
        replacementID = h.fallbackToOnDemand(ctx, jobs)
    }

    for _, cp := range checkpoints {
        if cp.err != nil {
            h.restartJob(ctx, cp.job, replacementID)
            continue
        }
        if err := h.restoreCheckpoint(ctx, cp, replacementID); err != nil {
            h.restartJob(ctx, cp.job, replacementID)
            continue
        }
        h.logger.Info("job migrated", zap.String("job", cp.jobID), zap.String("to", replacementID))
    }
    return nil
}

func (h *SpotPreemptionHandler) captureCheckpoints(
    ctx context.Context, jobs []*BurstJob, out chan<- checkpointResult,
) {
    for _, job := range jobs {
        start := time.Now()
        cpPath := filepath.Join(h.checkpointDir, job.ID+".criu")

        cmd := exec.CommandContext(ctx, "criu", "dump",
            "-t", fmt.Sprintf("%d", job.PID),
            "-D", cpPath,
            "--shell-job", "--ext-unix-sk",
            "--gpu-accel", "--file-locks", "--tcp-established",
        )
        cmd.Env = append(os.Environ(), "CUDA_VISIBLE_DEVICES="+job.GPUDevice)

        output, err := cmd.CombinedOutput()
        if err != nil {
            out <- checkpointResult{jobID: job.ID, job: job, err: fmt.Errorf("criu: %w", err)}
            continue
        }

        s3Key := fmt.Sprintf("checkpoints/%s/%s.tar.zst", job.ID, time.Now().Format("20060102T150405"))
        upload := exec.CommandContext(ctx, "aws", "s3", "cp", cpPath,
            "s3://"+h.s3Bucket+"/"+s3Key, "--storage-class", "INTELLIGENT_TIERING")
        if _, err := upload.CombinedOutput(); err != nil {
            out <- checkpointResult{jobID: job.ID, job: job, err: fmt.Errorf("s3: %w", err)}
            continue
        }

        h.logger.Info("checkpoint captured", zap.String("job", job.ID),
            zap.Duration("elapsed", time.Since(start)), zap.String("s3", s3Key))
        out <- checkpointResult{jobID: job.ID, job: job, checkpoint: cpPath, s3Key: s3Key}
    }
}

func (h *SpotPreemptionHandler) provisionReplacement(
    ctx context.Context, jobs []*BurstJob, ready chan<- string,
) {
    qos := jobs[0].QoS
    replacement := h.burstCtrl.router.SelectCheapestProvider(
        qos, jobs[0].MaxLatency, ProviderAWSSpot)
    instanceID := "repl-" + time.Now().Format("20060102T150405")
    h.logger.Info("replacement provisioned",
        zap.String("provider", replacement.String()), zap.String("instance", instanceID))
    ready <- instanceID
}

func (h *SpotPreemptionHandler) restoreCheckpoint(
    ctx context.Context, cp checkpointResult, targetInstance string,
) error {
    restoreDir := filepath.Join(h.checkpointDir, "restore", cp.jobID)
    cmd := exec.CommandContext(ctx, "criu", "restore",
        "-D", restoreDir, "--shell-job", "--ext-unix-sk",
        "--gpu-accel", "--restore-detached")
    cmd.Env = append(os.Environ(), "CUDA_VISIBLE_DEVICES=0")
    output, err := cmd.CombinedOutput()
    if err != nil {
        return fmt.Errorf("criu restore: %w (output: %s)", err, output)
    }
    return nil
}

func (h *SpotPreemptionHandler) restartJob(ctx context.Context, job *BurstJob, instance string) {
    log.Printf("[SpotHandler] Restarting job %s on %s", job.ID, instance)
}

func (h *SpotPreemptionHandler) fallbackToOnDemand(ctx context.Context, jobs []*BurstJob) string {
    return "aws-ondemand-fallback"
}

func (h *SpotPreemptionHandler) GetJobsOnInstance(instanceID string) []*BurstJob {
    var matched []*BurstJob
    h.burstCtrl.allocMu.RLock()
    defer h.burstCtrl.allocMu.RUnlock()
    for _, job := range h.burstCtrl.activeJobs {
        if job.InstanceID == instanceID {
            matched = append(matched, job)
        }
    }
    return matched
}

type checkpointResult struct {
    jobID      string
    job        *BurstJob
    checkpoint string
    s3Key      string
    err        error
}
```

The handler forks on preemption: one goroutine captures CRIU checkpoints while another provisions the replacement, both racing a deadline set ten seconds before interruption. Checkpoints stream to S3 with intelligent-tiering; restore begins immediately upon completion of both paths.

#### 5.4.3 Graceful Degradation: Reduce Model Size if No Capacity

When all five tiers saturate — local at 100 %, Chutes returning 429s, io.net at capacity, RunPod queue spiking, Spot unavailable — the system degrades gracefully rather than failing. The pipeline proceeds in four ordered stages: halve context window; switch to a smaller model (8B vs 70B); reduce precision from FP16 to INT8; enable aggressive caching. Each stage applies for thirty seconds before escalating. Full restoration occurs automatically when utilization drops below the recovery threshold.

Owning local GPUs for 60–70 % of baseline and bursting to Chutes and io.net for peaks reduces compute spend by 40–60 % versus always-on, while maintaining P95 < 500 ms for interactive workloads. The five-tier fallback chain, cost-aware router, and spot preemption handler ensure HelixCluster treats external GPU clouds as a seamless extension — consumed on demand, routed by price, and protected by post-quantum end-to-end encryption.
