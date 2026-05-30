# VM-Based Integration Tests

This directory contains integration tests for Helix Cluster behavior using QEMU virtual machines to simulate cluster nodes.

## Overview

The VM test framework spins up lightweight QEMU VMs (Alpine Linux cloud images) to test real cluster formation, failure detection, network partitions, and session migration in an environment that closely mirrors production deployments.

## Prerequisites

- **QEMU** (`qemu-system-x86`, `qemu-utils`)
- **KVM** (optional but strongly recommended for performance; TCG fallback is supported)
- **Go 1.25+**
- **VM test image** — downloaded automatically by CI, or manually to `~/.helix/vm-images/alpine-test.qcow2`

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `HELIX_VM_TESTS` | Set to `1` to enable VM tests | *(disabled)* |
| `HELIX_VM_IMAGE` | Path to the QCOW2 VM image | `~/.helix/vm-images/alpine-test.qcow2` |
| `HELIX_VM_NO_KVM` | Set to `1` to allow TCG fallback when KVM is unavailable | *(KVM required)* |

## Running Tests

```bash
# Download a VM image (example: Alpine 3.20 cloud image)
mkdir -p ~/.helix/vm-images
curl -L -o ~/.helix/vm-images/alpine-test.qcow2 \
  https://dl-cdn.alpinelinux.org/alpine/v3.20/releases/cloud/nocloud_alpine-3.20.0-x86_64-bios-r0.qcow2

# Run VM tests with KVM
HELIX_VM_TESTS=1 go test -tags=vm -v -timeout 30m ./tests/vm_nodes/...

# Run VM tests with TCG fallback (slower, no KVM required)
HELIX_VM_TESTS=1 HELIX_VM_NO_KVM=1 go test -tags=vm -v -timeout 45m ./tests/vm_nodes/...
```

## Test Scenarios

| Test | Description |
|------|-------------|
| `TestClusterFormation` | Boots 3 VMs, verifies they all join the cluster and report `joined` state |
| `TestNodeFailure` | Simulates a node crash and verifies the remaining nodes stay healthy |
| `TestNetworkPartition` | Creates a network partition, then verifies the cluster reconciles after healing |
| `TestSessionMigration` | Placeholder for session migration testing (requires `helix-agent`) |

## Architecture

- **`suite_test.go`** — Test cases using `testing` + `testify/require`
- **`helpers.go`** — `NodeSimulator` interface, `simNode` implementation, and helper functions (`spawnNodes`, `waitForClusterStable`, KVM/VM-image skips)
- **`.github/workflows/vm_integration.yml`** — GitHub Actions workflow that installs QEMU, downloads the VM image, and runs tests with KVM or TCG fallback

## Important Notes

- All test files use the `//go:build vm` build tag; they are excluded from normal builds
- Tests skip gracefully if KVM or the VM image is unavailable
- VMs are cleaned up automatically via `t.Cleanup`, even on test failure
- Maximum per-test timeout is **5 minutes** to avoid CI hangs
- **Do not commit VM images** to the repository
