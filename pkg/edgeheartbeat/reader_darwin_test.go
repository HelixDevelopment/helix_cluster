//go:build darwin

package edgeheartbeat

import (
	"strconv"
	"testing"
)

func TestParsePmsetBattDischarging(t *testing.T) {
	raw := "Now drawing from 'Battery Power'\n" +
		" -InternalBattery-0 (id=12345)\t73%; discharging; 3:21 remaining present: true\n"
	pct, charging := parsePmsetBatt(raw)
	if pct != 73 {
		t.Fatalf("pct: got %d want 73", pct)
	}
	if charging {
		t.Fatal("should be discharging (not charging)")
	}
}

func TestParsePmsetBattACCharged(t *testing.T) {
	raw := "Now drawing from 'AC Power'\n" +
		" -InternalBattery-0 (id=22020195)\t100%; charged; 0:00 remaining present: true\n"
	pct, charging := parsePmsetBatt(raw)
	if pct != 100 {
		t.Fatalf("pct: got %d want 100", pct)
	}
	if !charging {
		t.Fatal("AC/charged should be charging")
	}
}

func TestParsePmsetBattCharging(t *testing.T) {
	raw := "Now drawing from 'AC Power'\n" +
		" -InternalBattery-0 (id=1)\t42%; charging; 1:10 remaining present: true\n"
	pct, charging := parsePmsetBatt(raw)
	if pct != 42 || !charging {
		t.Fatalf("got pct=%d charging=%v want 42/true", pct, charging)
	}
}

func TestParseDefaultRouteIface(t *testing.T) {
	raw := "   route to: default\ndestination: default\n    gateway: 10.6.100.250\n  interface: en0\n"
	if got := parseDefaultRouteIface(raw); got != "en0" {
		t.Fatalf("iface: got %q want en0", got)
	}
}

func TestLookupAndClassifyWiFi(t *testing.T) {
	ports := "\nHardware Port: Ethernet Adapter (en4)\nDevice: en4\n\n" +
		"Hardware Port: Wi-Fi\nDevice: en0\nEthernet Address: 60:3e:5f:82:f4:46\n"
	port := lookupHardwarePort(ports, "en0")
	if port != "Wi-Fi" {
		t.Fatalf("port: got %q want Wi-Fi", port)
	}
	if got := classifyNetworkPort(port, "en0"); got != NetworkWiFi {
		t.Fatalf("classify: got %q want wifi", got)
	}
}

func TestClassifyEthernetAndCellular(t *testing.T) {
	if got := classifyNetworkPort("Thunderbolt Ethernet", "en7"); got != NetworkEthernet {
		t.Fatalf("ethernet classify: got %q", got)
	}
	if got := classifyNetworkPort("iPhone USB", "en6"); got != NetworkCellular {
		t.Fatalf("cellular classify: got %q", got)
	}
	if got := classifyNetworkPort("", "lo0"); got != NetworkUnknown {
		t.Fatalf("unknown classify: got %q", got)
	}
}

func TestParsePowermetricsTemp(t *testing.T) {
	raw := "**** SMC sensors ****\n\nCPU die temperature: 47.12 C\nGPU die temperature: 39.0 C\n"
	v, ok := parsePowermetricsTemp(raw)
	if !ok || v != 47.12 {
		t.Fatalf("temp: got %.2f ok=%v want 47.12", v, ok)
	}
	if _, ok := parsePowermetricsTemp("no temp here"); ok {
		t.Fatal("expected no temp parsed from unrelated output")
	}
}

// TestDarwinSourceWithFakesProducesHeartbeat drives the REAL darwin source code
// path with injected fixture command output (no hardware), proving the source
// assembles a valid heartbeat end to end.
func TestDarwinSourceWithFakesProducesHeartbeat(t *testing.T) {
	src := &darwinSource{
		pmsetFn: func() (string, error) {
			return "Now drawing from 'Battery Power'\n -InternalBattery-0 (id=1)\t58%; discharging; 2:00 remaining present: true\n", nil
		},
		routeFn: func() (string, error) {
			return "  interface: en0\n", nil
		},
		hardwarePortFn: func() (string, error) {
			return "Hardware Port: Wi-Fi\nDevice: en0\n", nil
		},
		powermetricsFn: func() (string, error) {
			return "CPU die temperature: 52.0 C\n", nil
		},
	}
	em := NewEmitter("fixture-dev", src, nil)
	hb := em.Sample()
	if err := hb.Validate(); err != nil {
		t.Fatalf("heartbeat invalid: %v", err)
	}
	if hb.BatteryPercent != 58 || hb.Charging {
		t.Fatalf("battery: got %d charging=%v want 58/false", hb.BatteryPercent, hb.Charging)
	}
	if hb.NetworkType != NetworkWiFi {
		t.Fatalf("net: got %s want wifi", hb.NetworkType)
	}
	if hb.CPUTempC != 52.0 {
		t.Fatalf("temp: got %.1f want 52.0", hb.CPUTempC)
	}
}

// TestDarwinRealHostBattery reads the REAL host battery via pmset and asserts a
// plausible heartbeat is produced (CLAUDE-2: real per-OS path exercised on the
// host). Battery percent must be in [0,100]; network type must be a valid value;
// temperature is either a real reading or the honest sentinel.
func TestDarwinRealHostBattery(t *testing.T) {
	src := NewHostTelemetrySource()
	pct, _ := src.Battery()
	if pct < 0 || pct > 100 {
		t.Fatalf("real host battery %d out of [0,100]", pct)
	}
	nt := src.NetworkType()
	if _, ok := validNetworkTypes[nt]; !ok {
		t.Fatalf("real host network type %q not valid", nt)
	}
	temp := src.CPUTempC()
	if temp != CPUTempSentinel && (temp < -50 || temp > 150) {
		t.Fatalf("real host temp %.2f implausible", temp)
	}

	em := NewEmitter("real-host", src, nil)
	hb := em.Sample()
	if err := hb.Validate(); err != nil {
		t.Fatalf("real-host heartbeat failed validation: %v", err)
	}
	t.Logf("REAL host heartbeat: battery=%d%% charging=%v net=%s temp=%s",
		hb.BatteryPercent, hb.Charging, hb.NetworkType, tempStr(temp))
}

func tempStr(t float64) string {
	if t == CPUTempSentinel {
		return "unavailable"
	}
	return strconv.FormatFloat(t, 'f', -1, 64) + "C"
}
