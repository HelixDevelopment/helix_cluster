# 5. Security Architecture

Federated multi-cluster systems face a fundamental security dilemma: each additional cell expands the attack surface, yet the federation's value depends on seamless cross-cell communication. HelixCluster Phase 6 resolves this tension through a defense-in-depth architecture built on Zero Trust principles, cryptographic workload identity, and layered encryption. Every component assumes breach — each cell operates as an independent trust boundary, every workload carries a cryptographically verifiable identity, and all inter-cell traffic traverses encrypted tunnels with explicit authorization. This chapter specifies the security model, identity infrastructure, encryption stack, policy enforcement framework, and threat analysis that together ensure a compromised cell cannot cascade into a federation-wide breach.

## 5.1 Zero Trust Model

### 5.1.1 NIST SP 800-207 Tenets Applied to Federated Clusters

NIST Special Publication 800-207 defines Zero Trust Architecture (ZTA) through seven foundational tenets: all data sources and computing services are treated as resources; all communications are secured regardless of network location; access is granted per-session with least privilege; policy determination is dynamic and informed by identity assurance and behavioral signals; asset integrity and security posture are continuously monitored; authentication and authorization are dynamically enforced; and telemetry is collected to improve security posture over time. HelixCluster applies each tenet across the federation scope.

The first tenet — all data sources and computing services are resources — extends BeyondCorp and BeyondProd models to the cell level. In Google's BeyondCorp, security shifts from network perimeter to individual users and devices. BeyondProd extends this to service-to-service communication through code provenance verification and workload isolation. In HelixCluster, every cell, node, pod, and service endpoint constitutes a resource subject to independent authorization. No cell receives implicit trust because it belongs to the federation.

The second and third tenets — secure all communications and grant per-session least-privilege access — manifest in the default-deny posture described in Section 5.1.3 and the encryption stack detailed in Section 5.3. Every cross-cell packet traverses WireGuard kernel encryption and mTLS-encrypted service mesh links. Session lifetimes are bounded by one-hour SVID certificates with automatic rotation at 50% TTL, ensuring that stolen credentials expire within minutes rather than months.

Dynamic policy determination, continuous monitoring, and dynamic enforcement use Cilium's eBPF-based identity-aware policies combined with OPA/Gatekeeper admission control. Cilium assigns each pod a cryptographically derived identity based on labels, enabling policies that survive pod rescheduling and cross-cluster migration. OPA evaluates every API server admission request against Rego policies distributed through GitOps, ensuring that policy changes are version-controlled, auditable, and consistently applied across all cells.

### 5.1.2 Trust Boundaries: Cell Boundary, Node Boundary, Workload Boundary

HelixCluster defines three concentric trust boundaries, each with distinct enforcement mechanisms and blast radius containment properties.

The **cell boundary** is the outermost security perimeter. Each cell maintains an independent control plane — etcd, API server, scheduler — and operates within its own SPIFFE trust domain (`spiffe://cell-name.helixcluster.local`). Cross-cell communication traverses WireGuard gateway tunnels with mutual attestation via SPIRE federation. A compromised cell cannot forge identities for other cells because root CAs are cryptographically isolated. Cell-level trust boundaries are enforced by Cilium ClusterMesh identity propagation, which only permits cross-cluster traffic between explicitly authorized service identities.

The **node boundary** protects against lateral movement within a cell. Cilium's eBPF-based host firewall restricts node-to-node traffic to explicitly allowed ports and protocols. The Kubelet API, container runtime socket, and SPIRE Agent Unix domain socket are accessible only from localhost or designated control plane nodes. Node compromise does not automatically grant pod-level access because each pod receives its own SPIFFE identity through the SPIRE Workload API, and network policies enforce identity-based segmentation independent of the underlying node.

The **workload boundary** is the innermost trust layer. Every pod receives a unique SPIFFE ID (e.g., `spiffe://us-east.helixcluster.local/ns/payments/sa/payment-service`) and corresponding X.509-SVID. mTLS between services validates both peer identities through SPIFFE ID checking in the SAN URI field. Cilium L7 policies enforce HTTP path, method, and header-level restrictions, creating microsegments around individual workloads. A compromised pod in the payment service cannot communicate with the database pod unless explicitly authorized by both SPIFFE identity and network policy rules.

### 5.1.3 Default-Deny: All Inter-Cell Traffic Blocked Unless Explicitly Allowed

The default-deny posture is the non-negotiable foundation of HelixCluster's security model. Every inter-cell network connection is denied unless it satisfies three independent authorization checks: network policy allow rules, SPIFFE identity verification, and mTLS certificate validation.

At the network layer, Cilium ClusterMesh deploys a global default-deny policy across all federated cells. CiliumNetworkPolicy resources use identity-based selectors rather than IP addresses, ensuring policies remain valid as pods reschedule across nodes and clusters. The following policy exemplifies the allowlist approach:

```yaml
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata:
  name: allow-frontend-to-payment
  namespace: payments
spec:
  endpointSelector:
    matchLabels:
      app: payment-processor
  ingress:
  - fromEndpoints:
    - matchLabels:
        app: web-frontend
        io.kubernetes.pod.namespace: web
    toPorts:
    - ports:
      - port: "8080"
        protocol: TCP
      rules:
        http:
        - method: POST
          path: /api/v1/payments
```

This policy allows only pods labeled `app: web-frontend` in namespace `web` to reach the payment processor on port 8080 with HTTP POST to `/api/v1/payments`. All other traffic — including traffic from pods in the same namespace or cell — is silently dropped at the eBPF layer before reaching the pod. Cross-cluster enforcement requires the `io.cilium.k8s.policy.cluster` label for cluster-scoped selectors, reflecting Cilium v1.19's changed default behavior.

**Table 5.1: Security Tier Matrix**

| Capability | Tier 1 (Basic) | Tier 2 (Standard) | Tier 3 (Enterprise) |
|---|---|---|---|
| **Workload Identity** | Kubernetes SA tokens | SPIFFE/SPIRE per cell | SPIRE federation + HSM-backed CA |
| **Node Encryption** | None | WireGuard cross-cell only | WireGuard all links + IPsec fallback |
| **Service mTLS** | Ingress TLS only | Linkerd mesh (33% overhead) | Full mesh with L7 authorization |
| **Network Policy** | Default-deny namespace | Cilium L3-L7 + ClusterMesh | Calico Enterprise tiers + federation |
| **Secret Management** | Kubernetes Secrets | External Secrets Operator | Vault Enterprise + auto-rotation |
| **Policy Enforcement** | Pod Security Standards | OPA/Gatekeeper admission | Cross-cluster GitOps policies |
| **Audit & Compliance** | Kubernetes audit logs | Centralized Loki + Falco | Immutable signed trails |
| **Post-Quantum Ready** | Cryptographic inventory | Hybrid cert deployment | Full PQC migration |
| **Monthly Cost** | $500–$2K | $5K–$20K | $50K–$200K |

Organizations should select tiers based on data sensitivity and compliance requirements. Tier 2 provides the recommended baseline for production federations, offering SPIFFE identity, WireGuard encryption, Linkerd mTLS, and OPA policy enforcement at manageable cost. Tier 3 adds HSM-backed certificate authorities, Calico Enterprise policy tiers, immutable audit trails, and post-quantum cryptography for regulated industries.

## 5.2 SPIFFE/SPIRE Cross-Cluster Identity

### 5.2.1 Per-Cell Trust Domain

HelixCluster assigns each cell an independent SPIFFE trust domain formatted as `spiffe://<cell-name>.helixcluster.local`. The trust domain is the trust anchor for all workload identities within that cell — the SPIRE Server in each cell acts as its own Certificate Authority, issuing X.509-SVIDs and JWT-SVIDs to local workloads. This separation is architecturally mandatory: shared root CAs across cells would allow a compromised cell to issue valid certificates for any workload in the federation, collapsing the entire security model.

Each SVID contains the SPIFFE ID in the Subject Alternative Name URI field (`URI:spiffe://us-east.helixcluster.local/ns/payments/sa/payment-service`), enabling universal service identification regardless of pod IP, node location, or cluster membership. SVIDs are short-lived — default one-hour TTL — with automatic rotation triggered at 50% of lifetime. Private keys are generated on-host by the SPIRE Agent and never transmitted over the network. The Workload API requires no bootstrap secrets, eliminating a common attack vector where compromised tokens cascade into broader access.

### 5.2.2 Nested SPIRE: Cell SPIRE Server Federates with Global SPIRE Server

At scale, a flat SPIRE topology — where every cell operates an independent root CA with pairwise federation relationships — creates O(n²) federation connections and administrative burden. Nested SPIRE solves this through a hierarchical trust architecture:

```
+------------------------------------------------------------------+
|                    NESTED SPIRE TOPOLOGY                          |
|                                                                  |
|  Tier 0: Global Root SPIRE Servers (3-5 nodes, HA)              |
|  +-- Trust Domain: spiffe://global.helixcluster.local            |
|  +-- Root CA; signs intermediate CAs for downstream servers      |
|  +-- PostgreSQL datastore with read replicas                     |
|                                                                  |
|           |              |              |                        |
|           v              v              v                        |
|                                                                  |
|  Tier 1: Cell SPIRE Servers (per-cell downstream)               |
|  +-- Trust Domain: spiffe://us-east.helixcluster.local           |
|  +-- Trust Domain: spiffe://eu-west.helixcluster.local           |
|  +-- Trust Domain: spiffe://ap-south.helixcluster.local          |
|  +-- Intermediate CA from global root; issues leaf SVIDs         |
|  +-- Continues operation if global root is offline               |
|                                                                  |
|           |              |              |                        |
|           v              v              v                        |
|                                                                  |
|  Tier 2: SPIRE Agents (DaemonSet on every node)                 |
|  +-- Workload attestation via Kubernetes PSAT                    |
|  +-- SVID delivery via Unix domain socket                        |
|  +-- Automatic rotation at 50% TTL with jitter                   |
|                                                                  |
|  Tier 3: Workloads                                              |
|  +-- Receive SVIDs through SPIFFE CSI Driver or Envoy SDS        |
|  +-- Present SVIDs for mTLS authentication                       |
|  +-- Validate peer SVIDs against trust bundles                   |
+------------------------------------------------------------------+
```

The global root SPIRE Servers hold the federation's root CA keys, protected by hardware security modules (HSMs) in Tier 3 deployments. Each cell operates downstream SPIRE Servers that obtain intermediate CA certificates from the global root. These downstream servers continue issuing and rotating SVIDs even during global root unavailability — a critical availability property for geographically distributed cells. Federation between cells uses SPIFFE Federation Protocol (OIDC-based bundle endpoints), where cell SPIRE Servers fetch, cache, and automatically rotate each other's trust bundles. A workload in `us-east` validates a peer SVID from `eu-west` against the cached `eu-west` trust bundle without requiring real-time cross-cell communication.

**Table 5.2: SPIRE Sizing for Federation Scale**

| Workloads | Agents | Server Units | CPU/RAM per Server | Datastore |
|---|---|---|---|---|
| 100 | 10 | 2 | 2 cores, 2 GB | PostgreSQL 1 node |
| 1,000 | 100 | 4 | 4 cores, 8 GB | PostgreSQL HA (2 nodes) |
| 10,000 | 5,000 | 8 | 16 cores, 16 GB | PostgreSQL HA + read replica |
| 100,000 | 50,000 | 16+ | 16–32 cores, 16–32 GB | PostgreSQL HA + PgBouncer |

For a 50-cell federation with 2,000 workloads per cell (100,000 total), the nested topology deploys 16 global root server units and approximately 4–8 downstream server units per cell. PostgreSQL datastore performance is the critical bottleneck — connection pooling via PgBouncer and read replicas for bundle endpoint queries are mandatory at this scale.

### 5.2.3 SVID Propagation and Production Validation

SVID propagation follows a pull model: workloads request SVIDs through the SPIFFE Workload API, and SPIRE Agents stream updated certificates as rotations occur. For cross-cluster service mesh, Linkerd's identity integration validates SPIFFE IDs from federated trust domains during mTLS handshake, enabling automatic authentication without manual CA distribution.

Production deployments at Netflix demonstrate 100,000+ workloads under nested SPIRE management with a 60% reduction in security incidents through consistent identity-based access control. Uber's multi-region microservices deployment simplified service onboarding by replacing manual certificate provisioning with automatic SPIFFE identity issuance. GitHub's developer platform improved DevOps-security collaboration through self-service workload registration. Deutsche Bank's financial services deployment achieved a 40% reduction in identity-related incidents through short-lived certificates and automatic rotation. These independently validated production deployments confirm that SPIRE's nested topology scales to HelixCluster's target federation sizes.

## 5.3 Encryption Stack

### 5.3.1 Layer 3: WireGuard Kernel Encryption

HelixCluster encrypts all node-to-node traffic using WireGuard in kernel mode. WireGuard's design prioritizes simplicity and performance: the codebase is approximately 4,000 lines versus OpenVPN's 100,000+ lines, reducing the attack surface for vulnerability discovery. Kernel-mode WireGuard adds only 3–5% CPU overhead at 10 Gbps throughput, with single-stream performance of ~8.0 Gbps and 8-stream aggregate of ~9.4 Gbps.

WireGuard operates transparently below the CNI layer. Cilium enables WireGuard encryption with a single Helm value (`encryption.enabled: true`, `encryption.type: wireguard`), automatically generating and distributing per-node WireGuard keys. Pod IPs are encrypted on the originating node and decrypted on the destination node — applications require no modification. Cilium's implementation uses the `ipv6` pod CIDR range to carry encryption keys in packet headers, avoiding the need for a separate key distribution service.

Key rotation leverages SPIRE integration: WireGuard node keys are derived from node SVIDs, enabling automatic rotation when SPIRE rotates node certificates. For headless nodes where SPIRE integration is not yet available, Cilium's WireGuard implementation supports automatic key generation with configurable rotation intervals (default 24 hours).

### 5.3.2 Layer 7: mTLS Service Mesh

While WireGuard encrypts node-to-node traffic, it does not authenticate individual services. A compromised node could inject traffic from any pod IP because WireGuard validates only node-level keys. Layer 7 mTLS closes this gap by providing per-service identity verification and authorization.

**Table 5.3: Service Mesh mTLS Overhead Comparison**

| Mesh | P99 Latency Increase | CPU Overhead | Memory per Proxy | Architecture | Best For |
|---|---|---|---|---|---|
| Istio (sidecar) | 166% | Highest | ~150 MB | Envoy sidecar per pod | Advanced L7 traffic management |
| Cilium (WireGuard) | 99% | Medium | Node-level | eBPF + kernel crypto | Cluster-wide node encryption |
| Cilium (IPsec) | 144% | Medium-High | Node-level | eBPF + IPsec | FIPS 140-2 compliance |
| **Linkerd** | **33%** | **Lowest** | **~50 MB** | **Rust micro-proxy** | **Latency-sensitive mTLS** |
| Istio Ambient | 8% | Low | Node-level (ztunnel) | eBPF + sidecarless | Feature-rich sidecarless |

The academic benchmark data reveals that pure mTLS protocol overhead is only 1–3% latency — the remaining overhead comes from proxy processing, HTTP parsing, policy evaluation, and metrics collection. Linkerd's Rust-based micro-proxy is approximately 5x more efficient than Istio's Envoy sidecar for mTLS-only use cases because it avoids full HTTP parsing and maintains optimized connection pooling. For HelixCluster's latency-sensitive cross-cell paths, Linkerd provides the optimal balance of low overhead and production maturity.

Linkerd's automatic mTLS enables zero-configuration encryption: all TCP traffic between meshed services is encrypted and authenticated without application changes or annotation-based opt-in. Certificate rotation occurs every 24 hours through Linkerd's internal identity component (backed by SPIFFE identities in the HelixCluster deployment), with graceful TLS session transitions that cause zero dropped connections.

### 5.3.3 Double Encryption Rationale: Defense in Depth

HelixCluster's dual-layer encryption — WireGuard at L3 plus mTLS at L7 — may appear redundant, but each layer addresses distinct threats that the other cannot.

WireGuard without mTLS fails against node compromise: an attacker who gains root access to a node can forge traffic from any pod IP on that node because WireGuard authenticates only the node, not individual workloads. mTLS without WireGuard fails against network-level attacks: an attacker who captures inter-cell packets could analyze traffic patterns, timing, and volumes even if they cannot decrypt application payload — metadata leakage that mTLS alone does not prevent because it operates above the network layer.

Together, the layers provide defense in depth. WireGuard encrypts all inter-cell traffic at the network layer, preventing metadata analysis, traffic fingerprinting, and denial-of-service attacks based on packet inspection. mTLS authenticates individual service identities, preventing lateral movement from compromised nodes and enabling per-service authorization policies. If an attacker compromises a WireGuard node key, they gain encrypted tunnel access but still cannot forge service identities because each SVID requires pod-level attestation through SPIRE. If an attacker compromises a service certificate, they gain access only to explicitly authorized peers and remain confined within the WireGuard-encrypted network perimeter.

The combined overhead is additive but manageable: WireGuard adds 3–5% CPU and negligible latency for kernel-mode operation, while Linkerd adds 33% P99 latency at the application layer. For most workloads, the security benefit of defense in depth outweighs the cumulative overhead. Latency-critical paths may disable WireGuard within a trusted cell while retaining it for cross-cell links, reducing overhead to Linkerd's 33% for intra-cell traffic.

## 5.4 OPA Policy Enforcement

### 5.4.1 Cross-Cluster Policies, Rego Examples, and GitOps Distribution

Open Policy Agent (OPA) with Gatekeeper provides admission-time policy enforcement across the HelixCluster federation. Gatekeeper operates as a Kubernetes Validating Admission Webhook, evaluating every API server request against Rego policies before resource persistence. This catch-at-the-gate model prevents policy violations from ever reaching the cluster, unlike runtime enforcement which detects violations after deployment.

HelixCluster distributes policies through GitOps: OPA ConstraintTemplates and Constraints are stored in a central Git repository, and ArgoCD ApplicationSets sync them to all federated cells. This approach ensures policy consistency, version control, and auditability. Changes follow the standard pull-request workflow with mandatory security team review, automated Rego unit testing via `conftest`, and staged rollout through dev/staging/production cell tiers.

The following Rego policies exemplify HelixCluster's security-critical enforcement:

**Policy 1: Require SPIFFE-Compatible Service Account Names**
```rego
package helixcluster.spiffe.enforce

violation[{"msg": msg}] {
  input.review.object.kind == "Pod"
  sa := input.review.object.spec.serviceAccountName
  not startswith(sa, "spiffe-")
  msg := sprintf("Pod %s/%s: serviceAccountName must use spiffe- prefix, got: %s", [
    input.review.object.metadata.namespace,
    input.review.object.metadata.name,
    sa
  ])
}
```

**Policy 2: Prevent Privileged Containers in Non-System Namespaces**
```rego
package helixcluster.security.noprivileged

violation[{"msg": msg}] {
  input.review.object.kind == "Pod"
  input.review.object.metadata.namespace != "kube-system"
  container := input.review.object.spec.containers[_]
  container.securityContext.privileged == true
  msg := sprintf("Privileged container %s in namespace %s violates security policy", [
    container.name,
    input.review.object.metadata.namespace
  ])
}
```

**Policy 3: Require Network Policy Attachment for Cross-Namespace Traffic**
```rego
package helixcluster.network.requiredpolicy

violation[{"msg": msg}] {
  input.review.object.kind == "Pod"
  namespace := input.review.object.metadata.namespace
  not data.inventory.namespace[namespace]["networking.k8s.io/v1"].NetworkPolicy
  msg := sprintf("Namespace %s has no NetworkPolicy; cross-namespace traffic denied", [namespace])
}
```

**Policy 4: Enforce Resource Limits to Prevent DoS via Federation Sync**
```rego
package helixcluster.resources.limits

violation[{"msg": msg}] {
  input.review.object.kind == "Pod"
  container := input.review.object.spec.containers[_]
  not container.resources.limits.memory
  msg := sprintf("Container %s missing memory limits; unbounded pods risk federation sync DoS", [container.name])
}
```

**Table 5.4: Cross-Cluster Policy Distribution Approaches**

| Approach | Mechanism | Maturity | Latency | Consistency Guarantee |
|---|---|---|---|---|
| GitOps sync (ArgoCD) | Store policies in Git; ArgoCD syncs to all cells | Production-ready | 1–3 min sync | Eventual (Git as source of truth) |
| Fleet + Policy Controller | Rancher Fleet with centralized policy management | Production-ready | 30–60 sec | Eventual with status reporting |
| OPA at federation layer | OPA sidecar on federation API server | Custom development | Real-time | Strong (single evaluation point) |
| KubeStellar | Multi-cluster dashboard with native Gatekeeper | Emerging (alpha) | 1–2 min | Eventual |

GitOps sync is the recommended approach for HelixCluster because it leverages existing ArgoCD infrastructure, provides full audit trails through Git history, and supports progressive delivery through cell-tier promotion. Fleet offers faster sync cycles with built-in status reporting but requires Rancher integration. The federation-layer OPA approach provides the strongest consistency but introduces a single point of failure and requires custom development.

Best practices for federated policy enforcement include: run constraints in `dry-run` mode for 48 hours before enforcement to understand blast radius; exclude `kube-system` and SPIRE namespaces from non-essential policies to prevent control plane lockout; namespace-scope policies preferentially over cluster-scope to limit blast radius; version-control all policies with mandatory PR review; and export policy violation metrics to Prometheus for alerting and compliance dashboards.

## 5.5 Threat Model

### 5.5.1 Attack Surfaces, Blast Radius Containment, and Lateral Movement Prevention

```
+==================================================================+
|              HELIXCLUSTER THREAT MODEL OVERVIEW                   |
|                                                                  |
|  EXTERNAL ATTACKERS                                              |
|  +-- Compromised CI/CD pipeline → poisoned container images      |
|  +-- Stolen kubeconfig / admin credentials                       |
|  +-- Vulnerable public-facing workload → container breakout      |
|  +-- Supply chain attack → malicious dependency                  |
|                                                                  |
|           |                                                      |
|           v                                                      |
|  +-----------------------------------------------------------+  |
|  |                    CELL COMPROMISE                         |  |
|  |  Affected cell: us-east.helixcluster.local                 |  |
|  |  Blast radius: CONTAINED to affected trust domain          |  |
|  |                                                             |  |
|  |  Trust boundary enforced by:                                |  |
|  |  +-- Separate SPIFFE trust domain (cannot forge others)    |  |
|  |  +-- WireGuard node keys (tunnel access only, no svc ids)  |  |
|  |  +-- Default-deny network policies (no lateral paths)      |  |
|  |  +-- OPA admission control (prevents privilege escalation) |  |
|  +-----------------------------------------------------------+  |
|                                                                  |
|           |                                                      |
|           |  Federation trust bundle REVOKED                     |
|           |  Network policies BLOCK compromised cluster          |
|           v                                                      |
|                                                                  |
|  +-----------------------------------------------------------+  |
|  |              FEDERATION SURVIVES                            |  |
|  |  eu-west, ap-south cells: UNAFFECTED                       |  |
|  |  SVIDs from us-east: INVALIDATED                           |  |
|  |  Cross-cell mTLS: REJECTS us-east peers                    |  |
|  +-----------------------------------------------------------+  |
|                                                                  |
|  LATERAL MOVEMENT PATHS (blocked controls):                    |
|  [Node A] --pod-to-pod--> [Node B]      BLOCKED by L7 policy   |
|  [Pod X] --svc discovery--> [Pod Y]     BLOCKED by identity    |
|  [Cell 1] --x-cell trust--> [Cell 2]    BLOCKED by federation  |
|  [CI/CD] --unsigned image--> [Registry] BLOCKED by Cosign+OPA  |
+==================================================================+
```

HelixCluster's threat model identifies three primary attack surfaces: per-cell attack surfaces common to all Kubernetes clusters, inter-cell attack surfaces unique to federation, and supply chain attack surfaces spanning CI/CD and container registries.

Per-cell attack surfaces include compromised nodes leading to lateral movement via pod-to-pod network access, malicious container images from supply chain compromise, overprivileged RBAC enabling privilege escalation, stolen kubeconfig files granting full cluster access, vulnerable workloads permitting container breakout, and exposed API servers enabling unauthorized access. Cilium's eBPF-based identity-aware network policies are the primary control against lateral movement, preventing 80%+ of attack paths through default-deny segmentation. Restricting `pods/exec`, `pods/log`, and `pods/portforward` permissions on sensitive namespaces blocks common post-compromise reconnaissance paths.

Federation-specific attack surfaces require additional controls. A compromised cluster could attempt to poison the federation by issuing rogue certificates — prevented by separate trust domains per cell with SPIFFE federation. Cross-cluster lateral movement via service mesh is blocked by mTLS identity verification that rejects unknown trust domains. Privilege escalation through federation RBAC is mitigated by OPA guardrails enforcing least-privilege role bindings. Data exfiltration via cross-cluster DNS tunneling is detected by Cilium's DNS-aware egress policies and Hubble network flow monitoring. Denial of service through federation sync overhead is prevented by API Priority and Fairness rate limiting and dedicated federation node pools.

Blast radius containment is proportional to trust domain isolation. With separate trust domains and SPIFFE federation, a compromised cell cannot forge SVIDs for other cells — cryptographic isolation limits exposure to the affected trust domain. SVID maximum TTL of one hour caps the exposure window: even if an attacker extracts valid certificates, they expire within 60 minutes. Federated trust bundles can be revoked immediately by removing the federation relationship, causing all peer cells to reject SVIDs from the compromised domain within seconds of cache invalidation.

### 5.5.2 FMEA: 15 Failure Modes for Federated Security

Failure Mode and Effects Analysis (FMEA) systematically evaluates potential security failures, their causes, effects, detection methods, and mitigations. The Risk Priority Number (RPN) is calculated as Severity (1–10) x Occurrence (1–10) x Detection (1–10), with higher values indicating greater risk.

**Table 5.5: Security FMEA — 15 Failure Modes**

| ID | Failure Mode | Cause | Effect | Severity | Occurrence | Detection | RPN | Mitigation |
|---|---|---|---|---|---|---|---|---|
| F01 | Global SPIRE root CA private key compromise | HSM bypass or insider threat | Attacker can issue valid SVIDs for entire federation | 10 | 2 | 3 | 60 | HSM with M-of-N activation; air-gapped offline root; mandatory 4-eyes principle for CA operations |
| F02 | Cell SPIRE downstream server compromise | Vulnerability exploitation or credential theft | Attacker issues SVIDs within cell's trust domain only | 8 | 3 | 4 | 96 | Separate trust domains limit blast radius; 1-hour SVID TTL; automated anomaly detection on SVID issuance rates |
| F03 | WireGuard node key compromise | Memory extraction from compromised node | Decryption of node-to-node traffic; metadata leakage | 7 | 4 | 5 | 140 | 24-hour key rotation; SPIRE-derived keys; node-level Falco alerting on memory access patterns |
| F04 | mTLS service certificate theft | Sidecar vulnerability or pod escape | Impersonation of legitimate service identity | 8 | 4 | 4 | 128 | 1-hour SVID TTL; automatic rotation at 50%; SPIFFE ID validation rejects stolen cert reuse |
| F05 | Compromised cluster joins federation | Stolen join tokens or credential reuse | Rogue cell receives federation trust and access | 9 | 3 | 5 | 135 | Admission control with OPA verification; mutual attestation required; manual approval for new cells |
| F06 | OPA policy bypass via webhook failure | Network partition or admission controller crash | Policy-violating resources admitted unchecked | 7 | 4 | 6 | 168 | Webhook failure policy set to `Fail`; redundant Gatekeeper replicas; health-check monitoring |
| F07 | Federation trust bundle poisoning | MitM during bundle endpoint sync | Acceptance of attacker-controlled CA | 9 | 2 | 4 | 72 | TLS + mutual auth on bundle endpoints; bundle signature verification; out-of-band hash confirmation |
| F08 | Privilege escalation via RBAC misconfiguration | Overprivileged ClusterRoleBindings | Compromised service account gains cluster-admin | 8 | 5 | 6 | 240 | OPA policy enforces least-privilege RBAC; regular RBAC audits; deny `*` on `*` rules |
| F09 | Lateral movement via unrestricted pod-to-pod traffic | Missing or overly permissive network policies | Compromised pod scans and exploits peers | 8 | 6 | 5 | 240 | Default-deny Cilium policies; identity-based L7 rules; Hubble flow monitoring with anomaly alerts |
| F10 | Data exfiltration via DNS tunneling | Compromised workload encodes data in DNS queries | Sensitive data leaves through allowed DNS port | 7 | 5 | 7 | 245 | Cilium DNS-aware egress policies (allow `*.stripe.com`, deny rest); DNS query length/volume monitoring |
| F11 | Supply chain attack via unsigned container image | Compromised CI/CD or registry | Malicious code executes in production pods | 9 | 5 | 6 | 270 | Cosign + Sigstore image signing; OPA admission rejects unsigned images; SBOM scanning with Trivy |
| F12 | etcd snapshot theft with secret exposure | Unencrypted backup or overly broad access | All cluster secrets including SPIRE data exposed | 9 | 3 | 5 | 135 | etcd encryption at rest (KMS); encrypted Velero backups; least-privilege backup access roles |
| F13 | Denial of service via certificate rotation storm | Synchronized rotation without jitter | API server / SPIRE overload; service degradation | 6 | 4 | 7 | 168 | Rotation jitter (0–20% of TTL); rate-limited Workload API; horizontal pod autoscaling on SPIRE servers |
| F14 | Post-quantum algorithm vulnerability | Premature PQC deployment with undiscovered weakness | Cryptographic exposure of all federation traffic | 8 | 2 | 3 | 48 | Hybrid mode (classical + PQC); algorithm agility in SPIRE; NIST-tracked migration timeline |
| F15 | Cross-cluster secret leakage via misconfigured ESO | Wrong Vault path or namespace mapping | Production secrets synced to staging / untrusted cell | 8 | 4 | 6 | 192 | OPA policy validates ExternalSecret CRD references; namespace-level ESO RBAC; secret access auditing |

The highest-risk failure modes (RPN > 200) demand immediate attention: supply chain attacks via unsigned images (RPN 270), lateral movement via missing network policies (RPN 240), privilege escalation via RBAC misconfiguration (RPN 240), and DNS tunneling data exfiltration (RPN 245). Each of these is mitigated through multiple independent controls — for example, supply chain attacks require both Cosign signing and OPA admission rejection of unsigned images, ensuring defense even if one control fails. Network policy default-deny is enforced at the eBPF layer where it cannot be bypassed by Kubernetes API manipulation, and RBAC guardrails use OPA policies that deny high-risk patterns such as wildcard permissions on wildcard resources.

Lower-RPN failures remain critical due to high severity despite low occurrence probability. Global root CA compromise (RPN 60) is extremely unlikely with proper HSM protection but would be catastrophic if realized; the 4-eyes principle and air-gapped offline root ensure no single individual can activate the CA key. Post-quantum vulnerability (RPN 48) follows NIST's conservative migration timeline with hybrid certificate deployment, allowing graceful fallback to classical algorithms if a PQC weakness is discovered.

The FMEA drives continuous improvement: detection scores above 5 indicate monitoring gaps requiring investment. Prometheus alerts on SVID issuance rates, Hubble flow anomalies, DNS query volume spikes, and OPA webhook latency provide the observability foundation for timely detection of all 15 failure modes.
