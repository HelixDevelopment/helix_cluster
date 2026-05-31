package security

import (
	"fmt"
	"net/url"
	"strings"
)

// SPIFFEID represents a parsed SPIFFE identity.
type SPIFFEID struct {
	TrustDomain string
	Path        string
}

// String returns the canonical SPIFFE URI string.
func (s SPIFFEID) String() string {
	return fmt.Sprintf("spiffe://%s%s", s.TrustDomain, s.Path)
}

// ParseSPIFFEID parses a raw SPIFFE ID string.
func ParseSPIFFEID(raw string) (*SPIFFEID, error) {
	if !strings.HasPrefix(raw, "spiffe://") {
		return nil, fmt.Errorf("invalid SPIFFE ID: must start with spiffe://")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse SPIFFE ID: %w", err)
	}
	if u.Scheme != "spiffe" {
		return nil, fmt.Errorf("invalid scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("trust domain is required")
	}
	if u.User != nil {
		return nil, fmt.Errorf("userinfo is not allowed in SPIFFE ID")
	}
	if u.RawQuery != "" {
		return nil, fmt.Errorf("query is not allowed in SPIFFE ID")
	}
	if u.Fragment != "" {
		return nil, fmt.Errorf("fragment is not allowed in SPIFFE ID")
	}

	return &SPIFFEID{
		TrustDomain: u.Host,
		Path:        u.Path,
	}, nil
}

// IsValidTrustDomain returns true if the trust domain is non-empty and
// contains no invalid characters.
func IsValidTrustDomain(td string) bool {
	if td == "" {
		return false
	}
	// Reject spaces and control characters.
	for i := 0; i < len(td); i++ {
		c := td[i]
		if c <= ' ' || c == 0x7f {
			return false
		}
	}
	return true
}

// Validate checks the SPIFFE ID for well-formedness.
func (s *SPIFFEID) Validate() error {
	if s == nil {
		return fmt.Errorf("SPIFFE ID is nil")
	}
	if !IsValidTrustDomain(s.TrustDomain) {
		return fmt.Errorf("invalid trust domain: %q", s.TrustDomain)
	}
	if s.Path == "" {
		return fmt.Errorf("path is required")
	}
	if !strings.HasPrefix(s.Path, "/") {
		return fmt.Errorf("path must start with /")
	}
	return nil
}
