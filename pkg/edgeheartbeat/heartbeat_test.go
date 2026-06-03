package edgeheartbeat

import (
	"testing"
	"time"
)

func TestHeartbeatEncodeDecodeRoundTrip(t *testing.T) {
	in := Heartbeat{
		DeviceID:       "dev-1",
		Timestamp:      time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC),
		Seq:            7,
		BatteryPercent: 64,
		Charging:       true,
		CPUTempC:       48.5,
		NetworkType:    NetworkWiFi,
	}
	raw, err := in.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out, err := DecodeHeartbeat(raw)
	if err != nil {
		t.Fatalf("DecodeHeartbeat: %v", err)
	}
	if out != in {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", out, in)
	}
}

func TestValidateRejectsBadBattery(t *testing.T) {
	for _, pct := range []int{-1, 101, 200} {
		h := Heartbeat{DeviceID: "d", BatteryPercent: pct, NetworkType: NetworkWiFi}
		if err := h.Validate(); err == nil {
			t.Fatalf("expected error for battery %d", pct)
		}
	}
}

func TestValidateRejectsBadNetworkType(t *testing.T) {
	h := Heartbeat{DeviceID: "d", BatteryPercent: 50, NetworkType: "satellite"}
	if err := h.Validate(); err == nil {
		t.Fatal("expected error for unknown network type")
	}
}

func TestValidateAcceptsSentinelTempButRejectsImplausible(t *testing.T) {
	ok := Heartbeat{DeviceID: "d", BatteryPercent: 50, NetworkType: NetworkEthernet, CPUTempC: CPUTempSentinel}
	if err := ok.Validate(); err != nil {
		t.Fatalf("sentinel temp should validate: %v", err)
	}
	bad := Heartbeat{DeviceID: "d", BatteryPercent: 50, NetworkType: NetworkEthernet, CPUTempC: 999}
	if err := bad.Validate(); err == nil {
		t.Fatal("expected error for implausible temp")
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	if _, err := DecodeHeartbeat([]byte("{not json")); err == nil {
		t.Fatal("expected decode error on garbage")
	}
	// Valid JSON but invalid battery must also be rejected.
	if _, err := DecodeHeartbeat([]byte(`{"device_id":"d","battery_percent":150,"network_type":"wifi"}`)); err == nil {
		t.Fatal("expected validation error on bad battery")
	}
}
