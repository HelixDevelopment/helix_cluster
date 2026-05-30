# Helix Cluster OS — Zig Coding Standards

| Field | Value |
|---|---|
| Revision | 1 |
| Created | 2026-05-30 |
| Last modified | 2026-05-30T20:43:00Z |
| Status | active |

## Style

- Zig `zig fmt` is the single source of truth.
- Indent: 4 spaces.
- Line length: soft 100, hard 120.
- File names: lowercase_snake_case.zig (per Constitution §11.4.29).

## Memory Safety

- Use `std.heap.GeneralPurposeAllocator` in debug builds; leak detection mandatory.
- Document allocator ownership at every function boundary.
- No `undefined` without justification comment.

## Error Handling

- Define error sets explicitly: `const MyError = error{ OutOfMemory, InvalidArgument };`
- Use `try` for propagation; use `catch` for local handling with logging.
- No silent error swallowing.

## comptime

- Leverage `comptime` for configuration tables and type generation.
- Document `comptime` complexity — prefer clarity over cleverness.

## Testing

- `test "description" { ... }` blocks inline with source.
- Standalone test runners in `tests/zig/` for integration/chaos/stress.
- Mutation-paired gates mandatory per Constitution §1.1.

## C Interop

- Declare C bindings in `src/c/` with `@cImport` / `@cInclude`.
- Document memory ownership across the Zig/C boundary.
