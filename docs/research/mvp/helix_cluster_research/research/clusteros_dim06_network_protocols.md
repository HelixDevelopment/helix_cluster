## Facet: Inter-Node Communication & Network Protocol Stack

### Key Findings

#### ZeroMQ Messaging Patterns & Performance
- **ZeroMQ core patterns**: REQ/REP (synchronous RPC), PUB/SUB (broadcast, 1:N fan-out), PUSH/PULL (pipeline load-balancing, fan-out/fan-in), ROUTER/DEALER (async request-reply with envelope routing), PAIR (inter-thread 1:1) [^186^]
- ZMTP (ZeroMQ Message Transport Protocol) framing: length-prefixed frames with flags field for multipart messages, sitting atop TCP as a message-oriented layer [^635^] [^637^]
- ZeroMQ uses its own frame format incompatible with existing protocols like HTTP — length-specified frames rather than text-based delimiters [^186^]
- PAIR sockets are explicitly **not suitable for network use** — they are designed for inter-thread signaling only [^186^]
- ROUTER/DEALER enable scalable request-reply topologies without clients knowing all workers [^493^]
- **10GbE limitation**: ZeroMQ could only saturate ~20% of 10GbE (2 Gbps) for messages under 1024 bytes in early versions [^613^]
- FairMQ Push-Pull achieves 2.6-4.8 GByte/s throughput for 10MB messages on test clusters [^492^]
- Latency for 1-byte messages on 1GbE: ~40 microseconds (25us network stack + 15us ZeroMQ overhead) [^622^]

#### gRPC: HTTP/2 Streaming, Flow Control & Load Balancing
- gRPC leverages HTTP/2 for: multiplexed streams, flow control, header compression, binary framing, and bidirectional streaming [^487^]
- gRPC health checking: unary `Check` for polling, `Watch` for real-time streaming health status updates [^486^]
- Layer 7 load balancing is essential — `kube-proxy` (L4) cannot properly balance gRPC streams [^487^]
- gRPC performance: 15,000-50,000 req/s throughput, 5-20ms p50 latency in microservice environments [^89^]
- **Streaming vs Unary**: Total transfer time is nearly identical (~720ms), but streaming delivers first byte in ~8ms vs 680ms for unary — critical for user experience [^609^]
- Streaming reduces peak server memory dramatically: ~3MB vs ~42MB for 10,000 records [^609^]
- C++ gRPC: Use callback API for most RPCs, async completion-queue API with `numcpu` threads for high QPS [^618^]
- Python streaming RPCs are **slower than unary** due to extra thread creation overhead [^618^]
- Bidirectional streaming requires producer/consumer queue (`Channel<T>`) for multi-threaded writes [^620^]

#### Apache Arrow Flight: Zero-Copy Data Transfer
- Arrow Flight achieves up to **6,000 MB/s (DoGet) and 4,800 MB/s (DoPut)** on InfiniBand networks [^64^]
- Flight reaches **95% of RDMA bandwidth** on InfiniBand (5.9 GB/s vs 6.2 GB/s RDMA theoretical max of 7 GB/s) [^629^] [^631^]
- On Mellanox ConnectX-3/Connect-IB interconnects, Flight utilizes up to 95% of total available bandwidth [^64^]
- Arrow Flight performs **20x better than turbodbc** and **30x better than ODBC** for Dremio queries [^64^]
- Single-node throughput: up to 10K MB/s localhost; 1.65 GB/s DoPut / 2 GB/s DoGet remote over InfiniBand [^631^]
- Flight achieves **18.7 GB/s single-node throughput** vs 0.8 GB/s REST/JSON, 2.4 GB/s gRPC/Protobuf, 1.6 GB/s JDBC, 1.9 GB/s ODBC [^61^]
- **23x throughput improvement** over REST/JSON and **26x end-to-end latency reduction** [^61^]
- Flight eliminates serialization overhead by transmitting Arrow's in-memory representation directly via gRPC streaming [^490^]
- Arrow Flight v2 with RDMA InfiniBand: tick-to-trade latency reduced from 180ms to 3.2ms (93% reduction) [^61^]

#### Cap'n Proto: Zero-Copy Serialization
- Cap'n Proto was built by **Kenton Varda**, the engineer who designed Protocol Buffers v2 at Google [^74^]
- Core claim: **"infinity times faster than Protobuf"** — the serialization step literally does not exist; no encode, no decode [^612^]
- Cap'n Proto encoding is designed to be both wire format AND in-memory representation — data is arranged like a compiler would arrange a struct with fixed widths, fixed offsets, and proper alignment [^612^]
- Variable-sized elements embedded as offset-based pointers for position independence; integers use little-endian [^612^]
- Backward compatibility: new fields added at end of struct, existing positions unchanged; recipient does bounds check per field [^612^]
- "Packing" compression achieves similar (better) sizes than Protobuf while remaining faster [^612^]
- **Best for**: high-frequency data pipelines, shared-memory IPC, latency-critical RPC chains, memory-mapped data stores [^74^]
- Cap'n Proto RPC supports **promise pipelining** — collapse 5 sequential round trips into 1 [^74^]
- For most web services, Protobuf/gRPC ecosystem breadth outweighs Cap'n Proto's raw speed advantage [^74^]

#### FlatBuffers: Zero-Copy Deserialization
- FlatBuffers, also from Google, allows **direct access to serialized data without parsing** — zero-copy deserialization [^574^]
- FlatBuffers had the **best deserialization performance** in Java JMH benchmarks vs JSON and Protobuf [^574^]
- Protobuf had the **smallest serialized size**, making it ideal for network efficiency [^574^]
- FlatBuffers generates **larger binary files** than Protobuf due to additional metadata [^574^]
- More complex API compared to JSON and Protobuf; not as widely supported [^574^]
- Best for: real-time systems, gaming (AWS GameLift), IoT edge (AWS Greengrass), Lambda@Edge [^569^]

#### Protocol Buffers: Google Serialization
- Protobuf reduces message sizes by up to **70%** and halves CPU parsing time vs JSON [^89^]
- Protobuf is fastest in Go benchmarks: ~6,500 ns/op encode vs ~42,000 ns/op JSON (6.5x faster) [^566^]
- Same object: Protobuf ~13 bytes vs MessagePack ~29 bytes vs JSON ~53 bytes [^564^]
- Protobuf's pre-compiled schema means no reflection overhead; extremely predictable p99 performance [^566^]
- Strong cross-language support via `.proto` files with `protoc` code generation [^569^]

#### MessagePack: Binary JSON
- MessagePack is **20-40% smaller than JSON** and faster to encode/decode [^564^]
- In Go: ~12,000 ns/op encode (3.5x faster than JSON), ~19,000 ns/op decode (3.5x faster than JSON) [^566^]
- Drop-in JSON replacement: schema-optional, no `.proto` files needed, simple transition [^566^]
- MessagePack is the **best practical upgrade** from JSON without rewriting architecture [^566^]
- Field names still embedded in payload (unlike Protobuf which uses field numbers only) [^564^]

#### WireGuard: Kernel vs Userspace Performance
- WireGuard kernel module: **~8 Gbps** single-stream TCP throughput on 10GbE [^77^]
- WireGuard kernel: **<0.5 ms** added one-way latency vs LAN [^77^]
- Tailscale userspace WireGuard: **~6.8 Gbps** direct, ~35 Mbps via DERP relay [^77^]
- WireGuard kernel uses **~3-5% CPU at 1 Gbps sustained** vs **~12-18% for userspace** [^77^]
- WireGuard is **5-10x faster** than userspace VPNs (OpenVPN capped at ~1.1 Gbps) [^77^]
- IPsec via strongSwan reached 6.8 Gbps but consumed ~30% more CPU than WireGuard [^77^]
- WireGuard attack surface: ~4,000 lines of kernel code vs hundreds of thousands for OpenVPN/IPsec [^77^]
- WireGuard cryptography: Curve25519, ChaCha20-Poly1305, BLAKE2s, HKDF [^77^]
- Linux kernel 5.6+ includes WireGuard upstream (CONFIG_WIREGUARD) [^522^]

#### Tailscale/Headscale: Mesh VPN Coordination
- All mesh VPNs (Tailscale, NetBird, Headscale) share WireGuard as the **data plane** — raw throughput is identical [^518^]
- Tailscale: closed-source coordination server, 100-device free tier, ~200 DERP relays globally [^77^] [^84^]
- Headscale: **open-source (BSD-3-Clause)** reimplementation of Tailscale control server, unlimited users/devices [^84^]
- Headscale runs on a **$4/month VPS** with 1GB RAM for hundreds of nodes [^518^]
- DERP relay fallback: **5-20ms additional latency** depending on region; ~35 Mbps throughput [^77^] [^519^]
- Direct peer connections establish in **1-3 seconds** with ~95% success rate [^77^]
- Tailscale's experimental kernel-mode backend on Linux 6.x exceeds 10 Gbps [^77^]
- DERP protocol: forwards WireGuard-encrypted packets over HTTPS/TCP 443 — relay never sees plaintext [^519^]

#### SSH Protocol Internals
- SSH protocol uses **channels** to multiplex logical sub-connections over a single TCP connection [^591^]
- Channel types: `session` (shell, SFTP, exec), `direct-tcpip` (local forwarding), `forwarded-tcpip` (remote forwarding), `x11` [^592^] [^598^]
- Protocol messages: `SSH_MSG_CHANNEL_OPEN`, `SSH_MSG_CHANNEL_REQUEST` (shell, exec, pty-req), `SSH_MSG_GLOBAL_REQUEST` (tcpip-forward) [^588^] [^592^]
- **Local port forwarding (-L)**: client sends `direct-tcpip` channel open with target host/port [^588^]
- **Remote port forwarding (-R)**: client sends `tcpip-forward` global request; server sends `forwarded-tcpip` when connections arrive [^588^]
- SSH ControlPersist: subsequent connections reuse the master connection's TCP/SSH handshake, skipping to channel setup only [^520^]
- ControlPersist can yield **33% improvement** for first run, more for subsequent runs within persist window [^520^]
- **TCP-over-TCP problem**: tunneling TCP over SSH TCP causes two layers of congestion control that fight each other, especially on lossy links [^123^]
- SSH tunnels suitable for management/control plane only; avoid for data plane due to TCP-over-TCP meltdown [^123^]

#### TLS 1.3: Performance & 0-RTT
- TLS 1.3 reduces handshake from **2 RTT to 1 RTT** — ~50% reduction in handshake latency vs TLS 1.2 [^545^] [^550^]
- TLS 1.3 + 0-RTT: **resumed connections in 2 RTT + DNS** (vs 3 RTT for TLS 1.2 resumed) [^551^]
- Production data: **40% reduction in p95 latency**, 28% reduction in ALB CPU, 85% fewer full handshakes [^549^]
- TLS 1.3 removes 300+ cipher suites down to **5 secure defaults** — eliminates misconfigurations [^549^]
- Forward secrecy is **mandatory** in TLS 1.3 (optional in TLS 1.2) [^545^]
- Over 90% of HTTPS connections in major browsers use TLS 1.3 as of 2025 [^550^]
- 0-RTT CPU time: 2.33-2.62ms per resumed connection vs 0.76-1.33ms for TLS 1.2 resumed (includes forward secrecy) [^544^]
- 0-RTT security concern: replay attacks possible — avoid for state-changing operations [^546^] [^553^]

#### mTLS: Mutual TLS for Services
- mTLS adds client certificate verification in addition to server certificate [^596^]
- Baseline mTLS impact: **up to 3% P99 latency increase**, but **up to 100% CPU/memory increase** [^596^] [^600^]
- Service mesh mTLS overhead varies dramatically: Istio 166% latency increase, Linkerd 33%, Cilium 99% [^600^] [^601^]
- Istio Ambient (sidecarless/eBPF): only 8% latency increase — **20x better than Istio sidecar** [^601^]
- Throughput with Istio sidecar dropped 95% vs baseline; not from mTLS itself but HTTP parsing/metrics in sidecar [^596^]
- Certificate management (rotation, expiry) is the primary operational challenge [^587^]

#### QUIC Protocol & HTTP/3
- QUIC provides: 0-RTT/1-RTT connection setup, independent stream reliability, integrated TLS 1.3, connection migration [^555^]
- QUIC eliminates TCP head-of-line blocking: lost packet blocks only its stream, not all streams [^555^]
- HTTP/3 slower than HTTP/2 on local networks by 50-100x due to userspace UDP overhead vs kernel-optimized TCP [^547^]
- On high-bandwidth links, HTTP/3 suffered data rate reductions of **up to 45.2%** vs HTTP/2 [^547^]
- QUIC generates 231K `netif_receive_skb` calls vs 15K for HTTP/2 — every one crosses user-kernel boundary [^547^]
- HTTP/3 excels on: mobile networks with packet loss, high-latency connections, network transitions (WiFi->cellular) [^547^] [^548^]
- TCP has been optimized in kernel for decades with hardware offloading (TSO, GRO, zero-copy); QUIC cannot replicate these [^547^]

#### RDMA over Converged Ethernet (RoCE)
- RDMA excels with **kernel bypass**: direct data exchange between apps and NICs, protocol stack latency ~1 microsecond [^570^]
- RDMA zero-copy eliminates data shuffle between application memory and OS buffers — CPU not engaged [^570^]
- **Ultra Ethernet (UEC)**: ~1.9us latency at 800G vs InfiniBand ~1.2us at NDR 400G vs RoCE 5-6us vs TCP >15us [^567^]
- RDMA is **highly sensitive to packet loss**: loss rate above 0.001 drastically reduces throughput; at 0.01 throughput drops to zero [^570^]
- RoCE requires **lossless Ethernet** via PFC (Priority Flow Control) and ECN for congestion management [^570^]
- RoCE switches (Asterfusion CX-N) can deliver InfiniBand-class performance at significantly lower cost [^570^]

#### DPDK: Kernel Bypass
- DPDK enables line-rate packet processing up to **100 Gbps+** by bypassing kernel network stack [^563^]
- Key techniques: Poll Mode Drivers (PMD), hugepages memory, CPU core pinning [^563^] [^565^]
- DPDK vs kernel networking: ultra-low latency (microseconds vs higher), high CPU efficiency, line-rate throughput [^563^]
- Use cases: NVMe/TCP, distributed storage, NFV, AI/ML pipelines, cloud-native databases [^563^]
- DPDK has **steep learning curve** — P4-DPDK bridges P4 language expressiveness with DPDK performance [^572^]
- VPP (Vector Packet Processing) uses DPDK for L2 and implements L3-L7 in userspace for complete kernel bypass [^571^]

#### libfabric: Unified Communication Framework
- libfabric (OFI) provides abstract interface for networks: supports verbs, TCP, PSM2/PSM3, CXI (Cray Slingshot), EFA (AWS) [^589^]
- Intel MPI supports: mlx, tcp, psm2, psm3, sockets, verbs, RxM, efa providers [^597^]
- RxM provider: emulates RDM endpoints over MSG endpoints of core providers (RDM over TCP) [^589^]
- Used by: MPICH, OpenMPI, OpenSHMEM, Apache Spark, RAPIDS [^640^]
- **No UCX backend for Cray Slingshot** — libfabric is the interface of choice for HPE systems [^595^]
- The MLX provider runs over UCX for Mellanox InfiniBand hardware [^597^]

#### UCX: Unified Communication X
- UCX provides unified API abstracting multiple transports: TCP, InfiniBand verbs, Intel Omni-Path, Cray uGNI, GPU (CUDA/ROCm) [^640^] [^118^]
- Key feature: **transparently upgrades from TCP to RDMA** when available — no code changes needed [^118^]
- Supports tag-matched send/receive, RMA, atomic operations, GPU-aware communication [^118^]
- Used by: OpenMPI, MPICH, Charm++, Dask, NCCL, NVSHMEM [^640^] [^642^]
- UCX auto-detects available transports at runtime; silently disables unsupported modules [^640^]
- Cross-transport multi-rail: can use multiple transports/hardware types simultaneously [^640^]
- Collective operations over UCX can reduce latency by **8% for small messages, 90% for large messages** [^641^]

#### Custom Binary Protocol Design
- **Framing**: Length-prefixed framing is dominant (4-byte big-endian int32 prefix); used by gRPC/HTTP/2, Kafka [^630^]
- Alternatives: fixed-size (inflexible), delimiter-based (fragile if payload contains delimiter) [^630^]
- **Type encoding**: fixed-width little/big-endian, variable-length varints (LEB128/Protobuf style), length-prefixed strings [^630^]
- **Versioning**: magic number + version byte + message type byte in header; reserve message types for future [^630^]
- Using Protobuf/Thrift for schema-based encoding handles versioning automatically through field tagging [^630^]
- ZMTP framing: single octet length (1-254) or `%xFF` + 8-byte length for 255+, flags byte, body [^635^]

### Major Players & Sources

| Entity | Role/Relevance |
|--------|---------------|
| **ZeroMQ (iMatix/Community)** | High-performance messaging library; ZMTP wire protocol; core patterns for distributed systems |
| **gRPC (Google)** | HTTP/2-based RPC framework; protobuf serialization; streaming, load balancing, health checking |
| **Apache Arrow Flight** | Zero-copy columnar data transfer over gRPC; 95% of RDMA bandwidth; 20-30x faster than ODBC |
| **Cap'n Proto (Kenton Varda)** | Zero-copy serialization; "infinity times faster than Protobuf"; capability-based RPC |
| **FlatBuffers (Google)** | Zero-copy deserialization; gaming/IoT/real-time systems; cross-platform |
| **Protocol Buffers (Google)** | Binary serialization standard; 70% smaller than JSON; gRPC integration |
| **MessagePack** | Binary JSON; schema-optional; 3.5x faster than JSON |
| **WireGuard (Jason Donenfeld)** | Kernel VPN module; ~8 Gbps; ~4000 LOC; ChaCha20-Poly1305 |
| **Tailscale** | Managed mesh VPN; WireGuard data plane; DERP relays; closed-source coordination |
| **Headscale (juanfont)** | Open-source Tailscale control server reimplementation; BSD-3-Clause; self-hosted |
| **OpenSSH (OpenBSD)** | SSH protocol implementation; ControlMaster/ControlPersist; channel multiplexing |
| **OpenSSL/LibreSSL** | TLS 1.3 implementation; 0-RTT session resumption; cipher suite management |
| **Istio/Linkerd/Cilium** | Service meshes with mTLS; Linkerd 33% latency, Istio 166% latency overhead |
| **QUIC (IETF/Google)** | UDP-based transport; HTTP/3 foundation; connection migration; 0-RTT |
| **RoCE (IBTA)** | RDMA over Ethernet; requires lossless fabric (PFC/ECN); ~5-6us latency |
| **DPDK (Intel/Linux Foundation)** | Kernel bypass packet processing; 100 Gbps+; PMD + hugepages |
| **libfabric (OFIWG)** | Unified network abstraction; verbs, TCP, PSM, CXI providers; MPI integration |
| **UCX (OpenUCX)** | Unified communication framework; TCP to RDMA transparent upgrade; GPU-aware |
| **Phoronix** | Independent VPN/WireGuard benchmark source; 7.5-8 Gbps WireGuard kernel measurements |
| **CERN (FairMQ)** | ZeroMQ performance benchmarks at scale; Push-Pull 4.8 GB/s throughput |

### Trends & Signals

- **Zero-copy becoming standard**: Arrow Flight, Cap'n Proto, FlatBuffers, RDMA all converge on eliminating serialization overhead [^64^] [^74^] [^612^]
- **Arrow Flight replacing ODBC/JDBC**: 20-30x faster query result transfer; major databases adopting Arrow-native protocols [^64^] [^490^]
- **Mesh VPN replacing traditional VPN**: WireGuard-based mesh (Tailscale/Headscale/NetBird) offers 5-10x throughput of OpenVPN with zero config [^77^] [^521^]
- **Sidecarless service mesh**: Istio Ambient with eBPF reduces mTLS overhead from 166% to 8% — indicates L7 proxy overhead is the real cost, not mTLS itself [^601^]
- **QUIC userspace penalty**: TCP kernel optimizations (TSO, GRO, delayed ACKs, zero-copy) give HTTP/2 advantage over HTTP/3 on stable datacenter networks [^547^]
- **UCX as portability layer**: Frameworks built on UCX transparently use TCP today, RDMA tomorrow — eliminates NIC-specific code [^118^] [^640^]
- **DPDK for storage not just networking**: NVMe/TCP, distributed storage becoming primary DPDK use cases beyond telco/NFV [^563^]
- **Headscale gaining traction**: 25k GitHub stars (vs Tailscale 20k); self-hosted control without client changes [^623^] [^624^]
- **FlatBuffers/Cap'n Proto niche but growing**: High-frequency trading, gaming, real-time systems driving adoption over Protobuf [^74^] [^569^]
- **Protobuf remains default**: Despite speed disadvantages, ecosystem breadth, tooling, and gRPC integration keep Protobuf dominant [^74^] [^566^]

### Controversies & Conflicting Claims

1. **ZeroMQ vs raw TCP for small messages**: ZeroMQ's own 10GbE tests showed only 20% link utilization for sub-1024B messages [^613^]; yet CERN FairMQ achieves 4.8 GB/s for 10MB messages [^492^] — message size matters enormously

2. **Cap'n Proto "infinity times faster"**: Fair for in-memory encode/decode only; does not account for wire size (Cap'n Proto messages are larger), network transfer time, or real-world parsing costs [^612^] [^617^]. Kenton Varda himself notes: "any serialization format can 'win' given the right benchmark" [^617^]

3. **HTTP/3 faster or slower?**: HTTP/3 is 50-100x **slower** than HTTP/2 on local networks due to userspace UDP overhead [^547^], yet 10-20% faster on lossy mobile networks [^548^]. Protocol choice is network-dependent.

4. **mTLS performance cost**: Baseline mTLS adds only 3% latency [^596^], but Istio sidecar + mTLS adds 166% latency [^601^]. The mTLS protocol itself is not the bottleneck — L7 proxy overhead is. Istio Ambient (eBPF) reduces to 8%.

5. **WireGuard kernel vs userspace**: Kernel claims 8 Gbps [^77^], but one benchmark showed TLS (userspace) matching WireGuard latency while using less CPU per unit throughput [^524^]. WireGuard at 100% CPU was only 2x CPU overhead but 20x efficiency overhead vs direct.

6. **Arrow Flight vs RDMA**: Flight achieves 95% of RDMA bandwidth [^629^], but requires gRPC overhead. For pure HPC, RDMA (6.2 GB/s) still beats Flight (5.9 GB/s). Flight's advantage is programmability and security, not raw throughput.

7. **JSON vs binary serialization**: JSON is 3-7x slower [^566^], but "premature optimization is the root of all evil" — unless handling 1M req/s, convenience may outweigh speed [^573^]. Protobuf requires schema discipline that small teams may not want.

### Recommended Deep-Dive Areas

1. **Arrow Flight SQL + ADBC**: The database connectivity layer unifying Arrow access across heterogeneous systems; critical for next-gen data infrastructure

2. **Cap'n Proto RPC with promise pipelining**: Collapsing sequential round trips; underutilized in distributed systems design

3. **Istio Ambient / eBPF-based service mesh**: Sidecarless mTLS at only 8% latency overhead — could replace sidecar proxies entirely

4. **UCX multi-rail and GPU-aware communication**: Transparent transport selection, multi-NIC utilization, GPUDirect integration

5. **DPDK for NVMe/TCP storage**: Kernel bypass for distributed storage — practical deployment patterns, complexity tradeoffs

6. **Headscale at production scale**: Self-hosted Tailscale for 1000+ node clusters; DERP relay topology design

7. **QUIC for inter-datacenter communication**: When does UDP-based transport win over kernel TCP? Quantitative decision framework

8. **ZeroMQ ROUTER/DEALER for cluster orchestration**: Pattern design for reliable request-reply with service discovery

9. **RoCEv2 deployment on commodity Ethernet**: Lossless fabric configuration (PFC/ECN), congestion control tuning, practical pitfalls

10. **Custom binary protocols with Protobuf framing**: Leveraging Protobuf for schema evolution while optimizing for zero-copy access patterns

---

### Raw Evidence Log

#### Claim 1: ZeroMQ PUSH/PULL achieves 4.8 GB/s throughput
Source: CERN Indico (FairMQ benchmarks)
URL: https://indico.cern.ch/event/373630/contributions/883880/attachments/744204/1020872/cwg13_20.Feb15.pdf
Date: 2015-02-20
Excerpt: "Push-Pull pattern Message size= 10 Mbyte Throughput= 4,8 Gbyte/s"
Context: Tested on DAQ test cluster with aidrefma03→aidrefma01, 3(4) cores receiving data via Ethernet/IPoverIB
Confidence: High

#### Claim 2: Arrow Flight achieves 95% of RDMA bandwidth, 6000 MB/s DoGet
Source: TU Delft / ACM Benchmarking Apache Arrow Flight paper
URL: https://dl.acm.org/doi/fullHtml/10.1145/3527199.3527264
Date: 2022-04-02
Excerpt: "We show that Flight is able to achieve up to 6000 MB/s and 4800 MB/s throughput for DoGet() and DoPut() operations respectively. On Mellanox ConnectX-3 or Connect-IB interconnect nodes Flight can utilize upto 95% of the total available bandwidth."
Context: Benchmarked on dual-socket Intel Xeon with Mellanox ConnectX-3/Connect-IB InfiniBand (56 Gbit/s)
Confidence: High

#### Claim 3: Cap'n Proto "infinity times faster" than Protobuf at serialization
Source: Cap'n Proto official website
URL: https://capnproto.org/
Date: Ongoing
Excerpt: "In fact, in benchmarks, Cap'n Proto is INFINITY TIMES faster than Protocol Buffers. This benchmark is, of course, unfair. It is only measuring the time to encode and decode a message in memory. Cap'n Proto gets a perfect score because there is no encoding/decoding step."
Context: Author Kenton Varda also created Protobuf v2 at Google; caveat about benchmark fairness is explicit
Confidence: High (with stated caveats)

#### Claim 4: WireGuard kernel ~8 Gbps, Tailscale userspace ~6.8 Gbps
Source: Tech Insider Tailscale vs WireGuard 2026 comparison
URL: https://tech-insider.org/tailscale-vs-wireguard-2026/
Date: 2026-05-04
Excerpt: "WireGuard kernel-mode posted approximately 7.5 to 8.0 Gbps of single-stream TCP throughput with around 15% lower CPU usage than userspace alternatives. OpenVPN, by comparison, capped at roughly 1.1 Gbps on the same hardware."
Context: Phoronix Linux VPN review on AMD EPYC 9654 hardware; cited as most-cited independent VPN performance reference in 2025
Confidence: High

#### Claim 5: TLS 1.3 40% p95 latency reduction in production
Source: Dev.to production TLS 1.3 migration case study
URL: https://dev.to/sreekanth_kuruba_91721e5d/tls-12-vs-tls-13-in-production-2025-5c0e
Date: 2025-12-09
Excerpt: "p95 TTFB (global): TLS 1.2 318 ms → TLS 1.3 194 ms (–40%); Full handshakes: ~40% → <6% (–85%); ALB CPU: –28%; 0-RTT usage: 58%"
Context: Global traffic 300M+ requests/day, Cloudflare → ALB → Nginx stack
Confidence: High

#### Claim 6: gRPC streaming vs unary: same total time, 85x faster first byte
Source: mehdi.cz blog - gRPC Streaming vs Unary wire analysis
URL: https://www.mehdi.cz/blog/grpc-streaming-vs-unary
Date: 2026-02-24
Excerpt: "Unary: Peak server memory ~42 MB, Latency to first byte 680ms, Total transfer 720ms | Streaming: Peak server memory ~3 MB, Latency to first byte 8ms, Total transfer 740ms"
Context: Go gRPC server fetching 10,000 records; streaming in batches of 100
Confidence: High

#### Claim 7: mTLS baseline only 3% latency increase but 100% CPU increase
Source: Service Mesh Performance Project (arXiv)
URL: https://arxiv.org/pdf/2411.02267
Date: 2024
Excerpt: "The latency increase in the baseline-mTLS test is relatively low. There is up to 3% increase in the 99th percentile... enabling mTLS led to a notable increase in resource usage, with CPU and memory consumption increasing by up to 100% in client pods"
Context: Kubernetes baseline tests at 320-12,800 RPS; mTLS enforced via Go crypto/tls
Confidence: High

#### Claim 8: HTTP/3 50-100x slower than HTTP/2 on local networks
Source: Ian Duncan's blog
URL: https://iankduncan.com/engineering/2026-02-10-http3-not-always-faster/
Date: 2026-02-10
Excerpt: "On my local network, HTTP/3 was consistently and measurably slower, by 50-100x... TCP has been optimized in the kernel for decades. It benefits from hardware offloading (TSO, GRO, zero-copy paths, delayed ACKs implemented where the interrupts live). QUIC runs in userspace, and the cost is not small."
Context: Benchmarks on GCE c3-standard-8 machines running Linux 5.10; HTTP/3 client library development
Confidence: High

#### Claim 9: DPDK enables 100 Gbps+ line-rate packet processing
Source: SimplyBlock DPDK glossary
URL: https://simplyblock.io/glossary/what-is-dpdk/
Date: 2026-04-06
Excerpt: "DPDK (Data Plane Development Kit) is an open-source software library that accelerates packet processing workloads on general-purpose CPUs... Throughput: Line rate up to 100 Gbps+"
Context: Originally developed by Intel, now Linux Foundation; used for NVMe/TCP, distributed storage, NFV
Confidence: High

#### Claim 10: UCX transparently upgrades TCP to RDMA
Source: OpenUCX FAQ
URL: https://openucx.readthedocs.io/en/master/faq.html
Date: Ongoing
Excerpt: "UCX provides a high-level and performance-portable network API... UCP API abstracts differences and fills in the gaps across interconnects implemented in the UCT layer... implementations of programming models and libraries is simplified while providing efficient support for multiple interconnects (uGNI, Verbs, TCP, shared memory, ROCM, CUDA, etc.)"
Context: Used by OpenMPI, MPICH, Charm++, Dask, NCCL; auto-detects available transports at runtime
Confidence: High

#### Claim 11: Protobuf 6.5x faster encode than JSON in Go
Source: Dev.to JSON vs MessagePack vs Protobuf benchmarks
URL: https://dev.to/devflex-pro/json-vs-messagepack-vs-protobuf-in-go-my-real-benchmarks-and-what-they-mean-in-production-48fh
Date: 2025-11-20
Excerpt: "Protobuf ~6,500 ns/op encode (6.5x faster than JSON ~42,000 ns/op); Protobuf ~9,000 ns/op decode (7.5x faster than JSON ~68,000 ns/op); Payload size: Protobuf ~190 bytes vs JSON ~500 bytes"
Context: Go 1.22, AMD Ryzen 7950X, Linux; real Order struct payload from production system; benchmarks averaged over 10 runs
Confidence: High

#### Claim 12: SSH TCP-over-TCP meltdown problem
Source: Unix StackExchange
URL: https://unix.stackexchange.com/questions/34499/are-there-disadvantages-in-ssh-tunneling
Date: 2012-03-19
Excerpt: "The performance problem arise when you are tunneling TCP over TCP because you have two layers doing adaptive corrections (slow start, congestion avoidance, fast restransmit see RFC2001). Not being aware of one another they will experience great difficulties if you have loss on the outer connection."
Context: Cites http://sites.inka.de/bigred/devel/tcp-tcp.html for detailed theory of operation
Confidence: High

#### Claim 13: ZeroMQ ZMTP framing specification
Source: ZeroMQ RFC
URL: https://rfc.zeromq.org/spec/37/
Date: Ongoing
Excerpt: "A ZMTP frame consists of a length, followed by a flags field and a frame body of (length - 1) octets... For frames with a length of 1 to 254 octets, the length SHOULD BE encoded as a single octet. For frames with lengths of 255 and greater, the length SHALL BE encoded as a single octet with the value 255, followed by the length encoded as a 64-bit unsigned integer in network byte order."
Context: ZMTP 3.1 specification; framing layer is the foundation of all ZeroMQ messaging
Confidence: High

#### Claim 14: RDMA packet loss sensitivity
Source: Asterfusion RoCE for HPC
URL: https://cloudswit.ch/blogs/roce-for-hpc-test-data-and-deploy-on-sonic/
Date: 2025-08-20
Excerpt: "RDMA is highly sensitive to packet loss. Unlike TCP which retransmits lost packets with precision — RDMA protocol retransmits all messages in a batch when a single packet is lost. A packet loss rate above 0.001 can drastically reduce effective network throughput, and at a rate of 0.01, it drops to zero."
Context: RoCE HPC deployment guide; requires lossless Ethernet via PFC and ECN
Confidence: High

#### Claim 15: Headscale runs on $4/month VPS for hundreds of nodes
Source: PkgPulse Mesh VPN 2026 Guide
URL: https://www.pkgpulse.com/guides/tailscale-vs-netbird-vs-headscale-mesh-vpn-2026
Date: 2026-03-09
Excerpt: "Headscale's control plane is lightweight — it handles peer discovery and key distribution only, with data flowing through WireGuard directly. The Go-based server runs comfortably on a $4/month VPS with 1 GB RAM for networks of up to a few hundred nodes."
Context: Compared to Tailscale managed service at $6-18/user/month
Confidence: High

#### Claim 16: QUIC HTTP/3 45.2% data rate reduction on high-bandwidth links
Source: Research cited in Ian Duncan blog
URL: https://iankduncan.com/engineering/2026-02-10-http3-not-always-faster/
Date: 2026-02-10
Excerpt: "A group of researchers quantified this in 'QUIC is not Quick Enough over Fast Internet.' On high-bandwidth links, HTTP/3 suffered data rate reductions of up to 45.2% compared to HTTP/2. The gap widened as bandwidth increased."
Context: kernel's UDP stack generated 231K netif_receive_skb calls for single QUIC download vs 15K for HTTP/2
Confidence: High

#### Claim 17: Service mesh mTLS overhead — Istio 166% vs Linkerd 33%
Source: Service Mesh Performance Project (arXiv)
URL: https://arxiv.org/html/2411.02267v1
Date: 2024
Excerpt: "Enforcing mTLS increased latency across all tested providers, with increases of 166% for Istio, 8% for Istio Ambient, 33% for Linkerd, and 99% for Cilium... Istio's latency increase was almost four times that of Linkerd and more than 6 times that of Istio Ambient."
Context: Tests at 3200 RPS, 1600 connections, intra-node communication; Istio sidecar proxy is the bottleneck, not mTLS itself
Confidence: High

#### Claim 18: ZeroMQ 10GbE limited to ~20% link utilization for small messages
Source: ZeroMQ 10GbE performance wiki
URL: http://wiki.zeromq.org/results:10gbe-tests
Date: Ongoing
Excerpt: "With 1Gb Ethernet, ØMQ is able to saturate the network with messages approximately 120 bytes long... The situation with 10Gb Ethernet is different. We aren't able to exhaust more than ~2 Gb/second for message sizes up to 1024 bytes."
Context: Intel Low Latency Lab, London; ZeroMQ 0.1; reasons include non-zero-copy property and OS socket implementation
Confidence: Medium (older ZeroMQ version)

#### Claim 19: Arrow Flight v2 RDMA: 93% reduction tick-to-trade latency
Source: IJIRSET paper on Arrow Flight
URL: https://www.ijirset.com/upload/2023/june/166_Apache%20Arrow%20Flight...
Date: 2023-06
Excerpt: "Tick-to-trade latency dropped to 12 ms — a 93% reduction. Arrow Flight v2 with RDMA InfiniBand further reduced to 3.2 ms, with 97% of the remaining latency attributable to strategy computation rather than data transport."
Context: Tier-1 quantitative trading firm replacing proprietary binary protocol; pre-Arrow latency 180ms
Confidence: Medium (journal quality)

#### Claim 20: OpenSSH ControlPersist 33% first-run improvement
Source: OneUptime Ansible ControlPersist guide
URL: https://oneuptime.com/blog/post/2026-02-21-how-to-use-ansible-controlpersist-for-ssh-performance/view
Date: 2026-02-21
Excerpt: "Results from a 20-host, 15-task playbook: ControlPersist disabled 2m48s; ControlPersist enabled 1m52s (33% improvement for first run); Second run within persist window 1m44s"
Context: Ansible benchmark with 20 hosts, 15 tasks; ControlPersist=300s
Confidence: High
