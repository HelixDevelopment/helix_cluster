# shellcheck shell=sh
# =============================================================================
# Helix Cluster OS — shared deploy helpers (config-driven, zero hardcoding).
# =============================================================================
# Sourced by deploy/deploy-workers.sh. Provides:
#   helix_load_config        — locate + source deploy/cluster.env
#   helix_detect_host_ip     — auto-detect the control-host LAN IP (no literals)
#   helix_etcd_endpoints     — derive "<ip>:<p1>,<ip>:<p2>,<ip>:<p3>"
#   helix_node_field         — split an inventory entry "id|host|region|wg-port"
# Every value comes from cluster.env or runtime detection; nothing is literal.
# =============================================================================

# Resolve the repo root from this file's location so the scripts work from any
# CWD without a hardcoded path.
helix_repo_root() {
  # $1 = path of the sourcing script (its $0)
  CDPATH= cd -- "$(dirname -- "$1")/.." && pwd
}

# Source deploy/cluster.env into the environment. $1 = deploy dir.
helix_load_config() {
  _cfg="$1/cluster.env"
  if [ ! -f "$_cfg" ]; then
    echo "helix: config not found: $_cfg" >&2
    return 1
  fi
  set -a
  # shellcheck disable=SC1090
  . "$_cfg"
  set +a
}

# Auto-detect the control-host LAN IP. macOS: ipconfig getifaddr <iface>;
# Linux: the source address of the default route. Falls back to the first
# global-scope address matching HELIX_LAN_PREFIX. NEVER returns a literal.
# Honors a pre-set HELIX_HOST_IP (operator override) if non-empty.
helix_detect_host_ip() {
  if [ -n "${HELIX_HOST_IP:-}" ]; then
    printf '%s\n' "$HELIX_HOST_IP"
    return 0
  fi
  _ip=""
  if command -v ipconfig >/dev/null 2>&1; then
    # macOS — try the configured interface, then en0/en1.
    for _if in "${HELIX_HOST_IFACE_DARWIN:-en0}" en0 en1; do
      _ip=$(ipconfig getifaddr "$_if" 2>/dev/null) || _ip=""
      [ -n "$_ip" ] && break
    done
  fi
  if [ -z "$_ip" ] && command -v ip >/dev/null 2>&1; then
    # Linux — source IP of the route to a public address (no packets sent).
    _ip=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src"){print $(i+1); exit}}')
  fi
  if [ -z "$_ip" ] && command -v ip >/dev/null 2>&1; then
    # Fallback — first global-scope address on the LAN prefix.
    _ip=$(ip -4 -o addr show scope global 2>/dev/null | awk '{print $4}' | cut -d/ -f1 \
          | grep -E "^${HELIX_LAN_PREFIX:-192.168.}" | head -1)
  fi
  if [ -z "$_ip" ]; then
    echo "helix: could not auto-detect host IP; set HELIX_HOST_IP" >&2
    return 1
  fi
  printf '%s\n' "$_ip"
}

# Build the comma-separated etcd client endpoint list from the detected host IP
# and the per-member published ports in cluster.env.
helix_etcd_endpoints() {
  _ip="$1"
  printf '%s:%s,%s:%s,%s:%s\n' \
    "$_ip" "${ETCD_1_CLIENT_PORT:-2379}" \
    "$_ip" "${ETCD_2_CLIENT_PORT:-2479}" \
    "$_ip" "${ETCD_3_CLIENT_PORT:-2579}"
}

# Extract a field (1-based) from a "id|host|region|wg-port" inventory entry.
helix_node_field() {
  printf '%s' "$1" | cut -d'|' -f"$2"
}
