## Facet: Distributed Operating Systems & Single System Image (SSI)

### Key Findings

#### Historic SSI Cluster Implementations (MOSIX, openMosix, Kerrighed, OpenSSI)
- **MOSIX** (1999-2017): Implemented process migration as a Linux kernel loadable module, splitting processes into a migratable user context (executing remotely) and a fixed system context ("deputy" at the home node). Communication used TCP/IP at the link layer to intercept system calls, signals, and events. It supported adaptive resource sharing without binary incompatibilities via the /proc filesystem. [^138^] [^143^]
- **openMosix** (2002-2008): Forked from MOSIX when the latter went proprietary in 2001. Founded by Moshe Bar to preserve open-source SSI clustering. Discontinued in March 2008 because multi-core processors reduced the perceived need for SSI clustering. The codebase was later repurposed into LinuxPMI. [^138^]
- **Kerrighed** (1998-~2010): Achieved the best performance among SSI systems but was the most unstable. A comparative study found Kerrighed had the best execution times but "wasn't able to efficiently distribute such big number of processes" and "Tests for Kerrighed and the number of program instances equal 384, 768 and 1536 weren't completed due to the system failure. Kerrighed is not stable system." [^10^] [^12^]
- **OpenSSI** (2004-mid-2000s): Covered nearly all SSI features including transparent process migration, unified filesystems, and process namespaces. However, "in the series with bigger number of processes, OpenSSI performance dropped significantly... It seems that OpenSSI wasn't able to efficiently distribute such big number of processes over the nodes in the cluster." Discontinued due to maintenance challenges with evolving Linux kernels. [^10^] [^12^]
- **Common failure reasons**: All SSI projects required kernel patchsets incompatible with distribution kernels. Porting to new kernel versions was labor-intensive. SSI declined after 2010 due to virtualization (Xen, VMware) being easier, and the rise of multi-core processors reduced demand. [^12^] [^138^]

#### Plan 9 from Bell Labs - Distributed Design Legacy
- Plan 9 was designed as a distributed operating system from the ground up, unlike Unix where network functionality was added later. In Plan 9, "networking is built into the foundations of the operating system" with all resources theoretically distributable transparently across a network. [^58^]
- **9P Protocol**: The Plan 9 file protocol comprising about 30 protocol messages. A Linux implementation was added to the kernel mainline in version 2.6.14, enabling Linux-Plan 9 interaction. 9P and its derivatives (9P2000/Styx) remain influential. [^58^]
- **Per-process namespace**: Each process in Plan 9 has its own view of the filesystem namespace, constructed by mounting resources. This enables composable distributed systems where resources are attached dynamically. [^58^]
- **Key concept**: "Everything is a file" was fully realized in Plan 9. System services follow the server principle and interact via file-based interfaces. Network interfaces appear as /net/tcp, /net/udp. No special socket API - all resources are files. [^58^]
- Plan 9 derivatives include 9front, Inferno, Akaros (many-core architectures), JehanneOS, and NIX (aimed at multicore systems and cloud computing). [^14^]
- A derivative called **node9** explicitly "demonstrates that a distributed OS can be constructed from per-process namespaces and generic cloud elements to construct a single-system-image of arbitrary size." [^14^]

#### Popcorn Linux - Heterogeneous-ISA Distributed Systems
- Popcorn Linux is a replicated-kernel OS based on Linux that enables POSIX shared memory applications to run across heterogeneous-ISA platforms (e.g., x86-64 + Xeon Phi, later ARMv8 + x86-64). It was the first Linux-based replicated-kernel OS running on a heterogeneous-ISA platform. [^49^]
- **Architecture**: Different kernels, each compiled for a different processor island, interact to provide applications with the illusion of a single OS. The OS state is partially replicated on all kernels. A communication layer provides basic data conversion between ISAs. [^49^] [^50^]
- **Compiler framework**: Built on clang/LLVM, it generates multi-ISA binaries with one .data section and multiple .text sections. The runtime transforms dynamic ISA-specific program state (stack, registers) on the fly during migration. [^53^]
- Popcorn Linux was extended in 2017 to migrate Linux containers between heterogeneous-ISA servers, introducing "heterogeneous continuations" and a heterogeneous-binary loader. [^50^]
- Performance evaluation showed Popcorn yields 30% energy savings on average (max 66%) and 11% reduction in energy-delay product for heterogeneous workloads. [^53^]

#### Barrelfish - The Multikernel Model
- Barrelfish was developed by ETH Zurich and Microsoft Research (2007-2020). Final release March 23, 2020; project now discontinued. [^56^]
- **Three design principles**: (1) Make all inter-core communication explicit, (2) Make OS structure hardware-neutral, (3) View state as replicated instead of shared. [^54^]
- "We view the OS as, first and foremost, a distributed system which may be amenable to local optimizations, rather than centralized system which must somehow be scaled to the network-like environment of a modern or future machine." [^54^]
- Each core runs an independent kernel (CPU driver + monitor process). Kernels share no memory, even on cache-coherent hardware. The OS uses message-passing between cores. [^54^]
- Barrelfish's SOSP 2009 paper demonstrated that "even on present-day machines, the performance of a multikernel is comparable with a conventional OS, and can scale better to support future hardware." [^158^]
- Related Microsoft research projects include Midori (managed-code OS) and Helios (heterogeneous multiprocessing with satellite kernels). [^54^] [^56^]

#### DragonFly BSD - Clustering Vision
- DragonFly BSD was founded by Matthew Dillon with an "unattainable goal" of "develop DragonFly into a transparently cluster-capable system implementing native SSI." This vision has driven its architectural decisions since inception. [^172^]
- **Lightweight kernel threading**: DragonFly uses a thread-centric model instead of a mutex-centric model, "partitioning major subsystems into threads instead of serializing data access with mutexes." [^172^]
- **HAMMER2 filesystem**: Block copy-on-write filesystem supporting online deduplication, clustering features, multiple mountable filesystem roots, snapshots, compression, encryption. Features dynamic radix tree, instant recovery on mount, and 2^63 logical file size limit. HAMMER2 was designed with future multi-master clustered volumes and replicated fanout mirrors as a goal. [^48^] [^55^]
- DragonFly BSD's HAMMER1 provided "near-live master:slave replication" similar to ZFS/Btrfs but with very low memory requirements. [^57^]

#### Distributed Shared Memory (DSM) Systems
- **Ivy** (Li & Hudak, 1989): The first major page-based DSM implementing sequential consistency. Uses virtual memory hardware to detect accesses. A write fault sends invalidate messages to all processors with copies. Suffered from extensive communication due to sequential consistency requirements. [^139^] [^141^]
- **Munin** (Carter, Bennett, Zwaenepoel, Rice University): First software DSM using release consistency (weaker than sequential). Introduced multiple consistency protocols selected by programmer annotations on shared variables (read-only, migratory, write-shared, conventional). Achieved performance within 5-10% of message passing implementations. Used a delayed update queue (DUQ) to buffer pending writes. [^148^] [^149^]
- **TreadMarks** (Keleher et al., Rice University): User-level DSM using lazy release consistency and multiple-writer protocols to reduce false sharing. Achieved speedups of 7.4x on 8 processors for Jacobi on 100Mbps ATM. Key finding: "Unix communication overhead remains the main obstacle... memory management cost is small and wire time is negligible." [^108^] [^109^]
- **JIAJIA**: Home-based lazy release consistency DSM implementing scope consistency. Used home migration of shared pages to adapt to memory access patterns. Compared favorably to homeless protocols in TreadMarks. [^169^]
- **Key lesson**: DSM overhead was dominated by software communication costs, not wire time. This fundamentally limited the granularity of parallelism that could be efficiently exploited.

#### Modern SSI Approximations (Kubernetes, LXD Clustering)
- **Kubernetes Federation (KubeFed)**: Provides multi-cluster management through a host cluster containing configurations propagated to member clusters. Uses Templates (base resource spec), Placement (target clusters), and Overrides (cluster-specific variations). Manages DNS entries for multi-cluster services. [^152^] [^153^] [^157^]
- **Limitation**: Kubernetes does NOT provide true SSI - it provides orchestration across independent clusters, not a unified process namespace, memory space, or filesystem. Containers cannot transparently migrate between nodes while maintaining open connections in the same way SSI systems allowed.
- **LXD Clustering**: A cluster in LXD is "a group of servers linked together under a common control plane. Containers can be distributed, balanced, and migrated across these nodes transparently." LXD supports live migration using CRIU for containers. Uses Ceph RBD as preferred storage backend for cross-node mobility. [^140^] [^142^]
- Container migration requires stopping containers first; live migration only supported for VMs in current LXD. When using Ceph, containers can be moved cheaply between nodes. [^142^] [^145^]

#### Process Migration Techniques & CRIU
- **CRIU (Checkpoint/Restore In Userspace)**: Released 2012, relies on Linux kernel features since 3.11. Completely implemented in userspace using /proc filesystem and ptrace(). Supports pre-copy migration via iterative dumps and post-copy via page server. Integrated into OpenVZ, Podman, Docker, LXC/LXD, Borg. [^70^] [^74^]
- **Pre-copy**: Memory copied while process runs, tracking dirtied pages, iteratively copying until dirty rate is low. Final brief stop to copy last dirty pages. Reduces downtime at cost of copying pages multiple times. [^72^]
- **Post-copy**: Stop process immediately, transfer minimal state (CPU registers), restart on target, fetch memory pages on-demand over network (page faults trigger fetch from source). Lower downtime but risk if source fails before all pages transferred. [^72^]
- **P.Haul**: Built on CRIU to coordinate live migration phases. OpenVZ and LXC attempted to use it. No longer actively developed. [^70^]
- **Limitation**: CRIU cannot checkpoint every possible process - kernel threads, certain device accesses, and shared memory with external processes are problematic. [^74^]

#### Linux Kernel Live Patching (kpatch, kGraft)
- **kpatch (Red Hat)**: Uses ftrace infrastructure to redirect kernel function calls. Builds kernel modules containing replacement functions. Since 2014, merged with kGraft concepts into upstream Linux livepatch core. Limited to function-level replacements; data structure changes may require reboots. [^66^] [^75^]
- **kGraft (SUSE)**: Also uses ftrace with INT3 breakpoint + JMP opcode replacement. Key feature: "never requires stopping the kernel, not even for a short time period." Uses per-thread flags and RCU-like trampolines to ensure consistent view. However, cannot apply another patch until all processes cross kernel-user boundary. [^67^]
- **Upstream Linux livepatch**: Since kernel 4.0, combines concepts from kpatch and kGraft. Requires CONFIG_LIVEPATCH, CONFIG_DEBUG_INFO, CONFIG_KALLSYMS. [^66^]
- **Limitations**: Only function-level patches, not data structure changes. Cannot patch code being probed by SystemTap/kprobes. Not all kernels receive live patches. Deep structural changes still need maintenance windows. [^67^] [^75^]
- **Relevance to Cluster OS**: Live patching enables rolling kernel updates across cluster nodes without rebooting, maintaining cluster availability. However, it does not solve the fundamental challenge of migrating running processes between nodes with different kernel versions.

#### Modern Academic Research - Post-SSI Directions
- **LegoOS** (Purdue, OSDI 2018 Best Paper): The first OS designed for hardware resource disaggregation. Proposed the "splitkernel" model - OS functionalities disseminated into loosely-coupled monitors running on separate hardware components (processor, memory, storage). Uses RDMA-based RPC over InfiniBand/RoCE. 1.3x-1.7x slower than monolithic Linux but improves resource packing and failure handling. [^165^] [^166^]
- **Popcorn Linux** (Virginia Tech, EuroSys 2015; ASPLOS 2017): Replicated-kernel OS for heterogeneous-ISA platforms. Extended to support heterogeneous-ISA containers. [^49^] [^50^]
- **Nanvix** (2025): A multikernel OS design for high-density environments, continuing the Barrelfish tradition. [^155^]
- **HelenOS**: A modern microkernel-based multiserver OS from Charles University in Prague, not currently distributed but with multiserver architecture that could support distribution. Used as research platform for software components and verification. [^111^] [^120^]
- **Fuchsia OS** (Google): Capability-based microkernel OS using Zircon. No global filesystem - each component has its own local namespace. Components identified by URLs, resolved/executed on demand. Uses sandboxing with explicit IPC declarations. Over 170 syscalls (more than typical microkernel). Not a distributed OS per se but has distributed-friendly component model. [^150^]
- **Key trend**: The field has shifted away from "true SSI" (transparent single-machine illusion across clusters) toward explicit distributed resource management, container orchestration, and hardware disaggregation.

### Major Players & Sources

| Entity | Role/Relevance |
|--------|---------------|
| **Amnon Barak / Hebrew University** | Creator of MOSIX (1981-2017), foundational SSI research, process migration pioneer |
| **Moshe Bar** | Creator of openMosix fork, co-project manager of MOSIX |
| **INRIA (Kerrighed team)** | Developed Kerrighed SSI system (best performance, worst stability) |
| **OpenSSI Project** | OpenSSI Linux-based SSI (most complete feature set, performance issues) |
| **Bell Labs (Pike, Ritchie, Thompson)** | Created Plan 9 (1990s), 9P protocol, per-process namespace - foundational distributed OS |
| **Virginia Tech SSRG (Binoy Ravindran)** | Popcorn Linux - replicated-kernel heterogeneous-ISA OS |
| **ETH Zurich / Microsoft Research** | Barrelfish multikernel OS (2007-2020), SOSP 2009 paper |
| **Matthew Dillon / DragonFly BSD** | DragonFly BSD, HAMMER filesystem, native SSI clustering vision |
| **Rice University (Zwaenepoel et al.)** | TreadMarks DSM, Munin DSM - foundational DSM research |
| **Purdue University (Yiying Zhang)** | LegoOS splitkernel OS for resource disaggregation (OSDI 2018 Best Paper) |
| **Charles University Prague** | HelenOS microkernel multiserver OS research platform |
| **Google** | Fuchsia OS - capability-based microkernel for connected devices |
| **Red Hat (kpatch)** | Linux kernel live patching, upstream livepatch core |
| **SUSE (kGraft)** | Alternative live patching approach, merged concepts into upstream |
| **CRIU Team / OpenVZ** | Checkpoint/restore in userspace, container migration foundation |

### Trends & Signals

- **Death of "True SSI"**: All kernel-level SSI projects (MOSIX, openMosix, Kerrighed, OpenSSI) are discontinued. The last active development ended around 2010-2013. The consensus is that SSI at the kernel level is "dead" as a practical approach. [^12^] [^138^]
- **Rise of Virtualization Over SSI**: Xen, KVM, VMware ESX provided easier cluster computing abstractions without requiring kernel modifications. SSI declined after 2010 precisely because virtualization was easier to deploy and maintain. [^12^]
- **Container Orchestration as SSI Approximation**: Kubernetes provides distributed deployment, service discovery, and load balancing but explicitly NOT transparent process migration or unified memory. It is an "orchestration" layer, not an SSI layer. [^152^] [^153^]
- **Hardware Resource Disaggregation**: The most active research direction in distributed OS design. LegoOS (2018) and similar projects are exploring breaking the monolithic server model into network-attached components. Enabled by RDMA, CXL, high-bandwidth networks (200Gbps+). [^165^] [^166^]
- **Heterogeneous Computing Driving OS Innovation**: Popcorn Linux and Fuchsia OS both address heterogeneous hardware, suggesting future cluster OS must handle diverse ISAs and accelerator types. [^49^] [^50^]
- **Live Patching Maturation**: Kernel live patching (kpatch, kGraft, Ksplice, KernelCare) is now production-grade, enabling zero-downtime security updates. However, it addresses function-level patches, not structural kernel changes. [^66^] [^67^] [^68^]
- **Message-Passing OS Revival**: Barrelfish's multikernel model (explicit message passing, no shared memory) is seeing renewed interest with Nanvix (2025) and influences on splitkernel designs. [^155^]

### Controversies & Conflicting Claims

- **SSI: Valuable Goal or Failed Dream?**: Matthew Dillon of DragonFly BSD maintains SSI as an "unattainable goal" worth pursuing [^172^], while the broader industry has abandoned it in favor of explicit orchestration. Hacker News commentary noted: "those academic efforts look terribly naive from today's distributed consensus standpoint" [^121^].
- **Multikernel Performance Claims**: Barrelfish claimed "performance comparable with a conventional OS" [^54^], but critics noted that the "no shared memory" idealism sacrifices "certain platform-specific performance optimizations, such as making use of a shared L2 cache between cores" [^54^]. The project was ultimately discontinued in 2020.
- **DSM Viability**: TreadMarks demonstrated "DSM is a viable technique for parallel computation on clusters of workstations" [^109^], but subsequent experience showed DSM overhead was dominated by communication software, not hardware - fundamentally limiting applicability to coarse-grained parallelism.
- **Container Migration vs. Process Migration**: CRIU-based container migration claims viability for "individual containers when time constraints are not a primary concern" [^70^], but live migration of containers remains far from the transparent process migration that MOSIX achieved decades ago.
- **Live Patching Scope**: Vendors claim live patching enables "100% CVE coverage" [^68^], but in practice "not all critical or important CVEs" can be addressed via live patches, and "deep structural changes still need maintenance windows" [^69^].

### Recommended Deep-Dive Areas

1. **Hardware Resource Disaggregation & CXL**: LegoOS demonstrated the concept with emulated hardware. With CXL (Compute Express Link) now shipping in real hardware, the splitkernel model deserves serious investigation. This could be the foundation for a new cluster OS that goes beyond SSI to true resource pooling.

2. **RDMA-Based Cluster Memory Systems**: Remote memory access is ~20x slower than local even over InfiniBand, but new RDMA NICs, CXL.mem, and memory semantic networks are narrowing this gap. A cluster OS that treats remote memory as a cacheable, coherent extension of local memory could be transformative.

3. **Heterogeneous-ISA Container Migration (Popcorn Linux)**: The ability to migrate containers between x86, ARM, and RISC-V nodes while maintaining execution state is a critical capability for future clusters. Popcorn Linux's compiler-based approach (multi-ISA binaries with aligned data sections) is a proven technique worth building on.

4. **9P/virtio-fs Distributed Filesystem Semantics**: Plan 9's per-process namespace and file-as-resource model is deeply relevant to container and microservice architectures. Modern systems like virtio-fs and FUSE-based distributed filesystems implement similar concepts.

5. **Checkpoint/Restore for Stateful Workloads (CRIU)**: For cluster OS to support true workload mobility, CRIU or similar checkpoint/restore must handle: GPU state, RDMA connections, persistent memory mappings, and kernel module state. Current CRIU limitations (no kernel threads, limited device support) are blockers.

6. **Live Patching + Rolling Migration**: Combining kernel live patching with process migration could enable zero-downtime kernel updates across clusters. The key challenge: migrating a process from a node running kernel version A to a node running kernel version B with different data structures.

7. **HAMMER2 Clustering Features**: DragonFly BSD's HAMMER2 filesystem includes clustering primitives that have been under development for over a decade. Understanding what has been implemented and what remains could inform Cluster OS filesystem design.

8. **seL4/L4 Microkernel as Cluster Foundation**: The formally verified seL4 microkernel and the broader L4 family provide trustworthy isolation primitives that could serve as the foundation for a distributed cluster OS. Current efforts (HelenOS, Genode) could be extended to multi-node scenarios.

### Raw Evidence Log

---

**Claim**: Kerrighed achieved best performance among SSI systems but was unstable - system failures occurred at higher process counts.
**Source**: Comparative Study of Single System Image Clusters (KAEiOG 2009)
**URL**: https://troja.uksw.edu.pl/pdf/kaeiog/KAEiOG2009.145-154.pdf
**Date**: 2009
**Excerpt**: "The best results were obtained for Kerrighed. However... Tests for Kerrighed and the number of program instances equal 384, 768 and 1536 weren't completed due to the system failure. Kerrighed is not stable system."
**Context**: Benchmark comparing openMosix, OpenSSI, and Kerrighed on a 3-node cluster
**Confidence**: High

---

**Claim**: OpenSSI covered nearly all SSI features but performance dropped significantly with many processes.
**Source**: Comparative Study of Single System Image Clusters (KAEiOG 2009)
**URL**: https://troja.uksw.edu.pl/pdf/kaeiog/KAEiOG2009.145-154.pdf
**Date**: 2009
**Excerpt**: "in the series with bigger number of processes... OpenSSI performance dropped significantly. It seems that OpenSSI wasn't able to efficiently distribute such big number of processes over the nodes in the cluster."
**Context**: Same benchmark suite, testing 768+ processes
**Confidence**: High

---

**Claim**: Plan 9 was designed as a distributed OS from the ground up, with networking built into foundations.
**Source**: Linux Magazine - The Plan 9 Network Operating System
**URL**: https://www.linux-magazine.com/content/download/62920/486498/file/Plan9_Network_Operating_System.pdf
**Date**: Unknown (circa 2006)
**Excerpt**: "The underlying concept for Plan 9 is that it is a distributed operating system, not like Unix, where network functionality is added by mechanisms such as remote login and networking filesystems. In Plan 9, networking is built into the foundations of the operating system."
**Context**: Plan 9 overview article
**Confidence**: High

---

**Claim**: Popcorn Linux is a replicated-kernel OS that migrates threads across heterogeneous ISA boundaries.
**Source**: Popcorn: Bridging the Programmability Gap in Heterogeneous-ISA Platforms (EuroSys 2015)
**URL**: https://www.ssrg.ece.vt.edu/papers/eurosys15.pdf
**Date**: 2015
**Excerpt**: "Our operating system is the first Linux-based replicated-kernel OS running on a heterogeneous-ISA platform. We also introduce an extended memory subsystem for Linux that allows consistent task-based address space replication and DSM amongst kernels."
**Context**: Peer-reviewed academic paper, EuroSys 2015
**Confidence**: High

---

**Claim**: Popcorn Linux extended to support heterogeneous-ISA containers, migrating Linux containers between ARMv8 and x86-64.
**Source**: Breaking the Boundaries in Heterogeneous-ISA Datacenters (ASPLOS 2017)
**URL**: https://www.ssrg.ece.vt.edu/papers/asplos_2017.pdf
**Date**: 2017
**Excerpt**: "We extended the Popcorn Linux replicated-kernel OS to support heterogeneous-ISA machines. Popcorn is based on the Linux kernel and re-implements several of its operating system services in a distributed fashion."
**Context**: Peer-reviewed academic paper, ASPLOS 2017
**Confidence**: High

---

**Claim**: Barrelfish OS structures the OS as a distributed system of cores with no inter-core sharing at the lowest level.
**Source**: The Multikernel: A new OS architecture for scalable multicore systems (SOSP 2009)
**URL**: https://www.sigops.org/s/conferences/sosp/2009/papers/baumann-sosp09.pdf
**Date**: October 2009
**Excerpt**: "we structure the OS as a distributed system of cores that communicate using messages and share no memory... The multikernel model is guided by three design principles: 1. Make all inter-core communication explicit. 2. Make OS structure hardware-neutral. 3. View state as replicated instead of shared."
**Context**: SOSP 2009 best paper, peer-reviewed
**Confidence**: High

---

**Claim**: Barrelfish was discontinued in 2020 after 13 years of development.
**Source**: Wikipedia - Barrelfish (operating system)
**URL**: https://en.wikipedia.org/wiki/Barrelfish_(operating_system)
**Date**: July 2025 (last edit)
**Excerpt**: "Barrelfish is a discontinued, open-source distributed operating system... The final official release was on March 23, 2020."
**Context**: Wikipedia article with citations
**Confidence**: High

---

**Claim**: DragonFly BSD's goal is native SSI clustering capability, and it uses thread-centric rather than mutex-centric kernel design.
**Source**: OSNews Interview with Matthew Dillon of DragonFly BSD
**URL**: https://www.osnews.com/story/6338/interview-with-matthew-dillon-of-dragonfly-bsd/
**Date**: 2026 (archive)
**Excerpt**: "our unattainable goal (which I hope actually winds up being attainable) is to develop DragonFly into a transparently cluster-capable system implementing native SSI... DragonFly uses a thread-centric model [instead of mutex-centric]."
**Context**: Interview with DragonFly BSD founder
**Confidence**: High

---

**Claim**: HAMMER2 filesystem supports clustering features, online deduplication, snapshots, with future multi-master cluster volumes planned.
**Source**: DragonFlyBSD HAMMER2 documentation
**URL**: https://www.dragonflybsd.org/hammer/
**Date**: 2008+ (ongoing)
**Excerpt**: "Block copy-on-write filesystem... clustering... multiple mountable filesystem roots... snapshots... Future master/slave mechanism... Recursive check codes to detect corruption"
**Context**: Official DragonFly BSD documentation
**Confidence**: High

---

**Claim**: TreadMarks achieved good speedups (7.4x Jacobi on 8 processors) using lazy release consistency and multiple-writer protocols.
**Source**: TreadMarks: Distributed Shared Memory on Standard Workstations (USENIX Winter 1994)
**URL**: https://www.eecg.toronto.edu/~amza/ece1747h/papers/treadmarks94.pdf
**Date**: 1994
**Excerpt**: "We achieved good speedups on the 8-processor ATM network for Jacobi (7.4), TSP (7.2), Quicksort (6.3), and ILINK (5.7)... Unix communication overhead, however, remains the main obstacle in the way of better performance for programs like Water."
**Context**: Peer-reviewed USENIX paper
**Confidence**: High

---

**Claim**: Munin achieved performance within 5-10% of message passing using multiple consistency protocols and release consistency.
**Source**: Implementation and Performance of Munin (SOSP 1991)
**URL**: https://www.bennetyee.org/ucsd-pages/CSE221.W01/carter,bennett,zwaenepoel.implementation_and_performance_of_munin.pdf
**Date**: October 1991
**Excerpt**: "We have achieved performance within 5-10 percent of message passing implementations of the same programs. Munin achieves this level of performance with only minor annotations to the shared memory programs."
**Context**: SOSP 1991 peer-reviewed paper
**Confidence**: High

---

**Claim**: CRIU is integrated into multiple container engines and supports pre-copy and post-copy migration patterns.
**Source**: A Comprehensive Review of Live Migration Technologies (arXiv)
**URL**: https://arxiv.org/html/2512.10979v1
**Date**: 2022
**Excerpt**: "CRIU relies on Linux kernel features available in kernels since version 3.11. It is mainly known for its effective container migration capabilities. It has been integrated into different container engines such as OpenVZ, Podman, Borg... CRIU supports incremental memory checkpointing."
**Context**: Comprehensive survey paper
**Confidence**: High

---

**Claim**: Kubernetes Federation uses host cluster with Templates, Placement, and Overrides for multi-cluster resource distribution.
**Source**: Kubernetes Federation Evolution (Official Kubernetes Blog)
**URL**: https://kubernetes.io/blog/2018/12/12/kubernetes-federation-evolution/
**Date**: December 2018
**Excerpt**: "A Template type holds the base specification of the resource... A Placement type holds the specification of the clusters the resource should be distributed to... An optional Overrides type holds the specification of how the Template resource should be varied in some clusters."
**Context**: Official Kubernetes project blog
**Confidence**: High

---

**Claim**: kGraft never requires stopping the kernel, not even for a short time, using RCU-like approach.
**Source**: SUSE Documentation - Live Patching the Linux Kernel Using kGraft
**URL**: https://documentation.suse.com/sles/12-SP5/html/SLES-all/cha-kgraft.html
**Date**: 2026
**Excerpt**: "The main advantage of kGraft is that it never requires stopping the kernel, not even for a short time period. A kGraft patch is a .ko kernel module... It is inserted into the kernel using the insmod command."
**Context**: Official SUSE documentation
**Confidence**: High

---

**Claim**: MOSIX went proprietary in 2001, leading to openMosix fork which was discontinued in 2008 due to multi-core processors reducing demand.
**Source**: Grokipedia - MOSIX
**URL**: https://grokipedia.com/page/mosix
**Date**: January 2026
**Excerpt**: "In late 2001, the MOSIX project transitioned to proprietary software... The openMosix project was discontinued, with founder Moshe Bar announcing the end in July 2007 and ceasing operations effective March 1, 2008, primarily due to the rise of multi-core processors."
**Context**: Encyclopedia-style article with citations
**Confidence**: High

---

**Claim**: SSI projects failed due to kernel maintenance burden, security vulnerabilities, and incompatibility with distribution kernels.
**Source**: Grokipedia - Single System Image
**URL**: https://grokipedia.com/page/single_system_image
**Date**: January 2026
**Excerpt**: "Upgrades pose additional challenges, risking cluster-wide downtime rather than rolling updates, and porting to new OS versions is labor-intensive, contributing to stalled development in projects like OpenSSI (last stable release in 2005) and Kerrighed (discontinued after ~2010)."
**Context**: Comprehensive SSI overview
**Confidence**: High

---

**Claim**: LegoOS is the first OS designed for hardware resource disaggregation, using splitkernel model with 1.3x-1.7x slowdown vs monolithic Linux.
**Source**: LegoOS: A Disseminated, Distributed OS for Hardware Resource Disaggregation (OSDI 2018)
**URL**: https://www.usenix.org/system/files/osdi18-shan.pdf
**Date**: October 2018
**Excerpt**: "LegoOS is only 1.3x to 1.7x slower with 25% of application working set available as DRAM cache at processor components... LegoOS largely improves resource packing and reduces system mean time to failure."
**Context**: OSDI 2018 Best Paper, peer-reviewed
**Confidence**: High

---

**Claim**: Fuchsia OS uses capability-based security with no concept of a user, no global filesystem, and components resolved/executed on demand via URLs.
**Source**: A Kernel Hacker Meets Fuchsia OS
**URL**: https://a13xp0p0v.tech/2022/05/24/pwn-fuchsia.html
**Date**: May 2022
**Excerpt**: "Fuchsia has no concept of a user. Instead, it is capability-based... Fuchsia even has no global file system. Instead, each component is given its own local namespace to operate... Fuchsia components are identified by URLs and can be resolved, downloaded, and executed on demand."
**Context**: Technical security analysis
**Confidence**: High

---

**Claim**: HelenOS is a research platform for microkernel multiserver OS design, supporting 9 architectures but NOT currently designed as a distributed OS.
**Source**: HelenOS: A Modern Microkernel-Based Multiserver Operating System
**URL**: https://machaddr.substack.com/p/helenos-a-modern-microkernel-based
**Date**: February 2025
**Excerpt**: "HelenOS is currently not designed to be a distributed operating system... HelenOS uses a more traditional kernel design where a single microkernel manages all the CPU cores in a multiprocessor system using shared memory."
**Context**: HelenOS technical overview (citing PhD thesis)
**Confidence**: High

---

**Claim**: LXD supports live migration using CRIU, with clustering for distributed container management. Container migration requires stopping first; live migration primarily for VMs.
**Source**: Ubuntu LXD Documentation
**URL**: https://documentation.ubuntu.com/lxd/latest/howto/instances_migrate/
**Date**: February 2026
**Excerpt**: "When migrating a container, you must stop it first. When migrating a virtual machine, you must either enable Live migration or stop it first... Live migration means migrating an instance to another server while it is running, avoiding any downtime. This method is supported for virtual machines."
**Context**: Official LXD documentation
**Confidence**: High

---

**Claim**: Live patching cannot address all CVEs and has incompatibilities with kernel subcomponents like SystemTap/kprobes.
**Source**: Red Hat Enterprise Linux Documentation
**URL**: https://docs.redhat.com/en/documentation/red_hat_enterprise_linux/8/html/managing_monitoring_and_updating_the_kernel/applying-patches-with-kernel-live-patching_managing-monitoring-and-updating-the-kernel
**Date**: Current
**Excerpt**: "you cannot address all critical or important CVEs... Some incompatibilities exist between kernel live patching and other kernel subcomponents... You must not use the SystemTap or kprobe tool during or after loading a patch."
**Context**: Official Red Hat documentation
**Confidence**: High

---

*Research compiled from 14+ independent searches across academic papers, official documentation, technical blogs, and encyclopedic sources. All citations use [^number^] format referencing sources found during web search operations.*
