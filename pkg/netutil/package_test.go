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

// --- Mutation tests ---

func TestIsValidPort_BoundaryLow_Mutation(t *testing.T) {
	// Mutation: lower bound check removed or changed → port 0 or negative considered valid
	if IsValidPort(0) {
		t.Error("port 0 must be invalid")
	}
	if IsValidPort(-1) {
		t.Error("negative port must be invalid")
	}
	if !IsValidPort(1) {
		t.Error("port 1 must be valid")
	}
}

func TestIsValidPort_BoundaryHigh_Mutation(t *testing.T) {
	// Mutation: upper bound check removed or changed → port > 65535 considered valid
	if IsValidPort(65536) {
		t.Error("port 65536 must be invalid")
	}
	if !IsValidPort(65535) {
		t.Error("port 65535 must be valid")
	}
}

func TestGetLocalIP_FallbackLoopback_Mutation(t *testing.T) {
	// Mutation: GetLocalIP returns empty string instead of fallback 127.0.0.1
	ip, err := GetLocalIP()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ip == "" {
		t.Error("GetLocalIP must never return an empty string")
	}
}
