package splitbrainalert_test

// Adversarial, mutation-verified audit of the split-brain alerting contract.
//
// This package emits *static* Prometheus rule strings; it never evaluates them
// against a live cluster. The safety-critical question is therefore: do the
// PromQL expressions, when evaluated against real (reachable,total) cluster
// states, fire EXACTLY on the dangerous states that pkg/splitbrain.Classify
// flags as non-quorum — and stay silent on a healthy quorum?
//
// The dominant failure we hunt is the FALSE NEGATIVE: a genuine split-brain /
// partition that the rules fail to flag (a missed alert in a distributed system
// is dangerous). To bite that, these tests embed a minimal evaluator for the
// exact comparison forms the rules use, then cross-check the union of the
// default rules against splitbrain.Classify (the independent oracle) across a
// full sweep of cluster sizes and the n/2 boundary.

import (
	"strings"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/splitbrain"
	"github.com/HelixDevelopment/helix_cluster/pkg/splitbrainalert"
)

// ---------------------------------------------------------------------------
// Minimal PromQL evaluator for the comparison forms used by the default rules.
//
// Supported metrics: helix_members_reachable, helix_members_total.
// Supported forms (whitespace-normalised):
//
//	A * 2 < B        A * 2 == B        A == 0
//
// where A/B are one of the two metric names or an integer literal. The evaluator
// is deliberately literal: it reads the rule's actual Expr string, so a mutation
// to the production Expr changes what is evaluated and the contract tests bite.
// ---------------------------------------------------------------------------

// evalExpr evaluates a supported PromQL comparison Expr for the given cluster
// state (reachable peers out of total members). It returns whether the alert
// fires (the comparison is true). It fails the test on an unsupported form so a
// silent mis-evaluation can never mask a missed alert.
func evalExpr(t *testing.T, expr string, reachable, total int) bool {
	t.Helper()

	// Resolve a token to an integer value in this cluster state.
	resolve := func(tok string) int {
		switch tok {
		case "helix_members_reachable":
			return reachable
		case "helix_members_total":
			return total
		case "0":
			return 0
		default:
			// Any other integer literal.
			n := 0
			neg := false
			s := tok
			if strings.HasPrefix(s, "-") {
				neg = true
				s = s[1:]
			}
			if s == "" {
				t.Fatalf("evalExpr: cannot resolve token %q in expr %q", tok, expr)
			}
			for _, c := range s {
				if c < '0' || c > '9' {
					t.Fatalf("evalExpr: cannot resolve token %q in expr %q", tok, expr)
				}
				n = n*10 + int(c-'0')
			}
			if neg {
				n = -n
			}
			return n
		}
	}

	fields := strings.Fields(expr)

	// Form: A == 0  (or any  X op Y  three-token comparison)
	if len(fields) == 3 {
		lhs := resolve(fields[0])
		op := fields[1]
		rhs := resolve(fields[2])
		return applyOp(t, expr, lhs, op, rhs)
	}

	// Form: A * 2 op B  (five tokens: A * 2 op B)
	if len(fields) == 5 && fields[1] == "*" {
		a := resolve(fields[0])
		mul := resolve(fields[2])
		lhs := a * mul
		op := fields[3]
		rhs := resolve(fields[4])
		return applyOp(t, expr, lhs, op, rhs)
	}

	t.Fatalf("evalExpr: unsupported expr form %q (fields=%v)", expr, fields)
	return false
}

func applyOp(t *testing.T, expr string, lhs int, op string, rhs int) bool {
	t.Helper()
	switch op {
	case "<":
		return lhs < rhs
	case "<=":
		return lhs <= rhs
	case ">":
		return lhs > rhs
	case ">=":
		return lhs >= rhs
	case "==":
		return lhs == rhs
	case "!=":
		return lhs != rhs
	default:
		t.Fatalf("evalExpr: unsupported operator %q in expr %q", op, expr)
		return false
	}
}

// anyDefaultRuleFires reports whether ANY default split-brain rule fires for the
// given cluster state, and returns the names of all firing rules. This models
// the operational reality: an operator is paged if any rule in the group fires.
func anyDefaultRuleFires(t *testing.T, reachable, total int) (bool, []string) {
	t.Helper()
	rules := splitbrainalert.DefaultSplitBrainRules()
	var fired []string
	for _, r := range rules {
		if evalExpr(t, r.Expr, reachable, total) {
			fired = append(fired, r.Name)
		}
	}
	return len(fired) > 0, fired
}

// ---------------------------------------------------------------------------
// Sanity: the evaluator itself agrees with hand-computed truths.
// If the evaluator were wrong, every detection test below would be worthless.
// ---------------------------------------------------------------------------

func TestEvaluator_SelfCheck(t *testing.T) {
	// 2*reachable < total : 1 of 3 -> 2 < 3 true ; 2 of 3 -> 4 < 3 false.
	if !evalExpr(t, "helix_members_reachable * 2 < helix_members_total", 1, 3) {
		t.Error("evaluator: 1-of-3 should satisfy 2r<total")
	}
	if evalExpr(t, "helix_members_reachable * 2 < helix_members_total", 2, 3) {
		t.Error("evaluator: 2-of-3 must NOT satisfy 2r<total")
	}
	// reachable == 0
	if !evalExpr(t, "helix_members_reachable == 0", 0, 5) {
		t.Error("evaluator: reachable==0 must be true at r=0")
	}
	if evalExpr(t, "helix_members_reachable == 0", 1, 5) {
		t.Error("evaluator: reachable==0 must be false at r=1")
	}
	// 2*reachable == total : 2 of 4 tie.
	if !evalExpr(t, "helix_members_reachable * 2 == helix_members_total", 2, 4) {
		t.Error("evaluator: 2-of-4 should satisfy tie 2r==total")
	}
}

// ---------------------------------------------------------------------------
// CENTERPIECE A — TRUE-POSITIVE DETECTION.
//
// For every cluster size and every reachable count, the union of the default
// rules MUST fire whenever splitbrain.Classify denies quorum (Minority, Tie
// without witness, TotalIsolation), and MUST NOT fire when Classify grants
// HasQuorum. splitbrain.Classify is the independent oracle.
//
// Mutation that breaks this test: relax/invert any rule condition (e.g. change
// HelixQuorumLoss "<" to "<=", drop "*2", or flip a comparison) so a genuine
// minority partition stops firing -> a real split-brain goes unflagged here.
// ---------------------------------------------------------------------------

func TestDetectionContract_MatchesClassifyOracle(t *testing.T) {
	const maxTotal = 12
	for total := 1; total <= maxTotal; total++ {
		for reachable := 0; reachable <= total; reachable++ {
			// Oracle: no witness — this is the dangerous, un-tiebroken view a
			// node has when it must decide locally whether to keep writing.
			part, action := splitbrain.Classify(reachable, total, false)

			fired, names := anyDefaultRuleFires(t, reachable, total)

			switch part {
			case splitbrain.PartitionHasQuorum:
				// Healthy majority: NO alert may fire (no false positive).
				if fired {
					t.Errorf("FALSE POSITIVE: reachable=%d total=%d is HasQuorum (%s) but rules fired %v",
						reachable, total, action, names)
				}
			case splitbrain.PartitionMinority,
				splitbrain.PartitionTie,
				splitbrain.PartitionTotalIsolation:
				// Dangerous non-quorum: at least one alert MUST fire.
				if !fired {
					t.Errorf("FALSE NEGATIVE (missed alert): reachable=%d total=%d is %s/%s "+
						"(non-quorum, writes must stop) but NO split-brain rule fired",
						reachable, total, part, action)
				}
			}
		}
	}
}

// TestDetectionContract_EachRuleHasARealTrigger constructs, for each default
// rule, a concrete cluster state that genuinely matches its Classify condition
// and asserts that specific rule fires there. This pins each rule to a real
// split-brain scenario, not just to a string.
func TestDetectionContract_EachRuleHasARealTrigger(t *testing.T) {
	rulesByName := func(name string) splitbrainalert.AlertRule {
		for _, r := range splitbrainalert.DefaultSplitBrainRules() {
			if r.Name == name {
				return r
			}
		}
		t.Fatalf("rule %q not found", name)
		return splitbrainalert.AlertRule{}
	}

	cases := []struct {
		ruleName         string
		reachable, total int
		wantPart         splitbrain.Partition
		scenario         string
	}{
		{
			ruleName:  "HelixQuorumLoss",
			reachable: 2, total: 5, // 4 < 5 : minority side believing nothing
			wantPart: splitbrain.PartitionMinority,
			scenario: "2-of-5 minority partition acting alone",
		},
		{
			ruleName:  "HelixTotalIsolation",
			reachable: 0, total: 7, // node cut off from all peers
			wantPart: splitbrain.PartitionTotalIsolation,
			scenario: "node isolated from all 6 peers",
		},
		{
			ruleName:  "HelixPartitionTie",
			reachable: 2, total: 4, // 4 == 4 : even split, each side has 2
			wantPart: splitbrain.PartitionTie,
			scenario: "even 2/2 split-brain, both sides equal",
		},
	}

	for _, tc := range cases {
		t.Run(tc.ruleName, func(t *testing.T) {
			// Oracle confirms the scenario really is the intended danger state.
			part, _ := splitbrain.Classify(tc.reachable, tc.total, false)
			if part != tc.wantPart {
				t.Fatalf("oracle mismatch: Classify(%d,%d) = %s, want %s (%s)",
					tc.reachable, tc.total, part, tc.wantPart, tc.scenario)
			}
			r := rulesByName(tc.ruleName)
			if !evalExpr(t, r.Expr, tc.reachable, tc.total) {
				t.Errorf("MISSED ALERT: rule %q did not fire for %s (reachable=%d total=%d); expr=%q",
					tc.ruleName, tc.scenario, tc.reachable, tc.total, r.Expr)
			}
		})
	}
}

// TestDetectionContract_TwoSidedSplitBrain models the classic dangerous case:
// a 5-node cluster splits 3|2. The minority (2) side must alert (quorum loss);
// the majority (3) side must NOT alert. If BOTH sides alerted, or NEITHER, the
// detector would be useless. Critically, the minority side firing is what stops
// a divergent second primary.
func TestDetectionContract_TwoSidedSplitBrain(t *testing.T) {
	const total = 5

	// Majority side: 3 of 5 reachable -> HasQuorum -> silent.
	majFired, majNames := anyDefaultRuleFires(t, 3, total)
	if majFired {
		t.Errorf("majority side (3 of 5) must stay silent but fired %v", majNames)
	}

	// Minority side: 2 of 5 reachable -> Minority -> MUST alert (quorum loss).
	minFired, minNames := anyDefaultRuleFires(t, 2, total)
	if !minFired {
		t.Error("FALSE NEGATIVE: minority side (2 of 5) did not fire any split-brain alert")
	}
	// And specifically the critical quorum-loss rule, not just any rule.
	if !contains(minNames, "HelixQuorumLoss") {
		t.Errorf("minority side fired %v but not HelixQuorumLoss (the critical write-stop rule)", minNames)
	}
}

// ---------------------------------------------------------------------------
// CENTERPIECE B — THRESHOLD / BOUNDARY around n/2.
//
// The most insidious bug is an off-by-one at the quorum boundary: alerting one
// member short, or one member late. We walk reachable from minority->majority
// for several totals and assert the firing flips at the exact mathematically
// correct point, cross-checked with Classify.
// ---------------------------------------------------------------------------

func TestBoundary_QuorumThresholdExact(t *testing.T) {
	type step struct {
		reachable     int
		wantFire      bool
		wantHasQuorum bool
	}
	cases := []struct {
		total int
		steps []step
	}{
		{
			// Even cluster of 4: 1->minority, 2->tie, 3->quorum.
			total: 4,
			steps: []step{
				{1, true, false}, // 2 < 4 minority -> fire
				{2, true, false}, // 4 == 4 tie     -> fire (warning)
				{3, false, true}, // 6 > 4 quorum   -> silent
			},
		},
		{
			// Odd cluster of 5: 2->minority, 3->quorum (no tie possible).
			total: 5,
			steps: []step{
				{2, true, false}, // 4 < 5 minority -> fire
				{3, false, true}, // 6 > 5 quorum   -> silent
			},
		},
		{
			// Even cluster of 6: 2->minority, 3->tie, 4->quorum.
			total: 6,
			steps: []step{
				{2, true, false}, // 4 < 6 minority -> fire
				{3, true, false}, // 6 == 6 tie     -> fire (warning)
				{4, false, true}, // 8 > 6 quorum   -> silent
			},
		},
	}

	for _, c := range cases {
		for _, s := range c.steps {
			part, _ := splitbrain.Classify(s.reachable, c.total, false)
			gotQuorum := part == splitbrain.PartitionHasQuorum
			if gotQuorum != s.wantHasQuorum {
				t.Errorf("oracle: Classify(%d,%d) quorum=%v, want %v", s.reachable, c.total, gotQuorum, s.wantHasQuorum)
			}
			fired, names := anyDefaultRuleFires(t, s.reachable, c.total)
			if fired != s.wantFire {
				t.Errorf("BOUNDARY off-by-one: reachable=%d total=%d -> fired=%v %v, want fired=%v (Classify=%s)",
					s.reachable, c.total, fired, names, s.wantFire, part)
			}
		}
	}
}

// TestBoundary_QuorumLossIsStrictLessThan pins HelixQuorumLoss to STRICT "<",
// not "<=". A "<=" mutation would make a healthy tie-breaking majority on an
// odd... no — more importantly it would make the exact-half tie also classified
// as quorum-loss-critical, AND on the next member up could falsely fire. The
// load-bearing assertion: at exactly half+epsilon (a true majority by one) the
// quorum-loss rule must NOT fire.
func TestBoundary_QuorumLossIsStrictLessThan(t *testing.T) {
	r := findRuleAdv(t, "HelixQuorumLoss")

	// 3 of 5: 2*3=6 > 5 -> strict majority. QuorumLoss must be SILENT.
	if evalExpr(t, r.Expr, 3, 5) {
		t.Error("HelixQuorumLoss fired on 3-of-5 (a real majority) — false positive / off-by-one (<= instead of <)")
	}
	// 2 of 5: 2*2=4 < 5 -> minority. QuorumLoss MUST fire.
	if !evalExpr(t, r.Expr, 2, 5) {
		t.Error("HelixQuorumLoss did NOT fire on 2-of-5 minority — missed quorum loss")
	}
	// Exact tie 2 of 4: 2*2=4 == 4 -> NOT strictly less -> QuorumLoss silent
	// (the tie rule handles this; quorum-loss must not double as the tie rule
	// via a relaxed <=).
	if evalExpr(t, r.Expr, 2, 4) {
		t.Error("HelixQuorumLoss fired on an exact tie (2-of-4) — Expr uses <= not strict <")
	}
}

// TestBoundary_TieIsExactEquality pins HelixPartitionTie to "==": it must fire
// at the exact half and NOWHERE else, so it neither masks a quorum (false
// silence elsewhere is fine, but firing on quorum is a false positive) nor
// over-fires.
func TestBoundary_TieIsExactEquality(t *testing.T) {
	r := findRuleAdv(t, "HelixPartitionTie")

	if !evalExpr(t, r.Expr, 2, 4) {
		t.Error("HelixPartitionTie did not fire on exact tie 2-of-4")
	}
	if evalExpr(t, r.Expr, 3, 5) {
		t.Error("HelixPartitionTie fired on 3-of-5 majority — false positive (not an exact tie)")
	}
	if evalExpr(t, r.Expr, 1, 5) {
		t.Error("HelixPartitionTie fired on 1-of-5 minority — false positive (not an exact tie)")
	}
}

// ---------------------------------------------------------------------------
// CENTERPIECE C — ROUND-TRIP integrity: no rule may be silently dropped,
// reordered, or corrupted by Render->Parse. A dropped rule is a permanent
// detection gap. We assert FULL fidelity of every field for every rule, in
// order, and that the count is preserved.
// ---------------------------------------------------------------------------

func TestRoundTrip_NoRuleDroppedOrCorrupted(t *testing.T) {
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
		t.Fatalf("RULE DROPPED: round-trip count %d != original %d", len(parsed), len(original))
	}

	for i := range original {
		o, g := original[i], parsed[i]
		if g.Name != o.Name || g.Expr != o.Expr || g.For != o.For ||
			g.Severity != o.Severity || g.RunbookURL != o.RunbookURL || g.Summary != o.Summary {
			t.Errorf("rule[%d] corrupted by round-trip:\n  orig=%+v\n  got =%+v", i, o, g)
		}
	}

	// Critically: every parsed rule must STILL fire on its danger state — proves
	// the round-trip preserved the detection-bearing Expr, not just the name.
	for _, r := range parsed {
		switch r.Name {
		case "HelixQuorumLoss":
			if !evalExpr(t, r.Expr, 2, 5) {
				t.Error("round-trip broke HelixQuorumLoss: no longer fires on 2-of-5 minority")
			}
		case "HelixTotalIsolation":
			if !evalExpr(t, r.Expr, 0, 5) {
				t.Error("round-trip broke HelixTotalIsolation: no longer fires on isolation")
			}
		case "HelixPartitionTie":
			if !evalExpr(t, r.Expr, 2, 4) {
				t.Error("round-trip broke HelixPartitionTie: no longer fires on exact tie")
			}
		}
	}
}

// TestRoundTrip_MultiRuleOrderPreserved uses a 3-rule custom set with distinct
// names to prove ordering is not scrambled (a reorder could silently swap a
// critical rule's severity onto the wrong condition).
func TestRoundTrip_MultiRuleOrderPreserved(t *testing.T) {
	custom := []splitbrainalert.AlertRule{
		{Name: "A_First", Expr: "helix_members_reachable == 0", For: "30s", Severity: "critical", RunbookURL: "https://x.test/a", Summary: "first"},
		{Name: "B_Second", Expr: "helix_members_reachable * 2 < helix_members_total", For: "2m", Severity: "critical", RunbookURL: "https://x.test/b", Summary: "second"},
		{Name: "C_Third", Expr: "helix_members_reachable * 2 == helix_members_total", For: "5m", Severity: "warning", RunbookURL: "https://x.test/c", Summary: "third"},
	}
	doc, err := splitbrainalert.Render(custom)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	parsed, err := splitbrainalert.Parse(doc)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(parsed) != 3 {
		t.Fatalf("expected 3 rules, got %d", len(parsed))
	}
	wantOrder := []string{"A_First", "B_Second", "C_Third"}
	for i, want := range wantOrder {
		if parsed[i].Name != want {
			t.Errorf("order scrambled at index %d: got %q want %q", i, parsed[i].Name, want)
		}
	}
}

// ---------------------------------------------------------------------------
// PARSE / RENDER robustness — malformed input must error, never yield a silent
// empty (and therefore alert-free) rule set.
// ---------------------------------------------------------------------------

// TestParse_DoesNotSilentlyReturnEmptyOnGarbage ensures a corrupt rules file is
// a hard error, not an empty rule slice that would silently disable ALL
// split-brain alerting.
func TestParse_DoesNotSilentlyReturnEmptyOnGarbage(t *testing.T) {
	garbageInputs := []string{
		"\t\tnot: [valid: yaml",
		"groups: \"this is a string not a list\"\n",
	}
	for _, g := range garbageInputs {
		out, err := splitbrainalert.Parse([]byte(g))
		if err == nil && len(out) == 0 {
			t.Errorf("Parse(%q) returned (empty, nil) — a silent alert-free rule set; want an error", g)
		}
	}
}

// TestParse_EmptyDocumentErrors ensures a wholly empty document is an error, not
// a silent empty rule set.
func TestParse_EmptyDocumentErrors(t *testing.T) {
	if _, err := splitbrainalert.Parse([]byte("")); err == nil {
		t.Error("Parse on empty document returned nil error; want 'no rule groups'")
	}
}

// ---------------------------------------------------------------------------
// EDGES — no panics on degenerate input.
// ---------------------------------------------------------------------------

// TestRender_EmptyRuleSet: rendering zero rules must not panic and must produce
// parseable YAML whose group has no rules (an honest empty group, not garbage).
func TestRender_EmptyRuleSet(t *testing.T) {
	doc, err := splitbrainalert.Render(nil)
	if err != nil {
		t.Fatalf("Render(nil) errored: %v", err)
	}
	parsed, err := splitbrainalert.Parse(doc)
	if err != nil {
		t.Fatalf("Parse of empty-ruleset render errored: %v", err)
	}
	if len(parsed) != 0 {
		t.Errorf("empty rule set round-tripped to %d rules, want 0", len(parsed))
	}
}

// TestEval_EmptyClusterStateNoPanic: a (0,0) cluster state is degenerate.
// Classify treats total<=0 as Unknown; the alert rules are still pure string
// comparisons and must evaluate without panic. We assert no panic and that
// isolation still fires on reachable==0 regardless of total.
func TestEval_DegenerateStateNoPanic(t *testing.T) {
	// total=0 -> Classify Unknown; we only assert the evaluator does not panic.
	_, _ = anyDefaultRuleFires(t, 0, 0)

	// reachable==0 with a positive total: isolation must fire.
	iso := findRuleAdv(t, "HelixTotalIsolation")
	if !evalExpr(t, iso.Expr, 0, 3) {
		t.Error("HelixTotalIsolation must fire at reachable=0,total=3")
	}
}

// TestSingleRule_RenderParse: a single-rule set survives render/parse intact.
func TestSingleRule_RenderParse(t *testing.T) {
	one := []splitbrainalert.AlertRule{{
		Name: "Solo", Expr: "helix_members_reachable == 0", For: "30s",
		Severity: "critical", RunbookURL: "https://x.test/solo", Summary: "solo",
	}}
	doc, err := splitbrainalert.Render(one)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	parsed, err := splitbrainalert.Parse(doc)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(parsed) != 1 || parsed[0].Name != "Solo" {
		t.Fatalf("single-rule round-trip lost the rule: %+v", parsed)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func findRuleAdv(t *testing.T, name string) splitbrainalert.AlertRule {
	t.Helper()
	for _, r := range splitbrainalert.DefaultSplitBrainRules() {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("rule %q not found in DefaultSplitBrainRules", name)
	return splitbrainalert.AlertRule{}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
