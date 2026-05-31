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
