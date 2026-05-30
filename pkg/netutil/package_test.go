package netutil

import "testing"

func TestGetLocalIP(t *testing.T) {
	ip, err := GetLocalIP()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ip == "" {
		t.Error("expected non-empty IP")
	}
}

func TestIsValidPort(t *testing.T) {
	if !IsValidPort(8080) {
		t.Error("expected 8080 to be valid")
	}
	if IsValidPort(0) {
		t.Error("expected 0 to be invalid")
	}
	if IsValidPort(70000) {
		t.Error("expected 70000 to be invalid")
	}
}
