# HELIXCLUSTER PHASE 3 — EDGE & MOBILE DEVICE INTEGRATION
## SBCs, Android, iOS, HarmonyOS — Unified Compute Architecture
## Version 1.0 | 2026-05-30

---

## 1. EXECUTIVE SUMMARY

HelixCluster Phase 3 extends the distributed computing cluster to include **Single Board Computers (SBCs), Android phones/tablets/TV boxes, iOS devices, and HarmonyOS devices** as fully integrated compute nodes. This represents the largest device expansion yet — from millions of potential PC/console nodes to **billions** of potential edge and mobile devices.

### The Vision: Every Device is Compute

```
┌───────────────────────────────────────────────────────────────────────┐
│                    HELIXCLUSTER — ALL PHASES COMBINED                  │
│                                                                        │
│  Phase 1: PCs & Laptops          Phase 2: PlayStations               │
│  ├─ Intel i7/i9                   ├─ PS4 / PS4 Pro                    │
│  ├─ AMD Ryzen 9                   ├─ PS5 / PS5 Pro                    │
│  └─ Apple Silicon M3/M4           └─ Vulkan compute, SEMI trust       │
│                                                                        │
│  Phase 3: Edge & Mobile Devices  ← YOU ARE HERE                       │
│  ├─ SBCs: Orange Pi 5 Max, RPi5   ├─ Android: Phones, Tablets        │
│  ├─ Android TV: RK3588 boxes      ├─ iOS: iPhone, iPad               │
│  └─ HarmonyOS: Huawei MatePad     └─ Billions of potential nodes      │
│                                                                        │
│  UNIFIED COMPUTE POOL: CPU + GPU + NPU from ALL devices               │
└───────────────────────────────────────────────────────────────────────┘
```

### Device Tier Classification (Phase 3)

| Tier | Device Category | Trust Level | Examples | Compute Focus |
|------|----------------|-------------|----------|---------------|
| **T3** | SBC — Premium | STANDARD | Orange Pi 5 Max | Full worker: CPU+GPU+NPU |
| **T4** | SBC — Standard | STANDARD | Raspberry Pi 5, RK3588 TV boxes | Worker: CPU+GPU |
| **T5** | Android TV Box (Linux) | STANDARD | H96 MAX V58, UGOOS X4 | Headless worker |
| **T6** | Android Phone/Tablet | SEMI | Samsung S24, Pixel 9, Xiaomi Pad | Charging-gated compute |
| **T7** | iOS Device | EDGE_DONOR | iPhone 16 Pro, iPad Pro M4 | Opportunistic inference |
| **T8** | HarmonyOS Device | SEMI | Huawei MatePad Pro | NPU inference + distributed |

### What Phase 3 Adds to the Cluster

| Resource Pool | Phase 1 (PC) | Phase 2 (Console) | **Phase 3 (Edge)** | **Total** |
|--------------|-------------|-------------------|-------------------|-----------|
| CPU Cores | ~100 | ~50 | **~500+** | ~650+ |
| GPU TFLOPS | ~50 | ~30 | **~20+** | ~100+ |
| NPU TOPS | ~5 | ~2 | **~100+** | ~107+ |
| RAM (GB) | ~512 | ~128 | **~256+** | ~896+ |
| Potential Nodes | ~20 | ~10 | **~1000+** | ~1030+ |
| Monthly Cost | ~$500 | ~$100 | **~$50** | ~$650 |

*Phase 3 adds NPU compute (not available in Phases 1-2), dramatically increases CPU core count, and does so at very low cost.*

---

## 2. SBC COMPUTE NODES (Tier 3-4)

### 2.1 Reference Platform: Orange Pi 5 Max

```yaml
sbc:
  model: "Orange Pi 5 Max"
  tier: 3
  soc: "Rockchip RK3588"
  
cpu:
  architecture: "ARM64 (aarch64)"
  cores: "4x Cortex-A76 @ 2.4GHz + 4x Cortex-A55 @ 1.8GHz"
  big_little: "Yes — A76 for performance, A55 for efficiency"
  simd: "NEON, dotprod, i8mm, fp16, ASIMD"
  crypto: "ARMv8 Cryptography Extensions (AES, SHA-1, SHA-256)"
  benchmarks:
    geekbench_5_sc: "~850"
    geekbench_5_mc: "~4,200"
    
gpu:
  model: "Mali-G610 MP4"
  architecture: "Valhall 4th Gen"
  api_support: "OpenCL 2.2, Vulkan 1.2, OpenGL ES 3.2"
  compute: "255 GFLOPS FP32"
  video: "8K@60fps decode, 8K@30fps encode"
  
npu:
  model: "RKNN-6TOPS"
  performance: "6 TOPS INT8"
  sdk: "RKNN Toolkit2, RKNN-LLM"
  supported_models: "YOLO, LLAMA, Qwen, DeepSeek, TinyLlama"
  tinyllama_1b: "~20 tok/s"
  
memory:
  type: "LPDDR5"
  size: "16 GB"
  bandwidth: "6,400 MT/s"
  
storage:
  emmc: "optional 32-128GB"
  nvme: "PCIe 3.0 x4 M.2 NVMe, 2,100-5,700 MB/s"
  sd: "microSD UHS-I"
  sata: "SATA 3.0 via expansion board"
  
network:
  ethernet: "2.5 Gigabit (Realtek RTL8125BG)"
  wifi: "WiFi 6E (802.11ax)"
  bluetooth: "5.0"
  
ports:
  usb: "2x USB 3.0, 2x USB 2.0, 1x USB-C (DP Alt Mode)"
  gpio: "40-pin GPIO header"
  pcie: "PCIe 3.0 x4 (M.2 slot)"
  display: "HDMI 2.1, eDP, MIPI DSI, DP over USB-C"
  
power:
  input: "5V/4A USB-C PD or DC barrel"
  typical: "10-15W idle, 15-25W full load"
  fan: "2-pin 5V PWM header"
  
cost:
  16gb_model: "$125"
  8gb_model: "$95"
  
linux:
  distributions: "Armbian Ubuntu 24.04, Armbian Debian, Orange Pi OS"
  kernel: "6.1.x (vendor) / 6.6+ (mainline WIP)"
  docker: "Fully supported (linux/arm64)"
  gpu_drivers: "Panfrost (Mali-G610, mainline)"
  npu_drivers: "RKNN driver (vendor)"
  
cluster_suitability:
  score: "9.5/10"
  strengths:
    - "16GB RAM at $125 price point"
    - "2.5GbE networking"
    - "PCIe 3.0 x4 NVMe storage"
    - "Mali-G610 GPU with OpenCL/Vulkan"
    - "6 TOPS NPU for AI inference"
    - "Full Docker support"
    - "Low power (15-25W)"
    - "Active cooling support"
  limitations:
    - "Vendor kernel for NPU (mainline NPU support WIP)"
    - "No AVX equivalent (ARM NEON only)"
    - "Mali GPU drivers less mature than AMDGPU"
```

### 2.2 SBC Node Agent

SBCs run the **standard Linux Node Agent** compiled for `linux/arm64`. No special adaptation needed — they are first-class cluster citizens.

```bash
# Cross-compile HelixCluster agent for ARM64
GOOS=linux GOARCH=arm64 CGO_ENABLED=1 \
  CC=aarch64-linux-gnu-gcc \
  go build -o helix-agent-arm64 ./cmd/agent

# Deploy to Orange Pi 5 Max
scp helix-agent-arm64 orangepi@192.168.1.100:/opt/helix/
ssh orangepi@192.168.1.100 "sudo systemctl restart helix-agent"
```

### 2.3 SBC-Specific Capabilities

```go
package sbc

// SBCAdapter handles SBC-specific hardware monitoring
type SBCAdapter struct {
    Model string // "orange_pi_5_max", "raspberry_pi_5", etc.
}

// GPIO fan control
func (a *SBCAdapter) SetFanSpeed(pwm int) error {
    // Write to /sys/class/gpio/ or /sys/class/hwmon/
    // Orange Pi 5 Max: /sys/class/hwmon/hwmon0/pwm1
}

// NPU inference (RK3588 only)
func (a *SBCAdapter) RunNPUInference(model string, input []byte) ([]byte, error) {
    // Use RKNN Toolkit2 C API
    // rknn_init → rknn_inputs_set → rknn_run → rknn_outputs_get
}

// Check if NPU is available
func (a *SBCAdapter) HasNPU() bool {
    return a.Model == "orange_pi_5_max" || a.Model == "h96_max_v58"
}
```

---

## 3. ANDROID COMPUTE NODES (Tier 5-6)

### 3.1 Two Deployment Models

```
┌─────────────────────────────────────────────────────────────────┐
│              ANDROID DEPLOYMENT MODELS                           │
│                                                                  │
│  MODEL A: Armbian Linux (TV Boxes)          ← RECOMMENDED       │
│  ┌──────────────────────────────────────┐                       │
│  │ Replace Android with Armbian Linux   │                       │
│  │ Full Linux, Docker, standard agent   │                       │
│  │ Same as SBC — first-class citizen    │                       │
│  │ TRUST_LEVEL: STANDARD                │                       │
│  └──────────────────────────────────────┘                       │
│                                                                  │
│  MODEL B: Android APK (Phones/Tablets)                           │
│  ┌──────────────────────────────────────┐                       │
│  │ HelixCluster Agent APK               │                       │
│  │ Termux + foreground service          │                       │
│  │ Vulkan compute for GPU workloads     │                       │
│  │ Charging-gated scheduling            │                       │
│  │ TRUST_LEVEL: SEMI                    │                       │
│  └──────────────────────────────────────┘                       │
└─────────────────────────────────────────────────────────────────┘
```

### 3.2 Android Agent Architecture (Model B)

```
┌──────────────────────────────────────────────────────────────┐
│              ANDROID AGENT APK ARCHITECTURE                   │
│                                                              │
│  ┌────────────────────────────────────────────────────────┐  │
│  │  Android App (Kotlin/Java wrapper)                     │  │
│  │                                                        │  │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────────────────┐   │  │
│  │  │ Foreground│ │ Battery  │ │ Notification         │   │  │
│  │  │ Service  │ │ Monitor  │ │ Manager (status)     │   │  │
│  │  │          │ │          │ │                      │   │  │
│  │  │ - Keep   │ │ - isCharging│ - Show compute    │   │  │
│  │  │   alive  │ │ - battery%  │   status           │   │  │
│  │  │ - Heart- │ │ - thermal   │ - Work unit progress│   │  │
│  │  │   beat   │ │             │ - Results uploaded  │   │  │
│  │  └──────────┘ └──────────┘ └──────────────────────┘   │  │
│  └────────────────────┬───────────────────────────────────┘  │
│                       │ JNI                                   │
│  ┌────────────────────▼───────────────────────────────────┐  │
│  │  Native Layer (C/C++/Go via NDK + gomobile)            │  │
│  │                                                        │  │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────────────────┐   │  │
│  │  │ Network  │ │ Compute  │ │ Workload             │   │  │
│  │  │ Client   │ │ Engine   │ │ Executor             │   │  │
│  │  │          │ │          │ │                      │   │  │
│  │  │ - QUIC   │ │ - Vulkan │ │ - Shell commands     │   │  │
│  │  │ - MQTT   │ │   GPU    │ │ - Python scripts     │   │  │
│  │  │ - mTLS   │ │ - NNAPI  │ │ - Small ML models    │   │  │
│  │  │          │ │   NPU    │ │ - Result collection  │   │  │
│  │  └──────────┘ └──────────┘ └──────────────────────┘   │  │
│  └────────────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────────────┘
```

### 3.3 Android Agent: Foreground Service

```kotlin
// AndroidManifest.xml
<service
    android:name=".HelixClusterService"
    android:foregroundServiceType="dataSync"
    android:exported="false" />

// Kotlin: Foreground Service
class HelixClusterService : Service() {
    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        // Create notification (REQUIRED for foreground service)
        val notification = NotificationCompat.Builder(this, CHANNEL_ID)
            .setContentTitle("HelixCluster Compute")
            .setContentText("Contributing compute power to cluster")
            .setSmallIcon(R.drawable.ic_compute)
            .setOngoing(true)
            .build()
        
        startForeground(1, notification)
        
        // Start native agent
        startNativeAgent()
        
        return START_STICKY // Restart if killed
    }
    
    private fun startNativeAgent() {
        // Load native Go library via gomobile
        System.loadLibrary("helix-agent")
        
        // Start agent with Android-specific config
        HelixAgent.start(Config(
            controlPlane = "100.64.0.1:8443",
            trustLevel = TrustLevel.SEMI,
            protocol = Protocol.QUIC, // Best for mobile
            powerGating = PowerGating.ONLY_WHEN_CHARGING,
            maxBatteryDrain = 10, // % per hour max
            workTypes = listOf(
                WorkType.SMALL_BATCH,
                WorkType.AI_INFERENCE_INT8,
                WorkType.DATA_PROCESSING
            )
        ))
    }
}
```

### 3.4 Charging-Gated Scheduling

```go
package android

// PowerGater decides when the device can accept work
type PowerGater struct {
    config PowerConfig
}

type PowerConfig struct {
    OnlyWhenCharging     bool
    MaxBatteryPercent    int    // Only work if battery > this
    NightModeOnly        bool   // Only work 22:00-06:00
    MaxCpuTempC          int    // Throttle if CPU > this
    MaxBatteryDrainPct   int    // Max % battery per hour
}

func (g *PowerGater) CanAcceptWork() (bool, string) {
    // Check charging state
    if g.config.OnlyWhenCharging && !battery.IsCharging() {
        return false, "not_charging"
    }
    
    // Check battery level
    if battery.GetLevel() < g.config.MaxBatteryPercent {
        return false, fmt.Sprintf("battery_low_%d%%", battery.GetLevel())
    }
    
    // Check night mode
    if g.config.NightModeOnly {
        hour := time.Now().Hour()
        if hour >= 6 && hour < 22 {
            return false, "daytime"
        }
    }
    
    // Check CPU temperature
    if cpu.GetTemperature() > g.config.MaxCpuTempC {
        return false, fmt.Sprintf("cpu_hot_%dC", cpu.GetTemperature())
    }
    
    return true, ""
}
```

### 3.5 Android GPU/NPU Compute

```java
// Vulkan Compute on Android — same SPIR-V runs on Adreno, Mali, all GPUs
public class VulkanCompute {
    static { System.loadLibrary("vulkan_compute"); }
    
    public native long createDevice();
    public native long compileShader(String spirvPath);
    public native void dispatchCompute(long device, long shader, 
                                        int groupsX, int groupsY, int groupsZ);
    
    // Run LLM inference via MLC LLM
    public void runLLMInference(String modelPath, String prompt) {
        // MLC LLM loads model, quantizes for device, runs on GPU/NPU
        MLCEngine engine = new MLCEngine();
        engine.loadModel(modelPath);
        String result = engine.generate(prompt, new Config()
            .setMaxTokens(256)
            .setTemperature(0.7f));
    }
}
```

---

## 4. iOS COMPUTE NODES (Tier 7)

### 4.1 iOS Agent: "Compute Donor" Model

iOS devices use a **pull-based donor model** rather than a persistent node model due to Apple's background execution restrictions.

```swift
// Swift: iOS HelixCluster Agent
import Foundation
import Metal
import CoreML
import BackgroundTasks

@main
struct HelixClusterApp: App {
    var body: some Scene {
        WindowGroup {
            ContentView()
        }
    }
    
    init() {
        // Register background refresh task
        BGTaskScheduler.shared.register(
            forTaskWithIdentifier: "com.helix.compute",
            using: nil
        ) { task in
            handleComputeTask(task as! BGAppRefreshTask)
        }
    }
}

func handleComputeTask(_ task: BGAppRefreshTask) {
    let queue = OperationQueue()
    queue.maxConcurrentOperationCount = 1
    
    let operation = BlockOperation {
        // Check conditions
        guard isCharging() || batteryLevel() > 50 else { return }
        guard isWiFiConnected() else { return }
        
        // Fetch work unit from cluster
        let workUnit = fetchWorkUnit()
        
        // Execute based on type
        switch workUnit.type {
        case .metalCompute:
            executeMetalCompute(workUnit)
        case .coreMLInference:
            executeCoreMLInference(workUnit)
        case .dataProcessing:
            executeDataProcessing(workUnit)
        }
        
        // Upload results
        uploadResults(workUnit.id, results)
    }
    
    task.expirationHandler = {
        queue.cancelAllOperations()
    }
    
    operation.completionBlock = {
        task.setTaskCompleted(success: !operation.isCancelled)
    }
    
    queue.addOperations([operation], waitUntilFinished: false)
}

// Metal Compute for GPU workloads
func executeMetalCompute(_ workUnit: WorkUnit) {
    guard let device = MTLCreateSystemDefaultDevice() else { return }
    
    let commandQueue = device.makeCommandQueue()!
    let library = try! device.makeLibrary(source: workUnit.shaderSource, options: nil)
    let pipeline = try! device.makeComputePipelineState(function: library.makeFunction(name: "compute")!)
    
    let commandBuffer = commandQueue.makeCommandBuffer()!
    let encoder = commandBuffer.makeComputeCommandEncoder()!
    encoder.setComputePipelineState(pipeline)
    // ... set buffers, dispatch
    encoder.endEncoding()
    commandBuffer.commit()
    commandBuffer.waitUntilCompleted()
}

// CoreML inference using Neural Engine
func executeCoreMLInference(_ workUnit: WorkUnit) {
    let config = MLModelConfiguration()
    config.computeUnits = .all // CPU + GPU + Neural Engine
    
    let model = try! MLModel(contentsOf: workUnit.modelURL, configuration: config)
    let prediction = try! model.prediction(from: workUnit.inputProvider)
    // Neural Engine: 35 TOPS on A18 Pro, 38 TOPS on M4
}
```

### 4.2 iOS Background Execution Schedule

```swift
// Schedule periodic background refresh
func scheduleBackgroundRefresh() {
    let request = BGAppRefreshTaskRequest(identifier: "com.helix.compute")
    request.earliestBeginDate = Date(timeIntervalSinceNow: 15 * 60) // 15 min minimum
    
    do {
        try BGTaskScheduler.shared.submit(request)
    } catch {
        print("Could not schedule: \(error)")
    }
}

// iOS also supports BGProcessingTask for longer work (up to ~30 min)
func scheduleProcessingTask() {
    let request = BGProcessingTaskRequest(identifier: "com.helix.longcompute")
    request.requiresNetworkConnectivity = true
    request.requiresExternalPower = true // Only when charging
    
    try? BGTaskScheduler.shared.submit(request)
}
```

---

## 5. HARMONYOS COMPUTE NODES (Tier 8)

### 5.1 HarmonyOS Agent

```typescript
// ArkTS/TypeScript: HarmonyOS HelixCluster Agent
// HarmonyOS uses ArkTS (TypeScript superset) for app development

import { backgroundTaskManager } from '@kit.BackgroundTasksKit';
import { hilog } from '@kit.PerformanceAnalysisKit';

class HelixAgent {
  private config: AgentConfig;
  
  async start() {
    // Register background task
    backgroundTaskManager.requestSuspendDelay('HelixCluster compute', () => {
      hilog.info(0x0000, 'HelixCluster', 'Background task expired');
    });
    
    // Connect to cluster via WebSocket
    this.connectToCluster();
    
    // Report capabilities
    this.reportCapabilities();
  }
  
  async reportCapabilities() {
    const capabilities = {
      deviceType: 'HarmonyOS',
      model: deviceInfo.model,
      cpu: 'Kirin 9000S',
      npu: 'Da Vinci 6 TOPS',
      ram: appManager.getAppMemorySize(),
      trustLevel: 'SEMI'
    };
    await this.sendToCluster('capabilities', capabilities);
  }
  
  async runNPUInference(model: string, input: ArrayBuffer): Promise<ArrayBuffer> {
    // Use HiAI Engine for NPU inference
    // Or use MindSpore Lite (Huawei's ML framework)
    const result = await hiAI.infer({
      modelPath: model,
      inputData: input,
      deviceType: 'NPU' // Use Da Vinci NPU
    });
    return result.output;
  }
}
```

---

## 6. EDGE PROTOCOL STACK

### 6.1 Protocol Selection by Device Tier

```
┌────────────────────────────────────────────────────────────────┐
│              PHASE 3 PROTOCOL STACK                             │
│                                                                │
│  TIER 3-5 (SBC, TV Box) ──────► MQTT + gRPC                   │
│  ├─ MQTT: Control messages (lightweight, reliable)            │
│  ├─ gRPC: Structured RPC (full feature set)                   │
│  └─ WireGuard: Encrypted mesh                                 │
│                                                                │
│  TIER 6 (Android Phone) ──────► QUIC + MQTT                    │
│  ├─ QUIC: Primary transport (0-RTT, connection migration)     │
│  ├─ MQTT: Work unit dispatch / results                        │
│  └─ WireGuard: Mesh VPN (or native IPsec)                     │
│                                                                │
│  TIER 7 (iOS) ────────────────► HTTP/2 + MQTT                 │
│  ├─ HTTP/2: Background fetch friendly                         │
│  ├─ MQTT: Subscribe to work queue                             │
│  └─ URLSession: Native iOS networking                         │
│                                                                │
│  TIER 8 (HarmonyOS) ──────────► WebSocket + MQTT              │
│  ├─ WebSocket: HarmonyOS native support                       │
│  ├─ MQTT: Standard messaging                                  │
│  └─ NearLink: Short-range device mesh (HarmonyOS unique)      │
│                                                                │
│  ALL TIERS: Arrow Flight for data transfer                     │
│  ALL TIERS: Cap'n Proto for serialization                     │
└────────────────────────────────────────────────────────────────┘
```

### 6.2 Work Unit Format for Edge Devices

Edge devices receive **small, self-contained work units** — not persistent sessions.

```protobuf
// edge_work_unit.proto
message EdgeWorkUnit {
  string id = 1;
  WorkType type = 2;
  bytes encrypted_payload = 3;     // Encrypted with device pubkey
  int32 max_duration_seconds = 4;   // Kill after this
  int32 max_memory_mb = 5;          // Memory limit
  int32 max_cpu_percent = 6;        // CPU throttle
  VerifyMode verify_mode = 7;       // How to verify results
  
  enum WorkType {
    SMALL_BATCH = 0;        // Shell script, data processing
    AI_INFERENCE_INT8 = 1;  // NPU/GPU inference
    AI_INFERENCE_FP16 = 2;  // GPU inference
    ENCODE_TRANSCODE = 3;   // Video/audio encoding
    COMPRESS_DECOMPRESS = 4; // Data compression
    TEST_COMPILE = 5;       // distcc compilation unit
    CRYPTO_HASH = 6;        // Hashing, proof-of-work
  }
  
  enum VerifyMode {
    LLM_VERIFY = 0;         // LLMsVerifier checks output
    REDUNDANT = 1;          // Compare with trusted node
    CHECKSUM = 2;           // Simple hash verification
    TRIVIAL = 3;            // No verification needed
  }
}

message EdgeWorkResult {
  string work_unit_id = 1;
  bytes output = 2;
  bytes signature = 3;             // ed25519 sign(output)
  int32 duration_ms = 4;
  int32 memory_peak_mb = 5;
  DeviceMetrics metrics = 6;
}
```

---

## 7. POWER-AWARE SCHEDULER

### 7.1 Edge-Aware Scheduling Plugin

```go
package scheduler

// EdgeAwarePlugin handles scheduling for edge/mobile devices
type EdgeAwarePlugin struct {
    thermalThreshold    int
    batteryMinPercent   int
    nightModeStart      int  // hour (22 = 10 PM)
    nightModeEnd        int  // hour (6 = 6 AM)
}

func (p *EdgeAwarePlugin) Filter(ctx context.Context,
    state *framework.CycleState, pod *v1.Pod,
    nodeInfo *framework.NodeInfo) *framework.Status {
    
    node := nodeInfo.Node()
    tier := node.Labels["device-tier"]
    
    // Tier 6-8 (mobile/edge) specific filters
    if tier >= "6" {
        // Only schedule work units (not sessions)
        if !isWorkUnit(pod) {
            return framework.NewStatus(framework.Unschedulable,
                "Edge devices only accept work units")
        }
        
        // Check device is available
        if !isDeviceAvailable(node) {
            return framework.NewStatus(framework.Unschedulable,
                "Device not available (sleeping/offline)")
        }
        
        // Check thermal state
        if getDeviceTemp(node) > p.thermalThreshold {
            return framework.NewStatus(framework.Unschedulable,
                "Device thermal throttling")
        }
        
        // Check battery for mobile
        if tier == "6" || tier == "8" {
            battery := getBatteryLevel(node)
            if battery < p.batteryMinPercent {
                return framework.NewStatus(framework.Unschedulable,
                    "Battery too low")
            }
            if !isCharging(node) {
                return framework.NewStatus(framework.Unschedulable,
                    "Not charging")
            }
        }
    }
    
    return framework.NewStatus(framework.Success)
}

// Prefer edge devices for inference workloads
// Prefer SBCs for batch processing
// Score based on device availability and thermal headroom
func (p *EdgeAwarePlugin) Score(ctx context.Context,
    state *framework.CycleState, pod *v1.Pod,
    nodeInfo *framework.NodeInfo) (int64, *framework.Status) {
    
    node := nodeInfo.Node()
    tier := node.Labels["device-tier"]
    score := int64(50) // Base score
    
    switch tier {
    case "3": // SBC Premium
        if isAIWorkload(pod) { score += 30 } // NPU advantage
        if isStorageWorkload(pod) { score += 20 } // NVMe speed
    case "4": // SBC Standard
        if isBatchWorkload(pod) { score += 20 }
    case "5": // Android TV (Linux)
        if isHeadlessWorkload(pod) { score += 25 }
    case "6": // Android Phone
        if isSmallWorkload(pod) { score += 15 }
        score += int64(getBatteryLevel(node)) / 5 // Prefer high battery
    case "7": // iOS
        if isInferenceWorkload(pod) { score += 40 } // NPU advantage
        score -= 20 // Penalize due to intermittent availability
    case "8": // HarmonyOS
        if isNPUWorkload(pod) { score += 35 } // Da Vinci NPU
    }
    
    // Thermal bonus
    temp := getDeviceTemp(node)
    if temp < 50 { score += 15 }
    if temp > 70 { score -= 30 }
    
    return score, nil
}
```

---

## 8. SECURITY MODEL FOR EDGE DEVICES

### 8.1 Trust Levels (All Phases)

```
┌────────────────────────────────────────────────────────────────┐
│              HELIXCLUSTER TRUST LEVELS (ALL PHASES)             │
│                                                                │
│  FULL    │ PC Workstations, Control Plane nodes                │
│          │ Full cluster access, sensitive data OK               │
│          │ Phase 1: Standard PCs                               │
│                                                                │
│  STANDARD│ SBCs, Armbian TV Boxes, Console (Linux mode)        │
│          │ Full worker access, no sensitive data                │
│          │ Phase 1: Trusted PCs, Phase 3: SBCs, TV Boxes       │
│                                                                │
│  SEMI    │ Consoles (Orbis), Android Phones, HarmonyOS         │
│          │ Encrypted work units, verified outputs               │
│          │ Phase 2: PS4/PS5, Phase 3: Android, HarmonyOS       │
│                                                                │
│  EDGE_   │ iOS Devices                                         │
│  DONOR   │ Pull-based work, opportunistic compute               │
│          │ Phase 3: iPhone, iPad                               │
│                                                                │
└────────────────────────────────────────────────────────────────┘
```

### 8.2 Workload Restriction Matrix

| Workload Type | FULL | STANDARD | SEMI | EDGE_DONOR |
|--------------|------|----------|------|------------|
| Interactive session | ✓ | ✓ | ✗ | ✗ |
| AOSP build (distcc) | ✓ | ✓ | ✓ | ✗ |
| AI inference (GPU) | ✓ | ✓ | ✓ | ✓ |
| AI inference (NPU) | ✓ | ✓ | ✓ | ✓ |
| Video transcode | ✓ | ✓ | ✓ | ✗ |
| Data processing | ✓ | ✓ | ✓ | ✓ |
| Small batch jobs | ✓ | ✓ | ✓ | ✓ |
| Sensitive data | ✓ | ✗ | ✗ | ✗ |
| Persistent storage | ✓ | ✓ | ✗ | ✗ |
| Network relay | ✓ | ✓ | ✓ | ✗ |

---

## 9. IMPLEMENTATION PLAN: PHASE 3 TASKS

### 9.1 New Tasks (24 tasks, ~200 hours)

| Phase | Task ID | Description | Hours | Priority |
|-------|---------|-------------|-------|----------|
| **0** | E-0.1 | ARM64 cross-compilation toolchain | 4 | P0 |
| **0** | E-0.2 | SBC Agent (Orange Pi 5 Max target) | 8 | P0 |
| **0** | E-0.3 | RK3588 NPU integration (RKNN SDK) | 12 | P1 |
| **0** | E-0.4 | Mali-G610 Vulkan compute validation | 6 | P0 |
| **0** | E-0.5 | Android Agent APK scaffolding (Kotlin+NDK) | 12 | P0 |
| **0** | E-0.6 | Android foreground service framework | 8 | P0 |
| **0** | E-0.7 | Android BatteryManager integration | 4 | P0 |
| **0** | E-0.8 | QUIC client for Android (lightweight) | 8 | P1 |
| **0** | E-0.9 | MQTT client for edge devices | 6 | P0 |
| **0** | E-0.10 | iOS Agent scaffolding (Swift) | 12 | P1 |
| **0** | E-0.11 | iOS Metal compute integration | 8 | P1 |
| **0** | E-0.12 | iOS CoreML inference engine | 8 | P2 |
| **0** | E-0.13 | HarmonyOS Agent (ArkTS) | 12 | P2 |
| **1** | E-1.1 | Edge node registration (all tiers) | 6 | P0 |
| **1** | E-1.2 | Edge heartbeat (battery, thermal, network) | 6 | P0 |
| **1** | E-1.3 | Edge protocol gateway (MQTT/QUIC/WS) | 8 | P0 |
| **2** | E-2.1 | EdgeAware scheduler plugin | 10 | P0 |
| **2** | E-2.2 | Power-gated scheduling (charging-only) | 6 | P0 |
| **2** | E-2.3 | Workload quantization per device tier | 8 | P1 |
| **2** | E-2.4 | NPU backend (RKNN, NNAPI, CoreML, CANN) | 16 | P1 |
| **5** | E-5.1 | MLC LLM integration for mobile | 10 | P1 |
| **7** | E-7.1 | Edge device chaos tests | 8 | P0 |
| **7** | E-7.2 | Battery/thermal stress testing | 6 | P0 |
| **8** | E-8.1 | Edge setup wizard | 10 | P0 |
| **8** | E-8.2 | APK/IPA distribution system | 8 | P1 |

**Total Phase 3 Additional Tasks: 26 tasks, ~200 hours (~5 weeks)**

---

## 10. WHAT PHASE 3 FILLS THAT OTHERS CANNOT

| Gap | How Edge Devices Fill It | Impact |
|-----|-------------------------|--------|
| **NPU Compute (AI Inference)** | 6-38 TOPS per device, dedicated AI accelerators | **100+ TOPS aggregate** from edge pool |
| **Ultra-Low-Cost Scaling** | $50-130 per device (TV boxes, SBCs) | **10x more nodes** for same budget |
| **Idle Device Utilization** | Phones/tablets idle 22h/day | **Billions of device-hours** available |
| **Power-Efficient Inference** | NPUs at 1-5W vs GPU at 100W+ | **20-50x better perf/watt** for inference |
| **Geographic Distribution** | Phones everywhere | **Edge compute at the edge** — low latency |
| **Elastic Capacity** | Users opt in/out dynamically | **Community-driven scaling** |
| **ARM Ecosystem** | ARM64 optimization benefits all ARM nodes | **Better code for Apple Silicon too** |
| **Specialized Hardware** | Da Vinci NPU, Hexagon DSP, Apple ANE | **Unique accelerators** not on PC/Console |
