// Package build provides build orchestration primitives for Helix Cluster OS.
package build

import (
	"fmt"
	"strings"
)

// Platform represents an OS/Architecture/Variant target for multi-arch builds.
type Platform struct {
	OS      string
	Arch    string
	Variant string // optional, e.g. "v8" for arm64/v8
}

// ParsePlatform parses a platform string such as "linux/amd64" or "linux/arm64/v8".
func ParsePlatform(s string) (Platform, error) {
	parts := strings.Split(s, "/")
	if len(parts) < 2 || len(parts) > 3 {
		return Platform{}, fmt.Errorf("invalid platform %q: expected os/arch or os/arch/variant", s)
	}
	p := Platform{
		OS:   strings.ToLower(strings.TrimSpace(parts[0])),
		Arch: strings.ToLower(strings.TrimSpace(parts[1])),
	}
	if len(parts) == 3 {
		p.Variant = strings.ToLower(strings.TrimSpace(parts[2]))
	}
	if p.OS == "" || p.Arch == "" {
		return Platform{}, fmt.Errorf("invalid platform %q: OS and Arch must be non-empty", s)
	}
	return p, nil
}

// String returns the canonical platform string.
func (p Platform) String() string {
	if p.Variant != "" {
		return fmt.Sprintf("%s/%s/%s", p.OS, p.Arch, p.Variant)
	}
	return fmt.Sprintf("%s/%s", p.OS, p.Arch)
}
