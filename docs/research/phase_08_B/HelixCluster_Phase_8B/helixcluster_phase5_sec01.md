# 1. Gaming & Handheld Computing Devices

The gaming handheld market represents one of the most compelling untapped reservoirs of distributed compute capacity for HelixCluster. With over four million Steam Deck units shipped, 155 million Nintendo Switches sold, and the PC handheld segment forecasted to reach 4.7 million annual units by 2029, these devices collectively represent a latent compute pool that rivals many commercial cloud regions. What makes this category uniquely valuable is not merely the aggregate FLOPS, but the intersection of capable hardware, functional Linux support, and usage patterns that leave devices idle for significant portions of each day. A Steam Deck purchased for $279 (refurbished) delivers 1.6 TFLOPS of RDNA 2 GPU compute and 448 GFLOPS of Zen 2 CPU performance in a 4–15 watt envelope, while its owner sleeps, works, or engages in other activities. This chapter examines every major gaming and handheld computing platform for HelixCluster viability, establishes a clear prioritization framework, and presents the integration architecture for what we designate the "Volunteer GPU Tier."

## 1.1 Steam Deck & Steam Deck OLED

### 1.1.1 Hardware: AMD Custom APU Architecture

The Steam Deck and its OLED successor share a common architectural foundation built around AMD custom APUs. The original LCD model uses the 7nm "Aerith" silicon, while the OLED refresh moves to the more efficient 6nm "Sephiroth" die. Both implement a quad-core, eight-thread Zen 2 CPU complex running at 2.4–3.5 GHz alongside eight RDNA 2 compute units operating at up to 1.6 GHz. This configuration yields approximately 448 GFLOPS FP32 from the CPU and 1.6 TFLOPS FP32 from the GPU, placing the Steam Deck's graphics capability in the same neighborhood as a desktop Radeon RX 550 or GTX 1050 Ti. The sixteen gigabytes of LPDDR5 memory (upgraded from 5500 MT/s on the LCD to 6400 MT/s on the OLED) operates as a unified memory architecture shared between CPU and GPU, a critical advantage for machine learning inference where model size frequently exceeds discrete GPU VRAM.

The thermal design power spans 4W to 15W, adjustable through SteamOS settings or direct power play table manipulation. At 4W, the device achieves extraordinary efficiency suitable for background CPU tasks; at 15W docked mode, it sustains full GPU clocks for compute-intensive workloads. Storage options range from 64GB eMMC on the base LCD model to 1TB NVMe on the premium OLED variant, with all models supporting microSD expansion. The OLED model additionally upgrades networking from Wi-Fi 5 to tri-band Wi-Fi 6E, a meaningful improvement for distributed workloads that depend on network throughput for task distribution and result upload.

### 1.1.2 SteamOS 3.0: Native Linux as First-Class Citizen

The Steam Deck's defining characteristic for HelixCluster purposes is not its silicon but its operating system. SteamOS 3.0 and later versions are derived from Arch Linux, running a modern kernel with Mesa RADV drivers providing first-class Vulkan 1.3+ support, OpenCL 3.0 via Mesa Rusticl, and community-validated ROCm compatibility through environment variable overrides. The desktop mode, accessible by switching from the Steam UI to a full KDE Plasma environment, exposes the complete Linux userspace: systemd, pacman, Docker, Flatpak, and all standard tooling expected on a contemporary Linux workstation.

This is not a hack, a jailbreak, or a vendor-tolerated modification. It is a supported, documented, and actively maintained feature of the platform. Valve employs Linux kernel developers who upstream driver improvements, contributes to Mesa and AMD GPU driver development, and has publicly committed to the openness of the Steam Deck ecosystem. For HelixCluster, this means zero engineering effort is required to establish a functional Linux environment. The agent installs through standard package management, container images pull from standard registries, and GPU compute workloads execute through standard APIs without vendor-specific SDKs or proprietary driver stacks.

### 1.1.3 GPU Compute via ROCm, Vulkan Compute, and OpenCL

The Steam Deck's GPU compute stack operates at three levels of capability and reliability. Vulkan compute shaders, exposed through the Mesa RADV driver, represent the primary and most stable path. This is the backend used by llama.cpp's Vulkan implementation, which achieves an estimated 8–12 tokens per second on 7B parameter models at Q4_0 quantization and 60–80 tokens per second in prompt processing. Vulkan requires no driver modifications, no environment workarounds, and no vendor-specific tooling. It works out of the box on every Steam Deck ever manufactured.

OpenCL 3.0 support arrives through Mesa Rusticl, an open-source OpenCL implementation that has been steadily improving its AMD GPU coverage. While functional for many compute workloads, Rusticl remains less mature than RADV's Vulkan path and may exhibit compatibility gaps with legacy OpenCL code written against proprietary drivers.

ROCm and HIP represent the third path, offering the broadest framework compatibility at the cost of unofficial status. The Steam Deck's RDNA 2 iGPU is not on AMD's officially supported hardware list for ROCm, but community workarounds using `HSA_OVERRIDE_GFX_VERSION=10.3.0` successfully trick the ROCm runtime into treating the integrated GPU as a discrete RDNA 2 card. This enables PyTorch HIP backend execution, rocBLAS matrix operations, and other ROCm-dependent frameworks. The limitation is stability: some ROCm versions crash during iGPU memory allocation, and the workaround may break with ROCm updates. For production HelixCluster workloads, Vulkan compute remains the recommended API; ROCm is offered as an optional secondary path with documented caveats.

### 1.1.4 Market Position: Highest-Impact Handheld for HelixCluster

With over four million units in circulation and a refurbished LCD entry price of $279, the Steam Deck delivers GPU compute at approximately $0.17 per GFLOPS — the best price-to-performance ratio among all handheld devices and competitive with many dedicated single-board computers. At 4–15W, a fleet of one hundred Steam Decks consumes less aggregate power than a single EPYC server while delivering 160 TFLOPS of GPU compute and 44.8 TFLOPS of CPU compute. The 16GB unified memory enables GPU inference on larger models than discrete mobile GPUs typically permit, and the built-in battery, display, and controls mean the device functions as a fully standalone node that can be deployed anywhere without peripheral dependencies.

The OLED model, despite its superior efficiency and Wi-Fi 6E networking, became significantly less attractive for dedicated procurement following a 43–46% price increase in May 2026. At $789–949, new OLED units compete with far more powerful alternatives. However, for the volunteer compute donor model — existing owners contributing idle cycles — both LCD and OLED models are equally viable. The primary HelixCluster value proposition for Steam Deck is not hardware procurement but zero-incremental-cost compute donation from a four-million-unit installed base.

The following table summarizes the key differences between LCD and OLED variants for cluster deployment purposes:

| Specification | Steam Deck LCD (Refurbished) | Steam Deck OLED (New) |
|---|---|---|
| **APU Process** | 7nm (Aerith) | 6nm (Sephiroth) |
| **CPU FLOPS** | ~448 GFLOPS FP32 | ~448 GFLOPS FP32 |
| **GPU FLOPS** | 1.6 TFLOPS FP32 | 1.6 TFLOPS FP32 |
| **RAM / Speed** | 16 GB LPDDR5-5500 | 16 GB LPDDR5-6400 |
| **RAM Bandwidth** | ~88 GB/s | ~102 GB/s |
| **Storage** | 64GB–512GB NVMe/eMMC | 512GB–1TB NVMe |
| **Wi-Fi** | Wi-Fi 5 (802.11ac) | Wi-Fi 6E (tri-band) |
| **Battery** | 40 Wh | 50 Wh |
| **TDP Range** | 4–15W | 4–15W |
| **Price** | $279–359 | $789–949 |
| **Cluster Suitability** | Excellent value | Diminished by price hike |

Realistic node estimates based on distributed computing opt-in rates (typically 2–5% for projects like BOINC or Folding@Home) suggest HelixCluster could attract 80,000–200,000 Steam Deck nodes, 10,000–25,000 x86 handheld nodes, and at most 1,000–5,000 Nintendo Switch hobbyist nodes in the near term. These figures, while conservative, represent a substantial compute pool: 200,000 Steam Decks alone deliver 320 petaFLOPS of aggregate GPU performance, equivalent to a mid-size supercomputing installation, operating entirely on donated idle cycles from volunteer device owners.

## 1.2 x86 Handhelds: ROG Ally, Legion Go, GPD Win, Ayaneo

### 1.2.1 AMD Z1 Extreme and Ryzen Z1: RDNA 3 Performance Leadership

The x86 handheld market extends well beyond the Steam Deck, and several competitors deliver substantially higher raw compute. The ASUS ROG Ally and Ally X, the Lenovo Legion Go, the GPD Win 4 and Win Mini, and various Ayaneo models all employ AMD's Ryzen Z1 Extreme or related Zen 4-based APUs with RDNA 3 graphics. The Z1 Extreme offers eight Zen 4 cores running at up to 5.1 GHz and twelve RDNA 3 compute units at 2.7 GHz, yielding 8.6 TFLOPS FP32 — more than five times the Steam Deck's GPU throughput. The ROG Ally X further extends this platform with 24GB of LPDDR5 memory (versus 16GB on the Steam Deck) and an 80Wh battery double the capacity of the original Ally.

The GPD Win 4 (2025) pushes even further with the Ryzen AI 9 HX 370 and its Radeon 890M integrated GPU, delivering up to 11.88 TFLOPS from sixteen RDNA 3.5 compute units alongside twelve Zen 5 CPU cores and 32GB of LPDDR5x memory. This is genuine desktop-replacement compute in a handheld form factor, complete with a physical keyboard, OCuLink external GPU expansion port, and full UEFI-based Linux compatibility.

The cumulative PC handheld installed base reached approximately 7.9 million units by the end of 2025, growing at 32% year over year. While the Steam Deck constitutes roughly half of this total, the x86 handheld segment collectively represents a rapidly expanding pool of high-performance volunteer compute donors. Each ROG Ally or GPD Win owner who installs Bazzite effectively doubles or triples their per-device contribution relative to a Steam Deck donor.

### 1.2.2 Linux Compatibility: Bazzite and Native Installation

Unlike the Steam Deck, these x86 handhelds do not ship with Linux. They run Windows 11 by default, but all models offer full Linux installation capability through standard UEFI boot with Secure Boot disabled. The Bazzite distribution — a Fedora-based, SteamOS-like operating system optimized for handheld gaming — has emerged as the de facto standard for this hardware category, with community builds that in some benchmarks achieve up to 32% better gaming performance than Windows. For HelixCluster, Bazzite provides the same container runtime, systemd, and GPU compute stack as SteamOS, making agent deployment straightforward across all x86 handhelds.

Ubuntu 23.04 and later versions boot out of the box on most models, with only occasional Wi-Fi driver issues that community kernels resolve. The standard Mesa RADV stack provides identical Vulkan and OpenCL support to the Steam Deck, while ROCm compatibility uses the `HSA_OVERRIDE_GFX_VERSION=11.0.0` override for RDNA 3. From a HelixCluster agent perspective, there is no meaningful difference in software environment between a Steam Deck running SteamOS and an ROG Ally running Bazzite.

### 1.2.3 Price/Performance Comparison

The following table compares the Steam Deck, the leading x86 handhelds, and the Orange Pi 5 Max reference platform across the metrics most relevant to HelixCluster deployment:

| Specification | Steam Deck (LCD refurb) | ROG Ally X | GPD Win 4 (2025) | Orange Pi 5 Max |
|---|---|---|---|---|
| **Price** | $279 | ~$999 | ~$1,100 | $125 (16GB) |
| **CPU** | Zen 2, 4c/8t | Zen 4, 8c/16t | Zen 5, 12c/24t | ARM A76, 8c |
| **CPU FLOPS** | ~448 GFLOPS | ~2,000+ GFLOPS | ~3,000+ GFLOPS | ~500 GFLOPS |
| **GPU** | 8 CU RDNA 2 | 12 CU RDNA 3 | 16 CU RDNA 3.5 | Mali-G610 MP4 |
| **GPU FLOPS** | 1.6 TFLOPS | 8.6 TFLOPS | 11.9 TFLOPS | ~0.5 TFLOPS |
| **RAM** | 16 GB LPDDR5 | 24 GB LPDDR5 | 32 GB LPDDR5x | 16 GB LPDDR4X |
| **RAM Bandwidth** | ~88 GB/s | ~88 GB/s | ~120 GB/s | ~34 GB/s |
| **TDP Range** | 4–15W | 9–30W | 20–35W | 5–10W |
| **Linux Support** | Native (SteamOS) | Full (Bazzite/Ubuntu) | Full (Bazzite/Ubuntu) | Full (Armbian) |
| **GPU Compute APIs** | Vulkan, OpenCL, ROCm | Vulkan, OpenCL, ROCm | Vulkan, OpenCL, ROCm | OpenCL, Vulkan |
| **$/GFLOPS (GPU)** | $0.17 | $0.09 | $0.09 | $0.25 |
| **Best Use Case** | Volunteer GPU donor | High-performance donor | Premium workstation node | Headless edge/ethernet |

The ROG Ally X and GPD Win 4 deliver superior raw compute per dollar at $0.09 per GFLOPS, but their higher acquisition cost limits volunteer adoption. The Steam Deck's $279 refurbished entry point, combined with its four-million-unit installed base and zero-friction Linux environment, makes it the highest-impact target despite lower per-device performance. The Orange Pi 5 Max remains competitive for headless CPU-only or ethernet-dependent workloads where its 2.5GbE port and sub-10W power envelope offset the weaker GPU ecosystem.

## 1.3 Nintendo Consoles

### 1.3.1 Original Switch: Tegra X1 with Homebrew Linux

The Nintendo Switch, with 155 million units sold, represents an enormous theoretical compute pool. The Tegra X1 SoC inside the original model provides four ARM Cortex-A57 cores and a 256-core Maxwell GPU delivering approximately 393 GFLOPS FP32 in docked mode. However, the practical HelixCluster viability of the original Switch is severely constrained. Running Linux requires the Atmosphere custom firmware, which in turn depends on an unpatchable bootROM exploit present only in early production units or a hardware modchip for later revisions. Nintendo actively bans modified consoles from online services, and the 4GB of RAM, absence of CUDA support, and reliance on Vulkan compute shaders alone limit practical workload compatibility to trivial proof-of-concept demonstrations.

Community efforts through the switchroot project maintain Ubuntu 24.04 LTS images for vulnerable hardware, but the ecosystem remains a hobbyist endeavor. For HelixCluster, the original Switch is classified as Tier 4 experimental at best, suitable only for research into ARM64 edge workloads and not for any production deployment.

### 1.3.2 Switch 2: Ampere GPU and the Homebrew Timeline

The Nintendo Switch 2, launched in 2025 with the custom NVIDIA T239 "Drake" SoC, dramatically improves the hardware proposition: eight Cortex-A78C cores, 1536 Ampere CUDA cores, 12GB of LPDDR5 memory with 102 GB/s bandwidth, and a docked GPU performance estimate of approximately 3.1 TFLOPS FP32. This is ten times the original Switch's compute and roughly double the Steam Deck's GPU throughput. The architectural leap from Maxwell to Ampere, combined with substantially more memory and the potential for CUDA support through NVIDIA's Linux for Tegra (L4T) distribution, makes the Switch 2 a theoretically high-value HelixCluster node.

The critical unknown is the homebrew timeline. As of mid-2025, no public jailbreak or kernel exploit exists. Nintendo learned from the Tegra X1's unpatchable RCM vulnerability and has likely implemented significantly enhanced boot chain security on the T239. Historical patterns suggest a 6–18 month window before a softmod or modchip solution emerges, but this is an estimate, not a guarantee. The microSD Express slot exposes a true PCIe Gen3 x1 NVMe link, which modders have already leveraged for storage expansion, but the system firmware remains uncompromised.

HelixCluster should monitor the Switch 2 homebrew scene on a quarterly basis. If and when Linux becomes bootable, the Switch 2 could deliver 3+ TFLOPS of Ampere GPU compute with CUDA compatibility in a portable, low-power device — a genuinely valuable addition to the cluster's ARM64 compute tier. Until then, it remains on the watch list with zero engineering investment.

## 1.4 Xbox and Other Gaming Platforms

### 1.4.1 Xbox Series X/S: Excluded Due to Sandboxed Dev Mode

The Xbox Series X presents a frustrating paradox for HelixCluster. Its hardware is genuinely impressive: an eight-core Zen 2 CPU at 3.8 GHz, 52 RDNA 2 compute units delivering 12.15 TFLOPS FP32, and 16GB of GDDR6 with up to 560 GB/s memory bandwidth. A jailbroken Series X running Linux would be an outstanding compute node, competitive with mid-range gaming PCs. However, no such jailbreak exists, and Microsoft's security has held across every Xbox generation.

The sanctioned Developer Mode offers only a sandboxed UWP environment with crippling restrictions: applications are limited to 1GB of RAM (5GB for games), 2–4 shared CPU cores, up to 45% GPU access, DirectX 11 only for applications, and no arbitrary code execution outside the UWP container. A $19 Partner Center account enables Dev Mode, but Microsoft can revoke access at any time. There is no path to native Linux, no container runtime, and no GPU compute API access suitable for distributed workloads.

For these reasons, all Xbox platforms — Series X, Series S, One X, and original One — are formally excluded from HelixCluster consideration. No engineering resources should be allocated to Xbox support. If a future jailbreak materializes, this assessment should be revisited immediately, but such an event is not predicted within the current planning horizon.

### 1.4.2 GPU Compute API Support Matrix

The diversity of GPU architectures across gaming devices creates a complex API compatibility landscape. The following table summarizes compute API availability across the platforms evaluated in this chapter:

| API / Platform | Steam Deck (RDNA 2) | ROG Ally (RDNA 3) | Switch (Maxwell) | Switch 2 (Ampere) | Xbox Series X (RDNA 2) |
|---|---|---|---|---|---|
| **Vulkan Compute** | Native (RADV) | Native (RADV) | Limited | Future potential | UWP-restricted |
| **OpenCL 3.0** | Mesa Rusticl | Mesa Rusticl | Not available | Future (L4T) | Not available |
| **ROCm/HIP** | Override required | Override required | N/A | N/A | Not available |
| **CUDA** | Not supported | Not supported | Not available | Potential (L4T) | Not available |
| **Native Linux** | Yes (SteamOS) | Yes (install) | Homebrew only | Pending jailbreak | No |
| **Workaround-free?** | Yes | Yes | No | No | N/A |
| **HelixCluster Tier** | Tier 2 (Compute) | Tier 2 (Compute) | Tier 4 (Exp.) | Tier 5 (Watch) | Unsupported |

The clear pattern emerging from this matrix is that Linux support and open driver stacks determine HelixCluster viability more strongly than raw FLOPS. The Steam Deck and ROG Ally achieve full compute API coverage through Mesa's open-source drivers, requiring at most an environment variable override for ROCm. The Switch platforms depend on homebrew maturity. The Xbox, despite possessing the most powerful GPU of the group, contributes zero useful compute due to its closed ecosystem.

## 1.5 Handheld Integration Architecture

### 1.5.1 Power-Aware Scheduling: Gaming-Aware Compute Management

The defining operational challenge for handheld compute nodes is that their primary purpose — gaming — must never be degraded by background cluster workloads. A Steam Deck owner contributing idle GPU cycles to HelixCluster must experience zero impact on frame rates, input latency, or battery life during gaming sessions. This requires a power-aware scheduling system that continuously monitors device state and adapts compute allocation in real time.

The scheduler operates on five distinct device state profiles. When the handheld is actively gaming, all GPU compute suspends immediately and CPU compute is restricted to low-priority background threads using no more than 25% of available cores. When docked with external power, the device may sustain full 15W TDP operation, dedicating both CPU and GPU to cluster workloads. On battery above 50% charge, a balanced profile permits 50% CPU and 50% GPU allocation at 10W TDP. Below 50% but above 20% charge, compute throttles to 25% of each processor. Below 20% battery, all cluster activity pauses to preserve remaining power for the device's primary function.

State detection relies on multiple signals: process name monitoring for known game executables, GPU utilization thresholds above 60% sustained for three seconds, Steam Deck-specific D-Bus signals from the Steam client, and external power presence detection. The transition from gaming to idle triggers within 500 milliseconds, releasing compute resources back to the cluster when the session ends. This responsiveness is essential for volunteer retention — a donor who experiences gaming interference will uninstall the agent permanently.

Beyond reactive state detection, the scheduler also implements predictive idle window estimation. By analyzing historical usage patterns, the agent learns a donor's typical gaming schedule and pre-stages workloads during anticipated idle periods. A donor who consistently plays for two hours each evening can have compute tasks queued and ready to execute the moment the game process terminates, maximizing utilization of each idle window without any perceptible startup delay.

### 1.5.2 Container Strategy: Distrobox and Toolbx for Isolated Agent Environments

HelixCluster agent deployment on handhelds uses a containerized approach that isolates cluster workloads from the host gaming environment while maintaining full GPU compute access. The recommended implementation uses Distrobox or Toolbx to create a mutable container layer atop the host's immutable or semi-immutable system (SteamOS, Bazzite), though standard Docker or Podman execution is equally viable on devices with standard filesystem layouts.

The container image is built from an Arch Linux base to match SteamOS and must include Mesa RADV drivers, Vulkan loader, and optional OpenCL and ROCm runtime components. The agent container requires privileged access to `/dev/dri` for GPU rendering, `/dev/kfd` for ROCm where applicable, and the host's network namespace for cluster communication. GPU memory is shared through the container boundary without overhead since AMD APUs use unified memory architecture.

```dockerfile
# HelixCluster Steam Deck Agent Container
FROM archlinux:latest

LABEL maintainer="helixcluster@example.org"
LABEL description="HelixCluster agent for Steam Deck and x86 handhelds"

# Core system dependencies
RUN pacman -Syu --noconfirm && \
    pacman -S --noconfirm \
        mesa vulkan-radeon vulkan-icd-loader \
        vulkan-tools clinfo \
        rocm-opencl-runtime rocm-hip-sdk \
        python python-pip docker \
        systemd dbus \
        wget curl git htop \
        && pacman -Scc --noconfirm

# ROCm workaround for RDNA 2 / RDNA 3 APU detection
ENV HSA_OVERRIDE_GFX_VERSION=10.3.0
ENV GGML_VULKAN=1

# HelixCluster agent binary and configuration
COPY helixcluster-agent /usr/local/bin/
COPY agent-config.yaml /etc/helixcluster/

# llama.cpp Vulkan backend for inference workloads
COPY --from=ghcr.io/ggml-org/llama.cpp:vulkan-latest \
    /app/llama-server /usr/local/bin/
COPY --from=ghcr.io/ggml-org/llama.cpp:vulkan-latest \
    /app/llama-cli /usr/local/bin/

# Health check endpoint
HEALTHCHECK --interval=60s --timeout=10s --start-period=30s --retries=3 \
    CMD helixcluster-agent health || exit 1

EXPOSE 9090/tcp

ENTRYPOINT ["/usr/local/bin/helixcluster-agent"]
CMD ["--config", "/etc/helixcluster/agent-config.yaml"]
```

The corresponding systemd unit file for host-level orchestration manages container lifecycle, automatic restart on failure, and graceful shutdown on gaming activity detection:

```ini
# /etc/systemd/system/helixcluster-agent.service
[Unit]
Description=HelixCluster Compute Agent for Handheld
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=simple
Restart=always
RestartSec=10

# Environment and GPU passthrough
Environment="HSA_OVERRIDE_GFX_VERSION=10.3.0"
Environment="GGML_VULKAN=1"
Environment="HELIX_TIER=edge"

ExecStartPre=-/usr/bin/docker rm -f helixcluster-agent
ExecStart=/usr/bin/docker run \
    --name helixcluster-agent \
    --device /dev/dri:/dev/dri \
    --device /dev/kfd:/dev/kfd \
    --group-add video \
    --group-add render \
    --network host \
    --pid host \
    -v /etc/helixcluster:/etc/helixcluster:ro \
    -v /var/lib/helixcluster:/var/lib/helixcluster \
    -e HSA_OVERRIDE_GFX_VERSION=10.3.0 \
    -e GGML_VULKAN=1 \
    -e HELIX_DEVICE_PROFILE=steamdeck \
    -e HELIX_POWER_AWARE=1 \
    ghcr.io/helixcluster/agent:handheld-latest

ExecStop=-/usr/bin/docker stop -t 30 helixcluster-agent
ExecStopPost=-/usr/bin/docker rm -f helixcluster-agent

[Install]
WantedBy=multi-user.target
```

The agent runtime integrates with the host's power management through D-Bus interfaces exposed by SteamOS and Bazzite. When the power daemon signals a transition from AC to battery power, or when Steam reports a game launch event, the agent receives a SIGUSR1 signal triggering a 500-millisecond checkpoint of in-memory state followed by voluntary container pause. On SIGUSR2 (game exit, AC reconnect, or explicit user resume), the container unpauses and resumes workload execution from the checkpointed state. This mechanism ensures that no task progress is lost during gaming interruptions and that compute donation remains entirely transparent to the device's owner.

For x86 handhelds running Bazzite rather than SteamOS, the same container and systemd configuration applies with only the `HELIX_DEVICE_PROFILE` environment variable changed to `bazzite-handheld`. The Bazzite distribution exposes identical D-Bus power signals and GPU device paths, making the agent fully portable across the x86 handheld ecosystem. For the Switch and other ARM-based handhelds where homebrew Linux becomes available, a separate ARM64 container build uses the same architecture but with Mali or Ampere GPU drivers substituted for AMD's Mesa stack.

The volunteer donor model, the tiered trust architecture, and the power-aware scheduling system together establish handheld gaming devices as a legitimate and productive tier of HelixCluster compute. The Steam Deck leads this tier by every practical metric: native Linux support, price per FLOPS, installed base size, and ecosystem openness. The x86 handhelds extend it with higher per-device performance for users willing to install Linux or Bazzite. The Nintendo platforms represent future potential contingent on homebrew maturity. And the Xbox, for all its impressive silicon, remains permanently excluded until its ecosystem opens in ways that current evidence does not suggest will occur. The volunteer GPU tier, anchored by the Steam Deck and extended through the growing x86 handheld ecosystem, represents a genuinely new paradigm for distributed computing: consumer entertainment hardware that contributes production-grade FLOPS during its many hours of daily idleness, at zero incremental hardware cost to the cluster operator.
