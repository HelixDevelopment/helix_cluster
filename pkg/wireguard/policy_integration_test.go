//go:build integration

package wireguard

// policy_integration_test.go — integration tests for the WireGuard mesh
// segmentation policy engine.
//
// # CLAUDE-2 BOUNDARY JUSTIFICATION
//
// Live WireGuard packet enforcement (the ability to confirm that a TCP
// connection to port 5432 is actually blocked by pf(4)/nftables) requires:
//   (a) Two or more real WireGuard peers with kernel (Linux) or userspace
//       (wireguard-go on macOS) tunnel interfaces.
//   (b) Root privileges to manipulate pf(4) or nftables.
//   (c) Actual TCP traffic whose reception can be verified.
//
// None of these are available in the CI/hermetic test environment on macOS or
// in a rootless Linux sandbox. Attempting them would either silently succeed
// (stub path) or require root — either outcome is a PASS-bluff (CLAUDE-1).
//
// The boundary is therefore drawn here:
//
//   PROVEN IN-UNIT (hermetic, pure-Go, no privileges):
//     • Decision logic (PolicyEngine.Decide) against all selector/port combos.
//     • Generated enforcement ruleset text (exact "block"/"pass" lines, port
//       numbers, policy name / per-run UUID in comments).
//
//   DEPLOYMENT LAYER (not proven in test):
//     • Loading the ruleset into pf(4) via `pfctl -f`.
//     • Loading into nftables via `nft -f`.
//     • Confirming that a real TCP packet is accepted/dropped.
//
// The integration tests in this file use only in-process state (pure-Go
// computation) and hard-FAIL on any unexpected error — they do NOT t.Skip
// because the dependency (pure-Go computation) is unconditionally available.
// The per-run UUID is embedded in a policy Name and asserted present in the
// generated ruleset — this is the sink-side evidence for the integration tier.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// buildIntegrationPolicyNodes creates a realistic multi-node cluster for
// integration testing. Each node has a WireGuard IP and a rich label set.
func buildIntegrationPolicyNodes() []*Node {
	return []*Node{
		{ID: "node-web-0", WireGuardIP: "10.200.0.1", Labels: map[string]string{
			"tier": "web", "env": "prod", "region": "us-east",
		}},
		{ID: "node-web-1", WireGuardIP: "10.200.0.2", Labels: map[string]string{
			"tier": "web", "env": "prod", "region": "us-east",
		}},
		{ID: "node-db-primary", WireGuardIP: "10.200.0.10", Labels: map[string]string{
			"tier": "db", "role": "primary", "env": "prod",
		}},
		{ID: "node-db-replica", WireGuardIP: "10.200.0.11", Labels: map[string]string{
			"tier": "db", "role": "replica", "env": "prod",
		}},
		{ID: "node-cache-0", WireGuardIP: "10.200.0.20", Labels: map[string]string{
			"tier": "cache", "env": "prod",
		}},
		{ID: "node-admin", WireGuardIP: "10.200.0.50", Labels: map[string]string{
			"tier": "admin", "env": "prod",
		}},
	}
}

// buildIntegrationPolicies returns a realistic multi-policy set. The per-run
// UUID is embedded in a policy name to prove it survives into generated rulesets.
func buildIntegrationPolicies(runUUID string) []*NetworkPolicy {
	return []*NetworkPolicy{
		{
			// Per-run UUID embedded in name — must appear in generated ruleset.
			Name:         fmt.Sprintf("deny-web-to-db-postgres-%s", runUUID),
			Action:       ActionDeny,
			FromSelector: LabelSelector{MatchLabels: map[string]string{"tier": "web"}},
			ToSelector:   LabelSelector{MatchLabels: map[string]string{"tier": "db"}},
			Ports:        []uint16{5432},
		},
		{
			Name:         "allow-web-to-cache-redis",
			Action:       ActionAllow,
			FromSelector: LabelSelector{MatchLabels: map[string]string{"tier": "web"}},
			ToSelector:   LabelSelector{MatchLabels: map[string]string{"tier": "cache"}},
			Ports:        []uint16{6379},
		},
		{
			Name:         "allow-admin-to-db-all",
			Action:       ActionAllow,
			FromSelector: LabelSelector{MatchLabels: map[string]string{"tier": "admin"}},
			ToSelector:   LabelSelector{MatchLabels: map[string]string{"tier": "db"}},
			Ports:        nil, // all ports
		},
		{
			Name:         "deny-cache-to-db",
			Action:       ActionDeny,
			FromSelector: LabelSelector{MatchLabels: map[string]string{"tier": "cache"}},
			ToSelector:   LabelSelector{MatchLabels: map[string]string{"tier": "db"}},
			Ports:        []uint16{5432, 3306},
		},
	}
}

// TestIntegration_PolicyEngine_DecisionTableAndRuleset builds the engine from a
// realistic multi-policy config, asserts a TABLE of (src,dst,port)->decision,
// and verifies the full generated ruleset for each node against expected
// content — embedding and asserting the per-run UUID.
//
// Hard-fail policy: t.Fatalf on any unexpected error (pure-Go, always available).
func TestIntegration_PolicyEngine_DecisionTableAndRuleset(t *testing.T) {
	runUUID := uuid.New().String()
	nodes := buildIntegrationPolicyNodes()
	policies := buildIntegrationPolicies(runUUID)

	engine, err := NewPolicyEngine(policies, nodes)
	if err != nil {
		t.Fatalf("NewPolicyEngine: %v", err)
	}

	type row struct {
		src, dst string
		port     uint16
		want     Action
		desc     string
	}

	// DENY-wins: where admin->db matches both deny-cache-to-db (not applicable)
	// and allow-admin-to-db-all, only ALLOW applies for admin (tier=admin).
	table := []row{
		// web -> db:5432 → DENY (deny-web-to-db-postgres)
		{"node-web-0", "node-db-primary", 5432, ActionDeny, "web-0→db-primary:5432 DENY"},
		{"node-web-1", "node-db-replica", 5432, ActionDeny, "web-1→db-replica:5432 DENY"},
		// web -> db:22   → ALLOW (no deny policy for port 22 from web)
		{"node-web-0", "node-db-primary", 22, ActionAllow, "web-0→db-primary:22 ALLOW"},
		// web -> cache:6379 → ALLOW (allow-web-to-cache-redis)
		{"node-web-0", "node-cache-0", 6379, ActionAllow, "web-0→cache-0:6379 ALLOW"},
		// web -> cache:443 → ALLOW (no deny)
		{"node-web-0", "node-cache-0", 443, ActionAllow, "web-0→cache-0:443 ALLOW"},
		// cache -> db:5432 → DENY (deny-cache-to-db)
		{"node-cache-0", "node-db-primary", 5432, ActionDeny, "cache-0→db-primary:5432 DENY"},
		// cache -> db:3306 → DENY (deny-cache-to-db)
		{"node-cache-0", "node-db-primary", 3306, ActionDeny, "cache-0→db-primary:3306 DENY"},
		// cache -> db:6379 → ALLOW (deny-cache-to-db only covers 5432,3306; port 6379 not listed → default-permit)
		{"node-cache-0", "node-db-primary", 6379, ActionAllow, "cache-0→db-primary:6379 ALLOW"},
		// admin -> db:5432 → ALLOW (allow-admin-to-db-all; no deny for tier=admin)
		{"node-admin", "node-db-primary", 5432, ActionAllow, "admin→db-primary:5432 ALLOW"},
		// admin -> db:22  → ALLOW (allow-admin-to-db-all)
		{"node-admin", "node-db-primary", 22, ActionAllow, "admin→db-primary:22 ALLOW"},
		// db -> web:80    → ALLOW (no policy)
		{"node-db-primary", "node-web-0", 80, ActionAllow, "db-primary→web-0:80 ALLOW"},
	}

	for _, row := range table {
		t.Run(row.desc, func(t *testing.T) {
			got, err := engine.Decide(row.src, row.dst, row.port)
			if err != nil {
				t.Fatalf("Decide(%s, %s, %d): %v", row.src, row.dst, row.port, err)
			}
			if got != row.want {
				t.Fatalf("Decide(%s, %s, %d) = %s, want %s",
					row.src, row.dst, row.port, got, row.want)
			}
		})
	}

	// ----- Ruleset verification -----

	// Generate rulesets for all nodes.
	rulesets, err := engine.GenerateFullMeshRuleset()
	if err != nil {
		t.Fatalf("GenerateFullMeshRuleset: %v", err)
	}
	if len(rulesets) != len(nodes) {
		t.Fatalf("expected %d rulesets, got %d", len(nodes), len(rulesets))
	}

	// The per-run UUID must appear in the ruleset for nodes involved in the
	// deny-web-to-db-postgres policy (node-web-* and node-db-*).
	webDBNodes := []string{"node-web-0", "node-web-1", "node-db-primary", "node-db-replica"}
	for _, id := range webDBNodes {
		rs, ok := rulesets[id]
		if !ok {
			t.Fatalf("missing ruleset for %s", id)
		}
		// UUID from policy name must survive into the generated ruleset.
		if !strings.Contains(rs.Ruleset, runUUID) {
			t.Fatalf("ruleset for %s missing per-run UUID %s\nRuleset:\n%s",
				id, runUUID, rs.Ruleset)
		}
		// Must contain "block" for the deny policy.
		if !strings.Contains(rs.Ruleset, "block on wg0") {
			t.Fatalf("ruleset for %s missing 'block on wg0'\nRuleset:\n%s",
				id, rs.Ruleset)
		}
	}

	// node-admin should have no block rule (only allow-admin-to-db-all applies,
	// which is ActionAllow).
	adminRS, ok := rulesets["node-admin"]
	if !ok {
		t.Fatalf("missing ruleset for node-admin")
	}
	if strings.Contains(adminRS.Ruleset, "block on wg0") {
		t.Fatalf("node-admin ruleset should not contain 'block on wg0': no deny policies apply\nRuleset:\n%s",
			adminRS.Ruleset)
	}
	// admin ruleset should have "pass" (allow-admin-to-db-all).
	if !strings.Contains(adminRS.Ruleset, "pass on wg0") {
		t.Fatalf("node-admin ruleset must contain 'pass on wg0' for allow-admin-to-db-all\nRuleset:\n%s",
			adminRS.Ruleset)
	}

	// Verify node-cache-0 ruleset has "block" for deny-cache-to-db.
	cacheRS, ok := rulesets["node-cache-0"]
	if !ok {
		t.Fatalf("missing ruleset for node-cache-0")
	}
	if !strings.Contains(cacheRS.Ruleset, "block on wg0") {
		t.Fatalf("node-cache-0 ruleset must contain 'block on wg0' for deny-cache-to-db\nRuleset:\n%s",
			cacheRS.Ruleset)
	}
}
