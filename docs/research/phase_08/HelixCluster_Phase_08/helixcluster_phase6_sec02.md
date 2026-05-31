# 2. Network Mesh & Connectivity Layer

Every HelixCluster cell is an island of strongly-consistent state—until it reaches across the network to discover siblings, negotiate trust, and weave itself into a larger fabric. The Network Mesh & Connectivity Layer provides the undersea cables between these islands: encrypted tunnels, NAT traversal, local discovery, and transport protocols that transform physically separated cells into a single logical topology. This chapter describes the full stack, from the WireGuard interfaces that encrypt every packet to the mDNS broadcasts that let neighboring cells discover each other on a shared LAN.

The design philosophy is **progressive enhancement with guaranteed connectivity**. Two cells in the same rack connect directly at line rate. Two cells separated by consumer NATs punch holes through firewalls. Two cells trapped behind symmetric corporate NATs relay through TURN over TCP 443. No pair is left unconnected, but every pair uses the fastest path available.

---

## 2.1 WireGuard Mesh Foundation

### 2.1.1 Every Node Gets a WireGuard Interface

When a node joins a HelixCluster federation, the mesh manager creates a WireGuard interface—by convention named `wg-helix`—and assigns it an address from the cell's mesh CIDR. The interface persists for the node's entire lifecycle; it is the node's passport into the federated network.

The mesh manager generates a fresh Curve25519 key pair locally. The private key never leaves the node; the public key is distributed to peers through the SPIFFE/SPIRE identity infrastructure described in Chapter 5. Each peer entry in the WireGuard configuration carries an `AllowedIPs` list that includes the cell's pod CIDR and service range, so cross-cell traffic routes automatically through the mesh without additional route advertisements.

The key exchange flow is orchestrated by SPIRE rather than by a central VPN controller. Each node receives a SPIFFE X.509-SVID from the local SPIRE agent; the SVID's URI SAN contains the node's WireGuard public key as a URI parameter. When two nodes perform mutual TLS authentication during federation join, they exchange SVIDs and thereby learn each other's WireGuard public keys. This binds the network-layer identity (WireGuard key) to the application-layer identity (SPIFFE ID), eliminating the need for a separate PKI or pre-shared key distribution mechanism.

**Table 2.1: Self-Hosted VPN Solution Comparison**

| Solution | Control Plane | Data Plane | NAT Traversal | Self-Hosted | Max Throughput | Best For |
|----------|--------------|------------|---------------|-------------|----------------|----------|
| **Headscale** | Open-source Tailscale server (SQLite/PostgreSQL) | Kernel WireGuard via Tailscale client | STUN + custom DERP | Yes | ~6.8 Gbps | Teams wanting Tailscale UX with data sovereignty |
| **NetBird** | Management + Signal + Relay services | Kernel WireGuard | ICE + TURN (parallel setup) | Yes | ~6.8 Gbps | Zero Trust with SSO/MFA; modern web UI |
| **NetMaker** | Netmaker Server + MQTT broker | Native kernel WireGuard (via `wg` CLI) | STUN only | Yes | ~9+ Gbps | Maximum throughput; infrastructure-heavy environments |
| **Nebula** | Lighthouse nodes (discovery only) | Custom UDP overlay (AES-256-GCM) | UDP hole punching | Yes | ~9+ Gbps | Certificate-based identity; Slack-tested at 2,000+ nodes |
| **ZeroTier** | Network Controller (self-hostable) | Proprietary VL2/VL3 overlay | STUN + relay | Partial | ~1.2 Gbps | Layer 2 bridging; virtual LAN with multicast |
| **Raw WireGuard** | None (manual configuration) | Kernel WireGuard | None (manual endpoints) | Yes | ~9.4 Gbps | Custom tooling; maximum control |

HelixCluster's default integration targets **Headscale** or **NetBird** as the control plane. Both offer full client compatibility with official Tailscale or WireGuard clients while keeping coordination infrastructure under operator control. Headscale provides the lowest-friction experience for teams already familiar with Tailscale; NetBird offers richer Zero Trust policy controls and parallel ICE+relay setup that shaves 200-500 ms off connection establishment.

For operators who need maximum throughput and are comfortable with additional configuration complexity, **NetMaker** configures the native kernel WireGuard interface directly. This avoids the 10-15% CPU overhead of Tailscale's userspace `wireguard-go` implementation, delivering near-line-rate performance: NetMaker has been benchmarked at ~852 Mbps on a 1 Gbps link versus Tailscale's ~268-290 Mbps under identical conditions. The trade-off is that NetMaker requires managing a wildcard DNS entry and an MQTT broker for client coordination.

**Nebula** occupies a unique position. Developed at Slack and battle-tested on over 2,000 production servers, Nebula uses its own UDP-based encrypted overlay rather than WireGuard. Lighthouse nodes provide peer discovery but do not route traffic, keeping the data path strictly peer-to-peer. Nebula's certificate-based identity model maps naturally to SPIFFE, and its pure-Go implementation compiles to a single static binary. The limitation is the lack of TCP fallback: unlike Tailscale's DERP or TURN relays, Nebula cannot traverse networks that block UDP entirely. HelixCluster treats Nebula as an alternative mesh backend for environments where UDP is unrestricted and maximum throughput is paramount.

### 2.1.2 Kernel WireGuard Performance: ~3-5% CPU at 10 Gbps

The WireGuard kernel module, merged into Linux 5.6, performs cryptographic operations in kernel space with highly optimized assembly implementations of Curve25519, ChaCha20, and Poly1305. The result is performance that is difficult to distinguish from unencrypted traffic.

**Table 2.2: WireGuard Performance Benchmarks (Confirmed)**

| Metric | Kernel WireGuard | Tailscale (Userspace) | NetMaker (Kernel) |
|--------|-----------------|----------------------|-------------------|
| Single-stream throughput | ~8.0 Gbps | ~6.8 Gbps | ~8.5 Gbps |
| 8-stream throughput | ~9.4 Gbps | ~9.1 Gbps | ~9.4 Gbps |
| CPU at 1 Gbps sustained | ~3-5% | ~12-18% | ~3-5% |
| Latency overhead vs. LAN | <0.5 ms | 1-2 ms | <0.5 ms |
| Memory footprint (stable) | ~27 MB | 15-25 MB (up to 1 GB under load) | ~27 MB |

The 3-5% CPU figure at 1 Gbps sustained scales roughly linearly with throughput up to about 8-9 Gbps, at which point memory bandwidth and interrupt overhead become the dominant bottlenecks rather than cryptography. At 10 Gbps, expect 5-8% CPU on a modern x86_64 server with AES-NI and AVX2. On ARM64 (Graviton, Ampere Altra), the numbers are comparable: WireGuard's ARM NEON assembly is highly optimized.

The critical architectural decision is **kernel WireGuard for the data plane, always**. Userspace implementations exist for platforms without kernel module support (macOS, some containerized environments), but HelixCluster's production target is Linux with the kernel module loaded. The 3x CPU overhead difference between kernel and userspace is not merely an efficiency concern; at scale, it determines whether a node can route 10 Gbps of federated traffic or becomes CPU-bound at 3 Gbps.

### 2.1.3 Headscale/NetBird: Self-Hosted Control Plane Configuration

The control plane maintains the network map—who is online, what their public endpoints are, which NAT traversal relays are available—and distributes it to all participants. HelixCluster operators choose between Headscale and NetBird based on policy requirements.

**Example WireGuard mesh configuration (NetMaker-style, kernel WireGuard):**

```ini
# /etc/wireguard/wg-helix.conf — Node: cell-alpha-gw-01
[Interface]
PrivateKey = <redacted>
Address = 10.200.1.1/24
ListenPort = 51820
MTU = 1280

# Peer: cell-beta-gw-01 (eu-west)
[Peer]
PublicKey = abcd1234...efgh5678
Endpoint = 203.0.113.45:51820
AllowedIPs = 10.201.0.0/16, 10.200.2.0/24
PersistentKeepalive = 25

# Peer: cell-gamma-gw-01 (ap-south)
[Peer]
PublicKey = ijkl9012...mnop3456
Endpoint = 198.51.100.22:51820
AllowedIPs = 10.202.0.0/16, 10.200.3.0/24
PersistentKeepalive = 25

# Peer: cell-alpha-worker-042
[Peer]
PublicKey = qrst6789...uvwx0123
Endpoint = 192.168.1.142:51820
AllowedIPs = 10.200.1.142/32
PersistentKeepalive = 25
```

The `PersistentKeepalive = 25` directive sends a keepalive packet every 25 seconds, keeping NAT bindings alive on consumer routers (which typically expire UDP mappings after 30-60 seconds of inactivity). The `MTU = 1280` accounts for the 40-byte WireGuard header plus IPv4/IPv6 encapsulation overhead, preventing fragmentation on paths with lower effective MTU.

The `AllowedIPs` field deserves careful attention. Each cell gateway advertises its cell's full pod CIDR and service range, so traffic to any pod in any cell routes through the appropriate gateway. Worker nodes, by contrast, advertise only their host IP (`/32`), since they do not forward traffic for other pods. This split between gateway and worker roles keeps the routing table compact: a 100-cell federation has at most a few hundred route entries, not thousands.

---

## 2.2 NAT Traversal Stack

### 2.2.1 Connection Chain: Direct → STUN/ICE → UPnP/PCP → TURN → Relay

The real world of cluster federation is a maze of NATs, firewalls, and asymmetric routing. A cell in a home lab sits behind a consumer router with dynamic port mapping. A cell in a corporate data center is behind a symmetric NAT that assigns a different external port for every destination. A cell on a mobile network is behind carrier-grade NAT (CGNAT) with unpredictable session handling.

HelixCluster implements a prioritized fallback chain that exhausts every possibility for direct connection before resigning to a relay:

**Priority 1 — Direct (same LAN/VPN):** Two cells on the same subnet connect via their local IP addresses, bypassing NAT entirely. Latency: <1 ms. Throughput: line rate. Reliability: high.

**Priority 2 — STUN + ICE hole punching:** Each node queries a STUN server to discover its public-facing IP and port (the "server-reflexive" address). Nodes exchange these addresses through the signaling channel and attempt simultaneous UDP sends to punch holes through their respective NATs. Success rate: 82-95% for non-symmetric NATs.

**Priority 3 — UPnP/PCP (opportunistic):** If the local router supports UPnP IGD or PCP, the node requests an explicit port mapping. This converts a hidden NAT'd endpoint into a publicly reachable one. Limited availability (~80% of consumer routers, ~5% of enterprise firewalls), but where it works, it provides direct connectivity without STUN complexity.

**Priority 4 — TURN relay over TCP 443:** When both endpoints are behind symmetric NATs (where STUN fails) or when UDP is blocked entirely, traffic relays through a TURN server. The TURN allocation runs over TCP port 443, making the traffic indistinguishable from HTTPS and therefore unblockable by even the strictest firewalls. Latency: +1 server hop. Throughput: relay-bounded. Reliability: 100% for any network that allows outbound HTTPS.

**Priority 5 — libp2p circuit relay / DERP:** Any publicly reachable peer in the federation can act as an application-layer relay. Bandwidth is limited and latency is higher than TURN, but this decentralized approach requires no dedicated relay infrastructure.

**Priority 6 — SSH tunnel (last resort):** A reverse SSH tunnel to a bastion host provides guaranteed administrative connectivity for bootstrapping and debugging. Not used for data plane traffic due to TCP-only constraints and single-threaded throughput limits.

### 2.2.2 ICE Implementation: Gathering Candidates, Connectivity Checks, Nomination

HelixCluster's NAT traversal engine implements the Interactive Connectivity Establishment (ICE) framework per RFC 8445. ICE systematically gathers connectivity candidates, tests them in priority order, and selects the best working pair.

The four candidate types, in priority order, are:

1. **Host candidates** — local interface addresses (e.g., `192.168.1.100:51820`)
2. **Server-reflexive candidates** — public address discovered via STUN (e.g., `203.0.113.45:41641`)
3. **Peer-reflexive candidates** — addresses learned during connectivity checks when a NAT mapping differs from the STUN-discovered mapping
4. **Relay candidates** — TURN server-allocated address (e.g., `turn.helix.example.com:53478`)

Candidate priority is calculated per RFC 8445: `priority = (2^24)*type_preference + (2^8)*local_preference + (256 - component_ID)`. Host candidates receive type preference 126, peer-reflexive 110, server-reflexive 100, and relay 0.

**NAT Traversal Engine (Go):**

```go
package mesh

import (
    "context"
    "fmt"
    "net"
    "sync"
    "time"
)

// NATType categorizes the local network environment.
type NATType int

const (
    NATUnknown NATType = iota
    NATNone           // Public IP, no NAT
    NATFullCone       // Endpoint-independent mapping
    NATRestricted     // Address-restricted cone
    NATPortRestricted // Port-restricted cone
    NATSymmetric      // Different mapping per destination (HARDCASE)
    NATBlocked        // UDP blocked entirely
)

// CandidateType represents an ICE candidate type.
type CandidateType int

const (
    CandidateHost CandidateType = iota
    CandidateServerReflexive
    CandidatePeerReflexive
    CandidateRelay
)

// ICECandidate is a connectivity endpoint discovered during NAT traversal.
type ICECandidate struct {
    Type     CandidateType
    Address  string // IP:port
    Priority uint32
    CellID   uint16
    NodeID   string
}

// NATTraversal manages the ICE process for establishing P2P connections.
type NATTraversal struct {
    stunServers []string
    turnServer  string
    turnCred    TURNCredentials

    localAddrs []string
    natType    NATType
    mappedAddr string // Server-reflexive address from STUN

    mu sync.RWMutex
}

// TURNCredentials holds TURN authentication info.
type TURNCredentials struct {
    Username string
    Password string
    Realm    string
}

// NewNATTraversal creates a NAT traversal engine with configured STUN/TURN servers.
func NewNATTraversal(stunServers []string, turnServer string,
                     turnCred TURNCredentials) *NATTraversal {
    return &NATTraversal{
        stunServers: stunServers,
        turnServer:  turnServer,
        turnCred:    turnCred,
    }
}

// DiscoverCandidates performs candidate gathering (ICE Phase 1).
func (nt *NATTraversal) DiscoverCandidates(ctx context.Context) ([]ICECandidate, error) {
    var candidates []ICECandidate

    // 1. Host candidates (local interfaces)
    localAddrs, err := nt.getLocalAddrs()
    if err != nil {
        return nil, fmt.Errorf("local addrs: %w", err)
    }
    for _, addr := range localAddrs {
        candidates = append(candidates, ICECandidate{
            Type:     CandidateHost,
            Address:  addr,
            Priority: nt.candidatePriority(CandidateHost),
        })
    }

    // 2. Server-reflexive candidate (STUN)
    if nt.stunServers != nil {
        mappedAddr, err := nt.querySTUN(ctx)
        if err == nil {
            nt.mappedAddr = mappedAddr
            candidates = append(candidates, ICECandidate{
                Type:     CandidateServerReflexive,
                Address:  mappedAddr,
                Priority: nt.candidatePriority(CandidateServerReflexive),
            })
            nt.natType = nt.classifyNAT(ctx)
        }
    }

    // 3. Relay candidate (TURN) - always gather as fallback
    if nt.turnServer != "" {
        relayAddr, err := nt.allocateTURN(ctx)
        if err == nil {
            candidates = append(candidates, ICECandidate{
                Type:     CandidateRelay,
                Address:  relayAddr,
                Priority: nt.candidatePriority(CandidateRelay),
            })
        }
    }

    return candidates, nil
}

// candidatePriority calculates ICE priority per RFC 8445.
func (nt *NATTraversal) candidatePriority(ct CandidateType) uint32 {
    typePrefs := map[CandidateType]int{
        CandidateHost:            126,
        CandidatePeerReflexive:   110,
        CandidateServerReflexive: 100,
        CandidateRelay:            0,
    }
    // Simplified: local preference = 65535, component = 1
    return uint32((1 << 24) * typePrefs[ct] + (1 << 8) * 65535 + (255 - 1))
}

// Connect performs connectivity checks and returns the best working candidate pair.
func (nt *NATTraversal) Connect(ctx context.Context,
                                 remoteCandidates []ICECandidate) (*net.UDPConn, error) {
    localCandidates, err := nt.DiscoverCandidates(ctx)
    if err != nil {
        return nil, err
    }

    // Priority-ordered candidate pairs
    pairs := nt.formCandidatePairs(localCandidates, remoteCandidates)

    // Attempt connectivity checks in parallel (bounded)
    resultCh := make(chan *net.UDPConn, 1)
    var wg sync.WaitGroup
    sem := make(chan struct{}, 5) // Max 5 parallel checks

    for _, pair := range pairs {
        wg.Add(1)
        sem <- struct{}{}
        go func(p candidatePair) {
            defer wg.Done()
            defer func() { <-sem }()

            conn, err := nt.checkConnectivity(ctx, p)
            if err == nil {
                select {
                case resultCh <- conn:
                default: // Another goroutine already succeeded
                    conn.Close()
                }
            }
        }(pair)
    }

    go func() {
        wg.Wait()
        close(resultCh)
    }()

    conn := <-resultCh
    if conn == nil {
        return nil, fmt.Errorf("no candidate pair succeeded")
    }
    return conn, nil
}

type candidatePair struct {
    local  ICECandidate
    remote ICECandidate
}

func (nt *NATTraversal) formCandidatePairs(local, remote []ICECandidate) []candidatePair {
    var pairs []candidatePair
    for _, l := range local {
        for _, r := range remote {
            pairs = append(pairs, candidatePair{local: l, remote: r})
        }
    }
    sortCandidatePairs(pairs)
    return pairs
}

// classifyNAT determines NAT type via multiple STUN queries to different servers.
func (nt *NATTraversal) classifyNAT(ctx context.Context) NATType {
    if len(nt.stunServers) < 2 {
        return NATUnknown
    }
    addr1, _ := nt.querySTUNWithServer(ctx, nt.stunServers[0])
    addr2, _ := nt.querySTUNWithServer(ctx, nt.stunServers[1])
    if addr1 == "" || addr2 == "" {
        return NATUnknown
    }
    if addr1 != addr2 {
        return NATSymmetric
    }
    return NATRestricted
}

// Stub implementations for external I/O
func (nt *NATTraversal) getLocalAddrs() ([]string, error) { return nil, nil }
func (nt *NATTraversal) querySTUN(ctx context.Context) (string, error) { return "", nil }
func (nt *NATTraversal) querySTUNWithServer(ctx context.Context,
    server string) (string, error) { return "", nil }
func (nt *NATTraversal) allocateTURN(ctx context.Context) (string, error) { return "", nil }
func (nt *NATTraversal) checkConnectivity(ctx context.Context,
    p candidatePair) (*net.UDPConn, error) { return nil, nil }
func sortCandidatePairs(pairs []candidatePair) {}
```

The engine performs five bounded parallel connectivity checks (the `sem` channel limits concurrency to prevent port exhaustion and excessive probing). The first successful check wins; all remaining goroutines clean up their attempted connections. In practice, host-to-host pairs succeed in microseconds on the same LAN, while STUN-based pairs complete within 100-300 ms across the internet.

**Table 2.3: NAT Type Classification & Traversal Strategy**

| NAT Type | STUN Discovery | Hole Punching | TURN Required? | Approx. Prevalence |
|----------|---------------|---------------|----------------|-------------------|
| Full Cone | Yes | Yes (direct) | No | ~5% of consumer networks |
| Restricted Cone | Yes | Yes | No | ~30% of consumer networks |
| Port-Restricted Cone | Yes | Yes | No | ~40% of consumer networks |
| **Symmetric NAT** | **No** | **No** | **Yes** | ~20% of enterprise/CGNAT |
| UDP Blocked | N/A | N/A | Yes (TCP 443) | ~5% of corporate networks |

### 2.2.3 libp2p DCUtR: ~70% Hole-Punch Success Rate

When a centralized STUN/TURN infrastructure is unavailable or undesirable, HelixCluster can fall back to libp2p's DCUtR (Direct Connection Upgrade Through Relay) protocol. DCUtR eliminates the need for dedicated signaling servers: any publicly reachable peer in the federation can act as a coordination relay.

The process works as follows:

1. The initiator establishes a relayed connection to the listener through any available public peer.
2. Both parties exchange their observed host and server-reflexive addresses over the relayed connection.
3. Each side measures RTT to synchronize timing.
4. A `SYNC` message triggers both sides to simultaneously dial each other directly.
5. If the NAT mappings align, a direct UDP flow is established and the relay is retired.

DCUtR's effectiveness has been validated at scale: 4.4 million measurements across 85,000+ networks show a **70% ± 7.1% conditional hole-punch success rate**, with 97.6% of successful punches completing on the first attempt. TCP and QUIC achieve comparable success rates when properly synchronized. Notably, 50% of peers that successfully upgrade experience RTT reduction to 70% or less of their relayed path latency.

The limitation is the ~30% of cases where DCUtR fails, primarily due to symmetric NATs or aggressive firewall rules. In these cases, the connection remains on the circuit relay with limited bandwidth until a TURN relay becomes available. HelixCluster uses DCUtR as an optimization to reduce relay load, not as a replacement for the guaranteed-connectivity TURN path.

### 2.2.4 TURN Relay over TCP 443: Guaranteed Connectivity for Symmetric NAT

Symmetric NATs—where the external port mapping differs for every destination—break both STUN discovery and hole punching. The STUN server sees mapping `A`, but the peer at a different destination would need mapping `B`, which the STUN response cannot predict. Approximately 15-20% of production connections encounter this scenario, particularly in enterprise environments and CGNAT deployments.

TURN (Traversal Using Relays around NAT, RFC 5766/8656) solves this by relaying all traffic through a server. Both peers connect to the same TURN server, which forwards packets between them. The server sees and relays encrypted WireGuard packets—it does not decrypt them—so the TURN operator cannot inspect traffic content.

The critical configuration is **TURN over TCP 443**. By running the TURN protocol over TCP port 443 with TLS, the traffic is byte-for-byte indistinguishable from HTTPS. No firewall that allows web browsing can block it without collateral damage. This is the connectivity method of last resort, and it is guaranteed to work anywhere HTTPS works.

**Table 2.4: NAT Traversal Fallback Chain Priority**

| Priority | Method | Latency | Throughput | Reliability | When Used |
|----------|--------|---------|------------|-------------|-----------|
| 1 | Direct LAN | <1 ms | Line rate | High | Same subnet/VLAN |
| 2 | P2P via STUN + ICE | 5-50 ms | Line rate | Medium-High | Non-symmetric NAT |
| 3 | UPnP/PCP mapped | 5-50 ms | Line rate | Medium | Router supports it |
| 4 | TURN relay (UDP) | 10-100 ms | Relay-bounded | High | Symmetric NAT, UDP allowed |
| 5 | TURN relay (TCP 443) | 20-200 ms | Relay-bounded | Very High | UDP blocked, HTTPS only |
| 6 | Circuit relay / DERP | 20-200 ms | Throttled (~2-10 Mbps) | Very High | No TURN available |
| 7 | SSH tunnel | +1-2 RTT | Single-threaded | Very High | Admin/debug only |

The latency penalty for TURN relay is typically one server hop: if the direct path is 50 ms and the TURN server is 25 ms from each peer, the relayed path is approximately 75 ms total. For HelixCluster's use case—cross-cell control plane traffic and moderate data transfer—this is entirely acceptable. High-bandwidth data replication uses direct paths or dedicated peering.

---

## 2.3 Local Discovery (mDNS/DNS-SD)

### 2.3.1 Zeroconf Service Announcement: `_helixcluster._tcp.local`

When two HelixCluster cells share a local area network—whether a physical rack, a VLAN, or a VPN—there is no need for STUN servers or DHT lookups to find each other. Multicast DNS (mDNS) and DNS Service Discovery (DNS-SD) provide zero-configuration peer discovery.

Each HelixCluster node runs an mDNS responder that advertises the `_helixcluster._tcp.local` service. The TXT records in the advertisement carry the metadata needed to initiate a WireGuard connection:

- `cellid` — the cell's federation identifier
- `nodeid` — the node's unique identifier
- `version` — the HelixCluster protocol version
- `wgpubkey` — the node's WireGuard public key
- `clusteraddr` — the node's cluster-internal API address

The Go implementation uses the `github.com/grandcat/zeroconf` library:

```go
package discovery

import (
    "context"
    "fmt"
    "strings"
    "time"

    "github.com/grandcat/zeroconf"
)

const (
    HelixServiceName = "_helixcluster._tcp"
    HelixDomain      = "local."
)

// mDNSServer advertises this cell on the local network.
type mDNSServer struct {
    server *zeroconf.Server
    cellID uint16
    nodeID string
    port   int
}

// NewmDNSServer creates an mDNS advertiser for this node.
func NewmDNSServer(cellID uint16, nodeID string, port int,
                  metadata map[string]string) (*mDNSServer, error) {
    txtRecords := []string{
        fmt.Sprintf("cellid=%d", cellID),
        fmt.Sprintf("nodeid=%s", nodeID),
        fmt.Sprintf("version=6.0.0"),
        fmt.Sprintf("wgpubkey=%s", metadata["wgpubkey"]),
        fmt.Sprintf("clusteraddr=%s", metadata["clusteraddr"]),
    }

    server, err := zeroconf.Register(
        fmt.Sprintf("helix-%s-%d", nodeID, cellID), // instance name
        HelixServiceName,                            // service type
        HelixDomain,                                 // domain
        port,                                        // port
        txtRecords,                                  // TXT records
        nil,                                         // interfaces (all)
    )
    if err != nil {
        return nil, fmt.Errorf("zeroconf register: %w", err)
    }

    return &mDNSServer{
        server: server,
        cellID: cellID,
        nodeID: nodeID,
        port:   port,
    }, nil
}

// Shutdown stops mDNS advertisement.
func (s *mDNSServer) Shutdown() {
    if s.server != nil {
        s.server.Shutdown()
    }
}

// mDNSBrowser discovers nearby HelixCluster cells via mDNS.
type mDNSBrowser struct {
    resolver *zeroconf.Resolver
    results  chan DiscoveredPeer
}

// DiscoveredPeer represents a peer discovered via mDNS.
type DiscoveredPeer struct {
    CellID          uint16
    NodeID          string
    Hostname        string
    IP              string
    Port            int
    WireGuardPubKey string
    ClusterAddr     string
    TTL             time.Duration
}

// NewmDNSBrowser creates a browser for HelixCluster services.
func NewmDNSBrowser() (*mDNSBrowser, error) {
    resolver, err := zeroconf.NewResolver(nil)
    if err != nil {
        return nil, fmt.Errorf("zeroconf resolver: %w", err)
    }
    return &mDNSBrowser{
        resolver: resolver,
        results:  make(chan DiscoveredPeer, 10),
    }, nil
}

// Browse starts discovering peers on the local network.
func (b *mDNSBrowser) Browse(ctx context.Context) (<-chan DiscoveredPeer, error) {
    entries := make(chan *zeroconf.ServiceEntry)

    go func() {
        for entry := range entries {
            peer := b.parseEntry(entry)
            if peer != nil {
                select {
                case b.results <- *peer:
                case <-ctx.Done():
                    return
                }
            }
        }
        close(b.results)
    }()

    go func() {
        if err := b.resolver.Browse(ctx, HelixServiceName,
                                    HelixDomain, entries); err != nil {
            fmt.Printf("mDNS browse error: %v\n", err)
        }
    }()

    return b.results, nil
}

func (b *mDNSBrowser) parseEntry(entry *zeroconf.ServiceEntry) *DiscoveredPeer {
    var peer DiscoveredPeer
    peer.Hostname = entry.Instance
    peer.TTL = entry.TTL
    if len(entry.AddrIPv4) > 0 {
        peer.IP = entry.AddrIPv4[0].String()
    }
    peer.Port = entry.Port

    for _, txt := range entry.Text {
        parts := strings.SplitN(txt, "=", 2)
        if len(parts) != 2 {
            continue
        }
        switch parts[0] {
        case "cellid":
            fmt.Sscanf(parts[1], "%d", &peer.CellID)
        case "nodeid":
            peer.NodeID = parts[1]
        case "wgpubkey":
            peer.WireGuardPubKey = parts[1]
        case "clusteraddr":
            peer.ClusterAddr = parts[1]
        }
    }

    if peer.CellID == 0 || peer.NodeID == "" {
        return nil // Invalid entry
    }
    return &peer
}
```

The browser runs continuously on every node, feeding discovered peers into the mesh manager. When a peer is discovered on the same LAN, the mesh manager adds it as a WireGuard peer with its local IP as the endpoint, bypassing NAT entirely. This provides sub-millisecond connectivity for co-located cells without any configuration.

### 2.3.2 Security: Verify mDNS-Discovered Nodes via SPIFFE Before Trust

**mDNS provides no authentication.** Any device on the local network can respond to mDNS queries with arbitrary TXT records, claiming any cell ID and WireGuard public key. The Responder tool—widely available in penetration testing distributions—can poison mDNS caches with false entries.

HelixCluster treats mDNS as **discovery only, never trust establishment**. A node discovered via mDNS is entered into a "pending verification" state. The mesh manager initiates the full SPIFFE mutual attestation protocol (described in Chapter 5) before adding the discovered peer to the trusted peer set. The SPIFFE verification confirms:

1. The peer's WireGuard public key matches the key embedded in its SPIFFE X.509-SVID.
2. The SVID was issued by a SPIRE server in the same trust domain or a federated trust domain.
3. The SVID has not expired and has not been revoked.
4. The cell ID in the mDNS advertisement matches the cell ID encoded in the SPIFFE ID.

Only after all four checks pass does the peer transition from "pending" to "trusted" and receive route advertisements. This design means an attacker on the local network can, at worst, cause a failed attestation attempt; they cannot inject routes, intercept traffic, or join the mesh.

---

## 2.4 SSH Tunnel Bridging

### 2.4.1 Reverse SSH Tunnels for NAT'd Nodes Behind Restrictive Firewalls

Some network environments block all UDP traffic and restrict outbound TCP to a whitelist of ports. In these extreme cases—typically corporate networks with strict egress filtering—HelixCluster falls back to reverse SSH tunnels for control plane connectivity.

The restricted node runs `autossh` to maintain a persistent reverse tunnel to a publicly reachable bastion host:

```bash
# On the NAT'd node — runs continuously via systemd
autossh -M 0 -N \
    -o "ServerAliveInterval=30" \
    -o "ServerAliveCountMax=3" \
    -o "ExitOnForwardFailure=yes" \
    -R 127.0.0.1:2222:localhost:22 \
    -R 127.0.0.1:7946:localhost:7946 \
    -i /etc/helix/keys/ssh_bridge_ed25519 \
    bastion@relay.helix.example.com
```

This forwards port 2222 on the bastion to the node's SSH service and port 7946 to the node's gossip service. Other cells can reach the restricted node by connecting to the bastion on those ports. The tunnel is automatically restarted on failure by `autossh`, with server-alive probes detecting dead connections within 90 seconds.

SSH tunnels are suitable for control plane bootstrapping, administrative access, and small-scale federation. They are **not suitable for high-throughput data plane traffic**: each tunnel is single-threaded per connection, adds 1-2 RTT of latency, and cannot carry UDP traffic (which rules out QUIC, WireGuard, and many real-time protocols). When a restricted node needs full data plane participation, the operator should deploy a TURN relay on TCP 443 instead.

**Table 2.5: SSH Tunnel Limitations for Cluster Mesh**

| Limitation | Impact | Mitigation |
|------------|--------|------------|
| TCP only | UDP traffic (QUIC, WireGuard) cannot tunnel | Use TURN relay for UDP-bearing protocols |
| Single-threaded per connection | Throughput bottleneck at high bandwidth | Deploy multiple tunnels with HAProxy |
| Latency | Adds 1-2 RTT minimum | Place bastion geographically close |
| No automatic mesh | Manual configuration of each tunnel pair | Use systemd units with template instantiation |
| Key management | SSH host keys scale poorly | Use SPIFFE-issued short-lived certificates |
| Resource usage | 100+ tunnels = significant CPU/memory | Offload to dedicated relay nodes |

---

## 2.5 Cloud VPN Bridging

### 2.5.1 Cloud Instances Join via WireGuard + TURN Relay

Cloud-hosted cells (AWS, GCP, Azure, Hetzner) join the federation through the same WireGuard mesh as on-premise cells, with cloud-specific optimizations for endpoint discovery and relay selection.

Each cloud provider offers metadata services that expose instance public IPs, allowing HelixCluster nodes to auto-configure their WireGuard endpoints without STUN:

```yaml
# /etc/helix/agent/cloud-bridge.yaml
cloud_bridge:
  enabled: true
  provider: aws  # aws, gcp, azure, hetzner, generic

  # AWS: use Global Accelerator for anycast endpoint
  aws:
    accelerator_enabled: true
    region: "us-east-1"

  # GCP: use Cloud Load Balancing anycast IP
  gcp:
    anycast_ip: "34.120.0.1"
    region: "us-central1"

  wireguard:
    listen_port: 51820
    endpoint_discovery: stun  # stun, static, metadata-service

  turn:
    enabled: true
    server: "turn.helix.example.com:3478"
    protocol: tcp  # tcp for firewall bypass; udp when available
    tls: true
```

Cloud cells prefer `metadata-service` endpoint discovery, which queries the provider's instance metadata API to obtain the public IP directly. This avoids STUN latency and provides a stable endpoint for as long as the instance lives. For spot instances and preemptible VMs—where IP addresses change on every restart—STUN discovery is used instead, with WireGuard endpoints updated dynamically.

Cloud egress firewalls are typically permissive for outbound UDP but restrictive for inbound. HelixCluster's NAT traversal stack handles this automatically: cloud cells initiate outbound connections to on-premise cells, letting the cloud firewall see the flow as "established" traffic. For cloud-to-cloud connectivity within the same provider, cells deploy to the same VPC or use VPC peering to avoid NAT entirely.

Global Accelerator (AWS) and Cloud Load Balancing anycast (GCP) provide additional optimizations: a single anycast IP fronts multiple gateway instances across regions, giving cloud cells a stable endpoint that automatically routes to the nearest healthy gateway. This eliminates endpoint churn during gateway failover.

---

## 2.6 QUIC Transport Layer

### 2.6.1 QUIC for NAT-Friendly Reliable Transport

QUIC (RFC 9000) is a UDP-based transport protocol with built-in TLS 1.3 encryption. Its design makes it inherently NAT-traversal friendly and well-suited for the unstable network paths that federated clusters encounter.

The primary advantage over TCP is **connection migration**: QUIC identifies connections by a 64-bit connection ID rather than the 4-tuple of source IP, source port, destination IP, and destination port. When a peer's network changes—WiFi to cellular handoff, NAT rebinding after a router restart, or a cloud instance receiving a new public IP—the connection continues uninterrupted. The peer sends packets from the new address with the same connection ID, and the recipient updates its routing table automatically.

For HelixCluster, connection migration saves 2-3 RTTs compared to TCP reconnection after a network change. At a 100 ms cross-country RTT, this is the difference between a 200-300 ms stall and zero perceptible interruption.

**Table 2.6: QUIC vs. TCP for NAT Traversal**

| Feature | QUIC | TCP | Impact on Federation |
|---------|------|-----|---------------------|
| Handshake time | 1 RTT (or 0-RTT resumed) | 1.5-2 RTT + TLS | Faster reconnection after partition |
| Connection migration | Yes (connection ID) | No (4-tuple bound) | IP changes without reconnect |
| Head-of-line blocking | Eliminated (per-stream) | Present | One slow stream doesn't block gossip |
| NAT timeout resilience | Better (UDP + keepalive) | Good (TCP established state) | Longer-lived NAT bindings |
| Hole punch time | 2 RTTs | 2.5 RTTs | Faster P2P establishment |
| Enterprise firewall | Sometimes blocked (UDP) | Rarely blocked | Use TURN TCP 443 fallback |

QUIC is used in HelixCluster for two specific purposes: (1) the gossip protocol's transport when UDP datagrams are insufficient for large messages, and (2) the inter-cell API stream for control plane RPCs. The WireGuard mesh remains the primary data plane for pod-to-pod traffic; QUIC complements it rather than replacing it.

The 0-RTT resumption feature is particularly valuable for federation: when a cell rejoins after a network partition, it can resume QUIC connections to previously contacted cells with zero round trips, immediately resuming gossip and control plane traffic.

---

## 2.7 libp2p Integration

### 2.7.1 libp2p as Application-Layer Complement

HelixCluster integrates libp2p not as a replacement for WireGuard but as a complementary application-layer peer-to-peer stack. While WireGuard provides encrypted tunnels between known cells, libp2p provides decentralized discovery, content routing, and pub/sub messaging for applications that need them.

The integration is modular: cells can enable libp2p services independently of the mesh layer. A cell running distributed machine learning training might enable libp2p's Bitswap for model checkpoint exchange. A cell running edge analytics might enable GossipSub for event streaming. The WireGuard mesh handles connectivity; libp2p handles application semantics.

**Table 2.7: WireGuard Mesh vs. libp2p — Layered Responsibilities**

| Dimension | WireGuard Mesh (Layer 3) | libp2p (Application Layer) |
|-----------|-------------------------|---------------------------|
| Encryption | Kernel WireGuard (Noise) | TLS 1.3 / Noise (userspace) |
| NAT traversal | ICE + TURN (~99% with relay) | DCUtR (~70% success) |
| Throughput | Near line rate (kernel) | Lower (userspace overhead) |
| Latency | Sub-ms (kernel path) | Higher (DHT lookups) |
| Discovery | Control plane / mDNS | Kademlia DHT (global) |
| Reliability | Guaranteed (relay fallback) | Best-effort P2P |
| Multi-stream | Per-tunnel | Native multiplexing (yamux) |
| Content routing | N/A (layer 3 only) | DHT + Bitswap |
| Pub/sub messaging | N/A | GossipSub v1.1 |
| Best for | Cluster network fabric | Application P2P patterns |

### 2.7.2 GossipSub for Cross-Cell Event Streaming

GossipSub v1.1 provides topic-based pub/sub messaging across the federation. Each cell subscribes to topics relevant to its workload—`helix.events.deployments`, `helix.metrics.aggregate`, `helix.alerts.critical`—and receives messages published by any other cell.

The protocol maintains two overlays: a mesh for full-message propagation and a gossip overlay for metadata. Each node maintains D peers in the full-message mesh (typically 6) and K peers in the gossip mesh (K ≥ D). This dual structure provides low-latency delivery with minimal bandwidth overhead: messages reach 85% of a 1,000-node network in approximately 9 seconds under 100 ms latency conditions.

For HelixCluster, GossipSub is used for cross-cell event distribution: deployment notifications, policy updates, and aggregated metrics. The hierarchical gossip system described in Chapter 3 handles membership and failure detection; GossipSub handles application-level message dissemination.

### 2.7.3 libp2p DCUtR Integration

When libp2p is enabled, HelixCluster uses DCUtR as a supplementary NAT traversal mechanism. If the WireGuard ICE process fails to establish a direct connection, the libp2p subsystem attempts DCUtR through any publicly reachable peer in the DHT. A successful DCUtR punch provides a direct UDP path that the WireGuard mesh can then use, upgrading the connection from relayed to direct.

The 70% DCUtR success rate is additive to the ICE success rate: if ICE direct connection fails (e.g., due to asymmetric routing), DCUtR may still succeed through a different coordination path. The combined direct-connection rate for HelixCluster's multi-mechanism approach exceeds 95% across diverse network conditions, with the remaining 5% handled by TURN relay.

libp2p does not replace WireGuard for the cluster mesh layer. It complements it for application-specific P2P patterns—decentralized discovery, content routing, and gossip messaging—that would be expensive and complex to implement on top of raw WireGuard tunnels. The layered approach gives HelixCluster both the performance of kernel-space encryption and the flexibility of a modern P2P application stack.
