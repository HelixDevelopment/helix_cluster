# Changelog — Helix Cluster OS v0.1.0

**Release Date:** 2026-05-30

Helix Cluster OS v0.1.0 is the initial release of the distributed cluster operating system. It establishes the foundational control plane, service mesh, Web UI, and deployment tooling.

---

## Features Implemented

### Control Plane

- **helixd** — Central control plane for cluster state management, service registration, and administrative API.
- **etcd integration** — etcd 3.5+ used as the single source of truth for all cluster state.
- **Gateway** — Edge gateway with request routing, authentication, and rate limiting.

### Service Mesh (14 Services)

| Service | Status | Description |
|---------|--------|-------------|
| Build Service | ✅ | Image builds and artifact generation |
| Scheduler Service | ✅ | Job placement and resource allocation |
| Registry Service | ✅ | Container image metadata and indexing |
| Log Service | ✅ | Centralized log aggregation and querying |
| Metrics Service | ✅ | Metrics collection and Prometheus export |
| Auth Service | ✅ | Authentication, token validation, SSO support |
| Notification Service | ✅ | Alerts, webhooks, and email delivery |
| Storage Service | ✅ | Object and volume storage abstraction |
| Network Service | ✅ | Overlay networking, DNS, and load balancing |
| Node Service | ✅ | Node lifecycle, health, and capacity tracking |
| Policy Service | ✅ | Admission control and compliance rules |
| Quota Service | ✅ | Resource quotas and rate limiting |
| Audit Service | ✅ | Event logging and audit trails |
| Config Service | ✅ | Dynamic configuration and feature flags |

### Web UI

- React 18 + TypeScript dashboard
- Real-time job status and log streaming
- Namespace and resource management views
- Dark mode support

### CLI

- **helixctl** — Command-line interface for job submission, service inspection, and cluster administration.

### Deployment

- Binary builds for Linux, macOS, and Windows
- Docker Compose configuration for local development
- Kubernetes manifests with Kustomize overlays (development, staging, production)
- Helm chart (beta)

### Security

- mTLS for all inter-service communication
- RBAC with built-in roles and custom policy support
- Internal CA for certificate issuance
- TLS 1.3 for external API and Web UI
- Audit logging for all API requests

### Monitoring

- Prometheus metrics on all services (`:9090/metrics`)
- Pre-built Grafana dashboards
- Health (`/healthz`) and readiness (`/readyz`) endpoints

---

## Known Limitations

| Limitation | Impact | Workaround |
|------------|--------|------------|
| No automated certificate rotation | Manual rotation required every 90 days | Use cert-manager in Kubernetes |
| SPIFFE/SPIRE not integrated | Service identity relies on CA CN verification | Planned for v0.2.0 |
| gVisor sandboxing optional | Build isolation depends on container runtime | Enable gVisor runtime class in Kubernetes |
| Helm chart is beta | Some advanced tuning requires manual manifests | Use Kustomize overlays for production |
| Web UI lacks mobile optimization | Suboptimal experience on small screens | Use desktop browser |
| Single-region deployment tested | Multi-region behavior is theoretical | Deploy per region with federation planned |

---

## Breaking Changes

**None.** This is the initial release. All APIs are at `v1alpha1` or `v1beta1` and may evolve in future releases.

---

## Upgrade Notes

There is no upgrade path from a previous version. Fresh installation is required.

---

## Contributors

Thank you to everyone who contributed design, code, documentation, and testing to this release.
