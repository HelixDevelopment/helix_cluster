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
