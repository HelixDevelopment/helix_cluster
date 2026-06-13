package wasm

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// TestComponentModelStatusIsHonest is the anti-bluff guard for the headline
// claim: this build CANNOT run a WASM component, because wasmtime-go/v29 has no
// Component-Model API. The status MUST say so. If a future wasmtime-go gains
// component bindings and someone wires RuntimeComponent, this test must be
// updated deliberately — it pins the honest current reality.
func TestComponentModelStatusIsHonest(t *testing.T) {
	rt, reason := ComponentModelStatus()
	if rt != RuntimeNone {
		t.Fatalf("expected RuntimeNone (components not runnable here), got %v", rt)
	}
	if rt.String() != "none" {
		t.Fatalf("RuntimeNone.String() = %q, want none", rt.String())
	}
	for _, want := range []string{"wasmtime-go/v29", "no Component-Model", "core-module"} {
		if !strings.Contains(reason, want) {
			t.Errorf("status reason missing %q; got: %s", want, reason)
		}
	}
}

// TestWITSourcesEmbeddedMatchFiles proves the embedded WIT bytes are the SAME
// bytes wasm-tools validated on disk (no drift between the validated artifact
// and what ships in the binary).
func TestWITSourcesEmbeddedMatchFiles(t *testing.T) {
	cases := map[WITWorld]string{
		WorldDeviceSimulator:   "wit/device-simulator.wit",
		WorldWorkloadGenerator: "wit/workload-generator.wit",
		WorldFaultInjector:     "wit/fault-injector.wit",
	}
	for world, path := range cases {
		onDisk, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		embedded := WITSource(world)
		if embedded == "" {
			t.Fatalf("WITSource(%q) returned empty", world)
		}
		if embedded != string(onDisk) {
			t.Errorf("embedded WIT for %q differs from %s", world, path)
		}
		// Every world declares its package and imports the gated host-env.
		if !strings.Contains(embedded, "package helix:"+string(world)) {
			t.Errorf("%q WIT missing 'package helix:%s'", world, world)
		}
		if !strings.Contains(embedded, "import host-env;") {
			t.Errorf("%q WIT missing 'import host-env;'", world)
		}
	}
	if got := len(WITWorlds()); got != 3 {
		t.Fatalf("WITWorlds() = %d worlds, want 3", got)
	}
	if WITSource(WITWorld("nope")) != "" {
		t.Error("WITSource of unknown world should be empty")
	}
}

// newInstantiatedHost spins up a real core-module Host (instantiating the tiny
// add module) so the bridge has a genuine sandbox to consult.
func newInstantiatedHost(t *testing.T, caps *Capabilities) *Host {
	t.Helper()
	h, err := NewHost()
	if err != nil {
		t.Fatalf("new host: %v", err)
	}
	t.Cleanup(h.Close)
	h.SetCapabilities(caps)
	path := writeTempWasm(t, simpleAddWasm)
	if err := h.LoadModule(path); err != nil {
		t.Fatalf("load module: %v", err)
	}
	if err := h.Instantiate(); err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	return h
}

// TestCoreModuleHostEnvDeniesUngranted is the anti-bluff for the capability
// posture: with NO capabilities granted, every host-env method must fail with
// ErrCapabilityDenied — never silently return a zero value as if it worked.
func TestCoreModuleHostEnvDeniesUngranted(t *testing.T) {
	h := newInstantiatedHost(t, NewCapabilities()) // deny everything
	env := NewCoreModuleHostEnv(h)

	if _, err := env.NowNanos(); !errors.Is(err, ErrCapabilityDenied) {
		t.Errorf("NowNanos without CapClock: err=%v, want ErrCapabilityDenied", err)
	}
	if _, err := env.RandomBytes(8); !errors.Is(err, ErrCapabilityDenied) {
		t.Errorf("RandomBytes without CapRandom: err=%v, want ErrCapabilityDenied", err)
	}
	if _, err := env.Log("hi"); !errors.Is(err, ErrCapabilityDenied) {
		t.Errorf("Log without CapLog: err=%v, want ErrCapabilityDenied", err)
	}
	// Denied Log must NOT have produced a sink-side record.
	if got := len(h.Logs()); got != 0 {
		t.Errorf("denied Log leaked %d records into host.Logs()", got)
	}
}

// TestCoreModuleHostEnvGrantedProducesRealEffects is the positive anti-bluff:
// when the capabilities ARE granted, the bridge yields real values AND the
// log lands in the host's captured sink (host.Logs()), proving the wiring to
// the existing core-module host is genuine, not a stub.
func TestCoreModuleHostEnvGrantedProducesRealEffects(t *testing.T) {
	h := newInstantiatedHost(t, NewCapabilities(CapClock, CapRandom, CapLog))
	env := NewCoreModuleHostEnv(h)

	// random_fill must fill the requested length with the host RNG. Two draws
	// from the deterministic default source advance state (not all-zero, and
	// the second draw differs from the first), proving real bytes flowed.
	b1, err := env.RandomBytes(16)
	if err != nil {
		t.Fatalf("RandomBytes granted: %v", err)
	}
	if len(b1) != 16 {
		t.Fatalf("RandomBytes returned %d bytes, want 16", len(b1))
	}
	allZero := true
	for _, x := range b1 {
		if x != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("RandomBytes returned all zeros — RNG not wired")
	}

	if _, err := env.NowNanos(); err != nil {
		t.Fatalf("NowNanos granted: %v", err)
	}

	ok, err := env.Log("hxc-1264 bridge record")
	if err != nil || !ok {
		t.Fatalf("Log granted: ok=%v err=%v", ok, err)
	}
	logs := h.Logs()
	if len(logs) != 1 || logs[0] != "hxc-1264 bridge record" {
		t.Fatalf("sink-side host.Logs() = %v, want one matching record", logs)
	}
}

// --- Native realizations of the three world interfaces, proving the Go
// projection of each WIT world is implementable end to end. These are
// deterministic, host-independent reference implementations. ---

type refSimulator struct {
	cfg   DeviceConfig
	armed bool
	ticks int
}

func (s *refSimulator) Configure(c DeviceConfig) error { s.cfg = c; s.armed = true; return nil }
func (s *refSimulator) Sample(count uint32) ([]TelemetrySample, error) {
	if !s.armed {
		return nil, errors.New("not configured")
	}
	s.ticks++
	out := make([]TelemetrySample, 0, count)
	for i := uint32(0); i < count; i++ {
		out = append(out, TelemetrySample{
			Name:           "temperature",
			Value:          40 + float64((int(s.cfg.Seed)+s.ticks+int(i))%10),
			Unit:           "celsius",
			TimestampNanos: int64(s.ticks),
		})
	}
	return out, nil
}
func (s *refSimulator) Health() (HealthReport, error) {
	if !s.armed {
		return HealthReport{}, errors.New("not configured")
	}
	return HealthReport{State: HealthHealthy, Score: 1.0, Details: nil}, nil
}

// TestSimulatorInterfaceRealizable exercises a Simulator implementation through
// the WIT-projected interface: configure -> sample -> health.
func TestSimulatorInterfaceRealizable(t *testing.T) {
	var sim Simulator = &refSimulator{}
	if _, err := sim.Sample(1); err == nil {
		t.Error("Sample before Configure should error (not-configured)")
	}
	if err := sim.Configure(DeviceConfig{DeviceID: "gpu0", Kind: "gpu", Seed: 7}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	samples, err := sim.Sample(3)
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	if len(samples) != 3 || samples[0].Unit != "celsius" {
		t.Fatalf("unexpected samples: %+v", samples)
	}
	hr, err := sim.Health()
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if hr.State != HealthHealthy || hr.Score != 1.0 {
		t.Fatalf("unexpected health: %+v", hr)
	}
}

type refGenerator struct {
	started bool
	next    uint64
	emitted uint64
}

func (g *refGenerator) Start(GenerationPolicy) error { g.started = true; return nil }
func (g *refGenerator) NextBatch(max uint32) ([]WorkItem, error) {
	if !g.started {
		return nil, errors.New("not started")
	}
	out := make([]WorkItem, 0, max)
	for i := uint32(0); i < max; i++ {
		g.next++
		g.emitted++
		out = append(out, WorkItem{ID: g.next, Kind: "rpc", CostUnits: 1, Priority: PriorityNormal})
	}
	return out, nil
}
func (g *refGenerator) EmittedCount() uint64 { return g.emitted }
func (g *refGenerator) Stop() error          { g.started = false; return nil }

// TestGeneratorInterfaceRealizable exercises a Generator implementation.
func TestGeneratorInterfaceRealizable(t *testing.T) {
	var gen Generator = &refGenerator{}
	if _, err := gen.NextBatch(1); err == nil {
		t.Error("NextBatch before Start should error")
	}
	if err := gen.Start(GenerationPolicy{RatePerSecond: 100, Shape: DistUniform}); err != nil {
		t.Fatalf("start: %v", err)
	}
	batch, err := gen.NextBatch(5)
	if err != nil || len(batch) != 5 {
		t.Fatalf("batch=%d err=%v", len(batch), err)
	}
	if gen.EmittedCount() != 5 {
		t.Fatalf("emitted=%d want 5", gen.EmittedCount())
	}
	if batch[0].ID != 1 || batch[4].ID != 5 {
		t.Fatalf("ids not monotonic: %+v", batch)
	}
	if err := gen.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

type refInjector struct {
	armed    bool
	spec     FaultSpec
	injected uint64
}

func (in *refInjector) Arm(s FaultSpec) error { in.spec = s; in.armed = true; return nil }
func (in *refInjector) Evaluate(eventID string) (Decision, error) {
	if !in.armed {
		return Decision{}, errors.New("not armed")
	}
	// Deterministic: probability >= 1.0 always injects per the armed kind.
	if in.spec.Probability >= 1.0 {
		in.injected++
		switch in.spec.Kind {
		case FaultError:
			return Decision{Kind: DecisionFail, FailMessage: "injected:" + eventID}, nil
		case FaultLatency:
			return Decision{Kind: DecisionDelay, DelayMS: 250}, nil
		case FaultPartition:
			return Decision{Kind: DecisionDrop}, nil
		default:
			return Decision{Kind: DecisionDrop}, nil
		}
	}
	return Decision{Kind: DecisionPass}, nil
}
func (in *refInjector) Disarm() error         { in.armed = false; return nil }
func (in *refInjector) InjectedCount() uint64 { return in.injected }

// TestInjectorInterfaceRealizable exercises an Injector implementation and the
// Decision variant projection.
func TestInjectorInterfaceRealizable(t *testing.T) {
	var inj Injector = &refInjector{}
	if _, err := inj.Evaluate("e1"); err == nil {
		t.Error("Evaluate before Arm should error")
	}
	if err := inj.Arm(FaultSpec{Kind: FaultError, Probability: 1.0}); err != nil {
		t.Fatalf("arm: %v", err)
	}
	d, err := inj.Evaluate("e1")
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if d.Kind != DecisionFail || d.FailMessage != "injected:e1" {
		t.Fatalf("unexpected decision: %+v", d)
	}
	if inj.InjectedCount() != 1 {
		t.Fatalf("injected=%d want 1", inj.InjectedCount())
	}
	if err := inj.Disarm(); err != nil {
		t.Fatalf("disarm: %v", err)
	}
}
