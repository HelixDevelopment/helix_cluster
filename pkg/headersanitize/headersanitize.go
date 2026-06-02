// Package headersanitize provides spoof-proof managed-header sanitization.
//
// A request carries client-supplied headers (a map). Some headers are
// "managed": their values express trust assertions that an upstream proxy or
// gateway is responsible for setting (for example the real client IP via
// X-Forwarded-For / X-Real-IP, or a verified tenant via X-Trusted-Tenant).
// A client must never be allowed to dictate these values by simply supplying
// the header itself — doing so would let a caller spoof its source IP or
// impersonate another tenant.
//
// Sanitize strips any client-supplied managed header and re-sets the managed
// values authoritatively from trusted connection information. Non-managed
// headers pass through unchanged.
//
// The package is pure, deterministic, and has no external dependencies.
package headersanitize

import (
	"sort"
	"strings"
)

// Canonical managed-header names. These are the standard headers whose values
// carry trust assertions and therefore must be derived from the connection,
// not from the client.
const (
	HeaderForwardedFor = "X-Forwarded-For"
	// HeaderRealIP is given in canonical MIME form (X-Real-Ip) so that it
	// equals the key Sanitize emits in its output map. Canonicalization
	// title-cases each hyphen token, lowercasing the remainder, so "IP"
	// becomes "Ip".
	HeaderRealIP        = "X-Real-Ip"
	HeaderTrustedTenant = "X-Trusted-Tenant"
)

// ConnInfo carries the trusted, connection-derived values. These come from the
// actual transport (the peer's socket address, the verified TLS/mTLS identity,
// etc.), never from client-supplied request headers.
type ConnInfo struct {
	// ClientIP is the real remote client IP as observed on the connection.
	ClientIP string
	// Tenant is the verified tenant identity for this connection.
	Tenant string
}

// authoritativeValue returns the connection-derived value that a given managed
// header MUST hold, plus whether this header is governed by the connection.
// Headers in the managed set that have no connection mapping (custom managed
// headers) are governed too: their authoritative value is the empty string,
// meaning "strip whatever the client sent and leave nothing trusted".
func (s *Sanitizer) authoritativeValue(canonical string, conn ConnInfo) (string, bool) {
	switch canonical {
	case canonicalize(HeaderForwardedFor):
		return conn.ClientIP, true
	case canonicalize(HeaderRealIP):
		return conn.ClientIP, true
	case canonicalize(HeaderTrustedTenant):
		return conn.Tenant, true
	}
	return "", false
}

// Sanitizer enforces managed-header policy. The Managed set lists the headers
// (by canonical form) that are managed and must not be trusted from the client.
type Sanitizer struct {
	// managed holds canonicalized managed header names.
	managed map[string]struct{}
}

// NewSanitizer builds a Sanitizer whose managed set is the union of the
// supplied header names and the three standard managed headers. Names are
// canonicalized (HTTP-style: each hyphen-delimited token title-cased) so that
// client casing variants ("x-forwarded-for", "X-FORWARDED-FOR") are all caught.
func NewSanitizer(managed ...string) *Sanitizer {
	s := &Sanitizer{managed: make(map[string]struct{})}
	for _, h := range []string{HeaderForwardedFor, HeaderRealIP, HeaderTrustedTenant} {
		s.managed[canonicalize(h)] = struct{}{}
	}
	for _, h := range managed {
		if h == "" {
			continue
		}
		s.managed[canonicalize(h)] = struct{}{}
	}
	return s
}

// Managed returns the sorted canonical list of managed header names. Useful for
// tests and audits.
func (s *Sanitizer) Managed() []string {
	out := make([]string, 0, len(s.managed))
	for h := range s.managed {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// IsManaged reports whether the given header name (any casing) is managed.
func (s *Sanitizer) IsManaged(name string) bool {
	_, ok := s.managed[canonicalize(name)]
	return ok
}

// Sanitize returns a NEW header map in which:
//
//   - every client-supplied managed header is removed, then
//   - each managed header is set AUTHORITATIVELY from conn (so any forged
//     client value is gone/overwritten), and
//   - every non-managed header passes through unchanged.
//
// The input map is never mutated. Output keys are canonicalized.
func (s *Sanitizer) Sanitize(headers map[string]string, conn ConnInfo) map[string]string {
	out := make(map[string]string, len(headers)+len(s.managed))

	// 1. Copy through only NON-managed headers. Any managed header that the
	//    client supplied is dropped here (never trusted).
	for k, v := range headers {
		if s.IsManaged(k) {
			continue
		}
		out[canonicalize(k)] = v
	}

	// 2. Set every managed header authoritatively from the connection. This
	//    overwrites/installs the trusted value regardless of what (if anything)
	//    the client sent.
	for canonical := range s.managed {
		val, _ := s.authoritativeValue(canonical, conn)
		out[canonical] = val
	}

	return out
}

// canonicalize converts a header name to canonical MIME form without depending
// on net/textproto's allocation behavior: each '-'-separated token has its
// first letter upper-cased and the rest lower-cased. ASCII only, which is all
// HTTP header field names may contain.
func canonicalize(name string) string {
	if name == "" {
		return ""
	}
	tokens := strings.Split(name, "-")
	for i, tok := range tokens {
		tokens[i] = titleToken(tok)
	}
	return strings.Join(tokens, "-")
}

func titleToken(tok string) string {
	if tok == "" {
		return ""
	}
	b := []byte(strings.ToLower(tok))
	c := b[0]
	if c >= 'a' && c <= 'z' {
		b[0] = c - ('a' - 'A')
	}
	return string(b)
}
