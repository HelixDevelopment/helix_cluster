//go:build darwin

package edgeheartbeat

import (
	"bufio"
	"os/exec"
	"strconv"
	"strings"
)

// darwinSource is the REAL macOS telemetry source (CLAUDE-2). It shells out to
// standard macOS tools that are present on every install:
//   - battery + charging: `pmset -g batt`
//   - network type:       `route -n get default` (default-route interface) mapped
//     to a hardware port via `networksetup -listallhardwareports`
//   - CPU temperature:    `powermetrics` (requires root); when unavailable the
//     honest CPUTempSentinel is returned rather than a fabricated value.
//
// Each external command is injectable (the *Fn fields) so unit tests can replay
// captured fixture output without touching real hardware, while production
// leaves them nil and calls the real tools.
type darwinSource struct {
	pmsetFn        func() (string, error)
	routeFn        func() (string, error)
	hardwarePortFn func() (string, error)
	powermetricsFn func() (string, error)
}

func newHostTelemetrySource() TelemetrySource { return &darwinSource{} }

func (s *darwinSource) pmset() (string, error) {
	if s.pmsetFn != nil {
		return s.pmsetFn()
	}
	out, err := exec.Command("pmset", "-g", "batt").Output()
	return string(out), err
}

func (s *darwinSource) route() (string, error) {
	if s.routeFn != nil {
		return s.routeFn()
	}
	out, err := exec.Command("route", "-n", "get", "default").Output()
	return string(out), err
}

func (s *darwinSource) hardwarePorts() (string, error) {
	if s.hardwarePortFn != nil {
		return s.hardwarePortFn()
	}
	out, err := exec.Command("networksetup", "-listallhardwareports").Output()
	return string(out), err
}

func (s *darwinSource) powermetrics() (string, error) {
	if s.powermetricsFn != nil {
		return s.powermetricsFn()
	}
	// -n 1: one sample; -i 200: 200ms; --samplers smc reads the SMC thermal
	// sensors. Requires root; on failure we fall back to the sentinel.
	out, err := exec.Command("powermetrics", "-n", "1", "-i", "200", "--samplers", "smc").Output()
	return string(out), err
}

// Battery reads `pmset -g batt`. Returns (percent, charging). On any parse
// failure it returns (100, true): a device whose battery cannot be read is
// treated as mains-powered, which is the safe scheduling default and matches a
// desktop/server with no battery.
func (s *darwinSource) Battery() (int, bool) {
	out, err := s.pmset()
	if err != nil {
		return 100, true
	}
	return parsePmsetBatt(out)
}

// parsePmsetBatt parses `pmset -g batt` output, e.g.:
//
//	Now drawing from 'AC Power'
//	 -InternalBattery-0 (id=...)\t100%; charged; 0:00 remaining present: true
//
// The percent token is "<n>%". Charging state is derived from the draw source
// line ("AC Power" => charging) and the per-battery status word ("charging",
// "charged", "AC attached" => charging; "discharging" => not).
func parsePmsetBatt(raw string) (int, bool) {
	percent := 100
	gotPercent := false
	charging := true

	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		low := strings.ToLower(line)
		if strings.HasPrefix(low, "now drawing from") {
			charging = strings.Contains(low, "ac power")
			continue
		}
		if !strings.Contains(line, "%") {
			continue
		}
		// Extract the "<n>%" token.
		if p, ok := parsePercentToken(line); ok {
			percent = p
			gotPercent = true
		}
		// Per-battery status overrides the draw-source guess.
		switch {
		case strings.Contains(low, "discharging"):
			charging = false
		case strings.Contains(low, "charging"), strings.Contains(low, "charged"),
			strings.Contains(low, "ac attached"):
			charging = true
		}
	}
	if !gotPercent {
		return 100, true
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	return percent, charging
}

// parsePercentToken finds the first token ending in '%' and parses the integer
// before it, e.g. "100%" -> 100.
func parsePercentToken(line string) (int, bool) {
	for _, tok := range strings.Fields(line) {
		t := strings.TrimRight(tok, ";,")
		if !strings.HasSuffix(t, "%") {
			continue
		}
		numStr := strings.TrimSuffix(t, "%")
		if n, err := strconv.Atoi(numStr); err == nil {
			return n, true
		}
	}
	return 0, false
}

// CPUTempC reads the SMC CPU die temperature via powermetrics. Requires root;
// when powermetrics is unavailable or yields no temperature, returns the honest
// CPUTempSentinel (CLAUDE-1: no fabricated readings).
func (s *darwinSource) CPUTempC() float64 {
	out, err := s.powermetrics()
	if err != nil {
		return CPUTempSentinel
	}
	if t, ok := parsePowermetricsTemp(out); ok {
		return t
	}
	return CPUTempSentinel
}

// parsePowermetricsTemp scans powermetrics SMC output for a CPU die temperature
// line, e.g. "CPU die temperature: 47.12 C". Returns (celsius, true) on success.
func parsePowermetricsTemp(raw string) (float64, bool) {
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		low := strings.ToLower(line)
		if !strings.Contains(low, "die temperature") && !strings.Contains(low, "cpu die") {
			continue
		}
		colon := strings.Index(line, ":")
		if colon < 0 {
			continue
		}
		fields := strings.Fields(line[colon+1:])
		if len(fields) == 0 {
			continue
		}
		if v, err := strconv.ParseFloat(strings.TrimRight(fields[0], "C"), 64); err == nil {
			return v, true
		}
	}
	return 0, false
}

// NetworkType determines the active link type from the default route's
// interface, mapped to a hardware-port label.
func (s *darwinSource) NetworkType() string {
	routeOut, err := s.route()
	if err != nil {
		return NetworkUnknown
	}
	iface := parseDefaultRouteIface(routeOut)
	if iface == "" {
		return NetworkUnknown
	}
	portsOut, err := s.hardwarePorts()
	if err != nil {
		return NetworkUnknown
	}
	port := lookupHardwarePort(portsOut, iface)
	return classifyNetworkPort(port, iface)
}

// parseDefaultRouteIface extracts the "interface:" field from `route get default`.
func parseDefaultRouteIface(raw string) string {
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "interface:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "interface:"))
		}
	}
	return ""
}

// lookupHardwarePort maps a device name (e.g. "en0") to its hardware port label
// (e.g. "Wi-Fi") using `networksetup -listallhardwareports` output, which lists
// repeating "Hardware Port: X" / "Device: Y" pairs.
func lookupHardwarePort(raw, device string) string {
	scanner := bufio.NewScanner(strings.NewReader(raw))
	curPort := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "Hardware Port:") {
			curPort = strings.TrimSpace(strings.TrimPrefix(line, "Hardware Port:"))
			continue
		}
		if strings.HasPrefix(line, "Device:") {
			dev := strings.TrimSpace(strings.TrimPrefix(line, "Device:"))
			if dev == device {
				return curPort
			}
		}
	}
	return ""
}

// classifyNetworkPort classifies a hardware-port label into a Network* type.
// Falls back to interface-name heuristics when the port label is empty/unknown.
func classifyNetworkPort(port, iface string) string {
	p := strings.ToLower(port)
	switch {
	case strings.Contains(p, "wi-fi"), strings.Contains(p, "wifi"), strings.Contains(p, "airport"):
		return NetworkWiFi
	case strings.Contains(p, "ethernet"), strings.Contains(p, "thunderbolt bridge"),
		strings.Contains(p, "lan"):
		return NetworkEthernet
	case strings.Contains(p, "iphone"), strings.Contains(p, "cellular"),
		strings.Contains(p, "modem"), strings.Contains(p, "wwan"):
		return NetworkCellular
	}
	// Heuristic fallback by interface name when no port label matched. en* may
	// be wired or Wi-Fi, so without a port label we cannot disambiguate and
	// report unknown rather than guessing. Cellular interfaces are name-distinct.
	if strings.HasPrefix(iface, "pdp_ip") || strings.HasPrefix(iface, "rmnet") {
		return NetworkCellular
	}
	return NetworkUnknown
}

var _ TelemetrySource = (*darwinSource)(nil)
