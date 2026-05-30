# Helix Cluster OS — Go Coding Standards

| Field | Value |
|---|---|
| Revision | 1 |
| Created | 2026-05-30 |
| Last modified | 2026-05-30T20:43:00Z |
| Status | active |

## Style

- Follow `gofmt` / `goimports` unconditionally.
- Line length: soft 100, hard 120.
- Package names: lowercase, no underscores.
- File names: lowercase_snake_case.go (per Constitution §11.4.29).

## Structure

```
cmd/<service>/          # Service binaries
pkg/<domain>/           # Public libraries
internal/<domain>/      # Private implementation
tests/<type>/           # Unit, integration, e2e, chaos, stress
```

## Concurrency

- Prefer `context.Context` for cancellation.
- Never leak goroutines — always `defer cancel()` or use `sync.WaitGroup`.
- Channel ownership: the sender closes; document ownership in comments.

## Error Handling

- Wrap errors with `fmt.Errorf("...: %w", err)`.
- Sentinel errors at package level: `var ErrNotFound = errors.New("not found")`.
- No naked `panic` in production code.

## Testing

- Table-driven tests by default.
- `t.Parallel()` for independent subtests.
- Mutation-paired gates mandatory per Constitution §1.1.
- Mock/stub/fake permitted ONLY in unit tests (§11.4.27).

## Anti-Bluff

Every test MUST:
1. Invoke a real user-visible action.
2. Capture state BEFORE and AFTER.
3. Assert positive evidence (not absence-of-error).
4. Carry a per-run UUID for cache-defeat.
