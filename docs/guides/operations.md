# Operations Guide

This guide covers deployment options, monitoring, troubleshooting, and scaling for Helix Cluster OS in production.

---

## Deployment Options

### 1. Binary Deployment

Build and run services directly on hosts:

```bash
make build
./bin/helixd --config helixd.yaml
./bin/helix-gateway --config gateway.yaml
# ... start remaining services
```

**Best for:** Development, bare-metal environments, or when you manage your own orchestration.

---

### 2. Docker Deployment

Build images and run with Docker Compose:

```bash
make docker-build
 docker compose -f deployments/docker-compose.yml up -d
```

**Best for:** Single-node demos, CI pipelines, or small staging environments.

---

### 3. Kubernetes Deployment

Deploy to Kubernetes using the provided manifests:

```bash
kubectl apply -k deployments/k8s/overlays/production/
```

Key resources:

| Resource | Purpose |
|----------|---------|
| `etcd` StatefulSet | 3-node etcd cluster with persistent volumes |
| `helixd` Deployment | Control plane with 2+ replicas |
| `helix-gateway` Deployment | Edge gateway with HPA |
| Service Deployments | 14 service Deployments, each with HPA |
| Ingress | TLS-terminated external access |

**Best for:** Production, multi-node clusters, automatic failover, and scaling.

---

## Monitoring and Alerting

Helix Cluster OS exposes Prometheus metrics on `:9090/metrics` for every service.

### Key Metrics

| Metric | Description | Alert Threshold |
|--------|-------------|-----------------|
| `helix_request_duration_seconds` | API latency | p99 > 500ms |
| `helix_request_errors_total` | Error rate | > 1% over 5m |
| `helix_etcd_watch_latency_seconds` | etcd watch lag | > 1s |
| `helix_node_health` | Node health status | == 0 (unhealthy) |
| `helix_build_queue_depth` | Pending build jobs | > 100 |

### Recommended Stack

- **Prometheus** for metrics collection
- **Grafana** for dashboards (dashboard JSON in `deployments/monitoring/`)
- **Alertmanager** for routing alerts
- **Loki** for log aggregation

### Health Checks

Each service exposes:

- `GET /healthz` — liveness probe
- `GET /readyz` — readiness probe

Configure these in your orchestrator for automatic restart and traffic routing.

---

## Troubleshooting Common Issues

### etcd Connection Failures

**Symptoms:** Services fail to start, logs show `etcd: connection refused`.

**Resolution:**
- Verify etcd endpoints: `etcdctl endpoint health`
- Check TLS certificates are valid and not expired.
- Ensure network policies allow traffic to etcd ports (2379, 2380).

### Gateway 502 / 503 Errors

**Symptoms:** Clients receive gateway errors.

**Resolution:**
- Check gateway logs for upstream connection failures.
- Verify target services are registered in etcd (`helixctl service list`).
- Check readiness probes on backend services.

### High Build Queue Depth

**Symptoms:** Jobs remain pending, `helix_build_queue_depth` is high.

**Resolution:**
- Scale build workers: `kubectl scale deployment helix-build --replicas=10`
- Check scheduler logs for resource constraints.
- Verify node capacity via `helixctl node list`.

### Certificate Expiry

**Symptoms:** mTLS handshake failures between services.

**Resolution:**
- Check certificate expiry: `openssl x509 -in cert.pem -noout -dates`
- Rotate certificates (see [TLS Setup](../security/tls-setup.md)).

---

## Scaling Guidelines

### Horizontal Scaling

| Component | Scaling Trigger | Action |
|-----------|----------------|--------|
| Gateway | CPU > 70% or RPS > 10k | Add gateway replicas |
| Build Service | Queue depth > 50 | Add build workers |
| Scheduler | Scheduling latency > 200ms | Add scheduler replicas |
| etcd | Disk latency > 10ms | Add etcd nodes (odd count) |

### Vertical Scaling

- **Gateway:** 2 vCPU, 4 GiB memory baseline
- **Services:** 1 vCPU, 2 GiB memory baseline
- **etcd:** 4 vCPU, 8 GiB memory, SSD storage

### etcd Sizing

| Cluster Size | Max Nodes | Max Concurrent Jobs |
|--------------|-----------|---------------------|
| 1            | 10        | 100                 |
| 3            | 500       | 10,000              |
| 5            | 5,000     | 100,000             |

> **Note:** etcd performance is sensitive to disk I/O. Use dedicated SSDs for production clusters.
