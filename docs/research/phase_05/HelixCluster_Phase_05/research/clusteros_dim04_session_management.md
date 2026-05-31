## Facet: Distributed Session Management & Terminal I/O Forwarding

### Key Findings

- **tmux Control Mode** provides a complete text-based protocol for programmatic interaction: commands are wrapped in `%begin`/`%end` blocks, and asynchronous notifications (`%output`, `%session-changed`, `%window-add`, `%layout-change`, etc.) are sent prefixed with `%` [^142^]. This protocol was originally designed by George Nachman for iTerm2 integration and allows full remote control of tmux over any byte stream (including SSH) [^142^].

- **tmux Server Architecture** uses a client-server model with a single server process per user (identified by socket path, default `/tmp/tmux-$UID/default`) [^327^]. The server holds all session state; clients connect via Unix domain sockets and render terminal output or, in control mode, emit the text protocol [^327^]. The server persists even when all clients detach, keeping sessions alive indefinitely [^327^].

- **vasic-digital/tmux is NOT a distributed fork** -- it is a hardened, verified, containerized build of standard tmux with extensive automated testing infrastructure [^137^]. The README explicitly shows this is a single-node optimized build with "test that PASSes carries positive evidence of the feature working" and CodeGraph integration [^137^]. It adds a `tmx` convenience wrapper but no distributed capabilities.

- **Zellij Architecture** uses a client/server model with IPC between them [^135^]. The client spawns a server process that daemonizes. The server holds all terminal session state (open programs, pane/tab layout). When Zellij first starts, the client spawns a new server process and daemonizes it [^135^]. Zellij also supports WebAssembly/WASI plugins and has a built-in web client using WebSockets and xterm.js [^135^] [^332^]. Web clients connect via axum (Rust web server) with separate WebSocket channels for terminal I/O and control messages [^135^].

- **GNU Screen** uses a single-process model where one backend process manages all windows in a session [^329^]. Attach/detach works through FIFOs (named pipes, typically in `/tmp/screens/` or `/run/screens/`) instead of Unix domain sockets [^329^]. Screen internally acts as a full terminal emulator maintaining an internal screen buffer of attributes and characters [^329^].

- **PTY (Pseudo-Terminal) forwarding** works via master/slave pairs: the slave (`/dev/pts/N`) is given to the application (shell, etc.), while the master is held by the multiplexer/shim which reads/writes and forwards over the network [^344^] [^336^]. The iximiuz/ptyme project demonstrates the simplest possible pattern: a shim creates a PTY, forks/exec the target program, then relays master-side bytes over TCP to attach clients [^344^].

- **CRIU (Checkpoint/Restore in Userspace)** can freeze running Linux processes and checkpoint their state to disk [^138^]. For TCP connections, CRIU uses the kernel's `TCP_REPAIR` mode (introduced in Linux 3.5) which allows a privileged process to read/write send/receive queues, get/set sequence numbers, timestamps, and open/close connections without notifying the remote end [^463^]. The `--tcp-established` flag enables checkpointing of active TCP sockets [^461^]. However, TCP migration across hosts with different IPs remains a major challenge [^462^] [^347^].

- **DMTCP (Distributed MultiThreaded CheckPointing)** transparently checkpoints distributed computations in user-space with no kernel modifications [^337^]. It explicitly supports "ptys (pseudo-terminals), terminal modes, ownership of controlling terminals" among many other OS artifacts [^483^]. On 128 distributed cores (32 nodes), checkpoint/restart times are typically 2 seconds [^479^]. The restart algorithm involves: (1) reopen files and recreate PTYs, (2) recreate and reconnect sockets via cluster-wide discovery, (3) fork into user processes, (4) rearrange FDs, (5) restore memory/threads, (6) resume [^479^].

- **Mosh (Mobile Shell)** is built on the State Synchronization Protocol (SSP), a UDP-based protocol that synchronizes abstract state objects between client and server [^338^] [^349^]. SSP uses AES-128 in OCB3 mode for encryption [^349^]. Mosh maintains a "split" terminal emulator with screen state at both client and server, enabling predictive local echo (70% of keystrokes displayed immediately) [^338^]. Roaming works statelessly: "the client sends datagrams to the server with increasing sequence numbers... Every time the server receives an authentic packet from the client with a sequence number higher than any it has previously received, the IP source address of that packet becomes the server's new target" [^349^].

- **EternalTerminal** implements resumable TCP via `BackedReader` and `BackedWriter` classes that track sequence numbers and buffer sent data [^164^]. On reconnect, the `BackedWriter` re-sends bytes from its buffer that the `BackedReader` hasn't acknowledged. ET bootstraps via SSH (like Mosh), then switches to its own encrypted TCP connection [^164^]. ET explicitly supports tmux control mode (`-CC`) since it operates as a raw pseudo-terminal relay, not SSH protocol [^164^].

- **ttyd/gotty** provide web-based terminal access using WebSocket relay. ttyd uses libwebsockets to bridge PTY I/O to WebSocket clients [^358^]. gotty uses xterm.js on the client side with a Go-based WebSocket server that relays TTY output to clients and forwards client input to the TTY [^368^]. Both are commonly used with tmux (`gotty tmux new -A -s gotty`) to enable persistent web-accessible sessions [^368^].

- **libtmux** provides a typed Python API over tmux with object-oriented control: `Server` -> `Session` -> `Window` -> `Pane` hierarchy [^131^]. It supports querying live sessions, sending keys, capturing output, and has a `.cmd()` escape hatch on every object [^131^]. tmuxp builds on libtmux to provide declarative YAML/TOML session definitions [^364^].

- **tmux-resurrect** saves session structure, working directories, pane layouts, and optionally running processes. **tmux-continuum** automates periodic saves (default 15 minutes) and can auto-restore on server start [^376^] [^382^]. Together they enable session persistence across reboots but NOT live migration -- sessions are only restored from disk snapshots.

- **dtach/abduco** provide minimal session management primitives: just detach/reattach of a single program, no window multiplexing [^381^] [^386^]. abduco (Latin: "to lead away") focuses solely on session management; it pairs with dvtm for multiplexing [^386^]. These are useful reference implementations for the minimal primitives needed.

- **SSH Multiplexing** (`ControlMaster`/`ControlPath`/`ControlPersist`) reuses a single TCP connection for multiple SSH sessions via a Unix domain socket [^359^] [^369^]. The first connection becomes the "control master"; subsequent connections attach as new channels within the existing TCP connection [^361^]. `ControlPersist` keeps the master alive after the last session closes [^369^]. This is relevant for cluster session management because it shows how to multiplex multiple logical sessions over a single transport.

- **Session Affinity vs Session Migration** represents a fundamental architectural tradeoff: affinity (sticky sessions) routes all requests from a client to the same server, while migration moves session state between servers [^391^] [^392^]. Affinity creates uneven load distribution and failover problems [^392^]. For terminal sessions, true migration (moving the PTY, process state, and terminal emulator state) is the more powerful but significantly harder approach.

- **Terminal Emulation** involves parsing ANSI/VT100 escape sequences via state machines: ESC sequences for cursor movement, color, scrolling regions, mode changes, etc. [^393^] [^394^]. xterm.js (used in VSCode, Zellij web client, ttyd, gotty) implements this in JavaScript/TypeScript for browser environments [^401^]. The xterm control sequences document runs 100+ pages, demonstrating the complexity of full terminal emulation [^393^].

- **Process Migration Techniques** include: (1) **Pre-copy**: iteratively copy memory pages while VM runs, then stop-and-copy final dirty pages -- minimizes downtime but may transfer pages multiple times [^383^] [^384^]. (2) **Post-copy** (lazy): move minimal state, start VM on destination, fault in pages on demand -- very short downtime but high "perceivable downtime" from page faults [^383^] [^387^]. (3) **Hybrid**: combines both approaches [^378^].

- **NO existing technology provides distributed tmux sessions across a cluster** -- confirmed by exhaustive searching. tmate provides terminal sharing but uses centralized proxy servers, not peer-to-peer distribution [^433^]. Zellij's web client connects to a single server per machine [^135^]. All existing multiplexers are fundamentally single-node designs.

- **tmate Architecture** provides relevant patterns: it's a tmux fork that connects to tmate.io proxy servers (SSH servers in SF, NYC, London, Singapore) [^425^]. The proxy acts as a reverse SSH tunnel accessible from anywhere, tolerating NATs and IP changes [^428^]. This proxy-mediated pattern could be adapted for cluster session management.

---

### Major Players & Sources

| Entity | Role/Relevance |
|--------|---------------|
| **tmux (OpenBSD)** | The dominant terminal multiplexer. Client-server via Unix domain sockets. Control mode (-C) provides the programmable interface. Source: `github.com/tmux/tmux` [^142^] |
| **Zellij** | Rust-based tmux alternative with WebAssembly plugins and web client. Client-server IPC architecture. Source: `github.com/zellij-org/zellij` [^135^] |
| **GNU Screen** | Legacy single-process multiplexer using FIFOs. Source: `git.savannah.gnu.org/cgit/screen.git` [^329^] |
| **vasic-digital/tmux** | Hardened single-node build of tmux with test infrastructure. NOT distributed. Source: `github.com/vasic-digital/tmux` [^137^] |
| **CRIU** | Linux process checkpoint/restore. TCP_REPAIR for connection migration. Source: `github.com/checkpoint-restore/criu` [^138^] |
| **DMTCP** | User-space distributed checkpointing for clusters. Handles PTYs explicitly. Source: `sourceforge.net/projects/dmtcp/` [^337^] |
| **Mosh** | UDP-based mobile shell with SSP protocol. Pattern for roaming/resilient transport. Source: `mosh.org`, `github.com/mobile-shell/mosh` [^338^] |
| **EternalTerminal** | Resumable TCP with BackedReader/BackedWriter. Explicitly supports tmux -CC. Source: `eternalterminal.dev` [^164^] |
| **ttyd** | C-based web terminal (WebSocket + PTY). Source: `github.com/tsl0922/ttyd` [^358^] |
| **gotty** | Go-based web terminal sharing. Source: `github.com/yudai/gotty` [^368^] |
| **libtmux / tmuxp** | Python API and declarative session manager for tmux. Source: `github.com/tmux-python/libtmux` [^131^] |
| **xterm.js** | Browser terminal emulator (used by VSCode, Zellij web). Source: `github.com/xtermjs/xterm.js` [^401^] |
| **tmate** | tmux fork for instant terminal sharing via proxy servers. Source: `github.com/tmate-io/tmate` [^433^] |
| **dtach / abduco** | Minimal session detach/reattach primitives. Source: `codeberg.org/unixchad/abduco` [^377^] |

---

### Trends & Signals

- **Web-based terminal access is becoming standard**: Zellij's built-in web client [^135^], ttyd/gotty for web terminals [^358^] [^368^], and xterm.js as the de facto browser terminal emulator [^401^] all point toward browser-based terminal access as a first-class interface.

- **Rust is emerging as the implementation language for new multiplexers**: Zellij is written in Rust and uses axum/tokio for its web server [^135^]. This trend suggests Rust's async ecosystem is well-suited for terminal I/O forwarding.

- **Resumable transport protocols are a solved problem at the application layer**: Mosh's SSP [^338^] and EternalTerminal's BackedReader/BackedWriter [^164^] demonstrate two different approaches to surviving network disruptions -- UDP-based state sync vs. TCP sequence number tracking.

- **Checkpoint/restore is maturing but cross-host TCP migration remains fragile**: CRIU's TCP_REPAIR works well for same-host restore but cross-host migration with different IPs fails [^462^]. DMTCP handles distributed checkpointing better but is less maintained [^337^].

- **Control mode is the key integration point**: The `-C` flag enables any external tool to observe and control tmux sessions [^142^]. ET explicitly supports this [^164^], and iTerm2's original integration proved the concept. A distributed session manager should leverage control mode as the primary API.

---

### Controversies & Conflicting Claims

- **CRIU cross-host TCP migration**: Some sources claim CRIU can migrate TCP connections between hosts [^343^], while the CRIU GitHub issues show this fails in practice when IPs differ [^462^] [^347^]. The kernel's TCP_REPAIR mode saves socket state but the remote endpoint will time out if the checkpoint/restore window is too long [^461^]. Resolution: migration works only with identical IPs (containers) or very fast transfer.

- **Pre-copy vs Post-copy migration**: Pre-copy minimizes downtime but may cause excessive network traffic from re-transferring dirty pages [^384^]. Post-copy minimizes total data transfer but causes high "perceivable downtime" from page faults [^387^]. No single approach dominates; hybrid methods attempt to combine benefits [^378^].

- **Session affinity vs stateless design**: Modern cloud architecture recommends externalized session storage (Redis, JWT) over sticky sessions [^392^]. However, terminal sessions have inherent locality (PTY state, process state, terminal emulator state) that cannot easily be externalized. This creates tension between load balancing and session migration.

- **Mosh's screen-diff approach vs byte-stream approach**: Mosh's state synchronization approach (synchronizing screen state rather than byte streams) enables roaming and local echo but breaks applications that depend on scrollback buffer accuracy [^340^]. The authors recommend using screen/tmux as "pagers for the entire terminal" for scrollback [^339^].

---

### Recommended Deep-Dive Areas

1. **tmux Control Mode as Distributed API**: The control mode protocol (`%begin`/`%end`, `%output`, `%session-changed`) provides a complete event stream and command interface. A distributed session manager should wrap this protocol, forwarding events across cluster nodes. The protocol is text-based and designed for parsing over network connections [^142^].

2. **PTY Master/Slave Pair Forwarding Over Network**: The iximiuz/ptyme project [^344^] and SSH's PTY allocation model [^336^] demonstrate the core pattern: create PTY on remote node, forward master-side bytes over reliable transport. This is the fundamental I/O primitive for distributed terminal sessions.

3. **Mosh's SSP Protocol for Resilient Transport**: The SSP protocol's approach to state synchronization, roaming, and UDP-based transport [^338^] [^349^] could be adapted for cluster-internal I/O forwarding where nodes may join/leave dynamically.

4. **CRIU/DMTCP for Session Migration**: While full live migration is hard, partial migration (checkpoint tmux server state, restore on new node) could enable session mobility across cluster nodes. DMTCP's explicit PTY support [^479^] makes it more suitable than CRIU for terminal sessions.

5. **Zellij's IPC and Plugin Architecture**: Zellij's client-server IPC model [^135^] and WebAssembly plugin system [^332^] provide a reference for how to structure a multi-backend session manager with extensible functionality.

6. **WebSocket-Based Terminal Streaming**: The ttyd/gotty/xterm.js pattern [^358^] [^368^] [^401^] provides a proven architecture for browser-based terminal access that could be extended to proxy to distributed backend sessions.

7. **EternalTerminal's BackedReader/BackedWriter**: This approach to resumable TCP [^164^] is directly applicable to maintaining terminal I/O connections across network disruptions in a cluster environment.

---

### Raw Evidence Log

#### Finding 1: tmux Control Mode Protocol Documentation
```
Claim: tmux control mode provides a complete text protocol with %begin/%end command wrapping and %prefixed asynchronous notifications.
Source: tmux/tmux GitHub Wiki - Control Mode
URL: https://github.com/tmux/tmux/wiki/Control-Mode
Date: 2020-09-01
Excerpt: "Control mode is a special mode that allows a tmux client to be used to talk to tmux using a simple text-only protocol. It was designed and written by George Nachman and allows his iTerm2 terminal to interface with tmux and show tmux panes using the iTerm2 UI. A control mode client is just like a normal tmux client except that instead of drawing the terminal, tmux communicates using text."
Context: Official tmux wiki documentation of the control mode feature.
Confidence: high
```

#### Finding 2: tmux Control Mode Notifications List
```
Claim: Control mode emits specific notifications for all session/window/pane changes.
Source: tmux(1) Linux manual page
URL: https://man7.org/linux/man-pages/man1/tmux.1.html
Date: 2026-05-24
Excerpt: "%output pane-id value - A window pane produced output. value escapes non-printable characters and backslash as octal \\xxx... %session-changed session-id name - The client is now attached to the session with ID session-id, which is named name. %sessions-changed - A session was created or destroyed."
Context: Official man page listing all control mode notifications.
Confidence: high
```

#### Finding 3: tmux Server Architecture
```
Claim: tmux uses a client-server model where the server is forked to the background automatically and persists beyond client detachment.
Source: Tao of Tmux documentation
URL: https://tao-of-tmux.readthedocs.io/en/latest/manuscript/04-server.html
Date: Unknown
Excerpt: "tmux uses a client-server model, but the server is forked to the background for you... The server part of tmux is how your sessions can stay alive, even after your client is detached. You can detach a tmux session from an SSH server and reconnect later."
Context: Educational documentation explaining tmux architecture.
Confidence: high
```

#### Finding 4: vasic-digital/tmux is NOT distributed
```
Claim: vasic-digital/tmux is a hardened, verified, containerized single-node build of tmux, not a distributed fork.
Source: vasic-digital/tmux GitHub repository
URL: https://github.com/vasic-digital/tmux
Date: 2026-05-29
Excerpt: "vasic-digital tmux — optimized + verified containerized build... This repo follows the vasic-digital anti-bluff covenant: every test that PASSes carries positive evidence of the feature working..."
Context: README of the vasic-digital/tmux repository explicitly showing this is a build/test harness project.
Confidence: high
```

#### Finding 5: Zellij Client-Server Architecture
```
Claim: Zellij uses a client/server architecture with IPC between them; the client spawns a server process that daemonizes.
Source: Zellij blog - "Terminal sessions you can bookmark"
URL: https://poor.dev/blog/building-zellij-web-terminal/
Date: 2025-08-18
Excerpt: "Keeping sessions alive in the background involves a client/server architecture. The client runs in the user's terminal like any other program and communicates over IPC with a server holding the state of the terminal session (open programs, pane and tab layout, etc.) When Zellij first starts, the client spawns a new server process and daemonizes it so that it keeps running independently in the background."
Context: Official Zellij blog post describing their architecture.
Confidence: high
```

#### Finding 6: GNU Screen Internals
```
Claim: GNU Screen uses a single backend process per session and communicates with attach clients via FIFOs (named pipes).
Source: StackOverflow - "How does GNU screen actually work"
URL: https://stackoverflow.com/questions/27727176/how-does-gnu-screen-actually-work
Date: 2017-10-10
Excerpt: "GNU screen uses one process for a whole session, which may have multiple screens... When you run screen -r to attach to it, it connects through a named pipe - a FIFO in the file system (on my gnu screen config, they are stored in /tmp/screens) instead of the unix socket I used, but same principle - it gets the state dumped to the screen, then carries on forwarding info back and forth."
Context: Detailed answer explaining GNU Screen's internal architecture.
Confidence: high
```

#### Finding 7: PTY Forwarding Pattern
```
Claim: PTY forwarding involves a shim that creates a pseudo-terminal, forks the target program with slave-side FDs, and relays master-side bytes over the network.
Source: iximiuz/ptyme GitHub repository
URL: https://github.com/iximiuz/ptyme
Date: 2019-08-11
Excerpt: "The shim creates a pseudoterminal and fork/exec-s a given executable binding its standard streams to the slave side of the pseudoterminal pair. At the same time the parent process keeps reading and writing the master end of the pair. The parent process also starts listening on TCP port. Each byte read from an incoming connection is then forwarded to a master side of the terminal."
Context: Minimal example project demonstrating PTY-based attach/exec pattern.
Confidence: high
```

#### Finding 8: CRIU TCP_REPAIR Mechanism
```
Claim: CRIU uses Linux's TCP_REPAIR socket option (kernel 3.5+) to checkpoint and restore TCP connection state without notifying the remote peer.
Source: LWN.net - "TCP connection repair"
URL: https://lwn.net/Articles/495304/
Date: Unknown
Excerpt: "With Pavel's patch, that support is available to suitably privileged processes. To dig into the internals of an active network connection, user space must put the associated socket into a new 'repair mode.' That is done with the setsockopt() system call, using the new TCP_REPAIR option... Once the socket is in repair mode, it can be manipulated in a number of ways. One of those is to read the contents of the send and receive queues."
Context: Technical article on Linux kernel's TCP repair feature.
Confidence: high
```

#### Finding 9: CRIU Cross-Host TCP Migration Limitation
```
Claim: CRIU TCP migration across hosts with different IPs fails in practice.
Source: CRIU GitHub Issue #2457
URL: https://github.com/checkpoint-restore/criu/issues/2457
Date: 2024-07-31
Excerpt: "It worked well in the single host machine. However, the thing is, Although I used the option properly (on the same way), I couldn't reproduce your Demo especially on diffrent hosts... the process restores successfully, but they cannot communicate."
Context: User bug report with reproduction steps confirming cross-host TCP migration issues.
Confidence: high
```

#### Finding 10: DMTCP Supports PTYs and Distributed Checkpointing
```
Claim: DMTCP transparently checkpoints distributed computations including PTYs, with checkpoint/restart times of ~2 seconds on 32 nodes.
Source: DMTCP IPDPS 2009 paper
URL: https://www.ccs.neu.edu/home/gene/papers/ipdps09.pdf
Date: 2009
Excerpt: "DMTCP automatically accounts for fork, exec, ssh, mutexes/semaphores, TCP/IP sockets, UNIX domain sockets, pipes, ptys (pseudo-terminals), terminal modes, ownership of controlling terminals, signal handlers, open file descriptors, shared open file descriptors, I/O (including the readline library), shared memory (via mmap), parent-child process relationships, pid virtualization, and other operating system artifacts."
Context: Peer-reviewed academic paper on DMTCP.
Confidence: high
```

#### Finding 11: Mosh SSP Protocol Design
```
Claim: Mosh's State Synchronization Protocol runs over UDP, uses AES-128-OCB3 encryption, and enables stateless roaming by tracking the highest-seen sequence number.
Source: mosh.org technical info
URL: https://mosh.org/
Date: 2015-05-31
Excerpt: "Roaming with SSP becomes easy: the client sends datagrams to the server with increasing sequence numbers, including a 'heartbeat' at least once every three seconds. Every time the server receives an authentic packet from the client with a sequence number higher than any it has previously received, the IP source address of that packet becomes the server's new target for its outgoing packets."
Context: Official Mosh website technical documentation.
Confidence: high
```

#### Finding 12: EternalTerminal Architecture
```
Claim: EternalTerminal implements resumable TCP via BackedReader/BackedWriter sequence tracking, bootstraps via SSH, and supports tmux control mode.
Source: EternalTerminal documentation - "How it works"
URL: https://eternalterminal.dev/howitworks/
Date: Unknown
Excerpt: "BackedReader class that keeps track of the number of bytes read (called the sequence number) and, upon reconnect, informs the other party of the sequence number. The BackedWriter class keeps an encrypted buffer of the last N bytes sent and the sequence number... ET does not implement any of the SSH protocol. Instead ET simply creates a psuedo-terminal on the server side... Works with tmux control center."
Context: Official ET architecture documentation.
Confidence: high
```

#### Finding 13: ttyd + tmux Web Terminal Architecture
```
Claim: ttyd bridges PTY to WebSocket, and is commonly combined with tmux for persistent browser-based terminals.
Source: Karan Sharma blog - "A Web Terminal for My Homelab"
URL: https://mrkaran.dev/posts/web-terminal-homelab/
Date: 2026-03-05
Excerpt: "Browser -> Caddy -> ttyd -> nsenter -> su - karan -> tmux(main)... ttyd handles terminal-over-websocket behavior well. -m 1 enforces a single active client, which avoids cross-tab resize contention."
Context: Practical blog post showing production web terminal architecture.
Confidence: high
```

#### Finding 14: gotty Architecture
```
Claim: gotty uses xterm.js for browser terminal emulation and a Go WebSocket server to relay TTY I/O.
Source: yudai/gotty GitHub repository
URL: https://github.com/yudai/gotty
Date: 2015-08-16
Excerpt: "GoTTY uses xterm.js and hterm to run a JavaScript based terminal on web browsers. GoTTY itself provides a websocket server that simply relays output from the TTY to clients and receives input from clients and forwards it to the TTY."
Context: Official gotty README describing its architecture.
Confidence: high
```

#### Finding 15: libtmux Python API
```
Claim: libtmux provides typed Python objects (Server, Session, Window, Pane) for programmatic tmux control.
Source: tmux-python/libtmux GitHub repository
URL: https://github.com/tmux-python/libtmux
Date: 2016-05-22
Excerpt: "libtmux is a typed Python API over tmux, the terminal multiplexer. Stop shelling out and parsing tmux ls. Instead, interact with real Python objects: Server, Session, Window, and Pane."
Context: Official libtmux README.
Confidence: high
```

#### Finding 16: tmux-resurrect and tmux-continuum
```
Claim: tmux-resurrect saves session layouts and tmux-continuum automates periodic saves with auto-restore capability.
Source: ArcoLinux - "Reconstructing Tmux Sessions After Restarts"
URL: https://arcolinux.com/everything-you-need-to-know-about-tmux-reconstructing-tmux-sessions-after-restarts/
Date: Unknown
Excerpt: "tmux-resurrect saves all the little details from your tmux environment so it can be completely restored after a system restart... tmux-continuum works in tandem with tmux-resurrect plugin to automate the saving and restoration of tmux environment."
Context: Tutorial article on tmux session persistence plugins.
Confidence: high
```

#### Finding 17: dtach/abduco Minimal Session Management
```
Claim: dtach and abduco provide only session detach/reattach primitives without window multiplexing.
Source: ArchWiki - dtach
URL: https://wiki.archlinux.org/title/Dtach
Date: 2025-12-20
Excerpt: "dtach is a tiny program that emulates the detach feature of screen, allowing you to run a program in an environment that is protected from the controlling terminal and attach to it later."
Context: Arch Linux community documentation.
Confidence: high
```

#### Finding 18: SSH Multiplexing Internals
```
Claim: SSH ControlMaster creates a Unix domain socket that subsequent SSH processes use to open new channels within an existing TCP connection.
Source: OpenSSH/Cookbook/Multiplexing
URL: https://en.wikibooks.org/wiki/OpenSSH/Cookbook/Multiplexing
Date: 2011-02-18
Excerpt: "The OpenSSH client supports multiplexing its outgoing connections, since version 3.9 (August 18, 2004), using the ControlMaster, ControlPath and ControlPersist configuration directives... ControlMaster determines whether ssh(1) will listen for control connections and what to do about them. ControlPath sets the location for the control socket used by the multiplexed sessions."
Context: Official OpenSSH cookbook documentation.
Confidence: high
```

#### Finding 19: Session Affinity Tradeoffs
```
Claim: Sticky sessions (session affinity) create uneven load distribution and complicate failover.
Source: Connected.app - "Sticky Sessions (Session Affinity)"
URL: https://www.connected.app/library/sticky-sessions-i5u9d2s
Date: 2025-12-11
Excerpt: "Sticky sessions are a confession that your application has a memory problem -- it remembers things it shouldn't, in places it shouldn't remember them... Sticky sessions limit everything load balancers are supposed to do well: Load distribution becomes uneven. Scaling down becomes complicated. Failover becomes painful."
Context: Technical article on load balancing patterns.
Confidence: high
```

#### Finding 20: Process Migration Pre-copy vs Post-copy
```
Claim: Pre-copy migration iteratively transfers dirty pages while VM runs; post-copy transfers minimal state then faults pages on demand.
Source: UMass lecture notes on VM Migration
URL: https://lass.cs.umass.edu/~shenoy/courses/677content/notes/spring23/Lec11_notes.pdf
Date: Unknown
Excerpt: "Pre-copy Migration: Enable dirty page tracking. Copy all memory pages to destination. Copy memory pages which were changed during the previous copy. Repeat until the number of memory pages is small. Stop VM, copy rest of memory pages at destination and start VM... Post-copy Migration: Stop VM and move non-memory VM states to destination. Start executing on new machine. In case of page faults in the new VM, copy the page from the source machine."
Context: University lecture notes on VM migration techniques.
Confidence: high
```

#### Finding 21: tmate Proxy Architecture
```
Claim: tmate uses centralized SSH proxy servers to enable NAT traversal and terminal sharing.
Source: cloudthrill.ca - "Tmate, the perfect Instant terminal sharing Tool"
URL: https://cloudthrill.ca/how-to-remotely-share-your-terminal
Date: 2024-07-05
Excerpt: "The SSH public server (ssh.tmate.to) resolves 4 IPs spread across San Francisco, New York, London, and Singapore which makes it highly available. When the fastest server is elected, the remote tmate(tmux) daemon sends back the connection strings to the session running tmate. Whenever a disconnection happens between a host and remote tmate, or remote tmate & proxy, or proxy & master, sessions reconnect automatically and sync back."
Context: Tutorial explaining tmate's proxy-based architecture.
Confidence: high
```

#### Finding 22: xterm.js Architecture
```
Claim: xterm.js implements terminal emulation in the browser and connects to backends via WebSocket, used by VSCode, Zellij web client, and web terminals.
Source: Presidio technical blog
URL: https://www.presidio.com/technical-blog/building-a-browser-based-terminal-using-docker-and-xtermjs/
Date: 2023-06-26
Excerpt: "Visual Studio Code's terminal is a good example of a web terminal. It is built on ElectronJS and features an integrated terminal with XtermJS, which allows users to access the terminal directly from within VS Code... Xterm-addon-attach helps to attach it to a web socket."
Context: Technical blog on building browser-based terminals.
Confidence: high
```

#### Finding 23: Terminal Emulation Complexity
```
Claim: Full xterm/VT100 terminal emulation involves complex state machines for parsing escape sequences covering cursor control, colors, scrolling, character sets, and mouse.
Source: XTerm Control Sequences reference
URL: https://invisible-island.net/xterm/ctlseqs/ctlseqs.pdf
Date: Unknown
Excerpt: [60+ pages of escape sequence definitions including] "ESC [ Pn A Cursor Up, ESC [ Pn B Cursor Down, ESC [ Ps ;...; Ps m Select Graphic Rendition..."
Context: Official xterm control sequences reference document.
Confidence: high
```

#### Finding 24: Zellij Web Server Technical Details
```
Claim: Zellij's web server uses axum (Rust), rustls for HTTPS, tokio-tungstenite for WebSockets, and separates terminal/control channels.
Source: Zellij blog - web client architecture
URL: https://poor.dev/blog/building-zellij-web-terminal/
Date: 2025-08-18
Excerpt: "We chose axum as our webserver because we liked its mix-and-match approach... it provides a 'native' websocket implementation (using tokio-tungstenite under the hood)... two websocket channels are established: a terminal channel and a control subchannel. The terminal channel is used by the server to send STDOUT bytes to the client and by the client to send STDIN bytes to the server."
Context: Official Zellij blog post on web client implementation.
Confidence: high
```

#### Finding 25: DMTCP Restart Algorithm
```
Claim: DMTCP restart involves 6 steps: reopen files/PTYs, recreate sockets via discovery, fork into user processes, rearrange FDs, restore memory/threads, refill kernel buffers.
Source: DMTCP IPDPS 2009 paper
URL: https://people.csail.mit.edu/jansel/papers/2009ipdps-dmtcp.pdf
Date: 2009
Excerpt: "1) Reopen files and recreate ptys. 2) Recreate and reconnect sockets. 3) Fork into user processes. 4) Rearrange FDs for user process. 5) Restore memory and threads. 6) Refill kernel buffers."
Context: Peer-reviewed academic paper.
Confidence: high
```
