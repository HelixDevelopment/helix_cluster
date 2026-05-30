# Research Area: HarmonyOS Devices (Huawei MatePad, etc.) as Compute Nodes

**Date:** 2025-07-31
**Searches Conducted:** 18 independent queries across architecture, benchmarks, native development, distributed computing, GPU/NPU compute, security, networking, and deployment topics.
**Sources:** Huawei Developer Docs, Wikipedia, GSMArena, HotHardware, CNBC, Medium/Huawei Developers, XDA Forums, GitHub, nanoreview, cputronic.com, benchmarks.ul.com, OpenHarmony documentation, Dev.to, and community blogs.

---

## Key Findings

### HarmonyOS Architecture & Distributed Computing

- HarmonyOS uses a **distributed architecture** with a microkernel design at its core. The system is built around the concept of a **"Super Device"** where multiple devices (phones, tablets, wearables, TVs) can pool their hardware resources into a unified virtual device [^1636^][^1640^].

- The **Distributed Soft Bus** acts as the communication backbone between devices, providing a unified communication capability with seamless, zero-waiting transmission. It abstracts networking and protocols away from developers [^1639^][^1646^].

- **Distributed Device Virtualization** allows resources to be fused across devices, creating a "super virtual terminal." Tasks can be assigned to the most appropriate hardware. A phone can use a tablet's display, or a tablet can use a phone's NPU [^1639^][^1687^].

- **Distributed Task Scheduling** provides a unified service management mechanism (discovery, synchronization, registration, and call) that supports remote application operations including startup, call, connection, and migration. It intelligently chooses the most suitable device based on capabilities, location, status, resources, and user habits [^1639^][^1688^].

- Starting with **HarmonyOS 5 (HarmonyOS NEXT)**, Huawei replaced the common Linux kernel with its own bespoke **HarmonyOS microkernel**, discarding the AOSP framework entirely. This is a clean break from Android [^1640^].

### Kirin 9000S Performance

- The **Kirin 9000S** is built on a **7nm process** (SMIC N+3), featuring an octa-core CPU: 1x 2.62 GHz Taishan big core + 3x 2.15 GHz Taishan mid cores + 4x 1.53 GHz Cortex-A510 efficiency cores [^1639^][^1668^].

- **Benchmarks vs Snapdragon 8 Gen 3 (4nm):**
  - Geekbench 6 Single-Core: Kirin 9000S ~1,222-1,324 vs Snapdragon 8 Gen 3 ~2,192 (**~79% slower**) [^1668^][^1671^]
  - Geekbench 6 Multi-Core: Kirin 9000S ~3,622-4,116 vs Snapdragon 8 Gen 3 ~7,085-7,304 (**~96% slower**) [^1668^][^1671^]
  - AnTuTu 10: Kirin 9000S ~823,241 vs Snapdragon 8 Gen 3 ~2,052,015 (**~150% slower**) [^1671^]
  - 3DMark Wild Life: Kirin 9000S ~4,940 vs Snapdragon 8 Gen 3 ~14,979 (**~203% slower on GPU**) [^1671^]

- The Kirin 9000S uses **Maleoon 910 GPU** (Huawei in-house design, not Mali/Adreno). It supports **Vulkan 1.0** and **OpenCL 2.0** [^1671^][^1698^]. The GPU is Tile-Based architecture and supports compute shaders, subgroup operations, and loop reduction via OpenCL/Vulkan extensions [^1698^][^1699^].

- Performance roughly comparable to **Snapdragon 888** (2021 flagship) or **Snapdragon 7+ Gen 2**. Not a viable flagship competitor to Snapdragon 8 Gen 2/3, but impressive given domestic Chinese manufacturing constraints [^1645^][^1684^].

- **Thermal throttling** occurs earlier during sustained loads (e.g., 30+ minute Genshin Impact sessions cause frame drops). The 7nm process and thermal envelope limit sustained performance vs. 4nm competitors [^1684^][^1679^].

### Native Development (C/C++)

- HarmonyOS provides a **Native Development Kit (NDK)** with full C/C++ support. The NDK includes: C runtime (musl libc), C++ runtime (libc++_shared), graphics libraries (OpenGL ES, Vulkan), window system, multimedia, compression (zlib/libz), libuv async I/O, Node-API for ArkTS/C++ interop, and Rawfile resource access [^1648^][^1662^].

- **Node-API (NAPI)** is the bridge between ArkTS and C/C++, similar to Android JNI. It allows ArkTS code to call C++ functions and access native dynamic libraries [^1657^][^1660^].

- DevEco Studio (based on IntelliJ IDEA) provides IDE support with multi-language debugging, hot reload, and cross-language code generation tools for NAPI glue code [^1657^][^1658^].

- The **BiSheng compiler** (based on LLVM) is Huawei's native C/C++ compiler offering optimizations beyond standard LLVM/GCC. Available in DevEco Studio 5.0.3.402+ [^1661^].

- NDK supports CPU feature customization including **NEON acceleration**. Target architectures: arm64-v8a (aarch64) [^1648^].

- **Rust** can be used via cross-compilation to `aarch64-unknown-linux-ohos` target. Community has demonstrated Rust+Cangjie mixed programming [^1731^].

- **Zig** has early-stage community support via `harmony-contrib/zig-addon` project for building native modules [^1690^].

- **Go** has no native HarmonyOS target, but could potentially run via WebAssembly (WASI) or be compiled as a shared library via `c-shared` mode through Zig's Go toolchain support. No direct evidence of Go native compilation found.

### GPU Compute Support

- **Maleoon 910 GPU** (Kirin 9000S) supports:
  - **OpenGL ES 3.1+**
  - **Vulkan 1.0** (compute shaders supported)
  - **OpenCL 2.0** [^1671^][^1698^]

- Huawei provides official **GPU best practices documentation** for compute shading optimization: recommended workgroup sizes of 32 or 64, subgroup extensions for loop reduction, barrier optimization, and low-precision (mediump) shader optimization [^1698^][^1699^].

- **Graphics Profiler** tool available for GPU analysis and optimization, supporting system/application-level trace recording for OpenGL ES and Vulkan, Maleoon GPU performance counters, and multi-GB data capture [^1700^].

- Key GPU compute limitation: Maleoon 910 supports Vulkan 1.0 only (not 1.3 like Adreno 750), limiting some modern compute features. OpenCL 2.0 is supported but not the latest versions [^1671^].

### DaVinci NPU & AI Compute

- Kirin 9000S includes a **dual big-core + tiny-core NPU** based on Huawei's **Da Vinci architecture**. The Da Vinci NPU supports INT8/INT16 precision [^1643^][^1656^].

- **CANN (Compute Architecture for Neural Networks)** is Huawei's heterogeneous computing framework for AI, providing unified access for AI models on HarmonyOS devices. CANN coordinates NPU, CPU, and GPU for optimal inference performance [^1650^].

- **HiAI Foundation Kit** provides C APIs for model compilation, loading, and inference on the NPU. Includes support for single-op execution (convolution, fused conv+activation) and tensor operations [^1686^].

- The Da Vinci NPU cube units deliver up to 4,096 FP16 MACs or 8,192 INT8 MACs per core [^1656^].

- On-device inference keeps data local, reducing network dependency and ensuring privacy [^1650^].

- Third-party camera apps cannot fully leverage the NPU — AI acceleration is tightly coupled to Huawei's proprietary firmware [^1684^].

### Background Execution Model

- HarmonyOS provides **4 types of background tasks** [^1672^][^1678^]:
  1. **Transient Tasks** — Short-lived background extensions (max 3 min per request, 10 min daily quota)
  2. **Continuous Tasks** — Long-running tasks like audio/navigation/data transfer (one per UIAbility, system checks that task matches claimed type)
  3. **Deferred Tasks** — Non-urgent jobs scheduled by system based on battery, network, charging state (max 10, 2 min per callback)
  4. **Agent-powered Reminders** — System-handled timed notifications even when app is killed (requires Huawei permission, tool apps only)

- **WorkScheduler API** allows scheduling tasks based on conditions: WiFi connectivity, charging state, battery level, cron-like scheduling [^1674^].

- System aggressively manages background apps for power — apps suspended when backgrounded unless they request specific background task types [^1672^].

- Continuous tasks claiming excessive CPU/memory for long periods may be cancelled by the system [^1672^].

### Network Capabilities

- **MatePad Pro 13.2 (2024)** supports:
  - **Wi-Fi 6 (802.11ax)** dual-band 2.4 GHz & 5 GHz [^1739^][^1741^]
  - **Bluetooth 5.2** with BLE, AAC, LDAC [^1741^]
  - **NearLink** — Huawei's proprietary short-range wireless protocol [^1741^]
  - **USB Type-C 3.1** with DisplayPort 1.2, OTG [^1739^]
  - GPS/GLONASS/BeiDou/Galileo/QZSS positioning [^1741^]
  - 4G LTE cellular on SIM-enabled models [^1739^]

- **HarmonyOS Network Kit** provides full socket support: TCP, UDP, WebSocket, HTTP, mDNS for device discovery. Standard socket APIs available in ArkTS via `@kit.NetworkKit` [^1666^][^1681^].

- **Distributed Soft Bus** provides inter-device networking abstraction with zero-waiting transmission, automatic discovery, and topology management [^1639^][^1646^].

- Wi-Fi 6E (6 GHz) support listed on some variants but unconfirmed on all models [^1736^]. No Wi-Fi 7 (unlike Snapdragon 8 Gen 3 devices) [^1671^].

### Security Model

- **App sandboxing**: Each app has an exclusive sandbox directory (`/data/data/`) inaccessible to other apps. Apps can only access their own files [^1675^].

- **Permission management**: Dynamic permission system with principle of least privilege. Real-time permission monitoring triggers "privacy shield" when background apps abnormally access camera/microphone [^1675^].

- **Data encryption**: AES-256 for local files, RSA for key exchange, full lifecycle key management by TEE (Trusted Execution Environment) [^1675^].

- **Microkernel + Formal verification**: HarmonyOS microkernel uses formal verification methods in TEE. The microkernel has roughly 1/1000th the code of Linux kernel, reducing attack surface [^1636^][^1689^].

- HarmonyOS is the **first OS to use formal verification in device TEE** [^1689^].

- Access Token Manager implements unified app permission management combining RBAC and CBAC models [^1665^].

### Memory & App Limits

- **MatePad Pro 13.2**: Available in 12GB RAM (256GB/512GB storage) and 16GB RAM (1TB storage) variants [^1739^].

- App memory limits are managed dynamically by the system. One developer reported OOM crashes before 1GB allocation on HarmonyOS 2.0 with `android:largeHeap="true"` [^1692^]. System uses aggressive memory management — may be more restrictive than Android.

- **"Super Memory Management"** in HarmonyOS NEXT can integrate physical memory of multiple devices into a continuous address space via cross-device virtual memory pool [^1687^].

- Background app survival rate increased by 23% in HarmonyOS NEXT vs traditional systems [^1687^].

### OpenHarmony vs Commercial HarmonyOS

- **OpenHarmony** is the fully open-source version donated to OpenAtom Foundation. It contains basic HarmonyOS capabilities and does not depend on AOSP [^1665^][^1693^].

- **HarmonyOS** is Huawei's commercial distribution of OpenHarmony, adding proprietary services and deep hardware optimization for Huawei devices [^1710^].

- OpenHarmony supports Linux kernel (4.19 or 5.10) and LiteOS, with POSIX API compatibility via musl libc [^1744^][^1665^].

- OpenHarmony has **1200+ standard POSIX APIs**, virtual memory, multi-core scheduling, lwIP networking, and the HDF driver framework [^1744^].

- Community has **10,000+ code contributors** and 1.2 billion devices as of early 2026 [^1665^]. Eclipse Foundation has an Oniro project based on OpenHarmony.

- **OpenHarmony can be used as an alternative** for building custom compute node images, but would require significant porting effort to match commercial HarmonyOS distributed features.

### Deployment via AppGallery

- Apps distributed through **Huawei AppGallery** via AppGallery Connect [^1701^][^1704^].

- For HarmonyOS NEXT apps: must use native HarmonyOS app format (no APK). Apps must be built with ArkTS/C++ and signed with Huawei's signing service [^1701^].

- 30,000+ apps and atomic services available as of late 2025, with top 5,000 apps accounting for ~99.9% of user time [^1691^].

- GitHub Actions integration available for CI/CD deployment to AppGallery [^1707^].

### Power & Battery

- **MatePad Pro 13.2**: 10,100 mAh battery (38.89 Wh, dual-cell design) [^1739^][^1741^].
- 88W-100W wired fast charging (85% in 40 minutes, full charge ~57 minutes) [^1739^][^1634^].
- Battery life: Active use score ~8h 15min (GSMArena). Kirin 9020 (successor) improved active use by 3+ hours over Kirin 9000S thanks to efficiency gains [^1634^].
- HarmonyOS NEXT: 18% better battery life than traditional systems via five-dimensional power management (screen, network, app status, temperature, user behavior) [^1687^].

---

## Technical Specifications

### Kirin 9000S (System-on-Chip)
| Component | Specification |
|-----------|--------------|
| Process Node | 7nm (SMIC N+3) |
| CPU | 1x Taishan V120 @ 2.62 GHz + 3x Taishan @ 2.15 GHz + 4x Cortex-A510 @ 1.53 GHz |
| GPU | Maleoon 910 MP4 @ 750 MHz, Tile-Based |
| NPU | Da Vinci Architecture, dual big-core + tiny core |
| RAM | Up to 16GB LPDDR5 (44 GB/s bandwidth) |
| Storage | UFS 3.1 / UFS 4.0 |
| Display | Up to 4K@60FPS |
| Video | 4K@60FPS recording/playback (H.264, H.265, VP9) |
| 5G | Integrated (NSA/SA, Sub-6 GHz) |
| Wi-Fi | Wi-Fi 6 (802.11ax) |
| Bluetooth | 5.2 |
| Vulkan | 1.0 |
| OpenCL | 2.0 |
| OpenGL ES | 3.1+ |
| Process tech power | ~6-7W TDP |

### Huawei MatePad Pro 13.2 (2024)
| Component | Specification |
|-----------|--------------|
| Display | 13.2" OLED, 2880x1920, 144Hz, HDR, 1000 nits peak |
| Processor | Kirin 9000S |
| RAM | 12GB / 16GB |
| Storage | 256GB / 512GB / 1TB UFS 3.1 |
| Battery | 10,100 mAh (38.89 Wh) |
| Charging | 88W-100W wired |
| Wi-Fi | 802.11a/b/g/n/ac/ax (Wi-Fi 6) |
| Bluetooth | 5.2 (A2DP, LE, L2HC) |
| USB | Type-C 3.1, OTG, DisplayPort 1.2 |
| Weight | 580g |
| Thickness | 5.5mm |
| OS | HarmonyOS 4.0 / 4.3 (upgradeable to HarmonyOS NEXT) |

---

## Major Projects & Tools

| Project | Description | URL | Status |
|---------|-------------|-----|--------|
| **OpenHarmony** | Open-source foundation of HarmonyOS | https://gitee.com/openharmony | Active, 10K+ contributors |
| **DevEco Studio** | Official IDE for HarmonyOS development | https://developer.harmonyos.com | Active, v5.x |
| **CANN** | Huawei's AI computing framework | https://developer.huawei.com/consumer/en/doc/hiai-guides/ | Production-ready |
| **HiAI Foundation** | On-device AI inference APIs | https://developer.huawei.com/consumer/en/doc/harmonyos-references-V5/hiaifoundation-V5 | API 11+ |
| **Graphics Profiler** | GPU debugging/optimization tool | https://developer.huawei.com/consumer/en/doc/tools-guides/overview | Production-ready |
| **harmony-contrib/zig-addon** | Zig language native module builder | https://github.com/harmony-contrib/zig-addon | Early stage |
| **Awesome-HarmonyOS** | Curated list of HarmonyOS resources | https://github.com/Awesome-HarmonyOS/HarmonyOS | Community |
| **zig-on-harmonyos** | LuaJIT with HarmonyOS cross-compile support | https://github.com/zhaozg/luajit-cmake | Community |
| **Eclipse Oniro** | OpenHarmony-based vendor-neutral OS | https://eclipse.org/oniro | Active |

---

## Code Examples

### NAPI: ArkTS calling C++

**index.d.ts (TypeScript declarations):**
```typescript
export const add: (a: number, b: number) => number;
```

**napi_init.cpp (C++ implementation):**
```cpp
#include "napi/native_api.h"

static napi_value Add(napi_env env, napi_callback_info info) {
    size_t argc = 2;
    napi_value args[2] = {nullptr};
    napi_get_cb_info(env, info, &argc, args, nullptr, nullptr);

    double value0, value1;
    napi_get_value_double(env, args[0], &value0);
    napi_get_value_double(env, args[1], &value1);

    napi_value sum;
    napi_create_double(env, value0 + value1, &sum);
    return sum;
}

EXTERN_C_START
static napi_value Init(napi_env env, napi_value exports) {
    napi_property_descriptor desc[] = {
        { "add", nullptr, Add, nullptr, nullptr, nullptr, napi_default, nullptr }
    };
    napi_define_properties(env, exports, sizeof(desc)/sizeof(desc[0]), desc);
    return exports;
}
EXTERN_C_END

static napi_module demoModule = {
    .nm_version = 1, .nm_flags = 0, .nm_filename = nullptr,
    .nm_register_func = Init, .nm_modname = "ndk1",
    .nm_priv = ((void*)0), .reserved = { 0 }
};
extern "C" __attribute__((constructor)) void RegisterNdk1Module(void) {
    napi_module_register(&demoModule);
}
```

### Network Socket (TCP client in ArkTS)

```typescript
import { socket } from '@kit.NetworkKit';
import { BusinessError } from '@kit.BasicServicesKit';

// Create TCP socket
const tcpSocket = socket.constructTCPSocketInstance();

// Connect to server
const address: socket.NetAddress = {
    address: '192.168.1.100',
    port: 8080
};
tcpSocket.connect(address).then(() => {
    console.info('TCP connected');
    // Send data
    tcpSocket.send({ data: 'Hello Compute Node' }).then(() => {
        console.info('Data sent');
    });
}).catch((err: BusinessError) => {
    console.error(`Connect failed: ${err.code}`);
});
```

### Background Continuous Task (ArkTS)

```typescript
import { backgroundTaskManager } from '@kit.BackgroundTaskKit';

// Request continuous task for long-running computation
const wantAgentInfo: backgroundTaskManager.WantAgentInfo = {
    wants: [{ bundleName: 'com.example.compute', abilityName: 'ComputeAbility' }],
    operationType: backgroundTaskManager.OperationType.START_ABILITY,
    requestCode: 0
};

const continuousTaskConfig: backgroundTaskManager.ContinuousTaskConfig = {
    notificationId: 1,
    notificationContent: { title: 'Compute Node', text: 'Running distributed compute...' },
    notificationSlotType: notificationManager.SlotType.SERVICE_INFORMATION,
    wantAgentInfo: wantAgentInfo,
    backgroundMode: backgroundTaskManager.BackgroundMode.DATA_TRANSFER
};

backgroundTaskManager.startBackgroundRunning(context, continuousTaskConfig)
    .then(() => console.info('Background task started'));
```

### HiAI NPU Model Inference (C API)

```c
#include "neural_network_core.h"

// Create tensor descriptor
HiAI_SingleOpTensorDesc* desc = HMS_HiAISingleOpTensorDesc_Create(
    dims, dimNum, dataType, format, false
);

// Create tensor from descriptor
HiAI_SingleOpTensor* tensor = HMS_HiAISingleOpTensor_CreateFromTensorDesc(desc);

// Load model and run inference (model compilation/loading steps required)
// HiAI Foundation supports Caffe/ONNX model conversion via CANN toolchain
```

### Rust Cross-Compilation for HarmonyOS

```bash
# Install Rust targets
rustup target add aarch64-unknown-linux-ohos

# Build dynamic library for HarmonyOS
cargo build --target aarch64-unknown-linux-ohos --release
# Output: target/aarch64-unknown-linux-ohos/release/librslib.so
```

---

## Key Questions Answered

### How does HarmonyOS distributed computing work (Super Device)?

HarmonyOS implements distributed computing through four key technologies [^1639^][^1646^]:
1. **Distributed Soft Bus** — Unified communication layer abstracting networking (TCP/UDP/BT/NearLink)
2. **Distributed Device Virtualization** — Makes remote hardware (display, camera, NPU, sensors) appear as local resources
3. **Distributed Data Management** — Shared data layer across devices
4. **Distributed Task Scheduling** — Intelligent task distribution based on device capabilities, battery, and load

Devices automatically discover each other and form a "Super Device" where apps can span multiple devices seamlessly. The Distributed Scheduler supports remote ability launch, remote calling, and migration [^1688^].

### Can we run native C/C++ on HarmonyOS?

**Yes.** The NDK provides full C/C++ support with musl libc, libc++_shared, OpenGL ES, Vulkan, OpenCL, libuv, and Node-API for ArkTS interop [^1648^][^1662^]. C++ is recommended for compute-intensive scenarios. The BiSheng compiler provides optimized code generation [^1661^].

### What's the performance of Kirin 9000S vs Snapdragon 8 Gen 3?

The Kirin 9000S is significantly behind the Snapdragon 8 Gen 3: ~79% slower single-core, ~96% slower multi-core, and ~203% slower GPU in benchmarks [^1668^][^1671^]. However, it roughly matches the Snapdragon 888 (2021 flagship) [^1645^]. The 7nm process vs 4nm accounts for much of this gap. AI/NPU workloads are competitive thanks to the Da Vinci NPU [^1684^].

### How to access DaVinci NPU for AI inference?

Use the **HiAI Foundation Kit** C APIs [^1686^] or **CANN** framework [^1650^]. Models need conversion to Huawei's format via the CANN toolchain. The NPU supports INT8/INT16 precision. Single-op APIs available for convolution, fused operations, and custom layers. Models trained in PyTorch/TensorFlow can be converted to `.om` format for on-device inference.

### Can HarmonyOS devices communicate over network (sockets)?

**Yes.** HarmonyOS Network Kit provides full TCP, UDP, WebSocket, and HTTP support [^1666^][^1681^]. Standard socket programming works in ArkTS via `@kit.NetworkKit`. The Distributed Soft Bus additionally provides higher-level inter-device communication abstractions [^1639^].

### What background execution is possible?

Four background task types available [^1672^]: transient (short, up to 3 min), continuous (long-running, data transfer/audio/nav), deferred (system-scheduled based on conditions), and agent reminders. Compute-intensive continuous tasks may be cancelled if they cause excessive CPU/memory usage. The system is **more restrictive than Android** for background execution.

### How to deploy compute apps on HarmonyOS?

Through **AppGallery Connect** [^1701^][^1704^]. Apps must be native HarmonyOS format (not APK for NEXT). Requires HUAWEI Developer ID, app signing via Huawei's service, and passing review guidelines. CI/CD integration available via GitHub Actions [^1707^].

### Can OpenHarmony be used instead of commercial HarmonyOS?

**Yes**, but with tradeoffs. OpenHarmony has POSIX APIs, Linux kernel support, and the full distributed framework [^1744^][^1665^]. However, it lacks Huawei's proprietary optimizations, HiAI/CANN integration, and Super Device features. Suitable for building custom compute node firmware but would require significant engineering to replicate HarmonyOS distributed capabilities.

### What's the GPU compute support (Vulkan, OpenCL)?

Maleoon 910 supports **Vulkan 1.0** and **OpenCL 2.0** with compute shaders [^1671^][^1698^]. Huawei provides official documentation for GPU compute optimization. Limitations: Vulkan 1.0 (not 1.3), no support for the latest OpenCL versions. Graphics Profiler tool available for debugging GPU compute workloads.

### Can we compile Go or Zig for HarmonyOS?

- **Zig**: Early-stage community support via `harmony-contrib/zig-addon` [^1690^]. Can build native `.so` libraries.
- **Go**: No native HarmonyOS target found. Could potentially work via WebAssembly (WASI) runtime or through Cgo/Zig cross-compilation toolchain. Not recommended for production.
- **Rust**: Supported via `aarch64-unknown-linux-ohos` target with community tooling [^1731^].
- **Cangjie**: Huawei's new Rust-like language (developer preview) that compiles to native code on HarmonyOS [^1725^][^1734^].

### How does Device Virtualization enable distributed compute?

Device Virtualization makes remote device hardware appear as local resources [^1639^]. A tablet can access a phone's NPU for AI inference, or a phone can use a tablet's larger display. This creates a "capability pool" where compute tasks can be offloaded to the most appropriate device. Combined with Distributed Task Scheduling, the OS can automatically route compute-intensive tasks to devices with available CPU/GPU/NPU resources [^1687^].

### Memory available to apps on MatePad Pro?

Device comes with 12GB or 16GB RAM [^1739^]. However, the system uses aggressive per-app memory limits. One developer reported OOM crashes allocating <1GB on HarmonyOS 2.0 [^1692^]. HarmonyOS NEXT introduces "Super Memory Management" with cross-device virtual memory pooling [^1687^]. Expect single-app limits of ~2-4GB depending on system state.

### Network capabilities (WiFi6, 5G, mesh)?

- **Wi-Fi 6 (802.11ax)** dual-band confirmed [^1739^]
- Wi-Fi 6E (6 GHz) on some variants [^1736^]
- **No Wi-Fi 7** (Snapdragon 8 Gen 3 has Wi-Fi 7) [^1671^]
- Bluetooth 5.2 + **NearLink** (Huawei proprietary low-latency protocol) [^1741^]
- 4G LTE on cellular models, integrated 5G via Kirin modem
- USB-C 3.1 with DisplayPort 1.2 for wired connectivity

### Power management and thermal characteristics?

- 7nm process with ~6-7W TDP [^1682^]
- Thermal throttling occurs earlier than 4nm competitors under sustained loads [^1684^]
- 10,100 mAh battery provides ~8h active use [^1739^]
- HarmonyOS NEXT: 18% better battery life, 23% better background app survival [^1687^]
- Five-dimensional power management (screen, network, app status, temperature, user behavior) [^1687^]
- 100W fast charging provides rapid recovery between compute sessions

---

## Raw Evidence Log

### Evidence 1: HarmonyOS Microkernel Architecture
Claim: HarmonyOS NEXT uses a bespoke microkernel, discarding Linux and AOSP entirely
Source: Wikipedia — HarmonyOS 5
URL: https://en.wikipedia.org/wiki/HarmonyOS_5
Date: 2024-01-29
Excerpt: "HarmonyOS NEXT both discards the common Unix-like Linux kernel and replaces the previous multikernel system with its own bespoke HarmonyOS microkernel."
Confidence: High

### Evidence 2: Kirin 9000S Benchmarks
Claim: Kirin 9000S significantly trails Snapdragon 8 Gen 3 in all benchmarks
Source: nanoreview.info
URL: https://nanoreview.info/en/compare/soc/hisilicon-kirin-9000s-vs-qualcomm-snapdragon-8-gen-3
Date: 2025
Excerpt: "Geekbench 6 Single-Core: Kirin 9000S 1324 vs SD8G3 2193. AnTuTu 10: Kirin 9000S 823,241 vs SD8G3 2,052,015. 3DMark Wild Life: Kirin 9000S 4940 vs SD8G3 14979."
Confidence: High

### Evidence 3: NDK Native Development
Claim: HarmonyOS NDK provides comprehensive C/C++ development support
Source: Huawei Developer Documentation
URL: https://developer.huawei.com/consumer/en/doc/harmonyos-guides/ndk-development-overview
Date: 2026-04-09
Excerpt: "The NDK covers only some basic underlying capabilities of HarmonyOS, such as the C runtime libc, graphics library, window system, multimedia, compression library, and Node-API that bridges ArkTS/JS and C code."
Confidence: High

### Evidence 4: Background Task Types
Claim: HarmonyOS provides 4 structured background task types with specific quotas
Source: Huawei Developers Medium
URL: https://medium.com/huawei-developers/introduction-to-background-tasks-in-harmonyos-next-0f8787706ba6
Date: 2025-07-10
Excerpt: "There are 4 types of background tasks: Transient Tasks (max 3 min, 10 min daily), Continuous Tasks (one per UIAbility), Deferred Tasks (max 10, 2 min each), Agent-powered Reminders (max 30, tool apps only)."
Confidence: High

### Evidence 5: Distributed Task Scheduling
Claim: HarmonyOS can distribute tasks across devices based on capabilities
Source: Kitemetric Blog
URL: https://kitemetric.com/blogs/journey-of-harmonyos-next-a-deep-dive-into-harmonyos-part-1
Date: Unknown
Excerpt: "Distributed Task Scheduling: This unified distributed service management mechanism supports remote application operations (startup, call, connection, and migration). It intelligently chooses the most suitable device based on capabilities, location, status, resources, and user habits."
Confidence: High

### Evidence 6: Maleoon GPU Compute Support
Claim: Maleoon 910 supports OpenCL 2.0 and Vulkan 1.0 with compute shaders
Source: Huawei Developer Best Practices
URL: https://developer.huawei.com/consumer/en/doc/best-practices/bpta-maleoon-gpu-best-practices
Date: 2026-05-27
Excerpt: "The Maleoon GPU supports the subgroup feature of Vulkan and OpenCL. Workgroup size of 32/64 runs more efficiently. Use cl_arm_import_memory_dma_buf extensions."
Confidence: High

### Evidence 7: CANN AI Framework
Claim: CANN provides unified AI inference on HarmonyOS devices with NPU coordination
Source: Huawei Developer Documentation
URL: https://developer.huawei.com/consumer/en/doc/hiai-guides/introduction-0000001051486804
Date: 2026-02-06
Excerpt: "CANN efficiently optimizes on-device intelligent computing performance by coordinating the NPU, CPU, and other hardware resources. While enhancing computing efficiency, it minimizes memory and power consumption."
Confidence: High

### Evidence 8: Kirin 9000S Thermal Throttling
Claim: Kirin 9000S throttles earlier than 4nm competitors under sustained load
Source: Alibaba Electronics
URL: https://electronics.alibaba.com/question/hisilicon-kirin-explained-performance,-ai,-us-restrictions
Date: 2026-04-30
Excerpt: "Where Kirin lags: sustained GPU workloads (e.g., 30+ minute Genshin Impact sessions cause frame drops at max settings)... thermal throttling occurs earlier during sustained loads."
Confidence: High

### Evidence 9: OpenHarmony POSIX Compatibility
Claim: OpenHarmony provides 1200+ POSIX APIs and Linux kernel support
Source: OpenHarmony Docs (LiteOS-A Overview)
URL: https://gitee.com/openharmony/docs/blob/master/en/device-dev/kernel/kernel-small-overview.md
Date: 2020-09-09
Excerpt: "The kernel supports more than 1200 standard POSIX APIs... LiteOS-A has virtual memory, system calling, multi-core, lightweight IPC, and DAC mechanisms."
Confidence: High

### Evidence 10: Zig Support for HarmonyOS
Claim: Zig has early-stage support for building HarmonyOS native modules
Source: Harmony OS Developers Blog
URL: https://www.harmony-developers.com/p/openharmony-zig-addon
Date: 2025-03-22
Excerpt: "This project can help us to build a native module library for OpenHarmony/HarmonyNext with zig-lang. Note: This project is still in the early stage of development."
Confidence: Medium

### Evidence 11: Rust Cross-Compilation
Claim: Rust can compile to HarmonyOS via aarch64-unknown-linux-ohos target
Source: IT营 Community
URL: https://bbs.itying.com/topic/675133df3f816201296481a6
Date: 2025
Excerpt: "rustup target add aarch64-unknown-linux-ohos; cargo build --target aarch64-unknown-linux-ohos --release"
Confidence: Medium

### Evidence 12: HarmonyOS NEXT Performance Improvements
Claim: HarmonyOS NEXT provides 30% performance boost and 18% better battery life
Source: HarmonyOS Architecture Article
URL: https://www.harmony-developers.com/p/harmonyos-rebirth-a-path-to-innovation
Date: 2025-02-23
Excerpt: "Test data shows that with the same hardware configuration, the battery life of HarmonyOS NEXT is about 18% faster than that of traditional systems, and the survival rate of background applications is increased by 23%."
Confidence: Medium

### Evidence 13: App Memory OOM Issue
Claim: HarmonyOS imposes strict per-app memory limits
Source: Stack Overflow
URL: https://stackoverflow.com/questions/68939452/huawei-matepad-pro-runs-8g-memory-and-the-system-only-runs-one-app-the-app-cra
Date: 2021-08-27
Excerpt: "HUAWEI MatePad Pro runs 8G memory, and the system only runs one App. The App crashes when running less than 1G... Could not allocate memory: System out of memory!"
Confidence: High

### Evidence 14: MatePad Pro Wi-Fi 6 Support
Claim: MatePad Pro 13.2 supports Wi-Fi 6 (802.11ax) dual-band
Source: GSMArena
URL: https://www.gsmarena.com/huawei_matepad_pro_13_2-12586.php
Date: 2025-07-16
Excerpt: "WLAN: Wi-Fi 802.11 a/b/g/n/ac/6, dual-band. Bluetooth: 5.2, A2DP, LE."
Confidence: High

---

## Cluster Compute Suitability Assessment

### Strengths for Compute Node Use
1. **Distributed architecture** is uniquely suited for cluster computing — devices can pool resources automatically
2. **Da Vinci NPU** provides dedicated AI inference hardware competitive with Qualcomm Hexagon
3. **C/C++ NDK** with OpenCL/Vulkan compute shader support enables custom compute kernels
4. **12-16GB RAM** on MatePad Pro is substantial for mobile devices
5. **10,100 mAh battery** with 100W fast charging enables extended compute sessions
6. **Socket networking** fully supported for distributed communication
7. **OpenHarmony** provides open-source foundation for custom firmware

### Limitations for Compute Node Use
1. **Kirin 9000S CPU** is ~2x slower than Snapdragon 8 Gen 3 in both single and multi-core
2. **Maleoon 910 GPU** significantly slower than Adreno 750; Vulkan 1.0 only (not 1.3)
3. **Aggressive background task management** may limit long-running compute without continuous task declaration
4. **Thermal throttling** under sustained loads reduces performance over time
5. **App memory limits** may restrict large-model inference or big-data processing
6. **No Wi-Fi 7** limits inter-node bandwidth vs modern competitors
7. **HarmonyOS NEXT** breaks Android compatibility — apps must be rebuilt natively
8. **Limited global app ecosystem** — mostly China-focused

### Verdict
HarmonyOS devices are **viable but suboptimal** compute nodes. The unique distributed architecture aligns well with cluster concepts, and the Da Vinci NPU is a strength for AI workloads. However, the Kirin 9000S's performance deficit vs Snapdragon 8 Gen 3 (2x slower), limited GPU compute features, and aggressive power management make them less attractive than Qualcomm-based alternatives for raw compute. Best suited for: AI inference workloads leveraging the NPU, Chinese-market deployments, and experiments with novel distributed computing paradigms using the Super Device architecture.
