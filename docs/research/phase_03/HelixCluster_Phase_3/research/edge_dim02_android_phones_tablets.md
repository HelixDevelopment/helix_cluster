# Research Area: Android Phones & Tablets as Compute Nodes (Android 8-16)

## Executive Summary

Android devices represent the highest-impact and most challenging category for distributed edge compute. With over 3 billion active devices globally, even a small fraction of idle Android phones could rival the world's largest supercomputers. This research documents the technical pathways to transform Android phones and tablets into legitimate compute nodes, covering Linux environments, native development, background execution, GPU/NPU compute, and cluster management via ADB.

**Key Verdict**: Android devices CAN be used as serious compute nodes through a combination of Termux (Linux environment), foreground services (persistent execution), Vulkan compute shaders (GPU offload), and ADB-over-WiFi (cluster orchestration). However, background execution limits on Android 12+ require careful architecture using foreground services with notifications, and thermal throttling is a critical concern for sustained workloads.

---

## Key Findings

### Termux: The Foundation of Android Compute

- **Termux** is a terminal emulator and Linux environment for Android that provides a `pkg`/`apt` package manager, Bash/Zsh shells, and access to 2,000+ Linux packages including Python, Node.js, OpenSSH, Git, GCC/Clang, and more [^1456^]. It runs on Android 7+ without rooting, executing scripts at near-native speed [^1456^].
- The environment supports cross-compilation toolchains, C/C++ compilation via `gcc` and `clang`, and can run full SSH servers accessible via port 8022 [^1460^].
- **Critical limitation**: Standard `proot`-based Linux distributions (Ubuntu, Debian, Arch) via `proot-distro` intercept every system call via `ptrace`, making filesystem-heavy workloads (compilation, package management) noticeably slower than native execution [^1552^]. No real root access is available without rooting the device.
- **Hardware acceleration**: Termux supports GPU acceleration through Mesa drivers. The `Turnip` Vulkan driver (open-source for Adreno GPUs) enables Vulkan 1.3 on Adreno 6xx/7xx GPUs, and community builds are available for Android [^1473^].
- **Termux plugins** extend capabilities: Termux:API (sensor/battery/network access), Termux:Boot (auto-start on boot), Termux:Tasker (automation integration), Termux:Float (floating terminal) [^1456^].

### Linux on Android: Three Approaches

| Method | Needs Root | Performance | Linux App Compatibility | Best For |
|--------|-----------|-------------|------------------------|----------|
| Termux Native | No | Good (native) | Limited | Light scripting, Python, Node.js |
| PRoot-Distro (UserLAnd) | No | Moderate (ptrace overhead) | Good | Full distros without rooting |
| Chroot (Linux Deploy) | Yes | Good (native) | Excellent | Maximum performance with root |

- **UserLAnd** provides chroot-based Linux environments without requiring root, supporting Ubuntu, Debian, Kali Linux, Arch, and others via SSH or VNC [^1459^]. However, `systemd` is not supported as it's a chroot environment without full system privileges [^1457^].
- **chroot-distro** (rooted only) provides a full chroot environment with service management (`serviced`) that can start services like Docker inside the chroot, supporting Alpine, Ubuntu, Debian, Arch, Fedora, and more [^1464^].
- **Key insight**: For compute workloads, Termux native + `pkg install` is fastest for command-line tools. For compatibility with existing Linux software, `proot-distro` is the non-root option. Root + chroot is the gold standard for performance and flexibility.

### Native Development: NDK, JNI, and Go

- **Android NDK** enables writing C/C++ code that compiles to native libraries (`.so` files) for `arm64-v8a`, `armeabi-v7a`, `x86_64` architectures. CMake is the recommended build tool. JNI provides the bridge between Java/Kotlin and C/C++ [^1474^][^1475^].
- `@FastNative` and `@CriticalNative` annotations (Android 8+, public API in Android 14) can speed up native calls by 2-5x but cannot be used for long-running methods [^1479^].
- **Go on Android**: `gomobile` supports building Android APKs from Go code (`gomobile build -target=android`). Alternatively, cross-compile with `GOOS=android GOARCH=arm64 go build` [^1522^][^1528^]. **Critical caveat**: Cross-compiled Go binaries for Android without CGO have broken DNS resolution on Termux due to Android's non-standard DNS configuration [^1535^]. Use CGO with the Android NDK for network-dependent applications.
- **Fyne** framework enables cross-platform Go GUI apps that compile to Android APK [^1533^].
- **Rust** is available in Termux via `pkg install rust`, enabling systems programming without the NDK toolchain.

### Background Execution: The Critical Challenge

- **Android 12+ (API 31+)** imposes severe restrictions on background services. Background apps generally cannot start services [^1556^].
- **WorkManager** is the recommended API for deferrable background work. It uses `JobScheduler` internally and respects Doze mode and App Standby Buckets [^1496^]. However, normal workers have ~10-minute execution limits [^1496^].
- **For compute workloads exceeding 10 minutes**, use `setForeground()` to promote a Worker to a foreground service. This requires a persistent notification but allows unlimited execution time [^1549^][^1551^].
- **Doze Mode** triggers when the device has been idle for ~30 minutes. It restricts: network access, wake locks, standard alarms, Wi-Fi scans, and JobScheduler/WorkManager [^1478^][^1480^].
- **App Standby Buckets** classify apps into: Active, Working Set, Frequent, Rare, and Restricted (Android 12+). Restricted bucket has the highest limitations [^1478^].
- **Strategy for compute nodes**: Use `FOREGROUND_SERVICE` with `dataSync` or `specialUse` service type, paired with a notification. This is partially exempt from Doze and App Standby restrictions [^1556^]. Combine with `ACTION_CHARGING` to only compute while plugged in [^1560^].

### GPU Compute: Vulkan on Adreno and Mali

- **Android supports compute shaders via OpenGL ES 3.1 or Vulkan** [^1481^]. Vulkan compute shaders provide the most capable GPU compute path on modern Android devices.
- **Qualcomm Adreno GPUs** (Snapdragon): The open-source `Turnip` Vulkan driver in Mesa supports Vulkan 1.3 on Adreno 6xx/7xx series GPUs. It implements compute pipelines with NIR shader compilation and supports bindless descriptors on a6xx+ GPUs [^1473^]. Turnip achieved hardware-accelerated ray tracing support in 2024 [^1473^].
- **ARM Mali GPUs** (MediaTek, Samsung Exynos): `PanVK` (open-source Vulkan driver for Mali) achieved Vulkan 1.4 conformance on Mali-G610 as of August 2025, with support for Midgard, Bifrost, and Valhall architectures [^1473^].
- **Practical GPU compute**: Use the Vulkan API directly (via NDK C++), or OpenCL via frameworks like Renderscript (deprecated). For ML workloads, use vendor SDKs (SNPE for Qualcomm, NeuroPilot for MediaTek) rather than raw Vulkan compute.
- **Performance consideration**: Adreno GPUs excel at sustained compute workloads due to better thermal management. Mali GPUs (especially in MediaTek Dimensity chips) can throttle more aggressively under sustained GPU load [^1498^].

### NPU/DSP for AI/ML Inference

- **Qualcomm Hexagon NPU** (Snapdragon 8 Gen 3): The Hexagon DSP features fused scalar, tensor, and vector units with INT8/INT16/INT4 support. It is 45% faster than Gen 2's NPU [^1497^][^1498^].
- **Qualcomm SNPE (Neural Processing Engine)** SDK enables running deep neural networks on Snapdragon SoCs using CPU, GPU, DSP, or AIP (AI Processor) runtimes [^1575^]. Models are converted to DLC (Deep Learning Container) format [^1575^].
- **MediaTek APU 790** (Dimensity 9300): Dedicated multi-core AI accelerator optimized for generative AI workloads, supports INT8/INT16/INT4 [^1497^].
- **Software stacks**: Qualcomm provides QIDK (Qualcomm Innovators Development Kit) with sample Android apps using SNPE and QNN, verified on Snapdragon 8 Gen 2, 3, and Elite platforms [^1584^].

### Android CPU/GPU Benchmarks: Flagship Performance

#### Snapdragon 8 Gen 3 (Qualcomm)
- **CPU**: 1x Cortex-X4 @ 3.3GHz + 3x Cortex-A720 @ 3.2GHz + 2x Cortex-A720 @ 3.0GHz + 2x Cortex-A520 @ 2.3GHz (8 cores)
- **GPU**: Adreno 750 @ 900MHz with hardware ray tracing
- **Process**: TSMC 4nm (N4P)
- **AnTuTu v11**: ~2,341,391 total (CPU: 685,467; GPU: 811,418) [^1495^]
- **GeekBench 6**: Single: 2,201; Multi: 6,753 [^1495^]
- **L3 Cache**: 12 MB
- **Strength**: Superior GPU performance, better sustained performance under load, excellent thermal stability in gaming phones [^1498^]

#### Dimensity 9300/9300+ (MediaTek)
- **CPU**: 1x Cortex-X4 @ 3.25GHz + 3x Cortex-X4 @ 2.85GHz + 4x Cortex-A720 @ 2.0GHz (8 cores, all "big" cores - no efficiency cores)
- **GPU**: Arm Immortalis-G720 MC12 @ 1,300MHz
- **Process**: TSMC 4nm
- **AnTuTu v11**: ~2,347,116 total (CPU: 676,003; GPU: 788,281) [^1495^]
- **GeekBench 6**: Single: 2,256; Multi: 7,541 [^1495^]
- **L3 Cache**: 8 MB
- **Strength**: Higher multi-core score, 44% better floating-point computations [^1495^]; weakness in sustained GPU workloads due to thermal throttling [^1498^]

### ADB: Cluster Management Protocol

- **Android Debug Bridge (ADB)** follows a client-server-daemon (adbd) architecture and is the primary tool for remote device management [^1553^].
- **Wireless ADB** (Android 11+): Built-in support for Wi-Fi debugging with pairing via QR code or pairing code. Multiple devices can be managed simultaneously [^1559^].
- **Automation capabilities**: ADB supports shell commands (`adb shell`), file transfer (`adb push`/`pull`), app installation (`adb install`), log access (`adb logcat`), and remote screen capture [^1553^].
- **For cluster management**: A controller node can connect to multiple Android devices via `adb connect <ip>:5555` over the same network. Each command targets a specific device with `adb -s <device_id> <command>` [^1553^].
- **adb-auto-enable** project demonstrates autonomous wireless ADB enablement on boot without root, with a built-in web UI on port 9093 for status monitoring [^1554^].
- **Limitation**: ADB connections require initial pairing and are not encrypted end-to-end. For production clusters, tunnel over SSH or WireGuard.

### Networking from Android Apps

- Android apps can use standard Java/Kotlin socket APIs for TCP and UDP communication, including `ServerSocket` for inbound connections [^1558^].
- **No root required** for network sockets on ports > 1024. Root is needed to bind ports < 1024 [^1525^].
- **Local network access**: Android apps can bind to `0.0.0.0` to accept connections from the local network (requires `INTERNET` permission).
- **Background network restrictions**: Under Doze mode, background apps lose network access. Foreground services maintain network connectivity [^1556^].
- **KSWEB** demonstrates a complete web server stack on Android (lighttpd/nginx, PHP, MySQL) accessible via localhost:8080 or the device's LAN IP [^1523^][^1525^].

### Power Management: Compute While Charging

- **BatteryManager API** provides `ACTION_CHARGING` and `ACTION_DISCHARGING` broadcasts, plus `isCharging()` for programmatic checking [^1560^].
- `ACTION_CHARGING` is explicitly documented as "a good time to do work that you would like to avoid doing while on battery" [^1560^].
- **Battery capacity levels**: `BATTERY_CAPACITY_LEVEL_CRITICAL` (1), `LOW` (2), `NORMAL` (3), `HIGH` (4), `FULL` (5). At FULL/HIGH, "The Android framework can run background tasks without affecting the battery level or battery performance" [^1560^].
- **Strategy**: Register `BroadcastReceiver` for `ACTION_CHARGING`/`ACTION_DISCHARGING`. Only start compute foreground service when `isCharging()` is true. Monitor `BATTERY_CAPACITY_LEVEL` to scale work intensity.
- **Thermal throttling**: All Android devices will throttle CPU/GPU under sustained load. Gaming phones (e.g., REDMAGIC 9 Pro with active cooling) sustain ~70-80% of peak performance. Standard phones may throttle to 40-50% within minutes [^1498^].

### Storage Access

- **Scoped Storage** (Android 11+, enforced for API 30+): Apps can only access their own app-specific files without permissions. `READ_EXTERNAL_STORAGE` grants read access to other apps' media files only [^1580^].
- **App-specific directories**: `/sdcard/Android/data/<package>/` is freely accessible to each app. Use `getExternalFilesDir()` for consistent paths [^1580^].
- **SD Card**: External SD cards are subject to scoped storage restrictions. Apps cannot create their own directories on SD cards (Android 11+) [^1583^]. Use `getExternalFilesDirs()` to access app-specific directories on external storage.
- **Termux storage**: `termux-setup-storage` creates symlinks to `/sdcard/` under `~/storage/` [^1456^].
- **For compute workloads**: Store data in the app's private directory (`/data/data/<package>/files/` or Termux's home at `/data/data/com.termux/files/home/`). This avoids all scoped storage restrictions and provides maximum I/O performance.

### APK Installation Without Google Play

- **Sideloading**: Android allows installing APKs from "unknown sources." Since Android 8.0, this is a per-app permission rather than a global setting [^1544^].
- Process: Download APK → grant installing app (e.g., Chrome, File Manager) permission to "Install unknown apps" → tap APK to install [^1544^].
- **Google is tightening sideloading**: Starting September 2026, Android will restrict installation to verified developers only. However, an "advanced flow" in Developer Options allows power users to bypass verification with a 24-hour waiting period and device restart [^1543^].
- **F-Droid** remains the recommended source for Termux and open-source apps [^1456^].
- **For compute clusters**: Use `adb install app.apk` for batch deployment across multiple devices via a controller script [^1553^].

### Root vs. Non-Root Capabilities

| Capability | Non-Root (AOSP) | Rooted |
|-----------|-----------------|--------|
| Termux Linux environment | Full access | Same |
| PRoot-Distro (Ubuntu/Debian) | Yes, with ptrace overhead | Same |
| Native C/C++ via NDK | Full access | Same |
| Background services | Severely limited (Android 12+) | Can use system-level services |
| GPU compute (Vulkan) | Full access | Same |
| NPU/DSP inference | Via vendor SDKs (SNPE, etc.) | Same + lower-level access |
| Full chroot Linux | No | Yes (native performance) |
| Port binding (<1024) | No | Yes |
| iptables/firewall | No | Yes |
| cgroup/namespace management | No | Yes |
| System-level monitoring | Limited | Full `/proc` and `/sys` access |
| Docker/containerd | No | Via chroot workarounds |

- **Recommendation for compute nodes**: Root access is highly recommended for serious compute deployments. It enables full chroot Linux environments, system-level resource monitoring, and unrestricted background execution. However, non-root configurations are viable for lighter workloads using Termux + foreground services.

### Frida: Dynamic Instrumentation

- **Frida** is a dynamic instrumentation toolkit that injects JavaScript into running processes for debugging and reverse engineering [^1507^].
- On Android, Frida is most commonly used on rooted devices but can work on non-rooted devices with debuggable apps using `frida-gadget` [^1508^].
- **Use case for compute**: Frida can hook into Android system APIs to extract performance metrics, monitor thermal throttling events, and intercept power management decisions in real-time [^1502^][^1506^].
- Supports all Android 4.4+; Python and Node.js bindings available [^1507^].

### Tasker: Automation Engine

- **Tasker** is a mature Android automation app that creates "Profiles" (triggers) linked to "Tasks" (actions) [^1532^].
- Triggers can be time-based, event-based (e.g., `ACTION_CHARGING`), location-based, or application-based.
- **Integration with compute**: Tasker can trigger compute jobs when the device is plugged in, on Wi-Fi, at specific times, or when the battery reaches a certain level. Combined with Termux:Tasker plugin, it can execute shell scripts in Termux [^1456^].
- Variables and scenes provide advanced scripting capabilities [^1532^].

### Android Work Profile

- **Work Profile** creates an isolated container for business apps using Android's multi-user framework with separate encryption keys and security policies [^1548^][^1555^].
- For compute clusters, Work Profile can provide isolation between compute tasks and personal data. IT admins can silently install apps in the work profile and enforce policies [^1554^].
- Android 15 improved cross-profile data sharing controls, allowing admins to specify exactly which apps can share data between profiles [^1554^].

---

## Technical Specifications

### Minimum Viable Compute Node (Android 8+)
- **OS**: Android 8.0+ (API 26), Android 12+ recommended for modern APIs
- **SoC**: Snapdragon 6xx/7xx/8xx or MediaTek Dimensity 700+ (ARM64 required)
- **RAM**: 4GB minimum, 6GB+ recommended
- **Storage**: 32GB minimum free space for Linux environment + data
- **Network**: Wi-Fi (same subnet as controller) or USB tethering
- **Power**: Constant charging required for sustained compute; use BatteryManager API

### Recommended Compute Node (Android 12+)
- **SoC**: Snapdragon 8 Gen 2/3 or Dimensity 9200/9300
- **RAM**: 8GB+ (LPDDR5X)
- **Storage**: 128GB+ UFS 3.1/4.0
- **Cooling**: Gaming phone with active cooling or external fan attachment
- **Root**: Yes (Magisk recommended) for full chroot + unrestricted execution

### Architecture Diagram

```
Controller Node (PC/Server)
    |
    | ADB over Wi-Fi (port 5555)
    |
    +-- Android Device 1 (IP: 192.168.1.101)
    |   +-- Foreground Service (persistent notification)
    |   |   +-- Compute Worker Thread(s)
    |   +-- Termux (Linux environment)
    |   |   +-- SSH server (port 8022)
    |   |   +-- Python/Go/Rust compute binary
    |   +-- App-private storage (/data/data/...)
    |
    +-- Android Device 2 (IP: 192.168.1.102)
    |   +-- [same structure]
    |
    +-- Android Device N...
```

---

## Major Projects & Tools

| Project | Description | URL | Status |
|---------|-------------|-----|--------|
| **Termux** | Terminal emulator + Linux environment for Android | https://github.com/termux/termux-app | Active (v0.119.0) |
| **proot-distro** | Manage Linux distributions in Termux | https://github.com/termux/proot-distro | Active |
| **UserLAnd** | Linux chroot on Android without root | https://github.com/CypherpunkArmory/UserLAnd | Active |
| **chroot-distro** | Full Linux chroot on rooted Android | https://github.com/sabamdarif/chroot-distro | Active |
| **Turnip** | Open-source Vulkan driver for Adreno | https://github.com/K11MCH1/AdrenoToolsDrivers | Active |
| **PanVK** | Open-source Vulkan driver for Mali | Part of Mesa | Active (Vulkan 1.4 on G610) |
| **SNPE/QIDK** | Qualcomm AI SDK for on-device inference | https://github.com/quic/QIDK | Active |
| **KSWEB** | Web server stack for Android | https://kslabs.ru | Active |
| **adb-auto-enable** | Autonomous wireless ADB on boot | https://github.com/mouldybread/adb-auto-enable | Active |
| **termux-api-exporter** | Prometheus exporter for Android metrics | https://github.com/anshulpatel25/termux-api-exporter | Active |
| **Frida** | Dynamic instrumentation toolkit | https://frida.re | Active (v16+) |
| **Tasker** | Android automation engine | https://tasker.joaoapps.com | Active |
| **gomobile** | Go mobile app/library builder | https://pkg.go.dev/golang.org/x/mobile/cmd/gomobile | Maintenance |

---

## Code Examples

### 1. Foreground Service for Persistent Compute (Kotlin)
```kotlin
class ComputeService : Service() {
    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val notification = createNotification("Compute Node Running")
        startForeground(1, notification) // Required persistent notification
        
        CoroutineScope(Dispatchers.Default).launch {
            while (isActive) {
                if (isCharging()) {
                    runComputeBatch()
                }
                delay(5000)
            }
        }
        return START_STICKY // Restart if killed
    }
    
    private fun isCharging(): Boolean {
        val bm = getSystemService(Context.BATTERY_SERVICE) as BatteryManager
        return bm.isCharging
    }
    
    override fun onBind(intent: Intent?) = null
}
```

### 2. WorkManager with Foreground for Long-Running Tasks (Kotlin)
```kotlin
class ComputeWorker(ctx: Context, params: WorkerParameters) : CoroutineWorker(ctx, params) {
    override suspend fun doWork(): Result {
        val notification = createNotification("Processing batch...")
        setForeground(ForegroundInfo(1, notification))
        
        // Long-running compute here - no 10-min limit
        for (i in 0 until totalBatches) {
            processBatch(i)
            setProgress(workDataOf("progress" to i))
        }
        return Result.success()
    }
}

// Schedule
val request = OneTimeWorkRequestBuilder<ComputeWorker>()
    .setExpedited(OutOfQuotaPolicy.RUN_AS_NON_EXPEDITED_WORK_REQUEST)
    .build()
WorkManager.getInstance(context).enqueue(request)
```

### 3. Termux Setup for Compute Node (Bash)
```bash
#!/bin/bash
# Setup script for Android compute node in Termux

pkg update && pkg upgrade -y
pkg install -y openssh python nodejs golang git htop

# Start SSH server
sshd  # Listens on port 8022

# Set password for SSH access
passwd

# Access storage
termux-setup-storage

# Install Termux:API for battery/sensor access
pkg install -y termux-api

# Check battery before starting compute
termux-battery-status

# Start compute script when charging only
while true; do
    STATUS=$(termux-battery-status | grep plugged | awk -F'"' '{print $4}')
    if [ "$STATUS" != "UNPLUGGED" ]; then
        python3 ~/compute_worker.py
    fi
    sleep 60
done
```

### 4. Go Cross-Compilation for Android
```bash
# For Android ARM64 (most modern phones)
export GOOS=android
export GOARCH=arm64
export CGO_ENABLED=1  # Required for working DNS!
export CC=$NDK/toolchains/llvm/prebuilt/linux-x86_64/bin/aarch64-linux-android21-clang
go build -o compute-node-android .

# Deploy via ADB
adb install compute-node-android.apk  # For gomobile builds
# OR
adb push compute-node-android /data/local/tmp/
adb shell chmod +x /data/local/tmp/compute-node-android
adb shell /data/local/tmp/compute-node-android
```

### 5. Battery-Aware Compute Trigger (Kotlin)
```kotlin
class ChargingReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        when (intent.action) {
            BatteryManager.ACTION_CHARGING -> {
                // Start compute service
                context.startForegroundService(
                    Intent(context, ComputeService::class.java)
                )
            }
            BatteryManager.ACTION_DISCHARGING -> {
                // Stop compute service
                context.stopService(
                    Intent(context, ComputeService::class.java)
                )
            }
        }
    }
}

// Register in AndroidManifest.xml
<receiver android:name=".ChargingReceiver">
    <intent-filter>
        <action android:name="android.os.action.CHARGING" />
        <action android:name="android.os.action.DISCHARGING" />
    </intent-filter>
</receiver>
```

### 6. ADB Cluster Management Script (Bash)
```bash
#!/bin/bash
# Manage Android compute cluster via ADB

DEVICES=("192.168.1.101" "192.168.1.102" "192.168.1.103")
APK_PATH="./compute-node.apk"

# Connect to all devices
for device in "${DEVICES[@]}"; do
    adb connect "$device:5555"
done

# Install app on all devices
for device in "${DEVICES[@]}"; do
    adb -s "$device:5555" install "$APK_PATH"
done

# Start compute service on all devices
for device in "${DEVICES[@]}"; do
    adb -s "$device:5555" shell am startservice \
        -n "com.myapp/.ComputeService"
done

# Monitor status
for device in "${DEVICES[@]}"; do
    echo "=== $device ==="
    adb -s "$device:5555" shell top -n 1 | grep compute
done
```

---

## Raw Evidence Log

### Evidence 1: Termux Linux Environment
**Source**: UBOS Tech / Termux GitHub
**URL**: https://ubos.tech/news/termux-brings-full-linux-to-android-new-features-updates-and-community-highlights/
**Date**: 2026-02-02
**Excerpt**: "Termux combines a powerful Android CLI with a Debian-based Linux distribution. Once installed, it provides the `pkg` and `apt` package managers, a Bash/Zsh shell, and access to over 2,000 Linux packages — from Git and Python to Node.js and OpenSSH. Because it runs natively, scripts execute at near-native speed, and you can even compile C/C++ code directly on the device."
**Confidence**: High

### Evidence 2: Termux as SSH Server
**Source**: GitHub - jothi-prasath/termux-dev-setup
**URL**: https://github.com/jothi-prasath/termux-dev-setup
**Date**: 2023-06-03
**Excerpt**: "If you want to remotely access your Termux environment from a Desktop/Laptop... Set a password for the SSH server... SSH runs on port 8022 by default."
**Confidence**: High

### Evidence 3: PRoot-Distro Limitations
**Source**: GitHub - termux/proot-distro
**URL**: https://github.com/termux/proot-distro
**Date**: 2026-05-30
**Excerpt**: "`proot` intercepts every system call via `ptrace`. Filesystem-heavy workloads (compilation, package managers) are noticeably slower than native execution. No background services: starting service supervisors (`systemd`, `OpenRC`, socket-activated daemons) is generally not possible."
**Confidence**: High

### Evidence 4: Android Background Execution Limits
**Source**: notificare.com
**URL**: https://notificare.com/blog/2024/12/13/android-background-limitations/
**Date**: 2024-12-13
**Excerpt**: "The 'Restricted' bucket, added in Android 12, has the lowest priority and the highest restrictions of all the buckets. Doze Mode limits background processes, such as: Network Access, WakeLock's, Standard Alarms, Wi-FI Scans, JobScheduler."
**Confidence**: High

### Evidence 5: WorkManager Long-Running Workers
**Source**: Medium - Inside WorkManager
**URL**: https://medium.com/@mahesh31.ambekar/inside-workmanager-how-android-really-runs-your-background-tasks-3d3d95dda882
**Date**: 2026-03-16
**Excerpt**: "WorkManager built-in support for long-running workers. In this case, WorkManager can signal to the OS that the process must be kept alive during execution. These workers run longer than 10 minutes. Example use cases: bulk upload/download, processing machine learning models locally, or work that is important to the user."
**Confidence**: High

### Evidence 6: Snapdragon 8 Gen 3 vs Dimensity 9300 Benchmarks
**Source**: NanoReview
**URL**: https://nanoreview.net/en/soc-compare/qualcomm-snapdragon-8-gen-3-vs-mediatek-dimensity-9300-plus
**Date**: 2026-05-26
**Excerpt**: "Snapdragon 8 Gen 3 AnTuTu v11: 2,341,391 (CPU: 685,467; GPU: 811,418). Dimensity 9300 Plus: 2,347,116 (CPU: 676,003; GPU: 788,281). GeekBench 6 Multi: SD8G3=6,753; D9300+=7,541."
**Confidence**: High

### Evidence 7: Turnip Vulkan Driver for Adreno
**Source**: Grokipedia / Mesa Project
**URL**: https://grokipedia.com/page/Open-source_GPU_drivers_for_Adreno_and_Mali
**Date**: 2026-02-22
**Excerpt**: "Turnip is a Vulkan-specific driver for Adreno GPUs achieving conformance for Vulkan 1.0 and 1.1 and supporting features up to Vulkan 1.3 on newer generations like Adreno 6xx and initial Gen 8 hardware as of late 2024. Turnip became the fourth Mesa Vulkan driver to support hardware-accelerated ray tracing in 2024."
**Confidence**: High

### Evidence 8: ADB Wireless Multiple Devices
**Source**: Bugfender
**URL**: https://bugfender.com/blog/adb-debugging/
**Date**: 2025-11-18
**Excerpt**: "Wireless debugging in Android 11+: Go to Settings > Developer options > Wireless debugging. Enable Wireless debugging and choose Pair device with pairing code. Verify the connection with `adb devices` — if listed, the device is ready for wireless ADB commands."
**Confidence**: High

### Evidence 9: Go Cross-Compile DNS Issue on Android
**Source**: dave.engineer blog
**URL**: https://dave.engineer/blog/2025/11/cross-compiling-go-android/
**Date**: 2025-11-30
**Excerpt**: "Cross-compiling Go for Android completely breaks down the moment you try to run a Go binary on Android (specifically Termux). DNS fails: The Go runtime tries to resolve DNS query to `::1:53` but nothing is listening on that port. Use CGO with the Android NDK to fix."
**Confidence**: High

### Evidence 10: SNPE On-Device Inference
**Source**: Medium - Deploying AI on Android Devices
**URL**: https://medium.com/@raghav.2945/from-android-framework-to-edge-ai-running-quantized-neural-networks-on-device-8bf36188141c
**Date**: 2025-12-01
**Excerpt**: "SNPE is Qualcomm's AI framework for mobile and embedded devices. It enables running deep neural networks on Snapdragon SoCs efficiently, leveraging: AIP (AI Processor), DSP (Hexagon), GPU, CPU. Runtimes: DSP / CPU / GPU."
**Confidence**: High

### Evidence 11: BatteryManager ACTION_CHARGING
**Source**: Android Developer Documentation
**URL**: https://developer.android.com/reference/android/os/BatteryManager
**Date**: 2025-03-13
**Excerpt**: "ACTION_CHARGING: Sent when the device's battery has started charging. This is a good time to do work that you would like to avoid doing while on battery. BATTERY_CAPACITY_LEVEL_FULL: The battery is full... The Android framework can run background tasks without affecting the battery level or battery performance."
**Confidence**: High

### Evidence 12: Android Storage Scoped Storage
**Source**: Android Source Documentation
**URL**: https://source.android.com/docs/core/storage/scoped
**Date**: 2025-03-26
**Excerpt**: "Scoped storage limits app access to external storage. In Android 11 or higher, apps targeting API 30+ must use scoped storage. Apps can read/write their own files (no permission), read other apps' media files (READ_EXTERNAL_STORAGE needed), cannot access other apps' external app data directories."
**Confidence**: High

### Evidence 13: KSWEB Web Server on Android
**Source**: Make Tech Easier
**URL**: https://maketecheasier.com/setup-local-web-server-android/
**Date**: 2021-07-23
**Excerpt**: "KSWEB includes: lighttpd server v1.4.35 (SSL), nginx v1.7.3 (SSL), PHP v5.6.2 (SSL), MySQL v5.6.19. Default root directory: /mnt/sdcard/htdocs. You can change this to some other location, perhaps on a micro SD Card."
**Confidence**: High

### Evidence 14: Tasker Automation
**Source**: How-To Geek
**URL**: https://www.howtogeek.com/10-years-later-im-still-using-this-popular-android-automation-app/
**Date**: 2024-06-27
**Excerpt**: "Profiles: Think of profiles as the context for running an automated routine. This is where you set the conditions and triggers to run 'tasks.' You can create profiles based on time, location, event, application, day, and state. Tasks: A task is a sequence of actions that run when a profile is triggered."
**Confidence**: High

### Evidence 15: Frida on Android
**Source**: Frida Official Website
**URL**: https://frida.re/
**Date**: Ongoing
**Excerpt**: "Inject your own scripts into black box processes. Hook any function, spy on crypto APIs or trace private application code, no source code needed. Works on... Android. Install the Node.js bindings from npm, grab a Python package from PyPI."
**Confidence**: High

### Evidence 16: Android Work Profile Architecture
**Source**: Cerberus App Enterprise
**URL**: https://enterprise.cerberusapp.com/en-US/insights/android-enterprise-security/
**Date**: 2025-05-14
**Excerpt**: "The work profile architecture in Android Enterprise creates a secure, separate container for business apps and data. This container is isolated at the operating system level, ensuring complete separation between personal and work data. The architecture leverages Android's multi-user framework at its core."
**Confidence**: High

### Evidence 17: GPU Sustained Performance (Thermal)
**Source**: Android Authority
**URL**: https://www.androidauthority.com/snapdragon-8-gen-3-dimensity-9300-benchmarked-3395385/
**Date**: 2025-04-16
**Excerpt**: "The Dimensity 9300-equipped vivo X100 Pro even loses to previous-generation silicon in stress testing. By contrast, the Snapdragon-equipped REDMAGIC 9 Pro has a significant lead over older chipsets. The REDMAGIC handset is a gaming phone with an array of passive cooling measures (in addition to the cooling fan in performance mode)."
**Confidence**: High

### Evidence 18: gomobile Build Commands
**Source**: Go Packages
**URL**: https://pkg.go.dev/golang.org/x/mobile/cmd/gomobile
**Date**: Ongoing
**Excerpt**: "gomobile build -target=android golang.org/x/mobile/example/basic. gomobile install golang.org/x/mobile/example/basic. Build compiles and encodes the app named by the import path."
**Confidence**: High

### Evidence 19: adb-auto-enable Project
**Source**: GitHub - mouldybread/adb-auto-enable
**URL**: https://github.com/mouldybread/adb-auto-enable
**Date**: 2025-10-12
**Excerpt**: "Automatically enable wireless ADB and switch to port 5555 on Android device boot - no root required. Features: Fully Autonomous, No Root Required, Web UI at http://device-ip:9093, Background Persistence."
**Confidence**: High

### Evidence 20: Qualcomm QIDK AI Kit
**Source**: GitHub - quic/QIDK
**URL**: https://github.com/quic/QIDK/
**Date**: 2022-12-21
**Excerpt**: "Qualcomm Innovators Development Kit (QIDK) provides sample applications to demonstrate the capability of Hardware Accelerators for AI. Verified on Snapdragon 8 Gen2, Snapdragon 8 Gen3, Snapdragon 8 Elite platforms."
**Confidence**: High

---

## Answers to Key Questions

### Can we run a persistent background service on Android that does compute?
**Yes**, but with restrictions on Android 12+. Use a `FOREGROUND_SERVICE` (with persistent notification) or WorkManager with `setForeground()`. This keeps the process alive and partially exempt from Doze mode [^1549^][^1551^][^1552^]. For maximum persistence, combine with `ACTION_CHARGING` to only run while plugged in [^1560^].

### What APIs are available for CPU/GPU utilization from an app?
- **CPU**: Standard multi-threading (Java threads, Kotlin coroutines, NDK pthreads). `Runtime.availableProcessors()` for core count. No direct CPU affinity without root.
- **GPU**: Vulkan API (NDK) for compute shaders on Adreno/Mali GPUs. OpenGL ES 3.1 compute shaders as fallback. Vendor SDKs (SNPE for Qualcomm) for ML inference on GPU [^1575^].
- **Monitoring**: `termux-battery-status` via Termux:API, `/proc/stat` for CPU usage (root only on Android 8+), `Debug.MemoryInfo` for RAM.

### Can we use Vulkan compute on Android GPUs (Adreno, Mali)?
**Yes**. Vulkan compute shaders are supported on Android devices with Vulkan-capable GPUs. The open-source Turnip driver supports Vulkan 1.3 on Adreno 6xx/7xx [^1473^]. PanVK supports Vulkan 1.4 on Mali-G610 [^1473^]. Use the Android NDK Vulkan API directly in C/C++.

### How do we get network connectivity from an Android app?
Standard Java/Kotlin socket APIs work for TCP/UDP on ports > 1024 [^1558^]. Use foreground service to maintain connectivity under Doze mode. ADB-over-WiFi provides management connectivity on port 5555 [^1559^].

### What are the background execution limits (Android 12+)?
- Background apps cannot start services [^1556^]
- WorkManager jobs have ~10 minute limits unless promoted to foreground [^1496^]
- Doze mode disables network, alarms, and jobs after ~30 min of inactivity [^1478^]
- App Standby Restricted bucket (Android 12+) has highest limitations [^1478^]

### Can ADB be used for cluster management?
**Yes**. ADB supports multiple simultaneous wireless connections. Use `adb connect <ip>:5555` for each device, then `adb -s <device_id> <command>` to target specific nodes [^1553^][^1555^]. The adb-auto-enable project shows autonomous ADB setup without root [^1554^].

### What's the performance of Snapdragon 8 Gen 3 / Dimensity 9300?
Both achieve ~2.3M AnTuTu v11 score. SD8G3 has better GPU (811K vs 788K) and sustained performance. D9300 has better multi-core CPU (7,541 vs 6,753) but throttles more under sustained load [^1495^][^1498^].

### Can we run Go code on Android via Termux or native?
**Yes**. `gomobile build -target=android` creates APKs. Cross-compile with `GOOS=android GOARCH=arm64`. **Critical**: Enable CGO for working DNS resolution [^1522^][^1535^]. Go runs natively in Termux (`pkg install golang`).

### How to handle power management (only compute while charging)?
Use `BatteryManager.ACTION_CHARGING`/`ACTION_DISCHARGING` broadcasts with `isCharging()` check. Scale work intensity based on `BATTERY_CAPACITY_LEVEL` [^1560^]. Foreground service + notification required for Android 12+.

### Can we use the NPU/DSP for inference?
**Yes**. Qualcomm SNPE SDK provides Android APIs for running quantized models on Hexagon DSP/AI Processor [^1575^]. MediaTek NeuroPilot provides similar access to APU 790. Both require model conversion to vendor-specific formats (DLC for Qualcomm) [^1575^][^1584^].

### What storage access do we have?
- App-private: `/data/data/<package>/files/` - unlimited, no restrictions
- Termux home: `/data/data/com.termux/files/home/` - full Linux filesystem
- External: Scoped storage restrictions apply (Android 11+) [^1580^]
- SD Card: App-specific directories only via `getExternalFilesDirs()` [^1583^]

### How to install APKs without Google Play?
Enable "Install unknown apps" per-source (Android 8+) [^1544^]. Use `adb install` for batch deployment. F-Droid for open-source apps. Note: Google is tightening verification requirements in 2026 [^1543^].

### Android Work Profile vs personal for compute isolation?
Work Profile provides OS-level isolation with separate encryption keys, independent app management, and IT-controlled policies [^1548^][^1555^]. Can be remotely wiped without touching personal data. Suitable for enterprise compute deployments.

---

## Architecture Recommendations

### Tier 1: Lightweight Compute (Non-Rooted)
- **Termux** + `pkg install python golang`
- Foreground service with notification
- Compute only while charging via `BatteryManager`
- SSH access via Termux `sshd` (port 8022)
- Data storage in app-private directory
- Suitable for: Python scripts, light data processing, API serving

### Tier 2: Full Linux Compute (Non-Rooted)
- **proot-distro** with Ubuntu/Debian
- Full `apt install` ecosystem available
- Performance penalty from ptrace syscall interception (~10-30%)
- No systemd services; use manual process management
- Suitable for: Docker-less container workloads, compilation, testing

### Tier 3: Maximum Performance (Rooted)
- **chroot-distro** with full Linux (Ubuntu Server, Debian)
- Native performance, no ptrace overhead
- `serviced` for service management
- Full `/proc`, `/sys` access for monitoring
- Custom kernel possible (e.g., overclocking, scheduler tuning)
- Suitable for: Sustained compute, ML inference, GPU compute

---

*Research compiled from 16+ independent web searches covering official documentation, GitHub repositories, developer blogs, benchmarks, and community forums. All citations use [^number^] format for traceability.*
