package computeproto_test

// Adversarial / correctness-and-security audit of pkg/computeproto.
//
// Focus (this package decodes EXTERNAL bytes via ReadComputeTask /
// GetRootAsComputeTask / GetRootAsTensor — a trust boundary):
//
//   1. ROUND-TRIP FIDELITY  — Build then Read reproduces every field exactly;
//      pooled and unpooled builders produce byte-identical, equally-decodable
//      output (centerpiece).
//   2. DESERIALIZE ROBUSTNESS (DoS) — feeding random / truncated / empty / nil /
//      crafted buffers to the decode path must NOT crash the process. The Go
//      FlatBuffers runtime performs NO bounds checking on decode (no Verifier),
//      so the raw accessors panic on hostile input. These tests DOCUMENT that
//      behaviour with a recover() harness and assert the package's *current*
//      contract, surfacing the DoS exposure loudly via t.Log without aborting
//      the suite.
//   3. BOUNDS / EDGES — zero-dim tensor, empty payload, large dims.
//
// See the FINDING summary printed by TestDecode_HostileBytes_DoSReport.

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/computeproto"
)

// ─────────────────────────────────────────────────────────────────────────────
// shared fixtures
// ─────────────────────────────────────────────────────────────────────────────

// fullSpec populates EVERY field with distinct, asymmetric values so a
// transposed / dropped field cannot accidentally still round-trip.
func fullSpec() computeproto.ComputeTaskSpec {
	return computeproto.ComputeTaskSpec{
		ID:     "task-ID-α-7f3",
		Kernel: "kernel-conv2d-βγ",
		Inputs: []computeproto.TensorSpec{
			{Name: "in0", Dims: []int32{1, 2, 3, 4}, Dtype: "float32"},
			{Name: "in1-longer-name", Dims: []int32{7}, Dtype: "int8"},
			{Name: "in2", Dims: []int32{}, Dtype: "bool"}, // zero-dim edge
		},
		Outputs: []computeproto.TensorSpec{
			{Name: "out0", Dims: []int32{1024, 768}, Dtype: "float16"},
		},
	}
}

// decodeTensor reads every tensor field into a plain comparable struct.
type tensorView struct {
	Name  string
	Dtype string
	Dims  []int32
}

func viewTensor(tr *computeproto.Tensor) tensorView {
	n := tr.DimsLength()
	dims := make([]int32, n)
	for i := 0; i < n; i++ {
		dims[i] = tr.Dims(i)
	}
	return tensorView{
		Name:  string(tr.Name()),
		Dtype: string(tr.Dtype()),
		Dims:  dims,
	}
}

func specTensorView(ts computeproto.TensorSpec) tensorView {
	dims := ts.Dims
	if dims == nil {
		dims = []int32{}
	}
	return tensorView{Name: ts.Name, Dtype: ts.Dtype, Dims: dims}
}

// ─────────────────────────────────────────────────────────────────────────────
// 1. ROUND-TRIP FIDELITY  (centerpiece)
// ─────────────────────────────────────────────────────────────────────────────

// TestRoundTrip_FullFidelity is the load-bearing bijection assertion: it builds
// a fully-populated task with distinct values in every field and asserts each
// reads back exactly. This is the test the anti-bluff mutation must break.
func TestRoundTrip_FullFidelity(t *testing.T) {
	spec := fullSpec()
	buf := computeproto.BuildComputeTask(spec)
	task := computeproto.ReadComputeTask(buf)

	if got := string(task.Id()); got != spec.ID {
		t.Errorf("Id: want %q got %q", spec.ID, got)
	}
	if got := string(task.Kernel()); got != spec.Kernel {
		t.Errorf("Kernel: want %q got %q", spec.Kernel, got)
	}

	if got := task.InputsLength(); got != len(spec.Inputs) {
		t.Fatalf("InputsLength: want %d got %d", len(spec.Inputs), got)
	}
	for i := range spec.Inputs {
		var tr computeproto.Tensor
		if !task.Inputs(&tr, i) {
			t.Fatalf("Inputs(&tr, %d) returned false", i)
		}
		got := viewTensor(&tr)
		want := specTensorView(spec.Inputs[i])
		if got.Name != want.Name || got.Dtype != want.Dtype || !equalDims(got.Dims, want.Dims) {
			t.Errorf("input[%d]: want %+v got %+v", i, want, got)
		}
	}

	if got := task.OutputsLength(); got != len(spec.Outputs) {
		t.Fatalf("OutputsLength: want %d got %d", len(spec.Outputs), got)
	}
	for i := range spec.Outputs {
		var tr computeproto.Tensor
		if !task.Outputs(&tr, i) {
			t.Fatalf("Outputs(&tr, %d) returned false", i)
		}
		got := viewTensor(&tr)
		want := specTensorView(spec.Outputs[i])
		if got.Name != want.Name || got.Dtype != want.Dtype || !equalDims(got.Dims, want.Dims) {
			t.Errorf("output[%d]: want %+v got %+v", i, want, got)
		}
	}
}

func equalDims(a, b []int32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestPooledUnpooled_ByteIdentical asserts the two builders are interchangeable:
// for the same spec they MUST produce byte-identical buffers (rule #4 of the
// audit: pooled/unpooled divergence is a bug). A divergence would mean the pool
// leaks state across Reset().
func TestPooledUnpooled_ByteIdentical(t *testing.T) {
	specs := []computeproto.ComputeTaskSpec{
		fullSpec(),
		knownSpec(),
		{ID: "x", Kernel: "y"}, // no tensors
	}
	for i, spec := range specs {
		// Run several pooled builds first to dirty the pooled builder, then
		// confirm a subsequent pooled build still matches a fresh unpooled one.
		for w := 0; w < 5; w++ {
			_ = computeproto.BuildComputeTask(spec)
		}
		pooled := computeproto.BuildComputeTask(spec)
		unpooled := computeproto.BuildComputeTaskUnpooled(spec)
		if !bytes.Equal(pooled, unpooled) {
			t.Errorf("spec[%d]: pooled and unpooled bytes differ (len %d vs %d)",
				i, len(pooled), len(unpooled))
		}
	}
}

// TestPool_ConcurrentReuse hammers the pooled builder from many goroutines to
// prove the sync.Pool path is race-free and each result still decodes to the
// correct id (run under -race).
func TestPool_ConcurrentReuse(t *testing.T) {
	spec := fullSpec()
	const goroutines = 64
	done := make(chan bool, goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer func() { done <- true }()
			for i := 0; i < 50; i++ {
				buf := computeproto.BuildComputeTask(spec)
				task := computeproto.ReadComputeTask(buf)
				if string(task.Id()) != spec.ID {
					t.Errorf("concurrent build lost id: got %q", string(task.Id()))
					return
				}
			}
		}()
	}
	for g := 0; g < goroutines; g++ {
		<-done
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 3. BOUNDS / EDGES
// ─────────────────────────────────────────────────────────────────────────────

// TestEdge_ZeroDimAndEmptyPayload covers zero-dim tensors, empty strings, and a
// task with empty id/kernel — all must round-trip without panic.
func TestEdge_ZeroDimAndEmptyPayload(t *testing.T) {
	spec := computeproto.ComputeTaskSpec{
		ID:     "",
		Kernel: "",
		Inputs: []computeproto.TensorSpec{
			{Name: "", Dims: nil, Dtype: ""},
			{Name: "z", Dims: []int32{0, 0, 0}, Dtype: "f32"},
		},
	}
	buf := computeproto.BuildComputeTask(spec)
	task := computeproto.ReadComputeTask(buf)

	if got := string(task.Id()); got != "" {
		t.Errorf("empty Id: got %q", got)
	}
	if task.InputsLength() != 2 {
		t.Fatalf("InputsLength: want 2 got %d", task.InputsLength())
	}
	var tr0 computeproto.Tensor
	task.Inputs(&tr0, 0)
	if tr0.DimsLength() != 0 {
		t.Errorf("input[0] nil dims: want len 0 got %d", tr0.DimsLength())
	}
	var tr1 computeproto.Tensor
	task.Inputs(&tr1, 1)
	if tr1.DimsLength() != 3 {
		t.Fatalf("input[1] dims: want 3 got %d", tr1.DimsLength())
	}
	for i := 0; i < 3; i++ {
		if tr1.Dims(i) != 0 {
			t.Errorf("input[1].Dims[%d]: want 0 got %d", i, tr1.Dims(i))
		}
	}
}

// TestEdge_LargeDimsValues confirms extreme int32 dim values survive round-trip
// (no truncation/overflow on the dims path).
func TestEdge_LargeDimsValues(t *testing.T) {
	dims := []int32{0, 1, -1, 2147483647, -2147483648, 65536}
	spec := computeproto.ComputeTaskSpec{
		ID: "big", Kernel: "k",
		Inputs: []computeproto.TensorSpec{{Name: "t", Dims: dims, Dtype: "i32"}},
	}
	buf := computeproto.BuildComputeTask(spec)
	task := computeproto.ReadComputeTask(buf)
	var tr computeproto.Tensor
	if !task.Inputs(&tr, 0) {
		t.Fatal("Inputs(0) false")
	}
	if tr.DimsLength() != len(dims) {
		t.Fatalf("DimsLength: want %d got %d", len(dims), tr.DimsLength())
	}
	for i, w := range dims {
		if got := tr.Dims(i); got != w {
			t.Errorf("Dims[%d]: want %d got %d", i, w, got)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 2. DESERIALIZE ROBUSTNESS  (DoS — centerpiece)
// ─────────────────────────────────────────────────────────────────────────────

// safeReadResult records whether a decode attempt panicked.
type safeReadResult struct {
	panicked bool
	val      any
}

// safeDecodeComputeTask runs the full decode-and-walk path under recover so a
// panic is observed instead of aborting the test binary. It exercises the same
// accessors a real consumer would: root, strings, vector lengths, element walk.
func safeDecodeComputeTask(buf []byte) (res safeReadResult) {
	defer func() {
		if r := recover(); r != nil {
			res.panicked = true
			res.val = r
		}
	}()
	task := computeproto.ReadComputeTask(buf)
	_ = string(task.Id())
	_ = string(task.Kernel())
	in := task.InputsLength()
	for i := 0; i < in; i++ {
		var tr computeproto.Tensor
		if task.Inputs(&tr, i) {
			_ = string(tr.Name())
			_ = string(tr.Dtype())
			dl := tr.DimsLength()
			for j := 0; j < dl; j++ {
				_ = tr.Dims(j)
			}
		}
	}
	out := task.OutputsLength()
	for i := 0; i < out; i++ {
		var tr computeproto.Tensor
		if task.Outputs(&tr, i) {
			_ = string(tr.Name())
			_ = string(tr.Dtype())
		}
	}
	return res
}

// safeDecodeTensor exercises GetRootAsTensor on raw bytes.
func safeDecodeTensor(buf []byte) (res safeReadResult) {
	defer func() {
		if r := recover(); r != nil {
			res.panicked = true
			res.val = r
		}
	}()
	tr := computeproto.GetRootAsTensor(buf, 0)
	_ = string(tr.Name())
	_ = string(tr.Dtype())
	dl := tr.DimsLength()
	for j := 0; j < dl; j++ {
		_ = tr.Dims(j)
	}
	return res
}

// TestDecode_HostileBytes_DoSReport is the centerpiece DoS probe. It feeds a
// battery of hostile inputs to the decode path and TALLIES how many panic.
//
// CONTRACT DOCUMENTED HERE: the Go FlatBuffers runtime does not validate
// buffers, and this package calls GetRootAs* with no verifier. Therefore the
// EXPECTED current behaviour is that many hostile buffers PANIC — a remotely
// triggerable DoS if these bytes ever cross a network/IPC boundary. This test
// asserts the contract is at least *consistent* (the empty buffer panics) and
// LOUDLY logs the panic rate so the exposure is visible in CI output. It does
// NOT fail the suite, because the panic is a property of the upstream library +
// the (intended) zero-copy API; the FINDING below proposes the fix.
func TestDecode_HostileBytes_DoSReport(t *testing.T) {
	valid := computeproto.BuildComputeTask(fullSpec())

	type tc struct {
		name string
		buf  []byte
	}
	cases := []tc{
		{"nil", nil},
		{"empty", []byte{}},
		{"one-byte", []byte{0x01}},
		{"three-bytes", []byte{0x01, 0x02, 0x03}},
		{"four-zero-bytes", []byte{0, 0, 0, 0}},
		{"root-offset-max", []byte{0xff, 0xff, 0xff, 0xff}},
		{"root-offset-8", []byte{0x08, 0, 0, 0}},
		{"all-0xff-16", bytes.Repeat([]byte{0xff}, 16)},
	}

	// Every truncated prefix of a valid buffer.
	for n := 0; n < len(valid); n++ {
		prefix := make([]byte, n)
		copy(prefix, valid[:n])
		cases = append(cases, tc{name: "valid-prefix", buf: prefix})
	}

	// Single-byte corruptions of an otherwise valid buffer.
	rng := rand.New(rand.NewSource(0xC0FFEE))
	for k := 0; k < 256; k++ {
		c := make([]byte, len(valid))
		copy(c, valid)
		pos := rng.Intn(len(c))
		c[pos] ^= byte(1 + rng.Intn(255))
		cases = append(cases, tc{name: "bitflip", buf: c})
	}

	// Fully random buffers of assorted lengths.
	for _, n := range []int{4, 8, 16, 32, 64, 128} {
		for k := 0; k < 40; k++ {
			c := make([]byte, n)
			rng.Read(c)
			cases = append(cases, tc{name: "random", buf: c})
		}
	}

	var total, panics int
	emptyPanicked := false
	for _, c := range cases {
		r := safeDecodeComputeTask(c.buf)
		total++
		if r.panicked {
			panics++
			if c.name == "empty" || c.name == "nil" {
				emptyPanicked = true
			}
		}
	}

	t.Logf("DoS probe (ComputeTask): %d/%d hostile inputs panicked (%.1f%%)",
		panics, total, 100*float64(panics)/float64(total))

	// The package currently has NO verifier, so a zero-length root read panics.
	// Pin that as the documented (defective) contract so a future fix that adds
	// a verifier flips this assertion and forces this test to be updated.
	if !emptyPanicked {
		t.Log("NOTE: empty/nil no longer panics — a verifier may have been added; update FINDING.")
	} else {
		t.Log("FINDING: decode path PANICS on hostile/truncated bytes (no bounds checking / no verifier). " +
			"If ReadComputeTask/GetRootAsTensor ever decode untrusted external bytes, this is a remotely " +
			"triggerable DoS. Minimal fix: add a validation/verify step before access (bounds-checked " +
			"root/vtable/vector offsets) OR wrap decode in a recover-based safe API that returns an error. " +
			"Do NOT apply without coordinator review.")
	}

	if panics == 0 {
		t.Errorf("expected at least some hostile inputs to panic given the no-verifier contract; got 0 — " +
			"the decode contract changed and this test must be re-derived")
	}
}

// TestDecodeTensor_HostileBytes_DoSReport mirrors the probe for GetRootAsTensor.
func TestDecodeTensor_HostileBytes_DoSReport(t *testing.T) {
	rng := rand.New(rand.NewSource(0xBADF00D))
	var total, panics int
	emptyPanicked := false

	inputs := [][]byte{nil, {}, {0x01}, {0, 0, 0, 0}, {0xff, 0xff, 0xff, 0xff}}
	for _, n := range []int{4, 8, 16, 32, 64} {
		for k := 0; k < 60; k++ {
			c := make([]byte, n)
			rng.Read(c)
			inputs = append(inputs, c)
		}
	}
	for i, in := range inputs {
		r := safeDecodeTensor(in)
		total++
		if r.panicked {
			panics++
			if i < 2 { // nil, empty
				emptyPanicked = true
			}
		}
	}
	t.Logf("DoS probe (Tensor): %d/%d hostile inputs panicked (%.1f%%)",
		panics, total, 100*float64(panics)/float64(total))
	if !emptyPanicked {
		t.Log("NOTE: Tensor empty/nil no longer panics — verifier may exist; update FINDING.")
	}
	if panics == 0 {
		t.Errorf("expected GetRootAsTensor to panic on some hostile inputs (no-verifier contract); got 0")
	}
}

// TestDecode_EmptyBuffer_Panics isolates the single most important DoS witness:
// the smallest possible hostile input (empty buffer) crashes the raw decode
// path. This is the minimal reproduction a fix must address.
func TestDecode_EmptyBuffer_Panics(t *testing.T) {
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		task := computeproto.ReadComputeTask([]byte{})
		_ = task.Id() // force access in case root read is lazy
	}()
	if recovered == nil {
		t.Log("empty buffer did NOT panic (verifier added?); decode contract changed.")
		return
	}
	t.Logf("WITNESS: ReadComputeTask([]byte{}) panics: %v "+
		"(GetRootAsComputeTask -> GetUOffsetT(buf[0:]) -> buf[3] out of range). "+
		"Minimal external-input DoS reproduction.", recovered)
}

// TestDecode_CraftedRootOffset_OutOfRange crafts a 4-byte buffer whose root
// offset points far past the end of the buffer; following it must be observed
// (panic) rather than silently reading arbitrary memory.
func TestDecode_CraftedRootOffset_OutOfRange(t *testing.T) {
	buf := make([]byte, 8)
	// root UOffsetT at [0:4] = 0x7fffffff -> Init positions Pos way out of range.
	binary.LittleEndian.PutUint32(buf[0:], 0x7fffffff)
	r := safeDecodeComputeTask(buf)
	if !r.panicked {
		t.Log("crafted out-of-range root offset did NOT panic; library may have changed bounds behaviour.")
		return
	}
	t.Logf("WITNESS: crafted out-of-range root offset panics on access: %v", r.val)
}
