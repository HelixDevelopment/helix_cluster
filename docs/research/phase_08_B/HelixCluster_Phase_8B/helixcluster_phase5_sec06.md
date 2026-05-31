## 6. IoT, Smart Home & Edge Devices

The compute fabric of a modern home extends far beyond laptops and servers. Wi-Fi routers handle packet routing with quad-core ARM CPUs; network-attached storage devices run Docker containers on x86-class processors; smart TVs stream 4K video while their multi-core SoCs sit largely idle; and wearables process neural workloads on-wrist. This chapter maps the hidden compute topology of the connected home, identifying which devices can serve as genuine HelixCluster nodes and which remain inaccessible behind locked-down platforms.

A clear pattern emerges from the analysis: **openness beats raw performance at the edge**. The most valuable edge nodes are not the most powerful devices but the ones that expose a Linux environment, package management, and persistent background services to the operator. A $159 OpenWrt router with Docker contributes more to the cluster than a $400 smart speaker with no developer access. This principle guides every recommendation in the sections that follow.

---

### 6.1 Routers as Cluster Gateways

Routers are the unsung heroes of distributed edge computing. They are already always-on, already networked, and---in the OpenWrt ecosystem---already running full Linux. The critical insight is that modern routers separate the packet forwarding path from the general-purpose CPU. Hardware offloading engines handle NAT, VLAN tagging, and Wi-Fi frame aggregation, leaving the ARM CPU cores available for user-space processes. A quad-core Cortex-A53 router forwarding gigabit traffic may use less than 15% of its CPU cycles for routing, with the remainder available for cluster duties.

#### 6.1.1 GL.iNet MT6000: The Best Edge Node

The GL.iNet GL-MT6000 (Flint 2) is the single most cost-effective edge compute node identified across all Phase 5 research. At approximately $159, it delivers a MediaTek MT7986AV (Filogic 830) SoC with a quad-core ARM Cortex-A53 @ 2.0 GHz, 1 GB of DDR4-3200 RAM, 8 GB of eMMC 5.1 storage, and---critically---dual 2.5 GbE ports. No other device under $200 combines this level of compute, container support, and high-speed networking.

The 8 GB eMMC is the decisive differentiator. Most OpenWrt routers ship with 16--256 MB of flash storage, barely enough for the operating system and a few packages. The MT6000's 8 GB eMMC enables Docker deployment with room for multiple container images, a lightweight database, or cached cluster state. Installing Docker on OpenWrt 24.x is straightforward: `opkg install dockerd luci-app-dockerman` enables the full container toolchain, and community reports confirm stable operation of Nginx Proxy Manager, AdGuard Home, Pi-hole, and other multi-container workloads [^2601^].

Network architecture is equally compelling. The dual 2.5 GbE ports function as WAN and LAN backhaul, while four additional Gigabit LAN ports provide downstream connectivity. WireGuard VPN throughput reaches 900 Mbps---sufficient for encrypted mesh backhaul between cluster segments---and OpenVPN achieves 190 Mbps for legacy compatibility [^2454^]. With typical power consumption under 20 W (from a 48 W peak adapter), the MT6000 delivers roughly 8--12 GFLOPS of FP32 compute per watt at a cost-per-GFLOPS-year that rivals dedicated SBCs.

#### 6.1.2 GL.iNet MT3000: Lightweight Relay

For smaller deployments or budget-constrained environments, the GL.iNet GL-MT3000 (Beryl AX) at approximately $89 provides a capable lightweight relay node. The MediaTek MT7981 (Filogic 820) offers a dual-core Cortex-A53 @ 1.3 GHz, 512 MB DDR3L, 256 MB NAND flash, one 2.5 GbE WAN port, two Gigabit LAN ports, and Wi-Fi 6 (AX) 2x2 connectivity [^2479^].

The MT3000's 256 MB of flash precludes Docker, but its 512 MB RAM is sufficient for a native opkg-installed HelixCluster agent written in Go or Rust. The USB-C power input (5 V / 3 A) means the device can run from a power bank during outages, and its compact form factor suits travel or temporary deployments. In a HelixCluster topology, the MT3000 serves as a mesh relay, heartbeat coordinator, or lightweight data aggregator rather than a full compute worker. The price-to-reliability ratio is exceptional: at $89, it costs less than most unmanaged switches while providing a full Linux environment.

#### 6.1.3 Network Appliance Architecture: Router as Mesh Gateway

The architectural pattern for router-integrated HelixCluster nodes treats the device as a dual-function appliance: Layer 3 gateway for the local network and Layer 7 cluster participant for the global fabric. The OpenWrt host maintains these responsibilities in separate execution contexts.

**OpenWrt Agent Architecture:**

```
+------------------+        +------------------+
|  Hardware        |        |  Linux Kernel    |
|  MT7986AV SoC    |------->|  OpenWrt 24.x    |
|  4x A53 @ 2GHz   |        |  1GB DDR4        |
|  8GB eMMC        |        |  2.5GbE x2       |
+------------------+        +------------------+
                                     |
        +----------------------------+-------------------------+
        |                            |                         |
   [Netfilter/     [Wi-Fi     [Docker Engine           [opkg]
    HW Offload]    mac80211]   (containerd)]         [packages]
        |                            |                         |
   Routing Plane           +--------|---------+       [helix-agent]
   (iptables/nftables)     |        |         |         (native)
                           |   +----+----+    |
                           |   |  Agent  |    |
                           |   | Container    |
                           |   +----+----+    |
                           |        |         |
                     [AdGuard] [Prometheus] [Mesh]
                           |                 Proxy    |
                           +----------------+---------+
                                            |
                                    WireGuard Mesh Tunnel
                                    (900 Mbps encrypted)
                                            |
                                +-----------+-----------+
                                |                       |
                          [Regional Hub]          [Peer MT6000]
                          (NanoPi R6S/x86)        (Remote Site)
```

The routing plane operates in kernel space with hardware acceleration, isolated from the user-space cluster agent. The HelixCluster agent runs either as a Docker container (recommended for MT6000-class storage) or as a native opkg package (required for flash-constrained devices like the MT3000). The agent communicates with regional cluster hubs via a WireGuard mesh tunnel that encrypts all cluster traffic without interfering with local LAN forwarding.

For flash-constrained routers, a split architecture is viable: the router runs a minimal 5 MB native agent that handles heartbeat, task dispatch, and local sensor aggregation, while heavier workloads (container execution, log analysis, ML inference) are forwarded to a regional hub with more RAM and storage. This "thin agent, thick hub" model maximizes the number of edge participants without overwhelming limited router resources.

**Table 6.1: Edge Router Comparison**

| Router | CPU | RAM | Storage | 2.5GbE | Docker | Power | Price | Cluster Tier |
|--------|-----|-----|---------|--------|--------|-------|-------|-------------|
| GL.iNet MT3000 | 2x A53 @ 1.3 GHz | 512 MB | 256 MB NAND | 1x | No | ~5--7 W | $89 | T2 (relay) |
| **GL.iNet MT6000** | **4x A53 @ 2.0 GHz** | **1 GB** | **8 GB eMMC** | **2x** | **Yes** | **<20 W** | **$159** | **T1 (edge)** |
| ASUS TUF-AX6000 | 4x A53 @ 2.0 GHz | 512 MB | 256 MB | 2x | No | ~15 W | $220 | T2 (relay) |
| NanoPi R6S | 4x A76 + 4x A55 | 8 GB | 32--64 GB eMMC | 2x | Yes | 4.6--11.4 W | $129 | T1 (compute) |
| TP-Link Archer C7 | QCA9558 MIPS | 128 MB | 16 MB | No | No | ~10 W | $80 | T3 (incompatible) |

The NanoPi R6S deserves special mention as a router-form-factor compute node. It is not a traditional router but rather an RK3588S SBC packaged in a router enclosure with dual 2.5 GbE ports. Its 8 GB of LPDDR4X, 6 TOPS NPU, and 4.6 W idle power consumption make it the most powerful device in this category, though it requires more technical setup than the turnkey MT6000 [^2505^].

---

### 6.2 NAS as Persistent Storage Nodes

Network-attached storage devices are ideal HelixCluster candidates: they are always-on, engineered for reliability, and increasingly powerful. A modern 4-bay NAS with an AMD Ryzen or Intel Celeron CPU, 32 GB of expandable RAM, and native Docker support is not merely a file server---it is a full member of the compute fabric.

#### 6.2.1 Synology DS923+: Full Cluster Member

The Synology DS923+ occupies the premium tier of home NAS devices. Its AMD Ryzen R1600 is a dual-core, four-thread processor with a 3.1 GHz boost clock and ECC RAM support---a rarity at this price point. The base configuration includes 4 GB of DDR4 ECC, expandable to 32 GB, and two M.2 NVMe slots that can serve as cache accelerators or independent storage pools [^2456^].

Docker support is provided through Synology's Container Manager (formerly Docker), which offers a web-based UI for pulling images from Docker Hub, managing volumes, and configuring network bridges. The DS923+ can run multiple containerized HelixCluster agents simultaneously: a storage-backed worker for data-intensive tasks, a Prometheus node for metrics collection, and a MinIO instance for S3-compatible object storage within the cluster.

The optional E10G22-T1-Mini add-on card upgrades the dual 1 GbE ports to a single 10 GbE connection, transforming the DS923+ from a network client into a backbone-class storage server. For HelixCluster deployments handling large dataset shuffling---machine learning training data, video transcode queues, or scientific dataset mirrors---this 10x bandwidth increase is transformative. Power consumption ranges from 12 W in hibernation to 32 W under full access, making the DS923+ efficient for its compute class.

#### 6.2.2 QNAP TS-464 and TrueNAS Options

The QNAP TS-464 provides a compelling alternative at approximately $450. Its Intel Celeron N5095 quad-core processor bursts to 2.9 GHz and includes hardware-accelerated AES encryption. The TS-464 ships with dual 2.5 GbE ports (no add-on card required), 4--8 GB of DDR4 upgradable to 16 GB, and a PCIe Gen3 x2 slot for 10 GbE or Edge TPU expansion cards [^2506^].

QNAP's Container Station supports Docker, LXD, and Kata Containers---the broadest container runtime support among consumer NAS devices. This flexibility allows operators to isolate cluster workloads at different security boundaries: Docker for trusted HelixCluster agents, LXD for semi-trusted community workloads, and Kata for untrusted third-party code requiring VM-level isolation.

TrueNAS SCALE (based on Debian Linux with Kubernetes via Helm) represents the open-source path. While TrueNAS hardware requires self-assembly or vendor integration (iXsystems Mini series), it offers native Docker/Kubernetes support and ZFS-based storage with unparalleled data integrity guarantees. For operators already running TrueNAS, adding a HelixCluster agent container is a single `docker run` command.

#### 6.2.3 Storage + Compute Dual-Role Architecture

The defining architectural pattern for NAS-integrated cluster nodes is the **storage-compute dual role**. The NAS continues to serve files via SMB/NFS/AFP to household clients while simultaneously donating idle CPU cycles, RAM, and disk I/O to the cluster. This is not a secondary function---it is a primary design principle enabled by modern NAS hardware and software.

**Table 6.2: NAS Cluster Node Comparison**

| NAS | CPU | RAM (max) | Networking | Docker | Storage Bays | Power | Price | Cluster Tier |
|-----|-----|-----------|------------|--------|-------------|-------|-------|-------------|
| Synology DS923+ | AMD R1600 (2C/4T) | 4--32 GB ECC | 2x 1 GbE + 10 GbE option | Yes (Container Manager) | 4 | 12--32 W | ~$550 | T1 (storage) |
| Synology DS224+ | Intel J4125 (4C) | 2--6 GB | 2x 1 GbE | Yes (Container Manager) | 2 | ~18 W | ~$300 | T1 (light) |
| QNAP TS-464 | Intel N5095 (4C) | 4--16 GB | 2x 2.5 GbE | Yes (Docker/LXD/Kata) | 4 | ~22 W | ~$450 | T1 (storage) |

#### NAS Docker Configuration

The recommended deployment for a Synology or QNAP NAS uses a `docker-compose.yml` file that defines the HelixCluster agent, persistent storage volumes, and resource constraints:

```yaml
# helix-agent-nas.yml
# Deploy: docker-compose -f helix-agent-nas.yml up -d
version: "3.8"

services:
  helix-agent:
    image: helixcluster/agent:latest
    container_name: helix-nas-agent
    restart: unless-stopped
    # Resource limits prevent the agent from impacting NAS file services
    deploy:
      resources:
        limits:
          cpus: "2.0"
          memory: 2G
        reservations:
          cpus: "0.5"
          memory: 256M
    environment:
      - HELIX_NODE_ROLE=storage-worker
      - HELIX_STORAGE_PATH=/data
      - HELIX_MESH_ENDPOINT=wss://hub.helix.local:8443
      - HELIX_WIREGUARD_PUBKEY=${WG_PUBKEY}
      - HELIX_MAX_TASKS=4
    volumes:
      # Persistent state for agent identity and cached data
      - /volume1/helix/config:/etc/helix:rw
      # Shared NAS folder for cluster-accessible storage
      - /volume1/helix/data:/data:rw
      # Read-only mount for NAS metrics export
      - /proc:/host/proc:ro
      - /sys:/host/sys:ro
    ports:
      - "9100:9100"   # Node exporter metrics
      - "7946:7946"   # Helix mesh gossip
      - "7946:7946/udp"
    # Run with elevated caps for network mesh and wireguard
    cap_add:
      - NET_ADMIN
      - NET_RAW
    sysctls:
      - net.ipv4.ip_forward=1
      - net.ipv4.conf.all.src_valid_mark=1

  # Optional: S3-compatible gateway for cluster object storage
  minio:
    image: minio/minio:latest
    container_name: helix-minio
    restart: unless-stopped
    command: server /data --console-address ":9001"
    environment:
      - MINIO_ROOT_USER=helix-cluster
      - MINIO_ROOT_PASSWORD=${MINIO_PASSWORD}
    volumes:
      - /volume1/helix/s3:/data:rw
    ports:
      - "9000:9000"
      - "9001:9001"
    deploy:
      resources:
        limits:
          cpus: "1.0"
          memory: 1G
```

This configuration pins the agent to 2 CPU cores and 2 GB of RAM, ensuring that DSM/QTS file services retain headroom for household clients. The `restart: unless-stopped` policy ensures the agent resumes after NAS reboots or DSM updates. Environment variables configure the node as a `storage-worker` role, which advertises high-capacity disk access to the cluster scheduler and receives data-intensive workloads (log aggregation, dataset preprocessing, model checkpoint storage) in preference to compute-only tasks.

Key operational considerations for NAS cluster nodes:

- **Update reboots**: DSM and QTS updates require system reboots, causing temporary node unavailability. Configure the agent with a graceful shutdown timeout and mark the node as `drainable` in the cluster scheduler.
- **Resource contention**: Plex or Jellyfin transcoding competes directly with cluster agents for CPU. Use cgroup CPU limits or Synology's built-in resource monitor to enforce hard caps.
- **Disk spin-up latency**: HDD-based NAS devices experience I/O stalls when sleeping disks spin up. Use NVMe cache pools for agent state and temporary workspace, reserving HDD arrays for bulk storage.

---

### 6.3 Smart TVs as Idle Compute Donors

The modern smart television is a surprisingly capable computer. A typical 2024-model 4K TV contains a quad-core ARM SoC, 2--4 GB of RAM, gigabit networking (via USB adapter or built-in), and dedicated video decode hardware that handles 4K HDR streaming with minimal CPU involvement. When the TV is on but the viewer is passively watching, or when the TV is in standby with the SoC still powered, significant CPU cycles sit unused. The challenge is not hardware capability but software access.

#### 6.3.1 LG webOS: The Most Open Platform

LG webOS is the most favorable smart TV platform for HelixCluster integration. Its background service model allows JavaScript services to run persistently on Node.js, with full access to Node.js core modules and support for third-party JavaScript packages (without C/C++ add-ons) [^2458^]. The webOS OSE (Open Source Edition) takes this further by providing a fully open-source build target, and the `ares-generate -t js_service` CLI tool creates production-ready service templates [^2457^].

A HelixCluster agent for webOS can be implemented as a JS Service: a lightweight Node.js process that starts at boot, maintains a WebSocket connection to the cluster mesh, and accepts task dispatches over the webOS bus. Realistic workloads include data relay (forwarding IoT sensor readings to regional hubs), heartbeat services (acting as a local coordination beacon for nearby edge nodes), and simple aggregation (computing rolling averages of temperature, power, or network latency data). The agent runs at low priority, yielding CPU instantly when the foreground application (Netflix, YouTube, the OS UI) demands resources.

The limitation is clear: webOS services run JavaScript only. No native code, no Docker, no GPU compute access. The platform is suitable for lightweight coordination and relay tasks, not for numerical workloads or ML inference.

#### 6.3.2 Samsung Tizen: Computational Capability During Decode

Samsung Tizen TVs offer background service applications written in Node.js, with `onStart()`, `onRequest()`, and `onExit()` callbacks managed by the Tizen SDK [^2459^]. Developer mode is enabled by entering the sequence "12345" on the Apps panel, after which apps can be sideloaded via the Tizen Studio IDE.

Tizen's background services are more restrictive than webOS. The security sandbox limits file system access, network destinations may be whitelisted by the platform, and the developer mode certificate expires periodically, requiring reactivation [^2585^]. A HelixCluster agent on Tizen would use the `MessagePort` API to communicate between background service and foreground companion app, with tasks limited to HTTP-based data relay and lightweight processing.

The key insight for both webOS and Tizen is that 4K video decode happens on dedicated VPU (Video Processing Unit) silicon. The ARM CPU cores handle UI rendering, network I/O, and application logic---but during passive viewing, even these duties are minimal. A background service consuming 5--10% of CPU during a movie causes no perceptible performance degradation.

#### 6.3.3 NVIDIA Shield TV Pro: A Switch-Class Compute Node

The NVIDIA Shield TV Pro is categorically different from other smart TV devices. Its Tegra X1+ SoC (T210 B01) is the same architecture that powers the Nintendo Switch, featuring a quad-core 2.0 GHz ARM CPU, 256-core Maxwell GPU, and 3 GB of DDR3 RAM [^2545^]. This is not a TV platform with a weak SoC; it is a low-power SBC disguised as a streaming device.

Full Android TV means full Android capabilities: native code via the NDK, background services with `JobScheduler` or foreground services, network sockets, and---uniquely---CUDA-accessible GPU compute. The Maxwell GPU, while aging, delivers approximately 100--150 GFLOPS FP32, suitable for lightweight ML inference or parallel data processing. At 5--10 W typical power draw, the Shield TV Pro outperforms many dedicated SBCs on a per-watt basis.

For HelixCluster, the Shield TV Pro is a Tier 1.5 node: not quite a full compute worker (3 GB RAM limits model sizes), but far more capable than any other TV-class device. It runs a containerized or native agent alongside its streaming duties, contributing GPU-accelerated tasks that would overwhelm CPU-only edge nodes.

**Table 6.3: Smart TV Compute Capability Comparison**

| Device | CPU | RAM | Background Services | Native Code | GPU Access | Openness | Cluster Tier |
|--------|-----|-----|---------------------|-------------|------------|----------|-------------|
| Samsung Tizen TV | Quad ARM A-series | 1.5--3 GB | Node.js (Tizen SDK) | No | No | Medium | T2 (relay) |
| LG webOS TV | Dual/Quad ARM | 1.5--4 GB | Node.js (JS Services) | No | No | High | T2 (relay) |
| Chromecast Google TV | 4x A55 @ 1.9 GHz | 2 GB | Android Services | Yes (NDK) | Mali-G31 | Medium | T2 (light) |
| **NVIDIA Shield TV Pro** | **Tegra X1+ 4-core** | **3 GB** | **Full Android** | **Yes (NDK)** | **256-core Maxwell** | **High** | **T1.5 (GPU)** |

Operational cautions for TV compute nodes are significant. Background services may be terminated during OS updates or memory pressure events. Network connectivity is unreliable: many TVs disconnect from Wi-Fi in deep standby. And there is no persistence guarantee---local databases or cached state may be wiped by platform maintenance. TV nodes should be treated as **ephemeral, best-effort participants**: valuable for augmenting cluster capacity during evening viewing hours, but never depended upon for critical-path workloads.

---

### 6.4 Wearables & Smart Speakers

Not every connected device can join the cluster. Some of the most computationally interesting hardware---Apple Watches with Neural Engines, HomePods with room-filling acoustic processing, Echo devices with always-on voice recognition---are inaccessible by design. This section explains the exclusions and identifies the specific platform restrictions that prevent integration.

#### 6.4.1 Exclusion Rationale: Closed Ecosystems, No Background Freedom

**Apple Watch (S9/S10 SiP)**. The Apple S9 SiP contains a capable dual-core CPU, 1 GB of RAM, 64 GB of storage, and a 4-core Neural Engine delivering approximately 5 TOPS---in theory, sufficient for on-device ML inference and lightweight cluster tasks. In practice, watchOS imposes draconian restrictions on background execution. Background tasks are limited to specific categories (background refresh, URL session, processing) and subject to strict time and battery constraints. The 300 mAh battery and 1--2 W thermal envelope make sustained compute donation physically impossible. The platform is entirely closed: no sideloading, no daemon installation, no WebSocket server that could maintain a cluster connection. The Apple Watch is excluded from all cluster tiers.

**Amazon Echo / Echo Dot**. The Echo Dot's MediaTek MT8516 (quad-core Cortex-A35 @ 1.3 GHz, 512 MB RAM, 4--8 GB eMMC) is modest but functional hardware. The fatal restriction is software: Alexa Skills execute exclusively as AWS Lambda functions in the cloud, not as on-device processes [^2534^]. There is no developer mode that enables persistent background code, no local package manager, and no path to running a HelixCluster agent. Amazon's AZ2 Neural Edge processor (in newer devices) is similarly locked. Echo devices are excluded.

**Apple HomePod / HomePod mini**. The HomePod mini uses the Apple S7 SiP with 1.5 GB of RAM and 32 GB of storage [^2540^]. The original HomePod used the Apple A8 with 1 GB of RAM. Both run audioOS, a variant of tvOS, with zero developer access, zero sideloading, and zero background service capability. The hardware is not the limitation; the software barrier is absolute.

**Google Nest Hub / Nest Mini**. The second-generation Nest Hub runs Fuchsia OS on an Amlogic S905D3---the same capable chip as the Chromecast with Google TV---with 2 GB of RAM. Despite Fuchsia's open-source pedigree, custom code execution is not available to end users. The device is a closed platform, and its compute potential remains untapped.

**Wear OS (Qualcomm Snapdragon W5+)**. The W5+ Gen 1 is the most advanced wearable SoC available: quad-core Cortex-A53 @ 1.7 GHz, Adreno 702 GPU, 1.5--2 GB LPDDR4, and a 4 nm process node [^2548^]. Wear OS permits background services with fewer restrictions than watchOS, but battery optimization policies aggressively suspend them. The 1--2 W sustained thermal envelope and intermittent connectivity (Bluetooth tethering to a phone) make the platform unsuitable for any meaningful compute donation. Wear OS is excluded.

#### 6.4.2 Future Possibilities If Platforms Open

The exclusions are not permanent. Several developments could unlock wearable and smart speaker compute for future HelixCluster versions:

- **Regulatory pressure**: The EU's Digital Markets Act and similar legislation may force Apple and Amazon to permit sideloading and alternative app stores on their devices, potentially opening watchOS and audioOS to developer tools.
- **Open-source firmware**: Community projects to reverse-engineer HomePod or Echo firmware could yield a Linux boot path, as happened with early smart TV models and game consoles.
- **New hardware categories**: AR glasses and AI pins are emerging as new wearable form factors, some of which (Rabbit R1, Humane Pin) run Android or Linux variants with more open software stacks.
- **Cloud-offload hybrids**: Even without on-device execution, wearables could function as cluster *sensors* rather than compute donors, streaming health, environmental, and location data to nearby edge nodes for processing. This "sensor edge" model does not require background compute freedom---only outbound network access.

For the present, however, these devices represent billions of dollars of silicon that cannot contribute to distributed compute. The cluster designer should ignore them entirely and focus engineering effort on the open platforms documented in Sections 6.1--6.3: routers with OpenWrt, NAS devices with Docker, and---for the adventurous---smart TVs running webOS or Android TV.

The strategic lesson of this chapter is that **the best cluster node is the one you can actually program**. A $50 Chromecast with developer mode enabled contributes more than a $400 HomePod with no access. A $159 OpenWrt router with Docker outperforms a $550 Apple Watch with a 5 TOPS Neural Engine. Openness is not merely a philosophical preference in distributed systems engineering; it is a hard technical prerequisite that determines whether a device exists in the cluster topology at all.
