# Chapter 3: Console Integration Architecture

## 3.1 High-Level Integration Overview

Console nodes are first-class citizens in the HelixCluster with specific adaptations. They connect through the same WireGuard mesh, register through the same Node Discovery service, and execute work through the same scheduler — but with a **semi-trusted security model** and **console-specific adapters**.

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                    HELIXCLUSTER WITH CONSOLE NODES                           │
│                                                                             │
│   Control Plane (PC/Laptop)                    Console Worker Nodes         │
│   ┌─────────────────────────┐                  ┌─────────────────────────┐  │
│   │  API Gateway            │                  │  PS4 Pro (Tier 2)       │  │
│   │  Node Discovery         │◄── WireGuard ──►│  Linux 6.15 + Docker    │  │
│   │  Resource Scheduler     │     Mesh VPN     │  Console Node Agent     │  │
│   │  Session Manager        │                  │  Vulkan Compute         │  │
│   │  GPU Compute Engine     │◄─────────────────┤  llama.cpp inference    │  │
│   │  Health Monitor         │                  └─────────────────────────┘  │
│   │  LLM Brain              │                  ┌─────────────────────────┐  │
│   │  Build Service          │◄── WireGuard ──►│  PS5 (Tier 1)           │  │
│   │  Security Manager       │     Mesh VPN     │  Ubuntu 24.04           │  │
│   │  Backup Service         │                  │  Console Node Agent     │  │
│   └─────────────────────────┘                  │  Vulkan Compute         │  │
│          │                                     │  Orbis I/O Agent        │  │
│          │                                     └─────────────────────────┘  │
│          │                                                                   │
│          │    ┌────────────────────────────────────────────────────────┐    │
│          └───►│           SEMI-TRUSTED SECURITY MODEL                 │    │
│               │  • Encrypted work units only                          │    │
│               │  • All results verified (LLMsVerifier/redundant)      │    │
│               │  • No access to cluster state (etcd)                  │    │
│               │  • No sensitive data ever on console                  │    │
│               │  • Idempotent workloads only                          │    │
│               └────────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────────────────────┘
```

## 3.2 Console Node Agent Architecture

The Console Node Agent is a Go binary compiled for `linux/amd64` that runs as a systemd service on the console's Linux distribution.

### Component Diagram

```
┌──────────────────────────────────────────────────────────────┐
│              CONSOLE NODE AGENT (Go binary)                   │
│                                                              │
│  ┌────────────┐  ┌────────────┐  ┌────────────────────────┐ │
│  │   Core     │  │  Console   │  │     Workload Engine     │ │
│  │  Engine    │  │  Adapter   │  │                         │ │
│  │            │  │  Layer     │  │ ┌──────┐ ┌──────────┐  │ │
│  │ - Heartbeat│  │            │  │ │Batch │ │ GPU      │  │ │
│  │ - Resource │  │ - Thermal  │  │ │Worker│ │ Compute  │  │ │
│  │   Reporter │  │   Monitor  │  │ └──────┘ │ (Vulkan) │  │ │
│  │ - Task     │  │ - Power    │  │ ┌──────┐ └──────────┘  │ │
│  │   Receiver │  │   Manager  │  │ │AI    │ ┌──────────┐  │ │
│  │ - Result   │  │ - Jailbreak│  │ │Infer │ │ Storage  │  │ │
│  │   Reporter │  │   Monitor  │  │ │(LLM) │ │ (cache)  │  │ │
│  │ - WireGuard│  │ - Auto-    │  │ └──────┘ └──────────┘  │ │
│  │   Peer     │  │   Exploit  │  │ ┌──────┐               │ │
│  └────────────┘  │ - GPU      │  │ │Video │               │ │
│                  │   Monitor  │  │ │Trans │               │ │
│                  └────────────┘  │ └──────┘               │ │
│                                  └────────────────────────┘ │
└──────────────────────────────────────────────────────────────┘
```

### Core Engine

The Core Engine is identical to the PC Node Agent but with console-specific adaptations:

```go
package agent

// ConsoleNodeAgent extends the base NodeAgent with console-specific capabilities
type ConsoleNodeAgent struct {
    *BaseNodeAgent  // Embedded: heartbeat, resource reporting, task execution
    
    ConsoleType     ConsoleType     // PS4_FAT, PS4_PRO, PS5, PS5_PRO
    Adapter         *ConsoleAdapter // Thermal, power, jailbreak management
    TrustLevel      TrustLevel      // Always SEMI for consoles
    
    // GPU compute (uses same Vulkan backend as PC)
    VulkanBackend   *vulkan.ComputeBackend
    
    // AI inference (llama.cpp subprocess)
    LLMEngine       *llama.InferenceEngine
}

func (a *ConsoleNodeAgent) RegisterWithCluster() error {
    node := &NodeRegistration{
        Type:        NODE_TYPE_CONSOLE,
        ConsoleType: a.ConsoleType,
        TrustLevel:  TRUST_SEMI,
        Resources:   a.scanConsoleResources(),
        Capabilities: []Capability{
            {Name: "gpu-vulkan-compute", Type: CAP_GPU},
            {Name: "ai-inference-llama", Type: CAP_AI},
            {Name: "batch-processing", Type: CAP_BATCH},
            {Name: "video-transcode", Type: CAP_VIDEO},
        },
    }
    return a.BaseNodeAgent.Register(node)
}

func (a *ConsoleNodeAgent) scanConsoleResources() *ConsoleNodeResources {
    return &ConsoleNodeResources{
        BaseResources: a.BaseNodeAgent.ScanResources(),
        ConsoleSpecific: ConsoleSpecificInfo{
            Model:           a.ConsoleType,
            Firmware:        a.Adapter.GetFirmwareVersion(),
            JailbreakVersion: a.Adapter.GetJailbreakVersion(),
            Thermal: ThermalState{
                CPUCurrentC:     a.Adapter.GetCPUTemp(),
                GPUCurrentC:     a.Adapter.GetGPUTemp(),
                FanSpeedPct:     a.Adapter.GetFanSpeed(),
                Throttling:      a.Adapter.IsThrottling(),
            },
            Power: PowerState{
                CurrentWatts: a.Adapter.GetPowerConsumption(),
            },
            GPU: GPUState{
                ClockMHz:        a.Adapter.GetGPUClock(),
                GPUTemperatureC: a.Adapter.GetGPUTemp(),
            },
        },
    }
}
```

### Console Adapter Layer

The Console Adapter is the unique component that handles console-specific hardware:

```go
package console

// ConsoleAdapter manages console-specific hardware interfaces
type ConsoleAdapter struct {
    consoleType ConsoleType
    sysfsPath   string       // /sys/class/amdtep/ on PS4/PS5
}

// Thermal Management
func (a *ConsoleAdapter) GetCPUTemp() int {
    // Read from /sys/class/thermal/thermal_zone*/temp
    // PS4/PS5 expose thermal zones via standard Linux thermal framework
}

func (a *ConsoleAdapter) GetGPUTemp() int {
    // Read from AMDGPU sysfs: /sys/class/drm/card0/device/hwmon/temp1_input
}

func (a *ConsoleAdapter) SetFanSpeed(percent int) error {
    // Write to /sys/class/hwmon/hwmon*/pwm1
    // Range: 0-100%
}

func (a *ConsoleAdapter) IsThrottling() bool {
    cpuTemp := a.GetCPUTemp()
    gpuTemp := a.GetGPUTemp()
    // PS4 Pro throttles at ~85°C CPU, ~80°C GPU
    // PS5 throttles at ~90°C CPU, ~85°C GPU
    return cpuTemp > 85000 || gpuTemp > 80000 // millidegrees
}

// Power Management
func (a *ConsoleAdapter) GetPowerConsumption() float64 {
    // Read from /sys/class/power_supply/ if available
    // Fallback: estimate from CPU/GPU load + thermal state
}

// Jailbreak Management
func (a *ConsoleAdapter) IsJailbroken() bool {
    // Check if homebrew capabilities are available
    // On Linux: always true (kexec succeeded)
    // On Orbis: check for GoldHen/etaHEN presence
    return a.detectJailbreakMarker()
}

func (a *ConsoleAdapter) TriggerExploit() error {
    // Signal auto-exploit hardware to send payload
    // Via USB serial to ESP32/Luckfox
    // Or: trigger software exploit chain
}

// Auto-exploit via USB serial
func (a *ConsoleAdapter) initAutoExploit() error {
    port, err := serial.Open("/dev/ttyUSB0", &serial.Config{Baud: 115200})
    if err != nil {
        return fmt.Errorf("auto-exploit hardware not found: %w", err)
    }
    // Configure ESP32 for automatic exploit on console boot
    _, err = port.Write([]byte("CONFIG:AUTO_EXPLOIT=ON\n"))
    return err
}
```

## 3.3 Vulkan Compute Integration

### Universal GPU Backend

The most important architectural decision for Phase 2: **no console-specific GPU code is needed**. Our existing Vulkan Compute Backend works on all consoles without modification.

```go
package vulkan

// ComputeBackend — same code runs on PC, PS4, PS4 Pro, PS5, PS5 Pro
type ComputeBackend struct {
    instance    vk.Instance
    device      vk.Device
    queue       vk.Queue
    queueFamily uint32
    memoryProps vk.PhysicalDeviceMemoryProperties
}

// Initialize discovers the GPU automatically
func NewComputeBackend() (*ComputeBackend, error) {
    // Vulkan enumerates all devices:
    // On PS4: AMD GCN Liverpool (radv)
    // On PS4 Pro: AMD GCN Polaris (radv)
    // On PS5: AMD RDNA2 Oberon (radv)
    // On PC: Whatever AMD/NVIDIA/Intel GPU is present
    // All use the SAME driver interface
}

// CompileShader compiles GLSL → SPIR-V → GPU binary
// SPIR-V is the universal intermediate representation
func (b *ComputeBackend) CompileShader(glslSource string) (*Shader, error) {
    // glslangValidator compiles GLSL to SPIR-V
    // SPIR-V is loaded by Vulkan on ANY GPU
}
```

### AI Inference: llama.cpp on Console

```go
package llama

// InferenceEngine wraps llama.cpp for console AI workloads
type InferenceEngine struct {
    modelPath    string
    gpuLayers    int      // 99 = offload all to GPU
    port         int      // HTTP server port
    process      *os.Process
}

func (e *InferenceEngine) Start() error {
    // Launch llama.cpp server with Vulkan backend
    cmd := exec.Command("/opt/llama.cpp/llama-server",
        "-m", e.modelPath,
        "--gpu-layers", strconv.Itoa(e.gpuLayers),
        "--ctx-size", "8192",
        "--port", strconv.Itoa(e.port),
        "--host", "0.0.0.0",
    )
    // Set Vulkan device selection
    cmd.Env = append(os.Environ(),
        "GGML_VULKAN_DEVICE=0",  // Use first Vulkan GPU
    )
    return cmd.Start()
}

// Expected performance:
// PS4:    ~25 tok/s (3B model), ~9 tok/s (7B model)
// PS4 Pro: ~55 tok/s (3B model), ~20 tok/s (7B model)  
// PS5:     ~104 tok/s (3B model), ~38 tok/s (7B MoE)
```

## 3.4 Semi-Trusted Security Model

### Architecture

Console nodes operate at `TRUST_LEVEL = SEMI`. This is a deliberate security posture acknowledging that jailbroken consoles have fully compromised kernels.

```
┌────────────────────────────────────────────────────────────────┐
│               SEMI-TRUSTED NODE FLOW                           │
│                                                                │
│  1. Control Plane creates encrypted work unit                  │
│     (encrypted with console's public key)                      │
│                                                                │
│  2. Work unit sent to console via WireGuard                    │
│                                                                │
│  3. Console decrypts and executes                              │
│     (runs in sandbox/container)                                │
│                                                                │
│  4. Console signs result with its key                          │
│     (ed25519 signature)                                        │
│                                                                │
│  5. Result returned to control plane                           │
│                                                                │
│  6. Control plane verifies result:                             │
│     a) Cryptographic signature valid?                          │
│     b) LLMsVerifier checks output sanity?                      │
│     c) OR: Redundant compute on trusted node matches?          │
│                                                                │
│  7. Only verified results accepted into cluster state          │
│                                                                │
│  CONSOLE CANNOT:                                               │
│  • Read cluster state (etcd)                                   │
│  • Modify any cluster resource                                 │
│  • Access sensitive data                                       │
│  • Initiate any cluster operation                              │
│  • Communicate with other nodes directly                       │
└────────────────────────────────────────────────────────────────┘
```

### Implementation

```go
package security

// SemiTrustedWorkUnit represents work sent to a console node
type SemiTrustedWorkUnit struct {
    ID          string            `json:"id"`
    Type        WorkType          `json:"type"`       // GPU_COMPUTE, AI_INFERENCE, BATCH
    EncryptedPayload []byte       `json:"payload"`    // Encrypted with console pubkey
    Environment map[string]string `json:"env"`        // Container environment
    Timeout     time.Duration     `json:"timeout"`
    VerifyMode  VerifyMode        `json:"verify_mode"` // LLM_VERIFY or REDUNDANT
}

type SemiTrustedResult struct {
    WorkUnitID  string            `json:"work_unit_id"`
    Output      []byte            `json:"output"`
    Signature   []byte            `json:"sig"`        // ed25519 signature
    Metrics     WorkMetrics       `json:"metrics"`    // Duration, GPU util, etc.
    ConsoleID   string            `json:"console_id"`
    Timestamp   time.Time         `json:"timestamp"`
}

func (s *SecurityManager) VerifyConsoleResult(result *SemiTrustedResult) error {
    // 1. Verify signature
    if !ed25519.Verify(consolePubkey, result.Output, result.Signature) {
        return ErrInvalidSignature
    }
    
    // 2. Check timestamp freshness (prevent replay)
    if time.Since(result.Timestamp) > 5*time.Minute {
        return ErrResultStale
    }
    
    // 3. Mode-specific verification
    switch result.VerifyMode {
    case LLM_VERIFY:
        // Use LLMsVerifier to check output sanity
        return s.llmVerifier.CheckOutput(result.Output)
    case REDUNDANT:
        // Compare with trusted node's result
        return s.compareWithTrusted(result)
    case TRIVIAL:
        // No verification needed (already trivial/known result)
        return nil
    }
    
    return nil
}
```

## 3.5 Scheduler Integration: Console-Aware Plugin

The scheduler needs to be aware of console-specific constraints:

```go
package scheduler

// ConsoleAwarePlugin prevents scheduling inappropriate workloads on consoles
type ConsoleAwarePlugin struct {
    thermalThreshold int  // Celsius
}

func (p *ConsoleAwarePlugin) Filter(ctx context.Context, 
    state *framework.CycleState, pod *v1.Pod, 
    nodeInfo *framework.NodeInfo) *framework.Status {
    
    node := nodeInfo.Node()
    
    // Check if target is a console node
    if node.Labels["node-type"] != "console" {
        return framework.NewStatus(framework.Success) // Not a console, allow
    }
    
    // Console-specific filters:
    
    // 1. Don't schedule AVX2-required workloads on PS4
    if node.Labels["console-model"] == "ps4" || node.Labels["console-model"] == "ps4-pro" {
        if requiresAVX2(pod) {
            return framework.NewStatus(framework.Unschedulable, 
                "PS4 lacks AVX2 support")
        }
    }
    
    // 2. Don't schedule >8GB RAM workloads on PS4
    if node.Labels["console-tier"] == "3" && memoryRequest(pod) > 6*GiB {
        return framework.NewStatus(framework.Unschedulable,
            "PS4 has limited RAM")
    }
    
    // 3. Don't schedule sensitive data workloads on any console
    if containsSensitiveData(pod) {
        return framework.NewStatus(framework.Unschedulable,
            "Console nodes cannot access sensitive data")
    }
    
    // 4. Check thermal state
    if isConsoleOverheating(node) {
        return framework.NewStatus(framework.Unschedulable,
            "Console thermal throttling")
    }
    
    // 5. Console nodes only get idempotent workloads
    if !isIdempotent(pod) {
        return framework.NewStatus(framework.Unschedulable,
            "Console nodes require idempotent workloads")
    }
    
    return framework.NewStatus(framework.Success)
}

func (p *ConsoleAwarePlugin) Score(ctx context.Context,
    state *framework.CycleState, pod *v1.Pod,
    nodeInfo *framework.NodeInfo) (int64, *framework.Status) {
    
    node := nodeInfo.Node()
    if node.Labels["node-type"] != "console" {
        return 0, nil // No score modification for non-consoles
    }
    
    score := int64(100)
    
    // Penalize overheating consoles
    cpuTemp, _ := getCPUTemp(node)
    if cpuTemp > 80 {
        score -= int64(cpuTemp - 80) * 5  // -5 points per degree over 80
    }
    
    // Bonus for consoles with good thermal headroom
    if cpuTemp < 60 {
        score += 20
    }
    
    // Prefer PS5 for GPU-intensive workloads
    if isGPUWorkload(pod) && node.Labels["console-tier"] == "1" {
        score += 50
    }
    
    return score, nil
}
```

## 3.6 Health Monitoring: Console-Specific Metrics

```yaml
# Console-specific Prometheus metrics
# These are collected by the Console Adapter and exposed by the node agent

# Temperature metrics
console_cpu_temperature_celsius{node_id="ps4-pro-001"} 72
console_gpu_temperature_celsius{node_id="ps4-pro-001"} 68
console_fan_speed_percent{node_id="ps4-pro-001"} 45

# Power metrics  
console_power_consumption_watts{node_id="ps4-pro-001"} 142.5
console_power_daily_kwh{node_id="ps4-pro-001"} 2.8

# GPU metrics (console-specific)
console_gpu_clock_mhz{node_id="ps4-pro-001"} 911
console_gpu_vram_used_bytes{node_id="ps4-pro-001"} 2147483648
console_gpu_throttling{node_id="ps4-pro-001"} 0

# Jailbreak metrics
console_jailbreak_active{node_id="ps4-pro-001"} 1
console_jailbreak_version{node_id="ps4-pro-001", version="2.4b"} 1
console_linux_uptime_seconds{node_id="ps4-pro-001"} 172800

# Storage health
console_ssd_health_percent{node_id="ps4-pro-001"} 94
console_ssd_wear_level{node_id="ps4-pro-001"} 6
console_ssd_power_on_hours{node_id="ps4-pro-001"} 8760

# Thermal throttling alerts
- alert: ConsoleThermalThrottling
  expr: console_cpu_temperature_celsius > 85 or console_gpu_temperature_celsius > 80
  for: 2m
  labels:
    severity: warning
  annotations:
    summary: "Console {{ $labels.node_id }} is thermal throttling"

- alert: ConsoleJailbreakLost
  expr: console_jailbreak_active == 0
  for: 1m
  labels:
    severity: critical
  annotations:
    summary: "Console {{ $labels.node_id }} lost jailbreak"
```

## 3.7 Auto-Exploit Hardware Integration

For unattended console cluster nodes, auto-exploit hardware automates the jailbreak process:

### ESP32 Auto-Exploit Setup

```cpp
// ESP32 firmware for automatic PS4/PS5 jailbreak
// Connects to console USB port, sends exploit on boot detection

#include <USB.h>
#include <PS4Exploit.h>  // Custom exploit payloads

const int CONSOLE_POWER_SENSE_PIN = 4;  // GPIO connected to console power LED
bool consolePowered = false;

void setup() {
    pinMode(CONSOLE_POWER_SENSE_PIN, INPUT);
    Serial.begin(115200);
    
    // Load exploit payload for target firmware
    loadPayload("goldhen_2.4b_900.bin");
}

void loop() {
    bool currentPower = digitalRead(CONSOLE_POWER_SENSE_PIN);
    
    if (currentPower && !consolePowered) {
        // Console just powered on — send exploit
        Serial.println("Console boot detected, sending exploit...");
        delay(5000);  // Wait for USB stack initialization
        sendExploitPayload();
        consolePowered = true;
    }
    
    if (!currentPower && consolePowered) {
        // Console powered off
        consolePowered = false;
    }
    
    delay(1000);
}
```

### Provisioning Integration

The setup wizard for console nodes includes auto-exploit hardware configuration:

```bash
$ htmux cluster add-console --auto-exploit

[Auto-Exploit Setup]
1. Connect ESP32 to console's front USB port
2. Connect ESP32 to cluster management network (WiFi)
3. Flashing auto-exploit firmware...
   [████████████████] 100%
4. Configuring for firmware 9.00 (detected)...
5. Testing auto-exploit cycle...
   Power off → Power on → Exploit sent ✓ → GoldHen loaded ✓
6. Console will now auto-jailbreak on every boot.
```
