package console

import (
	"fmt"
	"strings"
)

// TrustLevel represents the trust tier of a console node.
type TrustLevel string

const (
	// Full trust is reserved for nodes with verified secure boot,
	// measured boot, and hardware attestation.
	TrustFull TrustLevel = "FULL"

	// Semi trust is the default for console nodes that register
	// successfully but lack full attestation.
	TrustSemi TrustLevel = "SEMI"

	// Untrusted is assigned to nodes that fail attestation checks
	// or exhibit anomalous behavior.
	TrustUntrusted TrustLevel = "UNTRUSTED"
)

// AllTrustLevels contains every valid trust level.
var AllTrustLevels = []TrustLevel{TrustFull, TrustSemi, TrustUntrusted}

// ParseTrustLevel parses a trust level string (case-insensitive).
// Surrounding whitespace is trimmed so values sourced from environment
// variables or node labels parse cleanly.
func ParseTrustLevel(s string) (TrustLevel, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "FULL":
		return TrustFull, nil
	case "SEMI":
		return TrustSemi, nil
	case "UNTRUSTED":
		return TrustUntrusted, nil
	default:
		return "", fmt.Errorf("invalid trust level: %q", s)
	}
}

// IsValid returns true if the trust level is one of the known values.
func (tl TrustLevel) IsValid() bool {
	switch tl {
	case TrustFull, TrustSemi, TrustUntrusted:
		return true
	}
	return false
}

// String returns the string representation.
func (tl TrustLevel) String() string {
	return string(tl)
}
