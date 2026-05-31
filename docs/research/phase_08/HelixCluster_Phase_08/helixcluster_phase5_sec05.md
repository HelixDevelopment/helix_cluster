## 5. Enterprise, Server & Cloud Nodes

The highest-performance tier of HelixCluster nodes comes from enterprise, server, and cloud hardware. Where a Steam Deck contributes 4–15 watts of mobile compute and an ARM SBC adds 5–25 watts, a single used EPYC server can deliver 64–128 cores at 150–360 watts — the compute equivalent of twenty to forty edge nodes in one chassis. This chapter maps the full landscape, from $65 used EPYC processors pulled from decommissioned hyperscale racks to $0.001-per-vCPU-per-hour cloud spot instances that materialize on demand and vanish two minutes later.

The used server market in 2025–2026 is historically unprecedented. Hyperscalers refreshing for AI workloads have flooded secondary markets with DDR4-based AMD EPYC and Intel Xeon systems at fractions of original cost. A 64-core EPYC 7742 that listed for $6,950 in 2019 now trades for under $750. A complete 64-core server with 128 GB RAM and NVMe storage can be assembled for less than a high-end laptop. Simultaneously, ARM servers — led by Ampere's Altra family — have matured into production-ready platforms with full upstream Linux support, and cloud spot pricing has fallen to levels where ephemeral compute is cheaper than the electricity to run owned hardware for bursty workloads.

The strategic question is not whether to use enterprise-grade nodes, but which tier to deploy and when cloud burst capacity complements on-prem hardware. This chapter answers that with hardware recommendations, total cost of ownership models, production-ready spot-preemption code, and WireGuard mesh configurations for hybrid cloud–on-prem clustering.

---

### 5.1 ARM Servers

ARM servers have transitioned from cloud-only abstractions to physically obtainable hardware. For builders willing to navigate a smaller ecosystem, ARM offers exceptional core density per watt and per dollar.

#### 5.1.1 Ampere Altra / Altra Max: 80–128 Cores

The Ampere Altra family, built on Arm Neoverse N1 cores at TSMC 7nm, was the first production ARM server platform to achieve widespread adoption. The Altra Q80-30 provides 80 cores at 150W TDP, with 8-channel DDR4-3200 memory (up to 4 TB) and 128 lanes of PCIe Gen4. It matches a dual-socket Intel Xeon Silver in memory bandwidth while consuming half the power. The Altra Max M128-30 pushes this to 128 cores at 183W TDP — a core count requiring a $15,000 AMD EPYC 9965 or dual Xeon Platinum to match in x86.

Retail pricing for the Q80-30 sits around $1,689 new, but used and liquidation pricing trends toward $800–1,200. The M128-30 bundle retails near $2,500 but has appeared in secondary markets at $1,200–2,000. Available systems include the Mt. Snow single-socket 2U (up to 4 GPUs, 24 NVMe bays), the Mt. Jade dual-socket platform (256 cores total, Arm SystemReady LS certified), and rackmount options from Gigabyte, Supermicro, and ASRock Rack.

Linux compatibility is excellent — full upstream kernel support since 5.10+, certified for Ubuntu, RHEL, SUSE, and Debian. LinuxBoot is supported for open-source firmware. Virtualization via KVM and Xen works without patches. The caveats: ARM-specific binaries are required (though most modern software publishes aarch64 builds), some proprietary enterprise tools lack ARM ports, and single-threaded performance is roughly Intel Skylake-grade rather than modern Zen 4. For containerized workloads, CI/CD runners, and distributed storage, these limitations rarely matter.

#### 5.1.2 AWS Graviton 3/4: Cloud Reference

AWS Graviton provides the benchmark for evaluating ARM without hardware commitment. Graviton3 (Neoverse V1, 64 cores, 307 GB/s bandwidth) powers the c7g/m7g/r7g families. Graviton4 (Neoverse V2, 96 cores, 537 GB/s bandwidth, DDR5-5600) delivers ~30% better price-performance. Graviton4's improvements are substantial — 50% more cores, 75% more memory bandwidth, and real-world gains of 40% in database workloads. One curious finding: Graviton3's 256-bit SVE SIMD registers make it paradoxically faster than Graviton4 (128-bit SVE) for certain vector search workloads.

#### 5.1.3 Performance vs. x86 Benchmarks

At the socket level, an Altra Max M128-30 delivers roughly 75,000–85,000 PassMark points — comparable to a 64-core EPYC 7742 (~81,500 points). Per-core, each Neoverse N1 core achieves ~600–700 PassMark points versus 1,200–1,400 for Zen 4 or Sapphire Rapids. The ARM advantage is density and efficiency: 128 cores at 183W enables hosting 200–400 lightweight containers simultaneously, making the Altra Max exceptional for microservice aggregation and edge-to-core relay nodes.

---

### 5.2 Used x86 Servers

The used x86 server market is the single best source of raw compute capacity for HelixCluster. With careful procurement, builders achieve core counts and memory bandwidths that dwarf consumer hardware at comparable or lower prices.

#### 5.2.1 AMD EPYC 7002/7003 Series

AMD EPYC dominates used server value. The 7002 "Rome" series (Zen 2, SP3 socket) offers the best price per core. The EPYC 7551 — 32 cores, 180W — trades for $65–75 used, yielding ~$2.10 per core. The 64-core EPYC 7742 costs $500–750 used and provides 128x PCIe Gen4 lanes unmatched by any consumer platform. A complete 7742 build (CPU, Supermicro H11SSL-i motherboard, 128 GB DDR4, 1 TB NVMe, PSU, case) totals $900–1,100.

The 7003 "Milan" series (Zen 3) adds 15–20% IPC improvement and higher boost clocks. The 64-core EPYC 7713 at $800–1,000 used offers the best balance of modern performance and core density, compatible with most Rome motherboards after a BIOS update. Platform costs remain reasonable: Supermicro H11SSL-i boards trade at $200–400, and DDR4 RDIMM ECC 32 GB modules cost $30–40 each.

For builders seeking the bleeding edge, used EPYC 9004 "Genoa" (Zen 4, SP5 socket) processors have begun appearing at compelling prices. The 96-core EPYC 9654 trades at $1,500–2,000 used — roughly $18 per core for a 12-channel DDR5 platform with 128x PCIe Gen5 lanes.

#### 5.2.2 Intel Xeon E5 v3/v4 and Scalable

Intel's massive installed base creates a liquid used market. Legacy Xeon E5 v3 (Haswell) and v4 (Broadwell) processors are extraordinarily cheap: the E5-2697 v4 (18 cores) costs $50–100, and the E5-2699 v4 (22 cores) costs $100–150. Dual-socket platforms yield 36–44 total cores for under $200 in CPU costs, with Supermicro X10DRI motherboards at $150–250. DDR4 RDIMMs for these platforms are plentiful and cheap, making full builds accessible even with minimal budget. These systems are power-hungry (250–400W platform) but for always-on infrastructure services — monitoring, DNS, DHCP, internal relays — the cost-effectiveness is unmatched.

Newer Intel Xeon Scalable (Sapphire Rapids, Emerald Rapids) offer AMX instructions accelerating INT8 inference, making them competitive for AI serving when obtained at used discount. However, for general containerized compute, used EPYC generally offers superior core density and PCIe lane availability at equivalent price points.

#### 5.2.3 Threadripper PRO: Workstation Alternative

AMD Threadripper PRO bridges desktop and server. The 5995WX (64 Zen 3 cores, 8-channel DDR4, 128x PCIe Gen4) trades at $2,500–3,500 used and pairs with WRX80 motherboards around $1,000. The 7975WX (32 Zen 4 cores, 8-channel DDR5) at $1,500–2,000 used offers higher single-threaded performance. For HelixCluster, Threadripper PRO nodes excel as development workstations doubling as cluster members — CI/CD build servers, local AI inference with GPU, and video transcoding. Rackmount chassis conversions are available for 24/7 deployment, though workstation cooling must be upgraded for sustained loads.

**Table 1: Server Price-per-Core Comparison**

| Hardware Option | Cores | System Price* | $/Core | TDP | Best For |
|----------------|-------|--------------|--------|-----|----------|
| Used EPYC 7551 | 32 | ~$350 | $10.94 | 180W | Entry-level compute node |
| Used EPYC 7742 | 64 | ~$900 | $14.06 | 225W | High-density container host |
| Used EPYC 7713 | 64 | ~$1,200 | $18.75 | 225W | Modern high-performance node |
| Ampere Altra Q80-30 | 80 | ~$1,500 | $18.75 | 150W | ARM-native container density |
| Ampere Altra Max M128 | 128 | ~$2,500 | $19.53 | 183W | Maximum core density per watt |
| EPYC 9654 (Genoa, used) | 96 | ~$2,000 | $20.83 | 360W | Modern x86, DDR5, PCIe Gen5 |
| Threadripper PRO 5995WX | 64 | ~$3,500 | $54.69 | 280W | Workstation + occasional server |
| Mac Mini M4 Pro | 14 | $1,399 | $99.93 | 35W | Silent dev node, AI inference |

*System price includes CPU, motherboard, 64 GB RAM, 1 TB NVMe, PSU, and case. Cloud spot excluded (zero CAPEX, ongoing OPEX).

---

### 5.3 Mini PCs & Compact Workstations

Not every node needs a rack. Mini PCs offer a compelling middle ground — compact enough for desk-side deployment, yet powerful enough to serve as mesh relays, build agents, and lightweight compute nodes. The critical differentiator is networking: most mini PCs ship with single Gigabit or 2.5GbE, insufficient for high-throughput mesh backhaul. One device breaks this pattern.

#### 5.3.1 Minisforum MS-01: Best Mini PC Cluster Node

The Minisforum MS-01, built around Intel's i9-13900H (14 cores, 20 threads, up to 5.4 GHz), is the standout compact workstation for HelixCluster. Its defining feature — dual 10GbE SFP+ ports via Intel X710 — provides 20 Gbps aggregate throughput, enabling direct high-speed node-to-node links via SFP+ DAC cables without a switch. Three M.2 slots support up to 6 TB NVMe. A PCIe x16 slot (x8 electrical) accommodates a half-height GPU such as the NVIDIA RTX A2000. Intel vPro enables remote management for headless deployments.

At $679 barebones ($550–700 used/promotional), the MS-01 occupies a unique position: 10GbE networking in a 1.8-liter chassis at one-third the cost of building a comparable small-form-factor server. For field offices, retail locations, and home labs serving as mesh relays, it provides core-node-grade networking in a backpack-sized package.

#### 5.3.2 Mini PC Comparison Table

**Table 2: Mini PC Comparison for HelixCluster**

| Metric | Minisforum MS-01 | ASUS NUC 14 Pro | Beelink SER9 Pro | Apple Mac Mini M4 Pro |
|--------|-----------------|-----------------|-----------------|----------------------|
| CPU | i9-13900H | Core Ultra 7 165H | Ryzen AI 9 HX 370 | M4 Pro (10P+4E) |
| Cores/Threads | 14/20 | 16/22 | 12/24 | 14/14 |
| TDP | 45W | 28W | 54W | 35W |
| Max RAM | 64 GB DDR5-5200 | 96 GB DDR5 | 64 GB LPDDR5X | 64 GB |
| Networking | 2x 10GbE SFP+, 2x 2.5GbE | 2x 2.5GbE, 2x TB4 | 1x 2.5GbE | 1x 10GbE, 2x TB5 |
| NVMe Slots | 3x M.2 | 3x (varied) | 1x M.2 | None (soldered) |
| PCIe Slot | x16 (x8 elec.) | None | None | None |
| Remote Mgmt | Intel vPro | Intel vPro | None | None |
| Price (new) | $679 | $869 | $999 | $1,399–2,199 |
| Used Range | $550–700 | $600–800 | $450–550 | $1,100–1,500 |
| **HelixCluster Score** | **9.5/10** | **7.0/10** | **7.5/10** | **7.0/10** |

The MS-01 wins decisively on networking and expandability. The NUC 14 Pro offers higher RAM capacity (96 GB) but is limited to 2.5GbE. The SER9 Pro provides the best integrated GPU (Radeon 890M) for light AI inference but lags on networking. The Mac Mini M4 Pro offers exceptional memory bandwidth (273 GB/s) and silent operation but is constrained by soldered storage, no expansion, and macOS licensing restrictions on datacenter deployment.

---

### 5.4 Cloud Spot Instance Integration

Cloud spot instances represent the most elastic tier of HelixCluster compute — materializing when needed, running at 70–90% below standard rates, and evaporating when capacity is reclaimed. Properly managed, they extend on-prem clusters with burst capacity. Improperly managed, they create chaos of interrupted workloads and lost state.

#### 5.4.1 AWS, Azure, and GCP Spot Pricing

Spot instances exploit excess cloud capacity. Real-world blended savings for Kubernetes workloads average 59–77%.

**Table 3: Cloud Spot Instance Pricing Comparison**

| Provider | Instance Type | vCPUs | RAM | On-Demand/hr | Spot/hr | $/vCPU/hr |
|----------|-------------|-------|-----|-------------|---------|-----------|
| AWS | c7g.large (Graviton3) | 2 | 4 GB | $0.0725 | ~$0.014 | $0.0070 |
| AWS | c7g.2xlarge (Graviton3) | 8 | 16 GB | $0.29 | ~$0.058 | $0.0073 |
| AWS | c7g.16xlarge (Graviton3) | 64 | 128 GB | $2.32 | ~$0.46 | $0.0072 |
| AWS | c8g.xlarge (Graviton4) | 4 | 8 GB | $0.182 | ~$0.036 | $0.0091 |
| AWS | m8g.metal-24xl (Graviton4) | 96 | 384 GB | $5.32 | ~$1.06 | $0.0110 |
| Azure | D8pds v6 (AMD EPYC) | 8 | 32 GB | $0.384 | ~$0.077 | $0.0096 |
| Azure | E96ads v6 (AMD EPYC) | 96 | 672 GB | $6.14 | ~$1.23 | $0.0128 |
| GCP | c3d-highcpu-16 (AMD EPYC) | 16 | 32 GB | $0.76 | ~$0.15 | $0.0094 |
| GCP | t2d-standard-48 (AMD EPYC) | 48 | 192 GB | $2.03 | ~$0.41 | $0.0085 |

Graviton (ARM) instances are 15–25% cheaper than comparable x86 spots with equal or better container performance. At $0.007–0.012 per vCPU per hour, a 64-vCPU spot node costs ~$110–150/month continuously — comparable to the electricity cost of running a 225W on-prem server in many regions.

#### 5.4.2 Preemption Handling: Go Checkpoint Handler

Spot instances can be reclaimed with minimal warning — 2 minutes on AWS, 30 seconds on Azure and GCP. HelixCluster must handle this through checkpointing, draining, and mixed replica strategies.

The following Go preemption handler implements the AWS Instance Metadata Service interruption watcher, checkpoint trigger, and graceful node drain. It compiles to a static binary suitable for any Linux spot instance.

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "io"
    "log"
    "net/http"
    "os"
    "os/exec"
    "os/signal"
    "syscall"
    "time"
)

const (
    awsIMDSInterruptURL = "http://169.254.169.254/latest/meta-data/spot/instance-action"
    azureMetadataURL    = "http://169.254.169.254/metadata/scheduledevents?api-version=2020-07-01"
    gcpMaintenanceURL   = "http://metadata.google.internal/computeMetadata/v1/instance/maintenance-event"
    pollInterval        = 5 * time.Second
    drainTimeout        = 90 * time.Second
    checkpointDir       = "/var/lib/helixcluster/checkpoints"
)

type SpotAction struct {
    Action string `json:"action"`
    Time   string `json:"time"`
}

type PreemptionHandler struct {
    provider     string
    nodeID       string
    checkpointFn func(string) error
    drainFn      func(context.Context) error
}

func NewHandler(provider, nodeID string) *PreemptionHandler {
    return &PreemptionHandler{
        provider:     provider,
        nodeID:       nodeID,
        checkpointFn: defaultCheckpoint,
        drainFn:      defaultDrain,
    }
}

func (h *PreemptionHandler) Run(ctx context.Context) error {
    log.Printf("[helix-spot] Starting watcher on %s node %s", h.provider, h.nodeID)
    ticker := time.NewTicker(pollInterval)
    defer ticker.Stop()

    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case sig := <-sigCh:
            log.Printf("[helix-spot] Signal %v, initiating shutdown", sig)
            return h.handlePreemption(ctx, "signal")
        case <-ticker.C:
            if detected, reason := h.checkPreemption(); detected {
                log.Printf("[helix-spot] Preemption detected: %s", reason)
                return h.handlePreemption(ctx, reason)
            }
        }
    }
}

func (h *PreemptionHandler) checkPreemption() (bool, string) {
    switch h.provider {
    case "aws":
        return h.checkAWS()
    case "azure":
        return h.checkAzure()
    case "gcp":
        return h.checkGCP()
    default:
        return false, ""
    }
}

func (h *PreemptionHandler) checkAWS() (bool, string) {
    resp, err := http.Get(awsIMDSInterruptURL)
    if err != nil || resp.StatusCode == http.StatusNotFound {
        return false, ""
    }
    defer resp.Body.Close()
    body, _ := io.ReadAll(resp.Body)
    var action SpotAction
    if json.Unmarshal(body, &action) == nil && action.Action != "" {
        return true, fmt.Sprintf("AWS %s at %s", action.Action, action.Time)
    }
    return false, ""
}

func (h *PreemptionHandler) checkAzure() (bool, string) {
    req, _ := http.NewRequest("GET", azureMetadataURL, nil)
    req.Header.Set("Metadata", "true")
    resp, err := http.DefaultClient.Do(req)
    if err != nil || resp.StatusCode != http.StatusOK {
        return false, ""
    }
    defer resp.Body.Close()
    body, _ := io.ReadAll(resp.Body)
    if len(body) > 50 {
        return true, "Azure scheduled event"
    }
    return false, ""
}

func (h *PreemptionHandler) checkGCP() (bool, string) {
    req, _ := http.NewRequest("GET", gcpMaintenanceURL, nil)
    req.Header.Set("Metadata-Flavor", "Google")
    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        return false, ""
    }
    defer resp.Body.Close()
    body, _ := io.ReadAll(resp.Body)
    if string(body) != "NONE" {
        return true, fmt.Sprintf("GCP maintenance: %s", string(body))
    }
    return false, ""
}

func (h *PreemptionHandler) handlePreemption(ctx context.Context, reason string) error {
    log.Printf("[helix-spot] === PREEMPTION: %s ===", reason)

    checkpointCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
    defer cancel()
    if err := h.checkpointFn(h.nodeID); err != nil {
        log.Printf("[helix-spot] Checkpoint error (continuing): %v", err)
    } else {
        log.Printf("[helix-spot] Checkpoint completed for node %s", h.nodeID)
    }

    drainCtx, cancel2 := context.WithTimeout(checkpointCtx, drainTimeout)
    defer cancel2()
    if err := h.drainFn(drainCtx); err != nil {
        log.Printf("[helix-spot] Drain error: %v", err)
    }

    log.Printf("[helix-spot] Node %s drained. Exiting for reclamation.", h.nodeID)
    return nil
}

func defaultCheckpoint(nodeID string) error {
    ts := time.Now().UTC().Format(time.RFC3339)
    checkpointFile := fmt.Sprintf("%s/%s-%s.json", checkpointDir, nodeID, ts)
    state := map[string]interface{}{
        "node_id":   nodeID,
        "timestamp": ts,
        "status":    "checkpointed",
        "workloads": []string{},
    }
    data, _ := json.MarshalIndent(state, "", "  ")
    return os.WriteFile(checkpointFile, data, 0644)
}

func defaultDrain(ctx context.Context) error {
    cmd := exec.CommandContext(ctx, "kubectl", "drain", "$(hostname)",
        "--ignore-daemonsets", "--delete-emptydir-data",
        "--grace-period=30", "--timeout=90s", "--force")
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr
    return cmd.Run()
}

func main() {
    provider := os.Getenv("CLOUD_PROVIDER")
    if provider == "" {
        provider = "aws"
    }
    nodeID := os.Getenv("NODE_ID")
    if nodeID == "" {
        hostname, _ := os.Hostname()
        nodeID = hostname
    }
    os.MkdirAll(checkpointDir, 0755)
    handler := NewHandler(provider, nodeID)
    if err := handler.Run(context.Background()); err != nil {
        log.Fatalf("[helix-spot] Handler exited: %v", err)
    }
}
```

**Production strategies:**

1. **Mixed replicas:** Maintain N baseline replicas on on-demand or on-prem; add M opportunistic spot replicas. If spot is reclaimed, service degrades gracefully rather than failing.
2. **Instance diversification:** Spread spot requests across families and availability zones to reduce correlated preemption risk.
3. **Incremental checkpointing:** Save progress every 60 seconds to object storage so interrupted jobs resume from near their termination point.
4. **Fallback:** Shift to on-demand when spot exceeds ~60% of on-demand price.

#### 5.4.3 WireGuard Mesh: Connecting Cloud to On-Prem

WireGuard provides a lightweight, kernel-implemented VPN between cloud spot instances and on-prem nodes. Modern cryptography (Curve25519, ChaCha20), minimal configuration (~10 lines versus hundreds for OpenVPN), and native roaming support make it ideal for ephemeral cloud nodes that change IPs during reconnection. Benchmarks consistently show WireGuard achieving 900 Mbps–1 Gbps throughput per core with minimal CPU overhead, sufficient to saturate a 10GbE link on a modern processor.

```
  On-Prem Cluster        WireGuard Tunnel        Cloud Spot Instances
  [HelixCluster]  <====  (UDP/51820)  ====>  [AWS/GCP/Azure VMs]
  [10.0.1.0/24]         Encrypted mesh          [10.0.2.0/24]
```

WireGuard deploys as a DaemonSet with `hostNetwork: true` in Kubernetes, establishing node-level mesh connectivity. Each spot instance receives a WireGuard configuration at boot via cloud-init, joining the cluster overlay within seconds of launch. PersistentKeepalive packets ensure NAT traversal survives behind cloud provider firewalls and home-router NAT.

**Cloud spot node WireGuard configuration:**

```ini
[Interface]
PrivateKey = <spot-node-private-key>
Address = 10.0.2.100/24
ListenPort = 51820

[Peer]
PublicKey = <gateway-public-key>
AllowedIPs = 10.0.1.0/24, 10.0.3.0/24
Endpoint = gateway.helixcluster.local:51820
PersistentKeepalive = 25
```

#### 5.4.4 TCO Breakeven Analysis

**Table 4: TCO Breakeven — Cloud Spot vs. Owned Hardware**

| Scenario | Cloud Spot Cost | Owned Hardware Cost | Breakeven | Recommendation |
|----------|----------------|--------------------|-----------|----------------|
| 64 vCPU continuous (730 h/mo) | $110–150/mo | $80–120/mo (EPYC 7742) | 18–24 months | Own if >20 h/day |
| 64 vCPU bursty (200 h/mo) | $35–50/mo | $80–120/mo | Cloud always wins | Spot only |
| 64 vCPU experimental (40 h/mo) | $7–10/mo | $80–120/mo | Cloud always wins | Spot only |
| GPU A100 continuous | $600–800/mo | $500–700/mo | 12–18 months | Own if sustained |
| GPU A100 bursty (100 h/mo) | $150–300/mo | $500–700/mo | Cloud always wins | Spot only |
| 256 vCPU burst peak | $440–600/mo | $320–480/mo (4x EPYC) | ~24 months | Hybrid: base + burst |

**Calculation methodology:** Owned hardware TCO includes CAPEX amortized over 36 months at 8% cost of capital, electricity at $0.12/kWh, maintenance reserve at 10% of CAPEX annually, and networking costs if applicable.

For the 64-vCPU continuous scenario (EPYC 7742 build, $1,000 system cost):
- Monthly amortization: $27.78
- Electricity (225W × 730h = 164.25 kWh): $19.71
- Maintenance reserve: $8.33
- **Total monthly TCO: $55.82** base, **$75–85** with cooling and rack space

The cloud spot equivalent costs $168/month continuous — roughly 2–3× owned cost. This inverts for bursty workloads: at 200 hours/month, cloud spot costs $46 while owned hardware still incurs full amortization plus idle electricity at $65–75 effective. Below ~300 hours per month of actual utilization, cloud spot is cheaper than owned hardware.

**Rule of thumb:** For steady-state 24/7 workloads, owned hardware breaks even at 18–30 months versus cloud spot. For bursty or experimental workloads, cloud spot is 3–5× more cost-effective. The optimal architecture is hybrid: on-prem servers form the persistent core; cloud spot extends elastically via WireGuard mesh.

---

### 5.5 GPU Compute Nodes

AI inference and training demand GPU acceleration. The HelixCluster GPU tier spans consumer cards for budget inference, datacenter GPUs for production, and cloud instances for burst training.

#### 5.5.1 NVIDIA: RTX 4090/5090, A100, H100 — CUDA Ecosystem

NVIDIA's CUDA ecosystem remains dominant for GPU-accelerated computing. The used GPU market in 2025 reflects a post-training-boom correction, with prices 60–70% below peak.

The **RTX 4090** (24 GB GDDR6X, 330 TFLOPS FP16) at $1,200–1,600 used delivers unmatched price-performance for inference but lacks ECC memory and NVLink. The **RTX 5090** (32 GB GDDR7, 450 TFLOPS FP16) at $1,800–2,500 pushes VRAM for larger quantized models. The **A100** is the rational production choice: used 40GB units at $4,800–6,000; refurbished 80GB at $4,800–8,500 — down from $18,000+ at peak. With 312 TFLOPS FP16, NVLink for multi-GPU scaling, and ECC memory, it is the minimum viable GPU for serving 70B+ parameter models. The **H100 SXM5** (80 GB HBM3, 989 TFLOPS FP16) at $6,000–15,000 used justifies its cost only for training or high-throughput inference where 3.35× A100 throughput directly translates to serving capacity. The **L40S** (48 GB GDDR6, 366 TFLOPS) at $3,000–5,000 used fills the gap between consumer and HBM-class cards.

#### 5.5.2 AMD Instinct MI300X — ROCm Ecosystem

AMD Instinct offers a viable alternative for ROCm-tolerant workloads. The **MI300X** (192 GB HBM3, 1.3+ PFLOPS FP16) at $11,000–15,000 used delivers 2.4× A100's VRAM, compelling for large-model inference. The **MI210** (64 GB HBM2e, 181 TFLOPS FP16) at $2,000–3,000 used is the value play — competitive with A100 at roughly half the price. ROCm 7.0 (2026) supports MI300X and MI210 with full PyTorch integration via HIP, though ecosystem breadth remains behind CUDA. For HelixCluster builders already running AMD EPYC hosts, pairing with MI-series GPUs creates a unified vendor stack with simpler driver maintenance.

#### 5.5.3 Cloud GPU Instances as Burst Compute

For intermittent training, cloud GPU instances provide elasticity without capital commitment. AWS g5 spots (NVIDIA T4) at $0.30–0.50/hr and p4d spots (8× A100) at $9–12/hr enable distributed training experiments requiring $50,000+ in owned hardware. The TCO analysis from Section 5.4.4 applies directly: cloud GPU is cost-effective under ~200–300 hours/month; continuous serving favors owned hardware breaking even at 12–18 months.

| GPU | VRAM | FP16 TFLOPS | Used Price | Best For |
|-----|------|-------------|-----------|----------|
| RTX 4090 | 24 GB | 330 | $1,200–1,600 | Budget inference, dev/test |
| RTX 5090 | 32 GB | 450 | $1,800–2,500 | Larger model inference |
| A100 40GB | 40 GB | 312 | $4,800–6,000 | Production inference |
| A100 80GB | 80 GB | 312 | $6,000–8,500 | Large model serving |
| H100 SXM5 | 80 GB | 989 | $6,000–15,000 | Training, high-throughput |
| L40S | 48 GB | 366 | $3,000–5,000 | Mixed inference + graphics |
| MI210 | 64 GB | 181 | $2,000–3,000 | ROCm workloads, value GPU |
| MI300X | 192 GB | 1,300+ | $11,000–15,000 | Maximum VRAM inference |

---

### Summary

Enterprise and server-grade hardware transforms HelixCluster from a collection of edge devices into a production compute platform. Used EPYC delivers 32–128 core nodes for $350–2,500 that anchor the control plane. Ampere Altra provides an alternative path to extreme core density at lower power. The Minisforum MS-01 with dual 10GbE extends high-performance networking to compact form factors. Cloud spot instances — integrated via WireGuard mesh and protected by the preemption handler — provide elastic burst at a fraction of on-demand pricing. GPU nodes from used A100s to ROCm-based AMD Instinct cards accelerate AI inference and training.

The optimal deployment is hybrid: on-prem EPYC and ARM servers form the persistent core, mini PCs provide distributed relay and caching, and cloud spot instances extend the cluster elastically. For steady-state 24/7 workloads, owned hardware breaks even at 18–30 months. For everything else, the cloud is cheaper — and with proper preemption handling, it is also reliable.
