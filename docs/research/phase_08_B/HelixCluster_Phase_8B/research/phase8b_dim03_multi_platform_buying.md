# Phase 8B / Dim 03: Multi-Platform GPU Compute Buying Strategy

## Unified GPU Compute Procurement: HelixCluster Buying Framework

**Date:** 2026-06-17
**Research Depth:** 20+ independent searches across 10 platforms
**Focus:** Consuming external GPU compute as part of HelixCluster -- not participating in networks, but making them serve us

---

## Executive Summary

The GPU compute market in mid-2026 exhibits extreme price dispersion -- a factor of **23x** between the cheapest and most expensive H100 options [^2465^]. For HelixCluster, this creates a massive arbitrage opportunity. A unified buying manager can reduce compute costs by **50-70%** versus single-provider strategies while improving availability through multi-source failover.

**Key Finding:** The cheapest on-demand H100 is $1.38/hr (Thunder Compute) while AWS charges $6.88+/hr for the same GPU [^2465^]. The cheapest spot H100 is $1.03/hr (Spheron) [^3728^]. For inference APIs, open-weight models via Together AI cost **92% less** than frontier models with comparable quality [^3774^].

**Strategic Recommendation:** Build a unified `ComputeBroker` that routes workloads across 6-8 providers based on real-time pricing, latency requirements, and workload characteristics. This system should prioritize: (1) serverless APIs for bursty inference, (2) spot instances for training/fine-tuning, (3) reserved capacity for predictable production workloads.

---

## 1. Platform Deep Dives (Buyer Perspective)

### 1.1 Chutes.ai -- Decentralized Serverless Inference

**What it is:** Chutes.ai is a decentralized, peer-to-peer AI inference network. Miners host open-source models; buyers consume via API. Chutes offers TEE (Trusted Execution Environment) for privacy-sensitive workloads [^3761^].

**Pricing Model:**
- **Public Chutes (inference):** Pay-per-request via subscription + PAYG tiers
  - Plus: $10/month (5x value multiplier, 6% off PAYG)
  - Pro: $20/month (5x value multiplier, 10% off PAYG)
  - Enterprise: Custom pricing [^3708^]
- **Private Chutes (dedicated):** Billed by GPU-hour + one-time deployment fee (3x hourly rate)
  - RTX Pro 6000 (96GB): $1.80/hr, deployment fee $5.40 [^3761^]
  - No cold-start charges; only pay while instance is hot
  - `shutdown_after_seconds` default: 300s (configurable)
- **Free tier:** Community-powered inference with rate limits [^3775^]

**Available Models:** Llama 3.1 70B, DeepSeek-R1, Qwen 2.5 72B, embedding models, content moderation, 3D generation, plus BYOM (bring your own model) [^3708^]

**API Access:** OpenAI-compatible API, `chutes` CLI for deployment, Python SDK. RESTful endpoints with bearer auth [^3553^].

**Geographic Distribution:** Decentralized -- miners globally, routed to nearest node. TEE models run in isolated secure environments [^3553^].

**HelixCluster Integration Angle:** Chutes is ideal for **privacy-sensitive inference workloads** via TEE endpoints. Use as a secondary inference provider with automatic failover. The OpenAI-compatible API means zero client-side changes.

---

### 1.2 io.net -- Decentralized GPU Cloud (DePIN)

**What it is:** io.net is a decentralized GPU network built on Solana, aggregating 30,000+ GPUs from 5,000+ independent providers. Offers containerized deployment, Ray-native clusters, and OpenAI-compatible inference API [^3778^].

**Pricing Model (per GPU-hour):**

| GPU | io.net Price | AWS Equivalent | Savings |
|-----|-------------|----------------|---------|
| H100 SXM | $1.49-2.20/hr | $6.88/hr | 68-79% |
| A100 80GB | $2.30/hr | $4.55/hr | 49% |
| A100 40GB | $1.40/hr | $3.06/hr | 54% |
| RTX 4090 | $0.28/hr | N/A | N/A |
| H200 | Competitive | $4.98/hr | ~50% |
| B200 | $4.99/hr | $14.24/hr | 65% |

**Deployment Options:**
- **IO Cloud:** Container-as-a-Service (CaaS) with Docker support
- **IO Clusters:** Ray-native multi-GPU clusters with auto-scaling
- **IO Intelligence:** OpenAI-compatible inference API for 15+ models
- **VM-as-a-Service:** Full VMs with SSH access [^3503^]

**API Access:** REST API for container deploy/destroy, VM provisioning, real-time logs. Python SDK. CLI tool. OpenAI-compatible inference API at `api.intelligence.io.solutions` [^3503^].

**Geographic Availability:** 138+ countries, 2,752+ verified GPUs. Providers range from data centers to individual miners [^3794^].

**Unique Strengths:** Bare-metal access (no virtualization overhead), zkTFLOPs verification, consumer GPU availability, cheapest H100 rates for on-demand [^3778^].

**HelixCluster Integration Angle:** io.net is the **primary compute layer for cost-sensitive training workloads**. Deploy Ray clusters programmatically via API. Use RTX 4090s ($0.28/hr) for development/prototyping. Use H100s ($1.49/hr) for production training. The 5-10% performance gain from bare-metal over AWS virtualization is a free bonus [^3778^].

---

### 1.3 Akash Network -- Decentralized Compute Marketplace

**What it is:** Akash is a decentralized marketplace where providers bid for your workload. You specify requirements via SDL (Stack Definition Language); providers compete on price. 1,000+ GPUs across 65+ data centers [^3528^].

**Pricing Model:** Reverse auction -- providers bid DOWN for your workload.

| GPU | Akash Price Range | AWS Equivalent | Savings |
|-----|------------------|----------------|---------|
| RTX 4090 | $0.50-1.50/hr | N/A | N/A |
| A100 40GB | $1.50-2.50/hr | $4.10/hr | 50-65% |
| A100 80GB | $2.50-3.50/hr | $4.55/hr | 45% |
| H100 | $2.50-4.00/hr | $8.03/hr | 50-70% |
| H200 | $2.82/hr (seen) | $4.98/hr | 43% |

**Deployment Process:**
1. Write SDL (YAML configuration specifying GPU, RAM, storage)
2. Submit deployment to Akash blockchain
3. Providers submit bids
4. Accept lowest bid
5. Provider deploys workload

```yaml
# Example Akash SDL for GPU workload
gpu:
  units: 1
  attributes:
    vendor:
      nvidia:
        - model: a100
          ram: 80Gi
```

**API Access:** Go SDK and JavaScript/TypeScript SDK generated from protobuf definitions. Akash CLI. Console web UI. Programmatic deployment, bid management, lease creation [^3776^].

**Cost Arbitrage Opportunity:** Prices vary significantly by provider. A100 80GB ranges from $2.50-3.50/hr depending on the bid. The reverse auction model naturally drives prices down during low-demand periods.

**HelixCluster Integration Angle:** Akash is the **arbitrage engine for long-running training jobs**. Deploy workloads via SDL, accept the lowest bid. Use for fault-tolerant workloads with checkpointing (spot-like pricing without spot interruption risk). The $100 free credit for new users enables cost-free testing [^3528^].

---

### 1.4 RunPod -- Serverless GPU + Cloud Pods

**What it is:** RunPod offers both dedicated GPU pods (full SSH access) and serverless GPU endpoints (scale-to-zero). $120M ARR, per-second billing, SOC 2 certified [^3731^].

**Pricing Model:**

**Serverless (Flex Workers):**
| GPU | Per-Hour (Flex) | Per-Second | Cold Start |
|-----|----------------|------------|------------|
| RTX 4000 | $1.12/hr | $0.000311/sec | <200ms (FlashBoot) |
| A100 80GB | $3.49/hr | $0.000969/sec | 5-25s |
| H100 | $6.99/hr | $0.001942/sec | 5-25s |

- Active Workers: 20-32% discount but charge 24/7
- Scale-to-zero: No charges when idle
- FlashBoot: Sub-200ms cold starts for cached models [^3586^]

**Cloud Pods (Community Cloud):**
| GPU | Per-Hour |
|-----|----------|
| RTX 3090 | ~$0.22/hr |
| RTX 4090 | ~$0.44/hr |
| A100 80GB | $1.19-1.39/hr |
| H100 | $1.99-2.69/hr |
| H200 | $3.59/hr |

- Secure Cloud: ~20-40% premium over Community
- Per-minute billing
- Spot pricing available (40-90% discount) [^3732^]

**API Access:** REST API for pod management. Python SDK (`runpod` pip package). JavaScript/Go SDKs. `runpodctl` CLI. OpenAPI schema available [^3770^].

**HelixCluster Integration Angle:** RunPod is the **best serverless inference platform for HelixCluster APIs**. FlashBoot cold starts (<200ms) make it viable for production APIs. Use Flex Workers for bursty workloads (<30% utilization). Use Community Pods for development. The per-second billing with scale-to-zero is ideal for multi-tenant inference where demand is unpredictable.

---

### 1.5 Together AI -- Serverless Inference API

**What it is:** Together AI hosts 100+ open-weight models via a single API. Supports serverless inference, dedicated instances, GPU clusters, fine-tuning, and sandboxed code execution. FlashAttention and ATLAS optimizations for throughput [^3798^].

**Pricing Model (Serverless Inference):**

| Model | Input $/1M tokens | Output $/1M tokens |
|-------|------------------|-------------------|
| DeepSeek V4 Pro | $2.10 | $4.40 |
| GLM-5.1 | $1.40 | $4.40 |
| Kimi K2.6 | $1.20 | $4.50 |
| Qwen3.7-Max | $1.25 | $3.75 |
| Qwen3.6-Plus | $0.50 | $3.00 |
| Llama 3.3 70B | $0.88 | $0.88 |
| Qwen3.5 9B | $0.10 | $0.15 |
| gpt-oss-120B | $0.15 | $0.60 |
| LFM2 24B | $0.03 | $0.12 |
| MiniMax M2.5 | $0.30 | $1.20 |

**Dedicated Inference (single-tenant GPUs):**
| Hardware | Price/hour |
|----------|-----------|
| 1x H100 80GB | $6.49 |
| 1x HGX B200 | $11.95 |

**GPU Clusters (on-demand):**
| Hardware | Per-GPU/Hour |
|----------|-------------|
| HGX H100 | $5.49 |
| HGX H200 | $6.79 |
| HGX B200 | $9.95 |

Reserved clusters (7+ days): 9-19% discount [^3798^].

**Fine-Tuning Pricing:**
| Model Size | LoRA ($/1M tokens) | Full FT ($/1M tokens) |
|------------|-------------------|----------------------|
| Up to 16B | $0.48 | $0.54 |
| 17B-69B | $1.50 | $1.65 |
| 70-100B | $2.90 | $3.20 |

**API Access:** OpenAI-compatible REST API. Python SDK. Drop-in replacement for OpenAI client with base URL swap.

```python
from openai import OpenAI
client = OpenAI(base_url="https://api.together.ai/v1", api_key="YOUR_KEY")
```

**HelixCluster Integration Angle:** Together AI is the **primary inference API provider for open-weight models**. Use for: (1) zero-infrastructure inference with 100+ models, (2) fine-tuning with per-token pricing, (3) dedicated instances when serverless limits are hit. The FlashAttention-optimized serving gives 2-4x throughput improvement over standard vLLM deployments [^3798^].

---

### 1.6 Replicate -- Model Marketplace

**What it is:** Replicate hosts 50,000+ models (community + official). Simple API calls for any model. Acquired by Cloudflare in 2025 [^3588^]. Two billing tracks: hardware-per-second and output-based [^3767^].

**Pricing Model:**

**Hardware Rates (per-second):**
| Hardware | Per-Second | Per-Hour |
|----------|-----------|----------|
| CPU (Small) | $0.000025 | $0.09 |
| NVIDIA T4 | $0.000225 | $0.81 |
| NVIDIA L40S | $0.000975 | $3.51 |
| NVIDIA A100 80GB | $0.001400 | $5.04 |
| NVIDIA H100 | $0.001525 | $5.49 |

**Output-Based (Official Models):**
| Model Type | Per-Unit Price |
|------------|---------------|
| FLUX schnell (image) | $0.003/image |
| FLUX 1.1 Pro (image) | $0.04/image |
| Wan 480p video | $0.07-0.09/sec |
| Veo 3 with audio | $0.40/sec |
| Claude 3.7 Sonnet (tokens) | $3/M input, $15/M output |

**Critical Distinction:** Public models = pay only for active inference seconds (setup/idle free). Private deployments = pay for all online time including cold starts [^3767^].

**Cold Starts:** 30-120 seconds for custom models; near-instant for popular pre-hosted models [^3586^].

**API Access:** Python SDK (`replicate` pip package). REST API with simple model references.

```python
import replicate
output = replicate.run("stability-ai/sdxl", input={"prompt": "..."})
```

**HelixCluster Integration Angle:** Replicate is the **model marketplace for specialized AI tasks**. Use for: (1) image generation (FLUX), (2) video generation, (3) quick prototyping with 50,000+ pre-hosted models. Avoid for latency-sensitive production due to cold starts on custom models. The Cloudflare edge deployment (post-acquisition) may improve latency.

---

### 1.7 Modal -- Serverless Python Platform

**What it is:** Modal is a serverless Python platform that transforms Python functions into cloud GPU workloads. No Docker, no YAML -- pure Python deployment. $1.1B valuation (Sept 2025) [^3588^].

**Pricing Model (per-second):**

| GPU | Per-Second | Per-Hour |
|-----|-----------|----------|
| T4 | $0.000164 | $0.59 |
| L4 | $0.000222 | $0.80 |
| A10 | $0.000306 | $1.10 |
| L40S | $0.000542 | $1.95 |
| A100 40GB | $0.000583 | $2.10 |
| A100 80GB | $0.000694 | $2.50 |
| H100 | $0.001097 | $3.95 |
| B200 | $0.001736 | $6.25 |

Plus CPU: $0.0000131/core/sec. Memory: $0.00000222/GiB/sec. Volumes: $0.09/GiB/month [^3759^].

**Platform Fees:**
- Starter: Free ($30/month compute credits)
- Team: $250/month ($100 credits, 50 GPU concurrency)
- Enterprise: Custom (HIPAA, SSO, audit logs) [^3755^]

**Production Multiplier:** 3x cost for non-preemptible guaranteed execution. Regional selection adds 1.25x-2.5x [^3755^].

**Cold Starts:** Sub-second through Rust-based container system [^3588^].

**API Access:** Pure Python SDK with decorators. No REST API required -- functions become endpoints.

```python
import modal
app = modal.App()

@app.function(gpu="A100")
def run_inference(prompt: str) -> str:
    return result
```

**HelixCluster Integration Angle:** Modal is the **rapid development and prototyping layer**. Use for: (1) quick inference functions with sub-second cold starts, (2) cron-scheduled batch jobs, (3) data preprocessing pipelines. The Python-native approach eliminates Docker build cycles. However, the 3x production multiplier makes it expensive for guaranteed workloads. Calculate break-even at ~40% GPU utilization [^3755^].

---

### 1.8 AWS / GCP / Azure -- Hyperscaler GPU Cloud

**What it is:** The big three hyperscalers offer the broadest ecosystem but at premium pricing. Best for teams already embedded in their infrastructure.

**Pricing Comparison (H100 SXM5):**

| Provider | Instance | Per-GPU/Hour (On-Demand) | Spot | 1-Year Reserved |
|----------|----------|-------------------------|------|----------------|
| AWS | p5.48xlarge | $6.88 | ~$3.83 (44% off) | ~$3.50-4.50 |
| GCP | a3-highgpu-8g | $8.00-11.00 | ~$3.69 | ~$5.00-7.50 |
| Azure | ND96isr H100 v5 | $6.98-12.29 | ~$3.50-6.00 | ~$6.50-9.00 |

**AWS GPU Pricing Detail:**
| Instance | GPU | Per-Hour (Full Instance) |
|----------|-----|------------------------|
| p5.48xlarge | 8x H100 | $55.04 |
| p5e.48xlarge | 8x H200 | ~$40-45 |
| p6-b200.48xlarge | 8x B200 | $113.93 |
| p4de.24xlarge | 8x A100 80GB | ~$27.84 |
| p4d.24xlarge | 8x A100 40GB | $21.95 |
| g5.xlarge | 1x A10G | $1.01 |

**GCP GPU Pricing:** A100 80GB at $2.40/GPU/hr (a2-ultragpu-1g), competitive for single-GPU. H100 only in 8-GPU A3 instances [^3732^].

**Azure GPU Pricing:** NC H100 v5 at $6.98/GPU/hr. ND H100 v5 at ~$12.29/GPU/hr [^3728^].

**Spot Instance Characteristics:**
- AWS: 30s-2min interruption notice. 60-91% discount.
- GCP: Preemptible VMs, up to 91% discount.
- Azure: Spot VMs, 60-85% discount.
- Preemption rates: Training workloads see ~5-15% interruption/day on average [^3709^].

**HelixCluster Integration Angle:** Hyperscalers are the **enterprise compliance and ecosystem anchor**. Use for: (1) workloads requiring VPC/SOC2/HIPAA, (2) integration with S3/BigQuery/Azure AD, (3) reserved instances for predictable 24/7 production. Avoid on-demand for cost-sensitive workloads -- neo-clouds are 40-85% cheaper [^3728^].

---

### 1.9 CoreWeave -- Kubernetes-Native GPU Cloud

**What it is:** CoreWeave is a Kubernetes-native GPU cloud built for enterprise AI. 250,000+ GPUs across 32 data centers. Public company (CRWV), $50B market cap. NVIDIA invested $2B [^3713^].

**Pricing Model (on-demand):**

| Configuration | Per-Hour (Full Node) | Per-GPU | Spot |
|--------------|---------------------|---------|------|
| 8x HGX H100 | $49.24 | ~$6.16 | $19.71 (~40%) |
| 8x HGX H200 | $50.44 | ~$6.31 | $20.93 |
| 8x A100 80GB | $21.60 | ~$2.70 | $9.65 |
| 8x L40S | $18.00 | ~$2.25 | $7.88 |
| 1x GH200 | $6.50 | $6.50 | N/A |

**Key Features:**
- NVIDIA Quantum InfiniBand at 400 Gbps with GPUDirect RDMA
- Sub-microsecond inter-node latency
- SOC 2 Type II certified
- Reserved pricing: Up to 60% off on-demand [^3712^]

**API Access:** Kubernetes-native (kubectl). No self-serve signup -- requires sales engagement. Primary interface is Kubernetes YAML [^3713^].

**HelixCluster Integration Angle:** CoreWeave is the **enterprise training cluster provider**. Use for: (1) multi-node distributed training at 100+ GPU scale, (2) workloads requiring InfiniBand interconnect, (3) long-term reserved capacity for predictable workloads. Not suitable for on-demand experimentation due to sales-required onboarding.

---

### 1.10 Lambda Cloud -- Researcher's GPU Cloud

**What it is:** Lambda Cloud provides pre-configured GPU instances with Lambda Stack (PyTorch, TensorFlow, CUDA). $1.5B raised, building $500M AI factory. IPO targeted H2 2026 [^3729^].

**Pricing Model:**

| GPU | On-Demand | Reserved 1-Year | Reserved 3-Year |
|-----|-----------|----------------|----------------|
| H100 SXM (1x) | $4.29/hr | ~$3.43 (20%) | ~$3.18 (26%) |
| H100 SXM (8x) | $3.99/GPU/hr | ~$3.19 | ~$2.95 |
| H100 PCIe | $3.29/hr | ~$2.63 | ~$2.43 |
| A100 80GB SXM (8x) | $2.79/GPU/hr | Contact sales | Contact sales |
| A100 80GB PCIe | $1.99/hr | Contact sales | Contact sales |
| A100 40GB SXM | $1.29/hr | N/A | N/A |
| A10 | $0.75/hr | N/A | N/A |
| Quadro RTX 6000 | $0.50/hr | N/A | N/A |

**Key Features:**
- Per-minute billing, zero egress fees
- Pre-configured Lambda Stack
- SSH access, no Kubernetes required
- 1-Click Clusters for 2-week+ reservations [^3730^]

**API Access:** Web console + CLI. No formal REST API for programmatic deployment [^3729^].

**HelixCluster Integration Angle:** Lambda is the **research and development GPU provider**. Use for: (1) individual researcher access with pre-configured environments, (2) quick SSH-based experimentation. The availability problem (frequently sold out) makes it unreliable as a primary production source [^3729^].

---

## 2. Cross-Platform Price Comparison Tables

### 2.1 GPU Per-Hour Comparison (On-Demand, USD)

| Provider | H100 SXM | H100 PCIe | A100 80GB | A100 40GB | RTX 4090 | L40S |
|----------|----------|-----------|-----------|-----------|----------|------|
| **io.net** | $1.49-2.20 | N/A | $2.30 | $1.40 | $0.28 | N/A |
| **Spheron** | $2.50 | $2.01 | $1.07 | N/A | $0.55 | $0.91 |
| **RunPod** | $2.69 | $1.99 | $1.39 | N/A | $0.44 | $0.79 |
| **Vast.ai** | $1.53-2.27 | N/A | $0.67-1.50 | N/A | $0.20-0.35 | N/A |
| **Akash** | $2.50-4.00 | N/A | $2.50-3.50 | $1.50-2.50 | $0.50-1.50 | N/A |
| **Lambda** | $3.99-4.29 | $3.29 | $1.99 | N/A | N/A | N/A |
| **CoreWeave** | ~$6.16 | $4.25 | ~$2.70 | N/A | N/A | ~$2.25 |
| **AWS** | $6.88 | N/A | $3.67 | N/A | N/A | N/A |
| **GCP** | $8.00-11.00 | N/A | $2.40 | N/A | N/A | N/A |
| **Azure** | $6.98-12.29 | N/A | $3.67 | N/A | N/A | N/A |

### 2.2 GPU Per-Hour Comparison (Spot/Preemptible, USD)

| Provider | H100 SXM | H100 PCIe | A100 80GB | RTX 4090 |
|----------|----------|-----------|-----------|----------|
| **Spheron Spot** | **$1.03** | N/A | **$0.60** | N/A |
| **RunPod Spot** | ~$1.80 | ~$1.19 | ~$0.90 | ~$0.22 |
| **Vast.ai Spot** | $0.34-2.50 | N/A | $0.30-1.00 | $0.15-0.31 |
| **AWS Spot** | $3.83 | N/A | ~$1.50 | N/A |
| **GCP Spot** | $3.69 | N/A | ~$1.00 | N/A |
| **CoreWeave Spot** | $19.71/node | N/A | $9.65/node | N/A |
| **Azure Spot** | $3.50-6.00 | N/A | ~$1.50 | N/A |

### 2.3 LLM Inference Cost Per Million Tokens

| Provider | Approach | Llama 70B Equiv | DeepSeek V3 Equiv | 8B Model |
|----------|----------|----------------|-------------------|----------|
| **inference.net** | Self-hosted API | ~$0.10-0.30 | $0.14/$0.28 | $0.04-0.10 |
| **Chutes.ai** | Decentralized | Varies (PAYG) | Available | Available |
| **Together AI** | Serverless | $0.88 (Llama 3.3 70B) | $2.10/$4.40 (V4 Pro) | $0.10-0.30 |
| **Groq** | LPU Hardware | $0.20-0.50 | N/A | Free tier |
| **Replicate** | Per-prediction | Model-dependent | N/A | N/A |
| **Self-hosted (A100)** | $1.07/hr GPU | ~$0.57 | ~$0.57 | ~$0.05 |
| **Self-hosted (H100)** | $2.01/hr GPU | ~$0.47 | ~$0.47 | ~$0.04 |

### 2.4 Serverless GPU Comparison

| Platform | H100/hr | Cold Start | Billing | Scale-to-Zero | Best For |
|----------|---------|------------|---------|---------------|----------|
| **Modal** | $3.95 | Sub-second | Per-second | Yes | Python dev, rapid iteration |
| **RunPod Serverless** | $6.99 | <200ms (FlashBoot) | Per-second | Yes | Production inference APIs |
| **Replicate** | $5.49 | 30-120s custom | Per-second | Yes | Pre-hosted model variety |
| **Chutes.ai** | $1.80 (RTX Pro 6000) | Minimal | Per-request + GPU-hr | Configurable | Decentralized, TEE privacy |

---

## 3. Key Findings

### 3.1 Cheapest GPU Inference Per Million Tokens

For open-weight models, the cheapest inference is **self-hosted on H100 at $0.47/M tokens** (based on 1,200 tokens/sec throughput at $2.01/hr GPU cost) [^3728^]. Among managed APIs, **inference.net** offers the lowest at **$0.04-0.10/M tokens** for small models and **$0.14/$0.28/M** for DeepSeek V3.2 [^3774^]. Together AI's Llama 3.3 70B at $0.88/M is competitive for serverless convenience.

For frontier-class models, DeepSeek V3.2 via Together AI at $2.10/$4.40/M delivers **85-90% of GPT-5.2 quality at 92% lower cost** [^3774^].

### 3.2 Cheapest GPU Per Hour for Long-Running Jobs

The cheapest verified on-demand H100 is **$1.38/hr at Thunder Compute** [^2465^]. The cheapest spot H100 is **$1.03/hr at Spheron** [^3728^]. For consumer GPUs, **Vast.ai offers RTX 4090 at $0.20-0.35/hr** [^3728^].

For training workloads that tolerate interruption, spot instances across Spheron, Vast.ai, and RunPod offer **40-65% savings** over on-demand [^3709^].

### 3.3 Best API for Programmatic Access

**Winner: RunPod** -- Full REST API with OpenAPI schema, Python/JS/Go SDKs, CLI tool, and comprehensive documentation [^3770^]. **Runner-up: Modal** -- Python-native SDK with decorator-based deployment (no infrastructure code) [^3588^]. **Third: Vast.ai** -- Purpose-built for programmatic access with SDK designed for autonomous compute procurement [^3765^].

### 3.4 Cost Arbitrage Across Platforms

Real-time price differences reach **50%+ for identical GPU types** across providers [^3758^]. A multi-provider routing system like OneInfer achieved **28% raw GPU cost reduction** by routing to cheapest available provider meeting latency SLAs [^3757^]. Airbnb's multi-cloud orchestration achieves **47% cost reduction** across AWS/Azure/GCP [^3758^].

### 3.5 Spot vs On-Demand Price Ratio

| Provider | GPU | On-Demand | Spot | Ratio |
|----------|-----|-----------|------|-------|
| Spheron | H100 SXM | $2.50 | $1.03 | **2.4x** |
| Spheron | A100 80GB | $1.07 | $0.60 | **1.8x** |
| Spheron | B300 | $6.80 | $2.45 | **2.8x** |
| AWS | H100 SXM | $6.88 | $3.83 | **1.8x** |
| GCP | H100 SXM | $8.00-11.00 | $3.69 | **2.2-3.0x** |
| CoreWeave | H100 HGX | $49.24/node | $19.71 | **2.5x** |

---

## 4. Cost Arbitrage Algorithm

### 4.1 Arbitrage Architecture

```
+------------------------------------------------------------------+
|                     HelixCluster ComputeBroker                    |
|                                                                   |
|  +----------------+  +----------------+  +--------------------+  |
|  |  Workload      |  |  Price Feed    |  |  Provider          |  |
|  |  Classifier    |  |  Aggregator    |  |  Router            |  |
|  |                |  |                |  |                    |  |
|  | - inference    |  | - io.net API   |  | - Latency-aware    |  |
|  | - training     |  | - Akash bids   |  | - Cost-optimized   |  |
|  | - fine-tuning  |  | - Vast.ai SDK  |  | - Reliability      |  |
|  | - batch        |  | - RunPod API   |  | - GPU-type match   |  |
|  | - dev/test     |  | - AWS spot     |  |                    |  |
|  +-------+--------+  +-------+--------+  +--------+-----------+  |
|          |                   |                   |                |
|          v                   v                   v                |
|  +-------+--------+  +-------+--------+  +--------+-----------+  |
|  |  Cost Model    |  |  Availability|  |  Decision Engine   |  |
|  |  Evaluator     |  |  Tracker     |  |                    |  |
|  |                |  |              |  | Score = f(cost,    |  |
|  | $/token for LLM|  | Real-time GPU|  |       latency,     |  |
|  | $/hr for GPU   |  | inventory    |  |       reliability, |  |
|  | $/job for batch|  | per provider |  |       GPU match)   |  |
|  +----------------+  +----------------+  +--------+-----------+  |
|                                                   |               |
|                              +--------------------v--------+      |
|                              |  Provider Selection Queue   |      |
|                              |  (ranked by score)          |      |
|                              +-----------------------------+      |
+------------------------------------------------------------------+
```

### 4.2 Routing Decision Algorithm

```python
class ComputeBroker:
    """
    Unified GPU compute buying manager for HelixCluster.
    Routes workloads to the cheapest suitable provider.
    """
    
    PROVIDERS = {
        "io.net": {"type": "depin", "min_gpu_hr": 0.28, "api": "rest"},
        "akash": {"type": "marketplace", "min_gpu_hr": 0.50, "api": "sdk"},
        "runpod": {"type": "serverless", "min_gpu_hr": 0.44, "api": "rest"},
        "modal": {"type": "serverless_python", "min_gpu_hr": 0.59, "api": "sdk"},
        "together": {"type": "inference_api", "api": "openai_compat"},
        "chutes": {"type": "decentralized_inf", "api": "openai_compat"},
        "vastai": {"type": "marketplace", "min_gpu_hr": 0.20, "api": "sdk"},
        "aws": {"type": "hyperscaler", "min_gpu_hr": 3.67, "api": "sdk"},
        "lambda": {"type": "dedicated", "min_gpu_hr": 0.50, "api": "cli"},
        "coreweave": {"type": "enterprise", "min_gpu_hr": 2.25, "api": "k8s"},
    }
    
    def route_workload(self, workload: WorkloadSpec) -> ProviderSelection:
        """
        Core routing algorithm. Selects optimal provider for workload.
        
        Args:
            workload: Specifications including:
                - workload_type: inference | training | fine_tuning | batch
                - gpu_type: H100 | A100_80GB | RTX_4090 | L40S | any
                - min_vram_gb: minimum VRAM required
                - max_latency_ms: latency SLA (for inference)
                - duration_hours: expected duration
                - fault_tolerant: whether checkpointing is available
                - privacy_required: whether TEE/confidential compute needed
                - budget_limit: maximum acceptable cost
        
        Returns:
            ProviderSelection with provider, estimated cost, confidence score
        """
        candidates = []
        
        for name, config in self.PROVIDERS.items():
            # Check GPU availability
            if not self.gpu_available(name, workload.gpu_type):
                continue
            
            # Check latency SLA for inference
            if workload.max_latency_ms and \
               not self.meets_latency(name, workload.max_latency_ms):
                continue
            
            # Calculate cost estimate
            cost = self.estimate_cost(name, workload)
            
            # Check budget
            if workload.budget_limit and cost > workload.budget_limit:
                continue
            
            # Calculate composite score (lower is better)
            score = self.calculate_score(name, cost, workload)
            candidates.append((name, cost, score))
        
        # Sort by score, return best
        candidates.sort(key=lambda x: x[2])
        best = candidates[0]
        
        # If best fails, have 2 fallbacks ready
        fallbacks = [c[0] for c in candidates[1:3]]
        
        return ProviderSelection(
            provider=best[0],
            estimated_cost=best[1],
            fallbacks=fallbacks,
            confidence=self.calculate_confidence(best)
        )
    
    def calculate_score(self, provider: str, cost: float, 
                       workload: WorkloadSpec) -> float:
        """
        Composite scoring function balancing cost, latency, reliability.
        
        score = w1 * normalized_cost 
              + w2 * normalized_latency 
              + w3 * reliability_penalty
              + w4 * setup_overhead
        """
        weights = {
            "cost": 0.40 if workload.workload_type != "inference" else 0.30,
            "latency": 0.30 if workload.max_latency_ms else 0.0,
            "reliability": 0.20,
            "setup": 0.10
        }
        
        score = (weights["cost"] * self.norm_cost(provider, cost) +
                 weights["latency"] * self.norm_latency(provider) +
                 weights["reliability"] * self.reliability_penalty(provider) +
                 weights["setup"] * self.setup_overhead(provider))
        
        return score
```

### 4.3 Provider Selection by Workload Type

```
WORKLOAD TYPE          | PRIMARY PROVIDER    | FALLBACK 1       | FALLBACK 2
-----------------------|---------------------|------------------|------------------
LLM Inference (API)    | Together AI         | Chutes.ai        | Groq (for speed)
Image/Video Gen        | Replicate           | RunPod Serverless| Modal
Training (spot OK)     | Spheron Spot        | Vast.ai Spot     | Akash (lowest bid)
Training (on-demand)   | io.net              | RunPod Pods      | Lambda
Fine-tuning            | Together AI         | Self-hosted      | RunPod
Dev/Prototyping        | Modal               | RunPod Community | io.net (RTX 4090)
Production Inference     | RunPod Serverless   | Together AI      | Chutes.ai
Privacy-sensitive      | Chutes.ai TEE       | Private cluster  | CoreWeave
Multi-node training    | CoreWeave           | AWS p5 reserved  | io.net Clusters
Batch processing       | Modal               | Akash (low bid)  | AWS Spot
```

---

## 5. Unified Buying Manager Design

### 5.1 Architecture Diagram

```
                    +---------------------+
                    |  HelixCluster API   |
                    |  Gateway            |
                    +----------+----------+
                               |
                    +----------v----------+
                    |  ComputeBroker      |
                    |  (this module)      |
                    +----+-----+-----+----+
                         |     |     |
            +------------+     |     +------------+
            |                  |                  |
   +--------v-------+  +-------v--------+  +------v---------+
   |  Inference     |  |  Training      |  |  Batch         |
   |  Router        |  |  Provisioner   |  |  Scheduler     |
   |                |  |                |  |                |
   | - Together AI  |  | - io.net       |  | - Modal        |
   | - Chutes.ai    |  | - Akash        |  | - AWS Spot     |
   | - RunPod Svr   |  | - Spheron      |  | - Akash        |
   | - Replicate    |  | - Lambda       |  |                |
   | - Groq         |  | - CoreWeave    |  |                |
   +--------+-------+  +-------+--------+  +------+---------+
            |                  |                  |
            +------------------+------------------+
                               |
                    +----------v-----------+
                    |  Price Feed          |
                    |  Aggregator          |
                    |                      |
                    | - io.net pricing API |
                    | - Akash bid stream   |
                    | - Vast.ai SDK        |
                    | - AWS spot prices    |
                    | - RunPod pricing     |
                    +----------------------+
```

### 5.2 Cost Optimization Strategies

**Strategy 1: Real-Time Price Arbitrage**
- Query all providers' current GPU prices every 60 seconds
- Maintain a sorted priority queue of cheapest available GPUs
- Route new workloads to the cheapest provider meeting SLAs
- Expected savings: 25-35% [^3757^]

**Strategy 2: Spot Instance Cascade**
- Tier 1: Try Spheron spot ($1.03/hr H100) -- 59% savings
- Tier 2: Try Vast.ai spot ($0.34-2.50/hr H100) -- variable
- Tier 3: Try AWS/GCP spot ($3.69-3.83/hr H100) -- more reliable
- Maintain checkpointing every 15 minutes for fault tolerance
- Expected savings: 40-60% for training workloads [^3709^]

**Strategy 3: Serverless for Variable Inference**
- Below 30% GPU utilization: Use serverless (Modal/RunPod) -- pay per second
- Above 50% utilization: Use dedicated pods -- lower hourly rate
- Break-even at ~40% utilization [^3755^]

**Strategy 4: Model Routing by Task Complexity**
- Simple tasks (classification, extraction): Route to 8B models
- Medium tasks (summarization, QA): Route to 70B models
- Complex tasks (coding, reasoning): Route to frontier models
- Expected savings: 30-40% of total inference cost [^3757^]

**Strategy 5: Quantization for Non-Critical Workloads**
- Internal tools: AWQ INT4 quantization -- 2-4x VRAM reduction
- Customer-facing: Keep FP16 for quality
- Expected savings: 40% GPU count for internal tier [^3757^]

### 5.3 Implementation: ComputeBroker Class

```python
"""
HelixCluster ComputeBroker - Unified GPU Compute Buying Manager

This module implements a cost-arbitrage compute broker that routes
HelixCluster workloads to the cheapest suitable GPU provider.
"""

import asyncio
import json
from dataclasses import dataclass, field
from typing import Optional, List, Dict, Callable
from enum import Enum
import logging

logger = logging.getLogger("helixcluster.compute_broker")


class WorkloadType(Enum):
    INFERENCE = "inference"
    TRAINING = "training"
    FINE_TUNING = "fine_tuning"
    BATCH = "batch"
    DEV = "dev"
    PRODUCTION_INF = "production_inference"
    PRIVACY_SENSITIVE = "privacy_sensitive"


class GPUType(Enum):
    H100_SXM = "H100_SXM"
    H100_PCIE = "H100_PCIE"
    H200 = "H200"
    B200 = "B200"
    A100_80GB = "A100_80GB"
    A100_40GB = "A100_40GB"
    RTX_4090 = "RTX_4090"
    L40S = "L40S"
    ANY = "ANY"


@dataclass
class WorkloadSpec:
    """Specification for a compute workload."""
    workload_type: WorkloadType
    gpu_type: GPUType = GPUType.ANY
    gpu_count: int = 1
    min_vram_gb: int = 0
    max_latency_ms: Optional[int] = None
    duration_hours: float = 1.0
    fault_tolerant: bool = False
    privacy_required: bool = False
    budget_limit: Optional[float] = None
    region_preference: Optional[str] = None
    
    # For inference workloads
    model_name: Optional[str] = None
    expected_tokens_per_hour: Optional[int] = None


@dataclass
class ProviderCapabilities:
    """Capabilities and pricing for a compute provider."""
    name: str
    provider_type: str  # depin, marketplace, serverless, hyperscaler, etc.
    
    # GPU pricing per hour (on-demand, spot if available)
    gpu_pricing: Dict[GPUType, Dict[str, float]] = field(default_factory=dict)
    
    # Inference pricing per million tokens
    inference_pricing: Dict[str, Dict[str, float]] = field(default_factory=dict)
    
    # Capabilities
    supports_spot: bool = False
    supports_serverless: bool = False
    supports_tee: bool = False
    requires_k8s: bool = False
    requires_sales: bool = False
    
    # API access
    api_type: str = "rest"  # rest, sdk, cli, k8s, openai_compat
    cold_start_seconds: float = 0.0
    
    # Reliability score (0-1)
    reliability_score: float = 0.9
    
    # Geographic regions
    regions: List[str] = field(default_factory=list)


@dataclass
class ProviderSelection:
    """Result of provider selection."""
    provider: str
    estimated_cost: float
    estimated_cost_unit: str  # "USD/hr" or "USD/M_tokens"
    fallbacks: List[str]
    confidence: float
    reasoning: str = ""


class ComputeBroker:
    """
    Unified GPU compute broker for HelixCluster.
    
    Routes workloads to optimal providers based on real-time pricing,
    latency requirements, and workload characteristics.
    
    Usage:
        broker = ComputeBroker()
        spec = WorkloadSpec(
            workload_type=WorkloadType.TRAINING,
            gpu_type=GPUType.H100_SXM,
            gpu_count=8,
            duration_hours=72,
            fault_tolerant=True
        )
        selection = broker.route_workload(spec)
        print(f"Route to {selection.provider} at {selection.estimated_cost}/hr")
    """
    
    # Provider capability definitions
    PROVIDERS = {
        "io.net": ProviderCapabilities(
            name="io.net",
            provider_type="depin",
            gpu_pricing={
                GPUType.H100_SXM: {"on_demand": 1.85, "min": 1.49, "max": 2.20},
                GPUType.A100_80GB: {"on_demand": 2.30},
                GPUType.A100_40GB: {"on_demand": 1.40},
                GPUType.RTX_4090: {"on_demand": 0.28},
            },
            supports_spot=False,  # DePIN has priority tiers instead
            supports_serverless=False,
            api_type="rest",
            cold_start_seconds=120,
            reliability_score=0.85,
            regions=["us-east", "us-west", "eu", "asia"]
        ),
        "spheron": ProviderCapabilities(
            name="spheron",
            provider_type="marketplace",
            gpu_pricing={
                GPUType.H100_SXM: {"on_demand": 2.50, "spot": 1.03},
                GPUType.H100_PCIE: {"on_demand": 2.01},
                GPUType.A100_80GB: {"on_demand": 1.07, "spot": 0.60},
                GPUType.RTX_4090: {"on_demand": 0.55},
                GPUType.L40S: {"on_demand": 0.91},
            },
            supports_spot=True,
            supports_serverless=False,
            api_type="rest",
            cold_start_seconds=60,
            reliability_score=0.88,
        ),
        "runpod": ProviderCapabilities(
            name="runpod",
            provider_type="serverless",
            gpu_pricing={
                GPUType.H100_SXM: {"on_demand": 2.69, "community": 2.69},
                GPUType.H100_PCIE: {"on_demand": 1.99},
                GPUType.A100_80GB: {"on_demand": 1.39, "community": 1.19},
                GPUType.RTX_4090: {"on_demand": 0.44},
                GPUType.L40S: {"on_demand": 0.79},
                GPUType.H200: {"on_demand": 3.59},
            },
            inference_pricing={
                "serverless_h100": {"per_hour": 6.99},
                "serverless_a100": {"per_hour": 3.49},
            },
            supports_spot=True,
            supports_serverless=True,
            api_type="rest",
            cold_start_seconds=0.2,  # FlashBoot
            reliability_score=0.90,
        ),
        "modal": ProviderCapabilities(
            name="modal",
            provider_type="serverless_python",
            gpu_pricing={
                GPUType.H100_SXM: {"on_demand": 3.95},
                GPUType.A100_80GB: {"on_demand": 2.50},
                GPUType.A100_40GB: {"on_demand": 2.10},
                GPUType.L40S: {"on_demand": 1.95},
                GPUType.B200: {"on_demand": 6.25},
            },
            supports_serverless=True,
            api_type="sdk",
            cold_start_seconds=0.5,
            reliability_score=0.92,
        ),
        "together": ProviderCapabilities(
            name="together",
            provider_type="inference_api",
            inference_pricing={
                "llama_3.3_70b": {"input": 0.88, "output": 0.88},
                "deepseek_v4_pro": {"input": 2.10, "output": 4.40},
                "qwen3.7_max": {"input": 1.25, "output": 3.75},
                "glm_5.1": {"input": 1.40, "output": 4.40},
                "qwen3.5_9b": {"input": 0.10, "output": 0.15},
            },
            gpu_pricing={
                GPUType.H100_SXM: {"dedicated": 6.49, "cluster": 5.49},
                GPUType.H200: {"cluster": 6.79},
                GPUType.B200: {"cluster": 9.95},
            },
            supports_serverless=True,
            api_type="openai_compat",
            cold_start_seconds=1.0,
            reliability_score=0.95,
        ),
        "chutes": ProviderCapabilities(
            name="chutes",
            provider_type="decentralized_inf",
            gpu_pricing={
                GPUType.RTX_4090: {"private": 1.80},  # RTX Pro 6000
            },
            supports_serverless=True,
            supports_tee=True,
            api_type="openai_compat",
            cold_start_seconds=2.0,
            reliability_score=0.80,
        ),
        "akash": ProviderCapabilities(
            name="akash",
            provider_type="marketplace",
            gpu_pricing={
                GPUType.H100_SXM: {"on_demand": 3.25, "min": 2.50, "max": 4.00},
                GPUType.A100_80GB: {"on_demand": 3.00, "min": 2.50, "max": 3.50},
                GPUType.A100_40GB: {"on_demand": 2.00, "min": 1.50, "max": 2.50},
                GPUType.RTX_4090: {"on_demand": 1.00, "min": 0.50, "max": 1.50},
            },
            supports_spot=False,  # Reverse auction acts like spot
            api_type="sdk",
            cold_start_seconds=120,
            reliability_score=0.82,
        ),
        "vastai": ProviderCapabilities(
            name="vastai",
            provider_type="marketplace",
            gpu_pricing={
                GPUType.H100_SXM: {"on_demand": 1.90, "min": 1.53, "max": 2.27},
                GPUType.A100_80GB: {"on_demand": 1.10, "min": 0.67, "max": 1.50},
                GPUType.RTX_4090: {"on_demand": 0.28, "min": 0.20, "max": 0.35},
            },
            supports_spot=True,
            api_type="sdk",
            cold_start_seconds=30,
            reliability_score=0.75,
        ),
        "aws": ProviderCapabilities(
            name="aws",
            provider_type="hyperscaler",
            gpu_pricing={
                GPUType.H100_SXM: {"on_demand": 6.88, "spot": 3.83, "reserved_1y": 3.50},
                GPUType.A100_80GB: {"on_demand": 3.67},
                GPUType.H200: {"on_demand": 4.98},
                GPUType.B200: {"on_demand": 14.24},
            },
            supports_spot=True,
            api_type="sdk",
            cold_start_seconds=300,
            reliability_score=0.98,
        ),
        "lambda": ProviderCapabilities(
            name="lambda",
            provider_type="dedicated",
            gpu_pricing={
                GPUType.H100_SXM: {"on_demand": 3.99, "1x": 4.29, "8x": 3.99},
                GPUType.H100_PCIE: {"on_demand": 3.29},
                GPUType.A100_80GB: {"on_demand": 1.99, "sxm8x": 2.79},
                GPUType.A100_40GB: {"on_demand": 1.29},
            },
            api_type="cli",
            cold_start_seconds=180,
            reliability_score=0.85,
        ),
        "coreweave": ProviderCapabilities(
            name="coreweave",
            provider_type="enterprise",
            gpu_pricing={
                GPUType.H100_SXM: {"on_demand": 6.16, "spot": 2.46},
                GPUType.H200: {"on_demand": 6.31, "spot": 2.62},
                GPUType.A100_80GB: {"on_demand": 2.70, "spot": 1.21},
                GPUType.L40S: {"on_demand": 2.25, "spot": 0.99},
            },
            supports_spot=True,
            requires_k8s=True,
            requires_sales=True,
            api_type="k8s",
            cold_start_seconds=600,
            reliability_score=0.97,
        ),
    }
    
    # Workload-to-provider mapping
    WORKLOAD_ROUTES = {
        WorkloadType.INFERENCE: ["together", "chutes", "runpod", "groq"],
        WorkloadType.TRAINING: ["spheron", "vastai", "akash", "io.net", "runpod"],
        WorkloadType.FINE_TUNING: ["together", "runpod", "io.net"],
        WorkloadType.BATCH: ["modal", "akash", "aws", "spheron"],
        WorkloadType.DEV: ["modal", "runpod", "io.net"],
        WorkloadType.PRODUCTION_INF: ["runpod", "together", "chutes"],
        WorkloadType.PRIVACY_SENSITIVE: ["chutes", "coreweave"],
    }
    
    def __init__(self):
        self.price_cache: Dict[str, Dict] = {}
        self.availability_cache: Dict[str, Dict] = {}
        self.last_update: Optional[float] = None
        
    def route_workload(self, spec: WorkloadSpec) -> ProviderSelection:
        """Route a workload to the optimal provider."""
        
        # Get candidate providers for this workload type
        candidates = self.WORKLOAD_ROUTES.get(spec.workload_type, [])
        
        if not candidates:
            # Default to most reliable
            return ProviderSelection(
                provider="aws",
                estimated_cost=0,
                estimated_cost_unit="USD/hr",
                fallbacks=["runpod", "together"],
                confidence=0.5,
                reasoning="No specific routing rule, defaulting to AWS"
            )
        
        scored_candidates = []
        
        for provider_name in candidates:
            if provider_name not in self.PROVIDERS:
                continue
                
            provider = self.PROVIDERS[provider_name]
            
            # Check if provider has the requested GPU
            if spec.gpu_type != GPUType.ANY and \
               spec.gpu_type not in provider.gpu_pricing:
                continue
            
            # Estimate cost
            cost = self._estimate_cost(provider, spec)
            
            # Calculate score
            score = self._score_provider(provider, spec, cost)
            
            scored_candidates.append((provider_name, cost, score))
        
        if not scored_candidates:
            return ProviderSelection(
                provider="",
                estimated_cost=0,
                estimated_cost_unit="USD",
                fallbacks=[],
                confidence=0.0,
                reasoning="No suitable provider found"
            )
        
        # Sort by score (lower is better)
        scored_candidates.sort(key=lambda x: x[2])
        
        best = scored_candidates[0]
        fallbacks = [c[0] for c in scored_candidates[1:3]]
        
        return ProviderSelection(
            provider=best[0],
            estimated_cost=best[1],
            estimated_cost_unit="USD/hr" if spec.workload_type != WorkloadType.INFERENCE else "USD/M_tokens",
            fallbacks=fallbacks,
            confidence=1.0 / (1 + best[2]),
            reasoning=f"Selected based on cost=${best[1]:.2f}, score={best[2]:.2f}"
        )
    
    def _estimate_cost(self, provider: ProviderCapabilities, 
                       spec: WorkloadSpec) -> float:
        """Estimate cost for a workload on a provider."""
        
        if spec.workload_type == WorkloadType.INFERENCE and \
           provider.inference_pricing:
            # Use inference pricing (per million tokens)
            model_pricing = provider.inference_pricing.get(
                spec.model_name or "default", 
                list(provider.inference_pricing.values())[0]
            )
            avg_cost_per_m = (model_pricing.get("input", 0) + 
                             model_pricing.get("output", 0)) / 2
            expected_m_tokens = (spec.expected_tokens_per_hour or 1000000) / 1e6
            return avg_cost_per_m * expected_m_tokens * spec.duration_hours
        
        # Use GPU pricing (per hour)
        gpu_pricing = provider.gpu_pricing.get(spec.gpu_type, {})
        
        if not gpu_pricing:
            return float('inf')
        
        # Prefer spot for fault-tolerant workloads
        if spec.fault_tolerant and "spot" in gpu_pricing:
            rate = gpu_pricing["spot"]
        elif "on_demand" in gpu_pricing:
            rate = gpu_pricing["on_demand"]
        elif "community" in gpu_pricing:
            rate = gpu_pricing["community"]
        else:
            rate = list(gpu_pricing.values())[0]
        
        return rate * spec.gpu_count * spec.duration_hours
    
    def _score_provider(self, provider: ProviderCapabilities, 
                        spec: WorkloadSpec, cost: float) -> float:
        """
        Score a provider for a workload. Lower is better.
        
        Weights: cost 40%, reliability 30%, cold_start 20%, setup 10%
        """
        
        # Normalize cost (divide by reference cost of $5/hr)
        norm_cost = cost / (5.0 * spec.gpu_count * spec.duration_hours) if cost > 0 else 0
        
        # Reliability penalty (1 - reliability_score)
        reliability_penalty = 1 - provider.reliability_score
        
        # Cold start penalty (normalize to 0-1, max 600s)
        cold_penalty = min(provider.cold_start_seconds / 600.0, 1.0)
        
        # Setup penalty (CLI/K8S harder than REST/SDK)
        setup_penalty = {"rest": 0.0, "sdk": 0.1, "openai_compat": 0.0,
                        "cli": 0.3, "k8s": 0.5}.get(provider.api_type, 0.2)
        
        # Latency penalty for inference
        latency_penalty = 0.0
        if spec.max_latency_ms and spec.max_latency_ms < 500:
            latency_penalty = 0.2  # Favor low-latency providers
        
        score = (0.35 * norm_cost + 
                 0.25 * reliability_penalty +
                 0.20 * cold_penalty +
                 0.10 * setup_penalty +
                 0.10 * latency_penalty)
        
        # Boost for TEE if privacy required
        if spec.privacy_required and provider.supports_tee:
            score *= 0.7  # 30% discount for privacy match
        
        return score


# ---------------------------------------------------------------------------
# Example usage
# ---------------------------------------------------------------------------

def main():
    """Demonstrate ComputeBroker usage."""
    broker = ComputeBroker()
    
    # Example 1: Training workload (fault-tolerant, spot-eligible)
    training = WorkloadSpec(
        workload_type=WorkloadType.TRAINING,
        gpu_type=GPUType.H100_SXM,
        gpu_count=8,
        duration_hours=72,
        fault_tolerant=True
    )
    sel = broker.route_workload(training)
    print(f"Training: {sel.provider} at ${sel.estimated_cost:.2f} "
          f"(fallbacks: {sel.fallbacks})")
    
    # Example 2: Inference workload
    inference = WorkloadSpec(
        workload_type=WorkloadType.INFERENCE,
        model_name="llama_3.3_70b",
        expected_tokens_per_hour=10_000_000,
        duration_hours=720  # 1 month
    )
    sel = broker.route_workload(inference)
    print(f"Inference: {sel.provider} at ${sel.estimated_cost:.2f} "
          f"(fallbacks: {sel.fallbacks})")
    
    # Example 3: Privacy-sensitive workload
    privacy = WorkloadSpec(
        workload_type=WorkloadType.PRIVACY_SENSITIVE,
        gpu_type=GPUType.RTX_4090,
        gpu_count=1,
        duration_hours=24,
        privacy_required=True
    )
    sel = broker.route_workload(privacy)
    print(f"Privacy: {sel.provider} at ${sel.estimated_cost:.2f} "
          f"(fallbacks: {sel.fallbacks})")


if __name__ == "__main__":
    main()
```

---

## 6. HelixCluster Integration

### 6.1 Integration Architecture

```
+------------------+     +------------------+     +------------------+
|  HelixCluster    |     |  ComputeBroker   |     |  External GPU    |
|  Control Plane   |<--->|  (this module)   |<--->|  Providers       |
|                  |     |                  |     |                  |
| - Workload queue |     | - Price cache    |     | - io.net         |
| - Node manager   |     | - Provider pool  |     | - RunPod         |
| - Resource model |     | - Cost optimizer |     | - Akash          |
| - Scheduler      |     | - Failover logic |     | - Together       |
| - Metrics        |     | - Billing track  |     | - Chutes         |
+------------------+     +------------------+     +------------------+
         |                        |                        |
         +------------------------+------------------------+
                                  |
                    +-------------v-------------+
                    |  Unified Compute Layer    |
                    |                           |
                    | - Remote nodes appear as  |
                    |   local HelixCluster nodes|
                    | - Cost-aware scheduling   |
                    | - Auto-failover on preemption|
                    | - Budget enforcement      |
                    +---------------------------+
```

### 6.2 Specific Implementation: Remote Node Registration

```python
"""
HelixCluster remote node adapter.
Makes external GPU providers appear as HelixCluster nodes.
"""

import asyncio
from dataclasses import dataclass
from typing import Optional
import aiohttp


@dataclass
class RemoteNodeConfig:
    """Configuration for a remote compute node."""
    provider: str           # e.g., "io.net", "runpod", "akash"
    node_id: str            # HelixCluster node ID
    gpu_type: str           # e.g., "H100_SXM", "A100_80GB"
    gpu_count: int
    region: str
    cost_per_hour: float
    is_spot: bool = False
    max_duration_hours: Optional[float] = None
    api_key: str = ""
    endpoint_url: str = ""


class RemoteNodeAdapter:
    """
    Adapter that makes external GPU instances appear as HelixCluster nodes.
    
    Usage:
        adapter = RemoteNodeAdapter(compute_broker)
        node = await adapter.provision_node(
            gpu_type="H100_SXM",
            gpu_count=8,
            workload_type="training",
            budget=100.0
        )
        # node now appears as a HelixCluster node
        helixcluster.register_node(node)
    """
    
    def __init__(self, broker: ComputeBroker):
        self.broker = broker
        self.active_nodes: Dict[str, RemoteNodeConfig] = {}
        
    async def provision_node(self, gpu_type: str, gpu_count: int,
                            workload_type: str, budget: float,
                            fault_tolerant: bool = True) -> dict:
        """
        Provision a remote GPU node and return HelixCluster-compatible config.
        
        This is the main entry point for HelixCluster integration.
        """
        # Map to WorkloadSpec
        wl_type = WorkloadType[workload_type.upper()]
        gpu = GPUType[gpu_type.upper()]
        
        spec = WorkloadSpec(
            workload_type=wl_type,
            gpu_type=gpu,
            gpu_count=gpu_count,
            fault_tolerant=fault_tolerant,
            budget_limit=budget
        )
        
        # Route to optimal provider
        selection = self.broker.route_workload(spec)
        
        if not selection.provider:
            raise RuntimeError("No suitable provider found")
        
        # Provision based on provider type
        provisioners = {
            "io.net": self._provision_ionet,
            "runpod": self._provision_runpod,
            "spheron": self._provision_spheron,
            "akash": self._provision_akash,
            "modal": self._provision_modal,
            "together": self._provision_together,
            "chutes": self._provision_chutes,
            "vastai": self._provision_vastai,
            "aws": self._provision_aws,
            "lambda": self._provision_lambda,
        }
        
        provision_fn = provisioners.get(selection.provider)
        if not provision_fn:
            raise RuntimeError(f"No provisioner for {selection.provider}")
        
        node_config = await provision_fn(spec, selection)
        self.active_nodes[node_config.node_id] = node_config
        
        # Return HelixCluster-compatible node definition
        return {
            "node_id": node_config.node_id,
            "provider": selection.provider,
            "gpu_type": gpu_type,
            "gpu_count": gpu_count,
            "cost_per_hour": node_config.cost_per_hour,
            "is_spot": node_config.is_spot,
            "region": node_config.region,
            "endpoint": node_config.endpoint_url,
            "ssh_host": node_config.endpoint_url,  # if SSH available
            "max_duration": node_config.max_duration_hours,
            "labels": {
                "helixcluster.io/provider": selection.provider,
                "helixcluster.io/gpu": gpu_type,
                "helixcluster.io/spot": str(node_config.is_spot),
                "helixcluster.io/cost": str(node_config.cost_per_hour),
            }
        }
    
    async def _provision_ionet(self, spec: WorkloadSpec, 
                                sel: ProviderSelection) -> RemoteNodeConfig:
        """Provision on io.net via REST API."""
        async with aiohttp.ClientSession() as session:
            headers = {"Authorization": f"Bearer {self._get_api_key('ionet')}"}
            payload = {
                "cluster_name": f"helixcluster-{spec.workload_type.value}",
                "gpu_type": spec.gpu_type.value,
                "gpu_count": spec.gpu_count,
                "region": spec.region_preference or "us-east",
            }
            async with session.post(
                "https://cloud.io.net/api/v1/clusters",
                headers=headers, json=payload
            ) as resp:
                result = await resp.json()
                return RemoteNodeConfig(
                    provider="io.net",
                    node_id=result["cluster_id"],
                    gpu_type=spec.gpu_type.value,
                    gpu_count=spec.gpu_count,
                    region=result["region"],
                    cost_per_hour=sel.estimated_cost / (spec.gpu_count * spec.duration_hours),
                    endpoint_url=result["endpoint"],
                )
    
    async def _provision_runpod(self, spec: WorkloadSpec,
                                 sel: ProviderSelection) -> RemoteNodeConfig:
        """Provision on RunPod via REST API."""
        # Implementation using RunPod API
        # POST /v5/groups/{id}/pods with GPU type, count, etc.
        pass
    
    async def _provision_akash(self, spec: WorkloadSpec,
                                sel: ProviderSelection) -> RemoteNodeConfig:
        """Deploy on Akash via SDK."""
        # Build SDL, submit deployment, accept lowest bid
        # Uses Akash TypeScript/Go SDK
        pass
    
    async def _provision_modal(self, spec: WorkloadSpec,
                                sel: ProviderSelection) -> RemoteNodeConfig:
        """Deploy Modal function."""
        # Modal is serverless -- deploy as function, not persistent node
        pass
    
    # ... additional provisioners for each provider
    
    async def teardown_node(self, node_id: str):
        """Teardown a remote node when work is complete."""
        node = self.active_nodes.pop(node_id, None)
        if not node:
            return
        
        # Provider-specific teardown
        teardown_fns = {
            "io.net": self._teardown_ionet,
            "runpod": self._teardown_runpod,
            # ... etc
        }
        fn = teardown_fns.get(node.provider)
        if fn:
            await fn(node)
    
    def _get_api_key(self, provider: str) -> str:
        """Retrieve API key from secrets manager."""
        # Integrate with HelixCluster secrets
        import os
        return os.environ.get(f"{provider.upper()}_API_KEY", "")


# ---------------------------------------------------------------------------
# Helm values for ComputeBroker deployment in HelixCluster
# ---------------------------------------------------------------------------

COMPUTEBROKER_HELM_VALUES = """
computeBroker:
  enabled: true
  replicaCount: 2
  
  # Provider API keys (from secrets)
  providers:
    ionet:
      enabled: true
      apiKeySecret: ionet-api-key
      maxSpendPerHour: 100
    runpod:
      enabled: true
      apiKeySecret: runpod-api-key
    akash:
      enabled: true
      mnemonicSecret: akash-mnemonic
    together:
      enabled: true
      apiKeySecret: together-api-key
    modal:
      enabled: true
      tokenSecret: modal-token
    chutes:
      enabled: true
      apiKeySecret: chutes-api-key
    aws:
      enabled: true
      credentialsSecret: aws-credentials
    spheron:
      enabled: true
      apiKeySecret: spheron-api-key
    vastai:
      enabled: true
      apiKeySecret: vastai-api-key
  
  # Cost optimization settings
  costOptimization:
    enableSpotInstances: true
    spotFallbackToOnDemand: true
    enableServerlessForLowUtilization: true
    utilizationThreshold: 0.40
    priceUpdateIntervalSeconds: 60
    
  # Budget controls
  budgets:
    dailyMax: 1000
    weeklyMax: 5000
    alertThreshold: 0.80
    
  # Workload routing rules
  routing:
    inference:
      primary: together
      fallback: [chutes, runpod]
    training:
      primary: ionet
      spot: spheron
      fallback: [vastai, runpod]
    fineTuning:
      primary: together
      fallback: [runpod, ionet]
    batch:
      primary: modal
      fallback: [akash, aws]
"""
```

### 6.3 Key Integration Points

1. **HelixCluster Scheduler Extension:** The ComputeBroker acts as a "virtual node provider" -- when HelixCluster needs GPU capacity, it calls `broker.route_workload()` instead of provisioning local nodes.

2. **Cost-Based Scheduling:** Extend the Kubernetes scheduler (or custom scheduler) with a `ComputeBrokerPredicate` that factors in external GPU pricing when making placement decisions.

3. **Auto-Failover:** Implement a `NodeHealthMonitor` that detects spot preemptions (30-120s warning) and automatically migrates workloads to the next-cheapest provider.

4. **Budget Enforcement:** The `BudgetController` tracks cumulative spend across all providers and enforces daily/weekly/monthly limits with alerts at 80% threshold.

5. **Unified Billing:** The `BillingAggregator` normalizes usage data from all providers into a single cost model, enabling chargeback and showback to internal teams.

### 6.4 Expected Cost Savings

Based on the research data, HelixCluster with ComputeBroker should achieve:

| Workload Type | Single Provider Cost | Multi-Provider Cost | Savings |
|--------------|---------------------|---------------------|---------|
| Training (spot) | $4.50/GPU/hr (AWS) | $1.50/GPU/hr (avg) | **67%** |
| Training (on-demand) | $6.88/GPU/hr (AWS) | $2.50/GPU/hr (avg) | **64%** |
| LLM Inference (API) | $5.00/M tokens (OpenAI) | $0.50/M tokens (Together) | **90%** |
| Dev/Prototyping | $3.99/hr (Lambda H100) | $0.28/hr (io.net RTX 4090) | **93%** |
| Batch Processing | $6.88/hr (AWS H100) | $0.60/hr (Spheron A100 spot) | **91%** |
| Production Inference | $6.99/hr (RunPod H100) | $2.69/hr (RunPod H100) | **62%** |

**Overall expected savings: 50-90% depending on workload mix**, with the greatest savings coming from: (1) spot instance arbitrage, (2) serverless inference APIs replacing self-hosted, (3) consumer GPUs for dev workloads, and (4) model routing to use smaller models for simple tasks.

---

## 7. Risk Matrix

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Spot preemption during training | Medium | High | Checkpoint every 15min; auto-restart on cheapest provider |
| Provider API changes | Low | Medium | Abstract all APIs behind `ComputeBroker`; version pinning |
| GPU availability shortage | Medium | High | Maintain 3+ provider relationships; priority queue |
| Data egress costs | Medium | Medium | Co-locate compute with data; use providers with free egress |
| Rate limiting on APIs | Medium | Low | Implement exponential backoff; multiple API keys |
| Privacy/regulatory | Low | High | Use Chutes TEE or CoreWeave for sensitive workloads |
| Budget overrun | Low | High | Hard budget limits with 80% alert threshold |

---

## 8. References

- [^2465^] CloudZero: H100 GPU Cost In 2026: Buy, Rent, And Cloud Pricing Compared
- [^3528^] Akave Blog: Akash Network & Akave: Building Sovereign AI Infrastructure
- [^3530^] GitHub: chutesai/chutes - Chutes deployment and billing documentation
- [^3553^] AI Insights News: Chutes API Guide 2026 - Pricing, Models, TEE
- [^3586^] BuildMVPFast: Scale-to-Zero Serverless GPUs - RunPod, Modal comparison
- [^3588^] Introl: Serverless GPU Platforms - RunPod, Modal, Beam comparison
- [^3708^] Chutes.ai Pricing Page
- [^3709^] ComputePrices.com: Cloud GPU Pricing Comparison
- [^3710^] Spheron: GPU Cloud Pricing 2026 Comparison
- [^3712^] CoreWeave: Cloud Pricing Page
- [^3713^] BuildMVPFast: RunPod vs Lambda vs CoreWeave pricing
- [^3714^] CloudZero: Cloud GPU Pricing Comparison - AWS vs Azure vs GCP
- [^3728^] Spheron: GPU Cloud Pricing 2026 (comprehensive)
- [^3729^] BuildMVPFast: RunPod vs Lambda Labs vs CoreWeave
- [^3730^] CheckThat.ai: Lambda Labs Pricing 2026
- [^3731^] CheckThat.ai: RunPod Pricing 2026
- [^3732^] Verda: Cloud GPU Pricing Comparison 2025
- [^3748^] Spheron: RunPod vs Spheron comparison
- [^3755^] CheckThat.ai: Modal Pricing Guide
- [^3757^] OneInfer: We Saved 60% on GPU Costs
- [^3758^] Introl: Multi-Cloud GPU Orchestration Guide
- [^3759^] Modal.com: Plan Pricing
- [^3761^] Chutes.ai: Private Chutes Pricing
- [^3767^] CheckThat.ai: Replicate Pricing 2026
- [^3774^] Inference.net: LLM API Pricing Comparison 2026
- [^3775^] GitHub: Free-LLM APIs with permanent free tiers
- [^3778^] io.net: IO vs AWS GPU Cloud Pricing Comparison
- [^3779^] io.net: GPU Cluster Cheat Sheet
- [^3794^] io.net: 2025 Year in Review
- [^3798^] Together.ai: Pricing Page

---

*Document generated for HelixCluster Phase 8B / Dimension 03: Multi-Platform GPU Compute Buying Strategy. All pricing data reflects publicly available rates as of mid-2026. Prices fluctuate -- implement real-time price feeds for production use.*
