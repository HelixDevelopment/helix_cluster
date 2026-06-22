#!/usr/bin/env bash
# =============================================================================
# Helix Cluster OS — config-driven worker deployment (D14).
# =============================================================================
# Brings up every worker in the deploy/cluster.env inventory: ships the agent
# binary + launcher, then starts a PERSISTENT helix-agent that registers under
# /clusteros/nodes/<id> and keeps its etcd lease alive for its whole lifetime.
#
# NOTHING is hardcoded. The control-host IP is auto-detected; etcd endpoints,
# ports, node ids/hosts/regions/wg-ports all come from deploy/cluster.env.
#
# Usage:
#   deploy/deploy-workers.sh                 # deploy all HELIX_NODES
#   deploy/deploy-workers.sh thinker nezha   # deploy a subset
#   HELIX_HOST_IP=10.0.0.5 deploy/deploy-workers.sh   # pin host IP override
#
# Requires: ssh/scp to each worker, the linux agent binary at
#   <repo>/dist/${HELIX_AGENT_BINARY}-linux-amd64  (or pass HELIX_AGENT_SRC).
# =============================================================================
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
# shellcheck disable=SC1091
. "$SCRIPT_DIR/lib.sh"

REPO_ROOT=$(helix_repo_root "$0")
helix_load_config "$SCRIPT_DIR"

# Resolve control-host IP and the etcd endpoint list workers will dial.
HOST_IP=$(helix_detect_host_ip)
ENDPOINTS=$(helix_etcd_endpoints "$HOST_IP")
export HELIX_HOST_IP="$HOST_IP"

# Agent binary to ship (linux/amd64). Overridable via HELIX_AGENT_SRC.
# Default layout: dist/linux-amd64/<binary> (per-arch subdir produced by the
# cross-compile toolchain). A flat dist/<binary>-linux-amd64 is also accepted.
AGENT_SRC="${HELIX_AGENT_SRC:-}"
if [ -z "$AGENT_SRC" ]; then
  if [ -f "$REPO_ROOT/dist/linux-amd64/${HELIX_AGENT_BINARY:-helix-agent}" ]; then
    AGENT_SRC="$REPO_ROOT/dist/linux-amd64/${HELIX_AGENT_BINARY:-helix-agent}"
  else
    AGENT_SRC="$REPO_ROOT/dist/${HELIX_AGENT_BINARY:-helix-agent}-linux-amd64"
  fi
fi
# Launcher source of truth is the version-controlled deploy/agent-launch.sh
# (dist/ is gitignored build output). Fall back to the dist copy if present.
if [ -f "$REPO_ROOT/deploy/agent-launch.sh" ]; then
  LAUNCHER_SRC="$REPO_ROOT/deploy/agent-launch.sh"
else
  LAUNCHER_SRC="$REPO_ROOT/dist/agent-launch.sh"
fi

echo "== Helix worker deploy =="
echo "control-host IP : $HOST_IP   (auto-detected; override with HELIX_HOST_IP)"
echo "etcd endpoints  : $ENDPOINTS"
echo "agent binary    : $AGENT_SRC"
[ -f "$AGENT_SRC" ]    || { echo "ERROR: agent binary not found: $AGENT_SRC" >&2; exit 1; }
[ -f "$LAUNCHER_SRC" ] || { echo "ERROR: launcher not found: $LAUNCHER_SRC" >&2; exit 1; }

SSH_USER="${HELIX_SSH_USER:-milosvasic}"
REMOTE_HOME="${HELIX_AGENT_REMOTE_HOME:-/home/$SSH_USER}"
BIN_NAME="${HELIX_AGENT_BINARY:-helix-agent}"

# Which nodes to deploy: CLI args override the HELIX_NODES default list.
if [ "$#" -gt 0 ]; then
  NODES="$*"
else
  NODES="${HELIX_NODES:-}"
fi
[ -n "$NODES" ] || { echo "ERROR: no nodes selected (HELIX_NODES empty)" >&2; exit 1; }

for nid in $NODES; do
  # Resolve the inventory entry HELIX_NODE_<id>="id|host|region|wg-port".
  eval "entry=\${HELIX_NODE_${nid}:-}"
  if [ -z "$entry" ]; then
    echo "!! skip '$nid': no HELIX_NODE_${nid} entry in cluster.env" >&2
    continue
  fi
  id=$(helix_node_field "$entry" 1)
  host=$(helix_node_field "$entry" 2)
  region=$(helix_node_field "$entry" 3)
  wgport=$(helix_node_field "$entry" 4)
  [ -n "$region" ] || region="lan"
  [ -n "$wgport" ] || wgport="${HELIX_AGENT_WG_PORT:-51820}"
  swimport="${HELIX_AGENT_SWIM_PORT:-7946}"
  target="$SSH_USER@$host"

  echo
  echo "-- node $id ($host) region=$region wg=$wgport swim=$swimport --"

  # Stop any running agent BEFORE scp (avoids 'text file busy' on the binary).
  ssh "$target" "pkill -f '$BIN_NAME' 2>/dev/null; sleep 1" || true

  # Ship binary + launcher.
  scp -q "$AGENT_SRC" "$target:$REMOTE_HOME/$BIN_NAME"
  scp -q "$LAUNCHER_SRC" "$target:$REMOTE_HOME/agent-launch.sh"
  ssh "$target" "chmod +x '$REMOTE_HOME/$BIN_NAME' '$REMOTE_HOME/agent-launch.sh'"

  # Enable user-linger so the systemd user manager (and the agent service it
  # runs) survives SSH logout. Without this the agent is reaped seconds after
  # deploy, its etcd lease lapses, and the node de-registers (the process-
  # lifecycle leg of D14). A user may enable its own linger without root.
  ssh "$target" "loginctl enable-linger \"\$USER\" 2>/dev/null || true"

  # Launch via the config-derived launcher. All values passed explicitly so the
  # remote launcher needs no cluster.env of its own; bind-addr is auto-detected
  # ON the worker (its own LAN IP), never passed as a literal.
  # agent-launch.sh signature: <id> <etcd> [region] [swim-port] [wg-port]
  # (binary chosen on the worker via HELIX_AGENT_BINARY).
  ssh "$target" "cd '$REMOTE_HOME' && \
    HELIX_AGENT_BINARY='$BIN_NAME' \
    HELIX_LAN_PREFIX='${HELIX_LAN_PREFIX:-192.168.}' \
    ./agent-launch.sh '$id' '$ENDPOINTS' '$region' '$swimport' '$wgport'"

  echo "   launched $id -> etcd $ENDPOINTS"
done

echo
echo "== deploy complete =="
echo "Verify:  podman exec helix-etcd-1 etcdctl get /clusteros/nodes/ --prefix --keys-only"
