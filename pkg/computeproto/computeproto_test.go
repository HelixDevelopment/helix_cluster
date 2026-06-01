package computeproto_test

import (
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/computeproto"
)

// ── test fixtures ────────────────────────────────────────────────────────────

// knownSpec builds a deterministic ComputeTask with 2 inputs and 1 output.
// Field values are chosen so the test can assert exact bytes/ints.
func knownSpec() computeproto.ComputeTaskSpec {
	return computeproto.ComputeTaskSpec{
		ID:     "task-gpu-001",
		Kernel: "matmul_fp16",
		Inputs: []computeproto.TensorSpec{
			{Name: "weight", Dims: []int32{512, 1024}, Dtype: "float16"},
			{Name: "activation", Dims: []int32{32, 512}, Dtype: "float16"},
		},
		Outputs: []computeproto.TensorSpec{
			{Name: "result", Dims: []int32{32, 1024}, Dtype: "float32"},
		},
	}
}

// ── sink-side field assertions ───────────────────────────────────────────────

// TestBuildReadComputeTask_ExactFields is the primary sink-side assertion test.
// It builds a ComputeTask, reads it back through the generated FlatBuffers
// accessors, and asserts every field value exactly.
func TestBuildReadComputeTask_ExactFields(t *testing.T) {
	spec := knownSpec()
	buf := computeproto.BuildComputeTask(spec)

	if len(buf) == 0 {
		t.Fatal("BuildComputeTask returned empty buffer")
	}

	task := computeproto.ReadComputeTask(buf)

	// ── top-level string fields ──────────────────────────────────────────────
	if got := string(task.Id()); got != spec.ID {
		t.Errorf("Id: want %q got %q", spec.ID, got)
	}
	if got := string(task.Kernel()); got != spec.Kernel {
		t.Errorf("Kernel: want %q got %q", spec.Kernel, got)
	}

	// ── inputs count ────────────────────────────────────────────────────────
	if got := task.InputsLength(); got != len(spec.Inputs) {
		t.Fatalf("InputsLength: want %d got %d", len(spec.Inputs), got)
	}

	// ── input[0]: weight ─────────────────────────────────────────────────────
	var t0 computeproto.Tensor
	if !task.Inputs(&t0, 0) {
		t.Fatal("Inputs(&t0, 0) returned false")
	}
	assertTensorName(t, &t0, "weight")
	assertTensorDtype(t, &t0, "float16")
	assertTensorDims(t, &t0, []int32{512, 1024})

	// ── input[1]: activation ─────────────────────────────────────────────────
	var t1 computeproto.Tensor
	if !task.Inputs(&t1, 1) {
		t.Fatal("Inputs(&t1, 1) returned false")
	}
	assertTensorName(t, &t1, "activation")
	assertTensorDtype(t, &t1, "float16")
	assertTensorDims(t, &t1, []int32{32, 512})

	// ── outputs count ────────────────────────────────────────────────────────
	if got := task.OutputsLength(); got != len(spec.Outputs) {
		t.Fatalf("OutputsLength: want %d got %d", len(spec.Outputs), got)
	}

	// ── output[0]: result ────────────────────────────────────────────────────
	var out0 computeproto.Tensor
	if !task.Outputs(&out0, 0) {
		t.Fatal("Outputs(&out0, 0) returned false")
	}
	assertTensorName(t, &out0, "result")
	assertTensorDtype(t, &out0, "float32")
	assertTensorDims(t, &out0, []int32{32, 1024})
}

// TestBuildReadComputeTask_Roundtrip confirms that BuildComputeTask followed by
// ReadComputeTask is idempotent when called twice with the same spec.
func TestBuildReadComputeTask_Roundtrip(t *testing.T) {
	spec := knownSpec()
	buf1 := computeproto.BuildComputeTask(spec)
	buf2 := computeproto.BuildComputeTask(spec)

	if len(buf1) != len(buf2) {
		t.Errorf("repeated calls produced different buffer lengths: %d vs %d", len(buf1), len(buf2))
	}

	task1 := computeproto.ReadComputeTask(buf1)
	task2 := computeproto.ReadComputeTask(buf2)

	if string(task1.Id()) != string(task2.Id()) {
		t.Errorf("Id mismatch across two builds")
	}
	if task1.InputsLength() != task2.InputsLength() {
		t.Errorf("InputsLength mismatch across two builds")
	}
}

// TestBuildReadComputeTask_EmptyTensors verifies the builder handles zero
// inputs/outputs (edge case) without panic and the counts read back as 0.
func TestBuildReadComputeTask_EmptyTensors(t *testing.T) {
	spec := computeproto.ComputeTaskSpec{
		ID:      "empty-task",
		Kernel:  "noop",
		Inputs:  nil,
		Outputs: nil,
	}
	buf := computeproto.BuildComputeTask(spec)
	task := computeproto.ReadComputeTask(buf)

	if got := string(task.Id()); got != "empty-task" {
		t.Errorf("Id: want \"empty-task\" got %q", got)
	}
	if n := task.InputsLength(); n != 0 {
		t.Errorf("InputsLength: want 0 got %d", n)
	}
	if n := task.OutputsLength(); n != 0 {
		t.Errorf("OutputsLength: want 0 got %d", n)
	}
}

// TestPooledBuilder_FewerAllocsThanUnpooled asserts the pooled builder uses
// strictly fewer allocations than the unpooled path over N iterations.
// This is the §1.1 allocation assertion.
func TestPooledBuilder_FewerAllocsThanUnpooled(t *testing.T) {
	spec := knownSpec()
	const iterations = 10.0

	// Warm up the pool so the first call doesn't count the New() allocation.
	_ = computeproto.BuildComputeTask(spec)

	pooledAllocs := testing.AllocsPerRun(int(iterations), func() {
		_ = computeproto.BuildComputeTask(spec)
	})

	unpooledAllocs := testing.AllocsPerRun(int(iterations), func() {
		_ = computeproto.BuildComputeTaskUnpooled(spec)
	})

	t.Logf("pooledAllocs=%.1f  unpooledAllocs=%.1f", pooledAllocs, unpooledAllocs)

	if pooledAllocs >= unpooledAllocs {
		t.Errorf("expected pooledAllocs (%.1f) < unpooledAllocs (%.1f): pool is not reducing allocations",
			pooledAllocs, unpooledAllocs)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func assertTensorName(t *testing.T, tensor *computeproto.Tensor, want string) {
	t.Helper()
	if got := string(tensor.Name()); got != want {
		t.Errorf("Tensor.Name: want %q got %q", want, got)
	}
}

func assertTensorDtype(t *testing.T, tensor *computeproto.Tensor, want string) {
	t.Helper()
	if got := string(tensor.Dtype()); got != want {
		t.Errorf("Tensor.Dtype: want %q got %q", want, got)
	}
}

func assertTensorDims(t *testing.T, tensor *computeproto.Tensor, want []int32) {
	t.Helper()
	if got := tensor.DimsLength(); got != len(want) {
		t.Fatalf("Tensor.DimsLength: want %d got %d", len(want), got)
	}
	for i, wantDim := range want {
		if got := tensor.Dims(i); got != wantDim {
			t.Errorf("Tensor.Dims[%d]: want %d got %d", i, wantDim, got)
		}
	}
}

// ── benchmarks ───────────────────────────────────────────────────────────────

func BenchmarkBuildComputeTask_Pooled(b *testing.B) {
	spec := knownSpec()
	// warm up
	_ = computeproto.BuildComputeTask(spec)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = computeproto.BuildComputeTask(spec)
	}
}

func BenchmarkBuildComputeTask_Unpooled(b *testing.B) {
	spec := knownSpec()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = computeproto.BuildComputeTaskUnpooled(spec)
	}
}
