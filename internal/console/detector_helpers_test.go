package console

import (
	"os"
	"path/filepath"
	"testing"
)

// These tests exercise the OS-independent parsing/scoring helpers directly so
// the detection logic is validated on every platform (the public Detect() is
// gated behind runtime.GOOS == "linux"). The unexported helpers do not consult
// runtime.GOOS, so they are safe to call here.

func TestCheckCPUInfo_JaguarScoresPS4(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cpuinfo")
	// AMD Jaguar alone yields a 0.4 partial score and PS4 classification.
	if err := os.WriteFile(p, []byte("vendor_id\t: AuthenticAMD\nmodel name\t: AMD Jaguar APU\n"), 0644); err != nil {
		t.Fatal(err)
	}
	d := NewDetector()
	d.procCPUInfoPath = p

	ct, score, err := d.checkCPUInfo()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ct != TypePS4 {
		t.Errorf("expected TypePS4, got %s", ct)
	}
	// Mutation that fails this: change `score += 0.4` for jaguar to `score += 0.0`.
	if score < 0.39 || score > 0.41 {
		t.Errorf("expected jaguar partial score ~0.4, got %f", score)
	}
}

func TestCheckCPUInfo_Zen2ScoresPS5(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cpuinfo")
	if err := os.WriteFile(p, []byte("model name\t: AMD Custom APU (Zen 2)\n"), 0644); err != nil {
		t.Fatal(err)
	}
	d := NewDetector()
	d.procCPUInfoPath = p

	ct, score, err := d.checkCPUInfo()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Mutation that fails this: change detected = TypePS5 to detected = TypePS4
	// in the "zen 2" branch.
	if ct != TypePS5 {
		t.Errorf("expected TypePS5, got %s", ct)
	}
	if score < 0.39 {
		t.Errorf("expected partial score, got %f", score)
	}
}

func TestCheckCPUInfo_ModelHintCaps(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cpuinfo")
	// jaguar (0.4) + "playstation 5"/"ps5" hint (0.6) = 1.0 exactly, capped.
	if err := os.WriteFile(p, []byte("model name\t: AMD Jaguar PlayStation 5 (ps5)\n"), 0644); err != nil {
		t.Fatal(err)
	}
	d := NewDetector()
	d.procCPUInfoPath = p

	ct, score, err := d.checkCPUInfo()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ct != TypePS5 {
		t.Errorf("expected TypePS5 from ps5 hint, got %s", ct)
	}
	// jaguar(+0.4) + playstation/ps5 hint(+0.6) == 1.0 exactly here. The cap is
	// belt-and-suspenders; this asserts the value never exceeds 1.0.
	if score > 1.0 {
		t.Errorf("score must be capped at 1.0, got %f", score)
	}
	if score < 0.99 {
		t.Errorf("expected score saturated near 1.0, got %f", score)
	}
}

func TestCheckCPUInfo_GenericNoMatch(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cpuinfo")
	if err := os.WriteFile(p, []byte("model name\t: Intel(R) Core(TM) i9-13900K\n"), 0644); err != nil {
		t.Fatal(err)
	}
	d := NewDetector()
	d.procCPUInfoPath = p

	ct, score, err := d.checkCPUInfo()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ct != TypeUnknown {
		t.Errorf("expected TypeUnknown for generic CPU, got %s", ct)
	}
	// Mutation that fails this: initialize score := 1.0 instead of 0.0.
	if score != 0.0 {
		t.Errorf("expected zero score, got %f", score)
	}
}

func TestCheckCPUInfo_MissingFile(t *testing.T) {
	d := NewDetector()
	d.procCPUInfoPath = filepath.Join(t.TempDir(), "does-not-exist")
	_, _, err := d.checkCPUInfo()
	if err == nil {
		t.Fatal("expected error opening missing cpuinfo")
	}
	if !os.IsNotExist(err) {
		t.Errorf("expected not-exist error, got %v", err)
	}
}

func TestCheckDeviceTree_PS5(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compatible"), []byte("sony,playstation5\x00"), 0644); err != nil {
		t.Fatal(err)
	}
	d := NewDetector()
	d.deviceTreePath = dir

	ct, score, err := d.checkDeviceTree()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ct != TypePS5 {
		t.Errorf("expected TypePS5, got %s", ct)
	}
	// Mutation that fails this: change score = 1.0 to score = 0.0 in the ps5 branch.
	if score != 1.0 {
		t.Errorf("expected score 1.0, got %f", score)
	}
}

func TestCheckDeviceTree_PS4(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compatible"), []byte("sony,ps4-pro\x00"), 0644); err != nil {
		t.Fatal(err)
	}
	d := NewDetector()
	d.deviceTreePath = dir

	ct, score, err := d.checkDeviceTree()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Mutation that fails this: swap TypePS4 and TypePS5 in the two branches.
	if ct != TypePS4 {
		t.Errorf("expected TypePS4, got %s", ct)
	}
	if score != 1.0 {
		t.Errorf("expected score 1.0, got %f", score)
	}
}

func TestCheckDeviceTree_SonyConsolePartial(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compatible"), []byte("sony,generic-console-board\x00"), 0644); err != nil {
		t.Fatal(err)
	}
	d := NewDetector()
	d.deviceTreePath = dir

	ct, score, err := d.checkDeviceTree()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ct != TypeUnknown {
		t.Errorf("expected TypeUnknown for partial sony match, got %s", ct)
	}
	// Mutation that fails this: change the sony+console branch score 0.5 to 1.0.
	if score != 0.5 {
		t.Errorf("expected partial score 0.5, got %f", score)
	}
}

func TestCheckDeviceTree_MissingFile(t *testing.T) {
	d := NewDetector()
	d.deviceTreePath = filepath.Join(t.TempDir(), "nope")
	_, _, err := d.checkDeviceTree()
	if err == nil {
		t.Fatal("expected error reading missing compatible file")
	}
}

// detectLinux is OS-independent (the GOOS gate lives in Detect). Validate the
// confidence-merge fusion path between cpuinfo and device-tree.
func TestDetectLinux_MergeFusion(t *testing.T) {
	dir := t.TempDir()
	cpuinfo := filepath.Join(dir, "cpuinfo")
	// Jaguar -> 0.4, TypePS4.
	if err := os.WriteFile(cpuinfo, []byte("model name\t: AMD Jaguar\n"), 0644); err != nil {
		t.Fatal(err)
	}
	dt := filepath.Join(dir, "device-tree")
	if err := os.MkdirAll(dt, 0755); err != nil {
		t.Fatal(err)
	}
	// sony + console -> 0.5, TypeUnknown.
	if err := os.WriteFile(filepath.Join(dt, "compatible"), []byte("sony,console-x\x00"), 0644); err != nil {
		t.Fatal(err)
	}
	d := NewDetector()
	d.procCPUInfoPath = cpuinfo
	d.deviceTreePath = dt

	ct, conf, err := d.detectLinux()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// merged = 0.4 + 0.5*(1-0.4) = 0.7, which is < 0.8 so the fusion branch does
	// not early-return; it falls through to "higher confidence wins" = device
	// tree (0.5 > 0.4) with TypeUnknown.
	if ct != TypeUnknown {
		t.Errorf("expected TypeUnknown (dt wins on confidence), got %s", ct)
	}
	// Mutation that fails this: change `if cpuConf > dtConf` to `>=` AND the
	// returned conf — but precisely: replace the final `return dtType, dtConf`
	// with `return cpuType, cpuConf` and conf becomes 0.4.
	if conf < 0.49 || conf > 0.51 {
		t.Errorf("expected dt confidence 0.5 to win, got %f", conf)
	}
}

func TestDetectLinux_MergeReachesThreshold(t *testing.T) {
	dir := t.TempDir()
	cpuinfo := filepath.Join(dir, "cpuinfo")
	// Two distinct partial AMD signals: jaguar (0.4) + zen 2 (0.4) -> 0.8 in
	// cpuinfo alone, which short-circuits at the >= 0.8 cpu gate.
	if err := os.WriteFile(cpuinfo, []byte("model name\t: AMD Jaguar\nflags\t: amd zen 2 marker\n"), 0644); err != nil {
		t.Fatal(err)
	}
	d := NewDetector()
	d.procCPUInfoPath = cpuinfo
	d.deviceTreePath = filepath.Join(dir, "no-dt")

	ct, conf, err := d.detectLinux()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Mutation that fails this: change the cpu gate `if cpuConf >= 0.8` to
	// `>= 0.9`; the function would not early-return and conf path changes.
	if conf < 0.8 {
		t.Errorf("expected cpu gate to fire at >=0.8, got conf %f", conf)
	}
	// zen 2 was matched last, so detected == TypePS5.
	if ct != TypePS5 {
		t.Errorf("expected TypePS5, got %s", ct)
	}
}

func TestDetectLinux_BothMissingIsClean(t *testing.T) {
	dir := t.TempDir()
	d := NewDetector()
	d.procCPUInfoPath = filepath.Join(dir, "no-cpuinfo")
	d.deviceTreePath = filepath.Join(dir, "no-dt")

	ct, conf, err := d.detectLinux()
	// Both not-exist; os.IsNotExist is tolerated, so no error is surfaced.
	if err != nil {
		t.Fatalf("expected nil error when both sources absent, got %v", err)
	}
	if ct != TypeUnknown || conf != 0.0 {
		t.Errorf("expected (TypeUnknown,0.0), got (%s,%f)", ct, conf)
	}
}
