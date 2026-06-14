package auditproof

import (
	"crypto/sha256"
	"fmt"
	"math"
	"sort"
	"strconv"
	"testing"
)

// advTag derives a deterministic, pure per-test logging tag (no time/random)
// so adversarial runs are reproducible while still distinctly labeled.
func advTag(t *testing.T, salt string) string {
	t.Helper()
	h := sha256.Sum256([]byte(t.Name() + "|adv|" + salt))
	return fmt.Sprintf("adv-%x", h[:6])
}

// ---------------------------------------------------------------------------
// CENTERPIECE: determinism under Go map-iteration randomization.
//
// Go randomizes `range` order over maps per-range. If canonicalSerialize did
// NOT sort keys, the SAME logical incentive map would serialize to different
// bytes across iterations, and the commitment would be non-deterministic —
// breaking dedup/verification. This test ranges the serializer many times AND
// rebuilds the map under many different insertion orders, asserting byte- and
// digest-identity throughout.
//
// MUTATION THAT BITES: delete `sort.Strings(nodes)` in canonicalSerialize.
// The standalone probe in this audit produced 6 distinct digests for a 6-key
// map over 2000 runs; this test then fails on the first divergent iteration.
// ---------------------------------------------------------------------------
func TestAdv_DeterminismUnderMapRandomization(t *testing.T) {
	id := advTag(t, "det")

	// A map large enough that Go's per-range seed reliably permutes order.
	base := map[string]float64{
		"node-a": 12.5,
		"node-b": 5,
		"node-c": 9.375,
		"node-d": 1,
		"node-e": 2.25,
		"node-f": 3.75,
		"node-g": 100,
		"node-h": 0.125,
	}

	wantBytes := canonicalSerialize(base)
	wantDigest := sha256.Sum256(wantBytes)
	t.Logf("%s before: canonical=%q digest=%x", id, wantBytes, wantDigest[:8])

	const iters = 512
	digests := map[[32]byte]int{}
	for i := 0; i < iters; i++ {
		// Re-range the same map (Go randomizes iteration order each range).
		gotBytes := canonicalSerialize(base)
		gotDigest := sha256.Sum256(gotBytes)
		digests[gotDigest]++
		if string(gotBytes) != string(wantBytes) {
			t.Fatalf("%s: serialization NOT deterministic on iter %d:\n got=%q\nwant=%q",
				id, i, gotBytes, wantBytes)
		}
	}

	// Rebuild the SAME logical map under many distinct insertion orders.
	keys := make([]string, 0, len(base))
	for k := range base {
		keys = append(keys, k)
	}
	for perm := 0; perm < 64; perm++ {
		// Deterministic shuffle of insertion order driven by perm.
		order := append([]string(nil), keys...)
		for i := range order {
			j := (i*7 + perm*13 + 1) % len(order)
			order[i], order[j] = order[j], order[i]
		}
		rebuilt := make(map[string]float64, len(base))
		for _, k := range order {
			rebuilt[k] = base[k]
		}
		gotDigest := sha256.Sum256(canonicalSerialize(rebuilt))
		digests[gotDigest]++
		if gotDigest != wantDigest {
			t.Fatalf("%s: digest depends on insertion order (perm %d): %x != %x",
				id, perm, gotDigest[:8], wantDigest[:8])
		}
	}

	if len(digests) != 1 {
		t.Fatalf("%s: serialization produced %d distinct digests; canonical form must be unique",
			id, len(digests))
	}
	t.Logf("%s after: stable across %d ranges + 64 reinsertions, distinct_digests=%d",
		id, iters, len(digests))
}

// TestAdv_CommitDeterminismUnderRandomization is the same invariant one layer
// up: Commit/CommitReport must be order-independent and stable.
func TestAdv_CommitDeterminismUnderRandomization(t *testing.T) {
	id := advTag(t, "commitdet")
	data := Report{ComputeUnits: map[string]float64{
		"alpha": 8, "beta": 16, "gamma": 0.5, "delta": 40, "epsilon": 2.5,
	}}
	first := CommitReport(data)
	for i := 0; i < 256; i++ {
		again := CommitReport(data)
		if again != first {
			t.Fatalf("%s: CommitReport non-deterministic on iter %d: %x != %x",
				id, i, again[:8], first[:8])
		}
	}
	t.Logf("%s: stable commitment=%x over 256 commits", id, first[:8])
}

// ---------------------------------------------------------------------------
// canonicalFloat traps.
// ---------------------------------------------------------------------------

// TestAdv_CanonicalFloat_SignedZero pins the documented contract that +0.0 and
// -0.0 canonicalize to the SAME string (and thus the same commitment). A node
// whose incentive is -0.0 must not produce a different audit digest than +0.0.
//
// MUTATION THAT BITES: delete the `if f == 0 { f = 0 }` normalization in
// canonicalFloat — strconv then emits "-0" for negative zero, diverging.
func TestAdv_CanonicalFloat_SignedZero(t *testing.T) {
	id := advTag(t, "negzero")
	negZero := math.Copysign(0, -1)
	if !math.Signbit(negZero) {
		t.Fatalf("%s: test setup wrong, negZero is not negative", id)
	}
	posS := canonicalFloat(0.0)
	negS := canonicalFloat(negZero)
	t.Logf("%s: +0 -> %q, -0 -> %q", id, posS, negS)
	if posS != negS {
		t.Fatalf("%s: +0.0 and -0.0 canonicalize differently: %q vs %q", id, posS, negS)
	}
	if negS != "0" {
		t.Fatalf("%s: zero canonical form = %q, want %q", id, negS, "0")
	}

	// Sink-side: same commitment regardless of which zero a node carries.
	cPos := Commit(map[string]float64{"n": 0.0})
	cNeg := Commit(map[string]float64{"n": negZero})
	if cPos != cNeg {
		t.Fatalf("%s: signed-zero leaks into commitment: %x != %x", id, cPos[:8], cNeg[:8])
	}
}

// TestAdv_CanonicalFloat_Specials documents NaN/Inf behavior. NaN -> "NaN",
// +Inf -> "+Inf", -Inf -> "-Inf". These cannot arise from a normal report
// (incentive = units * 1.25) but the encoder must remain total (no panic) and
// must keep +Inf and -Inf DISTINCT (sign is load-bearing).
func TestAdv_CanonicalFloat_Specials(t *testing.T) {
	id := advTag(t, "specials")
	nan := canonicalFloat(math.NaN())
	pInf := canonicalFloat(math.Inf(1))
	nInf := canonicalFloat(math.Inf(-1))
	t.Logf("%s: NaN=%q +Inf=%q -Inf=%q", id, nan, pInf, nInf)
	if nan != "NaN" {
		t.Fatalf("%s: NaN canonical = %q, want \"NaN\"", id, nan)
	}
	if pInf != "+Inf" || nInf != "-Inf" {
		t.Fatalf("%s: infinity encoding lost sign: +Inf=%q -Inf=%q", id, pInf, nInf)
	}
	if pInf == nInf {
		t.Fatalf("%s: +Inf and -Inf collide (%q) — sign-of-infinity tamper hole", id, pInf)
	}
	// DOCUMENTED HAZARD (not a prod bug for this domain): distinct NaN bit
	// patterns all map to "NaN", so a report containing NaN incentives is NOT
	// tamper-distinguishable by NaN payload. Asserted here so the contract is
	// explicit; incentive = units*1.25 cannot produce NaN from finite units.
	nanA := math.Float64frombits(0x7ff8000000000000)
	nanB := math.Float64frombits(0x7ff8000000000001)
	if canonicalFloat(nanA) != canonicalFloat(nanB) {
		t.Fatalf("%s: NaN encoding unexpectedly distinguishes bit patterns; update contract doc", id)
	}
	t.Logf("%s: documented — all NaN payloads collapse to %q (out-of-domain)", id, "NaN")
}

// TestAdv_CanonicalFloat_PrecisionInjective is the precision-collision probe.
// 'g' with precision 17 is the minimum that round-trips every float64. Adjacent
// (1-ULP-apart) values, and values differing only in the 16th/17th significant
// digit, MUST encode to DISTINCT strings — otherwise two different payouts
// would commit to the same digest (a tamper/merge hole).
//
// MUTATION THAT BITES: coarsen precision in canonicalFloat (e.g. 17 -> 15).
// At precision 15 the 1-ULP pairs below collapse to one string and this fails.
func TestAdv_CanonicalFloat_PrecisionInjective(t *testing.T) {
	id := advTag(t, "precision")

	// Pairs that are distinct float64 values but very close. Note: literal
	// const arithmetic like 0.1+0.2 is folded exactly by the Go compiler, so we
	// force runtime float64 ops / use Nextafter to get genuine 1-ULP neighbors.
	one, two := 1.0, 2.0
	tenth, fifth := 0.1, 0.2
	type pair struct{ a, b float64 }
	pairs := []pair{
		{tenth + fifth, 0.3}, // classic: runtime 0.1+0.2 != 0.3
		{0.30000000000000004, 0.29999999999999999},
		{math.Nextafter(one, two), 1},      // 1 and the next float above 1
		{math.Nextafter(9.375, 10), 9.375}, // perturb a real sample incentive
		{1e16, math.Nextafter(1e16, 2e16)}, // large magnitude, 1 ULP apart
		{123456789.123456789, math.Nextafter(123456789.123456789, 1e9)},
	}
	for i, p := range pairs {
		if p.a == p.b {
			t.Fatalf("%s: pair %d not actually distinct float64s", id, i)
		}
		sa, sb := canonicalFloat(p.a), canonicalFloat(p.b)
		t.Logf("%s: pair %d  %q vs %q  bits %x/%x", id, i, sa, sb,
			math.Float64bits(p.a), math.Float64bits(p.b))
		if sa == sb {
			t.Fatalf("%s: PRECISION COLLISION pair %d: distinct floats %v / %v both -> %q",
				id, i, p.a, p.b, sa)
		}
		// Sink-side: distinct payouts must yield distinct commitments.
		if Commit(map[string]float64{"n": p.a}) == Commit(map[string]float64{"n": p.b}) {
			t.Fatalf("%s: distinct payouts %v/%v collide in commitment (pair %d)", id, p.a, p.b, i)
		}
	}

	// Full round-trip injectivity: encoding must parse back to the same bits.
	for _, f := range []float64{12.5, 5, 9.375, 0.1, 0.125, 1e-300, 1e300, math.Pi} {
		s := canonicalFloat(f)
		back, err := strconv.ParseFloat(s, 64)
		if err != nil {
			t.Fatalf("%s: canonicalFloat(%v)=%q does not parse: %v", id, f, s, err)
		}
		if math.Float64bits(back) != math.Float64bits(f) {
			t.Fatalf("%s: canonicalFloat not round-trip injective for %v: %q -> %v", id, f, s, back)
		}
	}
}

// TestAdv_CanonicalFloat_IntegerValued pins that integer-valued floats render
// without a trailing ".0" (1.0 -> "1") and, crucially, that this does NOT
// collide an integer-valued float with a nearby non-integer one.
func TestAdv_CanonicalFloat_IntegerValued(t *testing.T) {
	id := advTag(t, "intval")
	if got := canonicalFloat(1.0); got != "1" {
		t.Fatalf("%s: 1.0 -> %q, want %q", id, got, "1")
	}
	if got := canonicalFloat(5.0); got != "5" {
		t.Fatalf("%s: 5.0 -> %q, want %q", id, got, "5")
	}
	// 1 vs 1.0000000000000002 (next float) must stay distinct.
	if canonicalFloat(1) == canonicalFloat(math.Nextafter(1, 2)) {
		t.Fatalf("%s: integer 1 collides with its successor float", id)
	}
	t.Logf("%s: integer-valued floats render bare and stay distinct from neighbors", id)
}

// ---------------------------------------------------------------------------
// INJECTIVITY / tamper: distinct maps must never share bytes.
// ---------------------------------------------------------------------------

// TestAdv_Injective_ControlBytesInNodeID is the hard separator-ambiguity probe.
// Node ids are length-PREFIXED, so a node id may legally contain the unit
// separator (0x1f) or record separator (0x1e) without breaking framing. We
// brute-force many small maps whose node ids embed control bytes and assert
// NO two distinct maps share a serialization (and thus a commitment).
//
// MUTATION THAT BITES: drop the length prefix (the `strconv.AppendInt(... len)`
// + first 0x1f) in canonicalSerialize. Then a node id containing 0x1f merges
// with the value boundary and distinct maps collide — this test surfaces it.
func TestAdv_Injective_ControlBytesInNodeID(t *testing.T) {
	id := advTag(t, "ctrlbytes")
	us := string([]byte{0x1f}) // unit separator
	rs := string([]byte{0x1e}) // record separator

	// Targeted boundary-confusion pair: a single node whose id embeds 0x1f vs a
	// two-node map whose framing could be confused without length prefixing.
	single := map[string]float64{"a" + us + "1" + us: 1}
	twoNode := map[string]float64{"a": 1, "": 1}
	s1 := canonicalSerialize(single)
	s2 := canonicalSerialize(twoNode)
	t.Logf("%s: single=%q two=%q", id, s1, s2)
	if string(s1) == string(s2) {
		t.Fatalf("%s: boundary confusion — distinct maps share bytes", id)
	}

	// Brute force: build distinct maps from an alphabet including control bytes
	// and confirm serialization is injective (no two map-canonical-forms share
	// the same byte stream).
	alphabet := []string{"a", "b", "", "1", us, rs, us + rs, "ab"}
	seen := map[string]string{} // bytes -> canonical map repr
	var checked, collisions int

	canonRepr := func(m map[string]float64) string {
		ks := make([]string, 0, len(m))
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		var b []byte
		for _, k := range ks {
			b = append(b, fmt.Sprintf("%q=%v;", k, m[k])...)
		}
		return string(b)
	}

	// Enumerate maps of up to 3 (key,value) entries.
	for _, k1 := range alphabet {
		for v1 := 1; v1 <= 3; v1++ {
			for _, k2 := range alphabet {
				for v2 := 1; v2 <= 3; v2++ {
					m := map[string]float64{}
					m[k1] = float64(v1)
					m[k2] = float64(v2) // may overwrite if k1==k2; that's fine
					checked++
					b := string(canonicalSerialize(m))
					repr := canonRepr(m)
					if prev, ok := seen[b]; ok && prev != repr {
						t.Errorf("%s: COLLISION bytes=%q  mapA=%s  mapB=%s", id, b, prev, repr)
						collisions++
					} else {
						seen[b] = repr
					}
				}
			}
		}
	}
	t.Logf("%s: brute-forced %d maps, distinct_serializations=%d, collisions=%d",
		id, checked, len(seen), collisions)
	if collisions != 0 {
		t.Fatalf("%s: encoding is NOT injective (%d collisions)", id, collisions)
	}
}

// TestAdv_Injective_KeyValueBoundary specifically attacks the {"a":1,"b":2}
// vs {"ab":12} family of concatenation-merge collisions across both the
// node/value boundary and the inter-record boundary.
func TestAdv_Injective_KeyValueBoundary(t *testing.T) {
	id := advTag(t, "kvboundary")
	cases := [][2]map[string]float64{
		{{"a": 1, "b": 2}, {"ab": 1, "c": 2}},
		{{"a": 1, "b": 2}, {"a": 12, "b": 2}},
		{{"x": 1}, {"x": 1, "y": 1}},
		{{"1": 1}, {"": 11}},
		{{"a": 1, "ab": 2}, {"a": 1, "b": 2}},
	}
	for i, c := range cases {
		s0 := string(canonicalSerialize(c[0]))
		s1 := string(canonicalSerialize(c[1]))
		t.Logf("%s: case %d  %q  vs  %q", id, i, s0, s1)
		if s0 == s1 {
			t.Fatalf("%s: case %d distinct maps serialized identically: %q", id, i, s0)
		}
		if Commit(c[0]) == Commit(c[1]) {
			t.Fatalf("%s: case %d distinct maps share a commitment (tamper hole)", id, i)
		}
	}
}

// TestAdv_TamperEveryNode strengthens clause-2: perturbing ANY single node's
// value (by 1 ULP) must change the commitment — no value position is ignored.
func TestAdv_TamperEveryNode(t *testing.T) {
	id := advTag(t, "tamperall")
	base := map[string]float64{"n0": 1, "n1": 2.5, "n2": 9.375, "n3": 40, "n4": 0.125}
	orig := Commit(base)
	for k, v := range base {
		tampered := make(map[string]float64, len(base))
		for kk, vv := range base {
			tampered[kk] = vv
		}
		tampered[k] = math.Nextafter(v, v+1) // smallest possible change
		c := Commit(tampered)
		if c == orig {
			t.Fatalf("%s: 1-ULP tamper of node %q did not change commitment (value ignored)", id, k)
		}
	}
	t.Logf("%s: every node's value is load-bearing in the commitment", id)
}
