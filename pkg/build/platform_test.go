package build

import (
	"testing"
)

func TestParsePlatform(t *testing.T) {
	cases := []struct {
		input       string
		wantOS      string
		wantArch    string
		wantVariant string
		wantErr     bool
	}{
		{"linux/amd64", "linux", "amd64", "", false},
		{"linux/arm64/v8", "linux", "arm64", "v8", false},
		{"windows/amd64", "windows", "amd64", "", false},
		{"darwin/arm64", "darwin", "arm64", "", false},
		{"linux/ arm64", "linux", "arm64", "", false},
		{"Linux/AMD64", "linux", "amd64", "", false},
		{"invalid", "", "", "", true},
		{"/amd64", "", "", "", true},
		{"linux/", "", "", "", true},
		{"", "", "", "", true},
	}

	for _, tc := range cases {
		p, err := ParsePlatform(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParsePlatform(%q) expected error", tc.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParsePlatform(%q) unexpected error: %v", tc.input, err)
			continue
		}
		if p.OS != tc.wantOS {
			t.Errorf("ParsePlatform(%q) OS=%q, want %q", tc.input, p.OS, tc.wantOS)
		}
		if p.Arch != tc.wantArch {
			t.Errorf("ParsePlatform(%q) Arch=%q, want %q", tc.input, p.Arch, tc.wantArch)
		}
		if p.Variant != tc.wantVariant {
			t.Errorf("ParsePlatform(%q) Variant=%q, want %q", tc.input, p.Variant, tc.wantVariant)
		}
	}
}

func TestPlatformString(t *testing.T) {
	cases := []struct {
		p    Platform
		want string
	}{
		{Platform{OS: "linux", Arch: "amd64"}, "linux/amd64"},
		{Platform{OS: "linux", Arch: "arm64", Variant: "v8"}, "linux/arm64/v8"},
		{Platform{OS: "windows", Arch: "386"}, "windows/386"},
	}

	for _, tc := range cases {
		got := tc.p.String()
		if got != tc.want {
			t.Errorf("Platform.String() = %q, want %q", got, tc.want)
		}
	}
}

func TestParsePlatformRoundTrip(t *testing.T) {
	originals := []string{
		"linux/amd64",
		"linux/arm64/v8",
		"windows/386",
	}
	for _, orig := range originals {
		p, err := ParsePlatform(orig)
		if err != nil {
			t.Fatalf("ParsePlatform(%q) error: %v", orig, err)
		}
		if got := p.String(); got != orig {
			t.Errorf("round-trip failed: %q -> %q", orig, got)
		}
	}
}
