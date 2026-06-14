// Package computeproto provides FlatBuffers serialization for GPU compute payloads
// in Helix Cluster OS. Generated code lives in compute_generated.go (do not edit).
package computeproto

import (
	"errors"
	"fmt"
	"sync"

	flatbuffers "github.com/google/flatbuffers/go"
)

// ErrMalformedBuffer is returned by ReadComputeTaskSafe when buf is not a valid
// FlatBuffers ComputeTask encoding (too short, or crafted so the zero-copy
// accessors would index out of bounds).
var ErrMalformedBuffer = errors.New("computeproto: malformed ComputeTask buffer")

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
//
// WARNING: ReadComputeTask performs NO validation. The Go FlatBuffers runtime
// reads vtable/offset/length words straight out of buf with no bounds checking,
// so a truncated or crafted buffer makes the accessors panic with an
// out-of-range slice index. Use ReadComputeTask ONLY for trusted, in-process
// bytes (e.g. ones you just produced with BuildComputeTask). For UNTRUSTED bytes
// arriving from the network or IPC, use ReadComputeTaskSafe.
func ReadComputeTask(buf []byte) *ComputeTask {
	return GetRootAsComputeTask(buf, 0)
}

// ReadComputeTaskSafe is the validated counterpart of ReadComputeTask for
// decoding UNTRUSTED bytes. It rejects an undersized buffer and recovers any
// out-of-bounds panic from the FlatBuffers accessor offset arithmetic into
// ErrMalformedBuffer, so a hostile or truncated buffer yields an error instead
// of crashing the process — closing the remotely-triggerable DoS that the raw
// zero-copy ReadComputeTask exposes when fed external bytes.
//
// It eagerly walks every field of the task (id, kernel, and each input/output
// tensor's name/dtype/dims) so a lazy out-of-range access is forced to occur
// HERE, inside the recover at the trust boundary, rather than later in caller
// code where it could not be guarded. On success the returned *ComputeTask is
// the same zero-copy accessor ReadComputeTask returns and is safe to use; buf
// must remain alive for its lifetime.
func ReadComputeTaskSafe(buf []byte) (task *ComputeTask, err error) {
	if len(buf) < flatbuffers.SizeUOffsetT {
		return nil, fmt.Errorf("%w: buffer too short (%d bytes, need >= %d)",
			ErrMalformedBuffer, len(buf), flatbuffers.SizeUOffsetT)
	}
	defer func() {
		if r := recover(); r != nil {
			task = nil
			err = fmt.Errorf("%w: %v", ErrMalformedBuffer, r)
		}
	}()
	t := GetRootAsComputeTask(buf, 0)
	validateComputeTask(t)
	return t, nil
}

// validateComputeTask force-traverses every field of t so that any malformed
// offset/length triggers its panic synchronously (caught by the recover in
// ReadComputeTaskSafe) instead of lazily in a later accessor call.
func validateComputeTask(t *ComputeTask) {
	_ = t.Id()
	_ = t.Kernel()
	for i, n := 0, t.InputsLength(); i < n; i++ {
		var ten Tensor
		if t.Inputs(&ten, i) {
			touchTensor(&ten)
		}
	}
	for i, n := 0, t.OutputsLength(); i < n; i++ {
		var ten Tensor
		if t.Outputs(&ten, i) {
			touchTensor(&ten)
		}
	}
}

// touchTensor force-reads every field of a Tensor accessor.
func touchTensor(ten *Tensor) {
	_ = ten.Name()
	_ = ten.Dtype()
	for i, n := 0, ten.DimsLength(); i < n; i++ {
		_ = ten.Dims(i)
	}
}
