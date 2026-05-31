# HelixCluster Phase 3 — Edge & Mobile Device Research Cross-Verification

## HIGH CONFIDENCE

### HC-1: Orange Pi 5 Max is the Best SBC for HelixCluster
- Confirmed by: dim01, dim03
- RK3588 (4xA76+4xA55), 16GB LPDDR5, Mali-G610 (255 GFLOPS), 6 TOPS NPU
- 2.5GbE + WiFi 6E, PCIe 3.0 x4 NVMe2 @ 5,700 MB/s
- $125 for 16GB model — 4-5x better price/performance than Raspberry Pi 5
- Runs Armbian Ubuntu natively, Docker fully supported, Go/Zig/C++ compile trivially

### HC-2: Android Devices Are Viable Compute Nodes via Termux + Foreground Services
- Confirmed by: dim02, dim07
- Termux provides full Linux environment without root
- Foreground services (with notification) enable persistent compute on Android 12+
- Vulkan compute shaders work on Adreno 6xx/7xx and Mali-G610
- ADB over WiFi enables remote management
- DreamLab proved 100K phones can match 30x supercomputer speed
- Only-compute-while-charging model proven by DreamLab

### HC-3: Android TV Boxes (RK3588) Offer Best Price/Performance for Headless Compute
- Confirmed by: dim03, dim01
- H96 MAX V58: RK3588, 8GB RAM, 2.5GbE, 6 TOPS NPU for ~$130
- Armbian Linux replaces Android — becomes standard Linux server
- 3-7W power consumption, 24/7 capable
- No GPIO but USB 3.0 + Ethernet sufficient for cluster node

### HC-4: iOS Devices Are Compute-Capable but Background-Restricted
- Confirmed by: dim04
- A18 Pro: 2.29 TFLOPS GPU, 35 TOPS NPU, 8GB RAM — extremely powerful
- But: background execution limited to ~3 minutes, no persistent processes
- Native app (TestFlight) + Metal compute + CoreML is the only viable path
- Suitable for periodic batch jobs, NOT persistent cluster nodes
- Modern iPhones (A12+) cannot be jailbroken

### HC-5: HarmonyOS Super Device Aligns with HelixCluster's Distributed Vision
- Confirmed by: dim05
- Device Virtualization enables task distribution across HarmonyOS devices
- Da Vinci NPU (via CANN framework) strong for AI inference
- But Kirin 9000S ~2x slower than Snapdragon 8 Gen 3
- Best for Chinese market deployments and NPU inference workloads

### HC-6: QUIC Protocol is Optimal for Mobile/Edge Networking
- Confirmed by: dim06
- 0-RTT connection establishment, connection migration on WiFi/cellular handoff
- mQUIC showed significant gains on commercial 5G
- Better than TCP for unreliable mobile networks

### HC-7: llama.cpp with ARM Optimizations Runs Well on Mobile
- Confirmed by: dim06, dim07
- NEON, i8mm, dotprod kernels for ARM
- Q4_0_4_4 (NEON) and Q4_0_4_8 (i8mm) quantizations
- 29-30 tok/s on Cortex-X3 for small models
- MLC LLM provides universal deployment (iPhone, Android, WebGPU)

### HC-8: Power-Aware Scheduling is Essential for Battery Devices
- Confirmed by: dim06, dim02, dim07
- DreamLab model: only compute overnight while charging
- BatteryManager API on Android for charge detection
- DVFS-based scheduling for power management
- Flower federated learning framework for processor-specific cutoffs

## CONFLICT ZONES

### CZ-1: Real Linux vs Termux on Android
- dim07: PostmarketOS/Droidian offer real Linux but limited device support (~723 devices)
- dim02: Termux + proot-distro works on ALL Android devices without root
- RESOLUTION: Tiered approach — Armbian/PostmarketOS for SBCs and supported phones; Termux foreground service for all other Android devices

### CZ-2: iOS Native App vs Terminal Apps
- dim04: Native iOS app (Xcode/Swift) gives GPU/NPU access but limited background
- dim04: a-Shell/iSH give Linux environment but NO GPU/NPU access
- RESOLUTION: Native iOS app as PRIMARY (Metal compute + CoreML), a-Shell as fallback for simple shell tasks

### CZ-3: MQTT vs QUIC for Edge Messaging
- dim06: MQTT is proven, lightweight, 2-byte header, excellent for IoT
- dim06: QUIC is better for mobile (connection migration, 0-RTT)
- RESOLUTION: MQTT for SBC/Android TV (stable network); QUIC for mobile phones (unstable network); both supported
