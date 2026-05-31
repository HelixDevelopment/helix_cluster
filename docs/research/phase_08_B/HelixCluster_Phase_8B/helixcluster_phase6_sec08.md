## 8. Control Plane Federation

The preceding chapters established how individual HelixCluster cells bootstrap their mesh, tolerate faults, and pass chaos validation. This chapter addresses the natural follow-on question: how do multiple independent cells present a single, coherent control plane to operators and workloads? Control plane federation transforms a collection of self-managing cells into a unified distributed system — the "Block of Blocks."

HelixCluster Phase 6 adopts the principle of **per-cell strong consistency, cross-cell eventual consistency**. Raft-based consensus (etcd) never stretches across WAN; instead, a federation layer coordinates cross-cell operations without compromising per-cell autonomy. The result scales to 100 cells and 500,000 nodes while preserving the fault isolation that makes cells valuable.

### 8.1 Federated API Server

Every cell runs its own API server, scheduler, and etcd cluster — the full control plane for autonomous operation. The federated API server complements these local components by providing a single entry point for cross-cell operations while preserving each cell's ability to function independently.

#### 8.1.1 Single API Endpoint for All Cells

The federation proxy sits in front of all cell-local API servers, routing requests based on cell identity encoded in the request path or SPIFFE ID. When an operator issues `kubectl get pods --all-cells`, the proxy fans out the query to every reachable cell, aggregates results, and returns a unified response. When a workload specifies `cell: beta` in its placement policy, the proxy directs the request exclusively to Cell Beta's API server.

The routing layer makes three critical decisions per request: **Cell Targeting** (via `X-Helix-Cell` header, SPIFFE trust domain, or resource label), **Authentication** (cross-cell SPIFFE mTLS), and **Response Aggregation** (partial failures return available results with `Partial-Content: true` headers).

The following Go implementation shows the core federation proxy with cell routing, SPIFFE authentication, and request proxying:

```go
package federation

import (
    "context"
    "crypto/tls"
    "encoding/json"
    "fmt"
    "net/http"
    "net/http/httputil"
    "net/url"
    "strings"
    "sync"
    "time"
)

// CellBackend represents a single cell's API server endpoint.
type CellBackend struct {
    CellID      uint16
    Name        string
    TrustDomain string
    APIServer   string
    Proxy       *httputil.ReverseProxy
    Client      *http.Client  // mTLS-enabled
    Healthy     bool
    LastHealth  time.Time
    mu          sync.RWMutex
}

// FederatedAPIServer is the single entry point for cross-cell operations.
type FederatedAPIServer struct {
    listenAddr   string
    tlsConfig    *tls.Config
    bundleCache  *BundleCache
    backends     map[string]*CellBackend
    backendsMu   sync.RWMutex
    authz        *FederationManager
    router       *CellRouter
}

type CellRouter struct {
    strategies map[string]RouteStrategy
}

type RouteStrategy func(r *http.Request, backends map[string]*CellBackend) ([]string, error)

func NewFederatedAPIServer(addr string, tlsConf *tls.Config, cache *BundleCache) *FederatedAPIServer {
    return &FederatedAPIServer{
        listenAddr: addr,
        tlsConfig:  tlsConf,
        bundleCache: cache,
        backends:   make(map[string]*CellBackend),
        router: &CellRouter{
            strategies: map[string]RouteStrategy{
                "direct":    DirectRoute,
                "broadcast": BroadcastRoute,
                "affinity":  AffinityRoute,
            },
        },
    }
}

// RegisterCell adds a new cell backend to the federation proxy.
func (s *FederatedAPIServer) RegisterCell(cellID uint16, name, trustDomain, apiServer string, client *http.Client) error {
    targetURL, err := url.Parse(apiServer)
    if err != nil {
        return fmt.Errorf("invalid API server URL: %w", err)
    }
    proxy := httputil.NewSingleHostReverseProxy(targetURL)
    proxy.Transport = client.Transport
    proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
        s.markUnhealthy(name)
        w.WriteHeader(http.StatusServiceUnavailable)
        json.NewEncoder(w).Encode(map[string]string{
            "error": fmt.Sprintf("cell %s unreachable: %v", name, err),
        })
    }
    backend := &CellBackend{
        CellID: cellID, Name: name, TrustDomain: trustDomain,
        APIServer: apiServer, Proxy: proxy, Client: client,
        Healthy: true, LastHealth: time.Now(),
    }
    s.backendsMu.Lock()
    s.backends[name] = backend
    s.backendsMu.Unlock()
    return nil
}

// ServeHTTP implements the federation request handler.
func (s *FederatedAPIServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    spiffeID, err := s.authenticate(r)
    if err != nil {
        w.WriteHeader(http.StatusUnauthorized)
        json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
        return
    }
    strategy := r.Header.Get("X-Helix-Routing")
    if strategy == "" { strategy = "direct" }
    routeFn := s.router.strategies[strategy]
    if routeFn == nil {
        w.WriteHeader(http.StatusBadRequest)
        json.NewEncoder(w).Encode(map[string]string{"error": "unknown routing strategy"})
        return
    }
    s.backendsMu.RLock()
    backends := make(map[string]*CellBackend, len(s.backends))
    for k, v := range s.backends { backends[k] = v }
    s.backendsMu.RUnlock()

    targets, err := routeFn(r, backends)
    if err != nil {
        w.WriteHeader(http.StatusBadRequest)
        json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
        return
    }
    for _, target := range targets {
        backend := backends[target]
        if backend == nil { continue }
        if !s.authz.CanCommunicate(*spiffeID, SPIFFEID{TrustDomain: TrustDomain(backend.TrustDomain)}) {
            w.WriteHeader(http.StatusForbidden)
            json.NewEncoder(w).Encode(map[string]string{
                "error": fmt.Sprintf("access denied to cell %s", target),
            })
            return
        }
    }
    if len(targets) == 1 {
        s.routeSingle(w, r, backends[targets[0]])
    } else {
        s.routeAggregate(w, r, targets, backends)
    }
}

func (s *FederatedAPIServer) routeSingle(w http.ResponseWriter, r *http.Request, backend *CellBackend) {
    backend.mu.RLock()
    healthy := backend.Healthy
    backend.mu.RUnlock()
    if !healthy {
        w.WriteHeader(http.StatusServiceUnavailable)
        json.NewEncoder(w).Encode(map[string]string{
            "error": fmt.Sprintf("cell %s is unhealthy", backend.Name),
        })
        return
    }
    r.URL.Path = strings.TrimPrefix(r.URL.Path, "/federation/"+backend.Name)
    backend.Proxy.ServeHTTP(w, r)
}

func (s *FederatedAPIServer) authenticate(r *http.Request) (*SPIFFEID, error) {
    if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
        return nil, fmt.Errorf("mTLS required")
    }
    cert := r.TLS.PeerCertificates[0]
    for _, uri := range cert.URIs {
        if uri.Scheme == "spiffe" {
            return ParseSPIFFEID(uri.String())
        }
    }
    return nil, fmt.Errorf("no SPIFFE ID in certificate")
}

func (s *FederatedAPIServer) markUnhealthy(name string) {
    s.backendsMu.RLock()
    backend, ok := s.backends[name]
    s.backendsMu.RUnlock()
    if ok {
        backend.mu.Lock()
        backend.Healthy = false
        backend.mu.Unlock()
    }
}
```

#### 8.1.2 Cell-Local API Servers Handle Local Requests

Cell-local API servers serve all intra-cell operations — pod scheduling, service creation, config map updates — without federation involvement. This ensures that a network partition between cells does not degrade local operation: Cell Alpha continues scheduling pods even when it cannot reach Cell Beta. The federation proxy adds cross-cell capabilities without becoming a dependency for local functionality.

The `BroadcastRoute` strategy fans out read-only queries (`GET`, `LIST`) to all healthy cells and aggregates responses. Write operations (`POST`, `PUT`, `DELETE`) always use `DirectRoute` to a single target cell to avoid distributed transaction complexity. The `AffinityRoute` strategy pins sessions to a preferred cell based on latency or data locality, falling back on failure.

| Request Type | Routing Strategy | Failure Behavior | Consistency Guarantee |
|-------------|-----------------|------------------|----------------------|
| READ (GET/LIST) | Broadcast or Affinity | Partial results returned with warning | Eventual — may miss recent writes in partitioned cells |
| WRITE (POST/PUT) | Direct to target cell | Full error if target unreachable | Strong within target cell only |
| WATCH | Direct with cell affinity | Stream terminates; client reconnects | Per-cell linearizable |
| EXEC (kubectl exec) | Direct to pod's cell | Full error if cell unreachable | N/A — single cell operation |

*Table 8.1: Request routing strategies and their consistency guarantees. Reads can fan out; writes always target a single cell to avoid distributed transactions.*

### 8.2 Global Resource Scheduling

With multiple cells presenting a unified API, the next challenge is deciding *where* to place workloads. HelixCluster implements two-level scheduling modeled after the Borg/Omega hierarchy: a global allocator selects the target cell, and the cell-local Kubernetes scheduler selects the specific node.

#### 8.2.1 Two-Level Scheduling: Global Picks Cell, Local Picks Node

The global scheduler maintains a cached view of each cell's aggregate capacity — CPU, memory, GPU, and custom resources — propagated via hierarchical gossip. When a federated workload is submitted, the global scheduler evaluates cell candidates against placement constraints and selects the optimal cell. The cell-local scheduler then performs standard Kubernetes node selection within that cell.

This separation is architecturally essential. The global scheduler operates on aggregate cell-level metrics (O(cells) decision space), not individual node states (O(nodes) space). A 100-cell federation with 5,000 nodes each reduces the global scheduling problem from 500,000 nodes to 100 cell candidates — a 5,000x reduction in decision complexity. The cell-local scheduler, running within a single etcd consensus domain, handles node-level placement with full strong consistency.

The global scheduling algorithm evaluates candidates across weighted objectives:

```go
package scheduler

import (
    "context"
    "fmt"
    "math"
    "sort"
    "time"
)

type SchedulingConstraints struct {
    ResourceRequest  ResourceQuota
    DataLocality     []string
    MaxLatency       time.Duration
    CostBudget       float64
    ComplianceZones  []string
    CellAffinity     []string
    CellAntiAffinity []string
}

type CellSnapshot struct {
    Name         string
    CellID       uint16
    Region       string
    AvailableCPU int64
    AvailableMem int64
    AvailableGPU int64
    AvgLatency   time.Duration
    CostPerHour  float64
    Compliance   []string
    Labels       map[string]string
}

type GlobalScheduler struct {
    cellIndex map[string]*CellSnapshot
}

type CellScorer struct {
    CapacityWeight   float64
    LatencyWeight    float64
    CostWeight       float64
    BalanceWeight    float64
    ComplianceWeight float64
}

func defaultScorer() *CellScorer {
    return &CellScorer{
        CapacityWeight: 0.30, LatencyWeight: 0.25, CostWeight: 0.20,
        BalanceWeight: 0.15, ComplianceWeight: 0.10,
    }
}

// Schedule selects the best cell using weighted multi-objective scoring.
func (gs *GlobalScheduler) Schedule(ctx context.Context, c SchedulingConstraints) (*CellSnapshot, error) {
    candidates := gs.filterCandidates(c)
    if len(candidates) == 0 {
        return nil, fmt.Errorf("no cell satisfies hard constraints")
    }
    scores := gs.scoreCandidates(candidates, c)
    sort.SliceStable(scores, func(i, j int) bool { return scores[i].Score > scores[j].Score })
    return scores[0].Cell, nil
}

func (gs *GlobalScheduler) filterCandidates(c SchedulingConstraints) []*CellSnapshot {
    var valid []*CellSnapshot
    for _, cell := range gs.cellIndex {
        if cell.AvailableCPU < c.ResourceRequest.CPU || cell.AvailableMem < c.ResourceRequest.Memory {
            continue
        }
        if c.MaxLatency > 0 && cell.AvgLatency > c.MaxLatency {
            continue
        }
        if !hasAll(cell.Compliance, c.ComplianceZones) {
            continue
        }
        if contains(c.CellAntiAffinity, cell.Name) {
            continue
        }
        if !hasAllLabels(cell.Labels, c.DataLocality) {
            continue
        }
        valid = append(valid, cell)
    }
    return valid
}

func (gs *GlobalScheduler) scoreCandidates(cells []*CellSnapshot, c SchedulingConstraints) []ScoredCell {
    scorer := defaultScorer()
    maxCPU, maxMem, maxCost := normalizeBaselines(cells)
    results := make([]ScoredCell, 0, len(cells))
    for _, cell := range cells {
        cpuScore := float64(cell.AvailableCPU) / maxCPU
        memScore := float64(cell.AvailableMem) / maxMem
        capacityScore := (cpuScore + memScore) / 2.0
        latencyMs := float64(cell.AvgLatency.Milliseconds())
        latencyScore := math.Exp(-latencyMs / 50.0)
        costScore := 1.0 - (cell.CostPerHour / maxCost)
        if costScore < 0 { costScore = 0 }
        utilization := 1.0 - (float64(cell.AvailableCPU) / maxCPU)
        balanceScore := 1.0 - utilization
        complianceScore := 1.0
        if len(c.ComplianceZones) > 0 {
            complianceScore = complianceMatchScore(cell.Compliance, c.ComplianceZones)
        }
        affinityBonus := 0.0
        if contains(c.CellAffinity, cell.Name) { affinityBonus = 0.15 }
        score := scorer.CapacityWeight*capacityScore +
            scorer.LatencyWeight*latencyScore +
            scorer.CostWeight*costScore +
            scorer.BalanceWeight*balanceScore +
            scorer.ComplianceWeight*complianceScore +
            affinityBonus
        results = append(results, ScoredCell{Cell: cell, Score: score})
    }
    return results
}

type ScoredCell struct {
    Cell  *CellSnapshot
    Score float64
}

type ResourceQuota struct {
    CPU    int64
    Memory int64
    GPU    int64
}
```

#### 8.2.2 Constraints: Data Locality, Latency, Cost, Compliance

The scheduling algorithm evaluates four primary constraint categories:

**Data Locality.** Workloads accessing large datasets must run on cells that either host the data or have low-latency links. The scheduler checks cell labels (`data.helix.io/dataset-X=local`) and storage class availability. For stateful workloads, the scheduler additionally verifies that the target cell has sufficient storage capacity and that cross-cell volume migration completes within tolerance.

**Latency Requirements.** Latency-sensitive services specify maximum RTT from their clients. The scheduler uses continuously measured inter-cell latencies (gossip-propagated Phi accrual samples) to filter candidates. A service requiring `< 10ms` RTT from `us-east-1` will only schedule to cells in that region.

**Cost Optimization.** The federation supports heterogeneous cost profiles: on-premise (low marginal cost), cloud reserved (medium), cloud spot (low cost, interruptible), and edge (premium for latency). The scheduler normalizes these into cost-per-compute-unit. A workload with `costBudget: 5.00` USD/hour might prefer spot-capable cells but fall back to reserved instances when spot capacity is unavailable.

**Compliance Boundaries.** Data sovereignty requirements (GDPR, HIPAA, ITAR) are enforced as hard constraints. Cells advertise compliance certifications via gossip labels. A pod with `complianceZones: ["gdpr"]` can only schedule to GDPR-certified cells — no affinity weighting can override this.

### 8.3 Federated Service Discovery

Service discovery in a federation must answer: "Where is the nearest healthy instance of service X, and how do I reach it across cells?" HelixCluster solves this with a two-tier registry: cell-local registries handle intra-cell resolution, and a global federated registry enables cross-cell service location.

#### 8.3.1 Cell-Local Registry + Global Federated Registry

Each cell runs CoreDNS and Kubernetes EndpointSlice controllers for local services. When a service is annotated with `helix.io/federate: "true"`, the federation agent replicates its endpoints to the global registry via CRDT synchronization. The global registry maintains a merged view of all federated services, enabling cross-cell DNS resolution and health-aware load balancing.

The global registry is itself a distributed CRDT OR-Set (Observed-Removed Set), where each cell adds endpoint entries with unique tags and removes entries when they become unhealthy. Because OR-Sets are conflict-free, concurrent updates from multiple cells converge automatically without coordination.

| Registry Tier | Scope | Consistency | Technology | Update Latency |
|-------------|-------|-------------|-----------|----------------|
| Cell-local | Single cell | Strong (etcd-backed) | CoreDNS + EndpointSlices | < 1 second |
| Global federated | Cross-cell | Eventual (CRDT) | Gossip-propagated OR-Set | 5-30 seconds |
| DNS cache | Client-side | TTL-based | CoreDNS with federated forward | 30-300 seconds |

*Table 8.2: Service discovery tiers and their consistency properties. The global registry trades strong consistency for partition tolerance — the correct choice for cross-cell metadata.*

#### 8.3.2 Service Mesh Integration: Cilium Cluster Mesh for Cross-Cell Connectivity

While the global registry answers "where is the service," Cilium Cluster Mesh answers "how do I reach it." Cilium connects cells at L3/L4 using eBPF, enabling direct pod-to-pod connectivity across cluster boundaries without gateway hops or sidecar proxies.

Each cell runs Cilium with a unique cluster ID (1-255). Cluster Mesh establishes etcd-backed state synchronization between cells, propagating endpoints, network policies, and security identities. The eBPF datapath performs cross-cluster load balancing in kernel space, achieving **0.5-1ms p99 latency overhead**.

```yaml
# cilium-clustermesh.yaml — Cilium Cluster Mesh configuration
apiVersion: cilium.io/v2alpha1
kind: CiliumClusterMeshConfig
metadata:
  name: helix-federation-mesh
spec:
  clusters:
    - id: 1
      name: cell-alpha
      address: "cell-alpha-apiserver.helix.local:2379"
      caCertRef:
        name: cilium-ca-alpha
        namespace: kube-system
    - id: 2
      name: cell-beta
      address: "cell-beta-apiserver.helix.local:2379"
      caCertRef:
        name: cilium-ca-beta
        namespace: kube-system
    - id: 3
      name: cell-gamma
      address: "cell-gamma-apiserver.helix.local:2379"
      caCertRef:
        name: cilium-ca-gamma
        namespace: kube-system
  mesh:
    maxConnectedClusters: 255
    serviceAffinity: local
    loadBalancer:
      algorithm: maglev
      mode: dsr
    encryption:
      enabled: true
      type: wireguard
      nodeEncryption: true
    identityAllocation:
      mode: kvstore
      maxClusterIdentity: 65535
  crossClusterPolicies:
    enabled: true
    denyByDefault: true
    allowedLabels:
      - "helix.io/trust-tier=standard"
      - "helix.io/trust-tier=privileged"
```

For a service to be accessible across cells, annotate it with `io.cilium/global-service: "true"`. Cilium propagates endpoints to all connected clusters via Cluster Mesh etcd. A pod in Cell Alpha reaching `payment-api.default.svc.cluster.local` is transparently load-balanced to healthy instances in Cell Beta or Cell Gamma — with eBPF forwarding at kernel speed.

Network policies are equally cross-cluster capable. A `CiliumNetworkPolicy` can allow traffic from `app=frontend` in Cell Alpha to `app=payment-api` in any cell, with enforcement at the eBPF level on both source and destination nodes. Identity-based policies remain valid as pods scale and IP addresses change.

### 8.4 Configuration Management

Federation multiplies the configuration management challenge. A hundred-cell deployment requires a mechanism to declare desired state once and propagate it reliably — while allowing cell-local overrides for region-specific tuning.

#### 8.4.1 GitOps with ArgoCD ApplicationSets: Declarative Multi-Cluster Config

HelixCluster uses ArgoCD ApplicationSets as its primary configuration distribution mechanism. ApplicationSets generate one ArgoCD Application per target cell from a single template, enabling GitOps-driven deployment across the entire federation.

The `ClusterGenerator` auto-discovers all federated cells by querying ArgoCD's cluster secrets (populated by the HelixCluster federation agent when cells join). The `GitGenerator` enables per-cell overlays by reading files from a directory structure organized by cell name. Combined, they enable: base configuration everywhere, with cell-specific overlays for resource limits, replica counts, feature flags, and compliance settings.

```yaml
# federation-appset.yaml — ArgoCD ApplicationSet for multi-cell GitOps
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: helix-platform-services
  namespace: argocd
spec:
  generators:
    - matrix:
        generators:
          - clusters:
              selector:
                matchLabels:
                  helix.io/federation-member: "true"
                  helix.io/environment: "production"
          - git:
              repoURL: https://github.com/helixcluster/federation-config.git
              revision: HEAD
              files:
                - path: "config/{{name}}/app-config.yaml"
  template:
    metadata:
      name: 'platform-{{name}}'
      labels:
        helix.io/cell: '{{name}}'
        helix.io/managed-by: applicationset
    spec:
      project: federation-platform
      source:
        repoURL: https://github.com/helixcluster/federation-config.git
        targetRevision: HEAD
        path: 'platform-services/overlays/{{name}}'
        helm:
          values: |
            cellName: "{{name}}"
            cellID: "{{metadata.labels.helix.io/cell-id}}"
            region: "{{metadata.labels.helix.io/region}}"
            trustDomain: "{{metadata.labels.helix.io/trust-domain}}"
            resources:
              requests:
                cpu: "{{metadata.labels.helix.io/default-cpu-request}}"
                memory: "{{metadata.labels.helix.io/default-mem-request}}"
            mesh:
              clusterID: "{{metadata.labels.helix.io/cilium-cluster-id}}"
              enabled: true
              gatewayNodes: "{{metadata.labels.helix.io/gateway-count}}"
      destination:
        server: '{{server}}'
        namespace: helix-platform
      syncPolicy:
        automated:
          prune: true
          selfHeal: true
          allowEmpty: false
        retry:
          limit: 5
          backoff:
            duration: 5s
            factor: 2
            maxDuration: 3m
        syncOptions:
          - CreateNamespace=true
          - PrunePropagationPolicy=foreground
          - PruneLast=true
  strategy:
    type: RollingSync
    rollingSync:
      steps:
        - matchExpressions:
            - key: helix.io/canary
              operator: In
              values: ["true"]
          maxUpdate: 100%
        - matchExpressions:
            - key: helix.io/tier
              operator: In
              values: ["tier-2"]
          maxUpdate: 50%
        - matchExpressions:
            - key: helix.io/tier
              operator: In
              values: ["tier-1"]
          maxUpdate: 25%
```

This ApplicationSet demonstrates several production patterns. The `matrix` generator combines cluster auto-discovery with per-cell Git configuration. The `syncPolicy` enables automated pruning and self-healing — if a resource is removed from Git, ArgoCD removes it from the cell. The `RollingSync` strategy implements progressive rollout: canary cells first, then tier-2 at 50% concurrency, and finally tier-1 production at 25% to minimize blast radius.

#### 8.4.2 CRDT-Based Config Sync for Cell-Local Overrides

GitOps via ArgoCD works well for declarative base configurations, but some state changes too frequently for Git commits or requires cell-local resolution without central coordination. For this, HelixCluster uses CRDT-based configuration synchronization — each cell modifies configuration locally, and all cells converge to the same final state.

The configuration sync system uses three CRDT types: LWW-Register for single values like feature flags, G-Counter for numeric quotas, and OR-Set for label collections. Each cell maintains a local replica, and changes propagate via inter-cell gossip with delta-state encoding.

```go
package config

import (
    "encoding/json"
    "fmt"
    "sync"

    "helix.io/federation/crdt"
)

// ConfigKey is a namespaced configuration key.
type ConfigKey struct {
    Namespace string // e.g., "networking", "scheduling", "security"
    Name      string // e.g., "max-pods-per-node", "feature-flag-x"
}

func (k ConfigKey) String() string { return fmt.Sprintf("%s/%s", k.Namespace, k.Name) }

// FederatedConfigStore manages CRDT-based configuration across cells.
type FederatedConfigStore struct {
    mu         sync.RWMutex
    localCell  uint16
    hlc        *HLC
    registers  map[ConfigKey]*LWWRegister
    counters   map[ConfigKey]*GCounter
    sets       map[ConfigKey]*ORSet
    deltaQueue chan DeltaUpdate
}

type DeltaUpdate struct {
    CellID    uint16
    Key       ConfigKey
    Type      string // "register", "counter", "set"
    Payload   []byte
    Timestamp HLCTimestamp
}

func NewFederatedConfigStore(cellID uint16, hlc *HLC) *FederatedConfigStore {
    return &FederatedConfigStore{
        localCell:  cellID,
        hlc:        hlc,
        registers:  make(map[ConfigKey]*LWWRegister),
        counters:   make(map[ConfigKey]*GCounter),
        sets:       make(map[ConfigKey]*ORSet),
        deltaQueue: make(chan DeltaUpdate, 1000),
    }
}

// SetRegister updates an LWW-Register and queues delta for sync.
func (s *FederatedConfigStore) SetRegister(key ConfigKey, value []byte) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    ts := s.hlc.Now()
    reg, ok := s.registers[key]
    if !ok {
        reg = &LWWRegister{}
        s.registers[key] = reg
    }
    updated := reg.Set(value, ts.Physical, fmt.Sprintf("cell-%d", s.localCell))
    if !updated { return nil }
    select {
    case s.deltaQueue <- DeltaUpdate{
        CellID: s.localCell, Key: key, Type: "register",
        Payload: value, Timestamp: ts,
    }:
    default:
    }
    return nil
}

// GetRegister returns the current value of an LWW-Register.
func (s *FederatedConfigStore) GetRegister(key ConfigKey) ([]byte, bool) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    reg, ok := s.registers[key]
    if !ok { return nil, false }
    val, _, _ := reg.Get()
    return val, true
}

// ApplyDelta merges a remote delta into the local CRDT replica.
func (s *FederatedConfigStore) ApplyDelta(delta DeltaUpdate) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    switch delta.Type {
    case "register":
        reg, ok := s.registers[delta.Key]
        if !ok {
            reg = &LWWRegister{}
            s.registers[delta.Key] = reg
        }
        reg.Set(delta.Payload, delta.Timestamp.Physical,
            fmt.Sprintf("cell-%d", delta.CellID))
    case "counter":
        var remoteCounts map[string]uint64
        if err := json.Unmarshal(delta.Payload, &remoteCounts); err != nil {
            return fmt.Errorf("unmarshal counter delta: %w", err)
        }
        ctr, ok := s.counters[delta.Key]
        if !ok {
            ctr = crdt.NewGCounter()
            s.counters[delta.Key] = ctr
        }
        for node, count := range remoteCounts {
            if count > ctr.Counts[node] { ctr.Counts[node] = count }
        }
    case "set":
        var remoteDelta struct {
            Adds    map[string]map[string]struct{}
            Removes map[string]map[string]struct{}
        }
        if err := json.Unmarshal(delta.Payload, &remoteDelta); err != nil {
            return fmt.Errorf("unmarshal set delta: %w", err)
        }
        oset, ok := s.sets[delta.Key]
        if !ok {
            oset = crdt.NewORSet()
            s.sets[delta.Key] = oset
        }
        oset.Merge(&crdt.ORSet{Adds: remoteDelta.Adds, Removes: remoteDelta.Removes})
    default:
        return fmt.Errorf("unknown CRDT type: %s", delta.Type)
    }
    return nil
}

// DeltaSync returns pending deltas for inter-cell gossip transmission.
func (s *FederatedConfigStore) DeltaSync() []DeltaUpdate {
    var deltas []DeltaUpdate
    for i := 0; i < 100; i++ {
        select {
        case d := <-s.deltaQueue:
            deltas = append(deltas, d)
        default:
            return deltas
        }
    }
    return deltas
}

// AntiEntropy performs full state comparison with a remote cell
// using Merkle tree comparison to identify divergent keys.
func (s *FederatedConfigStore) AntiEntropy(remoteCell uint16, sendDelta func([]DeltaUpdate) error) error {
    tree := crdt.NewMerkleTree()
    s.mu.RLock()
    for key, reg := range s.registers {
        val, ts, _ := reg.Get()
        tree.Insert(key.String(), append(val, ts.ToJSON()...))
    }
    s.mu.RUnlock()
    deltas := s.DeltaSync()
    if len(deltas) > 0 { return sendDelta(deltas) }
    return nil
}
```

The CRDT configuration system provides a capability that GitOps alone cannot: **partition-tolerant local configuration changes**. If Cell Gamma becomes network-partitioned, operators can still modify feature flags, adjust rate limits, or update allow-lists locally. When the partition heals, CRDT merge semantics guarantee convergence without manual intervention. A cell that cannot modify its own configuration during a partition cannot adapt to failures.

| Config Distribution Method | Latency | Partition Tolerance | Override Support | Best For |
|---------------------------|---------|-------------------|------------------|----------|
| ArgoCD ApplicationSets | 1-3 minutes | Read-only during partition | Per-cell Git overlays | Base infrastructure, versioned configs |
| CRDT sync (real-time) | 5-30 seconds | Full read-write during partition | Automatic merge | Feature flags, rate limits, emergency overrides |
| Karmada PropagationPolicy | 1-10 seconds | Depends on hub availability | OverridePolicy per cluster | Workload resources, policy objects |

*Table 8.3: Configuration distribution mechanisms in HelixCluster federation. Each method targets different latency, consistency, and partition-tolerance requirements.*

The combination of ArgoCD ApplicationSets for declarative base configuration and CRDT-based sync for dynamic local state gives operators both the auditability of GitOps and the resilience of partition-tolerant replication. A feature flag can be flipped globally via Git commit (propagating through ArgoCD in 1-3 minutes) or locally via the cell's configuration API (converging to other cells via gossip in 5-30 seconds) — the right tool for the right operational scenario.

This control plane federation architecture — unified API entry point, two-level scheduling, two-tier service discovery, and dual-mode configuration management — provides the operational foundation for running workloads across 100 cells and 500,000 nodes as though they were a single cluster, while preserving the fault isolation and administrative autonomy that make multi-cell deployments viable in production.
