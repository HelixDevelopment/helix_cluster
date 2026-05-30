# Helix Cluster OS — C/C++ Coding Standards

| Field | Value |
|---|---|
| Revision | 1 |
| Created | 2026-05-30 |
| Last modified | 2026-05-30T20:43:00Z |
| Status | active |

## Style

- C: Linux kernel style (tabs, 80-char soft limit, 100-char hard).
- C++: Google C++ Style Guide (2-space indent, 80-char soft limit).
- File names: lowercase_snake_case.c / .cpp / .h / .hpp (per Constitution §11.4.29).

## Memory Safety

- C: use `__attribute__((cleanup(...)))` or explicit `free` at every `malloc` site.
- C++: RAII; `std::unique_ptr` / `std::shared_ptr` by default; no naked `new`/`delete`.
- No `strcpy`, `sprintf`, `gets` — use `strlcpy`, `snprintf`, `fgets`.

## GPU Kernels

- CUDA: `.cu` files; use `__global__` / `__device__` explicitly.
- ROCm/HIP: `.hip` files; wrap CUDA calls with HIP portability macros.
- oneAPI/SYCL: `.cpp` with SYCL headers; target `sycl::queue`.
- MLX: Objective-C++ `.mm` for Apple Silicon; document Metal pipeline.

## Error Handling

- C: return `int` status (0 = success, negative = error code); use `errno` sparingly.
- C++: exceptions permitted for unrecoverable errors; use `StatusOr<T>` for recoverable.

## Testing

- GoogleTest for C++ unit tests.
- Custom C test runner in `tests/c/`.
- GPU kernel tests MUST run on real hardware (PCS-2).
- Mutation-paired gates mandatory per Constitution §1.1.

## Build

- CMake 3.25+ for C/C++ components.
- `ccache` mandatory for incremental builds.
- Sanitizers (`-fsanitize=address,undefined`) enabled in debug builds.
