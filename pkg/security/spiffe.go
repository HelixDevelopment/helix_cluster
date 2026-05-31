package security

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

var (
	ErrInvalidSPIFFE      = errors.New("invalid SPIFFE ID")
	ErrInvalidScheme      = errors.New("invalid scheme")
	ErrMissingTrustDomain = errors.New("trust domain is required")
	ErrUserInfoNotAllowed = errors.New("userinfo is not allowed in SPIFFE ID")
	ErrQueryNotAllowed    = errors.New("query is not allowed in SPIFFE ID")
	ErrFragmentNotAllowed = errors.New("fragment is not allowed in SPIFFE ID")
	ErrNilSPIFFE          = errors.New("SPIFFE ID is nil")
	ErrInvalidTrustDomain = errors.New("invalid trust domain")
	ErrMissingPath        = errors.New("path is required")
	ErrInvalidPath        = errors.New("path must start with /")
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
		return nil, fmt.Errorf("invalid SPIFFE ID: must start with spiffe://: %w", ErrInvalidSPIFFE)
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse SPIFFE ID: %w", err)
	}
	if u.Scheme != "spiffe" {
		return nil, fmt.Errorf("invalid scheme %q: %w", u.Scheme, ErrInvalidScheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("trust domain is required: %w", ErrMissingTrustDomain)
	}
	if u.User != nil {
		return nil, fmt.Errorf("userinfo is not allowed in SPIFFE ID: %w", ErrUserInfoNotAllowed)
	}
	if u.RawQuery != "" {
		return nil, fmt.Errorf("query is not allowed in SPIFFE ID: %w", ErrQueryNotAllowed)
	}
	if u.Fragment != "" {
		return nil, fmt.Errorf("fragment is not allowed in SPIFFE ID: %w", ErrFragmentNotAllowed)
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
		return fmt.Errorf("SPIFFE ID is nil: %w", ErrNilSPIFFE)
	}
	if !IsValidTrustDomain(s.TrustDomain) {
		return fmt.Errorf("invalid trust domain: %q: %w", s.TrustDomain, ErrInvalidTrustDomain)
	}
	if s.Path == "" {
		return fmt.Errorf("path is required: %w", ErrMissingPath)
	}
	if !strings.HasPrefix(s.Path, "/") {
		return fmt.Errorf("path must start with /: %w", ErrInvalidPath)
	}
	return nil
}
