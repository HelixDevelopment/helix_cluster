package headersanitize

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
)

// runUUID returns a random hex id used to anchor before/after observable logs
// (CLAUDE-1: each test logs a run-UUID and observable state).
func runUUID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b[:])
}

// TestSanitize_ForgedManagedHeadersOverwritten covers CLOSURE clause 1:
// a request with FORGED X-Forwarded-For and X-Trusted-Tenant plus a real
// connection. Before: forged headers present. After Sanitize: the managed
// headers equal the connection-derived authoritative values (forged values
// gone), and a non-managed header is untouched.
func TestSanitize_ForgedManagedHeadersOverwritten(t *testing.T) {
	id := runUUID(t)

	conn := ConnInfo{ClientIP: "203.0.113.7", Tenant: "tenant-real"}
	s := NewSanitizer()

	// Client forges its source IP and tenant; X-Real-IP also forged.
	headers := map[string]string{
		"X-Forwarded-For":  "10.0.0.66",      // forged: claims a different source
		"x-trusted-tenant": "tenant-evil",     // forged + lower-case to test canonicalization
		"X-Real-IP":        "10.0.0.66",      // forged
		"User-Agent":       "curl/8.0",       // non-managed, must survive
	}

	// before observable: prove the forged values are actually present.
	if headers["X-Forwarded-For"] != "10.0.0.66" {
		t.Fatalf("precondition: forged XFF not present")
	}
	t.Logf("run=%s BEFORE xff=%q tenant=%q realip=%q ua=%q",
		id, headers["X-Forwarded-For"], headers["x-trusted-tenant"],
		headers["X-Real-IP"], headers["User-Agent"])

	out := s.Sanitize(headers, conn)

	t.Logf("run=%s AFTER xff=%q tenant=%q realip=%q ua=%q",
		id, out[HeaderForwardedFor], out[HeaderTrustedTenant],
		out[HeaderRealIP], out["User-Agent"])

	// Managed values MUST equal connection-derived authoritative values.
	// Mutation: if Sanitize passed managed headers through unchanged instead
	// of overwriting, out[XFF] would still be the forged "10.0.0.66" and these
	// fail.
	if got := out[HeaderForwardedFor]; got != conn.ClientIP {
		t.Errorf("X-Forwarded-For = %q, want authoritative %q", got, conn.ClientIP)
	}
	if got := out[HeaderRealIP]; got != conn.ClientIP {
		t.Errorf("X-Real-IP = %q, want authoritative %q", got, conn.ClientIP)
	}
	if got := out[HeaderTrustedTenant]; got != conn.Tenant {
		t.Errorf("X-Trusted-Tenant = %q, want authoritative %q", got, conn.Tenant)
	}

	// The forged values must be GONE (no managed key holds a client value).
	// Mutation: a pass-through bug leaves the forged IP somewhere.
	if out[HeaderForwardedFor] == "10.0.0.66" || out[HeaderTrustedTenant] == "tenant-evil" {
		t.Errorf("forged value survived: xff=%q tenant=%q",
			out[HeaderForwardedFor], out[HeaderTrustedTenant])
	}

	// Non-managed header passes through unchanged.
	// Mutation: stripping non-managed headers makes this fail (empty/missing).
	if got, ok := out["User-Agent"]; !ok || got != "curl/8.0" {
		t.Errorf("User-Agent = %q (present=%v), want %q untouched", got, ok, "curl/8.0")
	}

	// Input map must not be mutated.
	if headers["X-Forwarded-For"] != "10.0.0.66" {
		t.Errorf("input map was mutated: XFF now %q", headers["X-Forwarded-For"])
	}
}

// TestSanitize_NoManagedHeadersStillSet covers CLOSURE clause 2:
// a request with NO managed headers still gets the authoritative managed
// values set from the connection.
func TestSanitize_NoManagedHeadersStillSet(t *testing.T) {
	id := runUUID(t)

	conn := ConnInfo{ClientIP: "198.51.100.42", Tenant: "tenant-beta"}
	s := NewSanitizer()

	headers := map[string]string{
		"Accept":       "application/json",
		"Content-Type": "application/json",
	}

	// before observable: no managed headers present at all.
	for _, h := range []string{HeaderForwardedFor, HeaderRealIP, HeaderTrustedTenant} {
		if _, ok := headers[h]; ok {
			t.Fatalf("precondition: managed header %q unexpectedly present", h)
		}
	}
	t.Logf("run=%s BEFORE managed-absent accept=%q ctype=%q",
		id, headers["Accept"], headers["Content-Type"])

	out := s.Sanitize(headers, conn)

	t.Logf("run=%s AFTER xff=%q realip=%q tenant=%q accept=%q",
		id, out[HeaderForwardedFor], out[HeaderRealIP],
		out[HeaderTrustedTenant], out["Accept"])

	// Managed values must now be present and equal to connection-derived ones.
	// Mutation: if step 2 (set-from-conn) were removed, these keys would be
	// absent/empty and fail.
	if got, ok := out[HeaderForwardedFor]; !ok || got != conn.ClientIP {
		t.Errorf("X-Forwarded-For = %q (present=%v), want %q", got, ok, conn.ClientIP)
	}
	if got, ok := out[HeaderRealIP]; !ok || got != conn.ClientIP {
		t.Errorf("X-Real-IP = %q (present=%v), want %q", got, ok, conn.ClientIP)
	}
	if got, ok := out[HeaderTrustedTenant]; !ok || got != conn.Tenant {
		t.Errorf("X-Trusted-Tenant = %q (present=%v), want %q", got, ok, conn.Tenant)
	}

	// Non-managed headers preserved.
	if out["Accept"] != "application/json" || out["Content-Type"] != "application/json" {
		t.Errorf("non-managed headers altered: accept=%q ctype=%q",
			out["Accept"], out["Content-Type"])
	}
}

// TestSanitize_CustomManagedHeaderStripped proves that a custom header added to
// the managed set is stripped of its forged client value even though it has no
// connection mapping (authoritative value is empty). Reinforces clause 1 for
// arbitrary managed headers.
func TestSanitize_CustomManagedHeaderStripped(t *testing.T) {
	id := runUUID(t)

	conn := ConnInfo{ClientIP: "192.0.2.9", Tenant: "tenant-gamma"}
	s := NewSanitizer("X-Internal-Admin")

	if !s.IsManaged("x-internal-admin") {
		t.Fatalf("custom header not registered as managed")
	}

	headers := map[string]string{
		"X-Internal-Admin": "true", // forged privilege escalation attempt
		"X-Request-Id":     "abc-123",
	}
	t.Logf("run=%s BEFORE admin=%q reqid=%q", id, headers["X-Internal-Admin"], headers["X-Request-Id"])

	out := s.Sanitize(headers, conn)
	t.Logf("run=%s AFTER admin=%q reqid=%q", id, out["X-Internal-Admin"], out["X-Request-Id"])

	// The forged "true" must not survive. Authoritative value is empty.
	// Mutation: pass-through leaves admin="true".
	if got := out["X-Internal-Admin"]; got != "" {
		t.Errorf("X-Internal-Admin = %q, want empty (forged value stripped)", got)
	}
	// Non-managed request id untouched.
	if out["X-Request-Id"] != "abc-123" {
		t.Errorf("X-Request-Id = %q, want %q untouched", out["X-Request-Id"], "abc-123")
	}
}

// TestManaged_CanonicalSet checks the default managed set is exactly the three
// standard headers in canonical form. Mutation: dropping a standard header from
// NewSanitizer fails this.
func TestManaged_CanonicalSet(t *testing.T) {
	id := runUUID(t)
	s := NewSanitizer()
	got := s.Managed()
	want := []string{"X-Forwarded-For", "X-Real-Ip", "X-Trusted-Tenant"}
	t.Logf("run=%s managed=%v", id, got)
	if len(got) != len(want) {
		t.Fatalf("managed set = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("managed[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
