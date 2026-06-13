package security

import (
	"testing"
)

func TestParseSPIFFEID(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantTD   string
		wantPath string
		wantErr  bool
	}{
		{"valid workload", "spiffe://example.org/ns/default/sa/my-sa", "example.org", "/ns/default/sa/my-sa", false},
		{"valid root path", "spiffe://example.org/", "example.org", "/", false},
		{"missing scheme", "example.org/ns/default", "", "", true},
		{"wrong scheme", "https://example.org/ns/default", "", "", true},
		{"missing trust domain", "spiffe:///path", "", "", true},
		{"with userinfo", "spiffe://user@example.org/path", "", "", true},
		{"with query", "spiffe://example.org/path?foo=bar", "", "", true},
		{"with fragment", "spiffe://example.org/path#frag", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := ParseSPIFFEID(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseSPIFFEID(%q) error = %v, wantErr %v", tt.raw, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if id.TrustDomain != tt.wantTD {
				t.Errorf("trust domain = %q, want %q", id.TrustDomain, tt.wantTD)
			}
			if id.Path != tt.wantPath {
				t.Errorf("path = %q, want %q", id.Path, tt.wantPath)
			}
			if id.String() != tt.raw {
				t.Errorf("String() = %q, want %q", id.String(), tt.raw)
			}
		})
	}
}

func TestIsValidTrustDomain(t *testing.T) {
	if !IsValidTrustDomain("example.org") {
		t.Error("expected example.org to be valid")
	}
	if IsValidTrustDomain("") {
		t.Error("expected empty string to be invalid")
	}
	if IsValidTrustDomain("bad domain") {
		t.Error("expected space-containing domain to be invalid")
	}
	if IsValidTrustDomain("bad\x00domain") {
		t.Error("expected null-containing domain to be invalid")
	}
}

func TestSPIFFEID_Validate(t *testing.T) {
	if err := (&SPIFFEID{TrustDomain: "example.org", Path: "/workload"}).Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := (&SPIFFEID{TrustDomain: "", Path: "/workload"}).Validate(); err == nil {
		t.Error("expected error for empty trust domain")
	}
	if err := (&SPIFFEID{TrustDomain: "example.org", Path: ""}).Validate(); err == nil {
		t.Error("expected error for empty path")
	}
	if err := (&SPIFFEID{TrustDomain: "example.org", Path: "no-leading-slash"}).Validate(); err == nil {
		t.Error("expected error for path without leading slash")
	}
	if err := (*SPIFFEID)(nil).Validate(); err == nil {
		t.Error("expected error for nil SPIFFE ID")
	}
}
