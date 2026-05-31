# Chapter 4: Phase 2 Implementation Plan & Console Setup

## 4.1 Phase 2 Task Breakdown

HelixCluster Phase 2 adds **24 new tasks** (~176 hours, ~4.5 weeks) to the existing Phase 0-8 plan. These tasks are distributed across all phases, with the heaviest concentration in Phase 0 (foundations) and Phase 1 (infrastructure).

### Console-Specific Task Matrix

| Phase | Task ID | Description | Hours | Priority | Skill |
|-------|---------|-------------|-------|----------|-------|
| **0** | C-0.1 | Console Agent Go project scaffolding | 8h | P0 | GO |
| **0** | C-0.2 | ConsoleAdapter interface definition | 4h | P0 | GO |
| **0** | C-0.3 | Thermal/power monitoring via sysfs/hwmon | 8h | P0 | GO |
| **0** | C-0.4 | Jailbreak detection library | 8h | P0 | GO |
| **0** | C-0.5 | Auto-exploit ESP32 firmware (C++) | 16h | P1 | C |
| **0** | C-0.6 | Console capability scanner | 4h | P0 | GO |
| **0** | C-0.7 | PS5 Orbis I/O Agent (native) | 16h | P2 | C |
| **1** | C-1.1 | Console node registration (SEMI trust) | 4h | P0 | GO |
| **1** | C-1.2 | Console heartbeat with thermal metrics | 4h | P0 | GO |
| **1** | C-1.3 | WireGuard kernel module for PS4/PS5 Linux | 4h | P0 | GO |
| **1** | C-1.4 | ZeroMQ lightweight client for PS4 | 4h | P0 | GO |
| **1** | C-1.5 | gRPC client for PS5 | 4h | P0 | GO |
| **2** | C-2.1 | Vulkan Compute Backend validation on PS4/PS5 | 8h | P0 | C |
| **2** | C-2.2 | llama.cpp Vulkan integration for consoles | 8h | P0 | C |
| **2** | C-2.3 | Console-specific ClassAds expressions | 4h | P0 | GO |
| **2** | C-2.4 | ConsoleAware scheduler plugin | 8h | P0 | GO |
| **3** | C-3.1 | Minimal PTY session backend for consoles | 8h | P1 | GO |
| **4** | C-4.1 | AOSP distcc worker on PS4 | 8h | P1 | GO |
| **4** | C-4.2 | AOSP distcc + GPU worker on PS5 | 8h | P1 | GO |
| **5** | C-5.1 | AI inference agent (llama.cpp server) | 8h | P1 | GO |
| **7** | C-7.1 | Console chaos tests (power loss, thermal) | 8h | P0 | QA |
| **7** | C-7.2 | SEMI trust model verification testing | 8h | P0 | QA |
| **8** | C-8.1 | Console setup wizard (htmux add-console) | 8h | P0 | GO |
| **8** | C-8.2 | Auto-exploit hardware provisioning | 8h | P1 | GO |

### Critical Path for Phase 2

```
C-0.1 (scaffold) → C-0.2 (adapter) → C-0.3 (thermal) → C-0.4 (jailbreak)
     │                                                   │
     ▼                                                   ▼
C-0.6 (scanner)                                  C-1.1 (registration)
     │                                                   │
     ▼                                                   ▼
C-1.2 (heartbeat) → C-1.3 (WireGuard) → C-1.4/C-1.5 (messaging)
     │                                                   │
     ▼                                                   ▼
C-2.3 (ClassAds) → C-2.4 (scheduler plugin) ← C-2.1 (Vulkan test)
     │
     ▼
C-2.2 (llama.cpp) → C-5.1 (AI inference)
     │
     ▼
C-7.1 (chaos) → C-7.2 (verification) → C-8.1 (setup wizard) → C-8.2 (auto-exploit)
```

### Integration Points with Existing Components

| Existing Component | Console Integration | Effort |
|-------------------|-------------------|--------|
| Node Discovery | Add CONSOLE node type, SEMI trust level | Low |
| Resource Scheduler | ConsoleAware plugin (thermal, AVX2, RAM filters) | Medium |
| GPU Compute Engine | **No changes** — Vulkan backend is universal | None |
| Health Monitor | Add console-specific metrics (thermal, power, SSD) | Medium |
| LLM Brain | Console AI inference pool for parallel agents | Low |
| Build Service | Console distcc workers for AOSP | Low |
| Security Manager | SEMI trust model, encrypted work units | Medium |
| Session Manager | Minimal PTY for console nodes (no migration) | Low |

## 4.2 Console Setup Wizard

The console setup wizard is invoked via `htmux cluster add-console` and automates the entire provisioning process.

### Phase 1: Discovery

```
$ htmux cluster add-console --discover

Scanning local network for PlayStation consoles...

[DISCOVERED CONSOLES]
┌────┬──────────┬─────────────┬──────────────────┬────────────┬────────┐
│ ## │ Model    │ IP Address  │ MAC Address      │ Firmware   │ Status │
├────┼──────────┼─────────────┼──────────────────┼────────────┼────────┤
│ 01 │ PS4 Pro  │ 192.168.1.45│ A4:17:31:XX:XX:XX│ 9.00      │ JB ✓   │
│ 02 │ PS4 Fat  │ 192.168.1.47│ A4:17:31:XX:XX:XX│ 11.00     │ No JB  │
│ 03 │ PS5      │ 192.168.1.50│ 88:C9:E8:XX:XX:XX│ 4.51      │ JB ✓   │
└────┴──────────┴─────────────┴──────────────────┴────────────┴────────┘

Note: PS4 at 192.168.1.47 (firmware 11.00) cannot be jailbroken.
       Only firmwares ≤9.00 (PS4) or ≤4.51 (PS5) are exploitable.

Select consoles to add (comma-separated): 1,3
```

### Phase 2: Jailbreak

```
[PS4 Pro at 192.168.1.45]
Firmware: 9.00 ✓ (GoldHen-compatible)
Preparing jailbreak payload...

  [████████████████████] 100%
  GoldHen v2.4b loaded successfully
  Debug settings enabled
  FTP server active on port 2121
  BinLoader active on port 9090

[PS5 at 192.168.1.50]
Firmware: 4.51 ✓ (etaHEN-compatible)
Preparing jailbreak payload...

  [████████████████████] 100%
  etaHEN loaded successfully
  Homebrew enabled
  FTP server active on port 2121
```

### Phase 3: Linux Installation

```
[Installing Linux on PS4 Pro]
Downloading psxitarch v3 (kernel 6.15.4)...
  [████████████████████] 100% 1.2 GB downloaded

Preparing USB drive /dev/sdb...
Writing Linux payload...
  [████████████████████] 100%

Booting Linux via kexec...
  [████████████████████] 100%
  Linux 6.15.4-ps4 booted successfully
  8 CPU cores detected
  AMDGPU loaded (36 CUs)
  6.85 GB RAM available
  Gigabit Ethernet: UP (1000 Mbps)

[Installing Linux on PS5]
Downloading Ubuntu 24.04 for PS5...
  [████████████████████] 100% 2.1 GB downloaded

Writing to USB drive...
Booting via ps5-linux-loader...
  [████████████████████] 100%
  Ubuntu 24.04 booted successfully
  16 CPU threads detected (Zen 2)
  AMDGPU loaded (RDNA2, 36 CUs)
  12.5 GB RAM available
  Gigabit Ethernet: UP (1000 Mbps)
  M.2 SSD: detected
```

### Phase 4: Agent Installation

```
[Installing HelixCluster Console Agent]

Downloading console-agent-linux-amd64...
  [████████████████████] 100%

Installing systemd service...
  Creating user: helix (no sudo)
  Installing binary: /opt/helix/bin/console-agent
  Creating service: /etc/systemd/system/helix-console.service
  Enabling auto-start: ✓

Configuring agent...
  Control plane: auto-discovered at 192.168.1.10:8443
  WireGuard mesh: generating keys...
  Node labels: tier=2,model=ps4-pro

Starting agent...
  [████████████████████] 100%
  Agent running, PID 1847
```

### Phase 5: Cluster Registration

```
[Registering with HelixCluster]

PS4 Pro (192.168.1.45):
  Node ID: c4f8e2d1-7a3b-4c5d-9e0f-1a2b3c4d5e6f
  Trust Level: SEMI
  Tier: 2 (Standard)
  WireGuard IP: 100.64.2.15
  ┌─────────────────────────────────────────┐
  │  CAPABILITIES                           │
  │  ✓ gpu-vulkan-compute (GCN 4.0, 4.2TF) │
  │  ✓ ai-inference-llama (55 tok/s 3B)    │
  │  ✓ batch-processing (8x Jaguar)        │
  │  ✓ video-transcode (GPU shader)        │
  └─────────────────────────────────────────┘

PS5 (192.168.1.50):
  Node ID: d5e9f3g2-8b4c-5d6e-0a1b-2c3d4e5f6a7b
  Trust Level: SEMI
  Tier: 1 (Premium)
  WireGuard IP: 100.64.2.16
  ┌─────────────────────────────────────────┐
  │  CAPABILITIES                           │
  │  ✓ gpu-vulkan-compute (RDNA2, 10.3TF)  │
  │  ✓ ai-inference-llama (104 tok/s 3B)   │
  │  ✓ batch-processing (Zen2 8c/16t)      │
  │  ✓ video-transcode (GPU shader)        │
  │  ✓ hardware-decompress (Kraken, Orbis) │
  └─────────────────────────────────────────┘

═══════════════════════════════════════════════════════════════
  ✓ 2 console nodes successfully added to HelixCluster!
═══════════════════════════════════════════════════════════════

Cluster GPU TFLOPS: +14.5 (4.2 from PS4 Pro, 10.3 from PS5)
Cluster CPU cores:   +24 (8 from PS4 Pro, 16 from PS5)
Cluster RAM:         +20.35 GB

View status: htmux cluster status
```

## 4.3 Auto-Exploit Hardware Kit

### Bill of Materials

| Component | Model | Cost | Purpose |
|-----------|-------|------|---------|
| ESP32-S2 DevKit | NodeMCU-32-S2 | $4.50 | Auto-exploit MCU |
| USB-A Cable | 1ft right-angle | $2.00 | Connect to console |
| 3D Printed Case | Custom design | $1.00 | Enclosure |
| JST Connector | 2-pin | $0.50 | Power sense wire |
| **Total per kit** | | **~$8** | |

### Assembly

```
ESP32-S2 Wiring:
┌────────────────────────────────────┐
│         ESP32-S2 NodeMCU           │
│                                    │
│  GPIO 4 ──────► Power sense (opt) │
│  GPIO 19/20 ──► USB D-/D+         │
│  5V/GND ──────► USB power         │
│                                    │
│  USB-C (power/programming)         │
└────────────────────────────────────┘
        │
        ▼
┌────────────────────────────────────┐
│     PS4/PS5 Front USB Port         │
│                                    │
│  [USB-A] ←────── ESP32-S2         │
│  [USB-A] ←────── Other devices    │
│  [USB-C]                          │
└────────────────────────────────────┘
```

### Firmware Flashing

```bash
# Via htmux CLI
$ htmux cluster provision-auto-exploit --device /dev/ttyUSB0 --console ps4-pro-001

Flashing auto-exploit firmware to ESP32...
  Chip: ESP32-S2 (revision 0)
  Flash size: 4MB
  [████████████████████] 100%

Configuring:
  Target firmware: 9.00 (from console registration)
  Exploit type: GoldHen (USB method)
  Auto-trigger: ON (power sense)
  LED indicator: ON

Testing:
  Simulating console boot...
  Exploit payload sent ✓
  Expected GoldHen load: ~8 seconds
  
✓ Auto-exploit hardware provisioned for ps4-pro-001
```

## 4.4 Community Console Donation Model

A unique capability enabled by the semi-trusted model: **community members can donate idle console time**.

```
┌─────────────────────────────────────────────────────────────────┐
│              COMMUNITY CONSOLE DONATION                          │
│                                                                  │
│  [Community Member]                    [HelixCluster]           │
│  ┌──────────────────┐                  ┌──────────────────┐     │
│  │ "I have a PS4    │                  │ "Accepting       │     │
│  │  that's idle     │ ── Register ───► │  console nodes   │     │
│  │  22 hours/day"   │                  │  for AI inference │    │
│  └──────────────────┘                  └──────────────────┘     │
│         │                                      │                │
│         │  htmux cluster donate-console       │                │
│         │  --hours 22:00-06:00                │                │
│         │  --workload-types ai-inference      │                │
│         │                                      │                │
│         ▼                                      ▼                │
│  ┌──────────────────────────────────────────────────────┐      │
│  │  TIME-SHARED CONSOLE NODE                            │      │
│  │                                                      │      │
│  │  06:00 - 22:00 │ Gaming mode (console owner uses it) │      │
│  │  22:00 - 06:00 │ Cluster mode (AI inference, batch)  │      │
│  │                                                      │      │
│  │  Work units are:                                     │      │
│  │  • Encrypted (console never sees data)               │      │
│  │  • Verified (results checked by trusted nodes)       │      │
│  │  • Interruptible (gaming takes priority)             │      │
│  │  • Compensated (owner receives compute credits)      │      │
│  └──────────────────────────────────────────────────────┘      │
└─────────────────────────────────────────────────────────────────┘
```

## 4.5 Performance Validation Targets

### Acceptance Criteria for Phase 2

| Test | Target | Measurement |
|------|--------|-------------|
| PS4 Pro Vulkan compute | ≥3.5 TFLOPS sustained | clpeak benchmark |
| PS5 Vulkan compute | ≥8.5 TFLOPS sustained | clpeak benchmark |
| llama.cpp PS4 Pro (3B) | ≥50 tok/s | llama-bench |
| llama.cpp PS5 (3B) | ≥100 tok/s | llama-bench |
| AOSP build contribution | ≥8 parallel jobs | distcc monitor |
| Network throughput | ≥850 Mbps | iperf3 |
| Thermal stability (24h) | <80°C CPU sustained | stress-ng + monitoring |
| Jailbreak persistence | 30+ days in REST mode | Longevity test |
| Auto-exploit reliability | 99%+ success rate | 100 boot cycles |
| Console agent memory | <128 MB RAM | ps/aux measurement |
| Workload verification | 100% accuracy | Redundant compute check |
| Cluster integration | Same APIs as PC nodes | End-to-end test |
