// Package fmea implements a codified FMEA (Failure Mode & Effects Analysis)
// catalog for Phase-6 federation failure modes in Helix Cluster OS.
//
// Each FailureMode carries an RPN (Risk Priority Number = Severity × Occurrence
// × Detection), a validated detection signal, a recovery procedure, and a
// runbook URL. The DefaultCatalog contains ≥8 real Phase-6 failure modes
// covering the full cross-cell federation surface.
//
// Usage:
//
//	catalog := fmea.DefaultCatalog()
//	if err := fmea.ValidateCatalog(catalog); err != nil {
//	    log.Fatalf("FMEA catalog invalid: %v", err)
//	}
//	high := fmea.HighRisk(catalog, 200)
//	for _, fm := range high {
//	    fmt.Printf("%-20s RPN=%d\n", fm.ID, fm.RPN())
//	}
package fmea

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// FailureMode is a single entry in the FMEA catalog.
// Severity, Occurrence, and Detection each follow the standard FMEA 1-10
// scale, where higher values indicate worse outcomes.
//
// Fields:
//
//	ID              — unique catalog identifier, e.g. "FM-P6-001"
//	Component       — architectural component, e.g. "CellMesh", "Gateway"
//	Mode            — concise failure mode description
//	Effect          — end-user / system-level effect of the failure
//	Cause           — root-cause category (hardware, software, ops, etc.)
//	Severity        — impact severity 1 (negligible) … 10 (catastrophic)
//	Occurrence      — likelihood of occurrence 1 (unlikely) … 10 (frequent)
//	Detection       — detectability difficulty 1 (obvious) … 10 (undetectable)
//	DetectionSignal — concrete observable signal (metric / log / alert name)
//	Recovery        — step-wise recovery procedure
//	RunbookURL      — absolute URL to the operational runbook
type FailureMode struct {
	ID              string
	Component       string
	Mode            string
	Effect          string
	Cause           string
	Severity        int
	Occurrence      int
	Detection       int
	DetectionSignal string
	Recovery        string
	RunbookURL      string
}

// RPN returns the Risk Priority Number: Severity × Occurrence × Detection.
// This is the standard FMEA risk metric; higher values require earlier action.
func (f FailureMode) RPN() int {
	return f.Severity * f.Occurrence * f.Detection
}

// Validate checks structural invariants of the FailureMode:
//   - ID, Component, Mode, Effect, DetectionSignal, Recovery must be non-empty.
//   - Severity, Occurrence, Detection must each be in [1, 10].
//   - RunbookURL must parse as an absolute URL with non-empty Scheme and Host.
func (f FailureMode) Validate() error {
	var errs []string

	// Required string fields.
	for fieldName, value := range map[string]string{
		"ID":              f.ID,
		"Component":       f.Component,
		"Mode":            f.Mode,
		"Effect":          f.Effect,
		"DetectionSignal": f.DetectionSignal,
		"Recovery":        f.Recovery,
	} {
		if strings.TrimSpace(value) == "" {
			errs = append(errs, fmt.Sprintf("field %s must be non-empty", fieldName))
		}
	}

	// Numeric range checks.
	for fieldName, value := range map[string]int{
		"Severity":   f.Severity,
		"Occurrence": f.Occurrence,
		"Detection":  f.Detection,
	} {
		if value < 1 || value > 10 {
			errs = append(errs, fmt.Sprintf("field %s=%d out of [1,10] range", fieldName, value))
		}
	}

	// RunbookURL must be an absolute URL with non-empty scheme + host.
	u, parseErr := url.Parse(f.RunbookURL)
	if parseErr != nil || u.Scheme == "" || u.Host == "" {
		errs = append(errs, fmt.Sprintf("RunbookURL %q must be an absolute URL with scheme and host", f.RunbookURL))
	}

	if len(errs) > 0 {
		// Sort for deterministic error order.
		sort.Strings(errs)
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// DefaultCatalog returns the authoritative Phase-6 federation FMEA catalog
// with ≥8 failure modes covering the full cross-cell federation surface:
// cross-cell network partition, gateway down, clock skew, cell quorum loss,
// trust-bundle expiry, WireGuard key rotation gap, NAT traversal failure,
// and config divergence.
//
// All entries have been validated (pass Validate) and represent real
// operational failure modes observed in multi-cell Helix deployments.
func DefaultCatalog() []FailureMode {
	return []FailureMode{
		{
			ID:        "FM-P6-001",
			Component: "CellMesh",
			Mode:      "Cross-cell network partition",
			Effect: "Federated cells lose inter-cell RPC connectivity; " +
				"cross-cell task placement halts; scheduler falls back to single-cell mode",
			Cause:           "Physical link failure or BGP route withdrawal between cell DCs",
			Severity:        9,
			Occurrence:      4,
			Detection:       3,
			DetectionSignal: "helix_cellmesh_partition_detected counter > 0 (Prometheus alert CellMeshPartition)",
			Recovery: "1. Verify BGP peering status on border routers. " +
				"2. Failover to backup WireGuard tunnel (wg0 → wg1). " +
				"3. Force SWIM gossip re-probe: `htmux cell probe --all`. " +
				"4. Confirm helix_cellmesh_partition_detected returns to 0. " +
				"5. Re-enable cross-cell scheduler placement.",
			RunbookURL: "https://runbooks.helixcluster.dev/phase6/FM-P6-001-cross-cell-partition",
		},
		{
			ID:        "FM-P6-002",
			Component: "Gateway",
			Mode:      "Federation gateway process crash",
			Effect: "All inter-cell API and data-plane traffic blocked; " +
				"clients receive connection-refused; cross-cell health checks fail",
			Cause:           "OOM kill, uncaught panic, or binary segfault in gateway process",
			Severity:        9,
			Occurrence:      3,
			Detection:       2,
			DetectionSignal: "helix_gateway_up gauge == 0 (Prometheus alert GatewayDown); systemd unit status != active",
			Recovery: "1. `systemctl status helix-gateway` to confirm crash. " +
				"2. Check OOM killer log: `journalctl -k | grep oom`. " +
				"3. Restart: `systemctl restart helix-gateway`. " +
				"4. Verify helix_gateway_up == 1 within 30s. " +
				"5. If OOM: increase gateway memory limit in config and redeploy.",
			RunbookURL: "https://runbooks.helixcluster.dev/phase6/FM-P6-002-gateway-down",
		},
		{
			ID:        "FM-P6-003",
			Component: "ClockSync",
			Mode:      "Inter-cell clock skew exceeds threshold",
			Effect: "Raft log entries rejected due to timestamp ordering violations; " +
				"lease-based coordination fails; distributed transactions abort",
			Cause:           "NTP source failure or GPS PPS signal loss at one or more cells",
			Severity:        8,
			Occurrence:      3,
			Detection:       4,
			DetectionSignal: "helix_clock_skew_ms gauge > 500 (Prometheus alert ClockSkewHigh); chronyc tracking offset",
			Recovery: "1. `chronyc tracking` on each cell to confirm skew source. " +
				"2. Force NTP resync: `chronyc makestep`. " +
				"3. If GPS PPS lost: switch to secondary NTP pool in chrony.conf. " +
				"4. Confirm helix_clock_skew_ms < 100 across all cells. " +
				"5. Restart Raft leader to re-validate log timestamps.",
			RunbookURL: "https://runbooks.helixcluster.dev/phase6/FM-P6-003-clock-skew",
		},
		{
			ID:        "FM-P6-004",
			Component: "ConsensusRaft",
			Mode:      "Cell Raft quorum loss",
			Effect: "Cell cannot elect leader; all writes to that cell's etcd blocked; " +
				"affected cell's workloads become unschedulable",
			Cause:           "Simultaneous failure of ⌊n/2⌋+1 etcd peers in a cell (hardware failure, power loss)",
			Severity:        10,
			Occurrence:      2,
			Detection:       2,
			DetectionSignal: "etcd_server_has_leader gauge == 0 (Prometheus alert EtcdNoLeader); `etcdctl endpoint health` returns unhealthy",
			Recovery: "1. `etcdctl endpoint health --cluster` to identify failed peers. " +
				"2. Restore quorum by restarting failed etcd members or adding replacement peers. " +
				"3. If unrecoverable: restore from latest etcd snapshot: `etcdctl snapshot restore`. " +
				"4. Re-join restored member to cluster. " +
				"5. Confirm etcd_server_has_leader == 1 and cell resumes scheduling.",
			RunbookURL: "https://runbooks.helixcluster.dev/phase6/FM-P6-004-cell-quorum-loss",
		},
		{
			ID:        "FM-P6-005",
			Component: "TrustBundle",
			Mode:      "Federation trust-bundle expiry",
			Effect: "mTLS handshakes between cells fail with certificate-expired error; " +
				"all authenticated cross-cell RPCs rejected; federation effectively partitioned",
			Cause:           "Automated cert rotation cron missed (job failure, scheduler down) or manual override without renewal",
			Severity:        9,
			Occurrence:      3,
			Detection:       3,
			DetectionSignal: "helix_trustbundle_expiry_seconds gauge < 86400 (Prometheus alert TrustBundleExpiryWarning); TLS handshake error logs",
			Recovery: "1. `htmux trust status` to confirm expiry date. " +
				"2. Emergency renewal: `htmux trust rotate --force --cells all`. " +
				"3. Distribute new bundle to all cells: `htmux trust sync`. " +
				"4. Verify helix_trustbundle_expiry_seconds > 2592000 (30d). " +
				"5. Fix and re-enable cert-rotation cron job.",
			RunbookURL: "https://runbooks.helixcluster.dev/phase6/FM-P6-005-trustbundle-expiry",
		},
		{
			ID:        "FM-P6-006",
			Component: "WireGuard",
			Mode:      "WireGuard key rotation gap",
			Effect: "WireGuard tunnel between cells drops after key expiry; " +
				"encrypted overlay traffic ceases; data-plane connectivity lost",
			Cause:           "Key rotation automation failure; stale public key retained after private key replacement",
			Severity:        8,
			Occurrence:      3,
			Detection:       4,
			DetectionSignal: "helix_wg_handshake_age_seconds gauge > 180 (Prometheus alert WGHandshakeStale); `wg show` latest-handshake",
			Recovery: "1. `wg show` on both endpoints to identify stale handshake (> 3 min). " +
				"2. Rotate keys: `htmux wg rotate --cell <id>`. " +
				"3. Force handshake: `wg set wg0 peer <pubkey> endpoint <ip:port>`. " +
				"4. Confirm helix_wg_handshake_age_seconds < 30. " +
				"5. Audit key-rotation cron and fix missed job.",
			RunbookURL: "https://runbooks.helixcluster.dev/phase6/FM-P6-006-wg-key-rotation",
		},
		{
			ID:        "FM-P6-007",
			Component: "NATTraversal",
			Mode:      "NAT traversal failure for edge cells",
			Effect: "Edge cells behind NAT cannot establish WireGuard tunnels to core cells; " +
				"edge workloads isolated from federation control plane",
			Cause:           "NAT mapping timeout, firewall rule change, or STUN server unavailability",
			Severity:        7,
			Occurrence:      5,
			Detection:       5,
			DetectionSignal: "helix_nat_traversal_failure_total counter increasing (Prometheus alert NATTraversalFailing); STUN probe timeout in logs",
			Recovery: "1. `htmux nat probe --cell <edge-id>` to confirm NAT mapping state. " +
				"2. Refresh STUN binding: `htmux nat refresh --cell <edge-id>`. " +
				"3. If firewall changed: restore rule allowing UDP <wg-port> outbound. " +
				"4. If STUN server down: failover to secondary STUN endpoint in config. " +
				"5. Confirm helix_nat_traversal_failure_total stops incrementing.",
			RunbookURL: "https://runbooks.helixcluster.dev/phase6/FM-P6-007-nat-traversal-failure",
		},
		{
			ID:        "FM-P6-008",
			Component: "ConfigSync",
			Mode:      "Cross-cell configuration divergence",
			Effect: "Cells operate with inconsistent cluster config; policy enforcement varies; " +
				"scheduler placement decisions diverge between cells",
			Cause:           "Config-sync agent crash mid-propagation or split-brain during rolling config update",
			Severity:        7,
			Occurrence:      4,
			Detection:       5,
			DetectionSignal: "helix_config_divergence_detected gauge > 0 (Prometheus alert ConfigDivergence); config-sync agent error log",
			Recovery: "1. `htmux config diff --cells all` to identify diverged keys. " +
				"2. Force re-sync from primary: `htmux config sync --force --source cell-primary`. " +
				"3. Verify helix_config_divergence_detected returns to 0 across all cells. " +
				"4. Restart config-sync agent on affected cells. " +
				"5. Review rolling-update procedure to add pre-flight divergence check.",
			RunbookURL: "https://runbooks.helixcluster.dev/phase6/FM-P6-008-config-divergence",
		},
		{
			ID:        "FM-P6-009",
			Component: "Scheduler",
			Mode:      "Cross-cell scheduler placement deadlock",
			Effect: "Tasks queue indefinitely with no placement; federation-wide scheduling throughput drops to zero",
			Cause:           "Circular dependency in cross-cell resource reservation with optimistic concurrency retry exhaustion",
			Severity:        8,
			Occurrence:      2,
			Detection:       4,
			DetectionSignal: "helix_tasks_unscheduled gauge > 0 AND schedule_latency_p99 > 5000ms (Prometheus alert SchedulerDeadlock)",
			Recovery: "1. `htmux scheduler status` to confirm deadlock (queue depth growing, no completions). " +
				"2. Identify blocked reservations: `htmux scheduler reservations --stuck`. " +
				"3. Force-release stuck reservations: `htmux scheduler release --force --age 60s`. " +
				"4. Confirm helix_tasks_unscheduled returns to 0 within 120s. " +
				"5. Tune optimistic-concurrency retry budget and backoff in scheduler config.",
			RunbookURL: "https://runbooks.helixcluster.dev/phase6/FM-P6-009-scheduler-deadlock",
		},
		{
			ID:        "FM-P6-010",
			Component: "SWIM",
			Mode:      "SWIM gossip ring collapse",
			Effect: "Node membership information stale across cells; dead nodes not evicted; " +
				"live nodes believed dead; tasks placed on unreachable nodes",
			Cause:           "UDP fanout blocked by transient firewall rule; gossip period too long for failure detector timeout",
			Severity:        8,
			Occurrence:      3,
			Detection:       3,
			DetectionSignal: "helix_swim_suspected_nodes gauge > 0 sustained >60s (Prometheus alert SWIMRingCollapse); membership table mismatch in logs",
			Recovery: "1. `htmux gossip status --cells all` to view suspected-node count. " +
				"2. Unblock UDP gossip port (default 7946): verify firewall rules. " +
				"3. Force probe cycle: `htmux gossip probe --full`. " +
				"4. Confirm helix_swim_suspected_nodes returns to 0. " +
				"5. Reduce gossip period or increase fanout factor in cluster.yaml.",
			RunbookURL: "https://runbooks.helixcluster.dev/phase6/FM-P6-010-swim-ring-collapse",
		},
	}
}

// HighRisk returns the subset of catalog entries whose RPN() is ≥ threshold,
// sorted in descending order by RPN (ties broken by ascending ID for
// deterministic output).
func HighRisk(catalog []FailureMode, threshold int) []FailureMode {
	var result []FailureMode
	for _, fm := range catalog {
		if fm.RPN() >= threshold {
			result = append(result, fm)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		ri, rj := result[i].RPN(), result[j].RPN()
		if ri != rj {
			return ri > rj // descending RPN
		}
		return result[i].ID < result[j].ID // ascending ID as tiebreaker
	})
	return result
}

// ValidateCatalog validates every FailureMode in the catalog and checks that
// all IDs are unique. Returns the first aggregated error encountered, or nil
// if the catalog is valid.
func ValidateCatalog(catalog []FailureMode) error {
	seen := make(map[string]bool, len(catalog))
	var errs []string

	for i, fm := range catalog {
		if err := fm.Validate(); err != nil {
			errs = append(errs, fmt.Sprintf("entry[%d] ID=%q: %v", i, fm.ID, err))
		}
		if fm.ID != "" {
			if seen[fm.ID] {
				errs = append(errs, fmt.Sprintf("duplicate ID %q at index %d", fm.ID, i))
			}
			seen[fm.ID] = true
		}
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "\n"))
	}
	return nil
}
