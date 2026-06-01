// Package computeproto provides FlatBuffers serialization for GPU compute payloads
// in Helix Cluster OS. Generated code lives in compute_generated.go (do not edit).
package computeproto

import (
	"sync"

	flatbuffers "github.com/google/flatbuffers/go"
)

// defaultInitialSize is the initial FlatBuffer builder capacity in bytes.
// Sized to cover a typical ComputeTask without reallocation.
const defaultInitialSize = 1024

// builderPool holds recycled *flatbuffers.Builder instances to avoid
// per-call heap allocation on the hot serialisation path.
var builderPool = &sync.Pool{
	New: func() any {
		return flatbuffers.NewBuilder(defaultInitialSize)
	},
}

// TensorSpec describes one tensor (name, dimensions, data-type) passed to
// BuildComputeTask.
type TensorSpec struct {
	Name  string
	Dims  []int32
	Dtype string
}

// ComputeTaskSpec carries the logical fields for BuildComputeTask.
type ComputeTaskSpec struct {
	ID      string
	Kernel  string
	Inputs  []TensorSpec
	Outputs []TensorSpec
}

// BuildComputeTask serialises spec into a FlatBuffer and returns the finished
// bytes. The returned slice is a copy — safe to keep after the builder is
// returned to the pool.
func BuildComputeTask(spec ComputeTaskSpec) []byte {
	b := builderPool.Get().(*flatbuffers.Builder)
	b.Reset()
	buf := buildComputeTask(b, spec)
	// copy before returning builder to pool so callers own the bytes
	out := make([]byte, len(buf))
	copy(out, buf)
	builderPool.Put(b)
	return out
}

// BuildComputeTaskUnpooled is identical to BuildComputeTask but always
// allocates a fresh Builder. It exists solely to let the benchmark prove
// that the pooled path uses fewer allocations.
func BuildComputeTaskUnpooled(spec ComputeTaskSpec) []byte {
	b := flatbuffers.NewBuilder(defaultInitialSize)
	raw := buildComputeTask(b, spec)
	out := make([]byte, len(raw))
	copy(out, raw)
	return out
}

// buildComputeTask does the real serialisation work using builder b.
// Returns b.FinishedBytes() — valid only while b is alive / not Reset.
func buildComputeTask(b *flatbuffers.Builder, spec ComputeTaskSpec) []byte {
	// ── pre-compute all string / vector offsets (bottom-up, FlatBuffers rule) ──

	// Tensor helper: build one tensor, return its offset.
	buildTensor := func(ts TensorSpec) flatbuffers.UOffsetT {
		nameOff := b.CreateString(ts.Name)
		dtypeOff := b.CreateString(ts.Dtype)

		// dims vector (elements prepended in reverse order)
		TensorStartDimsVector(b, len(ts.Dims))
		for i := len(ts.Dims) - 1; i >= 0; i-- {
			b.PrependInt32(ts.Dims[i])
		}
		dimsOff := b.EndVector(len(ts.Dims))

		TensorStart(b)
		TensorAddName(b, nameOff)
		TensorAddDims(b, dimsOff)
		TensorAddDtype(b, dtypeOff)
		return TensorEnd(b)
	}

	// Build all output tensors.
	outputOffsets := make([]flatbuffers.UOffsetT, len(spec.Outputs))
	for i, ts := range spec.Outputs {
		outputOffsets[i] = buildTensor(ts)
	}

	// Build all input tensors.
	inputOffsets := make([]flatbuffers.UOffsetT, len(spec.Inputs))
	for i, ts := range spec.Inputs {
		inputOffsets[i] = buildTensor(ts)
	}

	// outputs vector
	ComputeTaskStartOutputsVector(b, len(outputOffsets))
	for i := len(outputOffsets) - 1; i >= 0; i-- {
		b.PrependUOffsetT(outputOffsets[i])
	}
	outputsVec := b.EndVector(len(outputOffsets))

	// inputs vector
	ComputeTaskStartInputsVector(b, len(inputOffsets))
	for i := len(inputOffsets) - 1; i >= 0; i-- {
		b.PrependUOffsetT(inputOffsets[i])
	}
	inputsVec := b.EndVector(len(inputOffsets))

	// top-level strings
	idOff := b.CreateString(spec.ID)
	kernelOff := b.CreateString(spec.Kernel)

	// ComputeTask table
	ComputeTaskStart(b)
	ComputeTaskAddId(b, idOff)
	ComputeTaskAddKernel(b, kernelOff)
	ComputeTaskAddInputs(b, inputsVec)
	ComputeTaskAddOutputs(b, outputsVec)
	root := ComputeTaskEnd(b)

	FinishComputeTaskBuffer(b, root)
	return b.FinishedBytes()
}

// ReadComputeTask returns a zero-copy *ComputeTask accessor for buf.
// buf must remain alive for the lifetime of the returned accessor.
func ReadComputeTask(buf []byte) *ComputeTask {
	return GetRootAsComputeTask(buf, 0)
}
