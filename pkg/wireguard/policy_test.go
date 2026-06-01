package wireguard

// policy_test.go — deterministic unit tests for the WireGuard segmentation
// policy engine (PolicyEngine.Decide + GenerateEnforcementRuleset).
//
// # ANTI-BLUFF (CLAUDE-1)
// Every assertion is a SINK-SIDE FACT:
//   - Decide(a,b,5432) == DENY (not "no error" or "non-nil")
//   - Decide(a,b,22)   == ALLOW
//   - Decide(a,c,5432) == ALLOW
//   - The generated ruleset text CONTAINS the exact "block … port 5432" line.
//   - The generated ruleset text does NOT contain "block … port 22" for an
//     ALLOW pair.
//
// # CROSS-PLATFORM PARITY (CLAUDE-2)
// All logic is pure-Go computation: label matching, decision table evaluation,
// and string rendering. No kernel interface, no packet I/O, no root privilege.
// The boundary with the live packet-filtering layer is documented in policy.go's
// GenerateEnforcementRuleset.
//
// # §1.1 MUTATION PAIRS
// Each branch under test is documented with the mutation that kills it and why
// the test then fails. Mutation execution evidence is captured in the
// mutation_proof field of the StructuredOutput.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- helpers ---------------------------------------------------------------

// makeNode is a test helper that creates a Node with the given ID, WG IP, and
// label key=value pairs (alternating key, value).
func makeNode(id, wgIP string, kvPairs ...string) *Node {
	labels := make(map[string]string, len(kvPairs)/2)
	for i := 0; i+1 < len(kvPairs); i += 2 {
		labels[kvPairs[i]] = kvPairs[i+1]
	}
	return &Node{ID: id, WireGuardIP: wgIP, Labels: labels}
}

// makeSelector returns a LabelSelector for the given key=value pairs.
func makeSelector(kvPairs ...string) LabelSelector {
	m := make(map[string]string, len(kvPairs)/2)
	for i := 0; i+1 < len(kvPairs); i += 2 {
		m[kvPairs[i]] = kvPairs[i+1]
	}
	return LabelSelector{MatchLabels: m}
}

// ---- TestDecide_DenyOnMatchedGroupAndPort ----------------------------------

// TestDecide_DenyOnMatchedGroupAndPort is the primary sink-side correctness
// test for the decision engine.
//
// Setup:
//   - Group A nodes: label group=app   (nodeA)
//   - Group B nodes: label group=db    (nodeB)
//   - Group C nodes: label group=cache (nodeC)
//   - Policy: DENY from group=app to group=db on port 5432
//
// Assertions (all sink-side decisions, not "no panic"):
//  1. Decide(A, B, 5432) == DENY    — matched policy
//  2. Decide(A, B, 22)   == ALLOW   — port not in policy
//  3. Decide(A, C, 5432) == ALLOW   — C not in group=db
//  4. Decide(B, A, 5432) == ALLOW   — reverse direction, B not in group=app
//
// §1.1 mutation pair documented below.
func TestDecide_DenyOnMatchedGroupAndPort(t *testing.T) {
	nodeA := makeNode("node-a", "10.200.0.1", "group", "app")
	nodeB := makeNode("node-b", "10.200.0.2", "group", "db")
	nodeC := makeNode("node-c", "10.200.0.3", "group", "cache")

	denyDBPort := &NetworkPolicy{
		Name:         "deny-app-to-db-postgres",
		Action:       ActionDeny,
		FromSelector: makeSelector("group", "app"),
		ToSelector:   makeSelector("group", "db"),
		Ports:        []uint16{5432},
	}

	engine, err := NewPolicyEngine(
		[]*NetworkPolicy{denyDBPort},
		[]*Node{nodeA, nodeB, nodeC},
	)
	require.NoError(t, err)

	// 1. DENY: group=app -> group=db on port 5432.
	// §1.1 mutation: change denyDBPort.Action to ActionAllow → Decide returns
	// ALLOW, assert.Equal(ActionDeny, got) fires.
	got, err := engine.Decide("node-a", "node-b", 5432)
	require.NoError(t, err)
	assert.Equal(t, ActionDeny, got,
		"group=app -> group=db on port 5432 must be DENY")

	// 2. ALLOW: same pair, different port → no matching policy → default-permit.
	// §1.1 mutation: remove matchesPort check → policy matches port 22 too →
	// Decide returns DENY, assert.Equal(ActionAllow, got) fires.
	got, err = engine.Decide("node-a", "node-b", 22)
	require.NoError(t, err)
	assert.Equal(t, ActionAllow, got,
		"group=app -> group=db on port 22 must be ALLOW (no matching policy)")

	// 3. ALLOW: A -> C on port 5432 → ToSelector group=db does not match group=cache.
	// §1.1 mutation: use empty ToSelector (matches all) → DENY applied to C too →
	// assert.Equal(ActionAllow, got) fires.
	got, err = engine.Decide("node-a", "node-c", 5432)
	require.NoError(t, err)
	assert.Equal(t, ActionAllow, got,
		"group=app -> group=cache on port 5432 must be ALLOW (ToSelector mismatch)")

	// 4. ALLOW: B -> A on port 5432 → FromSelector group=app does not match group=db.
	// §1.1 mutation: remove FromSelector check → reverse direction also denied →
	// assert.Equal(ActionAllow, got) fires.
	got, err = engine.Decide("node-b", "node-a", 5432)
	require.NoError(t, err)
	assert.Equal(t, ActionAllow, got,
		"group=db -> group=app on port 5432 must be ALLOW (reverse direction)")
}

// ---- TestDecide_DenyWinsOverAllow ------------------------------------------

// TestDecide_DenyWinsOverAllow proves DENY-wins semantics: when a DENY and an
// ALLOW policy both match (src, dst, port), the result is DENY.
//
// §1.1 mutation: swap resolution order so ALLOW wins → Decide returns ALLOW,
// assert.Equal(ActionDeny, got) fires.
func TestDecide_DenyWinsOverAllow(t *testing.T) {
	nodeA := makeNode("node-a", "10.0.0.1", "env", "prod")
	nodeB := makeNode("node-b", "10.0.0.2", "env", "prod", "role", "db")

	allowAll := &NetworkPolicy{
		Name:         "allow-all-prod",
		Action:       ActionAllow,
		FromSelector: makeSelector("env", "prod"),
		ToSelector:   makeSelector("env", "prod"),
		Ports:        nil, // all ports
	}
	denyDB := &NetworkPolicy{
		Name:         "deny-db-port",
		Action:       ActionDeny,
		FromSelector: makeSelector("env", "prod"),
		ToSelector:   makeSelector("role", "db"),
		Ports:        []uint16{5432},
	}

	// Register ALLOW first, DENY second — DENY-wins must still apply.
	engine, err := NewPolicyEngine(
		[]*NetworkPolicy{allowAll, denyDB},
		[]*Node{nodeA, nodeB},
	)
	require.NoError(t, err)

	// Both policies match A->B:5432; DENY must win.
	got, err := engine.Decide("node-a", "node-b", 5432)
	require.NoError(t, err)
	assert.Equal(t, ActionDeny, got,
		"DENY must win when both ALLOW and DENY match")

	// For port 22, only allowAll matches → ALLOW.
	got, err = engine.Decide("node-a", "node-b", 22)
	require.NoError(t, err)
	assert.Equal(t, ActionAllow, got,
		"only ALLOW matches port 22, must be ALLOW")
}

// ---- TestDecide_EmptyPolicySetDefaultPermit --------------------------------

// TestDecide_EmptyPolicySetDefaultPermit proves that with no policies, every
// decision is ALLOW (default-permit).
//
// §1.1 mutation: change default return to ActionDeny → assert fires for the
// no-policy case.
func TestDecide_EmptyPolicySetDefaultPermit(t *testing.T) {
	nodeA := makeNode("node-a", "10.0.0.1", "group", "x")
	nodeB := makeNode("node-b", "10.0.0.2", "group", "y")

	engine, err := NewPolicyEngine(nil, []*Node{nodeA, nodeB})
	require.NoError(t, err)

	got, err := engine.Decide("node-a", "node-b", 443)
	require.NoError(t, err)
	assert.Equal(t, ActionAllow, got, "empty policy set must default to ALLOW")
}

// ---- TestDecide_UnknownNodeReturnsError ------------------------------------

// TestDecide_UnknownNodeReturnsError proves that referencing a non-existent
// node ID returns an error.
//
// §1.1 mutation: remove the node-existence check → unknown ID silently treated
// as a zero-label node, no error → require.Error fires.
func TestDecide_UnknownNodeReturnsError(t *testing.T) {
	nodeA := makeNode("node-a", "10.0.0.1")
	engine, err := NewPolicyEngine(nil, []*Node{nodeA})
	require.NoError(t, err)

	_, err = engine.Decide("node-a", "does-not-exist", 80)
	assert.Error(t, err, "unknown dst node must return error")

	_, err = engine.Decide("does-not-exist", "node-a", 80)
	assert.Error(t, err, "unknown src node must return error")
}

// ---- TestDecide_EmptySelectorMatchesAll ------------------------------------

// TestDecide_EmptySelectorMatchesAll proves that an empty MatchLabels selector
// matches every node regardless of labels.
//
// §1.1 mutation: change Matches to require at least one label match → empty
// selector matches nothing → a DENY-all policy has no effect → test fails.
func TestDecide_EmptySelectorMatchesAll(t *testing.T) {
	nodeA := makeNode("node-a", "10.0.0.1", "group", "web")
	nodeB := makeNode("node-b", "10.0.0.2", "group", "db")

	denyAll := &NetworkPolicy{
		Name:         "deny-all-5432",
		Action:       ActionDeny,
		FromSelector: LabelSelector{}, // empty = match all
		ToSelector:   LabelSelector{}, // empty = match all
		Ports:        []uint16{5432},
	}

	engine, err := NewPolicyEngine([]*NetworkPolicy{denyAll}, []*Node{nodeA, nodeB})
	require.NoError(t, err)

	// Empty selectors match all nodes → DENY on port 5432.
	got, err := engine.Decide("node-a", "node-b", 5432)
	require.NoError(t, err)
	assert.Equal(t, ActionDeny, got,
		"empty selector must match all nodes → DENY on port 5432")

	// ALLOW on different port.
	got, err = engine.Decide("node-a", "node-b", 443)
	require.NoError(t, err)
	assert.Equal(t, ActionAllow, got, "port 443 not in policy → ALLOW")
}

// ---- TestGenerateEnforcementRuleset_ContainsExactDenyRule ------------------

// TestGenerateEnforcementRuleset_ContainsExactDenyRule proves that the
// generated ruleset for a node contains the EXACT deny rule text for the
// policy that DENY-matches traffic through that node.
//
// This is the critical sink-side artifact assertion from the closure criteria.
//
// §1.1 mutation: change "block" to "pass" in GenerateEnforcementRuleset →
// Contains("block on wg0") fails.
func TestGenerateEnforcementRuleset_ContainsExactDenyRule(t *testing.T) {
	nodeA := makeNode("node-a", "10.200.0.1", "group", "app")
	nodeB := makeNode("node-b", "10.200.0.2", "group", "db")

	denyPolicy := &NetworkPolicy{
		Name:         "deny-app-to-db-5432",
		Action:       ActionDeny,
		FromSelector: makeSelector("group", "app"),
		ToSelector:   makeSelector("group", "db"),
		Ports:        []uint16{5432},
	}

	engine, err := NewPolicyEngine([]*NetworkPolicy{denyPolicy}, []*Node{nodeA, nodeB})
	require.NoError(t, err)

	// Generate ruleset for nodeA (the source node for the deny policy).
	rs, err := engine.GenerateEnforcementRuleset("node-a")
	require.NoError(t, err)
	require.Equal(t, "node-a", rs.NodeID)

	// The ruleset must contain the "block" verb for the deny policy.
	// §1.1 mutation: emit "pass" instead of "block" → Contains("block") fails.
	assert.Contains(t, rs.Ruleset, "block on wg0",
		"DENY policy must produce 'block on wg0' in ruleset for node-a")

	// The deny rule must contain port 5432.
	// §1.1 mutation: omit port from rule → Contains("port 5432") fails.
	assert.Contains(t, rs.Ruleset, "port 5432",
		"DENY policy must include port 5432 in generated rule")

	// The policy name must appear as a comment.
	// §1.1 mutation: omit policy name from comment → Contains("deny-app-to-db-5432") fails.
	assert.Contains(t, rs.Ruleset, "deny-app-to-db-5432",
		"policy name must appear as a comment in the ruleset")

	// The ruleset must NOT contain "block" for an ALLOW-only scenario.
	// Generate for nodeB as a destination — it should still have a block rule
	// (it is the destination of the deny).
	rsB, err := engine.GenerateEnforcementRuleset("node-b")
	require.NoError(t, err)
	assert.Contains(t, rsB.Ruleset, "block on wg0",
		"DENY policy must also appear in ruleset for node-b (dst node)")
}

// ---- TestGenerateEnforcementRuleset_AllowPairOmitsBlockRule ----------------

// TestGenerateEnforcementRuleset_AllowPairOmitsBlockRule proves that a pair
// that is ALLOWED does not generate a "block" rule. The generated text for an
// ALLOW policy must be "pass", not "block".
//
// §1.1 mutation: emit "block" for ActionAllow in GenerateEnforcementRuleset →
// Contains("pass") assertion still fires OR NotContains("block") fires.
func TestGenerateEnforcementRuleset_AllowPairOmitsBlockRule(t *testing.T) {
	nodeA := makeNode("node-a", "10.0.0.1", "group", "web")
	nodeB := makeNode("node-b", "10.0.0.2", "group", "web")

	allowSSH := &NetworkPolicy{
		Name:         "allow-web-ssh",
		Action:       ActionAllow,
		FromSelector: makeSelector("group", "web"),
		ToSelector:   makeSelector("group", "web"),
		Ports:        []uint16{22},
	}

	engine, err := NewPolicyEngine([]*NetworkPolicy{allowSSH}, []*Node{nodeA, nodeB})
	require.NoError(t, err)

	rs, err := engine.GenerateEnforcementRuleset("node-a")
	require.NoError(t, err)

	// The ALLOW policy must produce "pass", not "block".
	assert.Contains(t, rs.Ruleset, "pass on wg0",
		"ALLOW policy must produce 'pass on wg0' in the ruleset")

	// The ruleset must NOT contain "block on wg0" for this all-ALLOW scenario.
	assert.NotContains(t, rs.Ruleset, "block on wg0",
		"ALLOW-only policy set must not produce any 'block' rules")
}

// ---- TestGenerateEnforcementRuleset_PolicyNameInRuleset --------------------

// TestGenerateEnforcementRuleset_PolicyNameInRuleset confirms the policy name
// (which may include a per-run UUID) survives into the generated ruleset.
// This is the UUID-embedding closure criterion.
//
// §1.1 mutation: omit the policy Name from the comment in
// GenerateEnforcementRuleset → Contains fails.
func TestGenerateEnforcementRuleset_PolicyNameInRuleset(t *testing.T) {
	const policyName = "deny-group-a-to-b-port-5432-uuid-12345678"
	nodeA := makeNode("node-a", "10.0.0.1", "grp", "a")
	nodeB := makeNode("node-b", "10.0.0.2", "grp", "b")

	policy := &NetworkPolicy{
		Name:         policyName,
		Action:       ActionDeny,
		FromSelector: makeSelector("grp", "a"),
		ToSelector:   makeSelector("grp", "b"),
		Ports:        []uint16{5432},
	}

	engine, err := NewPolicyEngine([]*NetworkPolicy{policy}, []*Node{nodeA, nodeB})
	require.NoError(t, err)

	rs, err := engine.GenerateEnforcementRuleset("node-a")
	require.NoError(t, err)

	// Full policy name (including the UUID portion) must survive into the ruleset.
	assert.Contains(t, rs.Ruleset, policyName,
		"policy name (including UUID) must appear in the generated ruleset")
}

// ---- TestDecide_MultiplePortsOneDenied -------------------------------------

// TestDecide_MultiplePortsOneDenied proves that a policy with multiple ports
// only DENY-matches the exact ports listed.
//
// §1.1 mutation: change matchesPort to return true for any port when Ports is
// non-empty → port 443 also gets DENY → assert.Equal(ActionAllow) fires.
func TestDecide_MultiplePortsOneDenied(t *testing.T) {
	nodeA := makeNode("node-a", "10.0.0.1", "tier", "frontend")
	nodeB := makeNode("node-b", "10.0.0.2", "tier", "backend")

	policy := &NetworkPolicy{
		Name:         "deny-frontend-backend-db-ports",
		Action:       ActionDeny,
		FromSelector: makeSelector("tier", "frontend"),
		ToSelector:   makeSelector("tier", "backend"),
		Ports:        []uint16{5432, 3306, 6379},
	}

	engine, err := NewPolicyEngine([]*NetworkPolicy{policy}, []*Node{nodeA, nodeB})
	require.NoError(t, err)

	for _, port := range []uint16{5432, 3306, 6379} {
		got, err := engine.Decide("node-a", "node-b", port)
		require.NoError(t, err)
		assert.Equal(t, ActionDeny, got,
			"port %d must be DENY (in policy list)", port)
	}

	// Port 443 is not listed → ALLOW.
	got, err := engine.Decide("node-a", "node-b", 443)
	require.NoError(t, err)
	assert.Equal(t, ActionAllow, got,
		"port 443 not in policy list → ALLOW")
}

// ---- TestNewPolicyEngine_Validation ----------------------------------------

// TestNewPolicyEngine_Validation proves constructor validation errors.
func TestNewPolicyEngine_Validation(t *testing.T) {
	// nil policy rejected.
	_, err := NewPolicyEngine([]*NetworkPolicy{nil}, []*Node{makeNode("n", "1.1.1.1")})
	assert.Error(t, err, "nil policy must be rejected")

	// nil node rejected.
	policy := &NetworkPolicy{Name: "p", Action: ActionAllow}
	_, err = NewPolicyEngine([]*NetworkPolicy{policy}, []*Node{nil})
	assert.Error(t, err, "nil node must be rejected")

	// Empty node ID rejected.
	_, err = NewPolicyEngine(nil, []*Node{{ID: "", Labels: map[string]string{}}})
	assert.Error(t, err, "empty node ID must be rejected")

	// Duplicate node ID rejected.
	n := makeNode("dup", "1.1.1.1")
	_, err = NewPolicyEngine(nil, []*Node{n, n})
	assert.Error(t, err, "duplicate node ID must be rejected")

	// Empty policy name rejected.
	badPolicy := &NetworkPolicy{Name: "", Action: ActionAllow}
	_, err = NewPolicyEngine([]*NetworkPolicy{badPolicy}, []*Node{makeNode("n", "1.1.1.1")})
	assert.Error(t, err, "empty policy name must be rejected")
}

// ---- TestGenerateFullMeshRuleset -------------------------------------------

// TestGenerateFullMeshRuleset proves that GenerateFullMeshRuleset produces a
// ruleset entry for every node and that each ruleset contains the right content.
func TestGenerateFullMeshRuleset(t *testing.T) {
	nodes := []*Node{
		makeNode("node-a", "10.0.0.1", "group", "app"),
		makeNode("node-b", "10.0.0.2", "group", "db"),
		makeNode("node-c", "10.0.0.3", "group", "cache"),
	}
	policies := []*NetworkPolicy{
		{
			Name:         "deny-app-to-db-5432",
			Action:       ActionDeny,
			FromSelector: makeSelector("group", "app"),
			ToSelector:   makeSelector("group", "db"),
			Ports:        []uint16{5432},
		},
	}

	engine, err := NewPolicyEngine(policies, nodes)
	require.NoError(t, err)

	rulesets, err := engine.GenerateFullMeshRuleset()
	require.NoError(t, err)

	// Must have an entry for every node.
	assert.Len(t, rulesets, 3, "GenerateFullMeshRuleset must produce one entry per node")

	// node-a and node-b are involved in the deny policy.
	assert.Contains(t, rulesets["node-a"].Ruleset, "block on wg0",
		"node-a ruleset must have deny block (it is the source)")
	assert.Contains(t, rulesets["node-b"].Ruleset, "block on wg0",
		"node-b ruleset must have deny block (it is the destination)")

	// node-c is not involved in the deny policy — its ruleset should have no
	// "block" line (there are no policies that involve node-c).
	assert.NotContains(t, rulesets["node-c"].Ruleset, "block on wg0",
		"node-c ruleset must not have a deny block (not involved in any deny policy)")
}

// ---- TestLabelSelector_Matches_Exhaustive ----------------------------------

// TestLabelSelector_Matches_Exhaustive exercises LabelSelector.Matches
// directly, covering empty selector, full match, partial mismatch.
//
// §1.1 mutation: change Matches to return true unconditionally → mismatch cases
// fail; change to return false → empty-selector case fails.
func TestLabelSelector_Matches_Exhaustive(t *testing.T) {
	labels := map[string]string{"env": "prod", "role": "db", "tier": "backend"}

	// Empty selector: matches everything.
	assert.True(t, LabelSelector{}.Matches(labels), "empty selector must match")
	assert.True(t, LabelSelector{}.Matches(nil), "empty selector must match nil labels")

	// Exact subset match.
	sel := LabelSelector{MatchLabels: map[string]string{"env": "prod"}}
	assert.True(t, sel.Matches(labels), "single-label subset must match")

	// Full match.
	fullSel := LabelSelector{MatchLabels: labels}
	assert.True(t, fullSel.Matches(labels), "full label set must match itself")

	// Wrong value.
	wrongVal := LabelSelector{MatchLabels: map[string]string{"env": "staging"}}
	assert.False(t, wrongVal.Matches(labels), "wrong value must not match")

	// Missing key.
	missingKey := LabelSelector{MatchLabels: map[string]string{"datacenter": "us-east"}}
	assert.False(t, missingKey.Matches(labels), "missing key must not match")

	// One match + one mismatch → overall false.
	mixed := LabelSelector{MatchLabels: map[string]string{"env": "prod", "role": "web"}}
	assert.False(t, mixed.Matches(labels), "partial mismatch must not match")
}

// ---- TestGenerateEnforcementRuleset_AllPortsSentinel -----------------------

// TestGenerateEnforcementRuleset_AllPortsSentinel proves that a policy with an
// empty Ports list generates a rule with "any" as the port specifier.
//
// §1.1 mutation: change the portStr="any" branch so it emits "all" instead →
// Contains("port any") fails.
func TestGenerateEnforcementRuleset_AllPortsSentinel(t *testing.T) {
	nodeA := makeNode("node-a", "10.0.0.1", "g", "x")
	nodeB := makeNode("node-b", "10.0.0.2", "g", "y")

	policy := &NetworkPolicy{
		Name:         "deny-all-ports",
		Action:       ActionDeny,
		FromSelector: makeSelector("g", "x"),
		ToSelector:   makeSelector("g", "y"),
		Ports:        nil, // all ports
	}

	engine, err := NewPolicyEngine([]*NetworkPolicy{policy}, []*Node{nodeA, nodeB})
	require.NoError(t, err)

	rs, err := engine.GenerateEnforcementRuleset("node-a")
	require.NoError(t, err)

	assert.Contains(t, rs.Ruleset, "port any",
		"empty Ports list must produce 'port any' in generated rule")
}

// ---- TestGenerateEnforcementRuleset_HeaderComment --------------------------

// TestGenerateEnforcementRuleset_HeaderComment proves the CLAUDE-2 boundary
// justification comment appears in every generated ruleset.
//
// §1.1 mutation: remove the header block → Contains("CLAUDE-2") fails.
func TestGenerateEnforcementRuleset_HeaderComment(t *testing.T) {
	nodeA := makeNode("node-a", "10.0.0.1")
	nodeB := makeNode("node-b", "10.0.0.2")
	engine, err := NewPolicyEngine(nil, []*Node{nodeA, nodeB})
	require.NoError(t, err)

	rs, err := engine.GenerateEnforcementRuleset("node-a")
	require.NoError(t, err)

	assert.True(t,
		strings.Contains(rs.Ruleset, "CLAUDE-2"),
		"generated ruleset must contain CLAUDE-2 boundary comment")
	assert.True(t,
		strings.Contains(rs.Ruleset, "node-a"),
		"generated ruleset must identify the target node")
}

// ---- TestDecide_MultiPolicyDecisionTable ------------------------------------

// TestDecide_MultiPolicyDecisionTable drives a TABLE of (src,dst,port)->decision
// against a multi-policy engine to prove the full combinatorial decision logic.
//
// This is the most comprehensive SINK-SIDE test. Every row is a concrete
// (srcID, dstID, port, expected) quadruple.
func TestDecide_MultiPolicyDecisionTable(t *testing.T) {
	nodes := []*Node{
		makeNode("node-web", "10.0.0.1", "tier", "web", "env", "prod"),
		makeNode("node-db", "10.0.0.2", "tier", "db", "env", "prod"),
		makeNode("node-cache", "10.0.0.3", "tier", "cache", "env", "prod"),
		makeNode("node-dev", "10.0.0.4", "tier", "web", "env", "dev"),
	}

	policies := []*NetworkPolicy{
		{
			// DENY: web -> db on postgres port
			Name:         "deny-web-db-5432",
			Action:       ActionDeny,
			FromSelector: makeSelector("tier", "web"),
			ToSelector:   makeSelector("tier", "db"),
			Ports:        []uint16{5432},
		},
		{
			// ALLOW: web -> cache on redis port
			Name:         "allow-web-cache-6379",
			Action:       ActionAllow,
			FromSelector: makeSelector("tier", "web"),
			ToSelector:   makeSelector("tier", "cache"),
			Ports:        []uint16{6379},
		},
		{
			// DENY: dev tier entirely denied to db on all ports
			Name:         "deny-dev-to-db-all",
			Action:       ActionDeny,
			FromSelector: makeSelector("env", "dev"),
			ToSelector:   makeSelector("tier", "db"),
			Ports:        nil, // all ports
		},
	}

	engine, err := NewPolicyEngine(policies, nodes)
	require.NoError(t, err)

	type row struct {
		src, dst string
		port     uint16
		want     Action
		desc     string
	}

	table := []row{
		// web -> db:5432 → DENY (policy deny-web-db-5432)
		{"node-web", "node-db", 5432, ActionDeny, "web->db:5432 DENY"},
		// web -> db:22   → ALLOW (no matching policy; deny-web-db-5432 not for port 22)
		// BUT deny-dev-to-db-all doesn't match node-web (env=prod not dev) → ALLOW
		{"node-web", "node-db", 22, ActionAllow, "web->db:22 ALLOW"},
		// web -> cache:6379 → ALLOW (allow-web-cache-6379 matches)
		{"node-web", "node-cache", 6379, ActionAllow, "web->cache:6379 ALLOW"},
		// web -> cache:5432 → ALLOW (no deny matches)
		{"node-web", "node-cache", 5432, ActionAllow, "web->cache:5432 ALLOW"},
		// db -> web:80   → ALLOW (no policy matches this direction)
		{"node-db", "node-web", 80, ActionAllow, "db->web:80 ALLOW"},
		// dev -> db:5432 → DENY (deny-dev-to-db-all all ports)
		{"node-dev", "node-db", 5432, ActionDeny, "dev->db:5432 DENY (all-port deny)"},
		// dev -> db:80   → DENY (deny-dev-to-db-all all ports)
		{"node-dev", "node-db", 80, ActionDeny, "dev->db:80 DENY (all-port deny)"},
		// dev -> cache   → ALLOW (no deny matches)
		{"node-dev", "node-cache", 6379, ActionAllow, "dev->cache:6379 ALLOW"},
		// web -> web (self→self) → ALLOW (no policy matches tier=web to tier=web for non-listed ports)
		{"node-web", "node-dev", 443, ActionAllow, "web->dev:443 ALLOW (tier=web->tier=web, no match)"},
	}

	for _, row := range table {
		t.Run(row.desc, func(t *testing.T) {
			got, err := engine.Decide(row.src, row.dst, row.port)
			require.NoError(t, err)
			assert.Equal(t, row.want, got, "decision for (%s->%s:%d)", row.src, row.dst, row.port)
		})
	}
}
