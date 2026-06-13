// Package wasm — HXC-1264 Component-Model layer (Go-side realization).
//
// STATUS (honest, read this first):
//
// The WebAssembly Component Model is NOT runnable through this module today.
// The root module pins github.com/bytecodealliance/wasmtime-go/v29, whose Go
// bindings expose ONLY the core-module API (Engine/Module/Linker/Instance —
// see host.go). They have NO Component-Model surface: there is no `Component`
// type, no `NewComponentFromFile`, and the `Linker` only links core modules.
// (Verified with `go doc github.com/bytecodealliance/wasmtime-go/v29`: the only
// "component" matches are incidental words in the ExportType/ImportType doc
// comments.) Wasmtime's Rust/C cores support components; the Go bindings lag.
//
// Therefore this file does NOT claim to instantiate or call a real WASM
// *component*. Instead it delivers the Go-side representation of the three WIT
// worlds authored under pkg/wasm/wit/ (device-simulator, workload-generator,
// fault-injector): typed Go structs/enums mirroring the WIT records, variants
// and enums, plus Go interfaces matching each world's exported interface. These
// are the contracts a component would satisfy; they are wired to the existing
// core-module Host (host.go) as the realization seam so that, when a host that
// can instantiate a WASM component built from these WITs becomes available
// (wasmtime-go gains Component-Model bindings, or we add a wasmtime-py/CLI
// bridge), an adapter can implement these same interfaces against a real
// component WITHOUT changing any caller.
//
// The WIT files themselves are the canonical, validated artifacts (see
// qa-results/hxc-1264/*/wit-validate.txt — wasm-tools parses & round-trips all
// three). The Go types below are a faithful, hand-maintained projection of
// those WITs; ComponentModelStatus() reports the runtime reality so no caller
// can mistake this for a running component.
package wasm

import (
	_ "embed"
	"fmt"
)

// ----------------------------------------------------------------------------
// Embedded WIT sources — the canonical artifacts, compiled into the binary so
// the contracts ship with the host and can be re-emitted / re-validated.
// ----------------------------------------------------------------------------

//go:embed wit/device-simulator.wit
var witDeviceSimulator string

//go:embed wit/workload-generator.wit
var witWorkloadGenerator string

//go:embed wit/fault-injector.wit
var witFaultInjector string

// WITWorld names a Helix plugin world defined under pkg/wasm/wit/.
type WITWorld string

const (
	// WorldDeviceSimulator is the helix:device-simulator world.
	WorldDeviceSimulator WITWorld = "device-simulator"
	// WorldWorkloadGenerator is the helix:workload-generator world.
	WorldWorkloadGenerator WITWorld = "workload-generator"
	// WorldFaultInjector is the helix:fault-injector world.
	WorldFaultInjector WITWorld = "fault-injector"
)

// WITSource returns the canonical WIT text for a world (the exact bytes of the
// validated .wit file), or "" for an unknown world. Callers can re-emit it to
// disk and re-run `wasm-tools component wit` to re-validate.
func WITSource(w WITWorld) string {
	switch w {
	case WorldDeviceSimulator:
		return witDeviceSimulator
	case WorldWorkloadGenerator:
		return witWorkloadGenerator
	case WorldFaultInjector:
		return witFaultInjector
	default:
		return ""
	}
}

// WITWorlds returns the three defined worlds in deterministic order.
func WITWorlds() []WITWorld {
	return []WITWorld{WorldDeviceSimulator, WorldWorkloadGenerator, WorldFaultInjector}
}

// ----------------------------------------------------------------------------
// Component-Model runtime status — the anti-bluff reporter.
// ----------------------------------------------------------------------------

// ComponentRuntime classifies how a WIT world can be executed in this build.
type ComponentRuntime int

const (
	// RuntimeNone: no component runtime is wired. The WIT contracts exist and
	// the Go projection below can be implemented natively, but no WASM
	// *component* can be instantiated or called by this module.
	RuntimeNone ComponentRuntime = iota
	// RuntimeCoreModuleBridge: the world is realized through the core-module
	// Host (host.go) via a host-supplied Go implementation of the world
	// interface — NOT by instantiating a compiled component.
	RuntimeCoreModuleBridge
	// RuntimeComponent: a real WASM component is instantiated and called.
	// Reserved; never returned while wasmtime-go/v29 lacks Component-Model
	// bindings.
	RuntimeComponent
)

func (r ComponentRuntime) String() string {
	switch r {
	case RuntimeCoreModuleBridge:
		return "core-module-bridge"
	case RuntimeComponent:
		return "component"
	default:
		return "none"
	}
}

// ComponentModelStatus reports, truthfully, whether this build can run a WASM
// component. It returns RuntimeNone together with a human-readable reason,
// because github.com/bytecodealliance/wasmtime-go/v29 exposes no Component-Model
// API. This is the programmatic anti-bluff: any code path that wants to "run a
// component" must consult this and will learn it cannot.
func ComponentModelStatus() (ComponentRuntime, string) {
	return RuntimeNone, "wasmtime-go/v29 exposes no Component-Model API " +
		"(no Component type / NewComponentFromFile; Linker links core modules only). " +
		"WIT worlds are realized through the core-module Host via Go interface bridges " +
		"(see Bind* constructors); compiled WASM components cannot be instantiated by this build."
}

// ----------------------------------------------------------------------------
// Go projection of the WIT host-env interface (imported by every world).
// Mirrors `interface host-env { now-nanos / random-bytes / log }`. A core-module
// realization satisfies this from the capability-gated Host (clock/random/log).
// ----------------------------------------------------------------------------

// HostEnv is the Go projection of the WIT `host-env` interface that every world
// imports. Its methods correspond 1:1 to the capability-gated core-module host
// functions in sandbox.go (clock_now / random_fill / log_write). An
// implementation MUST honor the same deny-by-default capability posture: a
// method whose capability is not granted returns an error rather than a value.
type HostEnv interface {
	// NowNanos returns wall-clock nanoseconds since epoch. Requires CapClock.
	NowNanos() (int64, error)
	// RandomBytes returns n bytes of host randomness. Requires CapRandom.
	RandomBytes(n uint32) ([]byte, error)
	// Log emits a record to the host sink, returning true when captured.
	// Requires CapLog.
	Log(message string) (bool, error)
}

// coreModuleHostEnv is the core-module realization of HostEnv: it satisfies the
// WIT host-env contract by drawing from the SAME capability-gated seams the
// core-module Host exposes to guests (sandbox.go's clock/rand/log). It is the
// RuntimeCoreModuleBridge seam — a Go caller of a world interface gets real,
// capability-checked host values WITHOUT a compiled component, and the same
// deny-by-default posture (an ungranted capability yields ErrCapabilityDenied,
// matching what a guest WASM call would trap with).
type coreModuleHostEnv struct {
	host *Host
}

// NewCoreModuleHostEnv builds a HostEnv backed by an instantiated core-module
// Host. Its methods honor the host's capability grant set: clock/random/log are
// served only when CapClock/CapRandom/CapLog are granted, else they return an
// error wrapping ErrCapabilityDenied — identical policy to the gated guest host
// functions in sandbox.go. Log records land in host.Logs(), so the sink-side
// effect is observable exactly as for a guest's log_write.
func NewCoreModuleHostEnv(host *Host) HostEnv {
	return &coreModuleHostEnv{host: host}
}

func (e *coreModuleHostEnv) sandbox() *sandbox {
	if e.host == nil {
		return nil
	}
	return e.host.sandbox
}

func (e *coreModuleHostEnv) NowNanos() (int64, error) {
	s := e.sandbox()
	if s == nil || !s.has(CapClock) {
		return 0, fmt.Errorf("now-nanos: %w", &capError{cap: CapClock})
	}
	return s.clock(), nil
}

func (e *coreModuleHostEnv) RandomBytes(n uint32) ([]byte, error) {
	s := e.sandbox()
	if s == nil || !s.has(CapRandom) {
		return nil, fmt.Errorf("random-bytes: %w", &capError{cap: CapRandom})
	}
	buf := make([]byte, n)
	s.rand(buf)
	return buf, nil
}

func (e *coreModuleHostEnv) Log(message string) (bool, error) {
	s := e.sandbox()
	if s == nil || !s.has(CapLog) {
		return false, fmt.Errorf("log: %w", &capError{cap: CapLog})
	}
	if s.logs != nil {
		*s.logs = append(*s.logs, message)
	}
	return true, nil
}

// capError reports an ungranted capability and unwraps to ErrCapabilityDenied so
// callers match it with errors.Is, mirroring the guest-side denial sentinel.
type capError struct{ cap Capability }

func (c *capError) Error() string { return capDeniedMessage(c.cap) }
func (c *capError) Unwrap() error { return ErrCapabilityDenied }

// Capability reports which capability was denied.
func (c *capError) Capability() Capability { return c.cap }

// ----------------------------------------------------------------------------
// device-simulator world — Go projection of wit/device-simulator.wit.
// ----------------------------------------------------------------------------

// HealthState mirrors WIT `enum health-state`.
type HealthState string

const (
	HealthHealthy  HealthState = "healthy"
	HealthDegraded HealthState = "degraded"
	HealthCritical HealthState = "critical"
	HealthOffline  HealthState = "offline"
)

// TelemetrySample mirrors WIT `record telemetry-sample`.
type TelemetrySample struct {
	Name           string
	Value          float64
	Unit           string
	TimestampNanos int64
}

// HealthReport mirrors WIT `record health-report`.
type HealthReport struct {
	State   HealthState
	Score   float64
	Details []string
}

// DeviceConfig mirrors WIT `record device-config`.
type DeviceConfig struct {
	DeviceID string
	Kind     string
	Seed     uint64
}

// Simulator mirrors the WIT `interface simulator` exported by the
// device-simulator world. A component, or a native Go realization, implements
// this; the host drives configure -> sample/health.
type Simulator interface {
	Configure(config DeviceConfig) error
	Sample(count uint32) ([]TelemetrySample, error)
	Health() (HealthReport, error)
}

// ----------------------------------------------------------------------------
// workload-generator world — Go projection of wit/workload-generator.wit.
// ----------------------------------------------------------------------------

// Distribution mirrors WIT `enum distribution`.
type Distribution string

const (
	DistUniform Distribution = "uniform"
	DistPoisson Distribution = "poisson"
	DistZipfian Distribution = "zipfian"
)

// Priority mirrors WIT `enum priority`.
type Priority string

const (
	PriorityLow      Priority = "low"
	PriorityNormal   Priority = "normal"
	PriorityHigh     Priority = "high"
	PriorityCritical Priority = "critical"
)

// Label mirrors WIT `record label`.
type Label struct {
	Key   string
	Value string
}

// WorkItem mirrors WIT `record work-item`.
type WorkItem struct {
	ID           uint64
	Kind         string
	CostUnits    uint32
	Payload      []byte
	Priority     Priority
	Labels       []Label
	CreatedNanos int64
}

// GenerationPolicy mirrors WIT `record generation-policy`.
type GenerationPolicy struct {
	RatePerSecond    float64
	Shape            Distribution
	MeanPayloadBytes uint32
	Seed             uint64
}

// Generator mirrors the WIT `interface generator` exported by the
// workload-generator world.
type Generator interface {
	Start(policy GenerationPolicy) error
	NextBatch(maxItems uint32) ([]WorkItem, error)
	EmittedCount() uint64
	Stop() error
}

// ----------------------------------------------------------------------------
// fault-injector world — Go projection of wit/fault-injector.wit.
// ----------------------------------------------------------------------------

// FaultKind mirrors WIT `enum fault-kind`.
type FaultKind string

const (
	FaultLatency          FaultKind = "latency"
	FaultError            FaultKind = "error"
	FaultPartition        FaultKind = "partition"
	FaultResourcePressure FaultKind = "resource-pressure"
	FaultDataCorruption   FaultKind = "data-corruption"
)

// Param mirrors WIT `record param`.
type Param struct {
	Key   string
	Value string
}

// FaultSpec mirrors WIT `record fault-spec`.
type FaultSpec struct {
	Kind        FaultKind
	Probability float64
	Params      []Param
	Seed        uint64
}

// DecisionKind discriminates the WIT `variant decision`.
type DecisionKind string

const (
	DecisionPass  DecisionKind = "pass"
	DecisionDelay DecisionKind = "delay"
	DecisionFail  DecisionKind = "fail"
	DecisionDrop  DecisionKind = "drop"
)

// Decision mirrors WIT `variant decision`. Kind selects which payload field is
// meaningful: DelayMS for `delay(u32)`, FailMessage for `fail(string)`; pass and
// drop carry no payload.
type Decision struct {
	Kind        DecisionKind
	DelayMS     uint32 // valid when Kind == DecisionDelay
	FailMessage string // valid when Kind == DecisionFail
}

// Injector mirrors the WIT `interface injector` exported by the fault-injector
// world.
type Injector interface {
	Arm(spec FaultSpec) error
	Evaluate(eventID string) (Decision, error)
	Disarm() error
	InjectedCount() uint64
}
