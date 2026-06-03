//go:build linux

package edgeheartbeat

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// linuxSource is the REAL Linux telemetry source (CLAUDE-2), reading directly
// from the kernel-exported sysfs/procfs files that are present on Linux:
//   - battery + charging: /sys/class/power_supply/BAT*/{capacity,status} and
//     AC*/online
//   - CPU temperature:    /sys/class/thermal/thermal_zone*/temp (millidegrees C)
//   - network type:       the default-route interface from /proc/net/route,
//     classified via /sys/class/net/<if>/{wireless,type} and /proc/net/wireless
//
// The filesystem root is injectable (root field) so unit tests can point at a
// fixture tree without root or special hardware.
type linuxSource struct {
	root string // filesystem root; "" means the real "/"
}

func newHostTelemetrySource() TelemetrySource { return &linuxSource{} }

func (s *linuxSource) path(p string) string {
	if s.root == "" {
		return p
	}
	return filepath.Join(s.root, p)
}

// Battery reads the first BAT* power supply's capacity and status. If no battery
// exists, an AC*/online entry reading 1 yields (100, true); a system with
// neither reports (100, true) as the safe mains-powered default.
func (s *linuxSource) Battery() (int, bool) {
	base := s.path("/sys/class/power_supply")
	entries, err := os.ReadDir(base)
	if err != nil {
		return 100, true
	}
	percent := 100
	gotBattery := false
	charging := true
	acOnline := false
	gotAC := false

	for _, e := range entries {
		name := e.Name()
		dir := filepath.Join(base, name)
		typ := strings.TrimSpace(readFile(filepath.Join(dir, "type")))
		upper := strings.ToUpper(name)
		if typ == "Battery" || strings.HasPrefix(upper, "BAT") {
			if cap, ok := readInt(filepath.Join(dir, "capacity")); ok {
				percent = cap
				gotBattery = true
			}
			status := strings.ToLower(strings.TrimSpace(readFile(filepath.Join(dir, "status"))))
			switch status {
			case "discharging":
				charging = false
			case "charging", "full", "not charging":
				// "not charging" means plugged in but battery full enough.
				charging = true
			}
			continue
		}
		if typ == "Mains" || strings.HasPrefix(upper, "AC") || strings.HasPrefix(upper, "ADP") {
			if v, ok := readInt(filepath.Join(dir, "online")); ok {
				gotAC = true
				acOnline = acOnline || v == 1
			}
		}
	}

	if !gotBattery {
		// No battery: charging reflects AC presence if known, else assume mains.
		if gotAC {
			return 100, acOnline
		}
		return 100, true
	}
	// With a battery present, AC online overrides an ambiguous status.
	if gotAC && acOnline {
		charging = true
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	return percent, charging
}

// CPUTempC returns the highest thermal-zone temperature (the package/CPU hot
// spot) from /sys/class/thermal, converting millidegrees to Celsius. Returns
// CPUTempSentinel when no thermal zone is readable.
func (s *linuxSource) CPUTempC() float64 {
	base := s.path("/sys/class/thermal")
	entries, err := os.ReadDir(base)
	if err != nil {
		return CPUTempSentinel
	}
	best := CPUTempSentinel
	found := false
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "thermal_zone") {
			continue
		}
		milli, ok := readInt(filepath.Join(base, e.Name(), "temp"))
		if !ok {
			continue
		}
		c := float64(milli) / 1000.0
		if !found || c > best {
			best = c
			found = true
		}
	}
	if !found {
		return CPUTempSentinel
	}
	return best
}

// NetworkType classifies the default-route interface. Wireless interfaces appear
// in /proc/net/wireless or expose /sys/class/net/<if>/wireless. Cellular
// interfaces use ARPHRD types or rmnet/wwan naming. Everything else with a
// carrier is treated as ethernet.
func (s *linuxSource) NetworkType() string {
	iface := s.defaultRouteIface()
	if iface == "" {
		return NetworkUnknown
	}
	// Wireless: presence of the wireless sysfs dir or a /proc/net/wireless row.
	if dirExists(s.path(filepath.Join("/sys/class/net", iface, "wireless"))) {
		return NetworkWiFi
	}
	if s.ifaceInProcWireless(iface) {
		return NetworkWiFi
	}
	low := strings.ToLower(iface)
	if strings.HasPrefix(low, "rmnet") || strings.HasPrefix(low, "wwan") ||
		strings.HasPrefix(low, "ppp") || strings.HasPrefix(low, "pdp_ip") {
		return NetworkCellular
	}
	// "en" already covers eno*/enp*/ens* predictable names.
	if strings.HasPrefix(low, "eth") || strings.HasPrefix(low, "en") {
		return NetworkEthernet
	}
	// type 1 == ARPHRD_ETHER; many wired and bridged links report this.
	if v, ok := readInt(s.path(filepath.Join("/sys/class/net", iface, "type"))); ok && v == 1 {
		return NetworkEthernet
	}
	return NetworkUnknown
}

// defaultRouteIface parses /proc/net/route and returns the interface for the
// default route (Destination == 00000000).
func (s *linuxSource) defaultRouteIface() string {
	data := readFile(s.path("/proc/net/route"))
	if data == "" {
		return ""
	}
	lines := strings.Split(data, "\n")
	for i, line := range lines {
		if i == 0 { // header
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[1] == "00000000" {
			return fields[0]
		}
	}
	return ""
}

// ifaceInProcWireless reports whether iface has a row in /proc/net/wireless.
func (s *linuxSource) ifaceInProcWireless(iface string) bool {
	data := readFile(s.path("/proc/net/wireless"))
	if data == "" {
		return false
	}
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, iface+":") {
			return true
		}
	}
	return false
}

func readFile(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

func readInt(p string) (int, bool) {
	s := strings.TrimSpace(readFile(p))
	if s == "" {
		return 0, false
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return v, true
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

var _ TelemetrySource = (*linuxSource)(nil)
