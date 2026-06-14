package headersanitize

import (
	"strings"
	"testing"
)

// This file pins the security contract of the managed-header sanitizer with
// adversarial, mutation-verified tests. The sanitizer's dominant bug class is
// "a forged client value for a MANAGED header survives sanitization" — either
// because a casing/obfuscation variant escapes the IsManaged denylist, or
// because the authoritative overwrite step is skipped. Each table case below
// asserts the security invariant directly: after Sanitize, every managed
// header equals its connection-derived authoritative value and no forged
// client value remains under the managed canonical key. A genuine-pass
// baseline (safe headers survive unchanged) guards against an always-strip
// mutation.

// invariantManagedAuthoritative asserts the core post-condition: the three
// standard managed headers in out hold exactly the connection-derived values.
func invariantManagedAuthoritative(t *testing.T, out map[string]string, conn ConnInfo) {
	t.Helper()
	if got, ok := out[HeaderForwardedFor]; !ok || got != conn.ClientIP {
		t.Errorf("X-Forwarded-For = %q (present=%v), want authoritative %q", got, ok, conn.ClientIP)
	}
	if got, ok := out[HeaderRealIP]; !ok || got != conn.ClientIP {
		t.Errorf("X-Real-Ip = %q (present=%v), want authoritative %q", got, ok, conn.ClientIP)
	}
	if got, ok := out[HeaderTrustedTenant]; !ok || got != conn.Tenant {
		t.Errorf("X-Trusted-Tenant = %q (present=%v), want authoritative %q", got, ok, conn.Tenant)
	}
}

// TestSanitize_CaseInsensitiveManagedMatch is the load-bearing security test.
// Every casing variant of every managed header MUST be recognized and its
// forged value overwritten by the authoritative connection value. If matching
// degraded to case-sensitive (the canonical mutation), the lower/upper/mixed
// variants would pass through unmanaged and the forged value would survive.
func TestSanitize_CaseInsensitiveManagedMatch(t *testing.T) {
	id := runUUID(t)
	conn := ConnInfo{ClientIP: "203.0.113.50", Tenant: "tenant-authoritative"}
	s := NewSanitizer()

	const forgedIP = "10.6.6.6"
	const forgedTenant = "tenant-attacker"

	cases := []struct {
		name    string // sub-test label
		key     string // header name as the client supplies it
		value   string // forged value
		canon   string // canonical managed key the forged value must NOT equal
		wantVal string // authoritative value the canonical key MUST hold
	}{
		{"xff-lower", "x-forwarded-for", forgedIP, HeaderForwardedFor, conn.ClientIP},
		{"xff-upper", "X-FORWARDED-FOR", forgedIP, HeaderForwardedFor, conn.ClientIP},
		{"xff-mixed", "X-fOrWaRdEd-FoR", forgedIP, HeaderForwardedFor, conn.ClientIP},
		{"xff-canon", "X-Forwarded-For", forgedIP, HeaderForwardedFor, conn.ClientIP},
		{"realip-lower", "x-real-ip", forgedIP, HeaderRealIP, conn.ClientIP},
		{"realip-allcaps-ip", "X-REAL-IP", forgedIP, HeaderRealIP, conn.ClientIP},
		{"realip-canon", "X-Real-Ip", forgedIP, HeaderRealIP, conn.ClientIP},
		{"tenant-lower", "x-trusted-tenant", forgedTenant, HeaderTrustedTenant, conn.Tenant},
		{"tenant-upper", "X-TRUSTED-TENANT", forgedTenant, HeaderTrustedTenant, conn.Tenant},
		{"tenant-mixed", "X-Trusted-TENANT", forgedTenant, HeaderTrustedTenant, conn.Tenant},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !s.IsManaged(tc.key) {
				t.Fatalf("IsManaged(%q) = false; casing variant escaped the managed set", tc.key)
			}
			headers := map[string]string{
				tc.key:         tc.value, // forged
				"X-Request-Id": "req-" + tc.name,
			}
			t.Logf("run=%s BEFORE key=%q forged=%q", id, tc.key, tc.value)

			out := s.Sanitize(headers, conn)
			t.Logf("run=%s AFTER canon[%q]=%q", id, tc.canon, out[tc.canon])

			// The canonical managed key holds the authoritative value, not the forgery.
			if got := out[tc.canon]; got != tc.wantVal {
				t.Errorf("out[%q] = %q, want authoritative %q (forged value survived case variant %q)",
					tc.canon, got, tc.wantVal, tc.key)
			}
			// The forged value must not survive anywhere under the canonical key.
			if out[tc.canon] == tc.value {
				t.Errorf("forged value %q survived under canonical key %q", tc.value, tc.canon)
			}
			// Full managed invariant still holds (other managed headers set too).
			invariantManagedAuthoritative(t, out, conn)
			// Non-managed sibling preserved.
			if out["X-Request-Id"] != "req-"+tc.name {
				t.Errorf("non-managed X-Request-Id altered: %q", out["X-Request-Id"])
			}
		})
	}
}

// TestSanitize_CustomManagedCaseInsensitive proves a custom managed header is
// matched case-insensitively too and its forged value is stripped to empty
// (no connection mapping => authoritative empty).
func TestSanitize_CustomManagedCaseInsensitive(t *testing.T) {
	id := runUUID(t)
	conn := ConnInfo{ClientIP: "198.51.100.1", Tenant: "tenant-x"}
	s := NewSanitizer("X-Internal-Admin")

	for _, variant := range []string{"x-internal-admin", "X-INTERNAL-ADMIN", "X-Internal-Admin", "x-InTeRnAl-aDmIn"} {
		t.Run(variant, func(t *testing.T) {
			if !s.IsManaged(variant) {
				t.Fatalf("custom managed header variant %q not recognized", variant)
			}
			headers := map[string]string{variant: "true"} // forged privilege escalation
			t.Logf("run=%s BEFORE %q=%q", id, variant, headers[variant])
			out := s.Sanitize(headers, conn)
			canon := canonicalize("X-Internal-Admin")
			t.Logf("run=%s AFTER %q=%q", id, canon, out[canon])
			if got := out[canon]; got != "" {
				t.Errorf("custom managed %q = %q, want empty (forged value must be stripped)", canon, got)
			}
		})
	}
}

// TestSanitize_NilAndEmptyInputFailClosed asserts the sanitizer fails CLOSED on
// degenerate input: a nil map and an empty map must NOT panic, and must still
// install the authoritative managed values. (A fail-OPEN sanitizer that
// returned input unchanged or skipped the overwrite on empty input would leave
// managed headers unset.)
func TestSanitize_NilAndEmptyInputFailClosed(t *testing.T) {
	id := runUUID(t)
	conn := ConnInfo{ClientIP: "192.0.2.123", Tenant: "tenant-closed"}
	s := NewSanitizer()

	for _, tc := range []struct {
		name string
		in   map[string]string
	}{
		{"nil-map", nil},
		{"empty-map", map[string]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Sanitize panicked on %s input: %v", tc.name, r)
				}
			}()
			out := s.Sanitize(tc.in, conn)
			t.Logf("run=%s %s -> xff=%q realip=%q tenant=%q", id, tc.name,
				out[HeaderForwardedFor], out[HeaderRealIP], out[HeaderTrustedTenant])
			invariantManagedAuthoritative(t, out, conn)
		})
	}
}

// TestSanitize_EmptyHeaderNameIgnored asserts an empty-string header name in the
// input does not corrupt the managed set or leak a forged value under a managed
// key. The empty name canonicalizes to "" (not managed) and is harmless.
func TestSanitize_EmptyHeaderNameIgnored(t *testing.T) {
	id := runUUID(t)
	conn := ConnInfo{ClientIP: "203.0.113.9", Tenant: "tenant-e"}
	s := NewSanitizer()

	headers := map[string]string{
		"":           "garbage", // empty header name
		"User-Agent": "probe/1.0",
	}
	out := s.Sanitize(headers, conn)
	t.Logf("run=%s AFTER empty=%q ua=%q", id, out[""], out["User-Agent"])

	invariantManagedAuthoritative(t, out, conn)
	if out["User-Agent"] != "probe/1.0" {
		t.Errorf("non-managed User-Agent altered: %q", out["User-Agent"])
	}
}

// TestSanitize_ObfuscatedNamesDocumentBoundary documents the matcher boundary:
// header NAMES carrying whitespace, CR, LF, or NUL are NOT valid HTTP field
// names (RFC 7230 field-name = token; token excludes SP, CTL). A conformant
// HTTP parser rejects them before they ever become map keys, so they are
// outside this sanitizer's map-based contract. This test asserts the safe,
// expected outcome IF such a name somehow appears: the managed canonical key
// STILL holds the authoritative value (the real protection is never defeated),
// even though the obfuscated variant is treated as a distinct non-managed key.
// It also pins that the authoritative managed key is never the forged value.
func TestSanitize_ObfuscatedNamesDocumentBoundary(t *testing.T) {
	id := runUUID(t)
	conn := ConnInfo{ClientIP: "203.0.113.77", Tenant: "tenant-b"}
	s := NewSanitizer()

	const forged = "10.9.9.9"
	obfuscated := []string{
		" X-Forwarded-For",  // leading space
		"X-Forwarded-For ",  // trailing space
		"X-Forwarded-For\r", // trailing CR
		"\nX-Forwarded-For", // leading LF
		"X-Forwarded-For\x00",
		"X--Forwarded-For", // doubled hyphen (distinct token shape)
	}
	for _, name := range obfuscated {
		t.Run(strings.NewReplacer("\r", "CR", "\n", "LF", "\x00", "NUL", " ", "SP").Replace(name), func(t *testing.T) {
			headers := map[string]string{name: forged}
			out := s.Sanitize(headers, conn)
			// The genuine canonical managed key is ALWAYS authoritative —
			// the obfuscated variant cannot poison it.
			if got := out[HeaderForwardedFor]; got != conn.ClientIP {
				t.Errorf("authoritative X-Forwarded-For = %q, want %q (obfuscated name %q defeated protection)",
					got, conn.ClientIP, name)
			}
			// The canonical managed key never holds the forged value.
			if out[HeaderForwardedFor] == forged {
				t.Errorf("forged value reached canonical managed key via %q", name)
			}
			t.Logf("run=%s name=%q -> canonXFF=%q (authoritative)", id, name, out[HeaderForwardedFor])
		})
	}
}

// TestSanitize_GenuinePassBaseline is the always-strip guard: a request of only
// SAFE, non-managed headers must pass every one of them through UNCHANGED (with
// canonical keys), while the managed values are added from the connection. A
// mutation that blanket-strips or blanket-empties values is caught here.
func TestSanitize_GenuinePassBaseline(t *testing.T) {
	id := runUUID(t)
	conn := ConnInfo{ClientIP: "203.0.113.200", Tenant: "tenant-ok"}
	s := NewSanitizer()

	safe := map[string]string{
		"User-Agent":      "curl/8.5.0",
		"Accept":          "application/json",
		"Content-Type":    "application/json; charset=utf-8",
		"Authorization":   "Bearer abc.def.ghi",
		"X-Request-Id":    "11111111-2222",
		"Accept-Encoding": "gzip, br",
	}
	out := s.Sanitize(safe, conn)
	t.Logf("run=%s baseline out-size=%d", id, len(out))

	for k, want := range safe {
		canon := canonicalize(k)
		if got, ok := out[canon]; !ok || got != want {
			t.Errorf("safe header %q (canon %q) = %q (present=%v), want %q unchanged", k, canon, got, ok, want)
		}
	}
	// Managed values added on top.
	invariantManagedAuthoritative(t, out, conn)
	// Input not mutated.
	if safe["User-Agent"] != "curl/8.5.0" {
		t.Errorf("input map mutated: User-Agent now %q", safe["User-Agent"])
	}
}
