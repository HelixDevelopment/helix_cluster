//go:build linux

package edgeheartbeat

import (
	"os"
	"path/filepath"
	"testing"
)

// buildFakeSysfs creates a temporary sysfs/procfs-like tree and returns its root.
func buildFakeSysfs(t *testing.T, battCap, battStatus, acOnline, thermalMilli, defaultIface string, wireless bool) string {
	t.Helper()
	root := t.TempDir()

	ps := filepath.Join(root, "sys/class/power_supply")
	if battCap != "" {
		bat := filepath.Join(ps, "BAT0")
		mustWrite(t, filepath.Join(bat, "type"), "Battery\n")
		mustWrite(t, filepath.Join(bat, "capacity"), battCap+"\n")
		mustWrite(t, filepath.Join(bat, "status"), battStatus+"\n")
	}
	if acOnline != "" {
		ac := filepath.Join(ps, "AC")
		mustWrite(t, filepath.Join(ac, "type"), "Mains\n")
		mustWrite(t, filepath.Join(ac, "online"), acOnline+"\n")
	}

	if thermalMilli != "" {
		tz := filepath.Join(root, "sys/class/thermal/thermal_zone0")
		mustWrite(t, filepath.Join(tz, "temp"), thermalMilli+"\n")
	}

	if defaultIface != "" {
		// /proc/net/route: header + one default row (Destination 00000000).
		route := "Iface\tDestination\tGateway\tFlags\n" +
			defaultIface + "\t00000000\t0102A8C0\t0003\n"
		mustWrite(t, filepath.Join(root, "proc/net/route"), route)
		// Mark interface wireless via sysfs dir if requested.
		if wireless {
			if err := os.MkdirAll(filepath.Join(root, "sys/class/net", defaultIface, "wireless"), 0o755); err != nil {
				t.Fatal(err)
			}
		} else {
			// ARPHRD_ETHER type=1 for a wired link.
			mustWrite(t, filepath.Join(root, "sys/class/net", defaultIface, "type"), "1\n")
		}
	}
	return root
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxBatteryDischarging(t *testing.T) {
	root := buildFakeSysfs(t, "64", "Discharging", "0", "45000", "eth0", false)
	s := &linuxSource{root: root}
	pct, charging := s.Battery()
	if pct != 64 {
		t.Fatalf("pct: got %d want 64", pct)
	}
	if charging {
		t.Fatal("should be discharging")
	}
}

func TestLinuxBatteryChargingViaAC(t *testing.T) {
	root := buildFakeSysfs(t, "80", "Charging", "1", "45000", "eth0", false)
	s := &linuxSource{root: root}
	pct, charging := s.Battery()
	if pct != 80 || !charging {
		t.Fatalf("got %d/%v want 80/true", pct, charging)
	}
}

func TestLinuxNoBatteryMainsOnline(t *testing.T) {
	root := buildFakeSysfs(t, "", "", "1", "40000", "eth0", false)
	s := &linuxSource{root: root}
	pct, charging := s.Battery()
	if pct != 100 || !charging {
		t.Fatalf("mains-only: got %d/%v want 100/true", pct, charging)
	}
}

func TestLinuxThermalHighestZone(t *testing.T) {
	root := buildFakeSysfs(t, "50", "Discharging", "0", "53000", "eth0", false)
	// add a second, hotter zone.
	mustWrite(t, filepath.Join(root, "sys/class/thermal/thermal_zone1/temp"), "61500\n")
	s := &linuxSource{root: root}
	if got := s.CPUTempC(); got != 61.5 {
		t.Fatalf("temp: got %.1f want 61.5 (hottest zone)", got)
	}
}

func TestLinuxThermalSentinelWhenAbsent(t *testing.T) {
	root := buildFakeSysfs(t, "50", "Discharging", "0", "", "eth0", false)
	s := &linuxSource{root: root}
	if got := s.CPUTempC(); got != CPUTempSentinel {
		t.Fatalf("expected sentinel, got %.2f", got)
	}
}

func TestLinuxNetworkEthernet(t *testing.T) {
	root := buildFakeSysfs(t, "50", "Discharging", "0", "45000", "eth0", false)
	s := &linuxSource{root: root}
	if got := s.NetworkType(); got != NetworkEthernet {
		t.Fatalf("net: got %s want ethernet", got)
	}
}

func TestLinuxNetworkWiFi(t *testing.T) {
	root := buildFakeSysfs(t, "50", "Discharging", "0", "45000", "wlan0", true)
	s := &linuxSource{root: root}
	if got := s.NetworkType(); got != NetworkWiFi {
		t.Fatalf("net: got %s want wifi", got)
	}
}

func TestLinuxNetworkCellular(t *testing.T) {
	root := buildFakeSysfs(t, "50", "Discharging", "0", "45000", "rmnet0", false)
	s := &linuxSource{root: root}
	if got := s.NetworkType(); got != NetworkCellular {
		t.Fatalf("net: got %s want cellular", got)
	}
}

// TestLinuxRealHostIfAvailable exercises the REAL host sysfs path when running
// on actual Linux hardware (root=""). It asserts plausibility, not exact values.
func TestLinuxRealHostIfAvailable(t *testing.T) {
	if _, err := os.Stat("/sys/class/net"); err != nil {
		t.Skip("no /sys/class/net on this host; real Linux sysfs unavailable")
	}
	s := &linuxSource{}
	pct, _ := s.Battery()
	if pct < 0 || pct > 100 {
		t.Fatalf("real host battery %d out of range", pct)
	}
	nt := s.NetworkType()
	if _, ok := validNetworkTypes[nt]; !ok {
		t.Fatalf("real host net type %q invalid", nt)
	}
	temp := s.CPUTempC()
	if temp != CPUTempSentinel && (temp < -50 || temp > 150) {
		t.Fatalf("real host temp %.2f implausible", temp)
	}
	t.Logf("REAL linux host: battery=%d%% net=%s temp=%.2f", pct, nt, temp)
}
