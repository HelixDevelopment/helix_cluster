# Phase 2 Roadmap — Console Compute Nodes

## Overview

Phase 2 extends the HelixCluster distributed computing architecture to include jailbroken PlayStation 4, PS4 Pro, PS5, and PS5 Pro consoles as fully integrated worker nodes. With a global installed base exceeding 210 million units, consoles represent an enormous reservoir of untapped compute power at roughly half the cost per TFLOP of equivalent PC hardware.

## Scope

### In Scope
- Console Node Agent for PS4/PS5 Linux
- Vulkan Compute Backend integration (universal, no console-specific GPU code)
- Semi-trusted security model with output verification
- Console-aware scheduler plugin (thermal throttling awareness)
- Auto-exploit hardware integration for unattended operation
- PS5 Orbis OS native agent for custom I/O decompression
- llama.cpp AI inference on console GPU pool

### Out of Scope
- PS3 Cell Broadband Engine support (excluded — obsolete, dead toolchain)
- Xbox console support (no viable Linux pathway)
- Nintendo Switch support (separate Phase 5 evaluation)

## Timeline

| Week | Deliverable | HXC |
|------|-------------|-----|
| 1 | Console Node Agent scaffolding, Linux boot verification | HXC-909 |
| 2 | WireGuard mesh integration for console nodes | HXC-910 |
| 3 | Vulkan Compute Backend binding, GPU acceleration test | HXC-911 |
| 4 | Semi-trusted security model, output verification | HXC-912 |
| 5 | Scheduler thermal plugin, auto-exploit integration | HXC-913 |
| 6 | PS5 I/O decompressor agent, llama.cpp inference pool | HXC-914 |

## Milestones

| Milestone | Week | Acceptance Criteria | Status |
|-----------|------|---------------------|--------|
| Console Linux boot | 1 | PS4/PS5 boots Linux, SSH accessible, `uname -a` returns Linux | ✅ Done |
| WireGuard mesh join | 2 | Console node registers in discovery, pingable from scheduler | ✅ Done |
| GPU compute verify | 3 | Vulkan compute sample executes on console GPU, returns correct result | ✅ Done |
| Security model | 4 | Console node runs with TRUST_LEVEL=SEMI, output verified by controller | ✅ Done |
| Thermal scheduler | 5 | Scheduler throttles jobs when console temp > 80°C | ✅ Done |
| AI inference pool | 6 | llama.cpp runs on 4+ console GPU pool, >10 tok/sec aggregate | ✅ Done |

## Dependencies

- Phase 1 (MVP): Distributed computing primitives, node agent, WireGuard mesh
- pkg/security: TRUST_LEVEL framework
- pkg/scheduler: Job scheduling with constraints
- internal/gpu: Vulkan Compute Backend

## Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|------|------------|--------|------------|
| Sony firmware patches jailbreak | Medium | High | Maintain exploit chain diversity, auto-update detection |
| Console thermal throttling | High | Medium | Thermal-aware scheduler, aggressive cooling profiles |
| GDDR5/GDDR6 memory errors | Medium | High | ECC-like verification, redundant computation |
| Community trust (donated cycles) | Medium | High | Transparent resource accounting, opt-in only |

## Success Criteria

1. PS4 Pro achieves ≥3.5 TFLOPS sustained GPU compute in cluster
2. PS5 achieves ≥8 TFLOPS sustained GPU compute in cluster
3. Console nodes participate in WireGuard mesh with <50ms latency
4. Thermal throttling reduces job errors by >90%
5. 4+ console pool runs llama.cpp 7B at >10 tok/sec

---

*Phase 2 Roadmap — Helix Cluster OS*
