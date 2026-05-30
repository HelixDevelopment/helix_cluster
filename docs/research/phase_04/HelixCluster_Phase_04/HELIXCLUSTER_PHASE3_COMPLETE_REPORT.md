# HelixCluster Phase 3 — Edge & Mobile Device Integration
## Executive Summary

### The Vision: Billions of Devices, One Compute Pool

HelixCluster Phase 3 is our most ambitious expansion yet — integrating **Single Board Computers, Android phones/tablets/TV boxes, iOS devices, and HarmonyOS devices** into a single unified compute cluster. Where Phase 1 added PCs and Phase 2 added PlayStations, Phase 3 opens the door to the **billions of edge and mobile devices** that surround us every day.

### Why Edge & Mobile?

Consider this: there are over **3 billion Android devices**, **1 billion iPhones**, and **hundreds of millions of SBCs and TV boxes** in active use worldwide. Most of these devices spend the majority of their time idle — charging overnight, sitting on desks, playing nothing on the living room TV. The collective compute power of these idle devices dwarfs even the largest supercomputers.

Vodafone's DreamLab proved this concept: **100,000 smartphones running overnight calculations matched the speed of 30 supercomputers** for cancer research. Our mission is to harness this power systematically.

### What Phase 3 Adds

| Category | Devices | Count Potential | Unique Value |
|----------|---------|----------------|--------------|
| **SBCs** | Orange Pi 5 Max, Raspberry Pi 5 | 10-100 nodes | 16GB RAM, 6 TOPS NPU, 2.5GbE, $125 |
| **Android TV Boxes** | RK3588 boxes, Xiaomi MiBox | 10-50 nodes | $50-130, ARM64 Linux, 24/7 capable |
| **Android Phones** | Samsung, Pixel, Xiaomi | 100+ devices | Charging-gated, billions available |
| **Android Tablets** | Samsung Tab, Xiaomi Pad | 10-50 devices | Large screens, good thermals |
| **iOS Devices** | iPhone 16 Pro, iPad Pro M4 | 10-50 devices | 35-38 TOPS NPU, Metal GPU |
| **HarmonyOS** | Huawei MatePad Pro | 5-10 devices | Da Vinci NPU, Super Device |

### Key Innovation: The "Overnight Supercomputer"

Phase 3's core innovation is the **charging-gated compute model**: mobile devices only receive work when they are (1) plugged in, (2) on WiFi, and (3) during configured hours (typically overnight). This model — proven by DreamLab, Folding@home, and BOINC — makes phone-based distributed computing practical without impacting user experience.

### Architecture Approach

- **SBCs & TV Boxes (Armbian)**: Run standard Linux Node Agent, first-class citizens
- **Android Phones**: APK with Termux foreground service + Vulkan compute
- **iOS Devices**: Native app with Metal/CoreML, pull-based donor model
- **HarmonyOS**: ArkTS app with Da Vinci NPU integration
- **All devices**: Semi-trusted security model with output verification

### Device Tier Classification

| Tier | Devices | Trust Level | Role |
|------|---------|-------------|------|
| T3 | Orange Pi 5 Max | STANDARD | Full worker with NPU |
| T4 | Raspberry Pi 5, RK3588 TV boxes | STANDARD | Standard worker |
| T5 | Android TV Box (Armbian) | STANDARD | Headless worker |
| T6 | Android Phone/Tablet | SEMI | Charging-gated compute |
| T7 | iPhone/iPad | EDGE_DONOR | Opportunistic inference |
| T8 | HarmonyOS device | SEMI | NPU inference |

### Investment

- **26 new implementation tasks**, ~200 hours (~5 weeks)
- Reference hardware investment: ~$500 (5-10 test devices)
- Potential compute return: **100+ NPU TOPS, 500+ CPU cores, 256GB+ RAM**
# Chapter 2: Device Category Deep Dives

## 2.1 SBCs: Orange Pi 5 Max — The Reference Platform

### Why Orange Pi 5 Max?

The Orange Pi 5 Max represents the sweet spot of price, performance, and cluster suitability. At $125 for the 16GB model, it delivers specifications that would cost $300+ to replicate with a Raspberry Pi 5 setup.

### Specifications

| Component | Specification | Cluster Relevance |
|-----------|-------------|-------------------|
| **SoC** | Rockchip RK3588 | 8-core ARM64, big.LITTLE |
| **CPU Big** | 4x Cortex-A76 @ 2.4 GHz | Performance cores for compute |
| **CPU Little** | 4x Cortex-A55 @ 1.8 GHz | Efficiency cores for background |
| **GPU** | Mali-G610 MP4 | 255 GFLOPS, Vulkan 1.2, OpenCL 2.2 |
| **NPU** | 6 TOPS INT8 | AI inference, LLM acceleration |
| **RAM** | 16 GB LPDDR5 | Large enough for ML models |
| **Storage** | PCIe 3.0 x4 NVMe | 2,100-5,700 MB/s — excellent |
| **Network** | 2.5 Gigabit + WiFi 6E | Faster than many PCs |
| **Power** | 15-25W full load | Very efficient |
| **Price** | $125 (16GB) | Best price/performance |

### Performance Benchmarks

| Benchmark | Orange Pi 5 Max | Raspberry Pi 5 | Ratio |
|-----------|----------------|----------------|-------|
| Geekbench 5 SC | ~850 | ~740 | 1.15x |
| Geekbench 5 MC | ~4,200 | ~2,300 | 1.83x |
| NPU Inference (TinyLlama 1B) | 20 tok/s | N/A | — |
| Storage Read (NVMe) | 5,700 MB/s | 450 MB/s | 12.7x |
| Network | 2.5 Gbps | 1 Gbps | 2.5x |
| RAM | 16 GB | 4-8 GB | 2-4x |
| Cost (16GB) | $125 | $305 | 0.41x |

### Linux Support

Armbian Ubuntu 24.04 runs natively with full hardware support:
- **Kernel**: 6.1.x (vendor) / 6.6+ (mainline WIP)
- **GPU**: Panfrost driver for Mali-G610 (Mesa 24+)
- **NPU**: RKNN Toolkit2 with C API
- **Docker**: Fully supported, linux/arm64 native
- **Go/Zig/C++**: All compile natively

```bash
# Cross-compile HelixCluster agent for Orange Pi 5 Max
GOOS=linux GOARCH=arm64 CGO_ENABLED=1 \
  CC=aarch64-linux-gnu-gcc \
  go build -o helix-agent-arm64 ./cmd/agent
```

### RK3588 NPU: AI Inference at the Edge

The 6 TOPS NPU is the Orange Pi 5 Max's secret weapon. Using RKNN Toolkit2:

```python
# Python example using RKNN
from rknnlite.api import RKNNLite

rknn = RKNNLite()
rknn.load_rknn('tinyllama_1b.rknn')
rknn.init_runtime(core_mask=RKNNLite.NPU_CORE_0)

# Run inference
outputs = rknn.inference(inputs=[input_data])
# Result: ~20 tok/s for TinyLlama 1.1B
```

Supported models: YOLOv5/v8, LLAMA, Qwen, DeepSeek, TinyLlama, ResNet, MobileNet.

## 2.2 Android Phones & Tablets

### The Opportunity: 3+ Billion Devices

Every Android device is a potential compute node. The key challenge is Android's restrictive background execution policies — solved by using **foreground services with persistent notifications**.

### Deployment: Termux + Foreground Service

Termux provides a full Linux environment on Android without requiring root:

```bash
# Install Termux from F-Droid
# Install SSH server
pkg install openssh
sshd  # Starts on port 8022

# Install full development stack
pkg install golang zig clang python nodejs

# Run HelixCluster agent
./helix-agent --config android.toml
```

The Android Agent APK wraps this in a foreground service that:
1. Starts Termux environment
2. Runs the native agent (compiled for Android via NDK)
3. Manages a persistent notification showing compute status
4. Monitors battery/charging state
5. Only accepts work when charging + WiFi connected

### GPU Compute on Android

All modern Android GPUs support Vulkan compute shaders:

| GPU | Devices | Vulkan | Compute GFLOPS |
|-----|---------|--------|----------------|
| Adreno 735 | Snapdragon 8 Gen 3 | 1.3 | ~2,500 |
| Adreno 650 | Snapdragon 865 | 1.1 | ~1,250 |
| Mali-G710 | Dimensity 9000 | 1.3 | ~1,000 |
| Mali-G610 | RK3588 | 1.2 | ~255 |

### NPU Access

| NPU | SDK | Performance | Devices |
|-----|-----|-------------|---------|
| Qualcomm Hexagon | SNPE/QNN | Up to 45 TOPS | Snapdragon 8 Gen 3 |
| MediaTek APU | NeuroPilot | Up to 30 TOPS | Dimensity 9300 |
| Google TPU | NNAPI | Variable | Pixel 8+ |

### Power Gating

```kotlin
// Android: Only compute when charging
val batteryStatus = registerReceiver(null, 
    IntentFilter(Intent.ACTION_BATTERY_CHANGED))
val status = batteryStatus?.getIntExtra(BatteryManager.EXTRA_STATUS, -1)
val isCharging = status == BatteryManager.BATTERY_STATUS_CHARGING
        || status == BatteryManager.BATTERY_STATUS_FULL

if (isCharging && wifiConnected && batteryPercent > 20) {
    acceptWorkUnits()
} else {
    enterIdleMode()
}
```

## 2.3 Android TV Boxes

### The Hidden Gem: $50 Linux Servers

Android TV boxes are dramatically undervalued as compute hardware. Many use the same RK3588 SoC as the Orange Pi 5 Max but cost less and include cases, power supplies, and cooling.

### Best TV Boxes for Compute

| Device | SoC | RAM | Price | Why |
|--------|-----|-----|-------|-----|
| **H96 MAX V58** | RK3588 | 8GB | ~$130 | Best overall, 2.5GbE |
| **X96 X10** | S928X | 8GB | ~$95 | Penta-core, NPU |
| **onn 4K Pro** | S905X4 | 3GB | ~$50 | Incredible value |
| **UGOOS X4 Pro** | S905X4 | 4GB | ~$100 | Active cooling |

### Armbian Linux: The Key Transformation

Instead of fighting Android's restrictions, replace Android with Linux:

```bash
# Flash Armbian to SD card
# Insert into TV box
# Boot from SD (hold recovery button while powering on)
# Armbian runs natively — full Linux server!

# Install Docker, run HelixCluster agent
curl -fsSL https://get.docker.com | sh
docker run -d helixcluster/agent:arm64
```

## 2.4 iOS Devices

### Power vs. Restriction

iOS devices have the most powerful mobile chips but the most restrictive OS:

| Device | CPU | GPU | NPU | RAM | Background |
|--------|-----|-----|-----|-----|------------|
| iPhone 16 Pro | A18 Pro | 2.29 TF | 35 TOPS | 8GB | ~3 min max |
| iPad Pro M4 | M4 | 3+ TF | 38 TOPS | 16GB | ~30 min (processing) |

### The Donor Model

iOS devices cannot be persistent cluster nodes. Instead, they operate as **compute donors**:

```swift
// iOS: Pull work during background refresh
BGTaskScheduler.shared.register(
    forTaskWithIdentifier: "com.helix.compute"
) { task in
    // Fetch work unit from queue
    let work = fetchWorkUnit()
    
    // Execute using Metal (GPU) or CoreML (NPU)
    let result = executeOnDevice(work)
    
    // Upload results
    uploadResults(result)
    
    task.setTaskCompleted(success: true)
}
```

### Metal Compute

```metal
// Metal compute shader — runs on all Apple GPUs
kernel void compute(
    device float *input [[ buffer(0) ]],
    device float *output [[ buffer(1) ]],
    uint id [[ thread_position_in_grid ]]
) {
    output[id] = input[id] * 2.0;
}
```

### CoreML + Neural Engine

```swift
// Use CoreML with Neural Engine for inference
let config = MLModelConfiguration()
config.computeUnits = .all // CPU + GPU + Neural Engine
let model = try MLModel(contentsOf: modelURL, configuration: config)
```

## 2.5 HarmonyOS Devices

### Unique Capabilities

HarmonyOS has a **Super Device** feature that allows seamless task distribution across HarmonyOS devices. This aligns conceptually with HelixCluster's distributed computing model.

| Component | Specification |
|-----------|-------------|
| **CPU** | Kirin 9000S (7nm, ~SD888 level) |
| **GPU** | Maleoon 910 (OpenCL 2.0, Vulkan 1.0) |
| **NPU** | Da Vinci (dual-core, INT8/INT16) |
| **Distributed** | Super Device, Device Virtualization |

### Development

HarmonyOS apps are written in ArkTS (TypeScript superset). The HiAI Engine provides access to the Da Vinci NPU for AI inference. The unique "Super Device" capability allows one HarmonyOS device to use another's compute resources — a built-in distributed computing primitive.
# Chapter 3: Architecture & Implementation

## 3.1 Multi-Platform Agent Architecture

HelixCluster Phase 3 uses **three agent architectures** tailored to device capabilities:

```
┌──────────────────────────────────────────────────────────────────┐
│              PHASE 3 MULTI-PLATFORM AGENT LANDSCAPE              │
│                                                                  │
│  ┌──────────────────┐  ┌──────────────────┐  ┌─────────────────┐ │
│  │  LINUX AGENT     │  │  ANDROID AGENT   │  │  iOS AGENT      │ │
│  │  (Go binary)     │  │  (APK: Kotlin +  │  │  (Swift app)    │ │
│  │                  │  │   NDK/Termux)    │  │                 │ │
│  ├──────────────────┤  ├──────────────────┤  ├─────────────────┤ │
│  │ SBCs             │  │ Phones/Tablets   │  │ iPhone/iPad     │ │
│  │ TV Boxes (Linux) │  │ TV Boxes (Andr.) │  │                 │ │
│  ├──────────────────┤  ├──────────────────┤  ├─────────────────┤ │
│  │ Trust: STANDARD  │  │ Trust: SEMI      │  │ Trust: DONOR    │ │
│  │ Full worker      │  │ Charging-gated   │  │ Pull-based      │ │
│  │ Persistent       │  │ Foreground svc   │  │ Background fetch│ │
│  │ WireGuard+MQTT   │  │ QUIC+MQTT        │  │ HTTP/2+MQTT     │ │
│  │ Docker OK        │  │ No Docker        │  │ No Docker       │ │
│  │ All workloads    │  │ Small batch+AI   │  │ AI inference    │ │
│  └──────────────────┘  └──────────────────┘  └─────────────────┘ │
│                                                                  │
│  ┌──────────────────┐                                            │
│  │  HARMONYOS AGENT │  (Additional)                              │
│  │  (ArkTS app)     │                                            │
│  │ Trust: SEMI      │                                            │
│  │ Super Device     │                                            │
│  │ WebSocket+MQTT   │                                            │
│  │ NPU inference    │                                            │
│  └──────────────────┘                                            │
└──────────────────────────────────────────────────────────────────┘
```

## 3.2 Protocol Stack

Phase 3 extends the network layer with protocols optimized for mobile/edge:

| Protocol | Purpose | Devices | Why |
|----------|---------|---------|-----|
| **MQTT** | Work dispatch, status | All edge | 2-byte header, pub/sub, QoS |
| **QUIC** | Mobile transport | Android phones | 0-RTT, connection migration |
| **WebSocket** | HarmonyOS/iOS | iOS, HarmonyOS | HTTP-friendly, easy proxy |
| **WireGuard** | Encrypted mesh | SBCs, TV boxes | Kernel module, fast |
| **HTTP/2** | Background fetch | iOS | iOS native support |

```go
// Protocol factory selects best protocol per device tier
func NewProtocolClient(tier DeviceTier) ProtocolClient {
    switch tier {
    case TIER_3, TIER_4, TIER_5:
        return mqtt.NewClient() // SBCs, TV boxes — reliable
    case TIER_6:
        return quic.NewClient()  // Android phones — mobile-optimized
    case TIER_7:
        return websocket.NewClient() // iOS — HTTP-friendly
    case TIER_8:
        return mqtt.NewClient()  // HarmonyOS — MQTT standard
    }
}
```

## 3.3 NPU Backend: Unified AI Inference

Phase 3 devices bring diverse NPUs. The AI Inference Engine adapts:

```
┌─────────────────────────────────────────────────────────────┐
│              UNIFIED NPU BACKEND                              │
│                                                              │
│  Input: ONNX model (universal format)                       │
│      │                                                       │
│      ▼                                                       │
│  ┌───────────────────────────────────────────────────────┐  │
│  │  Model Converter (per-device)                         │  │
│  │  ONNX → RKNN (RK3588)                                 │  │
│  │  ONNX → CoreML (iOS)                                  │  │
│  │  ONNX → TFLite + NNAPI (Android Snapdragon)           │  │
│  │  ONNX → MindSpore Lite (HarmonyOS)                    │  │
│  │  ONNX → Vulkan Compute (fallback for all GPUs)        │  │
│  └───────────────────────────────────────────────────────┘  │
│      │                                                       │
│      ▼                                                       │
│  ┌───────────────────────────────────────────────────────┐  │
│  │  Runtime (per-device)                                 │  │
│  │  RKNN C API → RK3588 NPU                              │  │
│  │  CoreML → Apple Neural Engine                         │  │
│  │  NNAPI → Qualcomm Hexagon                             │  │
│  │  HiAI → Da Vinci NPU                                  │  │
│  │  Vulkan → GPU (all platforms)                         │  │
│  │  CPU fallback (all platforms)                         │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

### Quantization per Device Tier

| Tier | Model Size | Quantization | Example |
|------|-----------|-------------|---------|
| T3 (SBC Premium) | Up to 4GB | Q4_0 (RKNN) | Qwen2.5 7B |
| T4 (SBC Standard) | Up to 2GB | Q4_0 (CPU) | TinyLlama 1.1B |
| T6 (Android Phone) | Up to 1GB | Q4_0 + NNAPI | Gemma 2B |
| T7 (iOS) | Up to 2GB | Q4_0 + CoreML | Llama 3.2 3B |
| T8 (HarmonyOS) | Up to 1GB | Q4_0 + HiAI | Qwen 1.8B |

## 3.4 Power-Aware Scheduling

The EdgeAware scheduler plugin implements device-specific scheduling rules:

```go
// Edge scheduling rules
rules := map[DeviceTier][]ScheduleRule{
    TIER_6: { // Android phones
        {Condition: "is_charging", Required: true},
        {Condition: "wifi_connected", Required: true},
        {Condition: "battery_above", Value: 20, Required: true},
        {Condition: "cpu_temp_below", Value: 70, Required: true},
        {Condition: "time_between", Start: 22, End: 6, Required: false},
    },
    TIER_7: { // iOS
        {Condition: "background_refresh_enabled", Required: true},
        {Condition: "low_power_mode_off", Required: true},
    },
    TIER_3: { // SBC
        {Condition: "cpu_temp_below", Value: 85, Required: true},
    },
}
```

## 3.5 Security Model

Phase 3 extends the trust model with two new levels:

| Level | Devices | Access | Workloads |
|-------|---------|--------|-----------|
| **STANDARD** | SBCs, Armbian TV boxes | Full worker access | All except sensitive |
| **SEMI** | Android, HarmonyOS, Consoles | Encrypted work units | Small batch, AI inference |
| **EDGE_DONOR** | iOS | Pull from queue | AI inference only |

All edge device outputs are verified through LLMsVerifier or redundant computation on trusted nodes.

## 3.6 Implementation Timeline

| Week | Tasks | Deliverable |
|------|-------|-------------|
| 1 | E-0.1 (ARM64 toolchain), E-0.2 (SBC Agent) | Agent runs on Orange Pi 5 Max |
| 2 | E-0.3 (RKNN NPU), E-0.4 (Mali Vulkan) | NPU + GPU compute validated |
| 3 | E-0.5-0.7 (Android APK, foreground service, battery) | Android agent prototype |
| 4 | E-0.8-0.9 (QUIC, MQTT), E-1.1-1.3 (registration, heartbeat, protocol) | Edge devices register with cluster |
| 5 | E-2.1-2.4 (scheduler, power gating, quantization, NPU backend) | Full edge scheduling active |

## 3.7 Gaps Filled by Phase 3

| Gap | How Phase 3 Fills It | Impact |
|-----|---------------------|--------|
| **NPU Compute** | 6-38 TOPS per device from dedicated AI accelerators | **100+ TOPS aggregate** |
| **Ultra-Low-Cost Scaling** | $50-130 per device (TV boxes, SBCs) | **10x nodes per dollar** |
| **Idle Device Utilization** | Phones idle 22h/day, charging-gated compute | **Billions of device-hours** |
| **Power-Efficient AI** | NPUs at 1-5W vs GPUs at 100W+ | **20-50x perf/watt** |
| **Geographic Distribution** | Phones everywhere = compute everywhere | **Edge latency reduction** |
| **ARM64 Optimization** | ARM64 code benefits Apple Silicon too | **Better Phase 1 performance** |
| **Elastic Community Scaling** | Users opt in/out dynamically | **Unlimited scaling potential** |
