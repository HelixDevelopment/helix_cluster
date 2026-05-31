# HelixCluster Phase 2 — Console Compute Nodes
## Executive Summary

### Project Context

HelixCluster Phase 1 established a distributed computing architecture that binds heterogeneous PCs and laptops into a single coherent compute block. Phase 2 extends this architecture to include **jailbroken PlayStation 4, PS4 Pro, PS5, and PS5 Pro consoles** as fully integrated worker nodes.

### Why Consoles?

The global installed base of PlayStation consoles exceeds **210 million units**. Millions of these devices spend the majority of their time in REST mode or idle — representing an enormous reservoir of untapped compute power. At used market prices of **$80-250 for PS4** and **$400-500 for PS5**, these devices deliver GPU compute at roughly **half the cost per TFLOP** of equivalent PC hardware.

### Console Hardware as Compute

| Console | CPU | GPU | RAM | Cost (Used) | GPU TFLOPS | $/TFLOP |
|---------|-----|-----|-----|-------------|------------|---------|
| PS4 Base | 8x Jaguar 1.6GHz | GCN 1.84 TF | 8GB GDDR5 | $80-150 | 1.84 | $81 |
| **PS4 Pro** | **8x Jaguar 2.1GHz** | **GCN 4.20 TF** | **8GB GDDR5** | **$150-250** | **4.20** | **$59** |
| **PS5** | **8c/16t Zen2 3.5GHz** | **RDNA2 10.3 TF** | **16GB GDDR6** | **$400-500** | **10.3** | **$49** |
| PS5 Pro | 8c/16t Zen2 3.85GHz | RDNA2+ ~33 TF | 16GB GDDR6+2GB | $550-700 | ~33 | ~$21 |

### Key Innovation: Linux on PlayStation

The foundational enabler for Phase 2 is **Linux on PlayStation** — a mature ecosystem for PS4 (kernels up to 6.15.4, Docker, full GPU acceleration) and a brand-new capability for PS5 (TheFlow's ps5-linux, April 2026, Ubuntu 24.04 with GPU support). This transforms consoles from closed gaming appliances into general-purpose Linux servers.

### Unique Capabilities Consoles Bring

1. **GPU Compute at Half PC Cost** — Discarded gaming hardware repurposed
2. **PS5 Custom I/O Decompressor** — 8-9 GB/s hardware decompression (no PC equivalent)
3. **GDDR5/GDDR6 Unified Memory** — 176-576 GB/s bandwidth vs DDR4's 25-50 GB/s
4. **Disposable Node Model** — At $80-250, failed nodes are replaced, not repaired
5. **Community Elastic Scaling** — Users can donate idle console time

### Architecture Approach

Console nodes run a **minimal Linux distribution** with our Console Node Agent. They connect to the cluster via WireGuard mesh, register with **TRUST_LEVEL=SEMI**, and execute workloads through the same Vulkan Compute Backend used by PC nodes. The **Console Adapter Layer** handles console-specific concerns: thermal management, power monitoring, jailbreak state detection, and auto-exploit triggering.

### PS3 Exclusion

The PlayStation 3's Cell Broadband Engine was thoroughly evaluated and **excluded** from Phase 2. Despite its historical significance (powering the Condor supercomputer at 500 TFLOPS for $2M), the Cell BE is obsolete: 192 GFLOPS vs a modern Ryzen 9's 2.7 TFLOPS, extreme programming complexity, dead toolchain ecosystem, and a single Raspberry Pi 4 outperforms it in most metrics.

### Phase 2 Scope

- **24 new implementation tasks**, ~176 hours (~4.5 weeks additional)
- Console Agent for PS4/PS5 Linux
- Vulkan Compute Backend (already universal — no console-specific GPU code needed)
- Semi-trusted security model with output verification
- Auto-exploit hardware integration for unattended operation
- PS5 Orbis OS native agent for custom I/O decompression
- llama.cpp AI inference on console GPU pool
- Console-aware scheduler plugin (thermal throttling awareness)

### Risk Summary

| Risk | Level | Mitigation |
|------|-------|------------|
| Semi-tethered jailbreak | Medium | Auto-exploit hardware (ESP32/Luckfox), REST mode persistence |
| Thermal throttling | Medium | Thermal monitoring, workload backoff, fan control |
| Kernel compromised (inherent) | High (contained) | SEMI trust model, encrypted work units, output verification |
| AVX2 absence (PS4) | Low | Use SSE4.2/AVX fallback paths, Vulkan compute for GPU work |
| Hardware availability | Low | 117M+ PS4s, 93M+ PS5s in market |
