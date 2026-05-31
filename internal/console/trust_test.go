package console

import (
	"testing"
)

func TestParseTrustLevel(t *testing.T) {
	tests := []struct {
		input   string
		want    TrustLevel
		wantErr bool
	}{
		{"FULL", TrustFull, false},
		{"SEMI", TrustSemi, false},
		{"UNTRUSTED", TrustUntrusted, false},
		{"full", TrustFull, false},
		{"semi", TrustSemi, false},
		{"untrusted", TrustUntrusted, false},
		{"invalid", "", true},
		{"", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseTrustLevel(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseTrustLevel(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ParseTrustLevel(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestTrustLevel_IsValid(t *testing.T) {
	if !TrustFull.IsValid() {
		t.Error("FULL should be valid")
	}
	if !TrustSemi.IsValid() {
		t.Error("SEMI should be valid")
	}
	if !TrustUntrusted.IsValid() {
		t.Error("UNTRUSTED should be valid")
	}
	if TrustLevel("BAD").IsValid() {
		t.Error("BAD should be invalid")
	}
}

func TestTrustLevel_String(t *testing.T) {
	if TrustFull.String() != "FULL" {
		t.Errorf("expected FULL, got %s", TrustFull.String())
	}
}

func TestAllTrustLevels(t *testing.T) {
	if len(AllTrustLevels) != 3 {
		t.Errorf("expected 3 trust levels, got %d", len(AllTrustLevels))
	}
}
