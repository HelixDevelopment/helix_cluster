package netutil

import (
	"net"
	"testing"
)

// These tests pin the SSRF-defense contract of the netutil IP-classification
// predicates. The high-value targets are IsPrivateIP and IsLoopbackIP: if a
// private/internal/loopback address is misclassified as public, an SSRF filter
// built on these primitives is bypassed and an attacker reaches internal
// services. The IsPrivateIP boundary matrix and the IPv4-mapped-IPv6 cases are
// the centerpieces.
//
// DOCUMENTED CONTRACT (per ipclass.go doc comments, pinned here):
//   - IsPrivateIP   => RFC1918 (10/8, 172.16/12, 192.168/16) OR IPv6 ULA fc00::/7.
//                      It does NOT include loopback, link-local, CGNAT 100.64/10,
//                      or 0.0.0.0. A complete SSRF gate must OR the other
//                      predicates in; see TestSSRF_GateCompositionContract.
//   - IsLoopbackIP  => the whole 127.0.0.0/8 range and ::1.
//   - IsLinkLocalIP => 169.254.0.0/16, fe80::/10, and link-local multicast.

// ---------------------------------------------------------------------------
// CENTERPIECE 1: IsPrivateIP exhaustive boundary matrix.
// For each RFC1918 / ULA range, an address just inside and just outside the
// boundary is asserted with its exact classification. Off-by-one bugs live here.
// ---------------------------------------------------------------------------

func TestIsPrivateIP_BoundaryMatrix(t *testing.T) {
	cases := []struct {
		ip      string
		private bool
		why     string
	}{
		// --- 10.0.0.0/8 boundaries ---
		{"9.255.255.255", false, "just below 10/8"},
		{"10.0.0.0", true, "10/8 network base"},
		{"10.0.0.1", true, "inside 10/8 low"},
		{"10.255.255.255", true, "10/8 top"},
		{"11.0.0.0", false, "just above 10/8"},

		// --- 172.16.0.0/12 boundaries (the classic off-by-one trap) ---
		{"172.15.255.255", false, "just below 172.16/12 (PUBLIC)"},
		{"172.16.0.0", true, "172.16/12 network base"},
		{"172.16.0.1", true, "inside 172.16/12 low"},
		{"172.20.10.5", true, "mid 172.16/12"},
		{"172.31.255.255", true, "172.16/12 top"},
		{"172.32.0.0", false, "just above 172.16/12 (PUBLIC)"},
		{"172.0.0.1", false, "172.0 outside 172.16/12"},
		{"172.255.0.1", false, "172.255 outside 172.16/12"},

		// --- 192.168.0.0/16 boundaries ---
		{"192.167.255.255", false, "just below 192.168/16"},
		{"192.168.0.0", true, "192.168/16 network base"},
		{"192.168.0.1", true, "inside 192.168/16 low"},
		{"192.168.255.255", true, "192.168/16 top"},
		{"192.169.0.0", false, "just above 192.168/16"},

		// --- ULA fc00::/7 boundaries (covers fc.. and fd.., excludes fe..) ---
		{"fbff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", false, "just below fc00::/7"},
		{"fc00::", true, "fc00::/7 network base"},
		{"fc00::1", true, "inside fc.."},
		{"fdff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", true, "top of fc00::/7 (fd..)"},
		{"fe00::", false, "just above fc00::/7"},
		{"fe80::1", false, "link-local, NOT ULA private"},

		// --- NON-private vectors that an SSRF dev might wrongly expect ---
		// IsPrivateIP is RFC1918+ULA ONLY by contract; these are FALSE here.
		{"127.0.0.1", false, "loopback is NOT 'private' per this predicate"},
		{"::1", false, "IPv6 loopback is NOT 'private' per this predicate"},
		{"169.254.0.1", false, "link-local is NOT 'private' per this predicate"},
		{"100.64.0.1", false, "CGNAT 100.64/10 is NOT covered by IsPrivateIP"},
		{"100.127.255.255", false, "CGNAT top is NOT covered by IsPrivateIP"},
		{"0.0.0.0", false, "unspecified is NOT covered by IsPrivateIP"},
		{"8.8.8.8", false, "public DNS"},
		{"1.1.1.1", false, "public DNS"},
		{"2001:4860:4860::8888", false, "public IPv6"},
	}

	for _, tc := range cases {
		ip := net.ParseIP(tc.ip)
		if ip == nil {
			t.Fatalf("test setup: %q failed to parse", tc.ip)
		}
		got := IsPrivateIP(ip)
		if got != tc.private {
			t.Errorf("IsPrivateIP(%s) = %v, want %v  [%s]", tc.ip, got, tc.private, tc.why)
		}
	}
}

// ---------------------------------------------------------------------------
// CENTERPIECE 2: IPv4-mapped IPv6 (::ffff:a.b.c.d) — the SSRF bypass vector.
// A mapped address MUST classify identically to its bare IPv4 form. A filter
// that treats ::ffff:10.0.0.1 as public is an SSRF hole. Go's net.IP
// canonicalizes mapped addresses (To4() returns the embedded v4), and
// IsPrivateIP delegates to To4(), so this is handled correctly — pinned here.
// ---------------------------------------------------------------------------

func TestIsPrivateIP_IPv4MappedIPv6_MatchesBareForm(t *testing.T) {
	cases := []struct {
		mapped  string
		bare    string
		private bool
	}{
		{"::ffff:10.0.0.1", "10.0.0.1", true},
		{"::ffff:172.16.0.1", "172.16.0.1", true},
		{"::ffff:192.168.1.1", "192.168.1.1", true},
		{"::ffff:172.15.255.255", "172.15.255.255", false}, // boundary, public
		{"::ffff:8.8.8.8", "8.8.8.8", false},
		{"::ffff:100.64.0.1", "100.64.0.1", false}, // CGNAT not covered, both agree
	}
	for _, tc := range cases {
		mip := net.ParseIP(tc.mapped)
		bip := net.ParseIP(tc.bare)
		if mip == nil || bip == nil {
			t.Fatalf("test setup: parse failed mapped=%q bare=%q", tc.mapped, tc.bare)
		}
		gotMapped := IsPrivateIP(mip)
		gotBare := IsPrivateIP(bip)
		if gotMapped != gotBare {
			t.Errorf("IsPrivateIP MISMATCH (SSRF bypass risk): %s=%v vs %s=%v",
				tc.mapped, gotMapped, tc.bare, gotBare)
		}
		if gotMapped != tc.private {
			t.Errorf("IsPrivateIP(%s) = %v, want %v", tc.mapped, gotMapped, tc.private)
		}
	}
}

// IPv4-mapped loopback (::ffff:127.0.0.1) MUST be loopback — Go canonicalizes
// it so IsLoopback sees 127.0.0.1. A filter treating it as public is an SSRF
// hole reaching the local host.
func TestIsLoopbackIP_IPv4MappedIPv6(t *testing.T) {
	if !IsLoopbackIP(net.ParseIP("::ffff:127.0.0.1")) {
		t.Error("::ffff:127.0.0.1 MUST be loopback (mapped form of 127.0.0.1) — SSRF risk if not")
	}
	if !IsLoopbackIP(net.ParseIP("::ffff:127.1.2.3")) {
		t.Error("::ffff:127.1.2.3 MUST be loopback (whole 127/8 is loopback)")
	}
	if IsLoopbackIP(net.ParseIP("::ffff:10.0.0.1")) {
		t.Error("::ffff:10.0.0.1 must NOT be loopback (it is private, not loopback)")
	}
}

// ---------------------------------------------------------------------------
// IPv6 special-range classification: ::1 loopback, fe80::/10 link-local,
// fc00::/7 ULA — and the boundary that fe80::/10 actually starts at fe80, not fc.
// ---------------------------------------------------------------------------

func TestIPv6_SpecialRanges(t *testing.T) {
	// ::1 is loopback, NOT private, NOT link-local.
	if !IsLoopbackIP(net.ParseIP("::1")) {
		t.Error("::1 must be loopback")
	}
	if IsPrivateIP(net.ParseIP("::1")) {
		t.Error("::1 is loopback, not private-by-contract")
	}
	if IsLinkLocalIP(net.ParseIP("::1")) {
		t.Error("::1 is not link-local")
	}

	// fe80::/10 link-local boundaries.
	for _, s := range []string{"fe80::1", "fe80::ffff:ffff", "febf:ffff::1"} {
		if !IsLinkLocalIP(net.ParseIP(s)) {
			t.Errorf("%s must be link-local (fe80::/10)", s)
		}
		if IsPrivateIP(net.ParseIP(s)) {
			t.Errorf("%s is link-local, must NOT be IsPrivateIP", s)
		}
	}
	// fec0:: is outside fe80::/10 (old deprecated site-local) — neither LL nor ULA.
	if IsLinkLocalIP(net.ParseIP("fec0::1")) {
		t.Error("fec0::1 is outside fe80::/10, must NOT be link-local")
	}

	// fc00::/7 ULA is private but not link-local/loopback.
	if !IsPrivateIP(net.ParseIP("fc00::1")) {
		t.Error("fc00::1 must be ULA private")
	}
	if IsLinkLocalIP(net.ParseIP("fc00::1")) {
		t.Error("fc00::1 (ULA) must NOT be link-local")
	}

	// IPv6 link-local multicast ff02:: is link-local per the predicate.
	if !IsLinkLocalIP(net.ParseIP("ff02::1")) {
		t.Error("ff02::1 must be link-local multicast")
	}
}

// IPv4 link-local multicast (224.0.0.0/24) is covered by IsLinkLocalIP.
func TestIsLinkLocalIP_IPv4Multicast(t *testing.T) {
	if !IsLinkLocalIP(net.ParseIP("224.0.0.251")) {
		t.Error("224.0.0.251 (mDNS) is link-local multicast")
	}
	if IsLinkLocalIP(net.ParseIP("224.1.0.1")) {
		t.Error("224.1.0.1 is global multicast, NOT link-local")
	}
}

// ---------------------------------------------------------------------------
// Loopback covers the WHOLE 127.0.0.0/8, not just 127.0.0.1.
// ---------------------------------------------------------------------------

func TestIsLoopbackIP_Whole127Slash8(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "127.0.0.0", "127.1.2.3", "127.255.255.255"} {
		if !IsLoopbackIP(net.ParseIP(s)) {
			t.Errorf("%s must be loopback (127/8) — SSRF risk if treated public", s)
		}
	}
	for _, s := range []string{"126.255.255.255", "128.0.0.1"} {
		if IsLoopbackIP(net.ParseIP(s)) {
			t.Errorf("%s must NOT be loopback (outside 127/8)", s)
		}
	}
}

// ---------------------------------------------------------------------------
// nil / malformed input must not panic and must classify as not-anything.
// ---------------------------------------------------------------------------

func TestPredicates_NilAndMalformed_NoPanic(t *testing.T) {
	// nil IP.
	if IsPrivateIP(nil) {
		t.Error("IsPrivateIP(nil) must be false")
	}
	if IsLoopbackIP(nil) {
		t.Error("IsLoopbackIP(nil) must be false")
	}
	if IsLinkLocalIP(nil) {
		t.Error("IsLinkLocalIP(nil) must be false")
	}
	if IsIPv4(nil) || IsIPv6(nil) {
		t.Error("IsIPv4/IsIPv6(nil) must be false")
	}

	// net.ParseIP returns nil for malformed strings; ensure predicates eat it.
	for _, bad := range []string{"", "not-an-ip", "999.999.999.999", "10.0.0.0/8", "::ffff::1"} {
		ip := net.ParseIP(bad)
		if ip != nil {
			t.Fatalf("test setup: expected %q to parse to nil", bad)
		}
		// Must not panic.
		_ = IsPrivateIP(ip)
		_ = IsLoopbackIP(ip)
		_ = IsLinkLocalIP(ip)
	}

	// A zero-length / garbage-length IP must not panic either.
	garbage := net.IP{0x01, 0x02, 0x03} // 3 bytes: neither v4 nor v6
	if IsPrivateIP(garbage) {
		t.Error("3-byte garbage IP must not be private")
	}
	_ = IsLoopbackIP(garbage)
	_ = IsLinkLocalIP(garbage)
}

// ---------------------------------------------------------------------------
// SSRF GATE COMPOSITION CONTRACT: documents that IsPrivateIP alone is NOT a
// complete internal-address filter. A correct SSRF gate must OR loopback,
// link-local, and (for full coverage) CGNAT/unspecified handled separately.
// This pins the gap so callers don't mistake IsPrivateIP for a full gate.
// ---------------------------------------------------------------------------

func TestSSRF_GateCompositionContract(t *testing.T) {
	// A representative gate built from the predicates this package exposes.
	internal := func(ip net.IP) bool {
		return IsPrivateIP(ip) || IsLoopbackIP(ip) || IsLinkLocalIP(ip)
	}

	mustBlock := []string{
		"10.0.0.1", "172.16.0.1", "192.168.1.1", // RFC1918
		"127.0.0.1", "::1", "::ffff:127.0.0.1", // loopback + mapped loopback
		"169.254.169.254",    // link-local (cloud metadata endpoint — classic SSRF target)
		"fe80::1", "fc00::1", // IPv6 LL + ULA
		"::ffff:10.0.0.1", // mapped private
	}
	for _, s := range mustBlock {
		if !internal(net.ParseIP(s)) {
			t.Errorf("SSRF gate FAILED to block internal address %s", s)
		}
	}

	// KNOWN GAP pinned: these are internal/special but NOT caught by the
	// composed gate above, because netutil exposes no CGNAT/unspecified
	// predicate. Callers needing SSRF defense MUST handle these themselves.
	knownGap := []string{
		"100.64.0.1", // CGNAT 100.64/10
		"0.0.0.0",    // unspecified (routes to localhost on many stacks)
		"192.0.2.1",  // TEST-NET-1 (documentation, not routable)
	}
	for _, s := range knownGap {
		if internal(net.ParseIP(s)) {
			t.Fatalf("contract drift: %s is now caught by the composed gate; "+
				"update the SSRF-gap documentation in ipclass.go and this test", s)
		}
	}

	// Public addresses must pass through (not be falsely blocked).
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "2001:4860:4860::8888"} {
		if internal(net.ParseIP(s)) {
			t.Errorf("SSRF gate wrongly blocked public address %s", s)
		}
	}
}

// ---------------------------------------------------------------------------
// NextIP carry / wraparound across octet boundaries (mutation-sensitive).
// ---------------------------------------------------------------------------

func TestNextIP_CarryAndWrap(t *testing.T) {
	cases := []struct{ in, want string }{
		{"10.0.0.255", "10.0.1.0"},     // single-octet carry
		{"10.0.255.255", "10.1.0.0"},   // double-octet carry
		{"10.255.255.255", "11.0.0.0"}, // triple-octet carry
		{"0.0.0.0", "0.0.0.1"},         // base increment
		{"255.255.255.255", "0.0.0.0"}, // full wraparound
		{"192.168.1.254", "192.168.1.255"},
	}
	for _, tc := range cases {
		got := NextIP(net.ParseIP(tc.in))
		if got.String() != tc.want {
			t.Errorf("NextIP(%s) = %s, want %s", tc.in, got, tc.want)
		}
	}
	// Input must not be mutated.
	in := net.ParseIP("10.0.0.255")
	_ = NextIP(in)
	if in.String() != "10.0.0.255" {
		t.Errorf("NextIP mutated its input: %s", in)
	}
}

// IPv6 NextIP carry across the low bytes.
func TestNextIP_IPv6Carry(t *testing.T) {
	got := NextIP(net.ParseIP("2001:db8::ff"))
	if got.String() != "2001:db8::100" {
		t.Errorf("NextIP(2001:db8::ff) = %s, want 2001:db8::100", got)
	}
	// All-ones IPv6 wraps to all-zeros.
	allOnes := net.ParseIP("ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff")
	got = NextIP(allOnes)
	if !got.Equal(net.IPv6zero) {
		t.Errorf("NextIP(all-ones v6) = %s, want :: (all zeros)", got)
	}
}

// ---------------------------------------------------------------------------
// ParseCIDR error semantics + valid parse.
// ---------------------------------------------------------------------------

func TestParseCIDR_Adversarial(t *testing.T) {
	good := []string{"10.0.0.0/8", "192.168.1.10/24", "::1/128", "fc00::/7", "0.0.0.0/0"}
	for _, s := range good {
		if _, err := ParseCIDR(s); err != nil {
			t.Errorf("ParseCIDR(%q) unexpected error: %v", s, err)
		}
	}
	bad := []string{"", "garbage", "10.0.0.0", "10.0.0.0/33", "10.0.0.0/-1",
		"256.0.0.0/8", "::ffff::/64", "10.0.0.0/8/8"}
	for _, s := range bad {
		if _, err := ParseCIDR(s); err == nil {
			t.Errorf("ParseCIDR(%q) expected error, got nil", s)
		}
	}
}

// ---------------------------------------------------------------------------
// IsValidPort boundaries: 0 / 1 / 65535 / 65536 / negative.
// ---------------------------------------------------------------------------

func TestIsValidPort_Boundaries(t *testing.T) {
	cases := []struct {
		port int
		want bool
	}{
		{-1, false},
		{0, false},
		{1, true},
		{80, true},
		{65535, true},
		{65536, false},
		{100000, false},
		{-65535, false},
	}
	for _, tc := range cases {
		if got := IsValidPort(tc.port); got != tc.want {
			t.Errorf("IsValidPort(%d) = %v, want %v", tc.port, got, tc.want)
		}
	}
}
