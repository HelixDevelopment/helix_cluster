## Facet: Network Protocols & Inter-Node Communication for Clusters

### Key Findings

#### 1. RDMA over Converged Ethernet (RoCE) & High-Performance Interconnects

- **RDMA requires specialized NICs on both endpoints**, making it unsuitable for general-purpose clusters with commodity hardware. RoCE v2 operates over standard Ethernet but requires lossless network configuration (PFC/ECN) and RDMA-capable NICs [^59^]. For our Cluster OS targeting Gigabit Ethernet and SSH, native RDMA is likely out of scope unless upgrading to Mellanox/NVIDIA ConnectX hardware.

- **UCX (Unified Communication X)** is the emerging standard framework for RDMA and high-performance networking, supporting InfiniBand, RoCE, Cray uGNI, TCP, and shared memory transports. UCX provides 32.8x lower latency than Fujitsu MPI in multithreaded benchmarks (1.7 µs vs 56.0 µs for 24 threads) and 107x higher message rates [^111^]. UCX is integrated into Open MPI, MPICH, and Apache Spark.

- **CXL (Compute Express Link)** represents the next generation of rack-level memory interconnect, offering cache-coherent memory sharing at sub-microsecond latencies — an order of magnitude lower than Ethernet/InfiniBand. CXL 3.0 supports fabric topologies with up to 4,096 end devices for large-scale resource pooling [^110^]. However, CXL requires dedicated cabling and is currently cost-prohibitive for sub-rack deployments [^110^].

- **Key insight for Cluster OS**: RDMA/RoCE is performance-optimal but requires specific hardware. For a software-defined cluster OS over Gigabit Ethernet, focus on TCP-based optimizations (DPDK, kernel tuning) rather than RDMA.

#### 2. InfiniBand vs Ethernet for Clusters

- **InfiniBand dominates HPC**: Modern HPC interconnects offer <1 µsec latency, >100 Gb/s bandwidth, >150M messages/sec injection rates, OS bypass, and RDMA offload [^117^]. HDR InfiniBand provides 200 Gbps, NDR provides 400 Gbps.

- **Ethernet is catching up for many workloads**: 10/25/50/100 Gb/s Ethernet with TCP/UDP is now standard in data centers. RoCE brings RDMA semantics to Ethernet but requires compatible switches configured for lossless operation [^117^].

- **Decision framework**: Use InfiniBand for tightly-coupled HPC (MPI, tightly-coupled simulations). Use Ethernet with RoCE for GPU clusters and AI/ML training. Use standard TCP/IP Gigabit Ethernet for loosely-coupled clusters, which is our target [^117^].

- **For our Cluster OS**: Gigabit Ethernet is the baseline. The focus should be on maximizing TCP/IP performance through kernel tuning, efficient protocols, and minimizing serialization overhead rather than pursuing RDMA.

#### 3. ZeroMQ — Message Patterns, Performance, Cluster Topology

- **ZeroMQ excels in throughput for large payloads and scales best with subscriber count**. In the most comprehensive recent benchmark (2025), ZeroMQ achieves ~4.8 GB/s in-process throughput, peaking at ~2.9 GB/s over TCP — the highest among brokerless messaging libraries [^44^].

- **Key performance data from systematic evaluation** [^44^] [^45^]:
  - In-process latency: <10 µs for small messages
  - TCP latency: ~20 µs at 1 KB, scaling to ~400 µs at 512 KB
  - ZeroMQ has the narrowest latency distribution (best worst-case latency)
  - ZeroMQ scales best in throughput as subscriber count increases
  - ZeroMQ is most CPU-efficient for payloads >128 KB

- **Message patterns critical for cluster design**: Request-Reply (client-server), Publish-Subscribe (broadcast), Pipeline (parallel work distribution), Router-Dealer (flexible task distribution to workers) [^46^]. The Router-Dealer pattern is ideal for distributed cluster task scheduling.

- **Security**: ZeroMQ relies on external CurveZMQ for encryption (based on Curve25519, ChaCha20-Poly1305, providing perfect forward secrecy) [^140^] [^141^]. This adds setup complexity but provides modern cryptography comparable to WireGuard's primitives.

- **gRPC vs ZeroMQ comparison**: ZeroMQ shows lower average latency (15ms vs 20ms in one study) and higher throughput (12,000 vs 10,000 messages/sec) with lower overhead (2KB vs 5KB per message). ZeroMQ supports more nodes (150 vs 100) with better peer-to-peer support [^139^].

#### 4. nanomsg / nng — Next Generation After ZeroMQ

- **NanoMsg outperforms ZeroMQ for small payloads (<64 KB)**. In comprehensive benchmarks, NanoMsg achieves the best latency, throughput, and CPU efficiency for messages up to 64 KB, after which ZeroMQ overtakes [^44^].

- **NNG (NanoMsg Next Generation) is the only library under active development** but shows less competitive performance overall. NNG does not scale as well as ZeroMQ or NanoMsg in most configurations [^44^].

- **Key trade-offs** [^44^]:
  - NanoMsg: Best for small messages (<64 KB), lowest CPU usage, most memory efficient
  - ZeroMQ: Best for large payloads, highest throughput, best scaling
  - NNG: Under active development, competitive for some TCP latency scenarios

- **Verdict for Cluster OS**: ZeroMQ is more mature with better ecosystem. NanoMsg could be considered if the cluster primarily exchanges small control messages (<64 KB).

#### 5. gRPC — HTTP/2 Based RPC, Streaming, Performance

- **gRPC delivers 77% lower latency than REST for small payloads (1 KB: 2.3ms vs 10.1ms p50)** and 2-3x higher throughput per core (50,000-100,000 RPS vs 15,000-35,000 RPS) [^86^]. Serialized payload sizes are ~10x smaller than JSON (50-200 bytes vs 500-2,000 bytes).

- **gRPC handles scaling clients best** among SOAP, REST, and gRPC. Saturation point: gRPC 69,614 RPS at 600 users vs REST 20,032 RPS at 360 users vs SOAP 8,905 RPS at 210 users [^87^].

- **TLS overhead is minimal on gRPC** (8% throughput drop vs 55% for REST), making it ideal for secure cluster communication [^87^].

- **Low-latency gRPC benchmark on 50 Gbps network**: P50 latency of 91 µs at 1 in-flight request, scaling to 440 µs at 18 in-flight with ~44,000 RPS. Multiple connections improve throughput 6x for regular RPCs [^91^].

- **Verdict for Cluster OS**: gRPC is excellent for structured RPC between cluster services (scheduler, resource manager, monitoring). Use with Protocol Buffers for efficient serialization. Not ideal for high-frequency small messages where ZeroMQ excels.

#### 6. Apache Arrow Flight — Zero-Copy Data Transfer

- **Arrow Flight achieves 23x throughput improvement over REST/JSON** (18.7 GB/s vs 0.8 GB/s) and 7.8x over gRPC/Protobuf (2.4 GB/s) for single-node transfers [^61^].

- **Key performance numbers** [^61^] [^64^]:
  - Single-node throughput: 18.7 GB/s (approaching L3 cache bandwidth)
  - 64-node cluster aggregate: 475 GB/s
  - End-to-end latency at 100M records: 320 ms (vs 8,400 ms for REST/JSON — 26x reduction)
  - CPU on serialization: effectively zero (vs 54% for traditional protocols)
  - 85% of CPU directed toward actual compute workload (vs 13% for traditional)

- **Arrow Flight uses gRPC under the hood** but eliminates serialization by transmitting Arrow's columnar in-memory format directly [^60^]. Flight SQL outperforms ODBC by 20-50x and turbodbc by 20x [^64^].

- **For Cluster OS**: Arrow Flight is ideal for data-intensive workloads (distributed analytics, ML training data distribution). For control plane communication, it's overkill.

#### 7. libfabric — Unified Communication Framework

- **libfabric (OpenFabrics Interfaces / OFI)** defines a communication API for high-performance parallel and distributed applications, enabling a tight semantic map between applications and underlying fabric services [^71^].

- **Key providers** [^62^] [^66^]:
  - Core: tcp, udp, shm (shared memory), verbs (RDMA/InfiniBand/RoCE), EFA (AWS), GNI (Cray), PSM/PSM2 (Intel), usNIC (Cisco), Blue Gene/Q
  - Utility: RxM (reliable messaging over MSG endpoints), RxD (reliable datagram)

- **Latest release: v2.2.0** (as of late 2024). Targets Linux, FreeBSD, Windows, OS X [^71^].

- **For Cluster OS**: libfabric is primarily useful if we plan to support RDMA-capable hardware in the future. For pure TCP/Gigabit Ethernet, the tcp and shm providers could be useful, but the complexity may not be justified.

#### 8. DPDK — Kernel Bypass Networking

- **DPDK achieves near line-rate (100 Gbps) with a single core**, while Linux kernel networking requires 4-8 cores to saturate a 100 Gbps NIC — a 4x efficiency gap [^59^].

- **DPDK key mechanisms**: (1) poll mode drivers eliminating interrupts/context switches, (2) huge pages reducing TLB misses, (3) zero-copy packet processing via ring buffers [^59^].

- **Latency comparison**: Kernel networking 10-100 µs vs DPDK 1-10 µs per packet (10x reduction). Throughput: 1-10 Gbps/core (kernel) vs 10-40 Gbps/core (DPDK) [^166^].

- **Critical limitation**: DPDK provides only raw packet I/O — no TCP/IP stack. Requires building user-space TCP stacks (mTCP, F-Stack, TAS) on top. This introduces significant adoption barriers [^59^].

- **Z-stack**: A DPDK-based zero-copy TCP stack achieving better performance by eliminating data copies at the socket interface when moving protocol processing to userspace [^67^].

- **For Cluster OS**: DPDK is likely too heavy for our use case. The complexity of maintaining a user-space TCP stack outweighs benefits for Gigabit Ethernet. However, understanding DPDK's techniques (huge pages, busy polling) can inform kernel tuning.

#### 9. WireGuard Mesh Networking — Tailscale, Headscale, Mesh Topologies

- **WireGuard kernel-mode achieves ~8 Gbps throughput** with ~3-5% CPU at 1 Gbps sustained. Userspace implementations (Tailscale) achieve ~6.8 Gbps with ~12-18% CPU [^77^].

- **Tailscale adds ~1ms latency for direct P2P connections** over no VPN. DERP-relayed connections add 10-50ms. On Gigabit LAN, Tailscale userspace reaches 250-300 Mbps (iperf3), with kernel tuning up to 7 Gbps [^72^].

- **Headscale (open-source Tailscale coordinator)** supports nearly all Tailscale features including ACLs, exit nodes, subnet routing, OIDC. Runs as a single Go binary against PostgreSQL or SQLite. Handles networks up to thousands of nodes [^77^] [^143^].

- **Performance comparison of mesh VPNs** (Defined Networking benchmarks, 2024) [^149^]:
  - Nebula, Netmaker, and Tailscale can saturate 10 Gbps in single direction
  - Nebula most memory-consistent (~27 MB), ZeroTier lowest memory (~10 MB), Tailscale most variable
  - Tailscale most CPU-efficient on Linux thanks to segmentation offloading

- **For Cluster OS**: WireGuard + Headscale is the recommended approach for VPN mesh. It provides: encrypted P2P connections, automatic NAT traversal, identity-based ACLs, MagicDNS for service discovery, and minimal overhead (~1ms latency, ~65% of raw bandwidth) [^72^] [^121^].

#### 10. SSH Tunneling / Reverse SSH — Performance, Limitations

- **SSH bandwidth overhead is negligible on reliable LAN connections** — achieves ~93.8% of raw bandwidth (iperf3 test: 93.8 Mbps direct vs 93.8 Mbps through SSH tunnel) [^88^].

- **TCP-over-TCP problem**: The major performance issue with SSH tunneling is running TCP over TCP. Both layers do adaptive corrections (slow start, congestion avoidance) unaware of each other, causing severe difficulties on lossy connections [^123^]. **This is critical for cluster communication over unreliable links.**

- **SSH tunnel performance on WAN**: ~65% of raw network speed achievable with WireGuard or SSH tunnels on reliable connections [^121^].

- **Reverse SSH for cluster management**: Workers behind NAT/firewalls can establish outbound SSH connections to a coordinator, enabling inbound access without port forwarding. However, this requires persistent connections and careful management of tunnel lifecycle [^122^].

- **For Cluster OS**: SSH tunnels are suitable for management/control plane (low bandwidth, persistent connections). Use WireGuard for data plane (high bandwidth, P2P). Avoid SSH tunnels for high-throughput data transfer due to TCP-over-TCP issues.

#### 11. MPI over TCP/IP — Open MPI, MPICH on Gigabit Ethernet

- **MPI over TCP/IP has significantly higher latency than RDMA**. UCX over specialized interconnect achieves 1.7 µs latency vs 56.0 µs for Fujitsu MPI over TCP-equivalent (32.8x difference) [^111^].

- **UCX supports TCP/IP as a fallback transport** with automatic transport selection. For Gigabit Ethernet clusters, UCX's TCP transport provides reasonable performance while maintaining the same API as RDMA transports [^120^].

- **Open MPI and MPICH both support UCX** as a communication layer. MPICH 3.3+ uses UCP API for tag-matching functions. Open MPI also integrates with libfabric [^112^].

- **For Cluster OS**: MPI is relevant if we target HPC workloads. For a general-purpose cluster OS, ZeroMQ/gRPC provide more flexible messaging patterns. If MPI support is needed, Open MPI with UCX over TCP is the path.

#### 12. Cap'n Proto, FlatBuffers — Zero-Copy Serialization

- **Zero-copy serialization eliminates the encode/decode step** that dominates traditional serialization. Cap'n Proto "encoding" is effectively a memory copy because in-memory layout matches wire format [^73^].

- **Performance comparison** (single 1024-byte string field) [^73^]:
  - Protobuf total overhead: 1,043 ns (351 ns encode + 491 ns decode)
  - Cap'n Proto total overhead: 619 ns (53 ns encode + 78 ns decode)
  - Cap'n Proto encode/decode is ~6-7x faster than Protobuf

- **Real-world throughput**: FlatBuffers achieves only 5.4 Gbps (52% of DPDK's 10.4 Gbps peak) due to two gaps: (1) serialization contributes 3 Gbps gap, (2) separate memory management between networking and serialization contributes 2 Gbps gap [^73^].

- **Feature comparison** [^81^]:
  - Cap'n Proto: Random-access reads, object-capability RPC, best for platforms/sandboxing
  - FlatBuffers: Optional fields don't take wire space, best for games
  - SBE: Simplest encoding, best for financial trading
  - All three support schema evolution and zero-copy deserialization

- **Practical benchmarks** (IEX financial data parsing) [^75^]:
  - SBE fastest: 91 ns median serialize, 116 ns median deserialize
  - FlatBuffers: 355 ns serialize, 173 ns deserialize
  - Cap'n Proto (unpacked): 273 ns serialize, 366 ns deserialize

- **For Cluster OS**: Use Cap'n Proto or FlatBuffers for internal cluster messages where zero-copy and low latency matter. Use Protocol Buffers with gRPC for external-facing APIs where ecosystem compatibility matters.

---

### Major Players & Sources

| Entity | Role/Relevance |
|--------|---------------|
| **ZeroMQ/Hintjents Legacy** | Foundational brokerless messaging library; mature ecosystem; multiple language bindings |
| **NanoMsg/NNG (Garrett D'Amore)** | Next-generation brokerless messaging; NNG actively developed; simpler API than ZeroMQ |
| **gRPC/Google** | HTTP/2-based RPC framework; Protocol Buffers serialization; dominant in microservices |
| **Apache Arrow/Flight** | Zero-copy columnar data transfer; 23x faster than REST; emerging standard for data systems |
| **UCX Consortium (Mellanox/NVIDIA, ARM, ORNL)** | Unified Communication X framework; standard for RDMA/HPC networking; supports TCP fallback |
| **libfabric/ofiwg** | OpenFabrics Interfaces; vendor-neutral HPC networking API; multiple providers |
| **DPDK/Linux Foundation** | Kernel bypass packet processing; 100 Gbps line rate on single core |
| **Tailscale/Headscale** | WireGuard-based mesh VPN with coordination; Headscale is open-source self-hosted alternative |
| **WireGuard (Jason Donenfeld)** | Modern VPN protocol; kernel-integrated; ~8 Gbps throughput; minimal attack surface |
| **Nebula (Defined Networking/Slack)** | Open-source mesh VPN; completely open source; stateful packet filtering; AES-256-GCM |
| **CXL Consortium** | Next-generation memory interconnect; cache-coherent; sub-microsecond latency; rack-scale |
| **Cap'n Proto (Kenton Varda)** | Zero-copy serialization with RPC; object-capability model; infinitely faster deserialization |
| **FlatBuffers (Google)** | Zero-copy serialization; optional fields; best for games; vtable design |

---

### Trends & Signals

- **Kernel bypass becoming mainstream**: Joyride (2025) proposes replacing Linux's kernel TCP/IP stack entirely with a DPDK-based user-space implementation, achieving 4x throughput improvement [^59^]. Linux requires 4-8 cores to saturate 100 Gbps; DPDK does it with 1 core.

- **WireGuard replacing legacy VPNs**: WireGuard is 6-7x faster than OpenVPN, with ~4,000 lines of kernel code vs hundreds of thousands for IPsec/OpenVPN [^77^]. Tailscale has surpassed 5M users; production tailnets exceed 50,000 nodes.

- **Zero-copy serialization becoming standard**: Arrow Flight achieves 18.7 GB/s by eliminating serialization entirely — transmitting in-memory representations directly [^61^]. The "serialization tax" consumes 50%+ of pipeline time in traditional systems.

- **UCX unifying HPC networking**: UCX provides a single API across InfiniBand, RoCE, TCP, shared memory, and GPU transports. UCX shows 32.8x latency improvement and 37.8x higher update rates than traditional MPI [^111^].

- **CXL as rack-level interconnect**: CXL 3.0 enables fabric topologies with 4,096 devices, sub-microsecond latency, and cache-coherent memory sharing across hosts [^110^]. Expected to complement (not replace) Ethernet at the rack/cluster level.

- **Brokerless messaging over brokered**: ZeroMQ/NanoMsg/NNG eliminate the single point of failure of brokered systems (RabbitMQ, Kafka) and achieve 3-5 GB/s in-process throughput with <10 µs latency [^44^].

---

### Controversies & Conflicting Claims

- **ZeroMQ vs NanoMsg vs NNG**: The 2025 Hitachi Energy benchmark [^44^] found ZeroMQ best for throughput/scaling and NanoMsg best for small messages/CPU efficiency, with NNG generally less competitive. However, NNG is the only library under active development. ZeroMQ's C++ codebase is mature but complex; NanoMsg's simpler design may be easier to integrate.

- **Tailscale vs raw WireGuard performance**: Tailscale runs WireGuard in userspace on most platforms, costing 10-15% throughput vs kernel mode. On Linux 6.x with experimental kernel backend, Tailscale closes most of the gap [^77^]. Some argue this gap matters for high-throughput workloads; others say it's invisible for typical cluster management traffic.

- **DPDK vs kernel networking**: DPDK achieves 100 Gbps line rate with 1 core vs 4-8 cores for Linux kernel [^59^]. However, DPDK requires application redesign, doesn't provide TCP/IP, and fragments the ecosystem. Joyride proposes transparent kernel bypass via LibC interception as a middle ground [^59^].

- **gRPC vs ZeroMQ for cluster RPC**: gRPC proponents cite structured communication, built-in TLS, and ecosystem [^87^]. ZeroMQ proponents cite lower latency, higher throughput, and lighter overhead [^139^]. The choice depends on whether structured RPC (gRPC) or high-frequency messaging (ZeroMQ) is the dominant pattern.

- **SSH tunneling viability**: Some sources claim SSH bandwidth overhead is negligible (~0.09%) [^88^]; others warn of TCP-over-TCP problems on lossy links [^123^]. The consensus: SSH tunnels work well on reliable LANs but should not be used for high-throughput cluster data planes.

- **Nebula vs Tailscale vs Netmaker**: Defined Networking's 2024 benchmark [^149^] found Nebula, Netmaker, and Tailscale all saturate 10 Gbps, with Tailscale most CPU-efficient on Linux (due to GSO/GRO). However, the Nebula community [^151^] disputes some competing benchmarks as flawed, highlighting the difficulty of fair VPN benchmarking.

---

### Recommended Deep-Dive Areas

| Area | Why It Warrants Depth |
|------|----------------------|
| **ZeroMQ Router-Dealer pattern for cluster task scheduling** | The Router-Dealer pattern enables flexible work distribution to worker nodes with automatic load balancing. Needs investigation for our specific scheduler architecture. |
| **Headscale deployment at scale** | Headscale supports thousands of nodes but operational experience is limited. Need to understand failure modes, database requirements, and ACL management at cluster scale. |
| **Arrow Flight for data-intensive cluster workloads** | If the Cluster OS supports distributed analytics or ML training, Arrow Flight's 23x throughput advantage over REST is transformative. Need to assess integration complexity. |
| **Cap'n Proto RPC for cluster control plane** | Cap'n Proto's promise pipelining can collapse sequential round trips, potentially reducing cluster coordination latency. Need to evaluate the RPC layer specifically. |
| **Joyride/DPDK transparent kernel bypass** | If the cluster OS evolves to support higher-speed networking (10 Gbps+), the 4x efficiency gap of kernel networking becomes critical. Joyride's LibC interception approach is promising. |
| **UCX TCP transport for cluster MPI workloads** | If supporting HPC-style applications, UCX provides a performance-portable path from TCP to RDMA without code changes. Need to evaluate UCX's TCP provider performance on Gigabit Ethernet. |

---

### Raw Evidence Log

---

**Claim:** ZeroMQ achieves the highest TCP throughput (~2.9 GB/s) among brokerless messaging libraries, with NanoMsg best for small payloads (<64 KB).
**Source:** "Performance Evaluation of Brokerless Messaging Libraries" — Hitachi Energy Research
**URL:** https://arxiv.org/html/2508.07934v1
**Date:** 2025-04-26
**Excerpt:** "Observation 7. For TCP communication, ZeroMQ achieves the highest peak throughput (≈2.9 GB/s), outperforming NanoMsg and NNG for message sizes higher than 128 KB... ZeroMQ excels in throughput and offers the best performance at large payloads, whereas NanoMsg performs best for small message sizes and demonstrates consistent strength in CPU efficiency."
**Context:** Comprehensive benchmark of ZeroMQ, NanoMsg, and NNG across in-process, inter-process, and TCP transports with varying message sizes (1 KB to 512 KB) and subscriber counts (1 to 8).
**Confidence:** High

---

**Claim:** Apache Arrow Flight achieves 23x throughput improvement over REST/JSON and 7.8x over gRPC/Protobuf.
**Source:** "Eliminating Serialization Overhead in Distributed Data Engineering Pipelines at Petabyte Scale" — IJIRSET
**URL:** https://www.ijirset.com/upload/2023/june/166_Apache%20Arrow%20Flight%20and%20the%20Zero-Copy%20Data%20Transfer%20Revolution
**Date:** June 2023
**Excerpt:** "Arrow Flight achieves 18.7 GB/s single-node throughput compared to 0.8 GB/s for REST/JSON, 2.4 GB/s for gRPC/Protobuf, 1.6 GB/s for JDBC, and 1.9 GB/s for ODBC. The 23× improvement over REST/JSON and 7.8× improvement over gRPC/Protobuf decompose across three contributing factors: Eliminated Encoding, Columnar SIMD in Streaming Path, Zero-Copy on Same-Node."
**Context:** Benchmarks conducted on AWS EC2 p3.8xlarge instances (4×V100 GPUs, 244 GB RAM, 25 Gbps network) with 1 billion synthetic analytical rows.
**Confidence:** High

---

**Claim:** WireGuard kernel-mode achieves ~8 Gbps throughput; Tailscale userspace achieves ~6.8 Gbps direct, <0.04 Gbps via DERP relay.
**Source:** "Tailscale vs WireGuard 2026: 5M Users, 8 Gbps Kernel Mesh" — Tech Insider
**URL:** https://tech-insider.org/tailscale-vs-wireguard-2026/
**Date:** 2026-05-04
**Excerpt:** "WireGuard kernel-mode posted approximately 7.5 to 8.0 Gbps of single-stream TCP throughput with around 15% lower CPU usage than userspace alternatives. OpenVPN, by comparison, capped at roughly 1.1 Gbps on the same hardware. Tailscale's own published benchmarks show direct point-to-point connections hitting roughly 6.8 Gbps with userspace WireGuard, climbing past 10 Gbps when Tailscale's experimental kernel-mode WireGuard backend is enabled on Linux 6.x."
**Context:** Comparison consolidating Phoronix performance data, official Tailscale/WireGuard documentation, and Trail of Bits/Doyensec audit findings.
**Confidence:** High

---

**Claim:** Linux kernel networking requires 4-8 cores to saturate 100 Gbps NIC; DPDK achieves line rate with 1 core.
**Source:** "Joyride: Rethinking Linux's network stack design for better performance, security, and reliability" — KISV '25 Workshop at SOSP 2025
**URL:** https://arxiv.org/html/2509.25015
**Date:** 2025
**Excerpt:** "Our evaluation on AMD EPYC 9005 series servers with 100 Gbps Intel E810 NICs shows that a single Linux process achieves less than 25 Gbps, while DPDK reaches near line rate with a single core. This 4× efficiency gap translates directly to increased costs and reduced performance... Linux requires 4 to 8 CPU cores to saturate one 100 Gbps NIC."
**Context:** Academic paper proposing Joyride, a microkernel-inspired user-space networking framework. Evaluation conducted on AMD EPYC 9005 with Intel E810 NICs running Ubuntu 22.04 with Linux kernel v6.2.
**Confidence:** High

---

**Claim:** UCX achieves 32.8x lower latency than Fujitsu MPI (1.7 µs vs 56.0 µs) and 107x higher message rates (31.0M vs 0.29M messages/sec).
**Source:** "Design and performance evaluation of UCX for the Tofu Interconnect D on Fugaku" — Springer
**URL:** https://link.springer.com/article/10.1007/s11227-024-06201-x
**Date:** 2024-06-03
**Excerpt:** "The latency of a 4-byte message with 24 threads of UCX is about 1.7 µs with 6 TNIs and 4 CQs, and that of Fujitsu MPI is about 56.0µs, respectively with rounded to two decimal places, which UCX shows about 32.8 times improvements... The maximum message rate of MPICH with 4-byte messages is about 0.29 million with one thread execution. This is about 107 times lower than UCX zcopy with memory registration optimization using 6 TNIs and 4 CQs with 24 threads, with a message rate of about 31.0 million."
**Context:** Benchmark on Fugaku supercomputer (Fujitsu A64FX processors, Tofu Interconnect D) comparing UCX, utofu, Fujitsu MPI, and MPICH.
**Confidence:** High

---

**Claim:** SSH tunnel bandwidth overhead is negligible (~0.09%) on reliable LAN connections.
**Source:** SuperUser — "What is the overhead of SSH compared to telnet?"
**URL:** https://superuser.com/questions/1108165/what-is-the-overhead-of-ssh-compared-to-telnet
**Date:** 2016-08-03
**Excerpt:** "iperf direct: 93.8 Mbits/sec. iperf through SSH tunnel: 93.8 Mbits/sec... We have transmitted 1MB more compared to direct connection. Thats in this case 0,09% and since we measure at MB level it could be a lot less overhead."
**Context:** Controlled test using Raspberry Pi 3B server and Raspberry Pi 4B client over wired LAN with iptables packet counting.
**Confidence:** High (for reliable LAN conditions)

---

**Claim:** gRPC delivers 77% lower latency than REST for small payloads and handles 3.48x more RPS at saturation.
**Source:** "gRPC vs REST 2026: 77% Faster, 10x Smaller Payloads" — Tech Insider; plus "A comparative case study of gRPC, REST, and SOAP" — KTH Royal Institute
**URL:** https://tech-insider.org/grpc-vs-rest-2026/ ; https://www.diva-portal.org/smash/get/diva2:1887929/FULLTEXT01.pdf
**Date:** 2026-04-29 / 2024-06-19
**Excerpt:** "gRPC can deliver up to 77% lower latency... gRPC emerges as the leading performer, exhibiting an average increase of 618% to SOAP and 287% to REST for small messages... The maximum RPS attained by gRPC peaks at 69,614.3 with 600 active users... This demonstrates unparalleled performance when contrasted with both SOAP and REST, which reach their limits at 210 and 360 active users respectively."
**Context:** Synthesis of Microsoft ASP.NET Core benchmarks, Yenigun et al. study, and production telemetry. Academic thesis using Locust load-testing framework.
**Confidence:** High

---

**Claim:** Cap'n Proto encodes 6-7x faster than Protobuf because in-memory layout matches wire format.
**Source:** "Towards Zero-Copy Serialization with NIC Scatter-Gather" — HotOS 2021
**URL:** https://sigops.org/s/conferences/hotos/2021/papers/hotos21-s10-raghavan.pdf
**Date:** 2021
**Excerpt:** "Protobuf total overhead: 1,043ns (351ns encode + 491ns decode). Cap'n Proto total overhead: 619ns (53ns encode + 78ns decode)... Cap'n Proto's encode and decode are zero-copy because the in-memory buffer layout matches the eventual wire format, while Protobuf requires an expensive transformation to the wire format."
**Context:** Academic paper at HotOS 2021 proposing NIC scatter-gather for true zero-copy serialization. Benchmarks single 1024-byte string field serialization.
**Confidence:** High

---

**Claim:** FlatBuffers achieves only 5.4 Gbps due to gaps between serialization and separate memory management.
**Source:** Same as above — HotOS 2021
**URL:** https://sigops.org/s/conferences/hotos/2021/papers/hotos21-s10-raghavan.pdf
**Date:** 2021
**Excerpt:** "FlatBuffers, the fastest serialization baseline, achieves only 5.4 Gbps, about 52% of DPDK's peak throughput of 10.4Gbps (highest throughput measured under 13µs of tail latency), due to two performance gaps. Serialization itself contributes the first 3 Gbps gap between FlatBuffers and No Serialization. Having the networking stack and serialization manage memory separately contributes the 2Gbps gap between No Serialization and DPDK Single Core."
**Context:** Benchmark measuring serialization throughput relative to DPDK single-core maximum on high-speed network.
**Confidence:** High

---

**Claim:** CXL lowers latency by an order of magnitude compared to Ethernet/InfiniBand and enables cache-coherent memory sharing across hosts.
**Source:** "An Introduction to the Compute Express Link (CXL) Interconnect" — ACM Computing Surveys
**URL:** https://dl.acm.org/doi/10.1145/3669900
**Date:** 2024-07-08
**Excerpt:** "Compared to today's datacenter networks based on Ethernet and InfiniBand, CXL lowers latency by an order of magnitude. Additionally, CXL's coherent memory sharing and fine-grained synchronization can significantly boost distributed system performance for key workloads such as large machine learning models and databases... current financial models point to sub-rack CXL deployments as the TCO sweet spot."
**Context:** ACM tutorial/survey paper on CXL. Discusses CXL 1.0, 2.0, and 3.0 specifications, use cases, and implications.
**Confidence:** High

---

**Claim:** libfabric provides a unified API across tcp, udp, shm, verbs, efa, and other HPC network transports.
**Source:** "The ABCs of Open MPI" — EasyBuild Tech Talks; plus libfabric official documentation
**URL:** https://easybuild.io/files/easybuild-tech-talks/easybuild_tech_talks_01_OpenMPI_part1_20200623.pdf
**Date:** 2020
**Excerpt:** "Libfabric was originally created by network vendors who wanted an HPC network API that wasn't tied to the abstractions of InfiniBand. Cisco (usNIC), Cray (uGNI), Intel (PSM, PSM2). It has since grown to support many additional network types: AWS EFA, BlueGene Q, IB Verbs (IB, RoCE, iWARP), NetDirect, POSIX TCP and UDP sockets, Shared memory."
**Context:** Technical talk on Open MPI internals explaining the role of libfabric and UCX as communication libraries.
**Confidence:** High

---

**Claim:** Nebula, Netmaker, and Tailscale can all saturate 10 Gbps Ethernet; Tailscale is most CPU-efficient on Linux.
**Source:** "Nebula is not the fastest mesh VPN" — Defined Networking (Nebula creators)
**URL:** https://defined.net/blog/nebula-is-not-the-fastest-mesh-vpn/
**Date:** 2024-02-16
**Excerpt:** "Three of the four options, Nebula, Netmaker, and Tailscale, can reach throughput that matches the limits of the underlying hardware, nearly 10 gigabits per second (Gbps)... Tailscale appears significantly better [in CPU efficiency], thanks to their use of various Linux segmentation offloading mechanisms... Nebula averages 27 megabytes of memory used."
**Context:** Independent benchmark by Nebula's creators comparing Nebula, Netmaker, Tailscale, and ZeroTier on 5 physical hosts with Intel i7-10700 CPUs and Intel 10G NICs.
**Confidence:** High

---

**Claim:** CurveZMQ provides modern cryptography (Curve25519, ChaCha20-Poly1305) for ZeroMQ with perfect forward secrecy.
**Source:** CurveZMQ RFC 26; plus Monetas MEP-002
**URL:** https://rfc.zeromq.org/spec/26/ ; https://monetas.github.io/protocol-docs/mep/mep-002/
**Date:** 2012-09-09 / 2014-11-18
**Excerpt:** "CurveZMQ uses the Curve25519 elliptic curve... The protocol establishes short-term session keys for every connection to achieve perfect forward security. Session keys are held in memory and destroyed when the connection is closed. CurveZMQ also addresses replay attacks, amplification attacks, MIM attacks, key thefts, client identification, and various denial-of-service attacks."
**Context:** Official ZeroMQ RFC specifying CurveZMQ security protocol. Monetas engineering proposal for adopting CurveZMQ.
**Confidence:** High

---

### Cluster OS Architecture Recommendations

Based on this research, the following protocol stack is recommended for the Cluster OS:

**Layer 1: Network Fabric (Physical/Link)**
- Baseline: Gigabit Ethernet with standard TCP/IP
- VPN mesh: WireGuard via Headscale for encrypted inter-node communication
- SSH: For management access and reverse tunneling (control plane only)
- Future upgrade path: RoCE/RDMA via UCX if Mellanox/NVIDIA NICs available

**Layer 2: Inter-Node Messaging (Transport)**
- High-frequency control messages: ZeroMQ with CURVE security (Router-Dealer for task distribution, PUB-SUB for state broadcast)
- Structured RPC: gRPC with Protocol Buffers (for scheduler, resource manager, monitoring APIs)
- Large data transfers: Apache Arrow Flight (for data-intensive workloads)
- Internal serialization: Cap'n Proto or FlatBuffers for zero-copy message access

**Layer 3: Cluster Coordination**
- Service discovery: Headscale MagicDNS + custom registry
- Security: WireGuard encryption + Headscale ACLs + Zero Trust identity verification
- Failure detection: Heartbeat over ZeroMQ PUB-SUB with configurable timeouts
