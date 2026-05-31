# 7. Testing, Chaos & Validation

> *"The most reliable systems are those that have failed the most in controlled conditions."*
>
> Every mechanism described in prior chapters — from CRDT convergence to WireGuard mesh reassembly — is only as trustworthy as the validation behind it. This chapter details the multi-layered testing strategy that hardens HelixCluster federation before it touches production traffic. The approach combines deterministic simulation at the protocol layer, chaos engineering at the system layer, failure-mode analysis at the architectural layer, and comprehensive observability that closes the feedback loop.

---

## 7.1 Deterministic Simulation

Production-hardened distributed systems share one trait: they were broken repeatedly in simulation before they ever saw a real network partition. etcd runs approximately 8,000 fault injections per day in its continuous functional tester, totaling 1.7 million injections over a single campaign. FoundationDB spent 18 months building its deterministic simulator before writing a byte to physical disk — an investment that yielded what many consider the most robust distributed database in existence.

### 7.1.1 Turmoil-based Multi-Cluster Protocol Testing in Rust

HelixCluster's gossip and consensus protocols are written in Rust. For unit-level deterministic simulation, the project adopts **Turmoil**, a framework from the Tokio project that simulates hosts, time, and network behavior within a single process on a single thread. Turmoil provides fine-grained control over message dropping, holding, and delaying without OS thread scheduling nondeterminism.

A Turmoil test for inter-cell gossip convergence:

```rust
let mut sim = turmoil::Builder::new()
    .simulation_duration(Duration::from_secs(300))
    .tick_duration(Duration::from_millis(10))
    .build();

// Spawn 10 simulated cells, 3 gateway nodes each
for cell_id in 0..10 {
    for gw in 0..3 {
        let name = format!("cell{}-gw{}", cell_id, gw);
        sim.host(name.clone(), move || {
            run_gateway_node(cell_id, gw)
        });
    }
}

// Simulate WAN partition between cell 0 and cell 1 at T=60s
sim.partition("cell0-gw0", "cell1-gw0");
sim.partition("cell0-gw1", "cell1-gw1");
sim.partition("cell0-gw2", "cell1-gw2");

// Heal at T=180s
sim.heal("cell0-gw0", "cell1-gw0");
sim.heal("cell0-gw1", "cell1-gw1");
sim.heal("cell0-gw2", "cell1-gw2");

sim.run()?;

// Assert: all cells eventually converge to identical membership state
assert_convergence(&sim, Duration::from_secs(30));
```

Turmoil's key property is **perfect reproducibility**: the same seed produces bit-identical execution. When a test fails, the developer receives a trace file that replays the exact event sequence. Turmoil tests run on every pull request, covering gossip convergence (every cell learns all others' membership within O(log C) rounds), split-brain prevention, CRDT monotonicity, and message durability under any single-node failure. These tests execute in seconds of wall-clock time but simulate hours of cluster activity.

### 7.1.2 Simulating 100 Cells with WAN Latency, Partitions, and Node Churn

For integration testing with real network stacks, HelixCluster uses **Shadow**, a discrete-event simulator that runs unmodified application binaries as native Linux processes through a simulated network. Shadow has simulated Tor at 6,500+ relay scale. A single machine can simulate 100+ HelixCluster cells with realistic WAN topologies:

| Scenario | Latency | Jitter | Packet Loss | Bandwidth | Test Purpose |
|----------|---------|--------|-------------|-----------|--------------|
| Same region, different AZ | 1-5 ms | 0.5 ms | 0.00% | 10 Gbps | AZ failover |
| Cross-region (US East-West) | 60-80 ms | 5 ms | 0.01% | 1 Gbps | Standard federation |
| Transatlantic | 140-180 ms | 10 ms | 0.10% | 500 Mbps | EU-US federation |
| Asia-Pacific | 200-300 ms | 20 ms | 0.50% | 200 Mbps | APAC federation |
| Degraded WAN | 300+ ms | 50 ms | 1-5% | 10 Mbps | Disaster scenario |
| Satellite link | 600+ ms | 100 ms | 2.0% | 1 Mbps | Edge/disconnected |

*Table 7.1: WAN Latency Simulation Matrix — six reference network profiles used in Shadow-based integration tests.*

Shadow tests inject the following fault patterns into the simulation:

- **Random node churn**: Kill and restart 5% of gateway nodes every 60 simulated seconds, modeling spot-instance termination and rolling restarts.
- **Rolling partitions**: Each 300-second interval, a randomly chosen pair of cells is partitioned for 60 seconds, then healed.
- **Asymmetric failures**: Cell A can reach Cell B, but B cannot reach A — the most pernicious partition class, created using directional packet filters.
- **Clock skew**: Advance or retard individual cell clocks by up to ±500 ms to test hybrid logical-clock behavior.

A full 100-cell Shadow simulation with 10,000 total nodes completes in approximately 45 minutes on a 64-core workstation. The test suite runs nightly on the project's CI cluster. Failures trigger automatic bisection to identify the minimal reproducing sequence.

---

## 7.2 Chaos Engineering Catalog

Deterministic simulation proves the protocols correct. Chaos engineering proves the implementation survives reality. The distinction matters: Antithesis found a critical etcd watch bug after 830 hours of testing that simulated 4.5 years of usage — a bug present in all stable releases and missed by years of conventional testing. Chaos is not an afterthought; it is a prerequisite for production deployment.

### 7.2.1 The 12 Chaos Experiments

HelixCluster defines twelve canonical chaos experiments organized across five categories: Node, Network, Resource, Time, and Cascading. Each experiment specifies target scope, expected system behavior, abort criteria that prevent customer impact, and the Chaos Mesh or custom tooling configuration that implements it.

| # | Category | Experiment | Tool | Target | Expected Behavior | Abort Criteria | Frequency |
|---|----------|-----------|------|--------|-------------------|----------------|-----------|
| 1 | Node | Kill random gateway | `PodChaos` (pod-kill) | Gateway pods | Mesh re-routes via alternative gateways within 5s | Cross-cell traffic drop >1% for 30s | Continuous (staging) |
| 2 | Node | Kill control-plane node | `PodChaos` (pod-kill) | etcd / API server pods | Cluster remains writable if quorum maintained | Any etcd quorum loss event | Weekly (staging) |
| 3 | Node | Rolling drain of cell | `NodeChaos` (drain) | All nodes in one cell | Cell enters graceful leaving state; peers redistribute workload | Any data loss event | Quarterly (Game Day) |
| 4 | Network | Full inter-cell partition | `NetworkChaos` (partition) | All links between two cells | Cells operate autonomously; queue cross-cell writes | Split-brain detected (dual leaders) | Per Game Day |
| 5 | Network | Partial/asymmetric partition | `tc netem` + custom | Directional filters on gateway nodes | Degraded path detected; traffic reroutes via alternative path | Inconsistent read detected | Per Game Day |
| 6 | Network | WAN latency spike (300 ms) | `NetworkChaos` (delay) | All inter-cluster links | Request timeouts trigger circuit breakers; local fallback activated | Error rate >0.1% for 60s | Weekly |
| 7 | Network | Packet loss burst (5%) | `NetworkChaos` (loss) | Inter-cluster links | TCP retransmissions; QUIC handles natively; no app errors | Error rate >0.5% for 60s | Weekly |
| 8 | Resource | Gossip bandwidth saturation | `StressChaos` (network) | Gossip daemon pods | Backpressure activates; convergence slows but does not fail | Memory exhaustion on any node | Monthly |
| 9 | Resource | etcd disk pressure | `StressChaos` (disk) | etcd data volumes | etcd compaction triggers automatically; writes may slow | etcd write latency >1s for 30s | Monthly |
| 10 | Time | Clock skew (+/- 500 ms) | `TimeChaos` (skew) | Node clocks in one cell | Hybrid logical clocks absorb skew; no ordering violations | Inconsistent event ordering detected | Per Game Day |
| 11 | Cascading | Sequential cell failures | Custom script | Three cells in 5-minute succession | Federation rebalances; no cascading overload | >50% federation capacity loss | Quarterly (Game Day) |
| 12 | Security | Certificate expiry simulation | Custom script | SPIFFE/SPIRE cert rotation | Automatic rotation before expiry; fallback to mTLS with warning | Any TLS handshake failure | Quarterly |

*Table 7.2: Chaos Experiment Catalog — twelve canonical experiments with tooling, safety criteria, and execution frequency.*

Experiment 5 (asymmetric partition) deserves special attention because it is the most difficult to simulate and the most likely to expose subtle bugs. Standard partition tools drop all traffic between nodes. Real-world asymmetric failures — where A can reach B but B cannot reach A, typically caused by firewall state desynchronization or NAT table exhaustion — require directional filtering. HelixCluster implements these using `tc` with flowid classification:

```bash
# Asymmetric partition: cell0 can reach cell1, but cell1 cannot reach cell0
# Applied on cell0 gateway nodes — outbound to cell1 is normal,
# but we drop INBOUND from cell1 by filtering on source IP at ingress

tc qdisc add dev eth0 root handle 1: prio
tc qdisc add dev eth0 parent 1:3 handle 30: netem drop 100%
tc filter add dev eth0 protocol ip parent 1:0 prio 3 u32 \
  match ip src 10.1.0.0/16 flowid 1:3
```

This directional drop survives for the experiment duration (typically 120 seconds), after which the `tc` rules are removed and connectivity is validated.

### 7.2.2 Chaos Mesh Multi-Cluster RemoteCluster Experiments

For staging and production chaos, HelixCluster uses **Chaos Mesh**, a CNCF incubating project that provides Kubernetes-native fault injection via CRDs. The `RemoteCluster` resource enables a single Chaos Mesh control plane to inject faults into multiple HelixCluster cells simultaneously:

```yaml
apiVersion: chaos-mesh.org/v1alpha1
kind: RemoteCluster
metadata:
  name: cell-ap-south
  namespace: chaos-mesh
spec:
  namespace: chaos-mesh
  kubeConfig:
    secretRef:
      name: cell-ap-south-kubeconfig
      namespace: chaos-mesh
---
apiVersion: chaos-mesh.org/v1alpha1
kind: NetworkChaos
metadata:
  name: partition-ap-south-from-us-east
  namespace: chaos-mesh
spec:
  remoteCluster: cell-ap-south
  action: partition
  mode: all
  selector:
    labelSelectors:
      'app.kubernetes.io/component': 'gateway'
  direction: both
  target:
    selector:
      labelSelectors:
        'app.kubernetes.io/component': 'gateway'
    mode: all
  duration: '5m'
  externalTargets:
    - 10.0.1.0/24  # us-east gateway CIDR
```

The `RemoteCluster` CRD stores each cell's kubeconfig as a Kubernetes secret. The central Chaos Mesh dashboard provides a unified view of experiment status across all cells. Key limitations to note: `RemoteCluster` is still in early development; version skew between the central Chaos Mesh instance and remote agent versions must be kept within one minor version, and authentication rotation requires manual secret updates.

### 7.2.3 Game Day Exercises: Quarterly Federation-Wide Chaos Drills

Game Days are planned, cross-team chaos exercises where failure is injected into staging or carefully scoped production infrastructure while the organization practices incident response. HelixCluster runs four Game Days per year:

**Q1: Inter-Region Partition.** The largest region is fully partitioned for 15 minutes. Validates regional autonomy, circuit breaker behavior, and CRDT reconvergence within 60 seconds of heal.

**Q2: Rolling Cell Evacuation.** Three cells sequentially leave with 5-minute spacing. Validates workload redistribution and 80% capacity ceiling on remaining cells.

**Q3: CA Compromise Simulation.** The SPIFFE intermediate CA for one region is revoked. Validates mTLS rejection, identity re-issuance within 10 minutes, and cross-region revocation propagation.

**Q4: Cascading Overload.** One cell driven to 95% CPU. Validates that circuit breakers, rate limiters, and bulkheads contain the blast radius.

Each Game Day follows strict protocol: pre-drill alignment document one week prior; 24-hour advance notice to on-call engineers; blast radius limited to 30% of production traffic; written abort criteria ("If API error rate exceeds 1% for 60 seconds, abort immediately"); and a post-drill retrospective within 48 hours.

---

## 7.3 FMEA: Failure Mode and Effects Analysis

Chaos engineering validates that the system survives known failure modes. FMEA identifies the modes that chaos might miss. The analysis below covers fifteen failure modes specific to federated multi-cluster systems, each characterized by detection time, blast radius, recovery procedure, and prevention strategy.

| ID | Failure Mode | Cause | Detection Time | Blast Radius | Recovery Procedure | Prevention |
|----|-------------|-------|----------------|--------------|-------------------|------------|
| F-01 | Single node failure | Hardware fault, kernel panic, OOM killer | 1-5 s (gossip health probe) | None — Raft replication masks failure | Automatic: replacement node joins, Raft log catches up | 3-5 node etcd clusters; memory limits on all pods |
| F-02 | Single cell partition | WAN link cut, ISP outage, firewall misconfig | 5-30 s (inter-cell gossip timeout) | Affected cell goes read-only if minority; others continue | Automatic on heal: queued writes replay, CRDTs merge | Quorum-based writes; multi-path gateway links |
| F-03 | Split-brain (dual leaders) | Network partition + quorum edge case | 1-60 s (Prometheus etcd_server_is_leader > 1) | Data inconsistency if writes proceed on both sides | Manual: identify epoch, force leader step-down on stale side | Strict majority quorum; witness nodes in odd clusters; etcd `--pre-vote` |
| F-04 | Inter-cell link degradation | Congestion, router bufferbloat, QoS drop | 5-15 s (probe RTT histogram) | Degraded cross-cell throughput; circuit breakers may open | Automatic: traffic reroutes via alternative gateways; link heals | Multiple independent WAN paths per cell pair; ECMP routing |
| F-05 | Complete cell failure | Power loss, natural disaster, full etcd loss | 10-60 s (gossip suspicion) | Total loss of that cell's workload until DR | Manual/DR: Velero restore from last backup (~15 min RPO) | Cross-cell workload replicas; Velero hourly backups; Pilot Light DR |
| F-06 | Gossip protocol saturation | Too many nodes per cell; fanout too high | Minutes (bandwidth metrics) | Slower convergence; stale membership state | Automatic: backpressure reduces fanout; gossip interval stretches | Bandwidth limits; max 5,000 nodes per cell; WAN fanout capped at 2 |
| F-07 | Clock skew > threshold | NTP drift, VM time smear, hypervisor bug | Varies (monitoring compares HLC vs wall) | Inconsistent event ordering; causality violations | NTP recovery; if persistent, vector clock divergence triggers anti-entropy | Logical hybrid clocks (HLC); NTP monitoring alerts at ±50 ms; `chrony` with `maxslewrate` |
| F-08 | Cascading failure overload | Retry storm, circuit breaker miss, bulkhead leak | Seconds to minutes (latency spikes propagate) | Full federation outage if not contained | Emergency: manual circuit breaker trip; rate limit injection; traffic shed | Bulkhead pattern per cell; rate limiting; retry with exponential backoff + jitter |
| F-09 | CRDT state divergence | Bug in merge function; missed delta; partition + concurrent update | Minutes to hours (Merkle tree hash mismatch) | Inconsistent cell state; divergent service routing | State rebuild from source of truth; full anti-entropy pass | Delta-CRDTs with Merkle tree comparison; periodic anti-entropy (15 min) |
| F-10 | X.509/SPIFFE certificate expiry | Rotation failure; SPIRE outage; clock skew | Days (cert expiry alerts) | TLS handshake failures; service mesh partition | Emergency rotation via manual `openssl` or SPIRE forced re-issue; hot-reload | Automated cert rotation (30-day expiry, 7-day renewal); SPIRE health monitoring |
| F-11 | etcd quorum loss | Simultaneous 2-of-3 node failure; disk corruption on leader | Immediate (`etcd_server_has_leader == 0`) | Control plane read-only; no new pods, no policy changes | Restore quorum: replace failed nodes from snapshot; if persistent, restore from Velero | Minimum 3 nodes per cell; SSD-backed storage; separate AZ placement |
| F-12 | Asymmetric network partition | Stateful firewall failure; NAT table exhaustion; BGP route leak | 10-60 s (bidirectional health check mismatch) | Subtle consistency issues; one-sided timeouts; ghost members | Automatic on heal; if persistent, manual routing table repair | Bidirectional health checks (both A→B and B→A); TCP + ICMP probes; SWIM indirect probes |
| F-13 | Control plane overload | API server abuse; watch explosion; large LIST queries | Seconds (`apiserver_request_duration_seconds` > 1s) | API degradation; scheduling stalls; policy lag | Scale out API server replicas; add rate limiting; restart abusive clients | Rate limiting (500 req/s per client); caching layers; etcd watch count limits |
| F-14 | Cross-cell state-sync bandwidth exhaustion | Large CRDT bulk sync; image layer replication; log flood | Minutes (bandwidth metrics plateau) | Stale cross-cell metadata; outdated service endpoints | Backpressure: prioritize delta over full sync; bandwidth quotas; sync cancellation | Bandwidth quotas per cell pair; prioritized sync queues; compression (zstd) |
| F-15 | Misconfiguration propagation | Invalid CRD applied via GitOps; webhook miss; canary skip | Hours to days (drift detection alert) | Federation-wide policy violation; security posture degradation | Config rollback via Git revert; automated canary validation gates | Validation webhooks on all CRDs; OPA/Gatekeeper policy enforcement; canary deployment (5% → 25% → 100%) |

*Table 7.3: FMEA — 15 Failure Modes for Federated Multi-Cluster Systems with detection, blast radius, recovery, and prevention.*

### 7.3.1 Failure Mode Interactions and Cascading Analysis

The fifteen modes above do not exist in isolation. The most dangerous incidents involve **interacting failures** that defeat individual mitigations. Three compound scenarios are explicitly modeled:

**Scenario A: F-02 (cell partition) + F-07 (clock skew).** A partitioned cell with drifting clocks may accept writes that appear causally inconsistent on heal. HelixCluster's hybrid logical clocks (HLC) combine a 48-bit physical component with a 16-bit logical counter, preserving causality even during clock skew.

**Scenario B: F-06 (gossip saturation) + F-08 (cascading overload).** Saturated gossip delays failure detection, which triggers unnecessary failovers that add load, further saturating gossip. The Phi Accrual detector breaks this positive feedback loop by automatically raising its suspicion threshold as gossip variance increases, reducing false positives by more than 50x.

**Scenario C: F-04 (link degradation) + F-12 (asymmetric partition).** A degraded asymmetric link may pass small health probes while failing large state-sync transfers. HelixCluster uses **variable-size health probes** that alternate between 64 B and 1 MB payloads; a mismatch between small-packet success and large-packet failure triggers an immediate asymmetric-partition alert.

### 7.3.2 Circuit Breakers and Bulkhead Isolation

F-08 (cascading failure) is the highest-severity mode because it can convert a localized overload into federation-wide outage. Every inter-cell RPC passes through a circuit breaker: **CLOSED** (normal, tracking failures over a 30-second window); **OPEN** (after 5 consecutive failures or 50% failure rate, all requests fail immediately for 30 seconds); **HALF-OPEN** (after cooldown, a single probe is allowed — success closes the breaker, failure restarts cooldown with exponential backoff up to 5 minutes).

Bulkhead isolation complements the circuit breaker by dedicating separate connection pools, goroutine pools, and retry budgets per target cell. Each cell-to-cell channel receives: max 100 concurrent connections, max 1,000 queued requests, 50 dedicated goroutine workers, and 10 retries per second.

---

## 7.4 Monitoring & Observability

Testing and chaos engineering generate failures. Observability turns those failures into understanding. A federated system without cross-cell telemetry is debugged via speculation; with it, mean-time-to-resolution (MTTR) drops from hours to minutes.

### 7.4.1 Prometheus Federation: Aggregate Metrics from All Cells

HelixCluster deploys a **hierarchical Prometheus federation** architecture. Each cell runs its own Prometheus instance (scraping local targets every 15 seconds), and a central Prometheus aggregates pre-computed metrics from all cell instances via their `/federate` endpoints every 60 seconds.

```yaml
# /etc/prometheus/central-prometheus.yaml — Central federation server
scrape_configs:
  - job_name: 'federate-cell-us-east'
    scrape_interval: 60s
    honor_labels: true
    metrics_path: '/federate'
    params:
      'match[]':
        - '{__name__=~"cell:.*"}'
        - '{__name__=~"federation:.*"}'
        - '{__name__=~"etcd:.*"}'
    static_configs:
      - targets: ['prometheus.cell-us-east.helix.local:9090']
        labels:
          cell: 'us-east'
          region: 'us-east-1'

  - job_name: 'federate-cell-eu-west'
    scrape_interval: 60s
    honor_labels: true
    metrics_path: '/federate'
    params:
      'match[]':
        - '{__name__=~"cell:.*"}'
        - '{__name__=~"federation:.*"}'
        - '{__name__=~"etcd:.*"}'
    static_configs:
      - targets: ['prometheus.cell-eu-west.helix.local:9090']
        labels:
          cell: 'eu-west'
          region: 'eu-west-1'

# Recording rules: pre-aggregate at the cell level
rule_files:
  - /etc/prometheus/rules/cell_aggregation.rules
```

The recording rules at each cell pre-aggregate high-cardinality metrics into low-cardinality federation-safe series:

```yaml
# /etc/prometheus/rules/cell_aggregation.rules
groups:
  - name: cell_aggregation
    interval: 60s
    rules:
      - record: cell:node_cpu_utilization:avg5m
        expr: avg(rate(node_cpu_seconds_total{mode!="idle"}[5m]))

      - record: cell:apiserver_request_duration_seconds:p99
        expr: histogram_quantile(0.99,
              rate(apiserver_request_duration_seconds_bucket[5m]))

      - record: federation:cross_cell_request_duration_seconds:p99
        expr: histogram_quantile(0.99,
              rate(federation_request_duration_seconds_bucket[5m]))

      - record: federation:gossip_convergence_seconds
        expr: max(serf_gossip_rtt_seconds)

      - record: etcd:server_has_leader
        expr: max(etcd_server_has_leader)
```

Best practices for federation:

- **Pre-aggregate aggressively**: Only federate recording-rule outputs, not raw counters.
- **Honor source labels**: `honor_labels: true` preserves the original cell and region labels from the source Prometheus.
- **External labels**: Each cell Prometheus adds `external_labels` identifying its cell and region, preventing series collision.
- **Hierarchical scaling**: For federations exceeding 100 cells, insert a regional aggregation tier between cell and global Prometheus to prevent the central instance from being overwhelmed.

### 7.4.2 OpenTelemetry Cross-Cell Tracing

Metrics tell you that something is wrong. Traces tell you why. HelixCluster uses OpenTelemetry with a tiered collector architecture:

```
                    +-----------------------------+
                    |   Central Tempo / Jaeger    |
                    |   (Trace storage + query)   |
                    +--------------+--------------+
                                   |
                    +--------------v--------------+
                    |  Federation Gateway OTLP    |
                    |  (TLS, auth, routing)       |
                    +--------------+--------------+
                                   |
          +------------------------+------------------------+
          |                        |                        |
+---------v---------+    +---------v---------+    +---------v---------+
| Cell OTLP Collector|    | Cell OTLP Collector|    | Cell OTLP Collector|
| (batch, process,   |    | (batch, process,   |    | (batch, process,   |
|  enrich with cell) |    |  enrich with cell) |    |  enrich with cell) |
+---------+---------+    +---------+---------+    +---------+---------+
          |                        |                        |
+---------v---------+    +---------v---------+    +---------v---------+
| Node OTLP Agent    |    | Node OTLP Agent    |    | Node OTLP Agent    |
| (receive, forward) |    | (receive, forward) |    | (receive, forward) |
+-------------------+    +-------------------+    +-------------------+
```

*Figure 7.1: Tiered OpenTelemetry Architecture — node agents forward to cell collectors, which batch and forward through a federation gateway to central trace storage.*

Key configuration:

```yaml
# /etc/otel/collector-cell.yaml — Cell-level OpenTelemetry Collector
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
  resource/cell:
    attributes:
      - key: cell.id
        value: ${CELL_ID}
        action: upsert
      - key: cell.region
        value: ${CELL_REGION}
        action: upsert
      - key: service.namespace
        value: "helix-federation"
        action: upsert

exporters:
  otlp/gateway:
    endpoint: otel-gateway.helix.local:4317
    tls:
      cert_file: /etc/otel/certs/client.crt
      key_file: /etc/otel/certs/client.key
      ca_file: /etc/otel/certs/ca.crt
    headers:
      x-scope-orgid: ${CELL_ID}

service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [batch, resource/cell]
      exporters: [otlp/gateway]
```

Cross-cell traces propagate via **W3C Trace Context** headers (`traceparent`, `tracestate`). When a service in cell A calls a service in cell B, the outgoing request carries the trace context. The cell B ingress collector extracts it and continues the same trace. The `cell.id` resource attribute on each span allows trace queries to distinguish which cell executed each operation.

### 7.4.3 Split-Brain Detection Alerts

The most critical alerts in a federated system are those that detect consensus divergence. The following PromQL alert rules are evaluated every 15 seconds by the central Prometheus:

```yaml
# /etc/prometheus/alerts/federation-critical.rules
groups:
  - name: federation-critical
    rules:
      - alert: SplitBrainDetected
        expr: |
          sum by (cell) (etcd_server_is_leader) > 1
        for: 30s
        labels:
          severity: critical
          team: federation-sre
        annotations:
          summary: "Split-brain detected in cell {{ $labels.cell }}"
          description: "Multiple etcd leaders detected for cell {{ $labels.cell }}. Immediate manual intervention required."
          runbook_url: "https://runbooks.helix.dev/federation/split-brain"

      - alert: FederationEtcdQuorumLost
        expr: |
          etcd_server_has_leader == 0
        for: 15s
        labels:
          severity: critical
          team: federation-sre
        annotations:
          summary: "etcd quorum lost in cell {{ $labels.cell }}"

      - alert: GossipConvergenceSlow
        expr: |
          histogram_quantile(0.99,
            serf_gossip_rtt_seconds{scope="inter_cell"}) > 5
        for: 2m
        labels:
          severity: warning
        annotations:
          summary: "Inter-cell gossip P99 RTT > 5s in {{ $labels.cell }}"

      - alert: CrossCellCircuitBreakerOpen
        expr: |
          federation_circuit_breaker_state == 2
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Circuit breaker open for {{ $labels.target_cell }}"

      - alert: CRDTDivergenceDetected
        expr: |
          federation_state_hash_mismatch_total > 0
        for: 1m
        labels:
          severity: critical
        annotations:
          summary: "CRDT divergence detected between cells"

      - alert: FederationHighLatency
        expr: |
          histogram_quantile(0.99,
            federation_request_duration_seconds) > 1
        for: 3m
        labels:
          severity: warning
        annotations:
          summary: "Cross-cell request P99 latency > 1s"
```

### 7.4.4 Grafana Dashboards

The central Grafana instance provides four primary dashboards for federation health:

| Dashboard | Purpose | Key Panels | Refresh |
|-----------|---------|------------|---------|
| **Global Health** | Federation-wide status at a glance | Cell status grid (up/down), total node count, alert summary, federation join/leave events | 10 s |
| **Cell-to-Cell Latency** | Heatmap of inter-cell RTT | Matrix heatmap (source cell × target cell), P50/P99 latency lines per cell pair, packet loss overlay | 30 s |
| **Gossip Convergence** | Epidemic propagation health | Rounds-to-convergence histogram, fanout effectiveness, inter-cell bandwidth per gateway, suspicion rate | 15 s |
| **Consensus Integrity** | etcd and CRDT correctness | Leader count per cell (must be 1), Raft commit index lag, CRDT Merkle tree comparison status, state sync queue depth | 10 s |

*Table 7.4: Grafana Dashboard Reference — four primary dashboards for federated cluster observability.*

The Global Health dashboard uses a **cell status grid** — a visual matrix where each cell is a colored square (green = healthy, yellow = degraded, red = critical, gray = partitioned). This single-panel view gives an SRE immediate situational awareness across up to 255 cells. Clicking any cell drills down to the cell-local Prometheus and Grafana instance for detailed debugging.

The Gossip Convergence dashboard is particularly important because gossip failures are subtle: a cell may appear healthy (its API server responds, its etcd has a leader) yet be propagating stale metadata. The dashboard tracks the time from a membership change event to its arrival at all other cells — empirically observed at O(log C) rounds — and alerts when convergence exceeds 2 × the theoretical bound.

---

## 7.5 Summary: The Validation Stack

HelixCluster's testing strategy operates at four layers, each catching defects that the layer above or below cannot:

| Layer | Tool / Method | Coverage | Execution |
|-------|--------------|----------|-----------|
| Protocol Unit | Turmoil (DST) | Gossip, consensus, CRDT merge correctness | Every PR; seconds to minutes |
| Integration | Shadow + tc/netem | Real binaries on simulated WAN topologies | Nightly; 45 min for 100 cells |
| System | Chaos Mesh + RemoteCluster | Full Kubernetes clusters with injected faults | Continuous (staging); weekly (prod-scoped) |
| Organizational | Game Days | Human response procedures, cross-team coordination | Quarterly; 4 scenarios per year |

The combination of deterministic simulation, chaos engineering, FMEA, and comprehensive observability creates a validation stack stronger than any single layer. The goal is not to prevent all failures — that is impossible — but to ensure that every plausible failure mode has been encountered, characterized, and either mitigated or documented with a runbook before it reaches production traffic.
