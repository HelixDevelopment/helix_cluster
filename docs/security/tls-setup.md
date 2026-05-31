# TLS Configuration

This document covers certificate issuance, rotation procedures, and mutual TLS (mTLS) configuration between Helix Cluster OS services.

---

## Certificate Issuance

Helix Cluster OS supports two modes for certificate issuance:

### 1. Internal CA (Default for v0.1.0)

An internal Certificate Authority is bootstrapped during cluster initialization. All service certificates are signed by this CA.

```bash
# Initialize the internal CA and generate service certificates
helixctl ca init --out-dir ./certs
helixctl cert issue --ca ./certs/ca.crt --key ./certs/ca.key \
  --service helix-gateway --out ./certs/gateway/
```

Generated files per service:

| File | Purpose |
|------|---------|
| `tls.crt` | Service certificate |
| `tls.key` | Private key |
| `ca.crt` | CA bundle for client verification |

### 2. External CA / Let's Encrypt

For public-facing deployments, use an external CA:

```bash
helixctl cert issue-external \
  --provider letsencrypt \
  --email admin@example.com \
  --domains gateway.helix.example.com
```

---

## mTLS Between Services

All inter-service communication requires mutual TLS. The following settings apply to every service:

```yaml
# Example: helix-gateway TLS config
tls:
  enabled: true
  cert_file: /etc/helix/certs/tls.crt
  key_file: /etc/helix/certs/tls.key
  ca_file: /etc/helix/certs/ca.crt
  client_auth: require_and_verify
```

### Verification Behavior

| `client_auth` Value | Behavior |
|---------------------|----------|
| `none` | No client certificate required (not recommended) |
| `request` | Request client cert, allow without |
| `require` | Require client cert, verify against CA |
| `require_and_verify` | Require client cert, verify CN against service registry (default) |

> **Note:** `require_and_verify` is enforced by default. The Gateway verifies that the client certificate's Common Name matches a registered service identity in etcd.

---

## Certificate Rotation

Certificates should be rotated before expiry. The default validity period is **90 days**.

### Automated Rotation (Kubernetes)

When deploying with cert-manager:

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: helix-gateway-tls
spec:
  secretName: helix-gateway-tls
  issuerRef:
    name: helix-ca-issuer
    kind: ClusterIssuer
  dnsNames:
    - helix-gateway
    - helix-gateway.helix-system.svc.cluster.local
  duration: 2160h   # 90 days
  renewBefore: 360h # 15 days
```

Services watch the certificate files and reload automatically on change.

### Manual Rotation

1. Issue new certificates:

   ```bash
   helixctl cert rotate --service helix-gateway --ca ./certs/ca.crt
   ```

2. Distribute to hosts or update Kubernetes Secrets.

3. Restart or signal services to reload:

   ```bash
   kubectl rollout restart deployment/helix-gateway
   ```

4. Verify:

   ```bash
   helixctl cert verify --service helix-gateway
   ```

---

## etcd TLS

etcd peers and clients must use TLS:

```bash
etcd \
  --cert-file=/etc/etcd/server.crt \
  --key-file=/etc/etcd/server.key \
  --trusted-ca-file=/etc/etcd/ca.crt \
  --peer-cert-file=/etc/etcd/peer.crt \
  --peer-key-file=/etc/etcd/peer.key \
  --peer-trusted-ca-file=/etc/etcd/ca.crt \
  --peer-client-cert-auth \
  --client-cert-auth
```

Service configuration:

```yaml
etcd:
  endpoints:
    - https://etcd-0:2379
  tls:
    cert_file: /etc/helix/certs/etcd-client.crt
    key_file: /etc/helix/certs/etcd-client.key
    ca_file: /etc/helix/certs/ca.crt
```

---

## Troubleshooting TLS

| Symptom | Cause | Resolution |
|---------|-------|------------|
| `certificate signed by unknown authority` | Missing or wrong CA file | Verify `ca_file` path and contents |
| `bad certificate` | Expired or mismatched cert | Check expiry, rotate if needed |
| `handshake failure` | Incompatible TLS versions | Ensure TLS 1.2+ on all endpoints |
| `CN mismatch` | Client cert not registered | Verify service registration in etcd |
