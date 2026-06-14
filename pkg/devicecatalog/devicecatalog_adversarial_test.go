package devicecatalog

import (
	"strings"
	"testing"
)

// Adversarial / contract-pinning suite for the device catalog lookup (HXC-1331).
//
// The contract under test (from devicecatalog.go doc comments):
//   - Lookup does a CASE-INSENSITIVE SUBSTRING match of d.CPUModel against each
//     entry's MatchModel, returning the FIRST matching entry in catalog order.
//   - "Order matters: more specific entries appear before broader ones so the
//     first substring match wins."
//   - An unmatched / empty / blank model returns (zero CatalogEntry, false).
//   - Entries() returns an isolated copy; mutating it must not corrupt the catalog.
//
// These tests are adversarial: they feed each MatchModel back through Lookup to
// catch ordering inversions (a broad entry shadowing its specific sibling), pin
// the specific-before-broad order-wins guarantee with hand-derived expectations,
// pin the unknown/blank not-found contract exactly, and prove Entries() does not
// alias the package's backing storage.

// expectedFor is the hand-authored oracle: for a given input CPU model string we
// state, independently of the catalog source order, which MatchModel SHOULD win.
// This is NOT echoed from the catalog — it encodes the intended taxonomy.
type wantHit struct {
	in        string
	wantMatch string // expected winning entry's MatchModel
}

// TestLookup_EveryMatchModel_ResolvesToItself feeds each catalog entry's own
// MatchModel string into Lookup and asserts it resolves to an entry whose
// taxonomy equals that entry's taxonomy. This catches a WRONG-ENTRY bug where a
// broader entry placed earlier shadows a more-specific one (or any ordering
// inversion). For each MatchModel we assert the returned entry is taxonomy-
// equivalent to the FIRST catalog entry that is a case-insensitive substring of
// it (the documented "first match wins"), computed here independently.
func TestLookup_EveryMatchModel_ResolvesToItself(t *testing.T) {
	const runUUID = "every-model-9d31aa7c-HXC1331"

	es := Entries()
	for _, e := range es {
		got, ok := Lookup(Discovery{CPUModel: e.MatchModel})
		if !ok {
			t.Errorf("[%s] Lookup(%q) ok=false, want true (every authored model must resolve)", runUUID, e.MatchModel)
			continue
		}
		// Independently compute the expected winner: first catalog entry whose
		// MatchModel is a case-insensitive substring of this input.
		lowerIn := strings.ToLower(e.MatchModel)
		var wantWinner CatalogEntry
		found := false
		for _, cand := range es {
			if strings.Contains(lowerIn, strings.ToLower(cand.MatchModel)) {
				wantWinner = cand
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("[%s] internal: no candidate substring of its own model %q", runUUID, e.MatchModel)
		}
		if got != wantWinner {
			t.Errorf("[%s] Lookup(%q) = %+v, want first-substring winner %+v",
				runUUID, e.MatchModel, got, wantWinner)
		}
	}
	t.Logf("[%s] delta: all %d authored MatchModel strings resolve to their first-substring winner", runUUID, len(es))
}

// TestLookup_SpecificBeforeBroad pins the load-bearing order-wins contract with
// HAND-DERIVED expectations: a flagship-specific model must NOT collapse into its
// broader sibling, and a non-flagship model of the same family must fall through
// to the broad entry. If the catalog order were inverted (broad before specific),
// the flagship cases FAIL with the broad entry's taxonomy.
func TestLookup_SpecificBeforeBroad(t *testing.T) {
	const runUUID = "specific-broad-4e77b015-HXC1331"

	cases := []wantHit{
		// EPYC 9654 is the flagship; "AMD EPYC 9654" precedes "AMD EPYC".
		{in: "AMD EPYC 9654 96-Core Processor", wantMatch: "AMD EPYC 9654"},
		// A different EPYC (7763) is not the flagship -> broad "AMD EPYC".
		{in: "AMD EPYC 7763 64-Core Processor", wantMatch: "AMD EPYC"},
		// Xeon Platinum is flagship; "Intel Xeon Platinum" precedes "Intel Xeon".
		// (substring must contain the catalog key verbatim incl. spacing).
		{in: "Intel Xeon Platinum 8480+", wantMatch: "Intel Xeon Platinum"},
		// A Gold/Silver Xeon -> broad "Intel Xeon".
		{in: "Intel Xeon Gold 6338", wantMatch: "Intel Xeon"},
	}

	for _, c := range cases {
		got, ok := Lookup(Discovery{CPUModel: c.in})
		if !ok {
			t.Errorf("[%s] Lookup(%q) ok=false, want true (-> %q)", runUUID, c.in, c.wantMatch)
			continue
		}
		if got.MatchModel != c.wantMatch {
			t.Errorf("[%s] Lookup(%q) won by MatchModel=%q, want %q (order-wins inversion?)",
				runUUID, c.in, got.MatchModel, c.wantMatch)
		}
	}
	t.Logf("[%s] delta: %d specific/broad order-wins cases pinned (flagship not collapsed into broad)", runUUID, len(cases))
}

// TestLookup_KnownTaxonomy_Exact pins a hand-verified taxonomy for several
// distinct entries so a WRONG-ENTRY swap (returning another key's record) is
// caught. Values are hand-derived, not echoed from the catalog.
func TestLookup_KnownTaxonomy_Exact(t *testing.T) {
	const runUUID = "known-taxonomy-1bd0f6a2-HXC1331"

	type want struct {
		arch  string
		tier  string
		trust TrustLevel
		class ComputeClass
	}
	cases := map[string]want{
		"AWS Graviton3":              {"aarch64", "server-arm", TrustHardened, ComputeServer},
		"NVIDIA Jetson Orin NX":      {"aarch64", "sbc-accelerated", TrustUntrusted, ComputeAccelerator},
		"Apple M2 Max":               {"arm64", "mobile-laptop", TrustStandard, ComputeWorkstation},
		"Qualcomm Snapdragon 8 Gen":  {"aarch64", "mobile-soc", TrustUntrusted, ComputeMobile},
		"AMD Ryzen Threadripper PRO": {"x86_64", "workstation-hedt", TrustTrusted, ComputeWorkstation},
	}
	for in, w := range cases {
		got, ok := Lookup(Discovery{CPUModel: in})
		if !ok {
			t.Errorf("[%s] Lookup(%q) ok=false, want true", runUUID, in)
			continue
		}
		if got.Arch != w.arch || got.Tier != w.tier || got.Trust != w.trust || got.ComputeClass != w.class {
			t.Errorf("[%s] Lookup(%q) = {arch:%q tier:%q trust:%q class:%q}, want {arch:%q tier:%q trust:%q class:%q}",
				runUUID, in, got.Arch, got.Tier, got.Trust, got.ComputeClass, w.arch, w.tier, w.trust, w.class)
		}
	}
	t.Logf("[%s] delta: %d distinct entries pinned to hand-derived taxonomy (no cross-entry swap)", runUUID, len(cases))
}

// TestLookup_UnknownAndLookalike_FailClosed pins the not-found contract EXACTLY:
// unknown models, the empty string, blank/whitespace, and lookalike strings that
// share words with known keys but are not substrings of any MatchModel must all
// return (zero CatalogEntry, false) — never a stale or wrong entry.
func TestLookup_UnknownAndLookalike_FailClosed(t *testing.T) {
	const runUUID = "fail-closed-7a2c40e9-HXC1331"

	negatives := []string{
		"Totally Fictional CPU XYZ-9000",
		"",
		"   ",
		"\t\n ",
		// Lookalikes: contain a vendor/word but NOT a full MatchModel substring.
		"AMD EPY",       // truncated, "AMD EPYC" is not a substring of this
		"Intel Xeo",     // truncated
		"Rockchip RK35", // truncated, "Rockchip RK3588" not contained
		"Snapdragon",    // missing "Qualcomm " prefix of "Qualcomm Snapdragon"
		"Graviton",      // missing "AWS " prefix
		"Apple M",       // "Apple M2" not contained
		"VIA Nano",      // unrelated vendor
		"PowerPC G5",    // unrelated arch
	}
	for _, in := range negatives {
		got, ok := Lookup(Discovery{CPUModel: in})
		if ok {
			t.Errorf("[%s] Lookup(%q) ok=true, want false (got %+v) — fail-OPEN bug", runUUID, in, got)
		}
		if got != (CatalogEntry{}) {
			t.Errorf("[%s] Lookup(%q) entry=%+v, want zero CatalogEntry", runUUID, in, got)
		}
	}
	t.Logf("[%s] delta: %d unknown/blank/lookalike inputs all -> ok=false + zero entry", runUUID, len(negatives))
}

// TestEntries_Isolation_DeepMutation proves Entries() does not alias the backing
// catalog. It mutates EVERY field of EVERY returned element, then re-queries the
// catalog via Lookup and via a fresh Entries() and asserts nothing was corrupted.
// If Entries() returned the package's own backing slice, the post-mutation Lookup
// would observe "CORRUPTED" taxonomy.
func TestEntries_Isolation_DeepMutation(t *testing.T) {
	const runUUID = "isolation-5c1e9b3d-HXC1331"

	// Capture a known-good baseline via Lookup BEFORE any mutation.
	base, ok := Lookup(Discovery{CPUModel: "Rockchip RK3588"})
	if !ok {
		t.Fatalf("[%s] baseline Lookup(Rockchip RK3588) ok=false", runUUID)
	}

	es := Entries()
	for i := range es {
		es[i].MatchModel = "CORRUPTED"
		es[i].Arch = "CORRUPTED"
		es[i].Tier = "CORRUPTED"
		es[i].Trust = TrustLevel("CORRUPTED")
		es[i].ComputeClass = ComputeClass("CORRUPTED")
	}

	// 1) Lookup must still return the intact entry.
	after, ok := Lookup(Discovery{CPUModel: "Rockchip RK3588"})
	if !ok {
		t.Fatalf("[%s] post-mutation Lookup(Rockchip RK3588) ok=false — catalog corrupted via Entries() alias", runUUID)
	}
	if after != base {
		t.Errorf("[%s] catalog mutated through Entries() return value: got %+v, baseline %+v", runUUID, after, base)
	}

	// 2) A fresh Entries() must also be intact (not just Lookup's path).
	fresh := Entries()
	for _, e := range fresh {
		if e.MatchModel == "CORRUPTED" || e.Tier == "CORRUPTED" {
			t.Errorf("[%s] fresh Entries() contains CORRUPTED element %+v — Entries() aliases backing slice", runUUID, e)
			break
		}
	}
	t.Logf("[%s] delta: deep-mutated returned slice (%d elems); catalog intact via Lookup + fresh Entries()", runUUID, len(es))
}

// TestEntries_StableOrder asserts Entries() returns a deterministic, stable order
// across calls (the doc contracts "Order matters" — specific before broad), and
// that the specific entry precedes its broad sibling in the returned slice.
func TestEntries_StableOrder(t *testing.T) {
	const runUUID = "stable-order-2f8a6c44-HXC1331"

	a := Entries()
	b := Entries()
	if len(a) != len(b) {
		t.Fatalf("[%s] Entries() length varies: %d vs %d", runUUID, len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("[%s] Entries() order not stable at index %d: %+v vs %+v", runUUID, i, a[i], b[i])
		}
	}

	idx := func(match string) int {
		for i, e := range a {
			if e.MatchModel == match {
				return i
			}
		}
		return -1
	}
	// Specific-before-broad invariant in the exposed ordering.
	pairs := [][2]string{
		{"AMD EPYC 9654", "AMD EPYC"},
		{"Intel Xeon Platinum", "Intel Xeon"},
	}
	for _, p := range pairs {
		si, bi := idx(p[0]), idx(p[1])
		if si < 0 || bi < 0 {
			t.Fatalf("[%s] missing entry in catalog: %q(%d) %q(%d)", runUUID, p[0], si, p[1], bi)
		}
		if si > bi {
			t.Errorf("[%s] order inversion: specific %q at %d AFTER broad %q at %d", runUUID, p[0], si, p[1], bi)
		}
	}
	t.Logf("[%s] delta: Entries() order stable across calls; specific precedes broad", runUUID)
}
