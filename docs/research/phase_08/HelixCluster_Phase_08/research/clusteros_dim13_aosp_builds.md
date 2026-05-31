## Facet: Android AOSP Build Acceleration via Cluster Computing

### Key Findings

- **AOSP builds are extremely resource-intensive**: A full build on a 6-core/64GB machine takes ~6 hours, while Google's 72-core/64GB machines complete builds in ~40 minutes. Incremental builds take several minutes. [^1011^]

- **AOSP build system uses a 4-layer architecture**: Android.bp files are parsed by Blueprint/Soong into Ninja manifests; Android.mk files are parsed by Kati into Ninja manifests; Ninja executes the final build graph. Soong outputs alone can be 6-10 GiB. [^1124^][^1126^]

- **Build time distribution**: ~10-15 min dependency resolution (Soong, single-threaded), 60-90 min compilation (parallelizable), 20-40 min linking/packaging (I/O-bound). [^1010^]

- **distcc provides distributed C/C++ compilation** with pump mode achieving 3x speedup over plain distcc by offloading preprocessing to remote servers. Benchmarks show 2.6x speedup with 3 machines on 100Mbps network. [^1020^][^1060^]

- **ccache direct mode achieves 145x speedup** on cache hits vs. uncached compilation. Cache miss overhead is 5-15% on Linux. For AOSP, 100GB+ cache on fast storage is recommended. [^1018^][^1010^]

- **Icecream (IceCC) offers superior scheduling** vs. distcc with a central scheduler that dynamically assigns compile jobs to fastest free servers, supporting cross-compilation and heterogeneous environments. [^1026^][^1028^]

- **sccache (backed by Mozilla) supports Rust/C++/CUDA caching** with distributed compilation capabilities, cloud storage backends (S3, GCS, Redis), and icecream-style distribution with authentication and TLS. AOSP includes sccache in toolchain/sccache. [^1116^]

- **Google's RBE (Remote Build Execution) for AOSP** uses reclient (reproxy/rewrapper/bootstrap/scandeps_server) to distribute build actions. Supports strategies: local, remote, remote_local_fallback, racing. Configuration via build/soong/docs/rbe.json. [^1058^][^1063^]

- **Buildbarn RBE cluster setup for AOSP documented**: Complete guide shows topology with storage (CAS/AC), frontend (REAPI), scheduler, browser, worker, and runner components. Uses systemd services. [^1114^]

- **Incredibuild reports 6.3x acceleration** for AOSP 16 on 32-core machine (1h46m -> 17m), and ~10x on 16-core workstation (3h18m -> 20m) using shared cache + distributed computing. [^1016^][^1021^]

- **LLD linker is 2-3x faster than GNU gold** and 5-10x faster than GNU ld, with simpler codebase (26K vs 164K LOC). Mold linker is even faster (26x vs GNU gold for Chrome). [^1082^][^1079^]

- **tmpfs/RAM disk for build directories** dramatically reduces I/O bottlenecks during compilation. ccache on RAM disk achieves sub-5ms cache hit times. [^1040^][^1044^]

- **cgroups v2 provide fine-grained resource control** for build jobs: CPU quota/weight, memory max/high, I/O bandwidth limits, PIDs max. Foundation for container-based build isolation. [^1042^][^1040^]

- **Docker enables reproducible AOSP builds**: Dockerfile-based setup with Jenkins CI includes ccache, repo tool, Python3, and all build dependencies. [^1041^]

- **Kubernetes CI/CD with Tekton + ArgoCD** provides GitOps-native build pipelines: Tekton handles CI (clone, test, build, push), ArgoCD handles CD (sync manifests to cluster). [^1037^][^1039^]

- **Gradle remote build cache** allows sharing build outputs across team. CI populates cache from clean builds; developers pull results. Bitrise reports 46.67% reduction in Android build times. [^1071^][^1059^]

- **AOSP dropped official ccache prebuilt** due to issues with non-reproducible results and limited gains at Google's scale. Users can still set USE_CCACHE and CCACHE_EXEC to a custom binary. [^1023^]

- **BuildXL (Microsoft)** handles 150,000+ builds/day internally on monorepos up to half-terabyte with half-million process executions per build, distributed to thousands of machines. [^1091^]

- **distcc pump mode limitations**: Requires identical system headers on all servers; build systems that rewrite headers during build (Linux kernel 2.6) need special handling. [^1070^]

- **Optimal -j parallelism**: For distcc, set -j to ~2x total available server CPUs. For pump mode with 40 servers, -j80 or larger may be appropriate. [^1070^]

---

### Major Players & Sources

- **Google/AOSP**: Official build system (Soong, Kati, Ninja, Bazel), RBE/reclient remote execution [^1058^][^1124^]
- **distcc team**: Free distributed C/C++ compiler, pump mode for preprocessing distribution [^1020^][^1060^]
- **SUSE/Icecream**: IceCC distributed compiler with central scheduler [^1026^][^1034^]
- **ccache project**: Compiler caching tool with direct/preprocessor/depend modes [^1018^]
- **Mozilla/sccache**: Rust-friendly compiler cache with distributed compilation [^1116^]
- **Incredibuild**: Commercial AOSP build acceleration (shared cache + distributed computing) [^1016^][^1021^]
- **Buildbarn**: Open-source RBE implementation (scheduler, worker, runner, storage) [^1114^][^1022^]
- **Buildfarm**: Bazel remote caching and execution service (Redis backplane) [^1025^][^1015^]
- **BuildBuddy**: Commercial RBE service with free tier [^1057^]
- **BuildXL/Microsoft**: Large-scale distributed build accelerator [^1091^][^1088^]
- **Mold/LLD**: High-performance linkers (Rui Ueyama, LLVM project) [^1079^][^1082^]
- **Gradle/Develocity**: Remote build cache for Gradle/Android builds [^1071^][^1072^]
- **crosstool-NG**: Cross-compiler toolchain generator for embedded builds [^1047^]

---

### Trends & Signals

- **RBE becoming standard for large-scale AOSP**: Google's reclient is now the officially supported remote execution path for AOSP, replacing earlier custom solutions. [^1058^]

- **Bazel migration paused**: Google had planned to replace Soong/Kati/Ninja with Bazel but halted the transition. Bazel currently only builds the kernel. [^1126^]

- **Shared cache + distributed processing is the winning combination**: Cache eliminates redundant work; distribution parallelizes remaining work. Together they achieve 6-10x speedups. [^1010^][^1021^]

- **Rust in AOSP driving sccache adoption**: AOSP includes Rust code (toolchain/sccache in AOSP git), and sccache supports Rust compilation caching while ccache does not. [^1116^]

- **Linker performance revolution**: LLD is becoming the default linker for large projects; Mold pushes boundaries even further. Google's interest in LLD is partly due to divesting from GNU gold. [^1083^]

- **Container-based build environments**: Docker for reproducible builds, Kubernetes for CI/CD orchestration. Buildbarn supports Docker containers for remote execution workers. [^1041^][^1022^]

- **Zero-migration integration**: Tools like Incredibuild intercept at OS level without modifying build scripts, unlike Bazel which requires build rule rewrites. [^1084^]

---

### Controversies & Conflicting Claims

- **ccache vs no ccache in AOSP**: Google dropped official ccache prebuilt due to "non-reproducible results and other failures" and "weren't seeing significant performance gains at large scale" [^1023^]. However, many developers report 15-20% improvements with proper configuration. [^1010^]

- **sccache vs ccache performance**: sccache local disk cache is reportedly 3-4.5x slower than ccache on cache hits due to client-server model overhead [^1090^]. However, sccache offers distributed compilation and cloud storage backends that ccache lacks. [^1116^]

- **distcc pump mode reliability**: Pump mode can cause build failures with systems that rewrite headers during builds (e.g., Linux kernel). Falls back to plain distcc silently in some cases, masking speedup opportunities. [^1062^][^1070^]

- **Incremental build correctness**: AOSP incremental builds can "break mysteriously, leading to the dreaded 'let's try a clean build'" [^1010^]. This undermines the value of caching strategies.

- **Bazel for AOSP: promise vs reality**: Bazel was positioned as the future build system but transition was halted after years of effort. Soong/Kati/Ninja remains the primary build system. [^1126^]

- **-j parallelism tuning**: Too high values "may in fact make the build slower" due to local machine overload preparing jobs. distcc recommends 2x total CPUs but this varies by network and client capability. [^1034^][^1070^]

- **Hardware vs distributed approach**: Some argue high-end workstations (64-core Threadripper, 128GB RAM, fast NVMe) at $6-8K "might cut your 3-hour build to 2 hours" but are expensive to scale [^1010^]. Distributed approaches can achieve greater speedups with existing hardware.

---

### Recommended Deep-Dive Areas

- **AOSP RBE with Buildbarn/Buildfarm**: The most promising open-source path. Setting up a complete RBE cluster for AOSP with reclient, reproxy, and Buildbarn storage/scheduler/worker components. [^1114^] warrants a full implementation study.

- **distcc/Icecream integration with AOSP's Soong/Ninja**: How to transparently intercept compiler invocations from Soong and distribute them across a cluster. Need to investigate Soong's compiler wrapper mechanism.

- **Shared ccache/sccache architecture for teams**: NFS-mounted or S3-backed shared cache that multiple developers and CI runners can use. Need to measure cache hit rates across team workflows.

- **RAM disk (tmpfs) for out/ directory**: Mounting the AOSP build output directory on tmpfs to eliminate I/O bottlenecks during linking and packaging. Requires sufficient cluster aggregate RAM.

- **cgroups-based resource isolation per build job**: Using cgroups v2 to enforce CPU, memory, and I/O limits per build job in a shared cluster environment. Critical for multi-tenant build farms.

- **Docker-based reproducible AOSP build with distributed compilation**: Combining containerized build environments with distcc/Icecream/RBE for both reproducibility and speed.

- **Optimal -j and NINJA_REMOTE_NUM_JOBS tuning**: Google's default is 500 for AOSP RBE but recommends 256 for safety, 128 for 16GB systems. Finding optimal values for specific cluster configurations. [^1123^]

- **Prebuilt artifact management**: Strategies for extracting and caching prebuilt modules to reduce build scope, particularly for teams building customizations on top of baseline AOSP.

- **Link-time optimization (LTO) bottlenecks**: LTO can make linking the dominant build phase. Distributed linking strategies and fast linker selection (LLD/Mold) need investigation.

- **Build performance profiling methodology**: Using Soong's built-in profiling (Kati profiling, verbose.log.gz), strace, and perf to identify cluster-specific bottlenecks. [^1087^]

---

### Raw Evidence Log

**Claim:** AOSP full build takes ~40 minutes on 72-core/64GB machine, ~6 hours on 6-core/64GB machine.
**Source:** Android Open Source Project - Official Requirements
**URL:** https://source.android.com/docs/setup/start/requirements
**Date:** 2025-04-04
**Excerpt:** "Google uses 72-core machine and 64 GB RAM to build Android. With this hardware, a complete Android build takes about 40 minutes; an Android incremental build takes a few minutes. By contrast, it takes approximately 6 hours for a full build with a 6-core machine with 64 GB of RAM."
**Context:** Official AOSP hardware requirements documentation.
**Confidence:** high

---

**Claim:** AOSP build time breakdown is ~10-15 min dependency resolution, 60-90 min compilation, 20-40 min linking/packaging.
**Source:** Medium - "Why AOSP Builds Take Forever"
**URL:** https://pratikmahalle.medium.com/why-aosp-builds-take-forever-and-what-you-can-actually-do-about-it-c077c40797ee
**Date:** 2026-01-22
**Excerpt:** "Dependency resolution and setup (10-15 minutes): Soong analyzing the entire source tree, generating ninja files, mapping dependencies. Almost entirely single-threaded. Throwing more cores at this phase does nothing. Compilation (60-90 minutes): The bulk of your time. Linking and packaging (20-40 minutes): Creating system images, vendor images, boot images."
**Context:** Detailed analysis of AOSP build bottlenecks with practical strategies.
**Confidence:** high

---

**Claim:** distcc pump mode speeds up builds by factor of 3 over plain distcc, achieving 50-200% improvement on open-source software.
**Source:** Google Open Source Blog
**URL:** https://opensource.googleblog.com/2008/08/distccs-pump-mode-new-design-for.html
**Date:** 2008-08
**Excerpt:** "we've developed an algorithm we call 'pump mode', which can be added to distcc to speed it up by a factor of 3... we have tested pump mode on some open source software and seen improvements in build speed between 50% (the Linux kernel) and 200% (Samba)."
**Context:** Google's original announcement of pump mode development.
**Confidence:** high

---

**Claim:** ccache direct mode cache hits are ~5x faster than preprocessor mode hits, achieving 145x speedup over uncached compilation.
**Source:** ccache.dev - Official Performance Documentation
**URL:** https://ccache.dev/performance.html
**Date:** Undated
**Excerpt:** "ccache 3.7.1 direct, second time: 0.0048 s, 0.69 %, 145.39 x... As can be seen above, cache hits in the direct mode are about 5 times faster than in the preprocessor mode."
**Context:** Benchmarked on Intel Core i5-4690K with standard SSD.
**Confidence:** high

---

**Claim:** Google dropped official ccache prebuilt from AOSP due to non-reproducible results and limited gains at scale.
**Source:** Stack Overflow / AOSP Mailing List
**URL:** https://stackoverflow.com/questions/59811821/how-to-use-ccache-to-speed-up-compiling-of-aosp
**Date:** 2022-08-02
**Excerpt:** "We no longer provide a ccache prebuilt. Ours was old, and had a number of issues that triggered non-reproducible results and other failures. Newer ccache versions may fix some of those issues, but at the large scale of our build servers, we weren't seeing significant performance gains."
**Context:** Official response from AOSP team about ccache support.
**Confidence:** high

---

**Claim:** Icecream uses central scheduler for dynamic job distribution, supports cross-compilation, and handles heterogeneous environments with environment tarball transfer.
**Source:** UL.com - C/C++ Developer's Guide to Icecream
**URL:** https://www.ul.com/sis/blog/cc-developers-guide-part-2-icecream
**Date:** 2024-08-02
**Excerpt:** "Icecream is created by SUSE and is based on ideas and code by distcc. Unlike distcc's peer-to-peer architecture, an icecream compile farm is built around a central server. A scheduler daemon in charge of distributing incoming compile jobs to the available build nodes runs on the server."
**Context:** Detailed technical explanation of Icecream architecture.
**Confidence:** high

---

**Claim:** AOSP RBE configuration uses reclient with environment variables like USE_RBE=1, NINJA_REMOTE_NUM_JOBS, and per-tool execution strategies.
**Source:** AOSP Official Documentation - build/soong/docs/rbe.md
**URL:** https://android.googlesource.com/platform/build/soong/+/7b067fb75/docs/rbe.md
**Date:** Undated
**Excerpt:** "With RBE enabled, it can speed up the Android Platform builds by distributing build actions through a worker pool sharing a central cache of build results... USE_RBE: If set to 1, enable RBE for the build."
**Context:** Official AOSP RBE configuration documentation.
**Confidence:** high

---

**Claim:** Buildbarn RBE cluster setup for AOSP documented with topology: storage (CAS/AC) + frontend (REAPI) + scheduler + worker + runner.
**Source:** maksonlee.com - "Setting Up a Buildbarn RBE Cluster for AOSP"
**URL:** https://www.maksonlee.com/setting-up-a-buildbarn-rbe-cluster-for-aosp-ubuntu-24-04-from-source/
**Date:** 2026-01-31
**Excerpt:** "Topology: rbe-a = storage (CAS/AC) + frontend (REAPI) + scheduler + browser + worker + runner; rbe-b = worker + runner. Build from source for five binaries: bb_storage, bb_scheduler, bb_worker, bb_runner, bb_browser."
**Context:** Complete step-by-step guide for Buildbarn RBE cluster for AOSP.
**Confidence:** high

---

**Claim:** Incredibuild achieves 6.3x acceleration for AOSP 16 on 32-core machine (1h46m -> 17m) and ~10x on 16-core workstation.
**Source:** Incredibuild AOSP Whitepaper
**URL:** https://www.incredibuild.com/wp-content/uploads/2025/12/AOSP-16-and-15-with-Incredibuild-Whitepaper.pdf.pdf
**Date:** Undated
**Excerpt:** "A similar benchmark on AOSP 16 running on a common 16-core developer workstation shows an acceleration ratio of ~10x faster build time over baseline! AOSP 16 build time on a 16-core machine: 0:20:15 min Incredibuild vs 3:18:21 hrs No Acceleration"
**Context:** Vendor benchmark claims on specific hardware configurations.
**Confidence:** medium (vendor-provided benchmarks)

---

**Claim:** LLD linker is 2-3x faster than GNU gold and 5-10x faster than GNU ld.bfd, with 26K LOC vs 164K LOC.
**Source:** Phoronix - "A Detailed Look At The Speed Advantages To LLVM's LLD Linker"
**URL:** https://www.phoronix.com/news/LLD-Linker-Why-So-Fast
**Date:** 2019-02-06
**Excerpt:** "Smith found LLD was faster than the Gold linker by two to three times while faster than the standard ld.bfd linker by five to ten times. Among the reasons why LLD is so much faster comes down to its threading model, continuously evaluating its performance with code changes, a custom memory allocator, more efficient data structures."
**Context:** Linaro's Peter Smith extensive performance analysis of LLD.
**Confidence:** high

---

**Claim:** Mold linker links Chrome 96 (1.89GB) in 2.2s vs GNU gold 53s and LLD 11.7s on 8-core machine - 26x faster than gold.
**Source:** desdelinux.net - "Mold, a modern Linker superior to GNU gold and LLVM lld"
**URL:** https://blog.desdelinux.net/en/mold-a-modern-linker-superior-to-gnu-gold-and-llvm-lld/
**Date:** 2021-12-16
**Excerpt:** "When compiling Chrome 96 (code size 1,89 GB), linking executable files with debuginfo on an 8-core computer takes 53 seconds with GNU Gold, LLVM lld takes 11,7 seconds, and Mold only 2,2 seconds (26 times faster than GNU gold)."
**Context:** Mold 1.0 release benchmarks across multiple projects.
**Confidence:** medium (third-party benchmark)

---

**Claim:** sccache supports C/C++, Rust, CUDA compilation caching with distributed compilation (icecream-style) and multiple cloud storage backends.
**Source:** AOSP toolchain/sccache README
**URL:** https://android.googlesource.com/toolchain/sccache/+/d2dd39805d19acb522e7ac6ef5cae7bf5c0e5a53
**Date:** Undated
**Excerpt:** "sccache is a ccache-like compiler caching tool... includes support for caching the compilation of C/C++ code, Rust, as well as NVIDIA's CUDA using nvcc. sccache also provides icecream-style distributed compilation (automatic packaging of local toolchains) for all supported compilers (including Rust)."
**Context:** Official AOSP sccache repository documentation.
**Confidence:** high

---

**Claim:** BuildXL runs 150,000+ builds/day internally at Microsoft on monorepos up to half-terabyte with half-million process executions per build.
**Source:** Microsoft BuildXL GitHub
**URL:** https://github.com/microsoft/buildxl
**Date:** Undated
**Excerpt:** "Internally at Microsoft, BuildXL runs 150,000+ builds per day on monorepo codebases up to a half-terabyte in size with a half-million process executions per build. It leverages distribution to thousands of data center machines and petabytes of source code, package, and build output caching."
**Context:** Microsoft's official BuildXL repository description.
**Confidence:** high

---

**Claim:** Soong generates build.ninja files of 6-10 GiB that must be regenerated whenever any Android.bp files change, taking significant time.
**Source:** 2net.co.uk - "The AOSP Build System" (ELC 2023 slides)
**URL:** https://2net.co.uk/slides/elc/aosp-build-eoss-2023.pdf
**Date:** 2023
**Excerpt:** "In the first phase, soong parses all Android.bp files and writes build rules to out/soong/build.ninja... This is a 'big' file: 6 to 10 GiB. Has to be regenerated whenever any Android.bp files are added or changed... takes a long time."
**Context:** Professional AOSP build system training materials.
**Confidence:** high

---

**Claim:** Docker-based reproducible AOSP build with Jenkins uses ccache, repo tool, and specific dependency packages in Ubuntu 24.04 container.
**Source:** maksonlee.com - "Build Android 15 AOSP with Jenkins and Docker"
**URL:** https://www.maksonlee.com/build-android-15-aosp-with-jenkins-and-docker-on-an-ssh-connected-agent/
**Date:** 2026-01-31
**Excerpt:** "ENV USE_CCACHE=1, ENV CCACHE_DIR=/ccache, ENV CCACHE_EXEC=/usr/local/bin/ccache... RUN apt-get update && apt-get install -y --no-install-recommends git-core gnupg flex bison build-essential zip curl zlib1g-dev..."
**Context:** Complete Dockerfile and Jenkins pipeline for AOSP builds.
**Confidence:** high

---

**Claim:** cgroups v2 CPU limiting works via cpu.max file (quota period microseconds), memory via memory.max, I/O via io.max with device-specific bandwidth/IOPS limits.
**Source:** Medium - "Taming the Beast: Resource Allocation with Linux cgroups"
**URL:** https://medium.com/@suyashadhikari99/taming-the-beast-resource-allocation-with-linux-cgroups-235f5221ba3c
**Date:** 2026-03-29
**Excerpt:** "CPU limiting in v2 works via the cpu.max file, which takes two numbers: quota and period, both in microseconds. If quota is 50000 and period is 100000, the group gets at most 50ms out of every 100ms - exactly 50% of one CPU core."
**Context:** Comprehensive cgroups v2 tutorial with practical examples.
**Confidence:** high

---

**Claim:** RBE configuration for AOSP discovered through source code analysis; many options not officially documented by Google.
**Source:** fosson.top - "Configuring RBE"
**URL:** https://fosson.top/aosp/remote-build-execution/configure-rbe
**Date:** 2026-05-22
**Excerpt:** "Many of these options are not officially documented by Google and were discovered through AOSP source code analysis. NINJA_REMOTE_NUM_JOBS: The number of parallel jobs to run remotely. Start with 256 and increase if you have more RAM. 128 should be safe for 16GB RAM systems."
**Context:** Community-discovered RBE configuration options for AOSP.
**Confidence:** medium (community source, not officially verified)

---

**Claim:** Tekton + ArgoCD provides complete GitOps CI/CD pipeline: Tekton handles CI (clone, test, build, push), ArgoCD handles CD (detect Git change, sync to cluster).
**Source:** Dev.to - "Tekton + Argo CD: Building a Complete GitOps Pipeline"
**URL:** https://dev.to/jamesli/tekton-argo-cd-building-a-complete-gitops-pipeline-end-to-end-4and
**Date:** 2026-05-19
**Excerpt:** "The key architectural insight: Tekton and Argo CD each own a clearly defined half of the pipeline, with a Git repository as the handoff point between them. CI (Tekton): clone, test, build, push. CD (Argo CD): watches Git, syncs to cluster."
**Context:** Detailed end-to-end GitOps pipeline tutorial.
**Confidence:** high

---

**Claim:** AOSP build system consists of >8000 Android.bp files and ~1000 Android.mk files, processed by Soong and Kati respectively into Ninja manifests.
**Source:** Medium - "Inside the AOSP Build System"
**URL:** https://medium.com/@mmohamedrashik/the-aosp-build-system-an-in-depth-guide-f686d89d28b7
**Date:** 2025-02-03
**Excerpt:** "Android.bp: Written in Blueprint, with over 8,000+ modules in AOSP. Android.mk: Written in Makefile format, deprecated but still in use with about 1,000+ modules."
**Context:** Comprehensive AOSP build system guide.
**Confidence:** high

---

**Claim:** Gradle remote build cache recommended setup: CI populates cache from clean builds, developers only load from it. Supports HTTP-based remote cache server.
**Source:** Gradle Official Documentation - Build Cache
**URL:** https://docs.gradle.org/current/userguide/build_cache.html
**Date:** 2018-09-07
**Excerpt:** "The recommended use case for the remote build cache is that your continuous integration server populates it from clean builds while developers only load from it... remote(HttpBuildCache) { url = 'https://example.com:8123/cache/' push = isCiServer }"
**Context:** Official Gradle build cache documentation.
**Confidence:** high

---

**Claim:** Kati profiling available via verbose.log.gz showing which Makefiles take most time; can profile specific makefiles with $(KATI_profile_makefile).
**Source:** AOSP Soong Documentation - perf.md
**URL:** https://android.googlesource.com/platform/build/soong/+/HEAD/docs/perf.md
**Date:** Undated
**Excerpt:** "verbose: *kati*: included makefiles: 73.640833 / 232810 (1066 unique)... By default this only includes the top 10 entries, but you can ask for the stats for any makefile to be printed with $(KATI_profile_makefile)."
**Context:** Official AOSP build performance debugging documentation.
**Confidence:** high

---

**Claim:** distcc achieves near-linear scalability for small numbers of machines: 2.6x speedup with 3 machines on 100Mbps switch (89% efficiency).
**Source:** distcc.org - Official Website
**URL:** https://www.distcc.org/
**Date:** Undated
**Excerpt:** "Building Linux 2.4.19 on a single 1700MHz Pentium IV machine with distcc 0.15 takes 6 minutes, 45 seconds. Using distcc across three such machines on a 100Mbps switch takes only 2 minutes, 30 seconds: 2.6x faster. The (unreachable) theoretical maximum speedup is 3.0x, so in this case distcc scales with 89% efficiency."
**Context:** Official distcc benchmark results.
**Confidence:** high

---

**Claim:** crosstool-NG is a versatile cross-toolchain generator supporting many architectures with menuconfig-style interface. Latest release 1.28.0 as of Sep 2025.
**Source:** crosstool-NG Official Website
**URL:** https://crosstool-ng.github.io/
**Date:** 2025-09-06
**Excerpt:** "Crosstool-NG is a versatile (cross) toolchain generator. It supports many architectures and components and has a simple yet powerful menuconfig-style interface. Released 1.28.0."
**Context:** Official crosstool-NG project website.
**Confidence:** high

---

**Claim:** Putting ccache on RAM disk with tmpfs and periodic save/restore can significantly speed up builds compared to SSD, especially for cache-heavy workloads.
**Source:** gessel.blackrosetech.com - "Putting ccache on a backed RAM disk to speed compiles"
**URL:** https://gessel.blackrosetech.com/2024/03/16/putting-ccache-on-a-backed-ram-disk-to-speed-compiles
**Date:** 2024-05-02
**Excerpt:** "Compiling and building ports can be meaningfully accelerated by caching (ccache) certain intermediate results and by moving work directories from slower media to faster (tmpfs /tmp). If you do regular builds, such as one might on a poudriere server, there can be a meaningful write workload."
**Context:** FreeBSD-focused but applicable to Linux tmpfs approaches.
**Confidence:** medium

---

**Claim:** sccache distributed compilation includes security features icecream lacks: authentication, TLS transport encryption, sandboxed compiler execution.
**Source:** AOSP toolchain/sccache README
**URL:** https://android.googlesource.com/toolchain/sccache/+/d2dd39805d19acb522e7ac6ef5cae7bf5c0e5a53
**Date:** Undated
**Excerpt:** "The distributed compilation system includes several security features that icecream lacks such as authentication, transport layer encryption, and sandboxed compiler execution on build servers."
**Context:** Official sccache documentation from AOSP repository.
**Confidence:** high
