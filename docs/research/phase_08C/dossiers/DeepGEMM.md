# DeepGEMM
- **Repo:** https://github.com/chutesai/DeepGEMM
- **Language:** Cuda (C++/CUDA kernels + Python/PyTorch bindings)
- **License:** MIT
- **Maturity:** fork-tracking (single "fixes" commit atop deepseek-ai/DeepGEMM `upstream/main`; upstream itself is production, tagged through v2.1.1.postN)
- **Distributed-Computing Relevance:** low
- **Portability Verdict:** SKIP
- **Target Helix Module:** none (single-node GPU compute primitive; at most a build/runtime dependency consumed by an inference worker, not a Helix module)
- **Effort:** XL (any integration requires CUDA toolchain, SM90/SM100 GPUs, CUTLASS 4.0+, PyTorch 2.1+ — none of which exist in HelixCluster's Go control plane)

## Purpose
DeepGEMM is a single-node, JIT-compiled CUDA tensor-core kernel library providing the core compute primitives of modern LLMs — FP8/FP4/BF16 GEMMs with fine-grained scaling, fused MoE with overlapped communication ("Mega MoE"), MQA scoring kernels for DeepSeek v3.2's lightning indexer, and HyperConnection (HC) kernels. It is the GPU math backend used inside an inference engine, not a service, scheduler, or networking layer.

## Capabilities
- FP8 GEMM with fine-grained (1D1D / 1D2D) scaling factors; SM90 uses FP32 scales, SM100 uses packed UE8M0 scales (4 packed into one int32).
- Grouped GEMMs in contiguous (M-axis grouped, fixed N/K) and masked layouts — tailored to MoE expert batching for training-forward/prefill and CUDA-graph decode respectively.
- K-axis-grouped GEMM for MoE weight-gradient backward passes.
- BF16 and TF32 GEMM, FP8xFP4 GEMM, FP4 indexer, and paged/non-paged MQA logits kernels.
- Mega MoE: fused MoE with communication/computation overlap (consumes DeepEP low-latency kernel output as input).
- Lightweight runtime JIT (low-CPU-overhead C++ JIT module) compiling kernels at runtime; no CUDA compilation at install time; per-kernel code generation and SASS/PTX emission.
- **Chutes-specific addition** (`csrc/apis/warmup.hpp`, ~417 new lines, exposed as `warmup_kernels`): pre-warms the JIT cache by generating code for a list of M values, deduplicating by emitted code string, and compiling only the unique kernels — eliminating first-request cold-start compile latency during serving. Also re-exports `get_mk_alignment_for_contiguous_layout`.

## Distributed-Computing Notes
- **No** GPU validation/attestation (no GraVal), **no** p2p/gossip (no fiber), **no** Bittensor subnet consensus/weights, **no** TEE/confidential compute, **no** E2EE transport, **no** inference routing/serving layer, **no** miner/validator logic, **no** fault tolerance or placement/scheduling across nodes.
- The only "scheduling" present is intra-kernel tile scheduling on a single GPU (`include/deep_gemm/scheduler/*.cuh` — gemm, mega_moe, paged_mqa_logits tile schedulers), which is GPU-internal and unrelated to cluster scheduling.
- The only "communication" is the intra-GPU/intra-node MoE comm overlap in Mega MoE (relies on DeepEP for the actual expert-parallel all-to-all); DeepGEMM itself does not implement any network transport.
- Chutes' single change is a serving-latency optimization (JIT warmup), reinforcing that this repo is consumed *inside* a GPU inference worker, far below any HelixCluster control-plane concern.

## HelixCluster Gaps Addressed
- None directly. HelixCluster is a Go 7-layer cluster OS; its GPU support is planned (resources/scheduler) and concerns *placement and lifecycle* of GPU workloads, not the math kernels that run on the GPU.
- Indirect relevance only: if Helix's LLMOrchestrator/inference workers ever embed a DeepSeek-style FP8 engine (e.g. SGLang/vLLM with DeepGEMM backend), DeepGEMM would be a transitive runtime dependency of that worker container — managed as an opaque pip/CUDA dependency, never ported into Helix Go code.

## Dependencies
- CUDA Toolkit 12.3+ (12.9+ recommended; 12.9+ required for SM100), NVIDIA SM90 (Hopper) or SM100 (Blackwell) GPU.
- PyTorch 2.1+ (build + runtime; `CUDAExtension`, links `cudart`, `nvrtc`).
- CUTLASS 4.0+ and `{fmt}` (git submodules: `third-party/cutlass`, `third-party/fmt`).
- C++20 compiler; pybind11; optional TileLang ops (`third-party/tilelang_ops`).

## Rationale
SKIP. This is a CUDA/PyTorch GPU kernel library with zero distributed-systems surface — no networking, consensus, attestation, scheduling, or serving. It cannot be ported to Go and offers nothing a Go control-plane module would consume directly. Chutes' delta is a thin JIT-warmup helper for serving latency, not a distributed feature. It is relevant to HelixCluster only as a deep transitive dependency *inside* a GPU inference worker image, which Helix would treat as an opaque container artifact. The MIT license is permissive and poses no portability gate, but there is nothing portable to gate.

## Risks
- **Language mismatch:** pure CUDA/C++/Python; HelixCluster control plane is Go. No FFI path that makes sense — DeepGEMM only runs meaningfully on a GPU inside a PyTorch process.
- **Hardware lock-in:** requires Hopper/Blackwell-class NVIDIA GPUs and matching CUDA 12.3–12.9+; unusable on CPU-only Helix nodes.
- **Heavy build deps:** CUTLASS, fmt submodules, runtime JIT/NVRTC, PyTorch — large, version-sensitive toolchain.
- **Fork drift:** the chutesai fork is a single "fixes" commit on top of upstream `main`; it will rot quickly relative to fast-moving deepseek-ai/DeepGEMM (frequent vX.Y.postN tags). If ever needed, prefer tracking upstream and cherry-picking the `warmup_kernels` JIT-warmup patch rather than depending on the fork.
- **Misclassification risk:** the one-liner ("FP8 GEMM kernels with fine-grained scaling") could tempt treating this as compute infrastructure; it is strictly a single-GPU math kernel library and must not be modeled as a Helix distributed component.
