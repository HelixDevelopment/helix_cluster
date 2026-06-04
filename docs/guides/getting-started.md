# Getting Started

This guide walks you through setting up Helix Cluster OS locally, starting the control plane and gateway, submitting your first build job, and viewing it in the Web UI.

---

## Prerequisites

| Component | Minimum Version | Notes |
|-----------|----------------|-------|
| Go        | 1.26+          | Required for building services |
| Node.js   | 18+            | Required for the Web UI |
| etcd      | 3.5+           | Distributed key-value store used as source of truth |
| Kubernetes| 1.28+          | Optional — for K8s-based deployments |

Verify your environment:

```bash
go version
node --version
etcd --version
kubectl version --client  # optional
```

---

## Clone and Build

```bash
git clone https://github.com/HelixCluster/helix-cluster-os.git
cd helix-cluster-os
make build
```

This compiles all 14 services and the CLI (`helixctl`).

---

## Run etcd Locally

```bash
etcd \
  --listen-client-urls http://127.0.0.1:2379 \
  --advertise-client-urls http://127.0.0.1:2379
```

> **Tip:** For local development, a single-node etcd is sufficient. For production, run a 3- or 5-node cluster.

---

## Start helixd (Control Plane)

```bash
./bin/helixd \
  --etcd-endpoints http://127.0.0.1:2379 \
  --bind-addr 0.0.0.0:8080
```

`helixd` registers services, manages cluster state, and exposes the administrative API.

---

## Start Gateway

```bash
./bin/helix-gateway \
  --etcd-endpoints http://127.0.0.1:2379 \
  --bind-addr 0.0.0.0:8443
```

The gateway is the entry point for all external traffic. It routes requests to the appropriate backend services.

---

## Submit Your First Build Job

Use `helixctl` to submit a build. `helixctl build` is a thin gRPC client of the
`helix-build` service (default `localhost:50051`, overridable by `--addr` or the
`HELIX_BUILD_ADDR` env var):

```bash
./bin/helixctl build submit \
  --repo-url https://github.com/helix/cluster \
  --ref main \
  --dockerfile Dockerfile \
  --build-arg FOO=bar
```

`submit` prints the returned build id, e.g. `build submitted: id=<job-id> queued=true`.

Check the job status, stream its logs, or cancel it:

```bash
./bin/helixctl build status <job-id>
./bin/helixctl build logs   <job-id>
./bin/helixctl build cancel <job-id>
```

---

## View in Web UI

Start the Web UI dev server:

```bash
cd web-ui
npm install
npm run dev
```

Open [http://localhost:3000](http://localhost:3000) in your browser. You should see the dashboard with your submitted build job.

---

## Next Steps

- Read the [Architecture Overview](./architecture.md) to understand how components interact.
- See the [Developer Guide](./development.md) for contributing code.
- Consult the [Operations Guide](./operations.md) for production deployments.
