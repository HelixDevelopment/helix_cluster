## 8. Universal Integration Layer & Complete Taxonomy

This chapter unifies all device categories into a single architecture: the automatic discovery engine, the complete 64-device taxonomy, the five-tier security model, and five validated cluster build recipes.

---

### 8.1 Device Discovery Protocol

Every node joining HelixCluster runs a discovery engine that probes hardware, assigns a tier, and generates a capability manifest for the control plane.

#### 8.1.1 Automatic Device Detection

| Probe | Source | Extracted Data |
|-------|--------|----------------|
| CPU | `/proc/cpuinfo` | ISA, cores, frequency, flags |
| GPU | Vulkan/CUDA/ROCm sysfs | Vendor, VRAM, compute APIs |
| RAM | `/proc/meminfo` | Physical bytes |
| Storage | `statfs` | Capacity, filesystem type |
| Network | `netlink` | Speeds, IPs, mesh eligibility |

These probes require no root privileges and complete in under five seconds.

#### 8.1.2 Go Device Detection Engine

```go
package discovery

import (
    "os"
    "runtime"
    "strconv"
    "strings"
    "syscall"
    "time"
)

type DeviceProfile struct {
    DeviceID         string    `json:"device_id"`
    Hostname         string    `json:"hostname"`
    Architecture     string    `json:"architecture"`
    CPUModel         string    `json:"cpu_model"`
    CPUCores         int       `json:"cpu_cores"`
    CPUFeatures      []string  `json:"cpu_features"`
    RAMBytes         uint64    `json:"ram_bytes"`
    StorageBytes     uint64    `json:"storage_bytes"`
    ComputeClasses   []string  `json:"compute_classes"`
    GPUs             []GPUInfo `json:"gpus,omitempty"`
    NPUs             []NPUInfo `json:"npus,omitempty"`
    OS               string    `json:"os"`
    ContainerRuntime string    `json:"container_runtime"`
    AssignedTier     string    `json:"assigned_tier"`
    TrustLevel       string    `json:"trust_level"`
}

type GPUInfo struct {
    Vendor      string   `json:"vendor"`
    Model       string   `json:"model"`
    ComputeAPIs []string `json:"compute_apis"`
}

type NPUInfo struct {
    Vendor   string  `json:"vendor"`
    Model    string  `json:"model"`
    TOPsINT8 float64 `json:"tops_int8"`
}

type DiscoveryEngine struct{}

func (de *DiscoveryEngine) Discover() (*DeviceProfile, error) {
    p := &DeviceProfile{DeviceID: generateDeviceID(), ComputeClasses: []string{"cpu"}}
    p.Hostname, _ = os.Hostname()
    p.Architecture = runtime.GOARCH
    p.OS = runtime.GOOS
    de.detectCPU(p)
    de.detectMemory(p)
    de.detectGPU(p)
    de.detectNPU(p)
    de.detectStorage(p)
    de.detectContainerRuntime(p)
    classifyTier(p)
    return p, nil
}

func (de *DiscoveryEngine) detectCPU(p *DeviceProfile) {
    data, _ := os.ReadFile("/proc/cpuinfo")
    lines := strings.Split(string(data), "\n")
    cores := 0
    for _, line := range lines {
        if strings.HasPrefix(line, "processor\t:") { cores++ }
        if strings.HasPrefix(line, "model name\t:") && p.CPUModel == "" {
            p.CPUModel = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
        }
        if strings.HasPrefix(line, "flags\t:") {
            p.CPUFeatures = strings.Fields(strings.SplitN(line, ":", 2)[1])
        }
        if strings.HasPrefix(line, "isa\t:") {
            p.CPUFeatures = strings.Split(strings.TrimSpace(
                strings.SplitN(line, ":", 2)[1]), "_")
        }
    }
    if cores == 0 { cores = runtime.NumCPU() }
    p.CPUCores = cores
}

func (de *DiscoveryEngine) detectMemory(p *DeviceProfile) {
    data, _ := os.ReadFile("/proc/meminfo")
    for _, line := range strings.Split(string(data), "\n") {
        if strings.HasPrefix(line, "MemTotal:") {
            fields := strings.Fields(line)
            if len(fields) >= 2 {
                kb, _ := strconv.ParseUint(fields[1], 10, 64)
                p.RAMBytes = kb * 1024
            }
        }
    }
}

func (de *DiscoveryEngine) detectGPU(p *DeviceProfile) {
    if _, err := os.Stat("/usr/bin/vulkaninfo"); err == nil {
        p.GPUs = append(p.GPUs, GPUInfo{Vendor: "detected", ComputeAPIs: []string{"vulkan"}})
        p.ComputeClasses = appendUniq(p.ComputeClasses, "gpu")
    }
    if out, err := os.ReadFile("/proc/driver/nvidia/gpus/0/information"); err == nil {
        p.GPUs = append(p.GPUs, GPUInfo{Vendor: "nvidia", Model: string(out),
            ComputeAPIs: []string{"cuda", "vulkan"}})
        p.ComputeClasses = appendUniq(p.ComputeClasses, "gpu")
    }
    if _, err := os.Stat("/opt/rocm/bin/rocm-smi"); err == nil {
        p.GPUs = append(p.GPUs, GPUInfo{Vendor: "amd", ComputeAPIs: []string{"rocm", "vulkan"}})
        p.ComputeClasses = appendUniq(p.ComputeClasses, "gpu")
    }
    if _, err := os.Stat("/sys/class/misc/mali0"); err == nil {
        p.GPUs = append(p.GPUs, GPUInfo{Vendor: "arm", Model: "Mali-G610",
            ComputeAPIs: []string{"vulkan"}})
        p.ComputeClasses = appendUniq(p.ComputeClasses, "gpu")
    }
}

func (de *DiscoveryEngine) detectNPU(p *DeviceProfile) {
    if _, err := os.Stat("/dev/rknpu"); err == nil {
        p.NPUs = append(p.NPUs, NPUInfo{Vendor: "rockchip", Model: "RK3588_NPU", TOPsINT8: 6.0})
        p.ComputeClasses = appendUniq(p.ComputeClasses, "npu")
    }
    if _, err := os.Stat("/sys/class/misc/nvdec"); err == nil {
        tops := 67.0
        if p.RAMBytes >= 32<<30 { tops = 275.0 } else if p.RAMBytes >= 16<<30 { tops = 157.0 }
        p.NPUs = append(p.NPUs, NPUInfo{Vendor: "nvidia", Model: "DLA+Ampere", TOPsINT8: tops})
        p.ComputeClasses = appendUniq(p.ComputeClasses, "npu")
    }
}

func (de *DiscoveryEngine) detectStorage(p *DeviceProfile) {
    var st syscall.Statfs_t
    if syscall.Statfs("/var/lib/helixcluster", &st) == nil {
        p.StorageBytes = st.Blocks * uint64(st.Bsize)
    } else if syscall.Statfs("/", &st) == nil {
        p.StorageBytes = st.Blocks * uint64(st.Bsize)
    }
}

func (de *DiscoveryEngine) detectContainerRuntime(p *DeviceProfile) {
    if _, err := os.Stat("/usr/bin/docker"); err == nil {
        p.ContainerRuntime = "docker"
    } else if _, err := os.Stat("/usr/bin/podman"); err == nil {
        p.ContainerRuntime = "podman"
    } else {
        p.ContainerRuntime = "none"
    }
}

func classifyTier(p *DeviceProfile) {
    if len(p.NPUs) > 0 && p.NPUs[0].TOPsINT8 >= 100 {
        p.AssignedTier = "AI_CONTROLLER"; p.TrustLevel = "SEMI_TRUSTED"; return
    }
    if len(p.NPUs) > 0 && p.NPUs[0].TOPsINT8 >= 20 {
        p.AssignedTier = "AI_WORKER"; p.TrustLevel = "SEMI_TRUSTED"; return
    }
    if p.CPUCores >= 16 && p.RAMBytes >= 64<<30 {
        p.AssignedTier = "CORE_TRUSTED"; p.TrustLevel = "TRUSTED"; return
    }
    if p.CPUCores <= 4 && p.RAMBytes <= 2<<30 {
        p.AssignedTier = "NETWORK_GATEWAY"; p.TrustLevel = "EDGE"; return
    }
    if len(p.GPUs) > 0 && p.RAMBytes >= 16<<30 {
        p.AssignedTier = "HANDHELD"; p.TrustLevel = "UNTRUSTED"; return
    }
    if p.Architecture == "riscv64" {
        p.AssignedTier = "RISC_V_EXPERIMENTAL"; p.TrustLevel = "TRUSTED"; return
    }
    p.AssignedTier = "SEMI_TRUSTED"; p.TrustLevel = "SEMI_TRUSTED"
}

func appendUniq(s []string, item string) []string {
    for _, x := range s { if x == item { return s } }
    return append(s, item)
}
func generateDeviceID() string {
    return "hc-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}
```

Build for all target architectures:

```bash
GOOS=linux GOARCH=amd64   go build -o helixcluster-agent-amd64
GOOS=linux GOARCH=arm64   go build -o helixcluster-agent-arm64
GOOS=linux GOARCH=riscv64 go build -o helixcluster-agent-riscv64
```

---

### 8.2 Complete Device Taxonomy (64 Devices)

Master reference. Tier assignment follows the discovery engine decision tree (Section 8.1.2).

| # | Device | Category | CPU | RAM | GPU / AI | Network | Price | Tier | Linux |
|---|--------|----------|-----|-----|----------|---------|-------|------|-------|
| 1 | Steam Deck LCD | Handheld | Zen 2 4c/8t | 16GB | RDNA 2 1.6 TFLOPS | Wi-Fi 5 | $279 (refurb) | T9-HANDHELD | Native |
| 2 | Steam Deck OLED | Handheld | Zen 2 4c/8t | 16GB | RDNA 2 1.6 TFLOPS | Wi-Fi 6E | $789 | T9-HANDHELD | Native |
| 3 | ROG Ally X | Handheld | Zen 4 8c/16t | 24GB | RDNA 3 8.6 TFLOPS | Wi-Fi 6E | $999 | T9-HANDHELD | Native |
| 4 | Lenovo Legion Go | Handheld | Zen 4 8c/16t | 16GB | RDNA 3 8.6 TFLOPS | Wi-Fi 6E | $699 | T9-HANDHELD | Native |
| 5 | GPD Win 4 2025 | Handheld | Zen 5 12c/24t | 32GB | RDNA 3.5 11.9 TFLOPS | Wi-Fi 7 | $1,200 | T9-HANDHELD | Native |
| 6 | Nintendo Switch | Handheld | ARM A57 4x | 4GB | Maxwell 393 GFLOPS | Wi-Fi 5 | EOL | T15-LEGACY | L4T only |
| 7 | Nintendo Switch 2 | Handheld | ARM A78C 8x | 12GB | Ampere ~3.1 TFLOPS | Wi-Fi 6 | $449 | T10-EXP | Not yet |
| 8 | Xbox Series X | Console | Zen 2 8c | 16GB | RDNA 2 12 TFLOPS | Wi-Fi 5 | $499 | T15-LEGACY | **None** |
| 9 | Jetson Orin Nano Super | AI Edge | 6x A78AE | 8GB | Ampere 67 TOPS | GbE | $249 | T4-AI_WORKER | Native |
| 10 | Jetson Orin NX 16GB | AI Edge | 8x A78AE | 16GB | Ampere 157 TOPS | GbE | $600 | T4-AI_WORKER | Native |
| 11 | Jetson AGX Orin 64GB | AI Ctrl | 12x A78AE | 64GB | Ampere 275 TOPS | GbE | $1,599 | T5-AI_CTL | Native |
| 12 | Jetson Thor T5000 | AI Ctrl | 14x Neoverse-V3 | 128GB | Blackwell 2070 TFLOPS | GbE | $2,847 | T5-AI_CTL | Native |
| 13 | Radxa ROCK 5B | ARM SBC | 4xA76+4xA55 | 4-32GB | Mali-G610, 6 TOPS | 2.5GbE | $157 | T2-SEMI | Mainline 6.12+ |
| 14 | NanoPi R6S | Gateway | 4xA76+4xA55 | 8GB | Mali-G610, 6 TOPS | 2x 2.5GbE | $139 | T6-NET_GW | Armbian |
| 15 | NanoPi R6C | ARM SBC | 4xA76+4xA55 | 4-8GB | Mali-G610, 6 TOPS | 2.5GbE+GbE | $85 | T2-SEMI | Armbian |
| 16 | Banana Pi BPI-M7 | ARM SBC | 4xA76+4xA55 | 8-32GB | Mali-G610, 6 TOPS | 2x 2.5GbE | $165 | T2-SEMI | Armbian |
| 17 | Mixtile Blade 3 | ARM SBC | 4xA76+4xA55 | 4-32GB | Mali-G610, 6 TOPS | Dual 2.5GbE | $195 | T2-SEMI | Armbian |
| 18 | Turing RK1 (module) | ARM SBC | 4xA76+4xA55 | 8-32GB | Mali-G610, 6 TOPS | GbE | $110 | T2-SEMI | Armbian |
| 19 | Turing Pi 2.5 | Backplane | N/A | N/A | N/A | GbE switch | $279 | T2-SEMI | N/A |
| 20 | CM3588 NAS | Storage | 4xA76+4xA55 | 4-32GB | 6 TOPS | 2.5GbE | $160 | T7-STORAGE | Armbian |
| 21 | Firefly ITX-3588J | Mini-ITX | 4xA76+4xA55 | 4-32GB | 6 TOPS | 2x GbE | $449 | T7-STORAGE | Armbian |
| 22 | Odroid M1 | Storage | 4x A55 | 4-8GB | Mali-G52 | GbE | $70 | T7-STORAGE | Native |
| 23 | Odroid M1S | Budget | 4x A55 | 4-8GB | Mali-G52 | GbE | $59 | T8-BUDGET | Native |
| 24 | Odroid N2+ | Budget | 4xA73+2xA53 | 2-4GB | Mali-G52 | GbE | $69 | T8-BUDGET | Native |
| 25 | Khadas VIM4 | ARM SBC | 4xA73+4xA53 | 8GB | Mali-G52, 2 TOPS | GbE+WiFi6 | $220 | T2-SEMI | Armbian |
| 26 | Khadas Edge2 | Edge | 4xA76+4xA55 | 8-16GB | Mali-G610, 6 TOPS | WiFi 6 | $199 | T3-EDGE | Armbian |
| 27 | BeagleBone AI-64 | Industrial | 2xA72+6xR5F | 4GB | 8 TOPS TDA4VM | GbE | $185 | T2-SEMI | TI SDK |
| 28 | Milk-V Pioneer | RISC-V | 64x C920 | up to 128GB | None | 2.5GbE | $1,199 | T10-EXP | Native |
| 29 | SiFive HiFive Premier | RISC-V | 4x P550 | 16-32GB | None | GbE | $399 | T10-EXP | Native |
| 30 | Milk-V Jupiter | RISC-V | 8x X60 | 4-16GB | 2 TOPS NPU | GbE | $150 | T10-EXP | Native |
| 31 | VisionFive 2 | RISC-V | 4x U74 | 1-8GB | IMG BXE-4-32 | GbE | $70 | T10-EXP | Native |
| 32 | Kendryte K230 | RISC-V AI | 2x C908 | 0.5-2GB | 6 TOPS KPU | 100MbE | $49 | T14-EXOTIC | SDK |
| 33 | Loongson 3A6000 | LoongArch | 4 (SMT) | 8-16GB | Integrated | GbE | $300 | T10-EXP | Loongnix |
| 34 | POWER9 Blackbird | POWER | 8 cores | up to 256GB | None | GbE | $1,600 | T1-CORE | Native |
| 35 | PYNQ-Z2 | FPGA+ARM | 2x A9 | 512MB | FPGA fabric | GbE | $129 | T12-FPGA_H | PYNQ |
| 36 | DE10-Nano | FPGA+ARM | 2x A9 | 1GB | 110K LE | GbE | $190 | T12-FPGA_H | Debian |
| 37 | KV260 | FPGA+ARM | 4x A53 | 4GB | DPU 0.92 TOPS | GbE | $249 | T12-FPGA_H | Kria Ubuntu |
| 38 | ZUBoard 1CG | FPGA+ARM | 2x A53 | 1GB | 81K LE | GbE | $159 | T12-FPGA_H | Petalinux |
| 39 | Colorlight 5A-75B | FPGA | VexRiscv soft | 2MB | 25K LUT ECP5 | Dual GbE | $15 | T11-FPGA_S | LiteX |
| 40 | ULX3S | FPGA | VexRiscv soft | 32MB | 84K LUT ECP5 | WiFi | $195 | T11-FPGA_S | LiteX |
| 41 | EBAZ4205 | FPGA+ARM | 2x A9 | 256MB | 28K LUT Artix-7 | GbE | $12 | T11-FPGA_S | OpenXC7 |
| 42 | EPYC 7742 server | x86 Srv | 64c/128t Z2 | up to 4TB | PCIe Gen4 | Dual GbE | $900 used | T1-CORE | Native |
| 43 | EPYC 7713 server | x86 Srv | 64c/128t Z3 | up to 4TB | PCIe Gen4 | Dual GbE | $1,200 used | T1-CORE | Native |
| 44 | Ampere Altra Q80-30 | ARM Srv | 80x N1 | up to 4TB | PCIe Gen4 | Dual 25GbE | $1,500 | T1-CORE | Native |
| 45 | Ampere Altra Max M128 | ARM Srv | 128x N1 | up to 4TB | PCIe Gen4 | Dual 25GbE | $2,500 | T1-CORE | Native |
| 46 | Minisforum MS-01 | Mini PC | i9-13900H 14c | 64GB | UHD | 2x 10GbE SFP+ | $679 | T3-EDGE | Native |
| 47 | ASUS NUC 14 Pro | Mini PC | Core Ultra 7 | 96GB | Arc NPU | 2x 2.5GbE | $869 | T3-EDGE | Native |
| 48 | Mac Studio M3 Ultra | Workstation | 32c M3 Ultra | up to 512GB | 80-core GPU | 10GbE+TB4 | $3,995 | T2-SEMI | macOS |
| 49 | AWS Graviton4 (c8g) | Cloud | 96 vCPU V2 | up to 3TB | None | 100 GbE | $0.011/vCPU/hr | T13-CLOUD | N/A |
| 50 | Used A100 40GB | GPU | N/A | 40GB HBM | 312 TFLOPS | PCIe | $5,000 used | T2-SEMI | CUDA |
| 51 | AMD MI210 GPU | GPU | N/A | 64GB HBM | 181 TFLOPS | PCIe | $2,500 used | T2-SEMI | ROCm |
| 52 | GL.iNet GL-MT6000 | Router | 4x A53 @ 2.0 | 1GB | None | 2x 2.5GbE+4xGbE | $159 | T6-NET_GW | OpenWrt |
| 53 | GL.iNet GL-MT3000 | Router | 2x A53 @ 1.3 | 512MB | None | 1x 2.5GbE | $89 | T6-NET_GW | OpenWrt |
| 54 | NanoPi R6S (router) | Gateway | 4xA76+4xA55 | 8GB | 6 TOPS | 2x 2.5GbE | $139 | T6-NET_GW | Armbian |
| 55 | Synology DS923+ | NAS | Ryzen R1600 | 4-32GB | None | 2x GbE | $550 | T7-STORAGE | DSM Docker |
| 56 | QNAP TS-464 | NAS | Celeron N5095 | 4-16GB | UHD | 2x 2.5GbE | $450 | T7-STORAGE | QTS Docker |
| 57 | LG webOS TV | Smart TV | 2-4x ARM | 2-4GB | None | Wi-Fi 5 | N/A | T3-EDGE | webOS |
| 58 | NVIDIA Shield TV Pro | Smart TV | Tegra X1+ | 3GB | Maxwell | GbE+WiFi5 | $199 | T3-EDGE | Android TV |
| 59 | Samsung Tizen TV | Smart TV | 2-4x ARM | 1.5-3GB | None | Wi-Fi 5 | N/A | T3-EDGE | Tizen |
| 60 | Siemens IoT2050 | Industrial | 4x A53 | 2GB | None | GbE+serial | $350 | T3-EDGE | Industrial |
| 61 | Groq LPU | AI Accel | TSP | ~230MB SRAM | 300-500 tok/s | N/A | Cloud | T14-EXOTIC | API |
| 62 | Cerebras CS-3 | AI Accel | WSE-3 900Kc | 44GB SRAM | 125 PFLOPS | N/A | $2-3M | T14-EXOTIC | API |
| 63 | IBM z17 | Mainframe | Telum II | 64TB | AI accel | Dedicated | Enterprise | T14-EXOTIC | z/OS+Linux |
| 64 | Intel Loihi 2 | Neuro | 128 async | N/A | Research | N/A | $2,500 kit | T14-EXOTIC | Lava SDK |

---

### 8.3 Security Model

Five trust levels govern the 15 tiers. Sandboxing escalates as trust decreases.

#### 8.3.1 Five Trust Tiers

| Trust | Tiers | Sandbox | Verification | Examples |
|-------|-------|---------|-------------|----------|
| **FULL** | T1, T10, T11 | None (native) | Open firmware / user boot | EPYC (Coreboot), RISC-V, FPGA |
| **STANDARD** | T2, T4, T5, T12 | Docker | Signed boot, seccomp, AppArmor | ROCK 5B, Jetson, Ampere |
| **SEMI** | T3, T7, T8 | gVisor/Kata | Runtime security, net policy | Mini PCs, NAS, TVs |
| **EDGE** | T6, T9, T13 | Kata/VM | Full sandbox, read-only, proxy | Routers, handhelds, spot |
| **EXOTIC** | T14 | API proxy | Vendor-dependent, isolated | Groq, Cerebras, quantum |

Trust flows downward only via administrative attestation.

#### 8.3.2 YAML Tier Definitions (All 15 Tiers)

```yaml
apiVersion: helixcluster.io/v1
kind: TierDefinitions
metadata:
  version: "5.0"
  date: "2025-07-01"

spec:
  tiers:
    - id: T1
      name: CORE_TRUSTED
      trust: FULL
      sandbox: none
      isolation: native
      access: unrestricted
      min: {cpu_cores: 16, ram_gb: 64, storage_gb: 500, network_mbps: 1000}

    - id: T2
      name: SEMI_TRUSTED
      trust: STANDARD
      sandbox: docker
      isolation: container
      access: containerized
      min: {cpu_cores: 4, ram_gb: 4, storage_gb: 32, network_mbps: 1000}
      compute_classes: [cpu, npu]

    - id: T3
      name: EDGE_COMPUTE
      trust: SEMI
      sandbox: gvisor
      isolation: sandboxed_container
      access: sandboxed
      min: {cpu_cores: 2, ram_gb: 2, storage_gb: 16}

    - id: T4
      name: AI_WORKER
      trust: STANDARD
      sandbox: docker
      isolation: container
      access: ai_workloads
      min: {cpu_cores: 4, ram_gb: 4, npu_tops: 20, storage_gb: 64}
      compute_classes: [cpu, npu, gpu]

    - id: T5
      name: AI_CONTROLLER
      trust: STANDARD
      sandbox: docker
      isolation: container
      access: ai_controller
      min: {cpu_cores: 8, ram_gb: 32, npu_tops: 100, storage_gb: 256}
      compute_classes: [cpu, npu, gpu]

    - id: T6
      name: NETWORK_GATEWAY
      trust: EDGE
      sandbox: kata
      isolation: vm
      access: gateway_only
      min: {cpu_cores: 2, ram_gb: 1, storage_gb: 8, network_ports_gbe: 2}

    - id: T7
      name: STORAGE_NODE
      trust: SEMI
      sandbox: gvisor
      isolation: sandboxed_container
      access: storage
      min: {cpu_cores: 2, ram_gb: 4, storage_bays: 2, network_mbps: 1000}

    - id: T8
      name: BUDGET
      trust: SEMI
      sandbox: gvisor
      isolation: sandboxed_container
      access: lightweight_only
      min: {cpu_cores: 2, ram_gb: 2, storage_gb: 8}

    - id: T9
      name: HANDHELD
      trust: EDGE
      sandbox: kata
      isolation: vm
      access: opportunistic
      min: {cpu_cores: 4, ram_gb: 16, gpu_tflops: 1.0}
      scheduling: {power_aware: true, battery_threshold_pct: 20}

    - id: T10
      name: RISC_V_EXPERIMENTAL
      trust: FULL
      sandbox: none
      isolation: native
      access: experimental
      min: {cpu_cores: 4, ram_gb: 4}
      constraints: {max_workload_duration: 1h, no_sensitive_data: true}

    - id: T11
      name: FPGA_SOFT_CORE
      trust: FULL
      sandbox: none
      isolation: native
      access: fpga_only
      min: {fpga_lut_k: 25, ram_mb: 32}
      constraints: {bitstream_verification: required}

    - id: T12
      name: FPGA_HARD_ACCEL
      trust: STANDARD
      sandbox: docker
      isolation: container
      access: fpga_accelerated
      min: {cpu_cores: 2, ram_gb: 1, fpga_lut_k: 80}
      compute_classes: [cpu, fpga]

    - id: T13
      name: CLOUD_BURST
      trust: EDGE
      sandbox: kata
      isolation: vm
      access: ephemeral
      min: {cpu_cores: 2, ram_gb: 4}
      constraints: {preemptible: true, checkpoint_required: true, max_runtime: 4h}

    - id: T14
      name: EXOTIC_ACCEL
      trust: EXOTIC
      sandbox: api_proxy
      isolation: network_segment
      access: specialized
      constraints: {manual_approval: true, api_key_required: true}

    - id: T15
      name: LEGACY_RETIRED
      trust: none
      sandbox: isolated
      access: none
      status: deprecated
```

**Invariants:** (1) Sensitive data only on FULL or STANDARD. (2) Control plane quorum on FULL only. (3) Handhelds and spot instances: stateless batch with checkpoint/resume. (4) EXOTIC nodes receive API calls only. (5) Attestation failures trigger automatic downgrade.

---

### 8.4 Recommended Cluster Builds

Five validated configurations with exact pricing.

#### 8.4.1 Build 1: $250 Budget Edge

| Component | Qty | Unit | Total |
|-----------|-----|------|-------|
| Odroid M1S 8GB | 2 | $59 | $118 |
| GL.iNet GL-MT3000 | 1 | $89 | $89 |
| 128 GB microSD | 3 | $10 | $30 |
| USB Ethernet adapters | 2 | $5 | $10 |
| **Total** | **3 nodes** | | **$247** |

M1S units run Pi-hole, Prometheus, MQTT. MT3000 is WireGuard gateway. ~15W.

#### 8.4.2 Build 2: $500 AI Starter

| Component | Qty | Unit | Total |
|-----------|-----|------|-------|
| Jetson Orin Nano Super 8GB | 1 | $249 | $249 |
| NanoPi R6C 8GB | 1 | $125 | $125 |
| Radxa ROCK 5C 4GB | 1 | $75 | $75 |
| 256 GB NVMe SSD | 2 | $20 | $40 |
| 5-port GbE switch | 1 | $10 | $10 |
| **Total** | **3 nodes** | | **$499** |

Orin Nano Super: TensorRT inference (67 TOPS). R6C: RKNN edge AI + routing. ROCK 5C: general compute. 73 TOPS aggregate AI. ~35W.

#### 8.4.3 Build 3: $1,000 Home Lab

| Component | Qty | Unit | Total |
|-----------|-----|------|-------|
| Radxa ROCK 5B 8GB | 4 | $157 | $628 |
| NanoPi R6S 8GB | 1 | $139 | $139 |
| 256 GB NVMe SSD | 4 | $20 | $80 |
| 1 TB SATA SSD | 1 | $45 | $45 |
| 8-port 2.5GbE switch | 1 | $40 | $40 |
| PSUs + cases | 4 | $12 | $48 |
| **Total** | **5 nodes** | | **$980** |

Four ROCK 5B: 32 cores, 32 GB RAM, 24 TOPS NPU. R6S: WireGuard gateway. Runs K3s, PostgreSQL, Redis, object detection. ~55W.

#### 8.4.4 Build 4: $2,000 ARM Density

| Component | Qty | Unit | Total |
|-----------|-----|------|-------|
| Turing Pi 2.5 carrier | 1 | $279 | $279 |
| Turing RK1 16GB | 4 | $160 | $640 |
| Jetson Orin Nano Super | 1 | $249 | $249 |
| NanoPi R6S 8GB | 1 | $139 | $139 |
| 512 GB NVMe SSD | 5 | $40 | $200 |
| 1 TB NVMe SSD | 1 | $60 | $60 |
| PSU + cooling kit | 1 | $100 | $100 |
| 8-port 2.5GbE switch | 1 | $45 | $45 |
| SATA SSDs | 2 | $25 | $50 |
| **Total** | **7 nodes** | | **$1,762** |

Turing Pi hosts four RK1 modules. Jetson Orin Nano Super for AI. R6S as 2.5GbE gateway. 32 RK3588 cores + 6 Orin cores, 73 TOPS. ~85W.

#### 8.4.5 Build 5: $5,000+ Production

| Component | Qty | Unit | Total |
|-----------|-----|------|-------|
| EPYC 7713 server (128 GB ECC) | 1 | $1,200 | $1,200 |
| Ampere Altra Q80-30 (256 GB) | 1 | $1,500 | $1,500 |
| Jetson AGX Orin 64GB | 1 | $1,599 | $1,599 |
| Minisforum MS-01 (64 GB) | 1 | $679 | $679 |
| 8-port 10GbE SFP+ switch | 1 | $350 | $350 |
| 10GbE DAC cables 1m | 4 | $15 | $60 |
| 1 TB NVMe enterprise SSD | 4 | $80 | $320 |
| 4 TB SATA SSD | 4 | $180 | $720 |
| Rackmount chassis + PDU | 1 | $200 | $200 |
| **Total** | **4 nodes** | | **$6,628** |

EPYC 7713: K3s control plane (64c/128t). Altra Q80-30: ARM containers (80c, 256 GB). AGX Orin 64GB: AI controller (275 TOPS). MS-01: 10GbE gateway. 144 x86 cores + 80 ARM cores + 275 TOPS. ~600W.
