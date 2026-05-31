# HelixCluster Phase 3 — Edge & Mobile Device Integration
## Executive Summary

### The Vision: Billions of Devices, One Compute Pool

HelixCluster Phase 3 is our most ambitious expansion yet — integrating **Single Board Computers, Android phones/tablets/TV boxes, iOS devices, and HarmonyOS devices** into a single unified compute cluster. Where Phase 1 added PCs and Phase 2 added PlayStations, Phase 3 opens the door to the **billions of edge and mobile devices** that surround us every day.

### Why Edge & Mobile?

Consider this: there are over **3 billion Android devices**, **1 billion iPhones**, and **hundreds of millions of SBCs and TV boxes** in active use worldwide. Most of these devices spend the majority of their time idle — charging overnight, sitting on desks, playing nothing on the living room TV. The collective compute power of these idle devices dwarfs even the largest supercomputers.

Vodafone's DreamLab proved this concept: **100,000 smartphones running overnight calculations matched the speed of 30 supercomputers** for cancer research. Our mission is to harness this power systematically.

### What Phase 3 Adds

| Category | Devices | Count Potential | Unique Value |
|----------|---------|----------------|--------------|
| **SBCs** | Orange Pi 5 Max, Raspberry Pi 5 | 10-100 nodes | 16GB RAM, 6 TOPS NPU, 2.5GbE, $125 |
| **Android TV Boxes** | RK3588 boxes, Xiaomi MiBox | 10-50 nodes | $50-130, ARM64 Linux, 24/7 capable |
| **Android Phones** | Samsung, Pixel, Xiaomi | 100+ devices | Charging-gated, billions available |
| **Android Tablets** | Samsung Tab, Xiaomi Pad | 10-50 devices | Large screens, good thermals |
| **iOS Devices** | iPhone 16 Pro, iPad Pro M4 | 10-50 devices | 35-38 TOPS NPU, Metal GPU |
| **HarmonyOS** | Huawei MatePad Pro | 5-10 devices | Da Vinci NPU, Super Device |

### Key Innovation: The "Overnight Supercomputer"

Phase 3's core innovation is the **charging-gated compute model**: mobile devices only receive work when they are (1) plugged in, (2) on WiFi, and (3) during configured hours (typically overnight). This model — proven by DreamLab, Folding@home, and BOINC — makes phone-based distributed computing practical without impacting user experience.

### Architecture Approach

- **SBCs & TV Boxes (Armbian)**: Run standard Linux Node Agent, first-class citizens
- **Android Phones**: APK with Termux foreground service + Vulkan compute
- **iOS Devices**: Native app with Metal/CoreML, pull-based donor model
- **HarmonyOS**: ArkTS app with Da Vinci NPU integration
- **All devices**: Semi-trusted security model with output verification

### Device Tier Classification

| Tier | Devices | Trust Level | Role |
|------|---------|-------------|------|
| T3 | Orange Pi 5 Max | STANDARD | Full worker with NPU |
| T4 | Raspberry Pi 5, RK3588 TV boxes | STANDARD | Standard worker |
| T5 | Android TV Box (Armbian) | STANDARD | Headless worker |
| T6 | Android Phone/Tablet | SEMI | Charging-gated compute |
| T7 | iPhone/iPad | EDGE_DONOR | Opportunistic inference |
| T8 | HarmonyOS device | SEMI | NPU inference |

### Investment

- **26 new implementation tasks**, ~200 hours (~5 weeks)
- Reference hardware investment: ~$500 (5-10 test devices)
- Potential compute return: **100+ NPU TOPS, 500+ CPU cores, 256GB+ RAM**
