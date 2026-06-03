package resources

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Accelerator probing (HXC-1300).
//
// pkg/resources enumerates GPUs from real sysfs (drm.go) and reads real CPU/mem
// (proc_*). This file adds peak-throughput and non-GPU accelerator reporting:
//
//   - GPU TFLOPS: a real per-OS probe (Apple Silicon GPU core count on darwin —
//     accel_darwin.go; a known-model PCI table on linux — accel_linux.go). When a
//     host cannot probe it, an OVERRIDE LABEL is honored.
//   - NPU TOPS / FPGA logic elements / QPU presence: these are not exposed by any
//     portable sysfs node on the hosts we target, so they are supplied entirely
//     through OVERRIDE LABELS (operator-set node labels / env vars). The probe
//     reads them verbatim; a declared NPU therefore yields NPUTops>0.
//
// The label source is injectable (LabelSource) so tests drive the override path
// deterministically without touching the process environment, and production
// reads from the process environment via EnvLabelSource.

// Override label keys. Operators set these as node labels (surfaced to the
// process as environment variables) to declare accelerator capacity the host
// cannot self-probe. They are the documented, honored override path.
const (
	// LabelGPUTFLOPS overrides/forces the GPU FP32 TFLOPS figure.
	LabelGPUTFLOPS = "HELIX_ACCEL_GPU_TFLOPS"
	// LabelNPUTops declares NPU throughput in TOPS.
	LabelNPUTops = "HELIX_ACCEL_NPU_TOPS"
	// LabelFPGALogicElements declares FPGA fabric size in logic elements.
	LabelFPGALogicElements = "HELIX_ACCEL_FPGA_LOGIC_ELEMENTS"
	// LabelQPUPresent declares a QPU is present ("1"/"true"/"yes").
	LabelQPUPresent = "HELIX_ACCEL_QPU_PRESENT"
)

// LabelSource yields override-label values by key. Lookup mirrors os.LookupEnv:
// (value, ok) where ok=false means the label is unset. Making this an interface
// lets tests inject a fixed map and lets production read the real environment,
// behind ONE code path — the override logic is identical for both.
type LabelSource interface {
	Lookup(key string) (string, bool)
}

// EnvLabelSource reads override labels from the process environment, which is
// how node labels are surfaced to the resource daemon in production.
type EnvLabelSource struct{}

// Lookup implements LabelSource against the real process environment.
func (EnvLabelSource) Lookup(key string) (string, bool) { return os.LookupEnv(key) }

// MapLabelSource is an in-memory LabelSource for tests and for callers that
// already hold a node-label map. A nil map behaves as an empty source.
type MapLabelSource map[string]string

// Lookup implements LabelSource against the backing map.
func (m MapLabelSource) Lookup(key string) (string, bool) {
	if m == nil {
		return "", false
	}
	v, ok := m[key]
	return v, ok
}

// parseFloatLabel parses a non-negative float override. An unset label yields
// (0, false, nil). A present-but-garbled or negative label is a hard error so a
// typo in an operator override is surfaced, never silently coerced to 0.
func parseFloatLabel(src LabelSource, key string) (val float64, present bool, err error) {
	raw, ok := src.Lookup(key)
	if !ok {
		return 0, false, nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false, nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false, fmt.Errorf("override %s=%q: %w", key, raw, err)
	}
	if v < 0 {
		return 0, false, fmt.Errorf("override %s=%q: negative", key, raw)
	}
	return v, true, nil
}

// parseIntLabel is the int64 companion of parseFloatLabel with identical error
// semantics.
func parseIntLabel(src LabelSource, key string) (val int64, present bool, err error) {
	raw, ok := src.Lookup(key)
	if !ok {
		return 0, false, nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("override %s=%q: %w", key, raw, err)
	}
	if v < 0 {
		return 0, false, fmt.Errorf("override %s=%q: negative", key, raw)
	}
	return v, true, nil
}

// parseBoolLabel parses a presence flag. Recognized true values are "1", "true",
// "yes", "on" (case-insensitive); "0"/"false"/"no"/"off"/empty are false. Any
// other token is an error so a bad override is surfaced.
func parseBoolLabel(src LabelSource, key string) (val bool, present bool, err error) {
	raw, ok := src.Lookup(key)
	if !ok {
		return false, false, nil
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "0", "false", "no", "off":
		return false, true, nil
	case "1", "true", "yes", "on":
		return true, true, nil
	default:
		return false, false, fmt.Errorf("override %s=%q: not a boolean", key, raw)
	}
}

// ReadAccelerators builds the non-GPU Accelerators block purely from override
// labels (NPU/FPGA/QPU have no portable self-probe). A declared NPU therefore
// yields NPUTops>0. An unset label leaves its field at zero. A garbled override
// is returned as an error so operators learn of the typo.
func ReadAccelerators(src LabelSource) (Accelerators, error) {
	if src == nil {
		src = EnvLabelSource{}
	}
	var a Accelerators

	if v, present, err := parseFloatLabel(src, LabelNPUTops); err != nil {
		return Accelerators{}, err
	} else if present {
		a.NPUTops = v
	}

	if v, present, err := parseIntLabel(src, LabelFPGALogicElements); err != nil {
		return Accelerators{}, err
	} else if present {
		a.FPGALogicElements = v
	}

	if v, present, err := parseBoolLabel(src, LabelQPUPresent); err != nil {
		return Accelerators{}, err
	} else if present {
		a.QPUPresent = v
	}

	return a, nil
}

// gpuTFLOPSCatalog maps a canonical "vendor:device" PCI identity (the same
// lower-case "0x" hex tokens ParseDRMTree emits) to the card's published peak
// FP32 throughput in TFLOPS. This is the linux probe path: when sysfs gives us a
// PCI identity we can attribute real peak compute to it without any vendor SDK.
// Unknown identities are left to the override label.
var gpuTFLOPSCatalog = map[string]float64{
	// NVIDIA datacenter (FP32 peak, no sparsity).
	"0x10de:0x20b2": 19.5, // A100 80GB
	"0x10de:0x20f1": 19.5, // A100 40GB
	"0x10de:0x2330": 67.0, // H100 SXM
	// NVIDIA consumer.
	"0x10de:0x2204": 35.6, // RTX 3090
	"0x10de:0x2684": 82.6, // RTX 4090
	// AMD.
	"0x1002:0x740c": 47.9, // Instinct MI250X (per GCD FP32)
	"0x1002:0x73bf": 23.0, // Radeon RX 6900 XT
	// Intel integrated.
	"0x8086:0x9a60": 2.1, // Iris Xe (integrated)
}

// gpuTFLOPSFromCatalog returns the catalog peak FP32 TFLOPS for the GPU identity
// carried in g.Model, and ok=false when the identity is unknown/unparseable. It
// reuses canonicalIdentity (gpuclass.go) so the " x N" and heterogeneous Model
// shapes resolve to the leading card's identity exactly as the multiplier
// catalog does.
func gpuTFLOPSFromCatalog(g GPUInfo) (float64, bool) {
	id := canonicalIdentity(g.Model)
	if id == "" {
		return 0, false
	}
	v, ok := gpuTFLOPSCatalog[id]
	return v, ok
}

// gpuTFLOPSOverride returns the operator-forced GPU TFLOPS override, ok=false
// when unset. This is the universal fallback when no per-OS probe and no catalog
// entry can attribute peak compute to the card.
func gpuTFLOPSOverride(src LabelSource) (float64, bool, error) {
	return parseFloatLabel(src, LabelGPUTFLOPS)
}

// AcceleratorProbe is a Reader that enriches a node with accelerator signals: it
// fills GPUInfo.TFLOPS (via the per-OS probeGPUTFLOPS, the PCI catalog, then the
// override label) and the non-GPU Accelerators block (override labels). It is
// additive — it reports Count==0 for GPU and relies on the aggregator's
// per-field merge to overlay TFLOPS onto a GPU another reader enumerated, while
// declaring NPU/FPGA/QPU on its own.
//
// gpuFn is injectable so tests can supply a GPUInfo without real hardware; in
// production it is the host's real GPU reader (NewDRMGPUReader().Read).
type AcceleratorProbe struct {
	labels LabelSource
	gpuFn  func() (GPUInfo, error)
}

// NewAcceleratorProbe builds a probe reading override labels from the real
// process environment and GPU identity from the host's real GPU reader.
func NewAcceleratorProbe() *AcceleratorProbe {
	return &AcceleratorProbe{
		labels: EnvLabelSource{},
		gpuFn:  hostGPUInfo,
	}
}

// NewAcceleratorProbeWith builds a probe with an injected label source and GPU
// function, used by tests to drive the override path deterministically. A nil
// LabelSource defaults to the real environment; a nil gpuFn reports no GPU.
func NewAcceleratorProbeWith(labels LabelSource, gpuFn func() (GPUInfo, error)) *AcceleratorProbe {
	if labels == nil {
		labels = EnvLabelSource{}
	}
	if gpuFn == nil {
		gpuFn = func() (GPUInfo, error) { return GPUInfo{}, nil }
	}
	return &AcceleratorProbe{labels: labels, gpuFn: gpuFn}
}

// resolveGPUTFLOPS determines a GPU's peak FP32 TFLOPS with a fixed precedence:
//  1. the per-OS hardware probe (probeGPUTFLOPS) when it can attribute a value;
//  2. otherwise the known-model PCI catalog;
//  3. otherwise the operator override label.
//
// Returning 0 (no signal) is honest — better than fabricating a figure.
func resolveGPUTFLOPS(g GPUInfo, src LabelSource) (float64, error) {
	if v, ok := probeGPUTFLOPS(g); ok && v > 0 {
		return v, nil
	}
	if v, ok := gpuTFLOPSFromCatalog(g); ok && v > 0 {
		return v, nil
	}
	v, ok, err := gpuTFLOPSOverride(src)
	if err != nil {
		return 0, err
	}
	if ok {
		return v, nil
	}
	return 0, nil
}

// Read implements Reader. It produces a NodeResources carrying ONLY accelerator
// enrichment (GPU.TFLOPS when resolvable, and the Accelerators block), leaving
// CPU/Mem/Disk/Net zero so the aggregator merges without clobbering other
// readers. GPU.Count is left 0 deliberately: this reader does not enumerate
// GPUs, it annotates them.
func (p *AcceleratorProbe) Read(nodeID string) (NodeResources, error) {
	g, err := p.gpuFn()
	if err != nil {
		return NodeResources{}, fmt.Errorf("accelerator probe gpu: %w", err)
	}

	tflops, err := resolveGPUTFLOPS(g, p.labels)
	if err != nil {
		return NodeResources{}, err
	}

	accel, err := ReadAccelerators(p.labels)
	if err != nil {
		return NodeResources{}, err
	}

	res := NodeResources{NodeID: nodeID, Accelerators: accel}
	if tflops > 0 {
		res.GPU.TFLOPS = tflops
	}
	return res, nil
}

// Ensure AcceleratorProbe satisfies the Reader interface.
var _ Reader = (*AcceleratorProbe)(nil)
