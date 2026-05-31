# HelixCluster Phase 3 — Edge & Mobile Device Insights

## Insight 1: The "DreamLab Overnight" Model Proves Mass Phone Compute is Viable

**Insight**: Vodafone's DreamLab demonstrated that 100,000 smartphones running overnight calculations matched the speed of 30 supercomputers for cancer research. This proves the concept of phone-based distributed computing at scale. The key was the "overnight while charging" model — phones only compute when plugged in and idle.

**Derived From**: dim07 (DreamLab 300K users, 100M+ calculations), dim02 (charging detection APIs)
**Rationale**: Battery is the #1 constraint for mobile compute. By gating all compute to charging state + nighttime hours, phones become viable compute nodes without impacting user experience. Android's BatteryManager.ACTION_CHARGING + isCharging() provide this detection.

**Implications**: HelixCluster Phase 3 must implement a "charging-gated" scheduling policy. Mobile devices only receive work when charging + WiFi connected + nighttime (configurable). This is the core innovation that makes phone integration practical.

**Confidence**: HIGH

---

## Insight 2: Orange Pi 5 Max + RK3588 TV Box = "Poor Man's PS5" Compute Node

**Insight**: The Orange Pi 5 Max ($125, 16GB RAM, RK3588, 6 TOPS NPU, 2.5GbE) and RK3588-based TV boxes ($130, 8GB RAM, 2.5GbE) deliver compute capabilities comparable to a low-end desktop PC at 1/5th the cost. The Mali-G610 GPU (255 GFLOPS) + 6 TOPS NPU effectively make these "budget AI accelerators."

**Derived From**: dim01 (Orange Pi 5 Max specs/benchmarks), dim03 (RK3588 TV boxes)
**Rationale**: The RK3588's 6 TOPS NPU can run TinyLlama 1.1B at 20 tok/s — useful for edge inference. The 2.5GbE networking matches desktop speeds. 16GB LPDDR5 at 6400 MT/s exceeds most DDR4 desktops. The price point ($125) makes building 10-node clusters affordable ($1,250 vs $3,000+ for PC equivalents).

**Implications**: SBCs become the PRIMARY edge compute tier in HelixCluster. Deployed as permanent, always-on nodes alongside PC/workstation nodes. Orange Pi 5 Max is the reference platform for Phase 3.

**Confidence**: HIGH

---

## Insight 3: Termux Foreground Service = Universal Android Compute Agent

**Insight**: Termux combined with Android foreground services creates a universal compute agent that works on ANY Android device (phone, tablet, TV box) without root access. The foreground service (with persistent notification) bypasses Android 12+ background execution limits. This is a game-changer — it means we can support the entire Android ecosystem (3+ billion devices) with a single approach.

**Derived From**: dim02 (Termux capabilities, foreground services), dim03 (Android TV Termux)
**Rationale**: Before Android 12, background services were killed aggressively. Foreground services (with a visible notification) are exempt from Doze mode and background restrictions. Termux already has the full Linux stack (sshd, Python, Node, C/C++, Go) and can run our Go agent natively. Combined with Vulkan compute for GPU workloads, this covers the vast majority of Android compute scenarios.

**Implications**: A single "HelixCluster Agent" APK that wraps a Termux environment + foreground service + Vulkan compute backend. This one APK works on phones, tablets, and TV boxes. No root required for basic functionality.

**Confidence**: HIGH

---

## Insight 4: iOS is a "Compute Donor" Not a "Compute Node"

**Insight**: iOS devices cannot be persistent cluster nodes due to Apple's 3-minute background execution limit. However, they excel as "compute donors" — devices that contribute bursts of compute when the app is foregrounded or during scheduled background refresh. The A18 Pro's 35 TOPS NPU and 2.29 TFLOPS GPU make them valuable for short, intense compute tasks.

**Derived From**: dim04 (iOS background limits, A18 Pro benchmarks)
**Rationale**: The distinction between "node" (persistent, always-available) and "donor" (episodic, high-contribution when available) is critical. iOS devices fall into the donor category. A CoreML inference task that runs in 30 seconds on an iPhone 16 Pro's NPU is valuable even if the device is only available intermittently.

**Implications**: iOS devices get a special EDGE_DONOR trust level. They pull work from a queue (not pushed), run it during foreground/background refresh windows, and push results back. The scheduler treats them as opportunistic capacity — valuable but not reliable.

**Confidence**: HIGH

---

## Insight 5: The "Folding@Home Pattern" is the Universal Model for Consumer Device Compute

**Insight**: Folding@home, DreamLab, BOINC, and Samsung Global Goals all use the same pattern: (1) Install lightweight app/agent, (2) Detect device idle/charging state, (3) Download encrypted work unit, (4) Compute in background, (5) Upload results, (6) Earn credits/rewards. This pattern is proven across billions of device-hours and should be the foundation of HelixCluster's mobile integration.

**Derived From**: dim07 (BOINC, DreamLab, Folding@home), dim02 (Android background execution)
**Rationale**: Why reinvent the wheel? These projects solved the hard problems: power management, background execution, user consent, result verification, and incentivization. HelixCluster adapts this pattern with modern protocols (QUIC, MQTT), better security (SEMI trust model), and more diverse workloads (not just scientific computing).

**Implications**: The mobile agent design follows the Folding@Home pattern exactly. Work units are small, self-contained, and verifiable. Results are cross-checked. Users opt in per-device. The system is transparent about resource usage.

**Confidence**: HIGH

---

## Insight 6: Armbian Linux on TV Boxes Eliminates Android Limitations

**Insight**: The most powerful Android TV boxes (RK3588-based) can run Armbian Linux natively from SD card or internal storage. This completely eliminates Android's background execution restrictions, turning a $130 TV box into a full Linux server with Docker, our full Go stack, and no compromises.

**Derived From**: dim03 (Armbian on TV boxes), dim01 (RK3588 Linux support)
**Rationale**: Rather than fighting Android's restrictions, simply replace Android with Linux on TV boxes. The hardware is excellent (RK3588, 8GB RAM, 2.5GbE, NVMe) but Android is the bottleneck. Armbian provides a first-class Linux experience with mainline kernel support. This is the RECOMMENDED approach for all TV box deployments.

**Implications**: TV boxes are provisioned with Armbian Linux, not Android. They become standard Linux nodes in the cluster (same trust level as SBCs). The "Android TV box" category effectively becomes an "ARM64 Linux server" category.

**Confidence**: HIGH

---

## Insight 7: MLC LLM + ONNX Runtime = Universal AI Inference Across ALL Device Tiers

**Insight**: The combination of MLC LLM (universal LLM engine for iPhone, Android, WebGPU) and ONNX Runtime Mobile (cross-platform with NNAPI/CoreML backends) provides a unified AI inference stack that works across every device category in Phase 3 — from SBCs to phones to tablets to TV boxes.

**Derived From**: dim06 (MLC LLM, ONNX Runtime), dim04 (CoreML), dim02 (NNAPI)
**Rationale**: AI inference is the primary workload for edge/mobile devices (bigger models run on PC/console nodes). Having a single inference framework that adapts to each device's acceleration (NPU for Snapdragon, ANE for Apple, NPU for RK3588, GPU Vulkan for all) simplifies development dramatically.

**Implications**: The AI Inference Engine is a single codebase that selects the best backend per device: CoreML (iOS), NNAPI (Android Snapdragon), Vulkan Compute (all GPUs), RKNN (RK3588 NPU), CPU fallback (all). Model quantization happens per-device-tier automatically.

**Confidence**: HIGH
