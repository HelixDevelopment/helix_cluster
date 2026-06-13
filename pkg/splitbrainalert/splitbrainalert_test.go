package splitbrainalert_test

import (
	"strings"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/splitbrainalert"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// findRule returns the first AlertRule in rules whose Name matches name, and
// a boolean indicating whether it was found.
func findRule(rules []splitbrainalert.AlertRule, name string) (splitbrainalert.AlertRule, bool) {
	for _, r := range rules {
		if r.Name == name {
			return r, true
		}
	}
	return splitbrainalert.AlertRule{}, false
}

// ---------------------------------------------------------------------------
// 1. DefaultSplitBrainRules: all rules pass Validate; required conditions covered.
// ---------------------------------------------------------------------------

// TestDefaultSplitBrainRules_AllValidate confirms that every rule returned by
// DefaultSplitBrainRules passes Validate() and that the three required
// split-brain conditions are present by name.
//
// Mutation that breaks this test: make Validate always return nil —
// the deliberately-broken-rule tests in TestValidate_BrokenRules will still
// catch that because they assert specific error messages, not just err != nil.
// Additionally, removing any required Name from DefaultSplitBrainRules breaks
// the coverage assertions below, regardless of Validate.
func TestDefaultSplitBrainRules_AllValidate(t *testing.T) {
	rules := splitbrainalert.DefaultSplitBrainRules()

	if len(rules) < 3 {
		t.Fatalf("expected >= 3 rules, got %d", len(rules))
	}

	// Every rule must pass Validate.
	for i, r := range rules {
		if err := r.Validate(); err != nil {
			t.Errorf("rules[%d] (%q) failed Validate: %v", i, r.Name, err)
		}
	}

	// Required condition coverage by name.
	required := []string{"HelixQuorumLoss", "HelixTotalIsolation", "HelixPartitionTie"}
	for _, name := range required {
		if _, ok := findRule(rules, name); !ok {
			t.Errorf("DefaultSplitBrainRules() is missing required rule %q", name)
		}
	}
}

// TestDefaultSplitBrainRules_QuorumLossExpr checks that the HelixQuorumLoss
// rule uses the correct split-brain arithmetic from pkg/splitbrain.Classify:
// reachable*2 < total (minority condition).
//
// Mutation that breaks this test: change the Expr to omit "*2" or change "<" to "<=".
func TestDefaultSplitBrainRules_QuorumLossExpr(t *testing.T) {
	rules := splitbrainalert.DefaultSplitBrainRules()
	r, ok := findRule(rules, "HelixQuorumLoss")
	if !ok {
		t.Fatal("HelixQuorumLoss rule not found")
	}
	const wantExpr = "helix_members_reachable * 2 < helix_members_total"
	if r.Expr != wantExpr {
		t.Errorf("HelixQuorumLoss.Expr = %q, want %q", r.Expr, wantExpr)
	}
}

// TestDefaultSplitBrainRules_TotalIsolationExpr checks that the
// HelixTotalIsolation rule fires exactly when reachable == 0.
//
// Mutation that breaks this test: change Expr to "helix_members_reachable < 1".
func TestDefaultSplitBrainRules_TotalIsolationExpr(t *testing.T) {
	rules := splitbrainalert.DefaultSplitBrainRules()
	r, ok := findRule(rules, "HelixTotalIsolation")
	if !ok {
		t.Fatal("HelixTotalIsolation rule not found")
	}
	const wantExpr = "helix_members_reachable == 0"
	if r.Expr != wantExpr {
		t.Errorf("HelixTotalIsolation.Expr = %q, want %q", r.Expr, wantExpr)
	}
}

// TestDefaultSplitBrainRules_TieExpr checks that the HelixPartitionTie rule
// uses the exact tie arithmetic: reachable*2 == total.
//
// Mutation that breaks this test: change "==" to ">" or remove "*2".
func TestDefaultSplitBrainRules_TieExpr(t *testing.T) {
	rules := splitbrainalert.DefaultSplitBrainRules()
	r, ok := findRule(rules, "HelixPartitionTie")
	if !ok {
		t.Fatal("HelixPartitionTie rule not found")
	}
	const wantExpr = "helix_members_reachable * 2 == helix_members_total"
	if r.Expr != wantExpr {
		t.Errorf("HelixPartitionTie.Expr = %q, want %q", r.Expr, wantExpr)
	}
}

// TestDefaultSplitBrainRules_Severities asserts exact severity values: quorum-loss
// and total-isolation are critical; partition-tie is warning.
//
// Mutation that breaks this test: swap any severity value.
func TestDefaultSplitBrainRules_Severities(t *testing.T) {
	rules := splitbrainalert.DefaultSplitBrainRules()
	cases := []struct {
		name    string
		wantSev string
	}{
		{"HelixQuorumLoss", "critical"},
		{"HelixTotalIsolation", "critical"},
		{"HelixPartitionTie", "warning"},
	}
	for _, tc := range cases {
		r, ok := findRule(rules, tc.name)
		if !ok {
			t.Errorf("rule %q not found", tc.name)
			continue
		}
		if r.Severity != tc.wantSev {
			t.Errorf("rule %q: Severity = %q, want %q", tc.name, r.Severity, tc.wantSev)
		}
	}
}

// ---------------------------------------------------------------------------
// 2. Round-trip: Parse(Render(rules)) preserves count, Names, and Exprs.
// ---------------------------------------------------------------------------

// TestRoundTrip_CountNamesExprs proves that Render produces real parseable
// YAML and that Parse recovers the original rule identities.
//
// Mutation that breaks this test: hardcode Render's output ignoring input —
// the parsed names/exprs will not match the originals.
func TestRoundTrip_CountNamesExprs(t *testing.T) {
	original := splitbrainalert.DefaultSplitBrainRules()

	doc, err := splitbrainalert.Render(original)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	parsed, err := splitbrainalert.Parse(doc)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if len(parsed) != len(original) {
		t.Fatalf("round-trip count mismatch: got %d, want %d", len(parsed), len(original))
	}

	for i, orig := range original {
		got := parsed[i]
		if got.Name != orig.Name {
			t.Errorf("rules[%d].Name round-trip: got %q, want %q", i, got.Name, orig.Name)
		}
		if got.Expr != orig.Expr {
			t.Errorf("rules[%d].Expr round-trip: got %q, want %q", i, got.Expr, orig.Expr)
		}
		if got.For != orig.For {
			t.Errorf("rules[%d].For round-trip: got %q, want %q", i, got.For, orig.For)
		}
		if got.Severity != orig.Severity {
			t.Errorf("rules[%d].Severity round-trip: got %q, want %q", i, got.Severity, orig.Severity)
		}
		if got.RunbookURL != orig.RunbookURL {
			t.Errorf("rules[%d].RunbookURL round-trip: got %q, want %q", i, got.RunbookURL, orig.RunbookURL)
		}
	}
}

// TestRoundTrip_CustomRules verifies round-trip with a non-default rule set so
// Render cannot cheat by returning a hardcoded default document.
//
// Mutation that breaks this test: ignore the input slice in Render and always
// emit DefaultSplitBrainRules — the custom name "CustomAlert" will be absent.
func TestRoundTrip_CustomRules(t *testing.T) {
	custom := []splitbrainalert.AlertRule{
		{
			Name:       "CustomAlert",
			Expr:       "helix_members_reachable * 2 < helix_members_total",
			For:        "3m",
			Severity:   "warning",
			RunbookURL: "https://example.com/runbooks/custom",
			Summary:    "Custom split-brain alert for testing.",
		},
	}

	doc, err := splitbrainalert.Render(custom)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	parsed, err := splitbrainalert.Parse(doc)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if len(parsed) != 1 {
		t.Fatalf("expected 1 parsed rule, got %d", len(parsed))
	}
	if parsed[0].Name != "CustomAlert" {
		t.Errorf("round-trip Name: got %q, want %q", parsed[0].Name, "CustomAlert")
	}
	if parsed[0].Expr != custom[0].Expr {
		t.Errorf("round-trip Expr: got %q, want %q", parsed[0].Expr, custom[0].Expr)
	}
}

// ---------------------------------------------------------------------------
// 3. Render sink-side: rendered YAML contains annotations.runbook_url for each rule.
// ---------------------------------------------------------------------------

// TestRender_ContainsRunbookURL is a sink-side test: it checks that the
// rendered YAML document physically contains the string "runbook_url" for every
// default rule, proving the annotation was written to the output and not merely
// stored in memory.
//
// Mutation that breaks this test: omit annotations.runbook_url from promRule
// or use a different key name.
func TestRender_ContainsRunbookURL(t *testing.T) {
	rules := splitbrainalert.DefaultSplitBrainRules()
	doc, err := splitbrainalert.Render(rules)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	content := string(doc)

	// The key must appear.
	if !strings.Contains(content, "runbook_url") {
		t.Error("rendered YAML does not contain the key 'runbook_url'")
	}

	// Each rule's RunbookURL value must appear in the document (sink-side).
	for _, r := range rules {
		if !strings.Contains(content, r.RunbookURL) {
			t.Errorf("rendered YAML does not contain RunbookURL %q for rule %q", r.RunbookURL, r.Name)
		}
	}
}

// TestRender_ContainsSummary is a sink-side test that proves annotations.summary
// is written into the rendered YAML and that the actual Summary *value* (not
// merely the key name) is present for every rule.
//
// Mutation that breaks this test: omit annotations.summary from promRule, or
// render an empty summary value — either causes the r.Summary substring check
// to fail for at least one rule.
func TestRender_ContainsSummary(t *testing.T) {
	rules := splitbrainalert.DefaultSplitBrainRules()
	doc, err := splitbrainalert.Render(rules)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	content := string(doc)

	// The "summary" key must appear at least once.
	if !strings.Contains(content, "summary") {
		t.Error("rendered YAML does not contain the 'summary' key")
	}

	// Sink-side: the actual Summary text value must appear in the document for
	// every rule. This is the real proof that Render wrote meaningful annotation
	// content, not just an empty "summary:" key.
	for _, r := range rules {
		if !strings.Contains(content, r.Summary) {
			t.Errorf("rendered YAML does not contain the Summary value %q for rule %q", r.Summary, r.Name)
		}
	}
}

// TestRender_GroupName checks that the output uses the expected group name,
// proving the document is structured as a rule-group file.
//
// Mutation that breaks this test: use a different groupName constant.
func TestRender_GroupName(t *testing.T) {
	doc, err := splitbrainalert.Render(splitbrainalert.DefaultSplitBrainRules())
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	if !strings.Contains(string(doc), "helix_split_brain") {
		t.Errorf("rendered YAML does not contain expected group name 'helix_split_brain'")
	}
}

// ---------------------------------------------------------------------------
// 4. Validate: broken rules each produce a distinct, specific error.
// ---------------------------------------------------------------------------

// TestValidate_BrokenRules verifies that deliberately broken AlertRule values
// fail Validate with errors that identify the specific invalid field. Each
// sub-test targets a different field so that dropping one validation path
// causes exactly that sub-test to fail.
func TestValidate_BrokenRules(t *testing.T) {
	// baseOK is a valid rule used as the starting point for mutations.
	baseOK := splitbrainalert.AlertRule{
		Name:       "HelixTest",
		Expr:       "helix_members_reachable == 0",
		For:        "1m",
		Severity:   "critical",
		RunbookURL: "https://docs.helixcluster.io/runbooks/test",
		Summary:    "Test alert.",
	}

	// Sanity: the base must be valid.
	if err := baseOK.Validate(); err != nil {
		t.Fatalf("base rule is unexpectedly invalid: %v", err)
	}

	tests := []struct {
		name        string
		mutate      func(r splitbrainalert.AlertRule) splitbrainalert.AlertRule
		wantErrFrag string // substring that MUST appear in the error message
	}{
		{
			// Mutation that keeps this test meaningful: remove the Name check from Validate.
			name: "empty Name",
			mutate: func(r splitbrainalert.AlertRule) splitbrainalert.AlertRule {
				r.Name = ""
				return r
			},
			wantErrFrag: "Name must be non-empty",
		},
		{
			// Mutation that keeps this test meaningful: remove the Expr check from Validate.
			name: "empty Expr",
			mutate: func(r splitbrainalert.AlertRule) splitbrainalert.AlertRule {
				r.Expr = ""
				return r
			},
			wantErrFrag: "Expr must be non-empty",
		},
		{
			// Mutation that keeps this test meaningful: remove the For parse check from Validate.
			name: "unparseable For",
			mutate: func(r splitbrainalert.AlertRule) splitbrainalert.AlertRule {
				r.For = "zzz"
				return r
			},
			wantErrFrag: "not a valid duration",
		},
		{
			// Mutation that keeps this test meaningful: remove the d <= 0 guard from
			// Validate — time.ParseDuration("0s") returns (0, nil) so the parse check
			// alone passes, and zero-duration rules incorrectly validate as OK.
			name: "zero For duration",
			mutate: func(r splitbrainalert.AlertRule) splitbrainalert.AlertRule {
				r.For = "0s"
				return r
			},
			wantErrFrag: "positive duration",
		},
		{
			// Mutation that keeps this test meaningful: accept "info" as a valid severity.
			name: "invalid Severity info",
			mutate: func(r splitbrainalert.AlertRule) splitbrainalert.AlertRule {
				r.Severity = "info"
				return r
			},
			wantErrFrag: "Severity",
		},
		{
			// Mutation that keeps this test meaningful: remove the Summary non-empty check
			// from Validate — an empty Summary incorrectly validates as OK.
			name: "empty Summary",
			mutate: func(r splitbrainalert.AlertRule) splitbrainalert.AlertRule {
				r.Summary = ""
				return r
			},
			wantErrFrag: "Summary must be non-empty",
		},
		{
			// Mutation that keeps this test meaningful: drop the RunbookURL scheme+host check.
			// If that check is dropped, "noscheme" parses successfully (url.Parse never errors
			// on a bare relative reference) and Validate returns nil — the test fails.
			name: "RunbookURL with no scheme",
			mutate: func(r splitbrainalert.AlertRule) splitbrainalert.AlertRule {
				r.RunbookURL = "noscheme"
				return r
			},
			wantErrFrag: "scheme and host",
		},
		{
			// Mutation that keeps this test meaningful: remove the Host check from Validate.
			name: "RunbookURL scheme-only no host",
			mutate: func(r splitbrainalert.AlertRule) splitbrainalert.AlertRule {
				r.RunbookURL = "https://"
				return r
			},
			wantErrFrag: "scheme and host",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			broken := tc.mutate(baseOK)
			err := broken.Validate()
			if err == nil {
				t.Fatalf("Validate() returned nil for broken rule (%s), want error containing %q", tc.name, tc.wantErrFrag)
			}
			if !strings.Contains(err.Error(), tc.wantErrFrag) {
				t.Errorf("Validate() error = %q, want it to contain %q", err.Error(), tc.wantErrFrag)
			}
		})
	}
}

// TestValidate_ValidRule confirms the base valid rule passes, distinct from the
// mutation tests so a trivially-returning Validate is caught by the broken
// tests above.
//
// Mutation that breaks this test: make Validate always return a non-nil error.
func TestValidate_ValidRule(t *testing.T) {
	r := splitbrainalert.AlertRule{
		Name:       "HelixValid",
		Expr:       "helix_members_reachable * 2 < helix_members_total",
		For:        "2m",
		Severity:   "critical",
		RunbookURL: "https://docs.helixcluster.io/runbooks/split-brain/quorum-loss",
		Summary:    "Valid rule.",
	}
	if err := r.Validate(); err != nil {
		t.Errorf("valid rule failed Validate: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 5. Render rejects invalid rules (does not silently emit broken YAML).
// ---------------------------------------------------------------------------

// TestRender_RejectsInvalidRule proves that Render validates its input and
// returns an error rather than emitting a broken YAML document.
//
// Mutation that breaks this test: remove the Validate call inside Render.
func TestRender_RejectsInvalidRule(t *testing.T) {
	bad := []splitbrainalert.AlertRule{
		{
			Name:       "BadRule",
			Expr:       "", // invalid: empty Expr
			For:        "1m",
			Severity:   "critical",
			RunbookURL: "https://docs.helixcluster.io/runbooks/bad",
			Summary:    "Bad rule.",
		},
	}
	_, err := splitbrainalert.Render(bad)
	if err == nil {
		t.Error("Render should have returned an error for a rule with empty Expr, got nil")
	}
}

// ---------------------------------------------------------------------------
// 6. Parse rejects malformed YAML.
// ---------------------------------------------------------------------------

// TestParse_MalformedYAML verifies that Parse returns an error on non-YAML input.
//
// Mutation that breaks this test: return nil error from Parse unconditionally.
func TestParse_MalformedYAML(t *testing.T) {
	_, err := splitbrainalert.Parse([]byte(":::not yaml:::"))
	if err == nil {
		t.Error("Parse should return an error on malformed YAML, got nil")
	}
}

// TestParse_EmptyGroups verifies that Parse returns an error when the document
// contains no rule groups.
//
// Mutation that breaks this test: return an empty slice instead of an error for
// empty groups.
func TestParse_EmptyGroups(t *testing.T) {
	// Valid YAML with an empty groups list.
	doc := []byte("groups: []\n")
	_, err := splitbrainalert.Parse(doc)
	if err == nil {
		t.Error("Parse should return an error when groups list is empty, got nil")
	}
	if !strings.Contains(err.Error(), "no rule groups") {
		t.Errorf("unexpected error text: %v", err)
	}
}
