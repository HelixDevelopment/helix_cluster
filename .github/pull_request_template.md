## Summary

<!-- One-line summary of the change -->

## Type

- [ ] Bug fix
- [ ] Feature
- [ ] Task (refactor, infra, doc)

## Cross-Platform Parity (PCS-1)

- [ ] Linux implementation provided
- [ ] macOS implementation provided
- [ ] Windows/WSL2 implementation provided
- [ ] N/A — platform-agnostic change

## GPU Backend Coverage (PCS-2)

- [ ] NVIDIA CUDA tested on real hardware
- [ ] AMD ROCm tested on real hardware
- [ ] Intel oneAPI tested on real hardware
- [ ] Apple MLX tested on real hardware
- [ ] N/A — no GPU code touched

## Testing

- [ ] Unit tests added/updated
- [ ] Integration tests added/updated
- [ ] E2E tests added/updated
- [ ] Mutation test paired per Constitution §1.1
- [ ] Anti-bluff helper invoked (real action + state delta + positive evidence)

## Documentation

- [ ] `CLAUDE.md` Applied Fixes table updated (if applicable)
- [ ] `AGENTS.md` / `QWEN.md` updated (if applicable)
- [ ] `docs/guides/` updated (if user-visible change)
- [ ] ADR created/updated (if architectural change)

## Checklist

- [ ] `gofmt` / `zig fmt` / `clang-format` applied
- [ ] No secrets or credentials in diff
- [ ] `.gitignore` covers new build artifacts
- [ ] Commit uses project wrapper (`scripts/commit_all.sh`)
