package console

import "testing"

// ParseTrustLevel must tolerate surrounding whitespace because trust values
// frequently arrive from environment variables and node labels.
func TestParseTrustLevel_TrimsWhitespace(t *testing.T) {
	cases := map[string]TrustLevel{
		"  FULL":        TrustFull,
		"SEMI  ":        TrustSemi,
		"\tuntrusted\n": TrustUntrusted,
		" Full ":        TrustFull,
	}
	for in, want := range cases {
		got, err := ParseTrustLevel(in)
		if err != nil {
			t.Errorf("ParseTrustLevel(%q) unexpected error: %v", in, err)
			continue
		}
		// Mutation: remove strings.TrimSpace in ParseTrustLevel -> the padded
		// inputs fall through to the default error branch and this fails.
		if got != want {
			t.Errorf("ParseTrustLevel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseTrustLevel_WhitespaceOnlyStillInvalid(t *testing.T) {
	if _, err := ParseTrustLevel("   "); err == nil {
		t.Error("expected error for whitespace-only input")
	}
}

// AllTrustLevels must contain exactly the levels IsValid accepts, with no
// drift between the two declarations.
func TestAllTrustLevels_MatchesIsValid(t *testing.T) {
	for _, tl := range AllTrustLevels {
		if !tl.IsValid() {
			t.Errorf("AllTrustLevels contains %q which IsValid rejects", tl)
		}
	}
	// Mutation: add a bogus entry to AllTrustLevels -> the loop above fails.
	if len(AllTrustLevels) != 3 {
		t.Errorf("expected 3 trust levels, got %d", len(AllTrustLevels))
	}
}
