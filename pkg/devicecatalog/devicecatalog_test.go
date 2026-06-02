package devicecatalog

import "testing"

// TestLookup_RK3588 is the LOAD-BEARING guard for HXC-1331.
//
// It pins the exact taxonomy data for the "Rockchip RK3588" ARM SBC entry:
// Tier="sbc-arm", Trust=TrustUntrusted, ComputeClass=ComputeEdge. If the RK3588
// entry's tier/trust/compute-class is mutated (e.g. ComputeEdge -> ComputeServer,
// or sbc-arm -> server), or if the substring match logic is broken, this test
// FAILS. Expected values are hand-derived from the device taxonomy, not echoed
// from the catalog.
func TestLookup_RK3588(t *testing.T) {
	const runUUID = "rk3588-guard-3f1c9a72-HXC1331"

	got, ok := Lookup(Discovery{CPUModel: "Rockchip RK3588"})
	if !ok {
		t.Fatalf("[%s] Lookup(Rockchip RK3588) ok=false, want true (entry must resolve)", runUUID)
	}

	// Hand-calculated expected taxonomy for the RK3588 ARM SBC.
	const (
		wantTier  = "sbc-arm"
		wantTrust = TrustUntrusted // "untrusted"
		wantClass = ComputeEdge    // "edge"
	)

	if got.Tier != wantTier {
		t.Errorf("[%s] Tier = %q, want %q", runUUID, got.Tier, wantTier)
	}
	if got.Trust != wantTrust {
		t.Errorf("[%s] Trust = %q, want %q", runUUID, got.Trust, wantTrust)
	}
	if got.ComputeClass != wantClass {
		t.Errorf("[%s] ComputeClass = %q, want %q", runUUID, got.ComputeClass, wantClass)
	}

	t.Logf("[%s] delta: discovery{CPUModel:%q} -> {Tier:%q, Trust:%q, ComputeClass:%q} (all three asserted exact)",
		runUUID, "Rockchip RK3588", got.Tier, got.Trust, got.ComputeClass)
}

// TestLookup_RK3588_Substring proves substring (not just exact) matching works:
// a realistic /proc/cpuinfo-style string containing the model still resolves to
// the same RK3588 entry. If match logic regressed to exact-only, this FAILS.
func TestLookup_RK3588_Substring(t *testing.T) {
	const runUUID = "rk3588-substr-8b22e1f4-HXC1331"

	in := "ARMv8 Processor Rockchip RK3588 rev 0 (aarch64)"
	got, ok := Lookup(Discovery{CPUModel: in})
	if !ok {
		t.Fatalf("[%s] Lookup(%q) ok=false, want true", runUUID, in)
	}
	if got.ComputeClass != ComputeEdge || got.Trust != TrustUntrusted {
		t.Errorf("[%s] got {Class:%q Trust:%q}, want {edge untrusted}", runUUID, got.ComputeClass, got.Trust)
	}
	t.Logf("[%s] delta: substring %q -> entry MatchModel=%q class=%q", runUUID, in, got.MatchModel, got.ComputeClass)
}

// TestLookup_Unknown asserts an unmatched model returns ok=false and the zero
// entry. If Lookup ever returned a default/non-zero entry for unknown input,
// this FAILS.
func TestLookup_Unknown(t *testing.T) {
	const runUUID = "unknown-7d4f0b91-HXC1331"

	got, ok := Lookup(Discovery{CPUModel: "Totally Fictional CPU XYZ-9000"})
	if ok {
		t.Fatalf("[%s] ok=true for unknown model, want false (got %+v)", runUUID, got)
	}
	if got != (CatalogEntry{}) {
		t.Errorf("[%s] entry = %+v, want zero CatalogEntry", runUUID, got)
	}
	t.Logf("[%s] delta: unknown model -> ok=false, zero entry (no false-positive match)", runUUID)
}

// TestLookup_EmptyModel asserts empty input does not match anything.
func TestLookup_EmptyModel(t *testing.T) {
	const runUUID = "empty-1a9c5e30-HXC1331"

	if _, ok := Lookup(Discovery{CPUModel: "   "}); ok {
		t.Fatalf("[%s] blank CPUModel matched, want ok=false", runUUID)
	}
	t.Logf("[%s] delta: blank CPUModel -> ok=false", runUUID)
}

// TestLookup_ServerCaseInsensitive proves case-insensitive matching across an
// x86 server entry and pins its distinct taxonomy (server class, hardened trust)
// to ensure entries are not collapsed.
func TestLookup_ServerCaseInsensitive(t *testing.T) {
	const runUUID = "xeon-6e0b22ad-HXC1331"

	got, ok := Lookup(Discovery{CPUModel: "intel xeon platinum 8480+"})
	if !ok {
		t.Fatalf("[%s] ok=false for lowercase Xeon, want true", runUUID)
	}
	if got.ComputeClass != ComputeServer {
		t.Errorf("[%s] ComputeClass = %q, want %q", runUUID, got.ComputeClass, ComputeServer)
	}
	if got.Trust != TrustHardened {
		t.Errorf("[%s] Trust = %q, want %q", runUUID, got.Trust, TrustHardened)
	}
	if got.Tier != "server-flagship" {
		t.Errorf("[%s] Tier = %q, want %q", runUUID, got.Tier, "server-flagship")
	}
	t.Logf("[%s] delta: lowercase %q -> {Tier:%q Class:%q Trust:%q}", runUUID, "intel xeon platinum 8480+", got.Tier, got.ComputeClass, got.Trust)
}

// TestEntries_NonEmpty sanity-checks the catalog has the required breadth
// (>= 12 well-known devices) and that the returned slice is a copy.
func TestEntries_NonEmpty(t *testing.T) {
	const runUUID = "entries-c0ffee12-HXC1331"

	es := Entries()
	if len(es) < 12 {
		t.Fatalf("[%s] catalog has %d entries, want >= 12", runUUID, len(es))
	}
	// Mutating the returned copy must not affect the package catalog.
	es[0].Tier = "CORRUPTED"
	if again := Entries(); again[0].Tier == "CORRUPTED" {
		t.Errorf("[%s] Entries() leaked internal catalog (copy not isolated)", runUUID)
	}
	t.Logf("[%s] delta: catalog breadth=%d, copy isolated", runUUID, len(es))
}
