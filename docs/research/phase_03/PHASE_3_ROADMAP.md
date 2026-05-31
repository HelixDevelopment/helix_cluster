# Phase 3 Roadmap — Edge Computing & Mobile Devices

## Overview

Phase 3 extends HelixCluster to edge computing nodes and mobile devices, enabling smartphones, tablets, ARM SBCs, and IoT devices to participate as first-class cluster citizens. This unlocks volunteer computing at massive scale (billions of devices) and enables edge-native workloads like real-time inference, sensor fusion, and offline-first processing.

## Scope

### In Scope
- ARM SBC node support (Raspberry Pi, Jetson, RK3588)
- Mobile device agent (Android via Termux, iOS via iSH)
- Edge-native workload types (inference, sensor fusion, stream processing)
- Offline capability with sync-on-connect
- Battery-aware scheduling
- Low-bandwidth protocol optimizations

### Out of Scope
- Apple Watch / wearable support (excluded — no viable Linux path)
- Smart home speaker support (Echo, HomePod excluded)
- Xbox / PlayStation mobile apps (separate evaluation)

## Timeline

| Week | Deliverable | HXC |
|------|-------------|-----|
| 1 | ARM SBC agent (64-bit ARM, Docker-capable) | HXC-915 |
| 2 | Jetson GPU backend (CUDA on ARM) | HXC-916 |
| 3 | Android Termux agent, battery-aware scheduling | HXC-917 |
| 4 | iOS iSH agent (x86 emulation layer) | HXC-918 |
| 5 | Offline sync protocol, delta compression | HXC-919 |
| 6 | Edge workload types, sensor fusion framework | HXC-920 |

## Milestones

| Milestone | Week | Acceptance Criteria | Status |
|-----------|------|---------------------|--------|
| ARM SBC join | 1 | Raspberry Pi 4/5 joins cluster, runs Docker workload | ✅ Done |
| Jetson CUDA | 2 | Jetson Orin Nano runs CUDA inference, >50 TOPS | ✅ Done |
| Android agent | 3 | Android phone (Termux) joins cluster, survives doze mode | ✅ Done |
| iOS agent | 4 | iPhone (iSH) joins cluster, basic compute verified | ✅ Done |
| Offline sync | 5 | Device completes jobs offline, syncs results on reconnect | ✅ Done |
| Sensor fusion | 6 | 3+ edge devices fuse sensor streams, real-time output | ✅ Done |

## Dependencies

- Phase 1 (MVP): Node agent, discovery, scheduler
- Phase 2: Console nodes (thermal management patterns)
- pkg/device: Device classification (Phase 5 taxonomy)
- internal/wireguard: Mesh networking

## Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|------|------------|--------|------------|
| Mobile OS kills background app | High | High | Foreground service, persistent notification, aggressive heartbeat |
| Battery drain complaints | High | Medium | Charge-only mode, battery threshold scheduling |
| Network churn (WiFi/cellular) | High | Medium | QUIC transport, 0-RTT reconnect, stateful resume |
| Device diversity (10K+ models) | Medium | High | Capability-based discovery, not model-based |

## Success Criteria

1. ARM SBC achieves ≥80% of x86 per-watt performance
2. Jetson Orin Nano delivers ≥50 TOPS inference
3. Android agent survives 24h with <5% battery drain/hour
4. iOS agent completes basic compute job end-to-end
5. Offline sync recovers 100% of jobs after 72h disconnect
6. 10+ edge devices participate in sensor fusion pipeline

---

*Phase 3 Roadmap — Helix Cluster OS*
