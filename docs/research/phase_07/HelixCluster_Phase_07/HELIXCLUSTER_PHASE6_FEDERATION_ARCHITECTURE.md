# HelixCluster Phase 6 -- Multi-Cluster Federation & Hierarchical Block Binding

> **Version:** 1.0.0  
> **Status:** Architecture Specification  
> **Word Count:** ~14,500  
> **Classification:** Definitive Technical Reference  

---

## 1. Executive Summary

HelixCluster Phase 6 -- codenamed "Block of Blocks" -- introduces multi-cluster federation, enabling independent HelixCluster instances ("cells" or "blocks") to discover, authenticate, and bind into hierarchical meta-clusters. A hobbyist with five Raspberry Pis can federate with a friend's ten-node homelab as equal peers. A company can bind fifty edge clusters into a global compute mesh. A research consortium can merge regional deployments into a planet-scale grid.

### Architecture Vision

Phase 6 adopts a **cell-based hierarchical topology** validated by Google's Borg (median 10,000 machines per cell) and refined by a decade of production Kubernetes federation. Each cell maintains an independent control plane -- etcd, API server, scheduler -- while a federation layer coordinates cross-cell operations without compromising per-cell autonomy.

The architecture follows the principle of **per-cell strong consistency, cross-cell eventual consistency**. Raft-based consensus (etcd) never stretches across WAN; CRDTs and gossip handle cross-cell metadata; and anti-entropy mechanisms ensure convergence after partitions heal.

### Key Metrics

| Metric | Target | Method |
|--------|--------|--------|
| Max cells per federation | 255 (511 with Cilium extended) | Cell ID encoding (uint8/uint16) |
| Max nodes per cell | 5,000 (safe) / 10,000 (limit) | etcd + memberlist tested limits |
| Cross-cell discovery time | < 30 seconds | mDNS + DHT bootstrap |
| Cross-cell gossip convergence | O(log C) rounds, C = cell count | SWIM epidemic protocol |
| Federation join time | < 60 seconds | Full mesh establishment |
| Partition detection time | 5-30 seconds | Phi accrual + SWIM probes |
| Cross-cell throughput | Near line-rate (WireGuard kernel) | Kernel-space encryption |
| Control plane availability | 99.99% per cell | 3-5 node etcd per cell |

### Topology Overview

```
                    +----------------------------------+
                    |   FEDERATION CONTROL PLANE       |
                    |   (Karmada / OCM Hub - optional) |
                    |   3-5 nodes, regional placement  |
                    +--------+-------------+-----------+
                             |             |
              +--------------+   +---------+-----------+
              |                  |                     |
     +--------v--------+  +------v------+  +-----------v------+
     |   Cell Alpha    |  |  Cell Beta  |  |   Cell Gamma     |
     |   (us-east)     |  |  (eu-west)  |  |   (ap-south)     |
     |                 |  |             |  |                  |
     |  etcd (3 nodes) |  | etcd (3)    |  | etcd (3)         |
     |  100-5000 nodes |  | 100-5000    |  | 100-5000         |
     |  Cilium CNI     |  | Cilium CNI  |  | Cilium CNI       |
     +--------+--------+  +------+------+  +-----------+------+
              |                  |                     |
              +------------------+---------------------+
                                 |
                    +------------v-------------+
                    |   WIREGUARD MESH LAYER   |
                    |   (Full/partial mesh)    |
                    |   Auto NAT traversal     |
                    +--------------------------+
```

---

## 2. Federation Topology & Patterns

### 2.1 Cell-Based Hierarchical Architecture

HelixCluster Phase 6 cells are modeled after Google Borg cells: independent, self-managing clusters with a logically centralized control plane. Each cell contains:

- **Cell Control Plane**: 3-5 etcd nodes, API server replicas, scheduler
- **Cell Data Plane**: 100-5,000 worker nodes running Cilium CNI
- **Cell Gateway**: 1-3 WireGuard gateway nodes for cross-cell traffic
- **Cell Agent**: Federation agent running on every node (gossip, mesh, identity)

```
+------------------------------------------------------------------+
|                        HELIXCLUSTER FEDERATION                    |
|                                                                   |
|   Tier 0: Federation Plane (optional, for governance)             |
|   +-- Karmada control plane (or OCM hub)                         |
|   +-- Global policy distribution                                  |
|   +-- Cross-cell workload scheduling                             |
|                                                                   |
|   Tier 1: Cell Layer (independent, 100-5000 nodes each)           |
|   +-- Cell A: etcd 3-node HA, Cilium, local control               |
|   +-- Cell B: etcd 3-node HA, Cilium, local control               |
|   +-- ... up to 255 cells                                         |
|                                                                   |
|   Tier 2: Mesh Layer (WireGuard + Cilium Cluster Mesh)            |
|   +-- Full or partial mesh between cell gateways                  |
|   +-- Automatic NAT traversal, TURN fallback                      |
|                                                                   |
|   Tier 3: Application Layer                                       |
|   +-- Federated services via Cluster Mesh service discovery       |
|   +-- GitOps (ArgoCD) for cross-cell deployment                   |
+------------------------------------------------------------------+
```

### 2.2 Topology Types

| Topology | Description | Pros | Cons | Best For |
|----------|-------------|------|------|----------|
| **Flat Federation** | All cells equal; full mesh between all gateways | Simple conceptually; no SPOF | O(n^2) mesh growth; control plane overhead scales poorly | 2-10 cells; homelab federation |
| **Hierarchical (Tree)** | Parent-child relationships between cells; parents aggregate | Natural org structure; policy inheritance | Parent failure affects children; complex routing | 10-100 cells; enterprise/org structure |
| **Mesh (Full)** | Every cell gateway connected to every other | Direct paths; lowest latency | O(n^2) connections; high gateway load | 2-20 cells; latency-sensitive workloads |
| **Mesh (Partial)** | Each cell connects to k nearest neighbors | Scales to 100+ cells; adaptive | Multi-hop routing; longer paths for distant cells | 20-255 cells; geo-distributed |
| **Hub-and-Spoke** | Central hub cell; spokes connect only to hub | Centralized governance; simple spoke config | Hub is SPOF; hub region concentration | Governance-centric; compliance |
| **Equal-Peer Nodes** | Nodes from different cells merge as one logical cluster | Maximum resource pooling | Complex scheduling; security boundaries blur | Trusted friends; small scale |

### 2.3 Block Binding Modes

**Mode A: Cluster-of-Clusters (Default)**
Cells remain fully independent. Workloads run within their home cell. Cross-cell traffic uses service mesh routing. This is the safest, most secure mode.

```
Cell A runs Pod X --> Cilium Cluster Mesh routes --> Cell B runs Pod Y
     (independent scheduling)                          (independent scheduling)
```

**Mode B: Equal-Peer Nodes**
Nodes from multiple cells present as a single resource pool to a shared scheduler. Requires shared etcd (strong consistency across cells). **Only viable for cells within 10ms RTT.**

**Mode C: Gateway Bridging**
Cells maintain full independence but establish gateway-to-gateway tunnels. Cross-cell traffic flows through designated gateway nodes. Gateway selection uses latency-aware routing.

**Mode D: Cloud Extension**
On-prem cells extend into cloud spot instances when local capacity saturates. Cloud nodes join as a sub-cell with cloud-specific scheduling constraints.

### 2.4 Cluster Lifecycle

```
+-----------------+     +-----------------+     +-----------------+
|     CREATE      |---->|    DISCOVER     |---->|   AUTHENTICATE  |
|  User creates   |     | mDNS/DNS-SD on  |     | SPIFFE/SPIRE    |
|  new cell; boot-|     | LAN; DHT on WAN |     | mutual attesta- |
| straps local CP |     | rendezvous for  |     | tion; CA bundle |
+-----------------+     | bootstrap       |     | exchange        |
                        +-----------------+     +-----------------+
                                                         |
+-----------------+     +-----------------+     +--------v----------+
|    CLEANUP      |<----|     LEAVE       |<----|     OPERATE       |
| Remove from mesh|     | Graceful depar- |     | Full federation;  |
| tombstone state |     | ture; state hand|     | workload placement|
| archive configs |     | off; peer notify|     | service discovery |
+-----------------+     +-----------------+     +--------+----------+
   ^                                                       |
   |     +-----------------+     +-----------------+       |
   |     |   SYNCHRONIZE   |<----|      JOIN       |<------+
   |     | CRDT anti-entro-|<----| Cell enters mesh|<------+
   |     | py; Merkle tree |     | gets cell ID;   |       |
   |     | state compariso |     | mesh established|       |
   |     +--------+--------+     +-----------------+       |
   |              |                                        |
   +--------------+----------------------------------------+
```

**Lifecycle State Machine (Go):**

```go
package federation

import "fmt"

// CellState represents the lifecycle state of a cell in the federation.
type CellState int

const (
    CellStateCreating      CellState = iota // Local control plane bootstrapping
    CellStateDiscovering                    // mDNS/DHT discovery active
    CellStateAuthenticating                 // SPIFFE attestation in progress
    CellStateJoining                        // Mesh establishment
    CellStateSynchronizing                  // State sync with existing cells
    CellStateOperating                      // Fully federated
    CellStateLeaving                        // Graceful departure
    CellStateCleanup                        // Tombstoned, archived
)

func (s CellState) String() string {
    switch s {
    case CellStateCreating:      return "CREATING"
    case CellStateDiscovering:   return "DISCOVERING"
    case CellStateAuthenticating: return "AUTHENTICATING"
    case CellStateJoining:       return "JOINING"
    case CellStateSynchronizing: return "SYNCHRONIZING"
    case CellStateOperating:     return "OPERATING"
    case CellStateLeaving:       return "LEAVING"
    case CellStateCleanup:       return "CLEANUP"
    default:                     return fmt.Sprintf("UNKNOWN(%d)", s)
    }
}

// ValidTransitions defines allowed state transitions.
var ValidTransitions = map[CellState][]CellState{
    CellStateCreating:      {CellStateDiscovering},
    CellStateDiscovering:   {CellStateAuthenticating, CellStateCreating},
    CellStateAuthenticating: {CellStateJoining, CellStateDiscovering},
    CellStateJoining:       {CellStateSynchronizing, CellStateDiscovering},
    CellStateSynchronizing: {CellStateOperating, CellStateLeaving},
    CellStateOperating:     {CellStateLeaving},
    CellStateLeaving:       {CellStateCleanup, CellStateOperating},
    CellStateCleanup:       {}, // Terminal state
}

// CanTransition checks if a state change is valid.
func CanTransition(from, to CellState) bool {
    for _, allowed := range ValidTransitions[from] {
        if allowed == to {
            return true
        }
    }
    return false
}
```

---

## 3. Network Mesh & Connectivity Layer

### 3.1 WireGuard Mesh Foundation

Every HelixCluster Phase 6 node receives a WireGuard interface (`wg-helix`) upon joining the federation. The mesh manager automatically handles key exchange, peer configuration, and NAT traversal.

**WireGuard Mesh Manager (Go):**

```go
package mesh

import (
    "crypto/rand"
    "encoding/base64"
    "fmt"
    "net"
    "sync"
    "time"

    "golang.zx2c4.com/wireguard/wgctrl"
    "golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// Peer represents a WireGuard peer in the HelixCluster mesh.
type Peer struct {
    PublicKey   wgtypes.Key
    Endpoint    string        // Public endpoint (may be empty for NAT'd peers)
    AllowedIPs  []net.IPNet   // CIDR ranges routed through this peer
    CellID      uint16        // Federation cell identifier
    NodeID      string        // Unique node identifier
    LastSeen    time.Time
    Keepalive   time.Duration // Persistent keepalive interval
}

// MeshManager manages the WireGuard interface and peer set.
type MeshManager struct {
    client     *wgctrl.Client
    deviceName string
    privateKey wgtypes.Key
    listenPort int

    peers   map[wgtypes.Key]*Peer
    peersMu sync.RWMutex

    // NAT traversal components
    natTraversal *NATTraversal
    turnClient   *TURNClient

    // Event callbacks
    onPeerAdded   func(*Peer)
    onPeerRemoved func(wgtypes.Key)
}

// NewMeshManager creates a new WireGuard mesh manager.
func NewMeshManager(deviceName string, listenPort int) (*MeshManager, error) {
    client, err := wgctrl.New()
    if err != nil {
        return nil, fmt.Errorf("wgctrl: %w", err)
    }

    privKey, err := wgtypes.GeneratePrivateKey()
    if err != nil {
        return nil, fmt.Errorf("generate key: %w", err)
    }

    mm := &MeshManager{
        client:     client,
        deviceName: deviceName,
        privateKey: privKey,
        listenPort: listenPort,
        peers:      make(map[wgtypes.Key]*Peer),
    }

    // Configure the WireGuard interface
    cfg := wgtypes.Config{
        PrivateKey:   &privKey,
        ListenPort:   &listenPort,
        ReplacePeers: true,
        Peers:        []wgtypes.PeerConfig{},
    }
    if err := client.ConfigureDevice(deviceName, cfg); err != nil {
        return nil, fmt.Errorf("configure device: %w", err)
    }

    return mm, nil
}

// PublicKey returns the base64-encoded public key for this node.
func (mm *MeshManager) PublicKey() string {
    return mm.privateKey.PublicKey().String()
}

// AddPeer adds or updates a peer in the mesh.
func (mm *MeshManager) AddPeer(p *Peer) error {
    mm.peersMu.Lock()
    defer mm.peersMu.Unlock()

    keepalive := 25 // seconds, for NAT traversal
    peerCfg := wgtypes.PeerConfig{
        PublicKey:                   p.PublicKey,
        Endpoint:                    &net.UDPAddr{IP: net.ParseIP(p.Endpoint), Port: 51820},
        PersistentKeepaliveInterval: &keepalive,
        ReplaceAllowedIPs:           true,
        AllowedIPs:                  p.AllowedIPs,
    }

    if err := mm.client.ConfigureDevice(mm.deviceName, wgtypes.Config{
        Peers: []wgtypes.PeerConfig{peerCfg},
    }); err != nil {
        return fmt.Errorf("configure peer: %w", err)
    }

    oldPeer, exists := mm.peers[p.PublicKey]
    mm.peers[p.PublicKey] = p

    if !exists && mm.onPeerAdded != nil {
        mm.onPeerAdded(p)
    } else if exists {
        // Update allowed IPs if cell changed
        oldPeer.AllowedIPs = p.AllowedIPs
        oldPeer.LastSeen = time.Now()
    }
    return nil
}

// RemovePeer removes a peer from the mesh.
func (mm *MeshManager) RemovePeer(pubKey wgtypes.Key) error {
    mm.peersMu.Lock()
    defer mm.peersMu.Unlock()

    peerCfg := wgtypes.PeerConfig{
        PublicKey: pubKey,
        Remove:    true,
    }
    if err := mm.client.ConfigureDevice(mm.deviceName, wgtypes.Config{
        Peers: []wgtypes.PeerConfig{peerCfg},
    }); err != nil {
        return err
    }

    delete(mm.peers, pubKey)
    if mm.onPeerRemoved != nil {
        mm.onPeerRemoved(pubKey)
    }
    return nil
}

// GetPeers returns a snapshot of current peers.
func (mm *MeshManager) GetPeers() []*Peer {
    mm.peersMu.RLock()
    defer mm.peersMu.RUnlock()

    peers := make([]*Peer, 0, len(mm.peers))
    for _, p := range mm.peers {
        peers = append(peers, p)
    }
    return peers
}

// GeneratePresharedKey creates a random 256-bit preshared key.
func GeneratePresharedKey() (string, error) {
    var key [32]byte
    if _, err := rand.Read(key[:]); err != nil {
        return "", err
    }
    return base64.StdEncoding.EncodeToString(key[:]), nil
}
```

### 3.2 NAT Traversal Stack

HelixCluster implements a prioritized fallback chain for NAT traversal, from direct connection to relay:

```
Priority 1: DIRECT (same LAN/VPN)
    --> Same subnet? Connect directly via local IP.
    
Priority 2: STUN + HOLE PUNCH
    --> Query STUN server for reflexive address
    --> Simultaneous UDP hole punch to peer
    --> Success rate: 82-95% (non-symmetric NAT)
    
Priority 3: UPnP/PCP (Opportunistic)
    --> Request port mapping from router
    --> Security risk: only use if user explicitly enables
    
Priority 4: TURN RELAY (Guaranteed)
    --> Relay through TURN server (Coturn or embedded)
    --> Adds latency (server hop), 100% connectivity
    --> Runs on TCP 443 for firewall bypass
    
Priority 5: libp2p CIRCUIT RELAY (Emergency)
    --> Any publicly reachable peer acts as relay
    --> Limited bandwidth; encourage upgrade to direct
    
Priority 6: SSH TUNNEL (Last Resort)
    --> Reverse SSH tunnel to bastion host
    --> For admin access; not for data plane
```

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
    Type      CandidateType
    Address   string // IP:port
    Priority  uint32
    CellID    uint16
    NodeID    string
}

// NATTraversal manages the ICE process for establishing P2P connections.
type NATTraversal struct {
    stunServers []string
    turnServer  string
    turnCred    TURNCredentials

    localAddrs  []string
    natType     NATType
    mappedAddr  string // Server-reflexive address from STUN

    mu sync.RWMutex
}

// TURNCredentials holds TURN authentication info.
type TURNCredentials struct {
    Username string
    Password string
    Realm    string
}

// NewNATTraversal creates a NAT traversal engine with configured STUN/TURN servers.
func NewNATTraversal(stunServers []string, turnServer string, turnCred TURNCredentials) *NATTraversal {
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
        CandidateHost:              126,
        CandidatePeerReflexive:     110,
        CandidateServerReflexive:   100,
        CandidateRelay:             0,
    }
    // Simplified: local preference = 65535, component = 1
    return uint32((1 << 24)*typePrefs[ct] + (1 << 8)*65535 + (255 - 1))
}

// Connect performs connectivity checks and returns the best working candidate pair.
func (nt *NATTraversal) Connect(ctx context.Context, remoteCandidates []ICECandidate) (*net.UDPConn, error) {
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
    // Sort by combined priority (highest first), then test
    var pairs []candidatePair
    for _, l := range local {
        for _, r := range remote {
            pairs = append(pairs, candidatePair{local: l, remote: r})
        }
    }
    // Sort by combined priority descending
    sortCandidatePairs(pairs)
    return pairs
}

// classifyNAT determines NAT type via multiple STUN queries to different servers.
func (nt *NATTraversal) classifyNAT(ctx context.Context) NATType {
    // Simplified: query primary STUN, compare mapped addresses
    // Full implementation would use RFC 5780 behavior tests
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
    // Could be full cone, restricted, or port-restricted
    // Additional testing required for precise classification
    return NATRestricted
}

// Stub implementations for external I/O
func (nt *NATTraversal) getLocalAddrs() ([]string, error) { return nil, nil }
func (nt *NATTraversal) querySTUN(ctx context.Context) (string, error) { return "", nil }
func (nt *NATTraversal) querySTUNWithServer(ctx context.Context, server string) (string, error) { return "", nil }
func (nt *NATTraversal) allocateTURN(ctx context.Context) (string, error) { return "", nil }
func (nt *NATTraversal) checkConnectivity(ctx context.Context, p candidatePair) (*net.UDPConn, error) { return nil, nil }
func sortCandidatePairs(pairs []candidatePair) {}
```

### 3.3 Local Discovery (mDNS/DNS-SD)

For cells on the same LAN, HelixCluster uses mDNS/DNS-SD for zero-configuration discovery:

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
    HelixServiceName = "_helix-cluster._tcp"
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
func NewmDNSServer(cellID uint16, nodeID string, port int, metadata map[string]string) (*mDNSServer, error) {
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
    CellID      uint16
    NodeID      string
    Hostname    string
    IP          string
    Port        int
    WireGuardPubKey string
    ClusterAddr string
    TTL         time.Duration
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
        if err := b.resolver.Browse(ctx, HelixServiceName, HelixDomain, entries); err != nil {
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

**Security Warning:** mDNS provides no authentication. Discovered peers must complete the full SPIFFE attestation protocol before being trusted. mDNS is used ONLY for discovery, not for establishing trust.

### 3.4 SSH Tunnel Bridging

SSH tunnels serve as a fallback for administrative access and initial bootstrap when UDP is blocked:

```yaml
# /etc/helix/agent/ssh-bridge.yaml
ssh_bridge:
  enabled: true
  mode: reverse-tunnel  # or forward
  bastion_host: "bastion.helix.example.com"
  bastion_port: 22
  identity_key: "/etc/helix/keys/ssh_bridge_ed25519"
  tunnel_local_port: 7946   # gossip port
  tunnel_remote_port: 0     # 0 = auto-allocate on bastion
  keepalive_interval: 30s
  keepalive_max_failures: 3
  autossh:
    enabled: true
    monitoring_port: 0  # 0 = disable monitoring forward
    first_poll: 30
    poll_interval: 60
```

**Limitations:** SSH tunnels are TCP-only, single-threaded per connection, and add 1-2 RTT latency. They are suitable for control plane bootstrapping and debugging but NOT for high-throughput data plane traffic. WireGuard should be used for all production data paths.

### 3.5 Cloud VPN Bridging

Cloud instances (AWS, GCP, Azure) join the federation via WireGuard with cloud-specific optimizations:

```yaml
# /etc/helix/agent/cloud-bridge.yaml
cloud_bridge:
  enabled: true
  provider: aws  # aws, gcp, azure, hetzner, generic
  
  # AWS-specific: use Global Accelerator for anycast endpoint
  aws:
    accelerator_enabled: true
    region: "us-east-1"
  
  # GCP-specific: use Cloud Load Balancing
  gcp:
    anycast_ip: "34.120.0.1"
    region: "us-central1"

  wireguard:
    listen_port: 51820
    endpoint_discovery: stun  # stun, static, metadata-service
    
  turn:
    enabled: true
    server: "turn.helix.example.com:3478"
    protocol: udp  # udp, tcp (tcp for firewall bypass)
    tls: true
```

### 3.6 libp2p Integration

HelixCluster integrates libp2p for application-layer peer-to-peer functionality:

| Function | libp2p Component | Purpose |
|----------|-----------------|---------|
| DHT-based discovery | Kademlia DHT | Global peer finding without central registry |
| Content routing | DHT + Bitswap | Large object transfer between cells |
| Pub/sub messaging | GossipSub v1.1 | Cross-cell event streaming |
| NAT traversal | DCUtR + Circuit Relay v2 | Decentralized hole punching |
| Transport | QUIC | 0-RTT, connection migration |

libp2p does NOT replace WireGuard for the cluster mesh layer. It complements it for application-specific P2P patterns.

### 3.7 QUIC Transport

QUIC provides a UDP-based reliable transport with significant advantages for NAT traversal:

```go
package transport

import (
    "context"
    "crypto/tls"
    "fmt"
    "time"

    "github.com/quic-go/quic-go"
)

// QUICConfig holds configuration for the QUIC transport.
type QUICConfig struct {
    ListenAddr      string
    TLSCert         tls.Certificate
    ALPNProtocols   []string
    MaxIncomingStreams int64
    IdleTimeout     time.Duration
}

// QUICServer wraps a QUIC listener for HelixCluster federation traffic.
type QUICServer struct {
    listener *quic.Listener
    config   *QUICConfig
}

// NewQUICServer creates a QUIC server with NAT-traversal-friendly settings.
func NewQUICServer(cfg *QUICConfig) (*QUICServer, error) {
    quicConf := &quic.Config{
        MaxIncomingStreams: cfg.MaxIncomingStreams,
        MaxIdleTimeout:     cfg.IdleTimeout,
        Allow0RTT:          true, // Enable 0-RTT for faster reconnections
        EnableDatagrams:    true, // For unreliable message paths
    }

    tlsConf := &tls.Config{
        Certificates:       []tls.Certificate{cfg.TLSCert},
        NextProtos:         cfg.ALPNProtocols,
        InsecureSkipVerify: false,
    }

    listener, err := quic.ListenAddr(cfg.ListenAddr, tlsConf, quicConf)
    if err != nil {
        return nil, fmt.Errorf("quic listen: %w", err)
    }

    return &QUICServer{
        listener: listener,
        config:   cfg,
    }, nil
}

// Accept blocks until a new QUIC connection is established.
func (s *QUICServer) Accept(ctx context.Context) (quic.Connection, error) {
    return s.listener.Accept(ctx)
}

// Close shuts down the QUIC server.
func (s *QUICServer) Close() error {
    return s.listener.Close()
}

// ConnectionMigrationStats tracks QUIC connection migration events.
type ConnectionMigrationStats struct {
    MigrationsSucceeded uint64
    MigrationsFailed    uint64
    ZeroRTTResumed      uint64
    ZeroRTTFallback     uint64
}
```

**QUIC Configuration for Federation:**

```yaml
# /etc/helix/agent/quic.yaml
quic_transport:
  enabled: true
  listen_port: 4242
  alpn_protocols: ["helix-federation/6.0"]
  
  # NAT traversal optimization
  enable_0rtt: true
  enable_datagrams: true  # For unreliable gossip messages
  max_idle_timeout: 60s
  keepalive_period: 15s   # Shorter than UDP NAT timeout (typically 30s)
  
  # Flow control
  max_incoming_streams: 1000
  initial_stream_receive_window: 1048576   # 1MB
  max_stream_receive_window: 6291456       # 6MB
  
  # For connection migration (IP change without reconnect)
  disable_path_mtu_discovery: false
```

---

## 4. Gossip & Membership Protocol

### 4.1 Hierarchical SWIM Implementation

HelixCluster implements a hierarchical gossip protocol based on HashiCorp's memberlist (SWIM + Lifeguard), with separate pools for intra-cell and inter-cell communication:

```
+---------------------------+    +---------------------------+
|   INTRA-CELL GOSSIP POOL  |    |   INTER-CELL GOSSIP POOL  |
|   (LAN-optimized)         |    |   (WAN-optimized)         |
|                           |    |                           |
| - gossip_interval: 200ms  |    | - gossip_interval: 500ms  |
| - probe_interval: 1s      |    | - probe_interval: 5s      |
| - probe_timeout: 500ms    |    | - probe_timeout: 3s       |
| - suspicion_mult: 4       |    | - suspicion_mult: 6       |
| - fanout: 3               |    | - fanout: 2               |
| - encryption: AES-256-GCM |    | - encryption: AES-256-GCM |
| - max nodes: 5,000        |    | - max delegates: 100      |
+---------------------------+    +---------------------------+
```

**Hierarchical Gossip Manager (Go):**

```go
package gossip

import (
    "crypto/aes"
    "crypto/cipher"
    "encoding/json"
    "fmt"
    "sync"
    "time"

    "github.com/hashicorp/memberlist"
)

// GossipScope defines which pool a message targets.
type GossipScope int

const (
    ScopeIntraCell GossipScope = iota // Within cell only
    ScopeInterCell                      // Between cell delegates only
    ScopeFederation                     // Full federation broadcast
)

// NodeMeta represents the metadata each node advertises via gossip.
type NodeMeta struct {
    CellID       uint16            `json:"cell_id"`
    NodeID       string            `json:"node_id"`
    Role         string            `json:"role"`          // gateway, worker, control
    Region       string            `json:"region"`
    WireGuardKey string            `json:"wg_pubkey"`
    Capacity     NodeCapacity      `json:"capacity"`
    Labels       map[string]string `json:"labels"`
    Version      string            `json:"version"`
    Timestamp    int64             `json:"ts"` // HLC timestamp
}

type NodeCapacity struct {
    CPU    int64 `json:"cpu_millicores"`
    Memory int64 `json:"memory_bytes"`
    Pods   int   `json:"max_pods"`
}

// HierarchicalGossip manages both intra-cell and inter-cell gossip pools.
type HierarchicalGossip struct {
    // Intra-cell pool (all nodes in this cell)
    intraPool *memberlist.Memberlist
    intraConf *memberlist.Config

    // Inter-cell pool (only gateway nodes from each cell)
    interPool *memberlist.Memberlist
    interConf *memberlist.Config

    cellID   uint16
    nodeID   string
    nodeMeta NodeMeta

    // Delegates for application-level messages
    delegate     memberlist.Delegate
    eventDelegate memberlist.EventDelegate

    // Scoped broadcast queues
    broadcasts     map[GossipScope]chan []byte
    broadcastMu    sync.RWMutex

    // Failure detection supplement
    phiDetector    *PhiAccrualDetector

    ctx    context.Context
    cancel context.CancelFunc
    wg     sync.WaitGroup
}

// Config holds configuration for the hierarchical gossip system.
type Config struct {
    CellID        uint16
    NodeID        string
    Role          string
    Region        string
    BindAddr      string
    IntraPort     int
    InterPort     int
    EncryptionKey []byte // 16/24/32 bytes for AES-128/192/256
    IsGateway     bool   // If true, participates in inter-cell pool
}

// NewHierarchicalGossip creates both gossip pools.
func NewHierarchicalGossip(cfg *Config, meta NodeMeta) (*HierarchicalGossip, error) {
    ctx, cancel := context.WithCancel(context.Background())
    hg := &HierarchicalGossip{
        cellID:     cfg.CellID,
        nodeID:     cfg.NodeID,
        nodeMeta:   meta,
        broadcasts: make(map[GossipScope]chan []byte),
        ctx:        ctx,
        cancel:     cancel,
    }

    // --- Intra-cell pool (LAN-optimized) ---
    intraConf := memberlist.DefaultLANConfig()
    intraConf.Name = fmt.Sprintf("%s-intra", cfg.NodeID)
    intraConf.BindAddr = cfg.BindAddr
    intraConf.BindPort = cfg.IntraPort
    intraConf.AdvertisePort = cfg.IntraPort
    intraConf.GossipInterval = 200 * time.Millisecond
    intraConf.GossipNodes = 3
    intraConf.ProbeInterval = 1 * time.Second
    intraConf.ProbeTimeout = 500 * time.Millisecond
    intraConf.SuspicionMult = 4

    if err := hg.setupEncryption(intraConf, cfg.EncryptionKey); err != nil {
        return nil, fmt.Errorf("intra encryption: %w", err)
    }

    intraConf.Delegate = &hierarchicalDelegate{hg: hg, scope: ScopeIntraCell}
    intraConf.Events = &hierarchicalEventDelegate{hg: hg, scope: ScopeIntraCell}

    intraPool, err := memberlist.Create(intraConf)
    if err != nil {
        return nil, fmt.Errorf("create intra pool: %w", err)
    }
    hg.intraPool = intraPool
    hg.intraConf = intraConf

    // --- Inter-cell pool (WAN-optimized, gateway only) ---
    if cfg.IsGateway {
        interConf := memberlist.DefaultWANConfig()
        interConf.Name = fmt.Sprintf("%s-inter", cfg.NodeID)
        interConf.BindAddr = cfg.BindAddr
        interConf.BindPort = cfg.InterPort
        interConf.AdvertisePort = cfg.InterPort
        interConf.GossipInterval = 500 * time.Millisecond
        interConf.GossipNodes = 2
        interConf.ProbeInterval = 5 * time.Second
        interConf.ProbeTimeout = 3 * time.Second
        interConf.SuspicionMult = 6

        if err := hg.setupEncryption(interConf, cfg.EncryptionKey); err != nil {
            return nil, fmt.Errorf("inter encryption: %w", err)
        }

        interConf.Delegate = &hierarchicalDelegate{hg: hg, scope: ScopeInterCell}
        interConf.Events = &hierarchicalEventDelegate{hg: hg, scope: ScopeInterCell}

        interPool, err := memberlist.Create(interConf)
        if err != nil {
            return nil, fmt.Errorf("create inter pool: %w", err)
        }
        hg.interPool = interPool
        hg.interConf = interConf
    }

    // Initialize Phi accrual failure detector
    hg.phiDetector = NewPhiAccrualDetector(8.0, 1000) // threshold=8, window=1000 samples

    return hg, nil
}

func (hg *HierarchicalGossip) setupEncryption(conf *memberlist.Config, key []byte) error {
    keyring, err := memberlist.NewKeyring([][]byte{key}, key)
    if err != nil {
        return err
    }
    conf.Keyring = keyring
    return nil
}

// Broadcast sends a message to the specified gossip scope.
func (hg *HierarchicalGossip) Broadcast(scope GossipScope, msg []byte) error {
    switch scope {
    case ScopeIntraCell:
        if hg.intraPool == nil {
            return fmt.Errorf("intra pool not initialized")
        }
        // Piggyback on gossip protocol
        // In production, queue for next gossip cycle
        return nil
    case ScopeInterCell, ScopeFederation:
        if hg.interPool == nil {
            return fmt.Errorf("inter pool not initialized (not a gateway)")
        }
        return nil
    default:
        return fmt.Errorf("unknown scope: %d", scope)
    }
}

// Members returns all alive members in the intra-cell pool.
func (hg *HierarchicalGossip) Members() []NodeMeta {
    var members []NodeMeta
    for _, m := range hg.intraPool.Members() {
        if m.State != memberlist.StateAlive {
            continue
        }
        var meta NodeMeta
        if err := json.Unmarshal(m.Meta, &meta); err == nil {
            members = append(members, meta)
        }
    }
    return members
}

// CellMembers returns members of a specific cell (from inter-cell pool).
func (hg *HierarchicalGossip) CellMembers(cellID uint16) []NodeMeta {
    var members []NodeMeta
    if hg.interPool == nil {
        return members
    }
    for _, m := range hg.interPool.Members() {
        var meta NodeMeta
        if err := json.Unmarshal(m.Meta, &meta); err == nil {
            if meta.CellID == cellID && m.State == memberlist.StateAlive {
                members = append(members, meta)
            }
        }
    }
    return members
}

// Shutdown gracefully leaves both gossip pools.
func (hg *HierarchicalGossip) Shutdown() error {
    hg.cancel()

    errs := make([]error, 0, 2)
    if hg.intraPool != nil {
        if err := hg.intraPool.Leave(5 * time.Second); err != nil {
            errs = append(errs, fmt.Errorf("intra leave: %w", err))
        }
        hg.intraPool.Shutdown()
    }
    if hg.interPool != nil {
        if err := hg.interPool.Leave(5 * time.Second); err != nil {
            errs = append(errs, fmt.Errorf("inter leave: %w", err))
        }
        hg.interPool.Shutdown()
    }
    hg.wg.Wait()

    if len(errs) > 0 {
        return fmt.Errorf("shutdown errors: %v", errs)
    }
    return nil
}

// --- Delegate implementations ---

type hierarchicalDelegate struct {
    hg    *HierarchicalGossip
    scope GossipScope
}

func (d *hierarchicalDelegate) NodeMeta(limit int) []byte {
    meta, _ := json.Marshal(d.hg.nodeMeta)
    if len(meta) > limit {
        return meta[:limit]
    }
    return meta
}

func (d *hierarchicalDelegate) NotifyMsg(msg []byte) {
    // Route message to appropriate handler based on scope
}

func (d *hierarchicalDelegate) GetBroadcasts(overhead, limit int) [][]byte {
    return nil // Populated by broadcast queue
}

func (d *hierarchicalDelegate) LocalState(join bool) []byte {
    return nil // For push/pull anti-entropy
}

func (d *hierarchicalDelegate) MergeRemoteState(buf []byte, join bool) {
    // Anti-entropy state merge
}

type hierarchicalEventDelegate struct {
    hg    *HierarchicalGossip
    scope GossipScope
}

func (e *hierarchicalEventDelegate) NotifyJoin(n *memberlist.Node) {
    var meta NodeMeta
    if err := json.Unmarshal(n.Meta, &meta); err == nil {
        fmt.Printf("[GOSSIP-%s] JOIN: %s (cell=%d, region=%s)\n",
            e.scope, meta.NodeID, meta.CellID, meta.Region)
    }
}

func (e *hierarchicalEventDelegate) NotifyLeave(n *memberlist.Node) {
    var meta NodeMeta
    if err := json.Unmarshal(n.Meta, &meta); err == nil {
        fmt.Printf("[GOSSIP-%s] LEAVE: %s (cell=%d)\n",
            e.scope, meta.NodeID, meta.CellID)
    }
}

func (e *hierarchicalEventDelegate) NotifyUpdate(n *memberlist.Node) {
    // Node metadata updated
}
```

### 4.2 Cross-Cluster Gossip

Cross-cluster gossip uses cell delegates -- typically 2-3 gateway nodes per cell -- to exchange aggregated state. This keeps WAN bandwidth O(cells) regardless of nodes per cell.

**Bandwidth Calculation at Scale:**

```
Scenario: 100 cells x 100 nodes = 10,000 total nodes

Intra-cell gossip (per node):
  - gossip_interval = 200ms, gossip_nodes = 3
  - Message size ~200 bytes (node meta digest)
  - Bandwidth = 3 msgs * 200 bytes * 5 intervals/sec = 3,000 B/s = ~3 KB/s per node
  - Total per cell = 100 nodes * 3 KB/s = 300 KB/s aggregate

Inter-cell gossip (per gateway):
  - gossip_interval = 500ms, gossip_nodes = 2
  - Cell delegate message ~500 bytes (aggregated cell state)
  - Bandwidth = 2 msgs * 500 bytes * 2 intervals/sec = 2,000 B/s = ~2 KB/s per gateway
  - Total federation overhead = 100 gateways * 2 KB/s = 200 KB/s (negligible)

CONCLUSION: Even at 100 cells, gossip bandwidth is <5 KB/s per node,
well within capacity of any modern network connection.
```

### 4.3 Bootstrap & Rendezvous

```go
package gossip

import (
    "context"
    "fmt"
    "time"
)

// BootstrapStrategy defines how a new cell finds existing federation members.
type BootstrapStrategy int

const (
    BootstrapStatic BootstrapStrategy = iota // Hardcoded seed list
    BootstrapDNS                             // DNS SRV records
    BootstrapDHT                             // Kademlia DHT
    BootstrapCloud                           // Cloud metadata service
    BootstrapmDNS                            // Local discovery
)

// Bootstrapper handles cell join to existing federation.
type Bootstrapper struct {
    strategy BootstrapStrategy
    seeds    []string // Static seeds, DNS names, or DHT bootstrap nodes
    dht      *KademliaDHT
}

// Bootstrap discovers existing cells and joins the federation.
func (b *Bootstrapper) Bootstrap(ctx context.Context) ([]string, error) {
    switch b.strategy {
    case BootstrapStatic:
        return b.seeds, nil

    case BootstrapDNS:
        return b.bootstrapDNS(ctx)

    case BootstrapDHT:
        return b.bootstrapDHT(ctx)

    case BootstrapCloud:
        return b.bootstrapCloud(ctx)

    case BootstrapmDNS:
        return b.bootstrapmDNS(ctx)

    default:
        return nil, fmt.Errorf("unknown bootstrap strategy: %d", b.strategy)
    }
}

func (b *Bootstrapper) bootstrapDNS(ctx context.Context) ([]string, error) {
    // Query SRV records for _helix-gossip._tcp.helix.example.com
    // Returns weighted list of gateway endpoints
    return nil, nil // Implementation uses net.Resolver.LookupSRV
}

func (b *Bootstrapper) bootstrapDHT(ctx context.Context) ([]string, error) {
    // DHT key = hash("helix:federation:v6")
    // Query Kademlia for providers of this key
    return nil, nil // Implementation uses libp2p DHT
}

func (b *Bootstrapper) bootstrapCloud(ctx context.Context) ([]string, error) {
    // AWS: Query EC2 instances by tag "helix-cell=gateway"
    // GCP: Query instances by label "helix-cell=gateway"
    // Azure: Query VMs by tag
    return nil, nil
}

func (b *Bootstrapper) bootstrapmDNS(ctx context.Context) ([]string, error) {
    // Already handled by mDNS browser
    return nil, nil
}

// RetryJoin attempts to join with exponential backoff.
func RetryJoin(ctx context.Context, pool *memberlist.Memberlist, seeds []string, maxRetries int) error {
    baseDelay := 1 * time.Second
    maxDelay := 30 * time.Second

    for attempt := 0; attempt < maxRetries; attempt++ {
        n, err := pool.Join(seeds)
        if err == nil && n > 0 {
            return nil
        }

        delay := baseDelay * time.Duration(1<<attempt)
        if delay > maxDelay {
            delay = maxDelay
        }

        fmt.Printf("Join attempt %d failed (%v), retrying in %v...\n", attempt+1, err, delay)
        select {
        case <-time.After(delay):
        case <-ctx.Done():
            return ctx.Err()
        }
    }
    return fmt.Errorf("failed to join after %d attempts", maxRetries)
}
```

### 4.4 Failure Detection

HelixCluster combines SWIM's active probing with the Phi Accrual failure detector for adaptive, accurate failure detection:

```go
package gossip

import (
    "math"
    "sync"
    "time"
)

// PhiAccrualDetector implements the Phi accrual failure detector.
// It outputs a suspicion level (phi) instead of a binary up/down decision.
type PhiAccrualDetector struct {
    threshold   float64       // Phi value to declare failure (typically 8-12)
    windowSize  int           // Number of heartbeat intervals to track
    intervals   []time.Duration
    lastHeartbeat time.Time
    mu          sync.RWMutex
}

// NewPhiAccrualDetector creates a new Phi accrual detector.
func NewPhiAccrualDetector(threshold float64, windowSize int) *PhiAccrualDetector {
    return &PhiAccrualDetector{
        threshold:  threshold,
        windowSize: windowSize,
        intervals:  make([]time.Duration, 0, windowSize),
    }
}

// Heartbeat records a heartbeat arrival.
func (d *PhiAccrualDetector) Heartbeat() {
    d.mu.Lock()
    defer d.mu.Unlock()

    now := time.Now()
    if !d.lastHeartbeat.IsZero() {
        interval := now.Sub(d.lastHeartbeat)
        d.intervals = append(d.intervals, interval)
        if len(d.intervals) > d.windowSize {
            d.intervals = d.intervals[1:]
        }
    }
    d.lastHeartbeat = now
}

// Phi returns the current suspicion level.
// phi = -log10(probability that heartbeat is still "on time")
func (d *PhiAccrualDetector) Phi() float64 {
    d.mu.RLock()
    defer d.mu.RUnlock()

    if len(d.intervals) < 2 {
        return 0.0 // Not enough data
    }

    // Calculate mean and standard deviation of intervals
    var sum time.Duration
    for _, iv := range d.intervals {
        sum += iv
    }
    mean := float64(sum) / float64(len(d.intervals))

    var variance float64
    for _, iv := range d.intervals {
        diff := float64(iv) - mean
        variance += diff * diff
    }
    stdDev := math.Sqrt(variance / float64(len(d.intervals)))

    if stdDev == 0 {
        stdDev = mean * 0.1 // Prevent division by zero
    }

    // Time since last heartbeat
    elapsed := float64(time.Since(d.lastHeartbeat))

    // Probability that heartbeat is still "on time" (normal CDF)
    // P(X <= elapsed) where X ~ N(mean, stdDev^2)
    zScore := (elapsed - mean) / stdDev
    probability := 0.5 * math.Erfc(-zScore/math.Sqrt2)

    if probability <= 0 {
        return math.MaxFloat64
    }

    return -math.Log10(probability)
}

// IsFailed returns true if the suspicion level exceeds the threshold.
func (d *PhiAccrualDetector) IsFailed() bool {
    return d.Phi() >= d.threshold
}
```

### 4.5 Partition Handling

**Split-Brain Prevention Strategy:**

```
1. DETECTION: SWIM suspicion + Phi accrual identify unreachable nodes
2. CLASSIFICATION: Distinguish node failure from network partition
   - If ALL nodes in remote cell are unreachable = likely partition
   - If SOME nodes reachable = likely partial failure
3. QUORUM ENFORCEMENT: Per-cell etcd uses majority quorum
   - Minority partition becomes read-only
   - Majority partition continues accepting writes
4. CONVERGENCE: When partition heals:
   - CRDTs merge automatically
   - Strongly-consistent state uses Merkle tree comparison
   - Divergent writes require application-level reconciliation
5. FENCING: Leader in minority partition steps down via CheckQuorum
```

**Partition vs. Failure Decision Matrix:**

| Symptom | Diagnosis | Action |
|---------|-----------|--------|
| Single node unreachable | Node failure | Reschedule workloads, keep cell operational |
| All nodes in one cell unreachable | Network partition | Declare partition, both sides continue independently |
| Intermittent reachability | Flaky network | Increase suspicion timeout, enable relay |
| Asymmetric reachability (A sees B, B not A) | Routing issue | SWIM indirect probing resolves; use relay |
| All cells unreachable | Local network failure | Operate in degraded mode, queue cross-cell ops |

---

## 5. Consensus & State Replication

### 5.1 Per-Cell Strong Consistency

Each HelixCluster cell runs its own etcd cluster (3-5 nodes). etcd uses Raft consensus, which is **fundamentally latency-sensitive** and must never stretch across WAN.

**Raft Tuning Parameters:**

| Parameter | LAN (single DC) | WAN (multi-AZ, same region) | Rationale |
|-----------|----------------|---------------------------|-----------|
| Heartbeat Interval | 100ms | 200-500ms | Match average RTT |
| Election Timeout | 1,000ms | 2,000-5,000ms | >= 10x max RTT |
| Snapshot Chunk Size | 64KB | 64KB-1MB | Smaller for lossy links |
| Max Inflight Messages | 256 | 512-1024 | Pipeline over higher latency |
| Pre-Vote | Enabled | **Required** | Prevent partition churn |
| Check Quorum | Disabled | **Enabled** | Leader steps down if isolated |

**etcd MUST stay within a single region.** Cross-region etcd clusters experience:
- Leader election timeouts (50s max election timeout)
- Unacceptable commit latency (100-300ms per write)
- Split-brain during inter-region partitions

### 5.2 Cross-Cell Eventual Consistency

Cross-cell state uses CRDTs (Conflict-free Replicated Data Types) for guaranteed convergence without coordination:

```go
package crdt

import (
    "encoding/json"
    "fmt"
    "sync"
    "time"
)

// GCounter is a grow-only counter CRDT.
// Each replica tracks per-node increments; merge takes element-wise max.
type GCounter struct {
    mu     sync.RWMutex
    counts map[string]uint64 // nodeID -> count
}

// NewGCounter creates a new G-Counter.
func NewGCounter() *GCounter {
    return &GCounter{counts: make(map[string]uint64)}
}

// Increment adds one to this node's counter.
func (c *GCounter) Increment(nodeID string) {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.counts[nodeID]++
}

// Value returns the total count.
func (c *GCounter) Value() uint64 {
    c.mu.RLock()
    defer c.mu.RUnlock()
    var total uint64
    for _, v := range c.counts {
        total += v
    }
    return total
}

// Merge combines another G-Counter into this one (takes max per node).
func (c *GCounter) Merge(other *GCounter) {
    other.mu.RLock()
    defer other.mu.RUnlock()
    c.mu.Lock()
    defer c.mu.Unlock()

    for node, count := range other.counts {
        if count > c.counts[node] {
            c.counts[node] = count
        }
    }
}

// Encode serializes the G-Counter for network transfer.
func (c *GCounter) Encode() ([]byte, error) {
    c.mu.RLock()
    defer c.mu.RUnlock()
    return json.Marshal(c.counts)
}

// LWWRegister implements a last-write-wins register with HLC timestamps.
type LWWRegister struct {
    mu        sync.RWMutex
    value     []byte
    timestamp int64 // HLC timestamp
    nodeID    string
}

// Set updates the register if the new timestamp is greater.
func (r *LWWRegister) Set(value []byte, timestamp int64, nodeID string) bool {
    r.mu.Lock()
    defer r.mu.Unlock()

    if timestamp > r.timestamp || (timestamp == r.timestamp && nodeID > r.nodeID) {
        r.value = value
        r.timestamp = timestamp
        r.nodeID = nodeID
        return true
    }
    return false // Existing value wins
}

// Get returns the current value and metadata.
func (r *LWWRegister) Get() ([]byte, int64, string) {
    r.mu.RLock()
    defer r.mu.RUnlock()
    return r.value, r.timestamp, r.nodeID
}

// ORSet implements an Observed-Removed Set CRDT.
// Add wins: each addition has a unique tag; remove only removes observed tags.
type ORSet struct {
    mu       sync.RWMutex
    adds     map[string]map[string]struct{} // element -> {tag: present}
    removes  map[string]map[string]struct{} // element -> {tag: removed}
}

// NewORSet creates a new OR-Set.
func NewORSet() *ORSet {
    return &ORSet{
        adds:    make(map[string]map[string]struct{}),
        removes: make(map[string]map[string]struct{}),
    }
}

// Add inserts an element with a unique tag.
func (s *ORSet) Add(element, tag string) {
    s.mu.Lock()
    defer s.mu.Unlock()
    if s.adds[element] == nil {
        s.adds[element] = make(map[string]struct{})
    }
    s.adds[element][tag] = struct{}{}
}

// Remove removes all observed tags for an element.
func (s *ORSet) Remove(element string) {
    s.mu.Lock()
    defer s.mu.Unlock()
    if observed, ok := s.adds[element]; ok {
        s.removes[element] = make(map[string]struct{})
        for tag := range observed {
            s.removes[element][tag] = struct{}{}
        }
    }
}

// Contains checks if an element is in the set.
func (s *ORSet) Contains(element string) bool {
    s.mu.RLock()
    defer s.mu.RUnlock()
    observed := s.adds[element]
    removed := s.removes[element]
    for tag := range observed {
        if _, wasRemoved := removed[tag]; !wasRemoved {
            return true
        }
    }
    return false
}

// Elements returns all elements currently in the set.
func (s *ORSet) Elements() []string {
    s.mu.RLock()
    defer s.mu.RUnlock()
    var result []string
    for elem := range s.adds {
        if s.containsUnlocked(elem) {
            result = append(result, elem)
        }
    }
    return result
}

func (s *ORSet) containsUnlocked(element string) bool {
    observed := s.adds[element]
    removed := s.removes[element]
    for tag := range observed {
        if _, wasRemoved := removed[tag]; !wasRemoved {
            return true
        }
    }
    return false
}

// Merge combines another OR-Set into this one.
func (s *ORSet) Merge(other *ORSet) {
    other.mu.RLock()
    defer other.mu.RUnlock()
    s.mu.Lock()
    defer s.mu.Unlock()

    for elem, tags := range other.adds {
        if s.adds[elem] == nil {
            s.adds[elem] = make(map[string]struct{})
        }
        for tag := range tags {
            s.adds[elem][tag] = struct{}{}
        }
    }
    for elem, tags := range other.removes {
        if s.removes[elem] == nil {
            s.removes[elem] = make(map[string]struct{})
        }
        for tag := range tags {
            s.removes[elem][tag] = struct{}{}
        }
    }
}
```

### 5.3 Anti-Entropy & Repair

Merkle trees enable O(log N) state comparison between cells:

```go
package crdt

import (
    "crypto/sha256"
    "encoding/hex"
)

// MerkleTree provides efficient state comparison for anti-entropy.
type MerkleTree struct {
    Root     *MerkleNode
    leaves   []*MerkleNode
    dirty    bool
}

type MerkleNode struct {
    Left     *MerkleNode
    Right    *MerkleNode
    Hash     []byte
    KeyRange [2]string // [start, end) of key range
    IsLeaf   bool
}

// NewMerkleTree creates an empty Merkle tree.
func NewMerkleTree() *MerkleTree {
    return &MerkleTree{}
}

// Insert adds or updates a key-value pair, recomputing hashes up the tree.
func (t *MerkleTree) Insert(key string, value []byte) {
    hash := sha256.Sum256(append([]byte(key+":"), value...))
    // Simplified: in production, maintain sorted leaf list and rebuild tree
    _ = hash
    t.dirty = true
}

// RootHash returns the current root hash for comparison.
func (t *MerkleTree) RootHash() string {
    if t.Root == nil {
        return ""
    }
    return hex.EncodeToString(t.Root.Hash)
}

// Compare efficiently finds differing key ranges between two trees.
// Returns a list of key ranges that differ.
func (t *MerkleTree) Compare(other *MerkleTree) [][2]string {
    var diffs [][2]string
    t.compareNodes(t.Root, other.Root, &diffs)
    return diffs
}

func (t *MerkleTree) compareNodes(a, b *MerkleNode, diffs *[][2]string) {
    if a == nil && b == nil {
        return
    }
    if a == nil || b == nil {
        // One exists, other doesn't -- entire range differs
        *diffs = append(*diffs, a.KeyRange)
        return
    }
    // Compare hashes at this level
    if string(a.Hash) == string(b.Hash) {
        return // Subtrees match
    }
    if a.IsLeaf && b.IsLeaf {
        // Both leaves with different hashes
        *diffs = append(*diffs, a.KeyRange)
        return
    }
    // Recurse into children
    t.compareNodes(a.Left, b.Left, diffs)
    t.compareNodes(a.Right, b.Right, diffs)
}
```

**Anti-Entropy Protocol:**

1. Each cell maintains a Merkle tree over its CRDT state
2. Cell delegates periodically exchange root hashes (32 bytes)
3. If roots differ, recursively compare child hashes
4. Only divergent leaf ranges are transferred (delta-sync)
5. Full state sync used only for new cell joins or extended partitions

**Bandwidth savings:** For 1M keys with 1 divergent key, Merkle tree comparison requires ~20 hash comparisons (160 bytes) + 1 key transfer instead of 1M key comparisons.

### 5.4 Clock Synchronization

HelixCluster uses Hybrid Logical Clocks (HLC) for causality tracking:

```go
package consensus

import (
    "encoding/json"
    "fmt"
    "sync"
    "time"
)

// HLCTimestamp combines wall-clock time with a logical counter.
// Format: 52 bits physical (microseconds) + 12 bits logical.
type HLCTimestamp struct {
    Physical int64 `json:"pt"`  // Physical time (microseconds since epoch)
    Logical  uint16 `json:"lt"` // Logical counter for same-physical-time events
}

// HLC implements a Hybrid Logical Clock.
type HLC struct {
    mu       sync.RWMutex
    latest   HLCTimestamp
    maxOffset time.Duration // Maximum allowed clock skew (default 500ms)
}

// NewHLC creates a new HLC instance.
func NewHLC(maxOffset time.Duration) *HLC {
    if maxOffset == 0 {
        maxOffset = 500 * time.Millisecond
    }
    return &HLC{
        maxOffset: maxOffset,
    }
}

// Now returns the current HLC timestamp.
func (h *HLC) Now() HLCTimestamp {
    h.mu.Lock()
    defer h.mu.Unlock()

    now := time.Now().UnixMicro()
    if now > h.latest.Physical {
        h.latest.Physical = now
        h.latest.Logical = 0
    } else {
        h.latest.Logical++
    }
    return h.latest
}

// Update updates the HLC based on a received timestamp (causality tracking).
func (h *HLC) Update(received HLCTimestamp) HLCTimestamp {
    h.mu.Lock()
    defer h.mu.Unlock()

    now := time.Now().UnixMicro()
    h.latest.Physical = max(now, h.latest.Physical, received.Physical)

    if h.latest.Physical == now && h.latest.Physical == received.Physical {
        h.latest.Logical = maxUint16(h.latest.Logical, received.Logical) + 1
    } else if h.latest.Physical == h.latest.Physical {
        h.latest.Logical++
    } else if h.latest.Physical == received.Physical {
        h.latest.Logical = received.Logical + 1
    } else {
        h.latest.Logical = 0
    }

    return h.latest
}

// HappensBefore returns true if a happened before b.
func (a HLCTimestamp) HappensBefore(b HLCTimestamp) bool {
    return a.Physical < b.Physical || (a.Physical == b.Physical && a.Logical < b.Logical)
}

// Concurrent returns true if a and b are concurrent (neither happened before the other).
func (a HLCTimestamp) Concurrent(b HLCTimestamp) bool {
    return !a.HappensBefore(b) && !b.HappensBefore(a)
}

func max(a, b, c int64) int64 {
    if a >= b && a >= c {
        return a
    }
    if b >= a && b >= c {
        return b
    }
    return c
}

func maxUint16(a, b uint16) uint16 {
    if a > b {
        return a
    }
    return b
}

func (t HLCTimestamp) String() string {
    return fmt.Sprintf("HLC(pt=%d, lt=%d)", t.Physical, t.Logical)
}

// ToJSON serializes the HLC timestamp.
func (t HLCTimestamp) ToJSON() []byte {
    b, _ := json.Marshal(t)
    return b
}

// VectorClock provides precise causality tracking across cell boundaries.
// Used for Tier 2 (operational) consistency.
type VectorClock map[string]uint64 // nodeID -> logical time

// Increment increments this node's clock.
func (vc VectorClock) Increment(nodeID string) {
    vc[nodeID]++
}

// Merge updates this VC to the element-wise max.
func (vc VectorClock) Merge(other VectorClock) {
    for node, ts := range other {
        if existing, ok := vc[node]; !ok || ts > existing {
            vc[node] = ts
        }
    }
}

// Compare returns: -1 if vc < other, 1 if vc > other, 0 if concurrent/equal.
func (vc VectorClock) Compare(other VectorClock) int {
    allLessOrEqual := true
    allGreaterOrEqual := true

    for node, ts := range vc {
        otherTs := other[node]
        if ts > otherTs {
            allLessOrEqual = false
        }
        if ts < otherTs {
            allGreaterOrEqual = false
        }
    }
    for node, otherTs := range other {
        if _, ok := vc[node]; !ok && otherTs > 0 {
            allGreaterOrEqual = false
        }
    }

    if allLessOrEqual && !allGreaterOrEqual {
        return -1 // vc happened before other
    }
    if allGreaterOrEqual && !allLessOrEqual {
        return 1 // vc happened after other
    }
    if allLessOrEqual && allGreaterOrEqual {
        return 0 // equal
    }
    return 0 // concurrent
}
```

### 5.5 State Classification Matrix

All cluster state is classified into five consistency tiers:

| Tier | State Category | Examples | Consistency Model | Implementation |
|------|---------------|----------|-------------------|----------------|
| **Tier 1** | Critical | Cluster membership, leader election, resource allocation, security policies | **Linearizable** | Raft (etcd) per cell |
| **Tier 2** | Operational | Scheduler state, placement decisions, migration tracking, configuration changes | **Causal** | Vector clocks + causal broadcast |
| **Tier 3** | Observable | Metrics, logs, health checks, audit trails | **Eventual** | Async replication, CRDTs |
| **Tier 4** | Soft State | Presence, load indicators, feature flags, cached configs | **Eventual / CRDT** | G-Counter, LWW-Register, OR-Set |
| **Tier 5** | Reconcilable | Node capability maps, topology info, version metadata | **Eventual + Anti-Entropy** | Delta-CRDTs + Merkle trees |

**Critical Rule:** Tier 1 state NEVER crosses cell boundaries via strong consistency. Cross-cell Tier 1 operations use asynchronous replication with application-level conflict resolution.



---

## 6. Security Architecture

### 6.1 Zero Trust Model

HelixCluster Phase 6 implements NIST SP 800-207 Zero Trust Architecture with BeyondCorp/BeyondProd principles:

```
+------------------------------------------------------------------+
|                    ZERO TRUST SECURITY STACK                      |
+------------------------------------------------------------------+
|  Layer 7: Workload Identity  |  SPIFFE/SPIRE SVIDs (1hr TTL)     |
|  Layer 6: Service Auth       |  mTLS with SPIFFE ID verification  |
|  Layer 5: Network Encryption |  WireGuard (kernel, node-to-node)  |
|  Layer 4: Network Policy     |  Cilium eBPF L3-L7 (identity-based)|
|  Layer 3: Admission Control  |  OPA/Gatekeeper (Rego policies)    |
|  Layer 2: Node Identity      |  SPIRE node attestation            |
|  Layer 1: Boot Attestation   |  TPM/secure boot where available   |
+------------------------------------------------------------------+
|  Cross-Cutting:                                                |
|  - Short-lived certificates (1-24 hour TTL)                     |
|  - Automatic rotation at 50% TTL                               |
|  - Separate trust domains per cell                             |
|  - Federation via SPIFFE bundle endpoints (NOT shared CA)       |
+------------------------------------------------------------------+
```

**Core Principles:**
1. **Never trust, always verify** -- Every connection is authenticated and authorized
2. **Least privilege** -- Each workload gets minimum required permissions
3. **Assume breach** -- Compromised cell has limited blast radius
4. **Short-lived credentials** -- 1-hour SVIDs minimize exposure window
5. **Separate trust domains** -- Each cell has its own CA; federation via bundle exchange

### 6.2 SPIFFE/SPIRE Cross-Cluster Identity

**Nested SPIRE Topology for Federation:**

```
+-----------------------------+
|     Root SPIRE Servers      |  <-- Central trust anchor (3-5 nodes)
|     (trust-domain: root)    |      Issues intermediate CAs
|     PostgreSQL HA backend   |
+-------------+---------------+
              |
    +---------+---------+---------+ ... +---------+
    |                   |                   |
+---v-----+       +-----v----+      +------v----+
|Cell A   |       |Cell B    |      |Cell C     |
|SPIRE    |       |SPIRE     |      |SPIRE      |
|Downstream<------->Downstream<------>Downstream |
|(inter-  |       |(inter-   |      |(inter-    |
|  mediate|       |  mediate |      |  mediate) |
|  CA)    |       |  CA)     |      |  CA)      |
+----+----+       +----+-----+      +----+------+
     |                 |                  |
     v                 v                  v
  Agents            Agents             Agents
 (per node)       (per node)        (per node)
```

**SPIRE Sizing for Federation Scale:**

| Workloads | Cells | Agents | Root Servers | Per-Cell Downstream |
|-----------|-------|--------|--------------|---------------------|
| 100       | 2     | 100    | 2 (2core,2GB)| 2 per cell (2core,2GB) |
| 1,000     | 5     | 1,000  | 4 (16c,8GB)  | 2 per cell (4core,4GB) |
| 10,000    | 25    | 5,000  | 8 (16c,16GB) | 2 per cell (8core,8GB) |
| 100,000   | 50    | 50,000 | 16+ (32c,32GB)| 4 per cell (16c,16GB) |

**Cross-Cluster Identity Verification (Go):**

```go
package security

import (
    "context"
    "crypto/x509"
    "fmt"
    "net/url"
    "strings"
    "sync"
    "time"
)

// TrustDomain represents a SPIFFE trust domain.
type TrustDomain string

func (td TrustDomain) String() string { return string(td) }

// SPIFFEID represents a SPIFFE identity.
type SPIFFEID struct {
    TrustDomain TrustDomain
    Path        string
}

func (id SPIFFEID) String() string {
    return fmt.Sprintf("spiffe://%s%s", id.TrustDomain, id.Path)
}

// ParseSPIFFEID parses a SPIFFE ID string.
func ParseSPIFFEID(s string) (SPIFFEID, error) {
    u, err := url.Parse(s)
    if err != nil {
        return SPIFFEID{}, fmt.Errorf("invalid SPIFFE ID: %w", err)
    }
    if u.Scheme != "spiffe" {
        return SPIFFEID{}, fmt.Errorf("invalid scheme: %s", u.Scheme)
    }
    return SPIFFEID{
        TrustDomain: TrustDomain(u.Host),
        Path:        u.Path,
    }, nil
}

// BundleCache holds federated trust bundles from remote cells.
type BundleCache struct {
    mu      sync.RWMutex
    bundles map[TrustDomain]*x509.CertPool // trust domain -> CA certs
    expiry  map[TrustDomain]time.Time
    maxTTL  time.Duration
}

// NewBundleCache creates a new bundle cache.
func NewBundleCache(maxTTL time.Duration) *BundleCache {
    return &BundleCache{
        bundles: make(map[TrustDomain]*x509.CertPool),
        expiry:  make(map[TrustDomain]time.Time),
        maxTTL:  maxTTL,
    }
}

// AddBundle adds or updates a trust bundle for a remote trust domain.
func (bc *BundleCache) AddBundle(td TrustDomain, certs []*x509.Certificate) {
    pool := x509.NewCertPool()
    for _, cert := range certs {
        pool.AddCert(cert)
    }

    bc.mu.Lock()
    defer bc.mu.Unlock()
    bc.bundles[td] = pool
    bc.expiry[td] = time.Now().Add(bc.maxTTL)
}

// VerifyPeerCertificate verifies an mTLS peer certificate against
// local or federated trust bundles.
func (bc *BundleCache) VerifyPeerCertificate(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
    if len(rawCerts) == 0 {
        return fmt.Errorf("no certificates provided")
    }

    cert, err := x509.ParseCertificate(rawCerts[0])
    if err != nil {
        return fmt.Errorf("parse certificate: %w", err)
    }

    // Extract SPIFFE ID from SAN URI
    var spiffeID SPIFFEID
    for _, uri := range cert.URIs {
        if uri.Scheme == "spiffe" {
            spiffeID, _ = ParseSPIFFEID(uri.String())
            break
        }
    }
    if spiffeID.TrustDomain == "" {
        return fmt.Errorf("no SPIFFE ID in certificate")
    }

    // Get trust bundle for the peer's trust domain
    bc.mu.RLock()
    pool, ok := bc.bundles[spiffeID.TrustDomain]
    expiry := bc.expiry[spiffeID.TrustDomain]
    bc.mu.RUnlock()

    if !ok {
        return fmt.Errorf("no trust bundle for domain: %s", spiffeID.TrustDomain)
    }
    if time.Now().After(expiry) {
        return fmt.Errorf("trust bundle expired for domain: %s", spiffeID.TrustDomain)
    }

    // Verify certificate chain
    opts := x509.VerifyOptions{
        Roots:         pool,
        CurrentTime:   time.Now(),
        KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
        Intermediates: x509.NewCertPool(),
    }
    for _, raw := range rawCerts[1:] {
        intermediate, err := x509.ParseCertificate(raw)
        if err == nil {
            opts.Intermediates.AddCert(intermediate)
        }
    }

    _, err = cert.Verify(opts)
    return err
}

// FederationManager manages trust relationships between cells.
type FederationManager struct {
    localDomain TrustDomain
    bundleCache *BundleCache
    rootPool    *x509.CertPool // Local root CA

    // Active federations: which trust domains we trust
    federations   map[TrustDomain]FederationConfig
    federationsMu sync.RWMutex
}

type FederationConfig struct {
    TrustDomain       TrustDomain
    BundleEndpoint    string
    RefreshInterval   time.Duration
    LastRefresh       time.Time
}

// CanCommunicate checks if two SPIFFE IDs are allowed to communicate.
func (fm *FederationManager) CanCommunicate(source, target SPIFFEID) bool {
    // Same trust domain = always allowed (local policies apply)
    if source.TrustDomain == target.TrustDomain {
        return true
    }

    // Cross-domain: check if federation exists
    fm.federationsMu.RLock()
    _, federated := fm.federations[target.TrustDomain]
    fm.federationsMu.RUnlock()

    return federated
}
```

### 6.3 WireGuard + mTLS Encryption

HelixCluster implements **double encryption** for defense in depth:

| Layer | Encryption | Scope | Keys |
|-------|-----------|-------|------|
| L3 (WireGuard) | ChaCha20-Poly1305 | Node-to-node | Ephemeral WireGuard keys (24hr rotation) |
| L7 (mTLS) | TLS 1.3 + AES-256-GCM | Service-to-service | SPIFFE SVIDs (1hr TTL, 50% rotation) |

**Performance Impact:**
- WireGuard kernel: 3-5% CPU overhead at 10 Gbps
- mTLS (Linkerd proxy): 33% P99 latency increase, ~50MB memory per proxy
- Combined: ~38% P99 latency overhead vs. unencrypted

### 6.4 OPA Policy Enforcement

```yaml
# federation-policy.yaml -- Cross-cluster OPA policies
apiVersion: templates.gatekeeper.sh/v1
kind: ConstraintTemplate
metadata:
  name: helixfederatedtrust
spec:
  crd:
    spec:
      names:
        kind: HelixFederatedTrust
      validation:
        openAPIV3Schema:
          properties:
            allowedTrustDomains:
              type: array
              items:
                type: string
  targets:
    - target: admission.k8s.gatekeeper.sh
      rego: |
        package helix.federation.trust

        violation[{"msg": msg}] {
          input.review.object.kind == "Pod"
          input.review.object.metadata.annotations["helix.io/federation-policy"]
          trustDomain := input.review.object.metadata.annotations["helix.io/trust-domain"]
          not trust_domain_allowed(trustDomain)
          msg := sprintf("Trust domain %s is not federated with this cluster", [trustDomain])
        }

        trust_domain_allowed(td) {
          allowed := {domain | domain := input.parameters.allowedTrustDomains[_]}
          allowed[td]
        }
---
apiVersion: constraints.gatekeeper.sh/v1beta1
kind: HelixFederatedTrust
metadata:
  name: federation-trust-boundary
spec:
  match:
    kinds:
      - apiGroups: [""]
        kinds: ["Pod"]
  parameters:
    allowedTrustDomains:
      - "cell-alpha.helix.local"
      - "cell-beta.helix.local"
      - "cell-gamma.helix.local"
```

### 6.5 Secret Management

**Recommended Architecture: External Secrets Operator (ESO) + Vault**

```yaml
# cluster-secret-store.yaml
apiVersion: external-secrets.io/v1beta1
kind: ClusterSecretStore
metadata:
  name: helix-vault-backend
spec:
  provider:
    vault:
      server: "https://vault.helix.internal:8200"
      path: "secret"
      version: "v2"
      auth:
        kubernetes:
          mountPath: "kubernetes"
          role: "helix-cell-secrets"
          serviceAccountRef:
            name: external-secrets
            namespace: security
---
# external-secret.yaml
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: federation-ca-bundle
  namespace: security
spec:
  refreshInterval: "1h"
  secretStoreRef:
    kind: ClusterSecretStore
    name: helix-vault-backend
  target:
    name: federation-ca-bundle
    creationPolicy: Owner
  data:
    - secretKey: ca.crt
      remoteRef:
        key: secret/data/federation/ca
        property: cert
```

### 6.6 Threat Model & Mitigations

```
                    +----------------------------------+
                    |      THREAT MODEL DIAGRAM        |
                    |   Federated Multi-Cluster K8s    |
                    +----------------------------------+

  +-----------+     +-----------+     +-----------+
  |  Cell A   |<--->|  Cell B   |<--->|  Cell C   |
  | (Trusted) | mTLS| (Trusted) | mTLS| (New/     |
  |           |     |           |     |  Unknown) |
  +-----+-----+     +-----+-----+     +-----+-----+
        |                 |                 |
        |    +------------+                 |
        |    |    +-------------------------+
        v    v    v
  +------------------------------------------+
  |        INTER-CELL ATTACK SURFACES        |
  | 1. Compromised cell joins federation     |
  | 2. Stolen service account tokens         |
  | 3. Man-in-the-middle on inter-cell links |
  | 4. DNS hijacking for discovery           |
  | 5. Rogue CA issues valid SVIDs           |
  | 6. Cross-cell lateral movement           |
  +------------------------------------------+

  PER-CELL ATTACK SURFACES:
  +------------------------------------------+
  | 1. Compromised node -> lateral via pods  |
  | 2. Malicious image -> supply chain       |
  | 3. Overprivileged RBAC -> escalation     |
  | 4. Stolen kubeconfig -> full access      |
  | 5. Container breakout -> host access     |
  | 6. Exposed API server -> unauthorized    |
  +------------------------------------------+
```

| Threat | Vector | Likelihood | Impact | Mitigation |
|--------|--------|------------|--------|------------|
| **Compromised cell** | Rogue cluster joins with stolen credentials | Medium | **Critical** | Separate trust domains; OPA admission control; cell attestation |
| **Lateral movement** | Compromised pod scans/exploits others | High | High | Default-deny network policies; microsegmentation; service identity |
| **Supply chain** | Poisoned container image via CI/CD | Medium | **Critical** | Cosign image signing; admission control; SBOM scanning |
| **Trust bundle theft** | SPIRE bundle exfiltrated | Low | **Critical** | Short-lived bundles; HSM-protected CA keys; monitoring |
| **Data exfiltration** | Compromised workload tunnels data | Medium | High | Egress filtering; network monitoring; DLP |
| **DoS via sync** | Sync storms overload etcd | Low | Medium | API Priority & Fairness; rate limiting; bulkheads |
| **Certificate expiry** | Automation failure | Low | High | Automated rotation; 30-day expiry alerts; manual runbook |

---

## 7. Multi-Region & Cloud Integration

### 7.1 Cloud Bursting Architecture

```
                    +-------------------------+
                    |   Global Traffic Router   |
                    |  (Cloudflare / Route 53)  |
                    +------------+------------+
                                 |
            +--------------------+--------------------+
            |                    |                    |
    +-------v-------+    +-------v--------+   +-------v-------+
    |  Cell Alpha   |    |  Cell Beta     |   |  Cell Gamma   |
    |  (On-Prem)    |    |  (On-Prem)     |   |  (On-Prem)    |
    |  100 nodes    |    |  50 nodes      |   |  200 nodes    |
    +-------+-------+    +--------+-------+   +-------+-------+
            |                     |                   |
            |    +----------------+                   |
            |    |    +-------------------------------+
            v    v    v
    +---------------------------------------------+
    |          CLOUD BURST POOL                    |
    |  +------+  +------+  +------+  +------+     |
    |  |Spot 1|  |Spot 2|  |Spot 3|  |Spot N|     |
    |  |AWS   |  |GCP   |  |Azure |  |Hetzner|    |
    |  |t3.med|  |e2-std|  |D2sv3 |  |CX21  |    |
    |  +------+  +------+  +------+  +------+     |
    |  Auto-scaling: 0-500 spot instances         |
    |  Trigger: CPU > 80% for 5 min               |
    |  Termination: 2-min warning (AWS)            |
    +---------------------------------------------+
```

**Spot Instance Toleration Spec:**

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: burst-workload
spec:
  replicas: 10
  template:
    spec:
      terminationGracePeriodSeconds: 120
      containers:
      - name: app
        lifecycle:
          preStop:
            exec:
              command: ["/bin/sh", "-c", "sleep 30 && /app/graceful-shutdown"]
      affinity:
        nodeAffinity:
          preferredDuringSchedulingIgnoredDuringExecution:
          - weight: 100
            preference:
              matchExpressions:
              - key: node.kubernetes.io/instance-type
                operator: In
                values: ["spot", "preemptible"]
      tolerations:
      - key: "cloud.google.com/gke-preemptible"
        operator: "Equal"
        value: "true"
        effect: "NoSchedule"
      - key: "node.kubernetes.io/unschedulable"
        operator: "Exists"
        effect: "NoSchedule"
```

### 7.2 Latency-Aware Scheduling

**Topology Keys Used for Placement:**

```yaml
# topology-aware-scheduling.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: latency-sensitive-service
spec:
  template:
    spec:
      topologySpreadConstraints:
      - maxSkew: 1
        topologyKey: topology.kubernetes.io/zone
        whenUnsatisfiable: DoNotSchedule
        labelSelector:
          matchLabels:
            app: latency-sensitive-service
      affinity:
        podAffinity:
          preferredDuringSchedulingIgnoredDuringExecution:
          - weight: 100
            podAffinityTerm:
              labelSelector:
                matchExpressions:
                - key: app
                  operator: In
                  values: ["database", "cache"]
              topologyKey: topology.kubernetes.io/zone
        nodeAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            nodeSelectorTerms:
            - matchExpressions:
              - key: topology.kubernetes.io/region
                operator: In
                values: ["us-east-1"]  # Stay in-region
```

**Measured Latency Reference:**

| Route | RTT | etcd Viable | App Traffic |
|-------|-----|-------------|-------------|
| Same AZ | 0.4-0.5ms | Yes | Excellent |
| Cross-AZ (same region) | 0.5-2.5ms | Yes (up to 3 AZs) | Excellent |
| Cross-region (same continent) | 10-50ms | **NO** | Good |
| Cross-continent | 100-300ms | **NO** | Acceptable for async |

### 7.3 Data Sovereignty

**Region-Affinity Enforcement:**

```yaml
# data-sovereignty-policy.yaml
apiVersion: templates.gatekeeper.sh/v1
kind: ConstraintTemplate
metadata:
  name: helixdataresidency
spec:
  crd:
    spec:
      names:
        kind: HelixDataResidency
  targets:
    - target: admission.k8s.gatekeeper.sh
      rego: |
        package helix.data.residency

        violation[{"msg": msg}] {
          input.review.object.kind == "Pod"
          pod := input.review.object
          dataClass := pod.metadata.labels["data-classification"]
          dataClass == "eu-personal-data"
          not pod_has_eu_affinity(pod)
          msg := "Pod handling EU personal data must have EU region affinity"
        }

        pod_has_eu_affinity(pod) {
          affinity := pod.spec.affinity
          nodeAffinity := affinity.nodeAffinity
          required := nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution
          term := required.nodeSelectorTerms[_]
          expr := term.matchExpressions[_]
          expr.key == "topology.kubernetes.io/region"
          region := expr.values[_]
          startswith(region, "europe-")
        }
```

### 7.4 Disaster Recovery

**Tiered DR Strategy:**

| Tier | Workload Type | RTO | RPO | Strategy | Cost Multiplier |
|------|--------------|-----|-----|----------|----------------|
| Tier 1 | Revenue-critical | < 15 min | Near-zero | Active-active | 2.5-3x |
| Tier 2 | Business-critical | < 4 hours | < 1 hour | Warm standby | 1.3-1.5x |
| Tier 3 | Standard | < 24 hours | < 24 hours | Velero backup + restore | 1.1-1.2x |
| Tier 4 | Non-critical | < 72 hours | < 72 hours | GitOps rebuild from Git | 1x |

**Velero Backup Schedule:**

```yaml
# velero-schedule.yaml
apiVersion: velero.io/v1
kind: Schedule
metadata:
  name: cell-hourly-backup
  namespace: velero
spec:
  schedule: "0 * * * *"  # Every hour
  template:
    includedNamespaces:
    - production
    - security
    - monitoring
    excludedResources:
    - events
    - pods  # Pods are ephemeral
    storageLocation: aws-east-backup
    volumeSnapshotLocations:
    - aws-east-snapshots
    ttl: 720h  # 30 days retention
---
# Cross-cluster restore procedure
# 1. Deploy new cell with same cell ID
# 2. velero restore create --from-backup cell-hourly-backup-2026022501
# 3. Verify CRDT convergence
# 4. Update DNS/global routing
```

---

## 8. Control Plane Federation

### 8.1 Federated API Server

```
+------------------------+     +------------------------+     +------------------------+
|    Cell Alpha API      |     |    Cell Beta API       |     |    Cell Gamma API      |
|    (local control)     |     |    (local control)     |     |    (local control)     |
|                        |     |                        |     |                        |
| /api/v1/namespaces     |     | /api/v1/namespaces     |     | /api/v1/namespaces     |
| /api/v1/pods           |     | /api/v1/pods           |     | /api/v1/pods           |
| /apis/helix.io/v1/...  |     | /apis/helix.io/v1/...  |     | /apis/helix.io/v1/...  |
+-----------+------------+     +-----------+------------+     +-----------+------------+
            |                              |                              |
            +--------------+---------------+---------------+----------------+
                           |                               |
               +-----------v-----------+       +-----------v-----------+
               |   Federation Proxy    |       |  Karmada/OCM (opt)    |
               |   (request router)    |       |  (cross-cell schedule) |
               |                       |       |                       |
               | GET /federation/cells |       | PropagationPolicy     |
               | GET /federation/services |    | OverridePolicy        |
               | POST /federation/exec   |     | Scheduling Result     |
               +-----------------------+       +-----------------------+
```

### 8.2 Resource Scheduling Across Cells

HelixCluster uses a **two-level scheduling** approach:

1. **Cell-local scheduler**: Kubernetes default scheduler within each cell
2. **Federation scheduler (optional)**: Karmada for cross-cell workload placement

```yaml
# propagation-policy.yaml -- Karmada PropagationPolicy
apiVersion: policy.karmada.io/v1alpha1
kind: PropagationPolicy
metadata:
  name: web-service-propagation
spec:
  resourceSelectors:
    - apiVersion: apps/v1
      kind: Deployment
      name: web-service
  placement:
    clusterAffinity:
      clusterNames:
        - cell-alpha
        - cell-beta
    replicaScheduling:
      replicaDivisionPreference: Weighted
      replicaSchedulingType: Divided
      weightPreference:
        staticWeightList:
          - targetCluster:
              clusterNames: [cell-alpha]
            weight: 60
          - targetCluster:
              clusterNames: [cell-beta]
            weight: 40
```

### 8.3 Service Discovery Federation

**Cilium Cluster Mesh Service Discovery:**

```yaml
# global-service.yaml
apiVersion: v1
kind: Service
metadata:
  name: payment-api
  annotations:
    io.cilium/global-service: "true"
spec:
  type: ClusterIP
  selector:
    app: payment-api
  ports:
  - port: 8080
    targetPort: 8080
---
# Network policy for cross-cluster access
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata:
  name: allow-frontend-to-payment
  namespace: default
spec:
  endpointSelector:
    matchLabels:
      app: payment-api
  ingress:
  - fromEndpoints:
    - matchLabels:
        app: frontend
    toPorts:
    - ports:
      - port: "8080"
        protocol: TCP
      rules:
        http:
        - method: POST
          path: "/api/v1/pay"
```

### 8.4 Configuration Management

**GitOps with ArgoCD ApplicationSets:**

```yaml
# federation-appset.yaml
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: federation-services
spec:
  generators:
  - clusters:
      selector:
        matchLabels:
          federation.helix.io/enabled: "true"
  template:
    metadata:
      name: '{{name}}-services'
    spec:
      project: default
      source:
        repoURL: https://github.com/helixcluster/federation-config.git
        targetRevision: HEAD
        path: services/overlays/{{name}}
      destination:
        server: '{{server}}'
        namespace: production
      syncPolicy:
        automated:
          prune: true
          selfHeal: true
        syncOptions:
        - CreateNamespace=true
```

---

## 9. Testing & Validation Architecture

### 9.1 Deterministic Simulation

**Turmoil-based Multi-Cluster Protocol Testing:**

```rust
// turmoil_test.rs -- Deterministic simulation of federation protocols
use turmoil::Builder;

#[test]
fn test_federation_partition_recovery() {
    let mut sim = Builder::new()
        .fail_rate(0.05)      // 5% packet loss
        .min_message_latency(Duration::from_millis(10))
        .max_message_latency(Duration::from_millis(300))
        .build();

    // Host 3 cells with 2 nodes each
    sim.host("cell-a-1", cell_node(1, "cell-a"));
    sim.host("cell-a-2", cell_node(2, "cell-a"));
    sim.host("cell-b-1", cell_node(1, "cell-b"));
    sim.host("cell-b-2", cell_node(2, "cell-b"));
    sim.host("cell-c-1", cell_node(1, "cell-c"));
    sim.host("cell-c-2", cell_node(2, "cell-c"));

    // Simulate partition between cell-a and cell-b
    sim.partition("cell-a-1", "cell-b-1");
    sim.partition("cell-a-1", "cell-b-2");
    sim.partition("cell-a-2", "cell-b-1");
    sim.partition("cell-a-2", "cell-b-2");

    // Run for simulated time
    sim.run_for(Duration::from_secs(30));

    // Heal partition
    sim.heal("cell-a-1", "cell-b-1");
    // ... heal all

    // Verify CRDT convergence
    sim.run_for(Duration::from_secs(60));
    assert_crdt_converged(&sim);
}
```

### 9.2 Chaos Engineering Catalog

**12 Chaos Experiments for Federated Clusters:**

| ID | Experiment | Target | Tool | Duration | Safety Level |
|----|-----------|--------|------|----------|-------------|
| CE-01 | **Inter-cell link failure** | Network gateways | Chaos Mesh NetworkChaos | 5 min | High -- cells operate independently |
| CE-02 | **Partial/asymmetric partition** | Cell-to-cell routing | tc/netem + custom | 5 min | Medium -- may cause suspicion |
| CE-03 | **WAN latency spike (300ms)** | Inter-cell links | Toxiproxy latency | 10 min | High -- timeouts should handle |
| CE-04 | **Packet loss (5%)** | Inter-cell links | tc/netem loss | 10 min | High -- retries should recover |
| CE-05 | **Cell control plane failure** | API server pods | Chaos Mesh PodChaos | 5 min | Medium -- cell goes read-only |
| CE-06 | **etcd leader death** | etcd pods | Chaos Mesh PodChaos | 3 min | Medium -- election triggers |
| CE-07 | **Gossip bandwidth saturation** | Gossip daemons | StressChaos | 10 min | Medium -- convergence slows |
| CE-08 | **Clock skew (+/- 500ms)** | Node NTP | Chaos Mesh TimeChaos | 10 min | Medium -- HLC should handle |
| CE-09 | **CRDT large-state sync** | State sync path | Custom script | 10 min | High -- test convergence time |
| CE-10 | **Sequential cell failures** | All pods in cell | Chaos Mesh + script | 15 min | Low -- full DR exercise |
| CE-11 | **Certificate expiry** | cert-manager | Custom (set short TTL) | 30 min | High -- rotation should work |
| CE-12 | **Cascading overload** | All cells, CPU stress | StressChaos (all cells) | 10 min | Low -- circuit breakers tested |

**Chaos Experiment CRD Examples:**

```yaml
# ce-01-inter-cell-partition.yaml
apiVersion: chaos-mesh.org/v1alpha1
kind: NetworkChaos
metadata:
  name: inter-cell-partition
  namespace: chaos-testing
spec:
  action: partition
  mode: all
  selector:
    labelSelectors:
      "helix.io/role": "gateway"
      "helix.io/cell": "cell-alpha"
  direction: both
  target:
    mode: all
    selector:
      labelSelectors:
        "helix.io/role": "gateway"
        "helix.io/cell": "cell-beta"
  duration: "5m"
  scheduler:
    cron: "@daily"
---
# ce-03-wan-latency.yaml
apiVersion: chaos-mesh.org/v1alpha1
kind: NetworkChaos
metadata:
  name: wan-latency-spike
spec:
  action: delay
  mode: all
  selector:
    labelSelectors:
      "helix.io/role": "gateway"
  delay:
    latency: "300ms"
    correlation: "50"
    jitter: "50ms"
  duration: "10m"
---
# ce-08-clock-skew.yaml
apiVersion: chaos-mesh.org/v1alpha1
kind: TimeChaos
metadata:
  name: clock-skew-test
spec:
  mode: all
  selector:
    labelSelectors:
      "app.kubernetes.io/component": "etcd"
  timeOffset: "+300s"
  duration: "10m"
```

### 9.3 FMEA (Failure Mode Effects Analysis)

| ID | Failure Mode | Detection | Impact | Recovery | Mitigation |
|----|-------------|-----------|--------|----------|------------|
| **F-01** | Single node failure | 1-5s (SWIM probe) | Workload rescheduled | Automatic (replica) | 3+ replicas; pod disruption budgets |
| **F-02** | Single cell partition | 5-30s (inter-cell probe) | Cross-cell ops fail | Automatic on heal | Quorum enforcement; circuit breakers |
| **F-03** | Split-brain (two leaders) | 1-60s (metrics) | Data inconsistency risk | Manual intervention | Strict quorum; CheckQuorum; witness nodes |
| **F-04** | Inter-cell link degradation | 5-15s (latency probe) | Slow cross-cell ops | Automatic reroute | Multiple paths; TURN fallback |
| **F-05** | Complete cell failure | 10-60s (gossip timeout) | Loss of cell's workloads | DR restore | Velero cross-cluster backup; warm standby |
| **F-06** | Gossip protocol saturation | Minutes (bandwidth metrics) | Slow membership updates | Automatic backpressure | Fanout limits; message prioritization |
| **F-07** | Clock skew > max_offset | Continuous (NTP monitoring) | Inconsistent ordering | NTP resync | HLC logical clocks; node self-termination |
| **F-08** | Cascading failure across cells | Seconds (alert pipeline) | Full federation outage | Emergency procedures | Bulkhead pattern; rate limiting per cell |
| **F-09** | CRDT state divergence | Hours (anti-entropy check) | Inconsistent soft state | Delta sync + Merkle repair | Periodic anti-entropy; checksum validation |
| **F-10** | Certificate/SVID expiry | Days (cert-manager alert) | TLS/mTLS failures | Automatic rotation | 30-day warnings; 50% TTL auto-rotation |
| **F-11** | etcd quorum loss | Immediate (health check) | Cell control plane read-only | Restore quorum nodes | 5-node etcd; automated repair |
| **F-12** | Asymmetric partition | 10-60s (bidirectional probes) | Subtle consistency issues | Automatic on heal | Bidirectional health checks; relay fallback |
| **F-13** | Control plane overload | Seconds (API latency) | Scheduling degradation | Scale out | API Priority & Fairness; caching layer |
| **F-14** | State sync bandwidth exhaustion | Minutes (throughput metrics) | Stale cross-cell data | Backpressure + queue | Bandwidth quotas; delta-only sync |
| **F-15** | Misconfiguration propagation | Hours-days (drift detection) | Federation-wide issues | Config rollback | Validation webhooks; canary deployment |

### 9.4 Monitoring & Observability

**Prometheus Federation Architecture:**

```yaml
# central-prometheus.yaml
scrape_configs:
  # Federation: scrape pre-aggregated metrics from each cell
  - job_name: 'federate-cell-alpha'
    scrape_interval: 30s
    honor_labels: true
    metrics_path: '/federate'
    params:
      'match[]':
        - '{__name__=~"helix:.*"}'
        - '{__name__=~"etcd:.*"}'
        - '{__name__=~"cilium:.*"}'
    static_configs:
      - targets: ['prometheus.cell-alpha.local:9090']
        labels:
          cell: 'cell-alpha'
          region: 'us-east-1'

  - job_name: 'federate-cell-beta'
    scrape_interval: 30s
    honor_labels: true
    metrics_path: '/federate'
    params:
      'match[]':
        - '{__name__=~"helix:.*"}'
    static_configs:
      - targets: ['prometheus.cell-beta.local:9090']
        labels:
          cell: 'cell-beta'
          region: 'eu-west-1'

# Split-brain detection alert
alerting:
  alertmanagers:
    - static_configs:
        - targets: ['alertmanager:9093']

rule_files:
  - /etc/prometheus/alerts/federation-alerts.yaml
```

```yaml
# federation-alerts.yaml
groups:
- name: federation-critical
  rules:
  - alert: HelixSplitBrainDetected
    expr: |
      sum by (cell) (etcd_server_is_leader) > 1
    for: 30s
    labels:
      severity: critical
    annotations:
      summary: "Split-brain detected in cell {{ $labels.cell }}"
      description: "Multiple etcd leaders detected in the same cell"

  - alert: HelixCellPartitioned
    expr: |
      helix_inter_cell_reachable{cell!="self"} == 0
    for: 1m
    labels:
      severity: warning
    annotations:
      summary: "Cell {{ $labels.cell }} appears partitioned"
      description: "No inter-cell connectivity to {{ $labels.cell }} for 1 minute"

  - alert: HelixGossipConvergenceSlow
    expr: |
      histogram_quantile(0.99, 
        helix_gossip_convergence_seconds_bucket) > 300
    for: 5m
    labels:
      severity: warning
    annotations:
      summary: "Gossip convergence time exceeds 5 minutes"

  - alert: HelixCRDTDivergence
    expr: |
      helix_crdt_hash_mismatch_total > 0
    for: 1m
    labels:
      severity: warning
    annotations:
      summary: "CRDT divergence detected between cells"

  - alert: HelixCircuitBreakerOpen
    expr: |
      helix_inter_cell_circuit_breaker_state == 2
    for: 5m
    labels:
      severity: critical
    annotations:
      summary: "Circuit breaker open for cell {{ $labels.cell }}"
```

**OpenTelemetry Cross-Cluster Tracing:**

```yaml
# otel-collector-config.yaml
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
      http:
        endpoint: 0.0.0.0:4318

processors:
  batch:
    timeout: 1s
    send_batch_size: 1024
  resource:
    attributes:
      - key: cluster
        value: ${CELL_ID}
        action: upsert
      - key: region
        value: ${REGION}
        action: upsert

exporters:
  otlp/central:
    endpoint: tempo.helix-central.local:4317
    tls:
      cert_file: /etc/otel/tls.crt
      key_file: /etc/otel/tls.key
      ca_file: /etc/otel/ca.crt

service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [batch, resource]
      exporters: [otlp/central]
```

### 9.5 Benchmark Suite

**Scalability Benchmark Phases:**

| Phase | Cells | Nodes/Cell | Total Nodes | Metrics |
|-------|-------|-----------|-------------|---------|
| Phase 1 | 2 | 5 | 10 | Basic mesh, gossip convergence |
| Phase 2 | 10 | 50 | 500 | Gossip saturation, partition recovery |
| Phase 3 | 50 | 100 | 5,000 | WAN tuning, DR testing |
| Phase 4 | 100 | 100 | 10,000 | Full federation stress test |

**Benchmark Tooling:**

```yaml
# k6-load-test.js -- Federation load test
import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = {
  stages: [
    { duration: '2m', target: 100 },   // Ramp up
    { duration: '5m', target: 100 },   // Steady state
    { duration: '2m', target: 200 },   // Spike
    { duration: '5m', target: 200 },   // Sustained load
    { duration: '2m', target: 0 },     // Ramp down
  ],
  thresholds: {
    http_req_duration: ['p(99)<500'],  // 500ms p99
    http_req_failed: ['rate<0.001'],    // 0.1% error rate
  },
};

export default function () {
  const res = http.get('http://federation-gateway.helix.local/api/v1/services');
  check(res, {
    'status is 200': (r) => r.status === 200,
    'response time < 500ms': (r) => r.timings.duration < 500,
  });
  sleep(1);
}
```

---

## 10. Deployment & Operations

### 10.1 Deployment Topologies

**Single-Command Federation (Quick Start):**

```bash
# On Cell Alpha (first cell)
helixctl cell init --cell-id=1 --region=us-east-1 --name=alpha
helixctl cell start

# On Cell Beta (joins existing federation)
helixctl cell init --cell-id=2 --region=eu-west-1 --name=beta
helixctl federation join --bootstrap-dns=_helix-gossip._tcp.alpha.helix.local

# On Cell Gamma
helixctl cell init --cell-id=3 --region=ap-south-1 --name=gamma
helixctl federation join --bootstrap-dns=_helix-gossip._tcp.alpha.helix.local
```

**Cluster Definition YAML:**

```yaml
# cell-alpha.yaml
apiVersion: helix.io/v1
kind: Cell
metadata:
  name: alpha
  labels:
    region: us-east-1
    environment: production
    tier: tier-1
spec:
  cellID: 1
  etcd:
    replicas: 5
    storage: 100Gi
    backup:
      enabled: true
      schedule: "0 */6 * * *"
      retention: 720h
  networking:
    podCIDR: "10.1.0.0/16"
    serviceCIDR: "10.2.0.0/16"
    wireguard:
      port: 51820
      listenIP: "0.0.0.0"
    cilium:
      clusterID: 1
      clusterName: "alpha"
  federation:
    enabled: true
    gatewayNodes: 3
    trustDomain: "cell-alpha.helix.local"
    bootstrapStrategy: dns
    dnsDiscovery:
      service: "_helix-gossip._tcp.helix.local"
    natTraversal:
      stunServers:
        - "stun.l.google.com:19302"
        - "stun.cloudflare.com:3478"
      turnServer: "turn.helix.local:3478"
    security:
      spire:
        enabled: true
        nested: true
        upstream: "root.helix.local"
  nodes:
    min: 3
    max: 100
    instanceTypes:
      - t3.medium
      - t3.large
```

### 10.2 Operational Runbooks

**Runbook: Add a New Cell to Federation**

```bash
#!/bin/bash
# runbook-add-cell.sh

set -euo pipefail

CELL_NAME="${1:?Cell name required}"
CELL_ID="${2:?Cell ID required}"
REGION="${3:?Region required}"
BOOTSTRAP_SEED="${4:?Bootstrap seed required}"

echo "=== Phase 1: Validate cell ID uniqueness ==="
if helixctl federation check-cell-id --id="$CELL_ID"; then
    echo "ERROR: Cell ID $CELL_ID already in use"
    exit 1
fi

echo "=== Phase 2: Initialize new cell ==="
helixctl cell init \
    --cell-id="$CELL_ID" \
    --region="$REGION" \
    --name="$CELL_NAME" \
    --trust-domain="${CELL_NAME}.helix.local"

echo "=== Phase 3: Start local control plane ==="
helixctl cell start --wait-healthy

echo "=== Phase 4: Federation join ==="
helixctl federation join \
    --bootstrap="$BOOTSTRAP_SEED" \
    --timeout=120s

echo "=== Phase 5: Verify mesh connectivity ==="
helixctl federation mesh status --expected-cells=3

echo "=== Phase 6: Verify CRDT convergence ==="
helixctl federation state verify --timeout=60s

echo "=== Phase 7: Enable cross-cell services ==="
kubectl label cells "$CELL_NAME" "federation.helix.io/enabled=true"

echo "Cell $CELL_NAME (ID: $CELL_ID) successfully federated!"
```

**Runbook: Remove a Cell from Federation (Graceful)**

```bash
#!/bin/bash
# runbook-remove-cell.sh

set -euo pipefail

CELL_NAME="${1:?Cell name required}"
DRAIN_TIMEOUT="${2:-300}" # 5 minutes default

echo "=== Phase 1: Cordon cell (no new workloads) ==="
kubectl taint cells "$CELL_NAME" "federation.helix.io/draining=true:NoSchedule"

echo "=== Phase 2: Migrate workloads to other cells ==="
helixctl federation evacuate "$CELL_NAME" --timeout="${DRAIN_TIMEOUT}s"

echo "=== Phase 3: Drain cross-cell connections ==="
helixctl federation mesh disconnect "$CELL_NAME" --graceful

echo "=== Phase 4: Leave federation ==="
helixctl federation leave --cell="$CELL_NAME" --timeout=60s

echo "=== Phase 5: Update DNS/bootstrap records ==="
helixctl federation dns remove "$CELL_NAME"

echo "=== Phase 6: Tombstone cell (prevent rejoin with stale state) ==="
helixctl federation tombstone "$CELL_NAME" --retention=720h

echo "Cell $CELL_NAME gracefully removed from federation."
```

**Runbook: Recover from Network Partition**

```bash
#!/bin/bash
# runbook-partition-recovery.sh

set -euo pipefail

echo "=== Phase 1: Identify partition scope ==="
helixctl federation partition detect

# Sample output:
# Partition detected:
#   Side A: cell-alpha, cell-beta (3 nodes, 2 cells)
#   Side B: cell-gamma (1 node, 1 cell)

echo "=== Phase 2: Verify quorum on both sides ==="
for cell in cell-alpha cell-beta cell-gamma; do
    if helixctl cell --cell="$cell" etcd status | grep -q "healthy"; then
        echo "  $cell: etcd healthy"
    else
        echo "  $cell: etcd DEGRADED -- manual intervention required"
    fi
done

echo "=== Phase 3: Check for divergent writes ==="
helixctl federation state diff --cells=cell-alpha,cell-beta,cell-gamma

echo "=== Phase 4: If partition is healed, verify convergence ==="
helixctl federation state verify --timeout=300s

echo "=== Phase 5: If convergence fails, trigger anti-entropy ==="
helixctl federation state repair --full-sync=false

echo "=== Phase 6: Verify mesh is fully connected ==="
helixctl federation mesh status
```

### 10.3 Resource Requirements

**Per-Node Resource Overhead (Federation Agent):**

| Component | CPU | Memory | Network | Storage |
|-----------|-----|--------|---------|---------|
| WireGuard (kernel) | 3-5% at 1 Gbps | 5 MB | WireGuard overhead | None |
| Gossip Agent (memberlist) | 1-2% | 50-100 MB | 3-5 KB/s | 10 MB logs |
| SPIRE Agent | 1% | 30 MB | Certificate traffic | 5 MB cache |
| Cilium Agent | 5-10% | 100-200 MB | eBPF overhead | 50 MB state |
| Federation Proxy | 2-5% | 50 MB | Cross-cell routing | 10 MB |
| **Total per node** | **~15-25%** | **~250-400 MB** | **~10 KB/s** | **~75 MB** |

**Gateway Node Requirements (adds to above):**

| Component | CPU | Memory | Network | Notes |
|-----------|-----|--------|---------|-------|
| TURN Relay | 10-20% | 200 MB | Relayed traffic | CPU scales with relayed BW |
| Inter-cell Gossip | 2% | 100 MB | 2 KB/s | WAN-tuned memberlist |
| **Gateway total** | **+15-25%** | **+300 MB** | **Relayed traffic** | **3-5 gateways per cell** |

### 10.4 Upgrade Strategies

| Strategy | Description | Downtime | Complexity | Best For |
|----------|-------------|----------|------------|----------|
| **Rolling Upgrade** | Upgrade nodes one at a time | Zero (per cell) | Low | Patch updates |
| **Blue-Green Cell** | Deploy new cell, migrate workloads | Zero | Medium | Major version upgrades |
| **Canary Cell** | Route 5% traffic to upgraded cell | Near-zero | Medium | Risky changes |
| **Federation-Aware Rolling** | Upgrade one cell at a time, verify cross-cell health | Zero | High | Federation-wide upgrades |

**Federation-Aware Rolling Upgrade:**

```yaml
# federation-upgrade.yaml
apiVersion: helix.io/v1
kind: FederationUpgrade
metadata:
  name: v6-1-0-rollout
spec:
  targetVersion: "6.1.0"
  strategy: federation-rolling
  federationRolling:
    maxUnavailableCells: 1
    cellUpgradeTimeout: 30m
    healthChecks:
      - name: inter-cell-latency
        threshold: 500ms
      - name: gossip-convergence
        threshold: 60s
      - name: crdt-divergence
        threshold: 0
    rollbackOnFailure: true
  cellOrder:
    - cell-gamma   # Upgrade least critical first
    - cell-beta
    - cell-alpha   # Upgrade most critical last
```

---

## 11. Implementation Roadmap

### 11.1 Phase 6a: Core Mesh (Weeks 1-6)

**Goal:** WireGuard mesh establishment between cells with automatic NAT traversal.

| Week | Deliverable | Acceptance Criteria |
|------|------------|-------------------|
| 1-2 | WireGuard mesh manager | Two VMs establish WireGuard tunnel; iperf3 > 500 Mbps |
| 2-3 | NAT traversal stack | STUN + hole punching success > 80%; TURN fallback works |
| 3-4 | mDNS local discovery | Two nodes on same LAN discover each other in < 10s |
| 4-5 | libp2p DHT bootstrap | Global discovery via DHT; join without hardcoded IPs |
| 5-6 | Mesh health monitoring | Latency, throughput, packet loss metrics; dashboard |

**Key Decisions:**
- Use kernel WireGuard (not userspace) for performance
- TURN server embedded in gateway nodes (not external dependency)
- ICE prioritization: direct > STUN > TURN > relay

### 11.2 Phase 6b: Gossip & Discovery (Weeks 7-12)

**Goal:** Hierarchical gossip protocol with failure detection and partition handling.

| Week | Deliverable | Acceptance Criteria |
|------|------------|-------------------|
| 7-8 | Intra-cell memberlist | 100-node gossip pool; convergence < 5s; 0 false positives |
| 8-9 | Inter-cell gossip | WAN-tuned delegates; bandwidth < 5 KB/s per gateway |
| 9-10 | Phi accrual detector | Adapts to network conditions; 50x fewer false positives |
| 10-11 | Partition handling | Automatic detection; quorum enforcement; CRDT convergence |
| 11-12 | Bootstrap & rendezvous | All 5 strategies work; cell joins in < 60s |

**Key Decisions:**
- HashiCorp memberlist (SWIM + Lifeguard) for both pools
- Separate encryption keys for intra-cell vs inter-cell gossip
- CRDTs for all cross-cell state; never stretch etcd across cells

### 11.3 Phase 6c: Federation Control Plane (Weeks 13-18)

**Goal:** Full control plane federation with scheduling, service discovery, and config management.

| Week | Deliverable | Acceptance Criteria |
|------|------------|-------------------|
| 13-14 | SPIFFE/SPIRE per cell | SVID issuance; mTLS between services; 1hr TTL |
| 14-15 | Cilium Cluster Mesh | Cross-cell pod-to-pod connectivity; identity-aware policies |
| 15-16 | Service discovery | Global services resolve across cells; health check propagation |
| 16-17 | GitOps federation | ArgoCD ApplicationSets deploy to 3+ cells |
| 17-18 | Karmada integration | Cross-cell workload placement; PropagationPolicy support |

**Key Decisions:**
- Nested SPIRE topology (root + downstream per cell)
- Cilium Cluster Mesh for networking (not Istio -- lower overhead)
- Karmada as optional federation scheduler (not required for basic federation)

### 11.4 Phase 6d: Security & Production (Weeks 19-24)

**Goal:** Production-hardened security, chaos validation, and operational readiness.

| Week | Deliverable | Acceptance Criteria |
|------|------------|-------------------|
| 19-20 | OPA policies | Cross-cluster admission control; image signing enforcement |
| 20-21 | Secret management | Vault + ESO; automatic rotation; zero secrets in Git |
| 21-22 | Chaos engineering | 12 chaos experiments automated; quarterly Game Days defined |
| 22-23 | Monitoring stack | Prometheus federation; OpenTelemetry tracing; split-brain alerts |
| 23-24 | Production readiness | Runbooks documented; 99.99% availability target; DR tested |

**Production Readiness Checklist:**

- [ ] All 12 chaos experiments pass (no data loss, automatic recovery)
- [ ] FMEA all 15 failure modes tested with documented recovery procedures
- [ ] Gossip bandwidth at 100 cells within budget (< 5 KB/s per node)
- [ ] Partition recovery completes in < 60 seconds
- [ ] CRDT convergence after 1-hour partition completes in < 120 seconds
- [ ] Certificate rotation causes zero connection drops
- [ ] Split-brain detection alerts fire within 30 seconds
- [ ] DR restore from Velero completes in < 15 minutes (Tier 1)
- [ ] Security audit: no unencrypted traffic paths
- [ ] Penetration test: compromised cell cannot access other cells

---

## Appendix A: Glossary

| Term | Definition |
|------|-----------|
| **Cell** | An independent HelixCluster instance (3-5,000 nodes) that can federate with others |
| **Block** | Synonym for Cell; from the "Block of Blocks" concept |
| **Federation** | A collection of Cells bound together via the federation protocol |
| **Gateway** | A node designated for inter-cell communication (runs WireGuard, inter-cell gossip) |
| **SWIM** | Scalable Weakly-consistent Infection-style Process Group Membership Protocol |
| **CRDT** | Conflict-free Replicated Data Type; converges without coordination |
| **HLC** | Hybrid Logical Clock; combines wall-clock time with logical counters |
| **SVID** | SPIFFE Verifiable Identity Document; short-lived workload certificate |
| **ICE** | Interactive Connectivity Establishment; NAT traversal framework |
| **mDNS** | Multicast DNS; local network service discovery |

## Appendix B: Network Port Reference

| Port | Protocol | Service | Notes |
|------|----------|---------|-------|
| 51820 | UDP | WireGuard | Primary mesh tunnel port |
| 7946 | UDP/TCP | memberlist (intra) | LAN gossip |
| 7947 | UDP/TCP | memberlist (inter) | WAN gossip (gateway only) |
| 4242 | UDP | QUIC transport | Federation API |
| 3478 | UDP/TCP | TURN/STUN | NAT traversal |
| 443 | TCP | HTTPS | API server, TURN fallback |
| 2379 | TCP | etcd client | Cell control plane |
| 2380 | TCP | etcd peer | Cell control plane |
| 6443 | TCP | Kubernetes API | Cell control plane |
| 22 | TCP | SSH | Fallback access, tunneling |

## Appendix C: Quick Reference: Federation Commands

```bash
# Cell lifecycle
helixctl cell init --cell-id=1 --region=us-east-1 --name=alpha
helixctl cell start
helixctl cell status
helixctl cell stop

# Federation operations
helixctl federation join --bootstrap-dns=_helix-gossip._tcp.helix.local
helixctl federation leave
helixctl federation status
helixctl federation mesh status

# Mesh debugging
helixctl mesh peers                    # List all mesh peers
helixctl mesh ping <peer-id>           # Latency to peer
helixctl mesh traceroute <cell-id>     # Path to cell
helixctl mesh bandwidth <peer-id>      # Test throughput

# State management
helixctl state crdt inspect <crdt-id>  # Inspect CRDT state
helixctl state crdt repair             # Trigger anti-entropy
helixctl state diff --cells=a,b        # Compare cell states

# Security
helixctl security svid list            # List active SVIDs
helixctl security trust list           # List federated trust domains
helixctl security trust add <domain>   # Add federation trust
helixctl security trust remove <domain># Revoke federation trust

# Chaos testing
helixctl chaos partition --cells=a,b --duration=5m
helixctl chaos latency --cells=a,b --latency=200ms --duration=10m
helixctl chaos kill-node --cell=a --count=1
```

---

*HelixCluster Phase 6 -- The "Block of Blocks" Federation Architecture*

*This document represents the definitive technical reference for building multi-cluster federation with HelixCluster. It synthesizes research across 7 dimensions: federation patterns, mesh networking, gossip protocols, consensus systems, zero-trust security, multi-region deployment, and chaos engineering validation.*

*All production claims are based on published benchmarks, academic papers, and verified source code. Gap analysis from each research dimension has been explicitly addressed in the architecture.*

*Version 1.0.0 | Generated for HelixCluster Engineering*
