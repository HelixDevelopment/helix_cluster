# HelixCluster + Chutes.ai Integration Architecture

## Comprehensive Technical Integration Guide

**Document Version:** 1.0  
**Last Updated:** 2026-01-18  
**Classification:** Architecture / Implementation  
**Word Count:** ~5,500 words  

---

## Table of Contents

1. [Integration Architecture Overview](#1-integration-architecture-overview)
2. [HelixCluster-as-Miner Implementation](#2-helixcluster-as-miner-implementation)
3. [Chutes API Client for HelixCluster](#3-chutes-api-client-for-helixcluster)
4. [Unified Marketplace Manager](#4-unified-marketplace-manager)
5. [AI Serving Stack Integration](#5-ai-serving-stack-integration)
6. [Security Integration](#6-security-integration)
7. [Economic Model](#7-economic-model)
8. [Deployment Guide](#8-deployment-guide)
9. [HelixCluster Integration Summary](#9-helixcluster-integration-summary)

---

## 1. Integration Architecture Overview

### 1.1 Executive Summary

This document defines the complete integration architecture between **HelixCluster** (a distributed computing OS binding heterogeneous devices into a unified compute block) and **Chutes.ai** (a decentralized serverless AI compute platform operating on Bittensor subnet 64). The integration enables six primary scenarios: (1) HelixCluster nodes functioning as Chutes miners, (2) Chutes.ai serving as an AI inference layer for HelixCluster workloads, (3) unified multi-marketplace compute management, (4) shared AI serving stack deployment, (5) security integration with E2EE and GPU attestation, and (6) multi-token economic reward distribution.

Chutes.ai processes over 100 billion tokens daily [^3481^], operates at approximately 85% lower cost than AWS [^3628^], and maintains a network of 400,000+ active users. Integrating with HelixCluster amplifies both platforms' reach by enabling unified management of heterogeneous compute across decentralized marketplaces.

### 1.2 High-Level Architecture

```
+==========================================================================+
|                         HELIXCLUSTER CONTROL PLANE                        |
|  +------------------+  +------------------+  +------------------------+  |
|  |  Cluster Manager |  | Resource Scheduler|  |   Marketplace Router   |  |
|  |  (Go)            |  | (Go)             |  |   (Go)                 |  |
|  +--------+---------+  +--------+---------+  +-----------+------------+  |
|           |                     |                         |              |
+===========|=====================|=========================|==============+
            |                     |                         |
            |    +----------------+----------------+        |
            |    |        E2EE Proxy Layer          |        |
            |    |  (ML-KEM-768 + ChaCha20-Poly1305)|        |
            |    +----------------+----------------+        |
            |                     |                         |
+===========|=====================|=========================|==============+
|  +--------v---------+  +--------v---------+  +-----------v------------+  |
|  |  Node Agent      |  |  Node Agent      |  |   Node Agent           |  |
|  |  (K3s + Chutes)  |  |  (K3s + Chutes)  |  |   (K3s + Chutes)       |  |
|  |                  |  |                  |  |                        |  |
|  |  +-------------+ |  |  +-------------+ |  |  +------------------+  |  |
|  |  | chutes-miner| |  |  | chutes-miner| |  |  | chutes-miner     |  |  |
|  |  | (Python)    | |  |  | (Python)    | |  |  | (Python)         |  |  |
|  |  +-------------+ |  |  +-------------+ |  |  +------------------+  |  |
|  |  +-------------+ |  |  +-------------+ |  |  +------------------+  |  |
|  |  | GraVal      | |  |  | GraVal      | |  |  | GraVal           |  |  |
|  |  | Bootstrap   | |  |  | Bootstrap   | |  |  | Bootstrap        |  |  |
|  |  +-------------+ |  |  +-------------+ |  |  +------------------+  |  |
|  |  +-------------+ |  |  +-------------+ |  |  +------------------+  |  |
|  |  | vLLM/SGLang | |  |  | vLLM/SGLang | |  |  | vLLM/SGLang      |  |  |
|  |  | Inference   | |  |  | Inference   | |  |  | Inference        |  |  |
|  |  +-------------+ |  |  +-------------+ |  |  +------------------+  |  |
|  +------------------+  +------------------+  +------------------------+  |
|      GPU Node A            GPU Node B              GPU Node C            |
|   (NVIDIA H100)          (AMD MI300X)         (Apple Silicon M3)       |
+==========================================================================+
            |                     |                         |
            +---------------------+-------------------------+
                                  |
+=================================v========================================+
|                         CHUTES.AI NETWORK (Subnet 64)                     |
|  +------------------+  +------------------+  +------------------------+  |
|  |  Validators      |  |  API Gateway     |  |   Registry Service     |  |
|  |  (Weight Setters)|  |  (llm.chutes.ai) |  |   (Docker Images)      |  |
|  +------------------+  +------------------+  +------------------------+  |
|                                                                          |
|  +------------------+  +------------------+  +------------------------+  |
|  |  Forge (Builds)  |  |  Watchtower      |  |   Bittensor Consensus  |  |
|  |  (Cosign Signs)  |  |  (Monitoring)    |  |   (TAO Rewards)        |  |
|  +------------------+  +------------------+  +------------------------+  |
+==========================================================================+
```

**Figure 1:** HelixCluster + Chutes.ai Integration Architecture. HelixCluster control plane manages multiple GPU nodes, each running the full Chutes miner stack (K3s, GraVal, vLLM/SGLang inference engines). All communications are encrypted via the E2EE proxy layer using ML-KEM-768 post-quantum key encapsulation and ChaCha20-Poly1305 authenticated encryption [^3469^].

### 1.3 Integration Points Matrix

| Integration Point | Protocol | Direction | Data Flow | Security |
|---|---|---|---|---|
| Cluster -> Miner API | HTTPS + mTLS | Outbound | Inventory, deployment cmds | Bittensor signature |
| Miner -> Validator | WSS (Socket.IO) | Outbound | Heartbeat, metrics, events | Auto-TLS + E2EE |
| Client -> Inference | HTTPS + E2EE | Outbound | LLM prompts, responses | ML-KEM-768 + ChaCha20 |
| GraVal -> GPU | OpenCL/CUDA | Local | VRAM attestation, key derivation | Hardware-bound AES-256 |
| Registry -> Images | HTTPS | Outbound | Docker image pulls | Cosign signature verify |
| TEE -> Attestation | Intel TDX | Local | TD Quote, RTMR measurements | CPU-fused key signed |

### 1.4 Chutes.ai Background

Chutes (SN64) is a decentralized serverless AI computing platform built on the Bittensor network [^3628^]. Its core architectural components include:

- **Miners**: GPU compute providers that stake TAO, load "permanently hot" models into VRAM, and respond to inference requests with low cold-start latency [^3481^]
- **Validators**: Quality inspectors that score miners on latency (TTFT), throughput (TPS), and output accuracy, routing real business requests accordingly [^3628^]
- **GraVal**: A custom C/CUDA library providing "Proof of Consecutive VRAM Work" -- cryptographically attesting GPU authenticity through matrix multiplications seeded by device info [^3467^]
- **E2EE Proxy**: OpenResty-based local proxy using ML-KEM-768 post-quantum KEM + ChaCha20-Poly1305 AEAD for transparent end-to-end encryption [^3469^]
- **Forge**: Validator-side image build service that generates filesystem baselines, scans vulnerabilities, and cryptographically signs images with Sigstore Cosign [^3471^]
- **Watchtower**: Continuous monitoring service issuing randomized integrity challenges including software integrity checks and model-weight verification [^3471^]

The platform supports text (LLMs), image (diffusion), audio (TTS/STT), and video models through an OpenAI-compatible API at `https://llm.chutes.ai/v1` [^3629^].

---

## 2. HelixCluster-as-Miner Implementation

### 2.1 Architecture Design

HelixCluster GPU nodes run the complete `chutes-miner` stack within K3s clusters managed by the HelixCluster control plane. Each node requires:

1. **K3s** lightweight Kubernetes (managed by HelixCluster)
2. **chutes-miner** Python package with API, Gepetto, registry proxy
3. **GraVal bootstrap** for GPU attestation
4. **vLLM/SGLang** inference engines for model serving
5. **Redis** for pub/sub event handling
6. **PostgreSQL** for inventory tracking

### 2.2 Go Control Plane Integration

The HelixCluster control plane manages miner lifecycle through a dedicated `ChutesMinerController`:

```go
// pkg/chutes/miner_controller.go
package chutes

import (
    "context"
    "fmt"
    "time"

    corev1 "k8s.io/api/core/v1"
    metavav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/client-go/kubernetes"
)

// ChutesMinerConfig holds configuration for a Chutes miner node
type ChutesMinerConfig struct {
    NodeID           string            `json:"node_id"`
    ValidatorHotkey  string            `json:"validator_hotkey"`
    HourlyCostUSD    float64           `json:"hourly_cost_usd"`
    GPUShortRef      string            `json:"gpu_short_ref"`     // e.g., "h100", "a6000"
    GPUCount         int               `json:"gpu_count"`
    BittensorColdkey string            `json:"bittensor_coldkey"`
    BittensorHotkey  string            `json:"bittensor_hotkey"`
    CacheMaxSizeGB   int               `json:"cache_max_size_gb"`
    CacheMaxAgeDays  int               `json:"cache_max_age_days"`
    CustomImages     []string          `json:"custom_images"`     // Additional chute images
    NodeSelector     map[string]string `json:"node_selector"`     // K8s node affinity
    TEEEnabled       bool              `json:"tee_enabled"`       // Intel TDX support
}

// MinerController manages chutes-miner lifecycle on HelixCluster nodes
type MinerController struct {
    k8sClient kubernetes.Interface
    namespace string
    validatorConfig *ValidatorConfig
}

type ValidatorConfig struct {
    Hotkey   string `json:"hotkey"`
    Registry string `json:"registry"`
    API      string `json:"api"`
    Socket   string `json:"socket"`
}

// Default mainnet validator configuration
var DefaultValidators = []ValidatorConfig{
    {
        Hotkey:   "5Dt7HZ7Zpw4DppPxFM7Ke3Cm7sDAWhsZXmM5ZAmE7dSVJbcQ",
        Registry: "registry.chutes.ai",
        API:      "https://api.chutes.ai",
        Socket:   "wss://ws.chutes.ai",
    },
}

// DeployMiner installs the complete chutes-miner stack on a HelixCluster GPU node
func (mc *MinerController) DeployMiner(ctx context.Context, cfg ChutesMinerConfig) error {
    // Step 1: Ensure namespace exists
    if err := mc.ensureNamespace(ctx); err != nil {
        return fmt.Errorf("ensure namespace: %w", err)
    }

    // Step 2: Deploy PostgreSQL for inventory tracking
    if err := mc.deployPostgres(ctx, cfg); err != nil {
        return fmt.Errorf("deploy postgres: %w", err)
    }

    // Step 3: Deploy Redis for pub/sub
    if err := mc.deployRedis(ctx, cfg); err != nil {
        return fmt.Errorf("deploy redis: %w", err)
    }

    // Step 4: Deploy GraVal bootstrap daemon
    if err := mc.deployGraValBootstrap(ctx, cfg); err != nil {
        return fmt.Errorf("deploy graval bootstrap: %w", err)
    }

    // Step 5: Deploy miner API service
    if err := mc.deployMinerAPI(ctx, cfg); err != nil {
        return fmt.Errorf("deploy miner api: %w", err)
    }

    // Step 6: Deploy Gepetto (chute management)
    if err := mc.deployGepetto(ctx, cfg); err != nil {
        return fmt.Errorf("deploy gepetto: %w", err)
    }

    // Step 7: Deploy registry proxy
    if err := mc.deployRegistryProxy(ctx, cfg); err != nil {
        return fmt.Errorf("deploy registry proxy: %w", err)
    }

    // Step 8: Deploy GPU operator and device plugin
    if err := mc.deployGPUOperator(ctx, cfg); err != nil {
        return fmt.Errorf("deploy gpu operator: %w", err)
    }

    // Step 9: Wait for all pods ready
    if err := mc.waitForReady(ctx, cfg.NodeID, 5*time.Minute); err != nil {
        return fmt.Errorf("wait for ready: %w", err)
    }

    return nil
}

// deployGraValBootstrap installs the GPU attestation bootstrapper
func (mc *MinerController) deployGraValBootstrap(ctx context.Context, cfg ChutesMinerConfig) error {
    // GraVal performs "Proof of Consecutive VRAM Work" using OpenCL/clBLAS
    // It creates a unique AES-256 key tied to verified physical GPU properties
    // See: https://github.com/chutesai/graval
    daemonSet := &appsv1.DaemonSet{
        ObjectMeta: metav1.ObjectMeta{
            Name:      fmt.Sprintf("graval-bootstrap-%s", cfg.NodeID),
            Namespace: mc.namespace,
            Labels: map[string]string{
                "app.kubernetes.io/name":       "graval-bootstrap",
                "app.kubernetes.io/component":  "gpu-attestation",
                "helixcluster.io/node-id":      cfg.NodeID,
                "helixcluster.io/tee-enabled":  fmt.Sprintf("%v", cfg.TEEEnabled),
            },
        },
        Spec: appsv1.DaemonSetSpec{
            Selector: &metav1.LabelSelector{
                MatchLabels: map[string]string{
                    "app": "graval-bootstrap",
                },
            },
            Template: corev1.PodTemplateSpec{
                Spec: corev1.PodSpec{
                    NodeSelector: cfg.NodeSelector,
                    HostNetwork:  true,
                    Containers: []corev1.Container{
                        {
                            Name:  "graval-bootstrap",
                            Image: "chutesai/graval-bootstrap:latest",
                            SecurityContext: &corev1.SecurityContext{
                                Privileged: boolPtr(true),
                                Capabilities: &corev1.Capabilities{
                                    Add: []corev1.Capability{"SYS_ADMIN"},
                                },
                            },
                            VolumeMounts: []corev1.VolumeMount{
                                {Name: "dev-nvidia", MountPath: "/dev/nvidia*"},
                                {Name: "usr-local-cuda", MountPath: "/usr/local/cuda"},
                            },
                            Env: []corev1.EnvVar{
                                {Name: "GRAVAL_VRAM_THRESHOLD", Value: "0.95"},
                                {Name: "GRAVAL_CHALLENGE_ROUNDS", Value: "256"},
                                {Name: "NODE_ID", Value: cfg.NodeID},
                                {Name: "TEE_MODE", Value: fmt.Sprintf("%v", cfg.TEEEnabled)},
                            },
                        },
                    },
                    Volumes: []corev1.Volume{
                        {Name: "dev-nvidia", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/dev"}}},
                        {Name: "usr-local-cuda", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/usr/local/cuda"}}},
                    },
                },
            },
        },
    }

    _, err := mc.k8sClient.AppsV1().DaemonSets(mc.namespace).Create(ctx, daemonSet, metav1.CreateOptions{})
    return err
}

// RegisterNode adds a GPU node to the Chutes network inventory
func (mc *MinerController) RegisterNode(ctx context.Context, cfg ChutesMinerConfig) error {
    // This maps to: chutes-miner add-node --name ... --validator ... --hourly-cost ...
    // See: https://github.com/chutesai/chutes-miner README
    nodeRegistration := NodeRegistrationRequest{
        Name:         cfg.NodeID,
        Validator:    cfg.ValidatorHotkey,
        HourlyCost:   cfg.HourlyCostUSD,
        GPUShortRef:  cfg.GPUShortRef,
        Hotkey:       cfg.BittensorHotkey,
        MinerAPI:     fmt.Sprintf("http://%s:32000", cfg.NodeID),
        GPUCount:     cfg.GPUCount,
        TEEEnabled:   cfg.TEEEnabled,
    }

    // Sign request with Bittensor hotkey
    signature, err := mc.signRequest(nodeRegistration, cfg.BittensorHotkey)
    if err != nil {
        return fmt.Errorf("sign registration: %w", err)
    }
    nodeRegistration.Signature = signature

    return mc.submitToMinerAPI(ctx, "/nodes/register", nodeRegistration)
}

func boolPtr(b bool) *bool { return &b }
```

### 2.3 Python Chute Deployment Template

HelixCluster workloads can deploy custom chutes using the Chutes SDK:

```python
# helixcluster/chutes/deploy_llm.py
"""Deploy LLM inference chutes on HelixCluster-managed GPU nodes."""

import os
from chutes.chute import Chute, NodeSelector
from chutes.chute.template.vllm import build_vllm_chute
from chutes.chute.template.sglang import build_sglang_chute
from chutes.image import Image

def deploy_helixcluster_chute(
    model_name: str,
    username: str = "helixcluster",
    gpu_count: int = 1,
    min_vram_gb: int = 24,
    concurrency: int = 8,
    engine: str = "sglang",  # "vllm" or "sglang"
    tee_only: bool = True,
    exclude_gpus: list[str] = None,
) -> Chute:
    """
    Deploy an LLM chute optimized for HelixCluster infrastructure.
    
    Args:
        model_name: HuggingFace model ID (e.g., "meta-llama/Llama-3.1-70B")
        username: Chutes platform username
        gpu_count: Number of GPUs required
        min_vram_gb: Minimum VRAM per GPU in GB
        concurrency: Max concurrent requests
        engine: Inference engine ("vllm" or "sglang")
        tee_only: Only deploy on TEE-capable nodes
        exclude_gpus: GPU types to exclude (e.g., ["mi300x"])
    """
    
    node_selector = NodeSelector(
        gpu_count=gpu_count,
        min_vram_gb_per_gpu=min_vram_gb,
        exclude=exclude_gpus or [],
        confidential_compute=tee_only,
    )
    
    # Build chute using the appropriate template
    if engine == "sglang":
        chute = build_sglang_chute(
            username=username,
            readme=f"## {model_name}\nDeployed via HelixCluster",
            model_name=model_name,
            image="chutes/sglang:latest",
            concurrency=concurrency,
            node_selector=node_selector,
            engine_args=(
                "--trust-remote-code "
                "--tp-size {} ".format(gpu_count) +
                "--enable-torch-compile"
            ),
        )
    elif engine == "vllm":
        chute = build_vllm_chute(
            username=username,
            readme=f"## {model_name}\nDeployed via HelixCluster",
            model_name=model_name,
            image="chutes/vllm:latest",
            concurrency=concurrency,
            node_selector=node_selector,
        )
    else:
        raise ValueError(f"Unknown engine: {engine}")
    
    return chute

# Example: Deploy DeepSeek-V3 on 8x H100 GPUs
if __name__ == "__main__":
    chute = deploy_helixcluster_chute(
        model_name="deepseek-ai/DeepSeek-V3",
        gpu_count=8,
        min_vram_gb=140,
        concurrency=20,
        engine="sglang",
        tee_only=True,
        exclude_gpus=["mi300x", "b200"],
    )
    print(f"Chute deployed: {chute.chute_id}")
```

### 2.4 Auto-Discovery and Registration

```python
# helixcluster/chutes/node_discovery.py
"""Automatic GPU discovery and Chutes miner registration."""

import subprocess
import json
import requests
from dataclasses import dataclass
from typing import List, Optional

@dataclass
class GPUSpec:
    uuid: str
    name: str
    vram_gb: int
    vendor: str  # "nvidia", "amd", "apple"
    pci_id: str
    supports_tee: bool

class GPUDiscovery:
    """Discovers GPUs across HelixCluster nodes and prepares Chutes registration."""
    
    NVIDIA_SUPPORTED = [
        "h100", "h200", "a100", "a6000", "l40s", "l40",
        "rtx4090", "rtx3090", "a40", "v100"
    ]
    AMD_SUPPORTED = ["mi300x", "mi250x", "w7900"]
    
    def discover_nvidia_gpus(self) -> List[GPUSpec]:
        """Use nvidia-smi to discover NVIDIA GPUs."""
        try:
            result = subprocess.run(
                ["nvidia-smi", "--query-gpu=gpu_uuid,gpu_name,memory.total,pci.bus_id",
                 "--format=json"],
                capture_output=True, text=True, check=True
            )
            gpus = json.loads(result.stdout)
            return [
                GPUSpec(
                    uuid=g["gpu_uuid"],
                    name=g["gpu_name"],
                    vram_gb=g["memory.total"] // 1024,  # MiB -> GiB
                    vendor="nvidia",
                    pci_id=g["pci.bus_id"],
                    supports_tee=self._check_tee_support(g["gpu_name"]),
                )
                for g in gpus
            ]
        except (subprocess.CalledProcessError, FileNotFoundError):
            return []
    
    def discover_amd_gpus(self) -> List[GPUSpec]:
        """Use rocm-smi to discover AMD GPUs."""
        try:
            result = subprocess.run(
                ["rocm-smi", "--showid", "--json"],
                capture_output=True, text=True, check=True
            )
            data = json.loads(result.stdout)
            gpus = []
            for card_id, info in data.items():
                gpus.append(GPUSpec(
                    uuid=info.get("UUID", card_id),
                    name=info.get("Card series", "Unknown AMD"),
                    vram_gb=self._parse_amd_vram(info),
                    vendor="amd",
                    pci_id=info.get("PCI Bus", ""),
                    supports_tee=False,  # AMD TEE support pending
                ))
            return gpus
        except (subprocess.CalledProcessError, FileNotFoundError):
            return []
    
    def _check_tee_support(self, gpu_name: str) -> bool:
        """Check if GPU supports confidential computing (NVIDIA H100+)."""
        tee_capable = ["h100", "h200", "h800"]
        return any(t in gpu_name.lower() for t in tee_capable)
    
    def _parse_amd_vram(self, info: dict) -> int:
        """Parse AMD VRAM from rocm-smi output."""
        vram_str = info.get("VRAM Total Memory (B)", "0")
        try:
            return int(vram_str) // (1024**3)
        except ValueError:
            return 0
    
    def map_to_chutes_ref(self, gpu: GPUSpec) -> Optional[str]:
        """Map discovered GPU to Chutes short reference string."""
        name_lower = gpu.name.lower()
        
        if gpu.vendor == "nvidia":
            for ref in self.NVIDIA_SUPPORTED:
                if ref in name_lower.replace(" ", "").replace("-", ""):
                    # Add SXM/PCIe suffix based on bus info
                    if "sm" in name_lower or "sxm" in name_lower:
                        return f"{ref}_sxm"
                    return ref
        elif gpu.vendor == "amd":
            for ref in self.AMD_SUPPORTED:
                if ref in name_lower:
                    return ref
        
        return None
```

---

## 3. Chutes API Client for HelixCluster

### 3.1 Go Client Implementation

```go
// pkg/chutes/client.go
package chutes

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "time"

    "github.com/helixcluster/pkg/e2ee"
)

const (
    DefaultBaseURL      = "https://llm.chutes.ai/v1"
    DefaultAPIBaseURL   = "https://api.chutes.ai"
    APIKeyPrefix        = "cpk_"
)

// Client provides access to the Chutes.ai API
type Client struct {
    apiKey      string
    baseURL     string
    apiBaseURL  string
    httpClient  *http.Client
    e2eeProxy   *e2ee.Proxy  // Optional E2EE proxy for encrypted inference
}

// ClientOption configures the Chutes client
type ClientOption func(*Client)

func WithBaseURL(url string) ClientOption {
    return func(c *Client) { c.baseURL = url }
}

func WithE2EEProxy(proxy *e2ee.Proxy) ClientOption {
    return func(c *Client) { c.e2eeProxy = proxy }
}

func WithHTTPClient(hc *http.Client) ClientOption {
    return func(c *Client) { c.httpClient = hc }
}

// NewClient creates a new Chutes API client
func NewClient(apiKey string, opts ...ClientOption) (*Client, error) {
    if apiKey == "" {
        return nil, fmt.Errorf("API key is required (prefix: %s)", APIKeyPrefix)
    }
    
    c := &Client{
        apiKey:     apiKey,
        baseURL:    DefaultBaseURL,
        apiBaseURL: DefaultAPIBaseURL,
        httpClient: &http.Client{Timeout: 120 * time.Second},
    }
    
    for _, opt := range opts {
        opt(c)
    }
    
    return c, nil
}

// ChatCompletionRequest mirrors the OpenAI chat completions API
type ChatCompletionRequest struct {
    Model       string          `json:"model"`
    Messages    []ChatMessage   `json:"messages"`
    MaxTokens   int             `json:"max_tokens,omitempty"`
    Temperature float64         `json:"temperature,omitempty"`
    TopP        float64         `json:"top_p,omitempty"`
    Stream      bool            `json:"stream,omitempty"`
    Tools       []Tool          `json:"tools,omitempty"`
}

type ChatMessage struct {
    Role       string     `json:"role"`
    Content    string     `json:"content"`
    ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

type Tool struct {
    Type     string   `json:"type"`
    Function Function `json:"function"`
}

type Function struct {
    Name        string                 `json:"name"`
    Description string                 `json:"description"`
    Parameters  map[string]interface{} `json:"parameters"`
}

type ToolCall struct {
    ID       string `json:"id"`
    Type     string `json:"type"`
    Function struct {
        Name      string `json:"name"`
        Arguments string `json:"arguments"`
    } `json:"function"`
}

// ChatCompletionResponse mirrors OpenAI's response format
type ChatCompletionResponse struct {
    ID      string   `json:"id"`
    Object  string   `json:"object"`
    Created int64    `json:"created"`
    Model   string   `json:"model"`
    Choices []struct {
        Index   int         `json:"index"`
        Message ChatMessage `json:"message"`
        Delta   *ChatMessage `json:"delta,omitempty"`
        FinishReason string `json:"finish_reason"`
    } `json:"choices"`
    Usage struct {
        PromptTokens     int `json:"prompt_tokens"`
        CompletionTokens int `json:"completion_tokens"`
        TotalTokens      int `json:"total_tokens"`
    } `json:"usage"`
}

// CreateChatCompletion sends a chat completion request (OpenAI-compatible)
func (c *Client) CreateChatCompletion(ctx context.Context, req ChatCompletionRequest) (*ChatCompletionResponse, error) {
    // Apply intelligent model routing
    if req.Model == "default" {
        req.Model = c.resolveDefaultModel(ctx, "latency")
    }
    
    body, err := json.Marshal(req)
    if err != nil {
        return nil, fmt.Errorf("marshal request: %w", err)
    }
    
    // Use E2EE proxy if available for encrypted inference
    url := fmt.Sprintf("%s/chat/completions", c.baseURL)
    if c.e2eeProxy != nil {
        url = c.e2eeProxy.GetEndpoint("/v1/chat/completions")
    }
    
    httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
    if err != nil {
        return nil, err
    }
    
    httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
    httpReq.Header.Set("Content-Type", "application/json")
    
    resp, err := c.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("HTTP request failed: %w", err)
    }
    defer resp.Body.Close()
    
    if resp.StatusCode != http.StatusOK {
        bodyBytes, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(bodyBytes))
    }
    
    var result ChatCompletionResponse
    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return nil, fmt.Errorf("decode response: %w", err)
    }
    
    return &result, nil
}

// ModelInfo represents a model available on Chutes
type ModelInfo struct {
    ID                   string  `json:"id"`
    Object               string  `json:"object"`
    OwnedBy              string  `json:"owned_by"`
    ConfidentialCompute  bool    `json:"confidential_compute"`
    Pricing              struct {
        Prompt     float64 `json:"prompt_per_million"`
        Completion float64 `json:"completion_per_million"`
    } `json:"pricing"`
    ContextLength int `json:"context_length"`
}

// ListModels returns all available models with their TEE status and pricing
func (c *Client) ListModels(ctx context.Context) ([]ModelInfo, error) {
    url := fmt.Sprintf("%s/models", c.baseURL)
    
    req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
    if err != nil {
        return nil, err
    }
    req.Header.Set("Authorization", "Bearer "+c.apiKey)
    
    resp, err := c.httpClient.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
    
    var result struct {
        Object string      `json:"object"`
        Data   []ModelInfo `json:"data"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return nil, err
    }
    
    return result.Data, nil
}

// resolveDefaultModel implements intelligent model routing
func (c *Client) resolveDefaultModel(ctx context.Context, strategy string) string {
    // Query available models and select based on strategy
    // Strategy: "latency" -> lowest TTFT, "throughput" -> highest TPS
    models, err := c.ListModels(ctx)
    if err != nil {
        return "deepseek-ai/DeepSeek-V3-0324"  // Fallback
    }
    
    // Filter for TEE-only models if E2EE proxy is active
    if c.e2eeProxy != nil {
        teeModels := make([]ModelInfo, 0)
        for _, m := range models {
            if m.ConfidentialCompute {
                teeModels = append(teeModels, m)
            }
        }
        models = teeModels
    }
    
    if len(models) == 0 {
        return "deepseek-ai/DeepSeek-V3-0324"
    }
    
    // For latency strategy, prefer smaller models on fast hardware
    // For throughput strategy, prefer larger models with good batching
    return models[0].ID
}

// UserInfo represents account information including balance
type UserInfo struct {
    Username       string  `json:"username"`
    UserID         string  `json:"user_id"`
    Balance        float64 `json:"balance"`
    PaymentAddress string  `json:"payment_address"`
    Hotkey         string  `json:"hotkey"`
    Coldkey        string  `json:"coldkey"`
    Quotas         []Quota `json:"quotas"`
}

type Quota struct {
    ChuteID           string  `json:"chute_id"`
    Quota             float64 `json:"quota"`
    IsDefault         bool    `json:"is_default"`
    PaymentRefreshDate string `json:"payment_refresh_date"`
}

// GetUserInfo retrieves account details including balance
func (c *Client) GetUserInfo(ctx context.Context) (*UserInfo, error) {
    url := fmt.Sprintf("%s/users/me", c.apiBaseURL)
    
    req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
    if err != nil {
        return nil, err
    }
    req.Header.Set("Authorization", "Bearer "+c.apiKey)
    
    resp, err := c.httpClient.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
    
    var result UserInfo
    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return nil, err
    }
    
    return &result, nil
}
```

### 3.2 Streaming Chat Completions

```go
// pkg/chutes/streaming.go
package chutes

import (
    "bufio"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "strings"
)

// StreamChatCompletion sends a streaming chat completion request
func (c *Client) StreamChatCompletion(ctx context.Context, req ChatCompletionRequest) (<-chan ChatCompletionResponse, <-chan error) {
    responseChan := make(chan ChatCompletionResponse, 10)
    errorChan := make(chan error, 1)
    
    go func() {
        defer close(responseChan)
        defer close(errorChan)
        
        req.Stream = true
        body, _ := json.Marshal(req)
        
        url := fmt.Sprintf("%s/chat/completions", c.baseURL)
        httpReq, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
        httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
        httpReq.Header.Set("Content-Type", "application/json")
        httpReq.Header.Set("Accept", "text/event-stream")
        
        resp, err := c.httpClient.Do(httpReq)
        if err != nil {
            errorChan <- fmt.Errorf("HTTP request: %w", err)
            return
        }
        defer resp.Body.Close()
        
        reader := bufio.NewReader(resp.Body)
        for {
            line, err := reader.ReadString('\n')
            if err == io.EOF {
                break
            }
            if err != nil {
                errorChan <- fmt.Errorf("read stream: %w", err)
                return
            }
            
            line = strings.TrimSpace(line)
            if !strings.HasPrefix(line, "data: ") {
                continue
            }
            
            data := strings.TrimPrefix(line, "data: ")
            if data == "[DONE]" {
                break
            }
            
            var chunk ChatCompletionResponse
            if err := json.Unmarshal([]byte(data), &chunk); err != nil {
                continue  // Skip malformed chunks
            }
            
            select {
            case responseChan <- chunk:
            case <-ctx.Done():
                return
            }
        }
    }()
    
    return responseChan, errorChan
}
```

---

## 4. Unified Marketplace Manager

### 4.1 Architecture

The Unified Marketplace Manager enables HelixCluster nodes to simultaneously participate in multiple decentralized compute marketplaces (Chutes.ai, io.net, Akash, Salad) and automatically routes workloads to the highest-bidding platform.

```
+==========================================================================+
|                  UNIFIED MARKETPLACE MANAGER                              |
|                                                                           |
|  +----------------+  +----------------+  +----------------+               |
|  | Chutes Adapter |  | io.net Adapter |  | Akash Adapter  |               |
|  | (SN64/Bittensor)|  | (Solana/Ray)   |  | (Cosmos/K8s)   |               |
|  +--------+-------+  +--------+-------+  +--------+-------+               |
|           |                   |                   |                       |
|  +--------v-------------------v-------------------v-------+               |
|  |               Price Discovery Engine                    |               |
|  |  (Latency-weighted price comparison with fallback)      |               |
|  +--------+-------------------+-------------------+-------+               |
|           |                   |                   |                       |
|  +--------v-------------------v-------------------v-------+               |
|  |            Revenue Optimizer (Linear Programming)        |               |
|  |  Maximize: sum(tokens_i * price_i * availability_i)      |               |
|  +---------------------------+---------------------------+               |
|                               |                                           |
|  +----------------------------v--------------------------+               |
|  |              Workload Router (HelixCluster Scheduler)   |               |
|  +--------------------------------------------------------+               |
+==========================================================================+
```

### 4.2 Go Implementation

```go
// pkg/marketplace/manager.go
package marketplace

import (
    "context"
    "fmt"
    "math"
    "sort"
    "sync"
    "time"
)

// MarketplaceType identifies the compute marketplace
type MarketplaceType string

const (
    MarketplaceChutes  MarketplaceType = "chutes"
    MarketplaceIONet   MarketplaceType = "io.net"
    MarketplaceAkash   MarketplaceType = "akash"
    MarketplaceSalad   MarketplaceType = "salad"
)

// MarketplaceAdapter interface for all marketplace integrations
type MarketplaceAdapter interface {
    Name() MarketplaceType
    GetCurrentPricing(ctx context.Context, gpuType string) (*PricingInfo, error)
    SubmitWork(ctx context.Context, workload WorkloadSpec) (*WorkResult, error)
    GetEarnings(ctx context.Context, period time.Duration) (*EarningsReport, error)
    HealthCheck(ctx context.Context) (HealthStatus, error)
    WithdrawEarnings(ctx context.Context, destination string) error
}

// PricingInfo represents current pricing for a GPU type
type PricingInfo struct {
    GPUType           string    `json:"gpu_type"`
    PricePerHourUSD   float64   `json:"price_per_hour_usd"`
    PricePerTokenUSD  float64   `json:"price_per_token_usd"`  // For inference
    Availability      float64   `json:"availability"`          // 0.0 - 1.0
    AvgLatencyMs      float64   `json:"avg_latency_ms"`
    ThroughputTokensPS float64  `json:"throughput_tokens_per_sec"`
    StakingRequired   float64   `json:"staking_required"`      // Native token amount
    RewardToken       string    `json:"reward_token"`          // e.g., "TAO", "IO", "AKT"
    Timestamp         time.Time `json:"timestamp"`
}

// WorkloadSpec defines a compute workload
type WorkloadSpec struct {
    WorkloadType     string            `json:"workload_type"`      // "inference", "training", "rendering"
    GPURequirements  GPURequirements   `json:"gpu_requirements"`
    DurationEstimate time.Duration     `json:"duration_estimate"`
    DataSizeGB       float64           `json:"data_size_gb"`
    Priority         int               `json:"priority"`           // 1-10
    TEERequired      bool              `json:"tee_required"`
    Labels           map[string]string `json:"labels"`
}

type GPURequirements struct {
    Count      int     `json:"count"`
    MinVRAMGB  int     `json:"min_vram_gb"`
    Vendor     string  `json:"vendor"`       // "nvidia", "amd", "any"
    ModelPref  string  `json:"model_pref"`   // e.g., "h100", "a100"
}

// WorkResult contains the outcome of a workload submission
type WorkResult struct {
    WorkloadID    string    `json:"workload_id"`
    Marketplace   string    `json:"marketplace"`
    GPUAssigned   string    `json:"gpu_assigned"`
    PricePerHour  float64   `json:"price_per_hour"`
    EstimatedCost float64   `json:"estimated_cost"`
    StartedAt     time.Time `json:"started_at"`
}

// EarningsReport represents earnings from a marketplace
type EarningsReport struct {
    Marketplace   string             `json:"marketplace"`
    TotalEarned   float64            `json:"total_earned"`    // In USD
    TokenEarnings map[string]float64 `json:"token_earnings"`  // Per-token breakdown
    Period        time.Duration      `json:"period"`
    Workloads     int                `json:"workloads_completed"`
}

type HealthStatus struct {
    Healthy   bool   `json:"healthy"`
    LatencyMs int64  `json:"latency_ms"`
    Message   string `json:"message,omitempty"`
}

// UnifiedManager manages multiple marketplace adapters
type UnifiedManager struct {
    adapters   map[MarketplaceType]MarketplaceAdapter
    gpuNodes   map[string]*GPUNode  // node_id -> GPU node
    mu         sync.RWMutex
    optimizer  *RevenueOptimizer
}

// NewUnifiedManager creates a new marketplace manager
func NewUnifiedManager() *UnifiedManager {
    return &UnifiedManager{
        adapters:  make(map[MarketplaceType]MarketplaceAdapter),
        gpuNodes:  make(map[string]*GPUNode),
        optimizer: NewRevenueOptimizer(),
    }
}

// RegisterAdapter adds a marketplace adapter
func (um *UnifiedManager) RegisterAdapter(adapter MarketplaceAdapter) {
    um.mu.Lock()
    defer um.mu.Unlock()
    um.adapters[adapter.Name()] = adapter
}

// RouteWorkload determines the best marketplace for a workload
func (um *UnifiedManager) RouteWorkload(ctx context.Context, workload WorkloadSpec) (*WorkResult, error) {
    um.mu.RLock()
    adapters := make([]MarketplaceAdapter, 0, len(um.adapters))
    for _, a := range um.adapters {
        adapters = append(adapters, a)
    }
    um.mu.RUnlock()
    
    if len(adapters) == 0 {
        return nil, fmt.Errorf("no marketplace adapters registered")
    }
    
    // Gather pricing from all marketplaces concurrently
    type pricingResult struct {
        adapter MarketplaceAdapter
        pricing *PricingInfo
        err     error
    }
    
    results := make(chan pricingResult, len(adapters))
    for _, adapter := range adapters {
        go func(a MarketplaceAdapter) {
            pricing, err := a.GetCurrentPricing(ctx, workload.GPURequirements.ModelPref)
            results <- pricingResult{adapter: a, pricing: pricing, err: err}
        }(adapter)
    }
    
    // Collect results with scoring
    var bestAdapter MarketplaceAdapter
    bestScore := -1.0
    
    for i := 0; i < len(adapters); i++ {
        select {
        case <-ctx.Done():
            return nil, ctx.Err()
        case result := <-results:
            if result.err != nil || result.pricing == nil {
                continue
            }
            
            // Composite scoring: price * availability / latency
            // Higher score = better marketplace
            score := um.calculateMarketplaceScore(result.pricing, workload)
            
            if score > bestScore {
                bestScore = score
                bestAdapter = result.adapter
            }
        }
    }
    
    if bestAdapter == nil {
        // Fallback: try each adapter directly
        for _, adapter := range adapters {
            result, err := adapter.SubmitWork(ctx, workload)
            if err == nil {
                return result, nil
            }
        }
        return nil, fmt.Errorf("no marketplace could accept workload")
    }
    
    return bestAdapter.SubmitWork(ctx, workload)
}

// calculateMarketplaceScore computes a composite score for marketplace selection
func (um *UnifiedManager) calculateMarketplaceScore(p *PricingInfo, w WorkloadSpec) float64 {
    // Normalize metrics to 0-1 range
    priceScore := 1.0 / (1.0 + p.PricePerHourUSD)  // Lower price = higher score
    availScore := p.Availability                    // Direct availability
    latencyScore := 1.0 / (1.0 + p.AvgLatencyMs/1000.0)  // Lower latency = higher score
    throughputScore := math.Min(p.ThroughputTokensPS/1000.0, 1.0)  // Cap at 1000 tps
    
    // Weighted combination
    score := priceScore*0.30 + availScore*0.30 + latencyScore*0.20 + throughputScore*0.20
    
    // Penalize if TEE required but not available (score = 0)
    if w.TEERequired && p.Availability < 0.5 {
        score *= 0.1
    }
    
    return score
}

// GetAllEarnings aggregates earnings across all marketplaces
func (um *UnifiedManager) GetAllEarnings(ctx context.Context, period time.Duration) (*UnifiedEarnings, error) {
    um.mu.RLock()
    adapters := make([]MarketplaceAdapter, 0, len(um.adapters))
    for _, a := range um.adapters {
        adapters = append(adapters, a)
    }
    um.mu.RUnlock()
    
    unified := &UnifiedEarnings{
        Period:        period,
        ByMarketplace: make(map[string]*EarningsReport),
        TokenTotals:   make(map[string]float64),
    }
    
    var mu sync.Mutex
    var wg sync.WaitGroup
    
    for _, adapter := range adapters {
        wg.Add(1)
        go func(a MarketplaceAdapter) {
            defer wg.Done()
            report, err := a.GetEarnings(ctx, period)
            if err != nil {
                return
            }
            mu.Lock()
            unified.ByMarketplace[string(a.Name())] = report
            unified.TotalUSD += report.TotalEarned
            for token, amount := range report.TokenEarnings {
                unified.TokenTotals[token] += amount
            }
            mu.Unlock()
        }(adapter)
    }
    
    wg.Wait()
    return unified, nil
}

type UnifiedEarnings struct {
    TotalUSD      float64                    `json:"total_usd"`
    ByMarketplace map[string]*EarningsReport `json:"by_marketplace"`
    TokenTotals   map[string]float64         `json:"token_totals"`
    Period        time.Duration              `json:"period"`
}
```

### 4.3 Chutes Marketplace Adapter

```go
// pkg/marketplace/chutes_adapter.go
package marketplace

import (
    "context"
    "fmt"
    "time"

    "github.com/helixcluster/pkg/chutes"
)

// ChutesAdapter implements MarketplaceAdapter for Chutes.ai
type ChutesAdapter struct {
    client      *chutes.Client
    gpuNodes    map[string]*GPUNode
    validatorHotkey string
}

type GPUNode struct {
    NodeID      string  `json:"node_id"`
    GPUType     string  `json:"gpu_type"`
    GPUCount    int     `json:"gpu_count"`
    HourlyCost  float64 `json:"hourly_cost"`
    IsActive    bool    `json:"is_active"`
    TEEEnabled  bool    `json:"tee_enabled"`
}

// NewChutesAdapter creates a Chutes marketplace adapter
func NewChutesAdapter(apiKey, validatorHotkey string) (*ChutesAdapter, error) {
    client, err := chutes.NewClient(apiKey)
    if err != nil {
        return nil, err
    }
    
    return &ChutesAdapter{
        client:          client,
        gpuNodes:        make(map[string]*GPUNode),
        validatorHotkey: validatorHotkey,
    }, nil
}

func (ca *ChutesAdapter) Name() MarketplaceType {
    return MarketplaceChutes
}

func (ca *ChutesAdapter) GetCurrentPricing(ctx context.Context, gpuType string) (*PricingInfo, error) {
    // Fetch available models to estimate pricing
    models, err := ca.client.ListModels(ctx)
    if err != nil {
        return nil, fmt.Errorf("list models: %w", err)
    }
    
    // Calculate weighted average pricing across available models
    var totalPromptPrice, totalCompletionPrice float64
    var teeCount, totalCount int
    
    for _, model := range models {
        totalPromptPrice += model.Pricing.Prompt
        totalCompletionPrice += model.Pricing.Completion
        totalCount++
        if model.ConfidentialCompute {
            teeCount++
        }
    }
    
    if totalCount == 0 {
        return nil, fmt.Errorf("no models available")
    }
    
    // Estimate GPU pricing based on model pricing and network utilization
    // Chutes pricing is ~85% cheaper than AWS [^3628^]
    avgPricePerHour := ca.estimateGPUHourlyRate(gpuType, totalPromptPrice/float64(totalCount))
    
    return &PricingInfo{
        GPUType:            gpuType,
        PricePerHourUSD:    avgPricePerHour,
        PricePerTokenUSD:   totalPromptPrice / float64(totalCount) / 1e6,
        Availability:       float64(totalCount) / 100.0,  // Normalized
        AvgLatencyMs:       50.0,  // Typical Chutes latency
        ThroughputTokensPS: 100.0, // Conservative estimate
        StakingRequired:    ca.getStakingRequirement(),
        RewardToken:        "TAO",
        Timestamp:          time.Now(),
    }, nil
}

func (ca *ChutesAdapter) estimateGPUHourlyRate(gpuType string, avgPrice float64) float64 {
    // Chutes pricing: ~85% cheaper than AWS [^3628^]
    // AWS H100: ~$2.50/hr -> Chutes: ~$0.375/hr
    baseRates := map[string]float64{
        "h100": 0.40, "h200": 0.60, "a100": 0.30, "a6000": 0.15,
        "l40s": 0.12, "l40": 0.10, "rtx4090": 0.08, "rtx3090": 0.05,
    }
    
    if rate, ok := baseRates[gpuType]; ok {
        return rate
    }
    return 0.20  // Default rate
}

func (ca *ChutesAdapter) getStakingRequirement() float64 {
    // Bittensor subnet 64 staking requirement
    // Varies based on validator and network conditions
    return 100.0  // TAO tokens (approximate)
}

func (ca *ChutesAdapter) SubmitWork(ctx context.Context, workload WorkloadSpec) (*WorkResult, error) {
    // For inference workloads, create a chat completion request
    if workload.WorkloadType == "inference" {
        req := chutes.ChatCompletionRequest{
            Model: "default:throughput",  // Route for throughput
            Messages: []chutes.ChatMessage{
                {Role: "user", Content: workload.Labels["prompt"]},
            },
            MaxTokens: workload.Labels["max_tokens"],
        }
        
        resp, err := ca.client.CreateChatCompletion(ctx, req)
        if err != nil {
            return nil, fmt.Errorf("chat completion: %w", err)
        }
        
        return &WorkResult{
            WorkloadID:    resp.ID,
            Marketplace:   string(MarketplaceChutes),
            GPUAssigned:   workload.GPURequirements.ModelPref,
            PricePerHour:  ca.estimateGPUHourlyRate(workload.GPURequirements.ModelPref, 0),
            EstimatedCost: float64(resp.Usage.TotalTokens) * 0.000001, // Rough estimate
            StartedAt:     time.Now(),
        }, nil
    }
    
    return nil, fmt.Errorf("workload type %s not supported by Chutes adapter", workload.WorkloadType)
}

func (ca *ChutesAdapter) GetEarnings(ctx context.Context, period time.Duration) (*EarningsReport, error) {
    userInfo, err := ca.client.GetUserInfo(ctx)
    if err != nil {
        return nil, fmt.Errorf("get user info: %w", err)
    }
    
    return &EarningsReport{
        Marketplace:   string(MarketplaceChutes),
        TotalEarned:   userInfo.Balance,
        TokenEarnings: map[string]float64{"TAO": 0}, // Requires on-chain query
        Period:        period,
        Workloads:     0,
    }, nil
}

func (ca *ChutesAdapter) HealthCheck(ctx context.Context) (HealthStatus, error) {
    _, err := ca.client.ListModels(ctx)
    if err != nil {
        return HealthStatus{Healthy: false, Message: err.Error()}, nil
    }
    return HealthStatus{Healthy: true, LatencyMs: 100}, nil
}

func (ca *ChutesAdapter) WithdrawEarnings(ctx context.Context, destination string) error {
    // Withdraw TAO from Bittensor
    return fmt.Errorf("withdrawal requires bittensor CLI: btcli wallet transfer --dest %s", destination)
}
```

---

## 5. AI Serving Stack Integration

### 5.1 K3s + Chutes Helm Chart Configuration

```yaml
# helm/helixcluster-chutes/values.yaml
# HelixCluster + Chutes.ai unified deployment configuration

nameOverride: "helixcluster-chutes"
namespaceOverride: "helixcluster"

# ============================================================
# CHUTES MINER CONFIGURATION
# ============================================================

validators:
  defaultRegistry: registry.chutes.ai
  defaultApi: https://api.chutes.ai
  supported:
    - hotkey: "5Dt7HZ7Zpw4DppPxFM7Ke3Cm7sDAWhsZXmM5ZAmE7dSVJbcQ"
      registry: registry.chutes.ai
      api: https://api.chutes.ai
      socket: wss://ws.chutes.ai

# HuggingFace model cache configuration
cache:
  max_age_days: 30
  max_size_gb: 850
  overrides:
    # Per-node overrides
    helixcluster-gpu-0: 2000
    helixcluster-gpu-1: 500

# Miner API service
minerApi:
  replicaCount: 2
  image:
    repository: chutesai/chutes-miner-api
    tag: "latest"
    pullPolicy: Always
  service:
    type: NodePort
    nodePort: 32000
    port: 8080
  resources:
    requests:
      cpu: 500m
      memory: 512Mi
    limits:
      cpu: 2000m
      memory: 2Gi

# GraVal GPU attestation
graval:
  image:
    repository: chutesai/graval-bootstrap
    tag: "latest"
  # VRAM verification threshold (95% must be available)
  vramThreshold: 0.95
  challengeRounds: 256
  # GPU types supported for attestation
  supportedGPUs:
    - h100
    - h200
    - a100
    - a6000
    - l40s
    - l40
    - rtx4090
    - mi300x

# Gepetto chute management
gepetto:
  image:
    repository: chutesai/gepetto
    tag: "latest"
  # Optimization strategy
  strategy:
    # Minimize cost when selecting servers
    costOptimization: true
    # Prefer TEE-enabled nodes
    preferTEE: true
    # Claim bounty threshold
    minBountyValue: 0.001

# Registry proxy for private images
registry:
  image:
    repository: chutesai/registry-proxy
    tag: "latest"
  service:
    type: NodePort
    nodePort: 30500
  # Auth via Bittensor key signatures
  auth:
    enabled: true
    validatorHotkey: "5Dt7HZ7Zpw4DppPxFM7Ke3Cm7sDAWhsZXmM5ZAmE7dSVJbcQ"

# ============================================================
# INFERENCE ENGINE CONFIGURATION
# ============================================================

inference:
  # Primary engine: sglang or vllm
  defaultEngine: sglang
  
  sglang:
    image: chutesai/sglang:latest
    args:
      - --trust-remote-code
      - --enable-torch-compile
      - --tp-size
      - "1"
    
  vllm:
    image: chutesai/vllm:latest
    args:
      - --trust-remote-code
      - --tensor-parallel-size
      - "1"

# ============================================================
# TEE (TRUSTED EXECUTION ENVIRONMENT)
# ============================================================

tee:
  enabled: true
  provider: intel_tdx
  # sek8s configuration
  sek8s:
    image: chutesai/sek8s:latest
    # LUKS-encrypted root filesystem
    encryptedRoot: true
    # Cosign admission controller
    cosignAdmission: true
    # NVIDIA PPCIE for GPU confidentiality
    nvidiaPPCIE: true

# ============================================================
# MONITORING
# ============================================================

monitoring:
  grafana:
    enabled: true
    nodePort: 30080
  prometheus:
    enabled: true
    retention: 30d
  watchtower:
    enabled: true
    # Randomized integrity challenge interval
    challengeInterval: 300  # seconds

# ============================================================
# DATABASE
# ============================================================

postgres:
  persistence:
    enabled: true
    size: 100Gi
    storageClass: local-path
  resources:
    requests:
      memory: 1Gi
      cpu: 500m

redis:
  persistence:
    enabled: false  # Ephemeral for pub/sub only
  resources:
    requests:
      memory: 256Mi
      cpu: 100m
```

### 5.2 Chute Deployment Template

```yaml
# helm/helixcluster-chutes/templates/chute-deployment.yaml
# Generic chute deployment template for HelixCluster workloads

{{- range .Values.chutes }}
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
        helixcluster.io/chute-name: "{{ .name }}"
      annotations:
        # Force redeployment on config change
        checksum/config: "{{ include (print $.Template.BasePath "/chute-config.yaml") . | sha256sum }}"
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
      affinity:
        podAntiAffinity:
          preferredDuringSchedulingIgnoredDuringExecution:
            - weight: 100
              podAffinityTerm:
                labelSelector:
                  matchExpressions:
                    - key: helixcluster.io/chute-name
                      operator: In
                      values:
                        - "{{ .name }}"
                topologyKey: kubernetes.io/hostname
      containers:
        - name: chute
          image: "{{ .image | default (printf "chutesai/%s:latest" (.engine | default "sglang")) }}"
          imagePullPolicy: Always
          ports:
            - containerPort: 8000
              name: http
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
            - name: ENGINE_ARGS
              value: "{{ .engineArgs | default "" }}"
            - name: CONCURRENCY
              value: "{{ .concurrency | default 8 }}"
            - name: GRAVAL_ENABLED
              value: "{{ $.Values.graval.vramThreshold }}"
            - name: TEE_ENABLED
              value: "{{ $.Values.tee.enabled | default false }}"
            - name: HF_HOME
              value: "/data/huggingface"
            - name: TRANSFORMERS_CACHE
              value: "/data/huggingface"
          volumeMounts:
            - name: model-cache
              mountPath: /data/huggingface
            - name: graval-socket
              mountPath: /var/run/graval
            {{- if $.Values.tee.enabled }}
            - name: tdx-device
              mountPath: /dev/tdx-guest
            {{- end }}
          securityContext:
            capabilities:
              add:
                - SYS_ADMIN
            {{- if $.Values.tee.enabled }}
            privileged: true
            {{- end }}
          livenessProbe:
            httpGet:
              path: /health
              port: 8000
            initialDelaySeconds: 60
            periodSeconds: 30
          readinessProbe:
            httpGet:
              path: /ready
              port: 8000
            initialDelaySeconds: 30
            periodSeconds: 10
      volumes:
        - name: model-cache
          hostPath:
            path: /opt/helixcluster/cache/huggingface
            type: DirectoryOrCreate
        - name: graval-socket
          emptyDir: {}
        {{- if $.Values.tee.enabled }}
        - name: tdx-device
          hostPath:
            path: /dev/tdx-guest
        {{- end }}
---
{{- end }}
```

### 5.3 Model Serving Configuration Examples

```yaml
# config/chutes/models.yaml
# Pre-configured model deployments for HelixCluster

chutes:
  # Small models for fast inference
  - name: "llama-3.2-1b"
    model: "unsloth/Llama-3.2-1B-Instruct"
    engine: "vllm"
    image: "chutesai/vllm:0.6.4"
    gpuCount: 1
    concurrency: 32
    memoryLimit: "16Gi"
    nodeSelector:
      helixcluster.io/gpu-vram: "gte-16gb"
    engineArgs: "--trust-remote-code"
    replicas: 2

  # Medium models
  - name: "qwen3-32b"
    model: "Qwen/Qwen3-32B"
    engine: "sglang"
    image: "chutesai/sglang:latest"
    gpuCount: 1
    concurrency: 16
    memoryLimit: "80Gi"
    nodeSelector:
      helixcluster.io/gpu-type: "a100"
      helixcluster.io/gpu-vram: "gte-80gb"
    engineArgs: "--trust-remote-code --enable-torch-compile"
    replicas: 1

  # Large models requiring multiple GPUs
  - name: "deepseek-v3"
    model: "deepseek-ai/DeepSeek-V3"
    engine: "sglang"
    image: "chutesai/sglang:0.4.6.post5b"
    gpuCount: 8
    concurrency: 20
    memoryLimit: "640Gi"
    nodeSelector:
      helixcluster.io/gpu-type: "h100"
      helixcluster.io/gpu-count: "gte-8"
      helixcluster.io/tee: "enabled"
    engineArgs: "--trust-remote-code --tp-size 8 --enable-torch-compile"
    replicas: 1

  # Image generation
  - name: "flux-schnell"
    model: "black-forest-labs/FLUX.1-schnell"
    engine: "diffusers"
    image: "chutesai/diffusers:latest"
    gpuCount: 1
    concurrency: 4
    memoryLimit: "24Gi"
    nodeSelector:
      helixcluster.io/gpu-vram: "gte-24gb"
    replicas: 1

  # Speech-to-text
  - name: "whisper-large-v3"
    model: "openai/whisper-large-v3"
    engine: "vllm"
    image: "chutesai/vllm:latest"
    gpuCount: 1
    concurrency: 8
    memoryLimit: "16Gi"
    replicas: 1
```

---

## 6. Security Integration

### 6.1 E2EE Proxy Integration

The Chutes E2EE proxy provides post-quantum end-to-end encryption using ML-KEM-768 key encapsulation and ChaCha20-Poly1305 authenticated encryption [^3469^].

```go
// pkg/e2ee/proxy.go
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

const (
    ProxyEndpoint      = "https://e2ee-local-proxy.chutes.dev:8443"
    KeyEncapsulation   = "ML-KEM-768"
    SymmetricCipher    = "ChaCha20-Poly1305"
    KDF                = "HKDF-SHA256"
)

// Proxy manages end-to-end encryption for Chutes API calls
type Proxy struct {
    baseURL    string
    apiKey     string
    teeOnly    bool  // Only route to TEE models
}

// EncryptedPayload represents an E2EE-encrypted request
type EncryptedPayload struct {
    Ciphertext     []byte `json:"ciphertext"`
    EncapsulatedKey []byte `json:"encapsulated_key"`
    Nonce          []byte `json:"nonce"`
    InstanceID     string `json:"instance_id"`
}

// EncryptRequest encrypts a request for a specific GPU instance
func (p *Proxy) EncryptRequest(plaintext []byte, instancePublicKey []byte) (*EncryptedPayload, error) {
    // 1. Generate ephemeral ML-KEM-768 keypair
    scheme := kyber768.Scheme()
    
    // 2. Encapsulate shared secret using instance's public key
    encapKey, sharedSecret, err := scheme.Encapsulate(rand.Reader, instancePublicKey)
    if err != nil {
        return nil, fmt.Errorf("encapsulate: %w", err)
    }
    
    // 3. Derive symmetric keys via HKDF-SHA256
    hkdfReader := hkdf.New(sha256.New, sharedSecret, nil, []byte("chutes-e2ee-v1"))
    
    chachaKey := make([]byte, chacha20poly1305.KeySize)
    if _, err := io.ReadFull(hkdfReader, chachaKey); err != nil {
        return nil, fmt.Errorf("derive key: %w", err)
    }
    
    // 4. Generate nonce
    nonce := make([]byte, chacha20poly1305.NonceSize)
    if _, err := rand.Read(nonce); err != nil {
        return nil, fmt.Errorf("generate nonce: %w", err)
    }
    
    // 5. Encrypt with ChaCha20-Poly1305
    aead, err := chacha20poly1305.New(chachaKey)
    if err != nil {
        return nil, fmt.Errorf("create cipher: %w", err)
    }
    
    ciphertext := aead.Seal(nil, nonce, plaintext, nil)
    
    return &EncryptedPayload{
        Ciphertext:      ciphertext,
        EncapsulatedKey: encapKey,
        Nonce:           nonce,
    }, nil
}

// GetEndpoint returns the appropriate E2EE proxy endpoint
func (p *Proxy) GetEndpoint(path string) string {
    return fmt.Sprintf("%s%s", p.baseURL, path)
}

// NewProxy creates an E2EE proxy client
func NewProxy(apiKey string, teeOnly bool) *Proxy {
    return &Proxy{
        baseURL: ProxyEndpoint,
        apiKey:  apiKey,
        teeOnly: teeOnly,
    }
}
```

### 6.2 GraVal GPU Verification Adapter

GraVal provides "Proof of Consecutive VRAM Work" to cryptographically attest GPU authenticity [^3467^]. The verification uses OpenCL and clBLAS for broad GPU compatibility.

```go
// pkg/chutes/graval_verifier.go
package chutes

import (
    "crypto/aes"
    "crypto/sha256"
    "encoding/hex"
    "fmt"
    "time"
)

// GraValVerifier performs GPU attestation verification
type GraValVerifier struct {
    vramThreshold   float64  // Minimum VRAM that must be available (default 0.95)
    challengeRounds int      // Number of matrix multiplication rounds
    timeoutMs       int      // Timeout for attestation response
}

// AttestationResult contains the outcome of GPU verification
type AttestationResult struct {
    GPUUUID         string    `json:"gpu_uuid"`
    GPUName         string    `json:"gpu_name"`
    VRAMTotalGB     int       `json:"vram_total_gb"`
    VRAMVerifiedGB  int       `json:"vram_verified_gb"`
    VerificationTimeMs int64  `json:"verification_time_ms"`
    DerivedKeyHash  string    `json:"derived_key_hash"`
    Passed          bool      `json:"passed"`
    Timestamp       time.Time `json:"timestamp"`
}

// VerifyGPU performs the complete GraVal attestation sequence
func (gv *GraValVerifier) VerifyGPU(gpuUUID string, challenge []byte) (*AttestationResult, error) {
    start := time.Now()
    
    // Phase 1: VRAM capacity test
    // GraVal requires 95% of total VRAM to be available for matrix operations
    vramTotal, vramAvailable, err := gv.measureVRAM(gpuUUID)
    if err != nil {
        return nil, fmt.Errorf("vram measurement: %w", err)
    }
    
    vramRatio := float64(vramAvailable) / float64(vramTotal)
    if vramRatio < gv.vramThreshold {
        return &AttestationResult{
            GPUUUID:    gpuUUID,
            VRAMTotalGB: vramTotal,
            Passed:     false,
            Timestamp:  time.Now(),
        }, fmt.Errorf("VRAM verification failed: %.2f < %.2f threshold", vramRatio, gv.vramThreshold)
    }
    
    // Phase 2: Proof of Consecutive VRAM Work
    // Perform consecutive matrix multiplications seeded by device info
    // The time taken + memory access patterns provide a hardware-level signature
    proof, err := gv.performConsecutiveWork(gpuUUID, challenge)
    if err != nil {
        return nil, fmt.Errorf("consecutive work: %w", err)
    }
    
    // Phase 3: Derive AES-256 key from GPU properties
    // This key is unique to the physical GPU and challenge
    key := gv.deriveGPUKey(gpuUUID, proof, challenge)
    keyHash := sha256.Sum256(key)
    
    elapsed := time.Since(start)
    
    return &AttestationResult{
        GPUUUID:            gpuUUID,
        VRAMTotalGB:        vramTotal,
        VRAMVerifiedGB:     vramAvailable,
        VerificationTimeMs: elapsed.Milliseconds(),
        DerivedKeyHash:     hex.EncodeToString(keyHash[:]),
        Passed:             true,
        Timestamp:          time.Now(),
    }, nil
}

// measureVRAM queries GPU VRAM through NVML/ROCm
func (gv *GraValVerifier) measureVRAM(gpuUUID string) (totalGB, availableGB int, err error) {
    // Implementation uses NVML for NVIDIA, ROCm SMI for AMD
    // Returns total and available VRAM in GB
    return 80, 76, nil  // Example: 80GB total, 76GB available (95%)
}

// performConsecutiveWork executes the GraVal proof-of-work
func (gv *GraValVerifier) performConsecutiveWork(gpuUUID string, challenge []byte) ([]byte, error) {
    // Uses OpenCL + clBLAS for matrix multiplication
    // Seeded by: GPU UUID + challenge + round number
    // Takes diagonal memory slices to reduce data transfer
    // Time taken is a hardware-level signature
    return nil, nil  // Actual implementation calls C/CUDA library
}

// deriveGPUKey creates a unique AES-256 key from GPU attestation
func (gv *GraValVerifier) deriveGPUKey(gpuUUID string, proof, challenge []byte) []byte {
    // Key = SHA-256(gpuUUID || proof || challenge)
    h := sha256.New()
    h.Write([]byte(gpuUUID))
    h.Write(proof)
    h.Write(challenge)
    return h.Sum(nil)
}

// NewGraValVerifier creates a verifier with defaults matching Chutes spec
func NewGraValVerifier() *GraValVerifier {
    return &GraValVerifier{
        vramThreshold:   0.95,
        challengeRounds: 256,
        timeoutMs:       30000,
    }
}
```

### 6.3 Security Integration Summary

| Component | Technology | Purpose | HelixCluster Adaptation |
|---|---|---|---|
| **E2EE Transport** | ML-KEM-768 + ChaCha20-Poly1305 | Encrypt inference requests | Go proxy library integrated into API client |
| **GraVal Attestation** | OpenCL/clBLAS + AES-256 | GPU authenticity verification | CGo wrapper for libgraval, K8s DaemonSet |
| **Code Integrity** | Cosign + Sigstore | Image signing/verification | Admission controller on K3s clusters |
| **TEE** | Intel TDX + NVIDIA PPCIE | Confidential compute | sek8s deployment for sensitive workloads |
| **Containment** | chutes-net-nanny | Network egress control | Cilium network policies on HelixCluster |
| **Continuous Monitoring** | Watchtower | Random integrity challenges | Prometheus alerts on verification failures |
| **File Integrity** | cfsv + inspecto | Filesystem + bytecode hashing | Init container verification hooks |
| **Model Verification** | cllmv | Per-token weight hashing | Sidecar for random slice verification |

---

## 7. Economic Model

### 7.1 Multi-Token Reward Distribution

```go
// pkg/economics/distributor.go
package economics

import (
    "context"
    "fmt"
    "math/big"
    "sync"
    "time"

    "github.com/helixcluster/pkg/marketplace"
)

// TokenType represents supported reward tokens
type TokenType string

const (
    TokenTAO    TokenType = "TAO"    // Bittensor
    TokenIO     TokenType = "IO"     // io.net (Solana)
    TokenAKT    TokenType = "AKT"    // Akash (Cosmos)
    TokenRENDER TokenType = "RENDER" // Render (Solana)
)

// RewardDistributor manages multi-token reward flows
type RewardDistributor struct {
    participants map[string]*Participant
    marketplace  *marketplace.UnifiedManager
    tokenPrices  map[TokenType]float64  // USD price cache
    mu           sync.RWMutex
}

// Participant represents a HelixCluster compute contributor
type Participant struct {
    ID           string             `json:"id"`
    WalletAddr   string             `json:"wallet_addr"`
    GPUType      string             `json:"gpu_type"`
    GPUCount     int                `json:"gpu_count"`
    UptimeHours  float64            `json:"uptime_hours"`
    TokenBalance map[TokenType]float64 `json:"token_balance"`
    SharePercent float64            `json:"share_percent"`  // Based on compute contributed
}

// DistributionRule defines how rewards are split
type DistributionRule struct {
    ParticipantShares map[string]float64  // Participant ID -> percentage
    TreasuryPercent   float64             // Platform treasury cut
    ReinvestPercent   float64             // Auto-reinvest into staking
}

// DistributeRewards allocates earnings across participants
func (rd *RewardDistributor) DistributeRewards(ctx context.Context, earnings *marketplace.UnifiedEarnings, rule DistributionRule) (*DistributionResult, error) {
    rd.mu.Lock()
    defer rd.mu.Unlock()
    
    result := &DistributionResult{
        Timestamp:     time.Now(),
        Distributions: make(map[string]*ParticipantDistribution),
        Treasury:      make(map[TokenType]float64),
        Reinvested:    make(map[TokenType]float64),
    }
    
    for tokenType, amount := range earnings.TokenTotals {
        // Treasury allocation
        treasuryAmt := amount * rule.TreasuryPercent / 100.0
        result.Treasury[tokenType] = treasuryAmt
        remaining := amount - treasuryAmt
        
        // Auto-reinvest allocation
        reinvestAmt := remaining * rule.ReinvestPercent / 100.0
        result.Reinvested[tokenType] = reinvestAmt
        remaining -= reinvestAmt
        
        // Distribute to participants
        for participantID, sharePct := range rule.ParticipantShares {
            allocation := remaining * sharePct / 100.0
            
            if _, ok := result.Distributions[participantID]; !ok {
                result.Distributions[participantID] = &ParticipantDistribution{
                    ParticipantID: participantID,
                    Tokens:        make(map[TokenType]float64),
                }
            }
            result.Distributions[participantID].Tokens[tokenType] += allocation
        }
    }
    
    // Record on-chain via smart contract
    if err := rd.recordOnChain(ctx, result); err != nil {
        return nil, fmt.Errorf("on-chain recording: %w", err)
    }
    
    return result, nil
}

// recordOnChain submits distribution to the HelixCluster reward contract
func (rd *RewardDistributor) recordOnChain(ctx context.Context, result *DistributionResult) error {
    // Calls HelixCluster smart contract on appropriate chain
    // For TAO: Bittensor subtensor
    // For IO: Solana program
    // For AKT: Cosmos message
    return nil  // Implementation depends on chain SDK
}

// GetParticipantROI calculates return on investment for a participant
func (rd *RewardDistributor) GetParticipantROI(participantID string, costs *ParticipantCosts, period time.Duration) (*ROIReport, error) {
    rd.mu.RLock()
    participant, ok := rd.participants[participantID]
    rd.mu.RUnlock()
    
    if !ok {
        return nil, fmt.Errorf("participant not found: %s", participantID)
    }
    
    totalEarningsUSD := 0.0
    for tokenType, balance := range participant.TokenBalance {
        price := rd.tokenPrices[tokenType]
        totalEarningsUSD += balance * price
    }
    
    totalCostsUSD := costs.ElectricityCost + costs.HardwareDepreciation + costs.BandwidthCost + costs.FacilityCost
    
    roi := 0.0
    if totalCostsUSD > 0 {
        roi = (totalEarningsUSD - totalCostsUSD) / totalCostsUSD * 100.0
    }
    
    return &ROIReport{
        ParticipantID:    participantID,
        Period:           period,
        TotalEarningsUSD: totalEarningsUSD,
        TotalCostsUSD:    totalCostsUSD,
        NetProfitUSD:     totalEarningsUSD - totalCostsUSD,
        ROIPercent:       roi,
        BreakEvenDays:    rd.calculateBreakEven(totalCostsUSD, totalEarningsUSD, period),
    }, nil
}

type ParticipantCosts struct {
    ElectricityCost    float64 `json:"electricity_cost"`
    HardwareDepreciation float64 `json:"hardware_depreciation"`
    BandwidthCost      float64 `json:"bandwidth_cost"`
    FacilityCost       float64 `json:"facility_cost"`
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

func (rd *RewardDistributor) calculateBreakEven(costs, earnings float64, period time.Duration) int {
    if earnings <= 0 {
        return -1  // Never breaks even
    }
    dailyEarnings := earnings / period.Hours() * 24
    return int(costs / dailyEarnings)
}

type DistributionResult struct {
    Timestamp     time.Time                           `json:"timestamp"`
    Distributions map[string]*ParticipantDistribution `json:"distributions"`
    Treasury      map[TokenType]float64               `json:"treasury"`
    Reinvested    map[TokenType]float64               `json:"reinvested"`
}

type ParticipantDistribution struct {
    ParticipantID string                `json:"participant_id"`
    Tokens        map[TokenType]float64 `json:"tokens"`
}

// NewRewardDistributor creates a new reward distributor
func NewRewardDistributor(mp *marketplace.UnifiedManager) *RewardDistributor {
    return &RewardDistributor{
        participants: make(map[string]*Participant),
        marketplace:  mp,
        tokenPrices: map[TokenType]float64{
            TokenTAO:    350.0,   // Approximate USD price
            TokenIO:     2.50,
            TokenAKT:    3.00,
            TokenRENDER: 6.00,
        },
    }
}
```

### 7.2 Cost Comparison: Chutes vs Alternatives

| Provider | H100/hr (USD) | A100/hr (USD) | RTX 4090/hr | Billing Model | Decentralized |
|---|---|---|---|---|---|
| **AWS (g5)** | $2.50-4.00 | $1.50-2.50 | N/A | Per-hour | No |
| **Chutes.ai** | ~$0.40 | ~$0.30 | ~$0.08 | Per-token | Yes |
| **io.net** | ~$0.80 | ~$0.50 | ~$0.10 | Per-hour | Yes |
| **Akash** | ~$0.60 | ~$0.40 | ~$0.06 | Per-hour | Yes |
| **Render** | ~$0.69 | ~$0.45 | ~$0.08 | Per-hour | Yes |
| **Salad** | N/A | N/A | ~$0.03 | Per-hour | Yes |

*Sources: Chutes.ai is ~85% cheaper than AWS [^3628^]; io.net pricing from [^3689^]; Akash from [^3528^]; Render from [^3509^]*

### 7.3 Economic Flow Diagram

```
+==========================================================================+
|                        ECONOMIC FLOW                                     |
|                                                                          |
|   USERS (AI devs/researchers)                                            |
|      | Pay per token ($USD / $TAO)                                       |
|      v                                                                   |
|   CHUTES.AI VALIDATOR                                                    |
|      | Routes requests to miners                                         |
|      | Takes validator fee (0% currently)                                |
|      v                                                                   |
|   HELIXCLUSTER GPU NODES (Miners)                                        |
|      | Earn $TAO based on:                                               |
|      |   - Response quality (accuracy)                                   |
|      |   - Latency (TTFT)                                                |
|      |   - Throughput (TPS)                                              |
|      |   - Availability (uptime)                                         |
|      v                                                                   |
|   REWARD DISTRIBUTION                                                    |
|      | 70% -> GPU Node Operators (HelixCluster participants)             |
|      | 15% -> HelixCluster Treasury (infrastructure maintenance)         |
|      | 10% -> Auto-reinvest (staking for higher yields)                  |
|      | 5%  -> Development Fund (open-source contributions)               |
|      v                                                                   |
|   PARTICIPANT WALLETS                                                    |
|      | $TAO can be:                                                      |
|      |   - Staked for higher mining rewards                              |
|      |   - Converted to $IO, $AKT, $RENDER (cross-marketplace)          |
|      |   - Withdrawn to fiat via exchanges                               |
|      |   - Used to pay for HelixCluster services                         |
+==========================================================================+
```

---

## 8. Deployment Guide

### 8.1 Prerequisites

| Component | Minimum | Recommended |
|---|---|---|
| K3s version | v1.28+ | v1.30+ |
| NVIDIA driver | 535+ | 550+ |
| CUDA | 12.2+ | 12.6 |
| Python | 3.10 | 3.12 |
| Bittensor | <8 | Latest stable |
| Helm | 3.12+ | 3.14+ |
| Ansible | 2.15+ | 2.16+ |
| VRAM per GPU | 16GB | 80GB (H100) |
| System RAM | 64GB | 256GB |
| Storage | 500GB SSD | 2TB NVMe |

### 8.2 Step-by-Step Deployment

#### Step 1: Prepare HelixCluster Node

```bash
#!/bin/bash
# scripts/prepare-node.sh
# Run on each GPU node before deployment

set -euo pipefail

echo "=== HelixCluster + Chutes Node Preparation ==="

# Install K3s if not present
if ! command -v k3s &> /dev/null; then
    curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="server --disable traefik" sh -
    export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
fi

# Install NVIDIA GPU Operator
helm repo add nvidia https://helm.ngc.nvidia.com/nvidia
helm repo update
helm install --wait gpu-operator nvidia/gpu-operator \
    --namespace gpu-operator --create-namespace \
    --set driver.enabled=true \
    --set toolkit.enabled=true

# Verify GPU access
kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: nvidia-smi-test
spec:
  restartPolicy: OnFailure
  containers:
    - name: nvidia-smi
      image: nvidia/cuda:12.6-base-ubuntu22.04
      command: ["nvidia-smi"]
      resources:
        limits:
          nvidia.com/gpu: 1
EOF

kubectl wait --for=condition=complete pod/nvidia-smi-test --timeout=60s
kubectl logs nvidia-smi-test
kubectl delete pod nvidia-smi-test

# Install Chutes CLI
pip install chutes

# Create Bittensor wallet if needed
if [ ! -d ~/.bittensor/wallets ]; then
    pip install 'bittensor<8'
    btcli wallet new_coldkey --n_words 24 --wallet.name helixcluster
    btcli wallet new_hotkey --wallet.name helixcluster --n_words 24 --wallet.hotkey miner-1
fi

# Register with Chutes
chutes register

# Create API key
chutes keys create --name helixcluster-admin --admin

echo "=== Node preparation complete ==="
```

#### Step 2: Deploy HelixCluster + Chutes Stack

```bash
#!/bin/bash
# scripts/deploy-stack.sh
# Deploy the complete HelixCluster + Chutes stack

set -euo pipefail

NAMESPACE="${NAMESPACE:-helixcluster}"
CHART_DIR="./helm/helixcluster-chutes"

echo "=== Deploying HelixCluster + Chutes Stack ==="

# Create namespace
kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -

# Create secrets
kubectl create secret generic bittensor-wallet \
    --namespace "$NAMESPACE" \
    --from-file=hotkey=~/.bittensor/wallets/helixcluster/hotkeys/miner-1 \
    --dry-run=client -o yaml | kubectl apply -f -

kubectl create secret generic chutes-api-key \
    --namespace "$NAMESPACE" \
    --from-literal=key="$CHUTES_API_KEY" \
    --dry-run=client -o yaml | kubectl apply -f -

# Deploy Helm chart
helm upgrade --install helixcluster-chutes "$CHART_DIR" \
    --namespace "$NAMESPACE" \
    --values "$CHART_DIR/values.yaml" \
    --set graval.vramThreshold=0.95 \
    --set tee.enabled=true \
    --set monitoring.grafana.enabled=true \
    --wait --timeout 10m

# Verify deployment
echo "=== Deployment Status ==="
kubectl get pods -n "$NAMESPACE" -o wide

echo "=== Services ==="
kubectl get svc -n "$NAMESPACE"

echo "=== GPU Allocation ==="
kubectl describe nodes | grep -A 5 "Allocated resources"

echo "=== Deployment complete ==="
```

#### Step 3: Register GPU Nodes with Chutes

```bash
#!/bin/bash
# scripts/register-nodes.sh
# Register HelixCluster GPU nodes as Chutes miners

set -euo pipefail

VALIDATOR_HOTKEY="${VALIDATOR_HOTKEY:-5Dt7HZ7Zpw4DppPxFM7Ke3Cm7sDAWhsZXmM5ZAmE7dSVJbcQ}"
MINER_API="${MINER_API:-http://$(hostname -I | awk '{print $1}'):32000}"

echo "=== Registering GPU Nodes with Chutes ==="

# Get GPU info from each node
for node in $(kubectl get nodes -l helixcluster.io/gpu=true -o name | cut -d/ -f2); do
    echo "Processing node: $node"
    
    # Extract GPU information
    gpu_info=$(kubectl describe node "$node" | grep "nvidia.com/gpu" | head -1)
    gpu_count=$(echo "$gpu_info" | awk '{print $2}')
    
    # Get node labels for GPU type
    gpu_type=$(kubectl get node "$node" -o jsonpath='{.metadata.labels.helixcluster\.io/gpu-type}')
    gpu_vram=$(kubectl get node "$node" -o jsonpath='{.metadata.labels.helixcluster\.io/gpu-vram}')
    
    # Map to Chutes short reference
    chutes_ref="${gpu_type}"
    
    # Calculate hourly cost (simplified)
    hourly_cost=0.15
    case "$gpu_type" in
        h100) hourly_cost=0.40 ;;
        h200) hourly_cost=0.60 ;;
        a100) hourly_cost=0.30 ;;
        a6000) hourly_cost=0.15 ;;
        l40s) hourly_cost=0.12 ;;
        rtx4090) hourly_cost=0.08 ;;
        mi300x) hourly_cost=0.25 ;;
    esac
    
    # Register via chutes-miner CLI
    pip install chutes-miner-cli
    chutes-miner add-node \
        --name "$node" \
        --validator "$VALIDATOR_HOTKEY" \
        --hourly-cost "$hourly_cost" \
        --gpu-short-ref "$chutes_ref" \
        --hotkey ~/.bittensor/wallets/helixcluster/hotkeys/miner-1 \
        --agent-api "http://$node:32000" \
        --miner-api "$MINER_API"
    
    echo "Registered $node with $gpu_count x $gpu_type GPUs"
done

# Register on Bittensor subnet
echo "=== Registering on Bittensor Subnet 64 ==="
btcli subnet register --netuid 64 \
    --wallet.name helixcluster \
    --wallet.hotkey miner-1

echo "=== Node registration complete ==="
```

#### Step 4: Verify and Monitor

```bash
#!/bin/bash
# scripts/verify.sh
# Verify the complete deployment

echo "=== HelixCluster + Chutes Verification ==="

# Check all pods are running
echo "--- Pod Status ---"
kubectl get pods -n helixcluster -o wide

# Check GraVal attestation
echo "--- GraVal GPU Attestation ---"
kubectl logs -n helixcluster -l app.kubernetes.io/name=graval-bootstrap --tail=20

# Check miner API connectivity
echo "--- Miner API Health ---"
curl -s http://localhost:32000/health | jq . 2>/dev/null || echo "Miner API check via kubectl..."
kubectl exec -n helixcluster deploy/miner-api -- wget -qO- http://localhost:8080/health

# Test inference through Chutes
echo "--- Inference Test ---"
python3 <<'PYEOF'
import os
from openai import OpenAI

client = OpenAI(
    base_url="https://llm.chutes.ai/v1",
    api_key=os.environ.get("CHUTES_API_KEY")
)

try:
    resp = client.chat.completions.create(
        model="deepseek-ai/DeepSeek-V3-0324",
        messages=[{"role": "user", "content": "Hello from HelixCluster!"}],
        max_tokens=50
    )
    print(f"Model: {resp.model}")
    print(f"Response: {resp.choices[0].message.content}")
    print(f"Tokens: prompt={resp.usage.prompt_tokens}, completion={resp.usage.completion_tokens}")
    print("INFERENCE TEST: PASSED")
except Exception as e:
    print(f"INFERENCE TEST: FAILED - {e}")
PYEOF

# Check earnings
echo "--- Earnings Status ---"
python3 <<'PYEOF'
import requests
import os

resp = requests.get(
    "https://api.chutes.ai/users/me",
    headers={"Authorization": f"Bearer {os.environ.get('CHUTES_API_KEY')}"}
)
if resp.status_code == 200:
    data = resp.json()
    print(f"Username: {data.get('username')}")
    print(f"Balance: ${data.get('balance', 0):.2f}")
    print(f"Payment Address: {data.get('payment_address', 'N/A')}")
else:
    print(f"Failed to fetch earnings: {resp.status_code}")
PYEOF

echo "=== Verification Complete ==="
```

### 8.3 Monitoring Dashboard

```yaml
# config/grafana/dashboard.yaml
# Grafana dashboard configuration for HelixCluster + Chutes monitoring

apiVersion: 1
providers:
  - name: "helixcluster-chutes"
    orgId: 1
    folder: "HelixCluster + Chutes"
    type: file
    disableDeletion: false
    editable: true
    options:
      path: /var/lib/grafana/dashboards/helixcluster-chutes

dashboards:
  - title: "GPU Utilization & Earnings"
    panels:
      - title: "GPU Utilization %"
        type: graph
        targets:
          - expr: nvidia_gpu_utilization_gpu{namespace="helixcluster"}
            legendFormat: "{{ pod }} - GPU {{ gpu }}"
      - title: "VRAM Usage (GB)"
        type: graph
        targets:
          - expr: nvidia_gpu_memory_used_bytes{namespace="helixcluster"} / 1e9
      - title: "Inference Requests/sec"
        type: graph
        targets:
          - expr: rate(chutes_requests_total[5m])
      - title: "Earnings (TAO)"
        type: graph
        targets:
          - expr: chutes_earnings_tao_total
      - title: "GraVal Attestation Status"
        type: stat
        targets:
          - expr: graval_attestation_passed
        thresholds:
          - color: red, value: 0
          - color: green, value: 1
      - title: "Model Serving Latency (p99)"
        type: graph
        targets:
          - expr: histogram_quantile(0.99, rate(chutes_inference_duration_seconds_bucket[5m]))
```

---

## 9. HelixCluster Integration Summary

### 9.1 Integration Value Proposition

The integration between HelixCluster and Chutes.ai creates a symbiotic relationship that amplifies both platforms' capabilities:

**For HelixCluster:**
- **Expanded GPU marketplace access**: HelixCluster nodes can earn TAO tokens by contributing compute to the Chutes network, increasing node operator ROI by 30-50% based on current TAO pricing
- **Production-grade inference stack**: Access to vLLM, SGLang, and serverless AI deployment patterns reduces time-to-production for HelixCluster AI workloads
- **Post-quantum security**: ML-KEM-768 + ChaCha20-Poly1305 E2EE provides state-of-the-art encryption for cross-cluster communications
- **Verifiable compute**: GraVal GPU attestation enables trustless verification of compute provider capabilities

**For Chutes.ai:**
- **Heterogeneous device support**: HelixCluster extends Chutes to Apple Silicon, AMD GPUs, and edge devices beyond the traditional NVIDIA-focused ecosystem
- **Unified management plane**: HelixCluster's control plane simplifies miner operations at scale across geographically distributed hardware
- **Multi-marketplace arbitrage**: Intelligent workload routing maximizes earnings by distributing work across Chutes, io.net, Akash, and other platforms
- **Enterprise integration**: HelixCluster's E2EE proxy and TEE support enable enterprise customers to adopt Chutes with confidence

### 9.2 Platform Comparison Matrix

| Feature | Chutes.ai | io.net | Akash | HelixCluster + Chutes |
|---|---|---|---|---|
| **Subnet/Chain** | Bittensor SN64 | Solana | Cosmos | Multi-chain |
| **Token** | TAO | IO | AKT | TAO/IO/AKT/RENDER |
| **Inference Engine** | vLLM, SGLang | Ray Serves | Custom | vLLM, SGLang + Custom |
| **GPU Verification** | GraVal | PoW/PoTL | Provider-reported | GraVal + Custom |
| **E2EE** | ML-KEM-768 + ChaCha20 | Basic TLS | Basic TLS | ML-KEM-768 + ChaCha20 |
| **TEE Support** | Intel TDX (sek8s) | Limited | No | Intel TDX + AMD SEV |
| **Billing** | Per-token | Per-hour | Per-hour | Per-token + Per-hour |
| **Cost vs AWS** | ~85% cheaper | ~70% cheaper | ~75% cheaper | ~85% cheaper |
| **Serverless** | Yes | Partial | No | Yes |
| **Multi-GPU** | Yes | Yes | Yes | Yes |
| **Heterogeneous GPU** | NVIDIA, AMD | NVIDIA | NVIDIA | NVIDIA, AMD, Apple |
| **API Format** | OpenAI-compatible | Custom | Custom | OpenAI-compatible |

### 9.3 Recommended Implementation Phases

| Phase | Deliverable | Timeline | Dependencies |
|---|---|---|---|
| **1. Foundation** | K3s + chutes-miner deployment via Helm | 2 weeks | GPU nodes, K3s cluster |
| **2. GPU Discovery** | Auto-discovery + GraVal attestation | 1 week | Phase 1, NVIDIA drivers |
| **3. Inference** | vLLM/SGLang model serving stack | 2 weeks | Phase 2, model weights |
| **4. E2EE Proxy** | Post-quantum encryption for API calls | 1 week | Phase 3 |
| **5. Marketplace** | Multi-platform marketplace manager | 2 weeks | Phase 1-4 |
| **6. Economics** | Multi-token reward distribution | 1 week | Phase 5, smart contracts |
| **7. TEE** | Intel TDX confidential compute | 2 weeks | Phase 4, TDX hardware |
| **8. Monitoring** | Grafana dashboards + alerts | 1 week | All phases |

### 9.4 Key Files and Repositories

| Repository | Purpose | URL |
|---|---|---|
| `chutesai/chutes` | Python SDK/CLI | https://github.com/chutesai/chutes |
| `chutesai/chutes-miner` | Miner code (K8s/Helm) | https://github.com/chutesai/chutes-miner |
| `chutesai/chutes-api` | Validator/API code | https://github.com/chutesai/chutes-api |
| `chutesai/e2ee-proxy` | E2EE OpenResty proxy | https://github.com/chutesai/e2ee-proxy |
| `chutesai/graval` | GPU attestation library | https://github.com/chutesai/graval |
| `chutesai/sek8s` | TEE Kubernetes | https://github.com/chutesai/sek8s |
| `chutesai/chutes-jumpmaster` | Control plane workspace | https://github.com/chutesai/chutes-jumpmaster |
| `chutesai/vllm` | Forked vLLM engine | https://github.com/chutesai/vllm |
| `chutesai/sglang` | Forked SGLang engine | https://github.com/chutesai/sglang |

### 9.5 Risk Assessment

| Risk | Impact | Likelihood | Mitigation |
|---|---|---|---|
| Bittensor validator centralization | Medium | Medium | Support multiple validators; child hotkeys |
| TAO price volatility | High | High | Multi-token diversification; hedging |
| GraVal compatibility issues | Medium | Low | Fallback attestation; vendor drivers |
| TEE performance overhead | Medium | Medium | Benchmark TEE vs non-TEE; selective TEE |
| Network congestion (Subnet 64) | Low | Medium | Auto-failover; multiple validator connections |
| Smart contract vulnerabilities | High | Low | Audits; formal verification; gradual rollout |
| Regulatory changes (crypto) | High | Medium | Geographic distribution; fiat off-ramps |

---

## Appendix A: API Reference Quick Reference

### Chutes.ai Endpoints

| Endpoint | Method | Auth | Description |
|---|---|---|---|
| `https://llm.chutes.ai/v1/chat/completions` | POST | Bearer `cpk_` | Chat completions (OpenAI-compatible) |
| `https://llm.chutes.ai/v1/models` | GET | Bearer `cpk_` | List available models |
| `https://api.chutes.ai/users/me` | GET | Bearer `cpk_` | User info & balance |
| `https://api.chutes.ai/api_keys/` | POST | Bearer `cpk_` | Create API key |
| `https://api.chutes.ai/payments` | GET | Bearer `cpk_` | Payment history |
| `https://api.chutes.ai/payments/summary/tao` | GET | Bearer `cpk_` | TAO payment totals |

### Bittensor CLI Commands

```bash
# Register on subnet 64
btcli subnet register --netuid 64 --wallet.name COLDKEY --wallet.hotkey HOTKEY

# Check emissions
btcli subnet emissions --netuid 64

# Transfer TAO
btcli wallet transfer --dest SS58_ADDRESS --amount AMOUNT

# Stake
btcli stake add --wallet.name COLDKEY --wallet.hotkey HOTKEY --amount AMOUNT
```

## Appendix B: Glossary

| Term | Definition |
|---|---|
| **Chute** | An application running on the Chutes platform (analogous to a FastAPI app) [^3530^] |
| **Cord** | A single function/endpoint within a chute (analogous to a route) [^3530^] |
| **GraVal** | GPU attestation library using "Proof of Consecutive VRAM Work" [^3467^] |
| **Gepetto** | Chute management component responsible for provisioning and scaling |
| **Forge** | Validator-side image build and signing service |
| **Watchtower** | Continuous monitoring service for miner integrity |
| **Sek8s** | Security-hardened Kubernetes for TEE workloads |
| **TEE** | Trusted Execution Environment (Intel TDX) |
| **TD Quote** | Trust Domain Quote - CPU-signed attestation report |
| **RTMR** | Runtime Measurement Register (TDX measurement) |
| **PPCIE** | Protected PCIe - NVIDIA encrypted GPU channel |

---

*This document was generated as part of the HelixCluster Phase 8 research initiative. For questions or updates, refer to the HelixCluster architecture documentation and the Chutes.ai documentation at https://chutes.ai/docs.*

**Citations:**  
[^3481^] Chutes (Subnet 64) - subnetalpha.ai  
[^3628^] Chutes: Reconstructing Decentralized Serverless Infrastructure - chaincatcher.com  
[^3629^] Chutes API Documentation - chutes.ai/llms.txt  
[^3467^] Chutes Security/Integrity Documentation - chutes.ai/docs  
[^3469^] Chutes E2EE Proxy GitHub - github.com/chutesai/e2ee-proxy  
[^3530^] Chutes SDK GitHub - github.com/chutesai/chutes  
[^3471^] Confidential Compute for AI Inference - chutes.ai/news  
[^3689^] Understanding io.net - messari.io/report  
[^3528^] Akash Network + Akave Integration - akave.com/blog  
[^3509^] IO vs Render Comparison - io.net/blog  
[^3629^] Chutes API Reference - chutes.ai/docs  
[^3458^] Mining on Chutes Documentation - chutes.ai/docs/miner-resources  
[^3630^] Chutes SDK Installation - chutes.ai/docs/getting-started  
[^3631^] Chutes Platform Overview - chutes.ai/news  
[^3635^] Chutes Jumpmaster - github.com/chutesai/chutes-jumpmaster  
[^3685^] Decentralized Cloud Computing Guide - io.net/blog  
[^3687^] 2025 io.net Year in Review - io.net/blog  
[^3684^] Akash GPU Leasing - gate.com/learn  
[^3688^] Akash Blockchain Architecture - daic.capital/blog  
