//go:build darwin

package powergater

import (
	"math"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestParsePmset_FixtureReplay drives the darwin pmset parser with captured
// real `pmset -g batt` output, asserting both the AC and battery cases. This is
// load-bearing: changing the AC/Battery discrimination breaks it.
func TestParsePmset_FixtureReplay(t *testing.T) {
	acFixture := "Now drawing from 'AC Power'\n" +
		" -InternalBattery-0 (id=22020195)\t100%; charged; 0:00 remaining present: true\n"
	battFixture := "Now drawing from 'Battery Power'\n" +
		" -InternalBattery-0 (id=22020195)\t63%; discharging; 2:14 remaining present: true\n"

	if !parsePmsetCharging(acFixture) {
		t.Errorf("AC fixture should be charging")
	}
	if parsePmsetCharging(battFixture) {
		t.Errorf("Battery fixture should NOT be charging")
	}
	if pct, ok := parsePmsetBattery(acFixture); !ok || pct != 100 {
		t.Errorf("AC fixture battery = %d,%v want 100,true", pct, ok)
	}
	if pct, ok := parsePmsetBattery(battFixture); !ok || pct != 63 {
		t.Errorf("Battery fixture battery = %d,%v want 63,true", pct, ok)
	}

	// No-battery (desktop) output yields no percentage token.
	if _, ok := parsePmsetBattery("Now drawing from 'AC Power'\n"); ok {
		t.Errorf("no-battery output should not yield a percentage")
	}
}

func TestParsePowermetricsTemp_Fixture(t *testing.T) {
	out := "*** Sampled system activity ***\n" +
		"CPU die temperature: 52.4 C\n" +
		"GPU die temperature: 41.0 C\n"
	temp, ok := parsePowermetricsTemp(out)
	if !ok || math.Abs(temp-52.4) > 0.001 {
		t.Errorf("temp = %v,%v want 52.4,true", temp, ok)
	}
	if _, ok := parsePowermetricsTemp("no temperature here"); ok {
		t.Errorf("absent temperature should report not-found")
	}
}

// TestDarwinSource_RealHostBattery is the CLAUDE-2 integration test: it reads
// the REAL host battery via the darwinSource and asserts it matches an
// independent `pmset -g batt` oracle invoked separately. No skip — pmset is
// present on every macOS host.
func TestDarwinSource_RealHostBattery(t *testing.T) {
	out, err := exec.Command("pmset", "-g", "batt").Output()
	if err != nil {
		t.Fatalf("oracle pmset failed: %v", err)
	}
	oracle := string(out)

	src := NewPowerSource()
	gotPct := src.BatteryPercent()
	gotCharging := src.Charging()

	if gotPct < 0 || gotPct > 100 {
		t.Fatalf("reader battery %% = %d out of range", gotPct)
	}

	// Oracle percentage (if the host has a battery).
	if m := regexp.MustCompile(`(\d+)%`).FindStringSubmatch(oracle); m != nil {
		want, _ := strconv.Atoi(m[1])
		if gotPct != want {
			t.Errorf("reader battery %d%% != pmset oracle %d%%", gotPct, want)
		}
	} else if gotPct != 100 {
		t.Errorf("no-battery host should read 100%%, got %d", gotPct)
	}

	// Oracle charging state.
	lower := strings.ToLower(oracle)
	wantCharging := strings.Contains(lower, "drawing from 'ac power'")
	if strings.Contains(lower, "drawing from 'battery power'") {
		wantCharging = false
	}
	if gotCharging != wantCharging {
		t.Errorf("reader charging=%v != pmset oracle charging=%v\noracle: %s",
			gotCharging, wantCharging, oracle)
	}

	t.Logf("REAL host: battery=%d%% charging=%v (oracle ok)", gotPct, gotCharging)
}

// TestDarwinSource_CPUTempNeverPanics confirms CPUTempC returns a value or the
// documented TempUnknown sentinel without panicking, on a host where
// powermetrics may require root.
func TestDarwinSource_CPUTempNeverPanics(t *testing.T) {
	temp := NewPowerSource().CPUTempC()
	if !math.IsNaN(temp) && (temp < -50 || temp > 150) {
		t.Errorf("implausible temp %v", temp)
	}
}
