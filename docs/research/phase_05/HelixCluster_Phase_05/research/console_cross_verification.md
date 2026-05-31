# HelixCluster Phase 2 — Console Research Cross-Verification

## Verified Findings

### HIGH CONFIDENCE (Multiple independent sources confirm)

**HC-1: PS4 Linux is Mature and Production-Ready for Cluster Compute**
- Confirmed by: dim01, dim04, dim05
- Kernel 6.15.4 available, Docker works, full GPU acceleration via Mesa
- Go/Zig/C++ compile natively, standard Linux network stack
- KVM virtualization confirmed on PS4 Pro

**HC-2: PS5 Linux Boot Achieved April 2026 (TheFlow's ps5-linux)**
- Confirmed by: dim02, dim04
- Ubuntu 24.04 with full GPU acceleration, 4K60 HDMI
- Zen 2 8c/16t @ 3.5 GHz, 16GB GDDR6, RDNA2 GPU
- M.2 SSD support, custom Ethernet driver

**HC-3: PS3 Cell BE NOT Viable for Modern Cluster Deployment**
- Confirmed by: dim03, dim06
- 192 GFLOPS SP vs Ryzen 9 7950X 2.7 TFLOPS (14x slower)
- Extreme programming difficulty, dead toolchain, 256MB RAM
- Raspberry Pi 4 outperforms in most metrics
- VERDICT: Excluded from HelixCluster Phase 2

**HC-4: ROCm Does NOT Work on PlayStation GPUs**
- Confirmed by: dim05
- ROCm dropped GCN 1.1/2.0 support, gfx1013 (PS5 APU) unsupported
- Mesa rusticl (OpenCL) works on PS4 GCN
- Vulkan compute is THE path for PS5 RDNA2

**HC-5: PS4 Pro Offers Best Cost/Performance Ratio ($59/TFLOP)**
- Confirmed by: dim01, dim06
- Used PS4 Pro $150-250, 4.2 TFLOPS GPU
- Full PC equivalent build costs $300+ minimum
- Power efficiency competitive with mid-range PCs

**HC-6: Jailbreak is Semi-Tethered (Non-Persistent)**
- Confirmed by: dim01, dim02
- Cold boot loses jailbreak, must re-exploit
- REST mode preserves jailbreak
- Auto-exploit hardware (Luckfox MCU, ESP32) can automate

**HC-7: PS5 Custom I/O Decompressor Inaccessible from Linux**
- Confirmed by: dim02
- Kraken decompressor (8-9 GB/s) only accessible via Orbis OS
- NOT available when running Linux
- This is a significant missed opportunity

**HC-8: gRPC Too Heavy for PS4 Jaguar CPU**
- Confirmed by: dim05
- ZeroMQ works well, lightweight protocols needed
- PS5 Zen 2 can handle gRPC without issues

**HC-9: Folding@Home Proved Console GPU Compute Viability**
- Confirmed by: dim03 (PS3), dim05 (PS4 potential)
- PS3 GPUs contributed massively to protein folding research
- llama.cpp Vulkan achieves 104 tok/s on PS5-class hardware (BC-250)

### CONFLICT ZONES

**CZ-1: Orbis OS Native vs Linux — Which Path?**
- dim01/dim04: OpenOrbis toolchain enables native Orbis homebrew
- dim04: Linux is far more mature, Docker works, standard tools
- RESOLUTION: Linux is primary path for PS4/PS5. Orbis OS native as fallback for special I/O access (PS5 decompressor).

**CZ-2: PS4 vs PS5 for Cluster — Cost vs Performance**
- dim01/dim06: PS4 Pro at $150-250 is best value per dollar
- dim02: PS5 at $400-500 offers 2.5x+ GPU, 4x CPU performance
- RESOLUTION: Mixed cluster — PS4 Pro as worker fleet (quantity), PS5 as compute accelerators (quality). Target ratio 4:1 PS4:PS5.

**CZ-3: Security Trust Model for Jailbroken Consoles**
- dim06: Semi-trusted only — no secure boot, no TPM
- dim01: GoldHen has full kernel access — console is COMPLETELY compromised
- RESOLUTION: Console nodes marked as TRUST_LEVEL=SEMI. Suitable only for non-critical compute with output verification. No sensitive data on console nodes. LLMsVerifier validates all console outputs.
