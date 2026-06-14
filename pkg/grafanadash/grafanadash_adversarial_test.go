package grafanadash_test

// Adversarial / sink-side test suite for pkg/grafanadash.
//
// These tests probe the ACTUAL generated JSON bytes (the sink), not merely
// "does it not panic". Bug classes probed:
//
//   (a) DETERMINISM      — the same spec rendered MANY times must be
//                          byte-identical (GitOps/diff stability). Doc claims:
//                          "calling this function twice produces identical JSON".
//   (b) JSON ESCAPING    — hostile panel titles / queries / tags containing
//                          quotes, backslashes, newlines, </script>, unicode
//                          must round-trip exactly and never corrupt the JSON.
//   (c) ID/UID UNIQUENESS — the dashboard UID is stable; panels are uniquely
//                          identifiable (here: by GridPos, since the model has
//                          no numeric panel id field). No two non-zero-area
//                          panels overlap.
//   (d) NUMERIC / LAYOUT — overlap detection is correct at the boundary
//                          (adjacent edges do NOT overlap; a 1-cell incursion
//                          DOES); huge / negative dimensions behave per the
//                          documented contract.
//
// Anti-bluff: every claim is backed by the actual []byte from Render() and an
// encoding/json round-trip, run across many iterations where determinism is at
// stake.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/grafanadash"
)

// renderOrFatal renders d and fails the test on error.
func renderOrFatal(t *testing.T, d grafanadash.Dashboard) []byte {
	t.Helper()
	b, err := d.Render()
	if err != nil {
		t.Fatalf("Render() error = %v, want nil", err)
	}
	return b
}

// --------------------------------------------------------------------------
// (a) DETERMINISM — both canonical builders, many iterations, byte-identical.
// --------------------------------------------------------------------------

func TestAdversarial_Determinism_GlobalHealth(t *testing.T) {
	t.Parallel()
	const iterations = 200

	want := renderOrFatal(t, grafanadash.BuildGlobalHealthDashboard())
	for i := 0; i < iterations; i++ {
		got := renderOrFatal(t, grafanadash.BuildGlobalHealthDashboard())
		if !bytes.Equal(want, got) {
			t.Fatalf("iteration %d: rendered JSON diverged.\n--- want ---\n%s\n--- got ---\n%s",
				i, want, got)
		}
	}
}

func TestAdversarial_Determinism_TierCost(t *testing.T) {
	t.Parallel()
	const iterations = 200

	want := renderOrFatal(t, grafanadash.BuildTierCostDashboard())
	for i := 0; i < iterations; i++ {
		got := renderOrFatal(t, grafanadash.BuildTierCostDashboard())
		if !bytes.Equal(want, got) {
			t.Fatalf("iteration %d: rendered JSON diverged.\n--- want ---\n%s\n--- got ---\n%s",
				i, want, got)
		}
	}
}

// TestAdversarial_Determinism_ManyTagsRoundTrip pins that a dashboard carrying
// many tags renders deterministically across iterations. Tags is a []string
// (ordered) not a map, so order must be stable — this would catch a regression
// to a map-backed tag set.
func TestAdversarial_Determinism_ManyTags(t *testing.T) {
	t.Parallel()
	const iterations = 100

	base := grafanadash.BuildTierCostDashboard()
	base.Tags = []string{"z", "a", "m", "helix", "b", "q", "cost", "gpu", "tier"}

	want := renderOrFatal(t, base)
	for i := 0; i < iterations; i++ {
		got := renderOrFatal(t, base)
		if !bytes.Equal(want, got) {
			t.Fatalf("iteration %d: tag order diverged.\n--- want ---\n%s\n--- got ---\n%s",
				i, want, got)
		}
	}
	// Tag order in the JSON must equal insertion order exactly.
	idxZ := bytes.Index(want, []byte(`"z"`))
	idxA := bytes.Index(want, []byte(`"a"`))
	if idxZ < 0 || idxA < 0 || idxZ > idxA {
		t.Fatalf("tag order not preserved: idx(z)=%d should precede idx(a)=%d in:\n%s",
			idxZ, idxA, want)
	}
}

// --------------------------------------------------------------------------
// (b) JSON ESCAPING / INJECTION — hostile strings must round-trip exactly and
// produce valid JSON. We assert SINK-SIDE: the bytes parse back to the exact
// original string, and the raw bytes never contain an unescaped break-out.
// --------------------------------------------------------------------------

func TestAdversarial_JSONEscaping_HostileStrings(t *testing.T) {
	t.Parallel()

	hostile := []string{
		`quote " inside`,
		`backslash \ inside`,
		"newline\ninside",
		"tab\tinside",
		`</script><script>alert(1)</script>`,
		`","uid":"injected","x":"`, // attempts to break out into a sibling key
		"unicode ☃ 🚀 é",
		"\x00null-byte",
		`{"nested":"json"}`,
		"control\r\nreturn",
	}

	for _, h := range hostile {
		d := grafanadash.Dashboard{
			Title:         h,
			UID:           "u-" + "x",
			SchemaVersion: 36,
			Tags:          []string{h, "helix"},
			Panels: []grafanadash.Panel{
				{
					Title: h,
					Type:  "stat",
					Targets: []grafanadash.Target{
						{Expr: "metric{label=" + h + "}", LegendFormat: h},
					},
					GridPos: grafanadash.GridPos{X: 0, Y: 0, W: 8, H: 4},
				},
			},
		}

		raw := renderOrFatal(t, d)

		// 1. Output must be valid JSON.
		if !json.Valid(raw) {
			t.Fatalf("hostile %q produced invalid JSON:\n%s", h, raw)
		}

		// 2. Round-trip via the package Parse must reproduce the exact strings.
		got, err := grafanadash.Parse(raw)
		if err != nil {
			t.Fatalf("Parse of hostile %q failed: %v\n%s", h, err, raw)
		}
		if got.Title != h {
			t.Errorf("Title round-trip mismatch for %q: got %q", h, got.Title)
		}
		if len(got.Panels) != 1 || got.Panels[0].Title != h {
			t.Errorf("panel Title round-trip mismatch for %q: got %+v", h, got.Panels)
		}
		if got.Panels[0].Targets[0].Expr != "metric{label="+h+"}" {
			t.Errorf("Expr round-trip mismatch for %q: got %q", h, got.Panels[0].Targets[0].Expr)
		}

		// 3. Injection probe: the breakout attempt must NOT have created a real
		//    second top-level "uid" key. The decoded UID must be exactly what we
		//    set, never "injected".
		if got.UID != "u-x" {
			t.Errorf("JSON injection succeeded for %q: UID = %q, want %q", h, got.UID, "u-x")
		}
	}
}

// TestAdversarial_QuoteInTitle_NoRawBreakout asserts the literal double-quote
// in a title is escaped in the byte stream (appears as \" not a bare ") so it
// cannot terminate the JSON string early.
func TestAdversarial_QuoteInTitle_NoRawBreakout(t *testing.T) {
	t.Parallel()

	d := grafanadash.BuildTierCostDashboard()
	d.Title = `pwn" , "uid" : "evil`

	raw := renderOrFatal(t, d)
	if !json.Valid(raw) {
		t.Fatalf("quote-injected title produced invalid JSON:\n%s", raw)
	}
	got, err := grafanadash.Parse(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if got.UID != "helix-tier-cost-v1" {
		t.Fatalf("UID was overwritten by injection: got %q", got.UID)
	}
	if got.Title != `pwn" , "uid" : "evil` {
		t.Fatalf("Title corrupted: got %q", got.Title)
	}
}

// --------------------------------------------------------------------------
// (c) UID / PANEL UNIQUENESS — UID stable; panels uniquely placed (no two
// non-zero-area panels share a grid cell). The model has no numeric panel id,
// so GridPos is the uniqueness key; we assert no collision in either builder.
// --------------------------------------------------------------------------

func TestAdversarial_PanelGridPosUniqueness(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		d    grafanadash.Dashboard
	}{
		{"global", grafanadash.BuildGlobalHealthDashboard()},
		{"tiercost", grafanadash.BuildTierCostDashboard()},
	} {
		// Validate must accept (no overlapping panels).
		if err := tc.d.Validate(); err != nil {
			t.Fatalf("%s: Validate() = %v, want nil", tc.name, err)
		}
		// No two panels share an identical GridPos (would visually collide).
		seen := map[grafanadash.GridPos]string{}
		for _, p := range tc.d.Panels {
			if prev, dup := seen[p.GridPos]; dup {
				t.Errorf("%s: panels %q and %q share identical GridPos %+v",
					tc.name, prev, p.Title, p.GridPos)
			}
			seen[p.GridPos] = p.Title
		}
	}
}

// --------------------------------------------------------------------------
// (d) NUMERIC / LAYOUT — overlap detection correctness at the boundary.
// --------------------------------------------------------------------------

// TestAdversarial_Overlap_AdjacentNotFlagged: two panels whose edges touch
// exactly (no shared cell) must NOT be reported as overlapping.
func TestAdversarial_Overlap_AdjacentNotFlagged(t *testing.T) {
	t.Parallel()
	d := grafanadash.Dashboard{
		Title: "adj", UID: "adj", SchemaVersion: 36,
		Panels: []grafanadash.Panel{
			{Title: "L", Type: "stat", Targets: []grafanadash.Target{{Expr: "x"}},
				GridPos: grafanadash.GridPos{X: 0, Y: 0, W: 12, H: 8}},
			{Title: "R", Type: "stat", Targets: []grafanadash.Target{{Expr: "x"}},
				GridPos: grafanadash.GridPos{X: 12, Y: 0, W: 12, H: 8}},
		},
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("adjacent (edge-touching) panels wrongly flagged as overlap: %v", err)
	}
}

// TestAdversarial_Overlap_OneCellIncursionFlagged: shifting the right panel one
// cell into the left panel's territory MUST be caught.
func TestAdversarial_Overlap_OneCellIncursionFlagged(t *testing.T) {
	t.Parallel()
	d := grafanadash.Dashboard{
		Title: "ov", UID: "ov", SchemaVersion: 36,
		Panels: []grafanadash.Panel{
			{Title: "L", Type: "stat", Targets: []grafanadash.Target{{Expr: "x"}},
				GridPos: grafanadash.GridPos{X: 0, Y: 0, W: 12, H: 8}},
			{Title: "R", Type: "stat", Targets: []grafanadash.Target{{Expr: "x"}},
				GridPos: grafanadash.GridPos{X: 11, Y: 0, W: 12, H: 8}}, // 1-cell incursion
		},
	}
	err := d.Validate()
	if err == nil {
		t.Fatalf("one-cell overlap NOT detected; Validate() = nil")
	}
	if !strings.Contains(err.Error(), "overlapping GridPos") {
		t.Fatalf("unexpected error text: %v", err)
	}
}

// TestAdversarial_Overlap_ZeroAreaSkipped pins the DOCUMENTED carve-out:
// zero-area panels (W==0 or H==0) are intentionally skipped by the overlap
// check. This is a contract, not a bug — pinned so a future change is forced
// to update the docs.
func TestAdversarial_Overlap_ZeroAreaSkipped(t *testing.T) {
	t.Parallel()
	d := grafanadash.Dashboard{
		Title: "zero", UID: "zero", SchemaVersion: 36,
		Panels: []grafanadash.Panel{
			{Title: "full", Type: "stat", Targets: []grafanadash.Target{{Expr: "x"}},
				GridPos: grafanadash.GridPos{X: 0, Y: 0, W: 12, H: 8}},
			// W==0 → zero area → sits "inside" full but is skipped by design.
			{Title: "zero-w", Type: "stat", Targets: []grafanadash.Target{{Expr: "x"}},
				GridPos: grafanadash.GridPos{X: 2, Y: 2, W: 0, H: 8}},
		},
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("zero-area panel was NOT skipped (contract drift): %v", err)
	}
}

// TestAdversarial_Numeric_HugeAndNegativeDimensions: large and negative ints
// must serialize as plain JSON numbers and round-trip exactly (no NaN/Inf path
// exists because GridPos is integer-only). Negative dims are NOT rejected by
// the documented Validate contract; we pin current behavior.
func TestAdversarial_Numeric_HugeAndNegativeDimensions(t *testing.T) {
	t.Parallel()
	d := grafanadash.Dashboard{
		Title: "num", UID: "num", SchemaVersion: 36,
		Panels: []grafanadash.Panel{
			{Title: "huge", Type: "stat", Targets: []grafanadash.Target{{Expr: "x"}},
				GridPos: grafanadash.GridPos{X: -2147483648, Y: 2147483647, W: 1, H: 1}},
		},
	}
	raw := renderOrFatal(t, d)
	if !json.Valid(raw) {
		t.Fatalf("huge/negative dims produced invalid JSON:\n%s", raw)
	}
	got, err := grafanadash.Parse(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	gp := got.Panels[0].GridPos
	if gp.X != -2147483648 || gp.Y != 2147483647 {
		t.Fatalf("numeric round-trip mismatch: got %+v", gp)
	}
}

// TestAdversarial_RenderParseRoundTrip_Canonical: the canonical dashboards
// survive a full Render→Parse→Render cycle byte-identically (idempotent),
// proving the serialized form is a stable fixed point for GitOps.
func TestAdversarial_RenderParseRoundTrip_Canonical(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		d    grafanadash.Dashboard
	}{
		{"global", grafanadash.BuildGlobalHealthDashboard()},
		{"tiercost", grafanadash.BuildTierCostDashboard()},
	} {
		first := renderOrFatal(t, tc.d)
		parsed, err := grafanadash.Parse(first)
		if err != nil {
			t.Fatalf("%s: Parse failed: %v", tc.name, err)
		}
		second := renderOrFatal(t, parsed)
		if !bytes.Equal(first, second) {
			t.Fatalf("%s: Render→Parse→Render not idempotent.\n--- first ---\n%s\n--- second ---\n%s",
				tc.name, first, second)
		}
	}
}
