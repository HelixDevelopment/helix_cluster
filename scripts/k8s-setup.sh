#!/usr/bin/env bash
set -euo pipefail

# k8s-setup.sh — bootstrap local Kubernetes cluster for Helix Cluster development

CLUSTER_NAME="${CLUSTER_NAME:-helix}"
K8S_TOOL="${K8S_TOOL:-kind}"   # or "k3d"

echo "=== Helix Cluster K8s Setup ==="
echo "Tool: $K8S_TOOL"
echo "Cluster: $CLUSTER_NAME"

if command -v "$K8S_TOOL" &>/dev/null; then
    echo "$K8S_TOOL is already installed."
else
    echo "Installing $K8S_TOOL..."
    if [[ "$K8S_TOOL" == "kind" ]]; then
        curl -Lo ./kind https://kind.sigs.k8s.io/dl/v0.23.0/kind-$(uname)-amd64
        chmod +x ./kind
        sudo mv ./kind /usr/local/bin/kind
    elif [[ "$K8S_TOOL" == "k3d" ]]; then
        curl -s https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh | bash
    else
        echo "Unknown tool: $K8S_TOOL"
        exit 1
    fi
fi

if $K8S_TOOL get clusters | grep -q "$CLUSTER_NAME"; then
    echo "Cluster '$CLUSTER_NAME' already exists."
else
    echo "Creating cluster '$CLUSTER_NAME'..."
    if [[ "$K8S_TOOL" == "kind" ]]; then
        kind create cluster --name "$CLUSTER_NAME"
    elif [[ "$K8S_TOOL" == "k3d" ]]; then
        k3d cluster create "$CLUSTER_NAME" --agents 2
    fi
fi

echo "Cluster ready. Context:"
kubectl config current-context

echo "=== Done ==="
