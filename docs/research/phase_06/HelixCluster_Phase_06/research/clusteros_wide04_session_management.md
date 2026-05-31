# Facet: Session Management, Terminal Multiplexing & Remote I/O

## Key Findings

### tmux Architecture & Control Mode

- **tmux uses a client-server architecture** where a persistent server process manages all sessions, windows, and panes, while clients connect via Unix domain sockets to view and interact with sessions [^36^]. When an SSH connection drops, only the client terminates; the server and all running processes continue unaffected [^36^].

- **tmux Control Mode** (`tmux -C` or `tmux -CC`) is a special interface designed for programmatic interaction rather than human interaction. It was designed by George Nachman to allow iTerm2 to interface with tmux [^142^]. Control mode clients accept standard tmux commands and return output wrapped in `%begin`/`%end` guard lines, plus asynchronous `%`-prefixed notifications including `%output %<pane_id> <data>`, `%window-add`, `%session-changed`, `%layout-change`, etc. [^142^] [^33^]

- **Control Mode output format**: Pane output is sent as `%output %<pane_id> <escaped_data>` where characters below ASCII 32 and `\` are replaced with octal equivalents. This allows external programs to receive real-time terminal output and send commands via stdin [^142^].

- **Session groups** in tmux allow multiple clients to share the same set of windows while keeping independent focus — effectively `screen -x` equivalent. Sessions in the same group share windows via `tmux new-session -t <target_session> -s <new_name>` [^127^] [^133^]

- **Multiple clients can attach to the same tmux session** simultaneously, but by default they share the same "current window" focus. Session groups solve this limitation [^133^] [^134^]

### vasic-digital/tmux Fork

- **vasic-digital/tmux is a reproducible, hardened build of tmux** with containerized compilation, jemalloc support, OOM-protection helper, and an 8-test verification gate [^109^]. It runs natively on any Linux host with podman/docker and supports macOS via a transparent bridge into the podman machine VM [^109^].

- The fork provides a `tmx` command that coexists with system `tmux` — the system command stays untouched. The project follows a "vasic-digital anti-bluff covenant" where every test carries positive evidence [^109^].

- **Key additions**: Multi-line copy+paste with Claude Code TUI support, OS clipboard integration via `@clip-read` user option, SQLite-based workable-items tracking, DOCX/HTML/PDF export pipeline, and CodeGraph MCP server integration for Claude Code, OpenCode, Kimi CLI, Crush, and Qwen Code [^109^].

- **Not distributed**: The fork does NOT extend tmux for distributed cluster operation. It is a hardened, verified build pipeline with enhanced clipboard and documentation features, but the core tmux architecture remains single-node [^109^].

### GNU Screen Architecture

- **GNU Screen uses a single-process model** where one backend process manages a whole session (which may have multiple screens/windows) [^116^]. It communicates with attach clients through named pipes (FIFOs) stored in `/tmp/screens/` [^116^].

- Screen is significantly smaller than tmux (~280KB statically compiled vs ~2MB for tmux with dependencies), making it valuable for embedded/resource-constrained environments [^31^]. However, it has limited development activity and a shrinking community [^32^].

- Screen lacks a plugin ecosystem, has inconsistent Unicode/emoji support, and offers only basic scripting compared to tmux [^32^]. Its primary advantage is ubiquity on legacy systems.

### Zellij Architecture

- **Zellij is a terminal workspace written in Rust** with a client-server architecture: the client runs in the user's terminal and communicates over IPC with a background server that holds session state [^108^]. When Zellij starts, the client spawns and daemonizes a server process [^108^].

- **Zellij uses a WASM plugin system** allowing plugins in any language compiling to WebAssembly [^93^]. It has a built-in web client (since v0.43.0) with a web server per machine that translates between browser WebSockets and Zellij server IPC channels [^108^] [^121^].

- **Zellij's web terminal** uses a URL scheme where sessions are bookmarkable (e.g., `https://127.0.0.1/backend-code`). Two WebSocket channels are established: a terminal channel (STDOUT/STDIN) and a control subchannel (window resizes, configuration) [^108^].

- **Zellij pipes** are unidirectional communication channels to/from plugins that can be started from CLI, keybindings, or other plugins, supporting backpressure and broadcast patterns [^136^].

- **Zellij is ~50MB binary** (Rust static linking), compared to tmux ~2MB and screen ~280KB [^31^].

### ttyd / GoTTY / Web Terminal Sharing

- **ttyd** is a C implementation built on libwebsockets and libuv with xterm.js for browser-side terminal emulation. It supports WebSocket communication, SSL (OpenSSL/Mbed TLS), basic authentication, ZMODEM file transfers, Sixel image output, and CJK/IME support [^35^] [^37^] [^39^] [^43^].

- **GoTTY** (yudai/gotty) is a Go-based terminal sharing tool that inspired ttyd. It uses hterm/xterm.js in the browser and provides a WebSocket server that relays TTY output to clients and forwards client input to the TTY [^167^]. GoTTY starts a new process per client by default; for sharing, tmux/screen integration is recommended [^167^].

- **Wetty** is a Node.js-based web terminal (Web + TTY) using xterm.js and WebSockets instead of Ajax for lower latency [^67^] [^68^]. It provides terminal access via HTTP/HTTPS without client software installation.

- All web terminal tools share a common architecture: a WebSocket server relays PTY I/O between a server-side command and a browser-based terminal emulator (xterm.js/hterm) [^167^] [^108^].

### SSH Session Multiplexing (ControlMaster)

- **SSH ControlMaster** enables connection multiplexing where one SSH connection (the "master") is shared by subsequent connections to the same host via a Unix domain socket (control socket) [^59^] [^63^].

- Configuration: `ControlMaster auto`, `ControlPath ~/.ssh/sockets/%r@%h:%p`, `ControlPersist 4h` [^59^]. The first connection pays the full handshake cost; subsequent connections reuse the existing TCP connection and open new channels in milliseconds [^59^].

- Control commands via `ssh -O`: `check`, `forward`, `stop`, `exit` allow explicit management of the master lifecycle [^59^].

- SSH multiplexing pairs naturally with terminal multiplexers: use ControlMaster for connection efficiency, tmux/screen on the remote for session persistence [^63^].

### dtach / abduco — Lightweight Session Management

- **abduco** (Latin: "to lead away") is a minimal session management tool providing only detach/reattach functionality without window multiplexing [^65^] [^66^]. Written by Marc Andre Tanner in 2014. Sessions stored in Unix domain sockets at `$ABDUCO_SOCKET_DIR/abduco` or `/tmp/abduco/$USER` [^66^].

- **dtach** provides similar minimal session detach/reattach. Both are designed to be paired with terminal multiplexers like dvtm for window management [^31^].

- abduco pairs with **dvtm** (dynamic virtual terminal manager) — dvtm provides tiling window management for the console, reusing concepts from dwm [^139^] [^145^]. Together they form a simpler/cleaner alternative to tmux/screen [^62^].

### Mosh — Mobile Shell with State Synchronization

- **Mosh** uses the **State Synchronization Protocol (SSP)**, a new UDP-based protocol that securely synchronizes client and server state across IP address changes and intermittent connectivity [^94^] [^95^].

- **Two key contributions**: (1) SSP for state synchronization over UDP with roaming support, (2) Speculative local echo — the client maintains a copy of terminal state and predicts keystroke effects, rendering 70% of keystrokes immediately [^95^].

- **SSP architecture**: Three modules — cryptographic module (AES encryption), datagram layer (UDP), and transport layer (diff-based state synchronization). Mosh uses SSH for initial authentication then switches to its own UDP-based protocol [^94^].

- **Mosh operates at a different layer than SSH**: Instead of transmitting an octet stream, it synchronizes terminal screen states between client and server. This enables predictive local echo and makes "Control-C" always work within one RTT [^95^].

- Median keystroke latency on commercial 3G: Mosh 4.8ms vs SSH 503ms [^94^]. Mosh errs in predictions only 0.9% of the time, correcting within one RTT [^94^].

- Mosh does NOT support X forwarding, port forwarding, or non-interactive SSH uses. It requires UDP ports 60000-61000 to be accessible [^99^].

### Eternal Terminal

- **Eternal Terminal (ET)** implements **Eternal TCP** — a layer between applications and Unix TCP sockets that makes connections robust to TCP disconnects including roaming and connection failure [^100^] [^104^].

- **BackedReader/BackedWriter**: The reader tracks a sequence number; upon reconnect, informs the other party. The writer keeps an encrypted buffer of last N bytes sent. On reconnect, it resends the difference between sequence numbers [^104^].

- ET uses SSH for initial connection and authentication, then the server creates a session password and starts an ETServer process. The SSH connection is closed and ET continues over its own protocol [^104^].

- ET **supports tmux control mode** (`tmux -CC`), distinguishing it from Mosh which does not support native scrolling or tmux control mode [^100^] [^102^].

- ET maintains tmux sessions even when the TCP connection dies, resuming quickly without waiting for SSH timeout + re-attach cycle [^100^].

### Terminal I/O Forwarding & PTY Architecture

- **Pseudo terminals (PTYs)** are pairs of character devices: a master and a slave. The slave provides a TTY-compatible interface; the master is manipulated by another process. Anything written to the master appears as input to the slave, and vice versa [^119^].

- **Telnetd model**: The classic remote terminal approach — telnetd forwards data from the network connection to the master side of the PTY, and data from the shell (via PTY slave) back to the remote client [^120^]. This pattern is the foundation for all terminal forwarding.

- **libtmux** provides a typed Python API over tmux with object hierarchy: `Server` -> `Session` -> `Window` -> `Pane`. It enables programmatic control, pane output capture (`capture_pane`), and key injection (`send_keys`) [^131^] [^126^].

### Process Migration & Cluster Computing

- **CRIU (Checkpoint/Restore In Userspace)** can freeze a running container/application and checkpoint its state to disk for restoration elsewhere [^138^] [^147^]. Used by Docker, Podman, Kubernetes, LXC/LXD for live migration.

- **Google's Borg** uses CRIU for task migration at scale: checkpoint, transfer encrypted/compressed state to distributed storage, restore on new machine. gRPC automatically reconnects transparently [^141^].

- **DMTCP** provides transparent checkpointing for cluster computations with distributed coordination, supporting MPI and socket restoration through wrapper-based interception [^172^].

- **MOSIX** is a Linux kernel extension providing transparent process migration for cluster computing with adaptive load balancing, memory ushering, and file I/O optimization [^174^].

### Session Managers & Automation Ecosystem

- **tmuxinator** is a Ruby-based declarative session manager using YAML configurations to define windows, panes, layouts, and startup commands [^114^] [^110^].

- **tmuxp** is a Python-based alternative using YAML/TOML, built on top of libtmux [^111^] [^117^].

- **smug** is a Go-based session manager with similar YAML-based configuration [^112^].

---

## Major Players & Sources

| Entity | Role/Relevance |
|--------|----------------|
| **tmux (OpenBSD/Nicholas Marriott)** | The dominant terminal multiplexer. Client-server architecture, control mode, extensive plugin ecosystem (TPM). Source of truth for terminal multiplexing [^36^] [^142^]. |
| **vasic-digital/tmux** | Hardened, verified build pipeline for tmux with containerized compilation, jemalloc, OOM protection, enhanced clipboard. NOT distributed. Our base project [^109^]. |
| **GNU Screen** | Legacy terminal multiplexer, ubiquitous on Unix systems. Single-process model. Limited active development [^32^] [^116^]. |
| **Zellij** | Modern Rust-based terminal workspace. WASM plugin system, built-in web client, discoverable UI. Growing but immature ecosystem [^93^] [^108^]. |
| **ttyd (tsl0922)** | C-based terminal sharing over Web. Built on libwebsockets, xterm.js. Fast, lightweight, feature-rich [^43^]. |
| **GoTTY (yudai)** | Go-based terminal sharing. Predecessor/inspiration for ttyd. Simpler feature set [^167^]. |
| **Wetty** | Node.js web terminal. Uses xterm.js + WebSockets [^67^]. |
| **Mosh** | Mobile shell with UDP-based state synchronization. Predictive local echo. Academic research from MIT CSAIL [^94^] [^95^]. |
| **Eternal Terminal** | Resumable TCP over SSH. Supports tmux control mode. Reconnects transparently after network outages [^100^] [^104^]. |
| **abduco/dvtm** | Minimal session management + tiling window manager. Unix philosophy: do one thing well [^62^] [^145^]. |
| **CRIU** | Linux checkpoint/restore for process migration. Powers container live migration [^138^] [^147^]. |
| **libtmux/tmuxp** | Python programmatic control of tmux. Server->Session->Window->Pane object model [^131^]. |

---

## Trends & Signals

- **Web-based terminals becoming first-class**: Zellij's built-in web client (v0.43.0) blurs the line between terminal and browser clients, with browser clients connected to the same IPC channels as terminal clients appearing as "regular users" [^108^].

- **Terminal multiplexers evolving into "terminal workspaces"**: Zellij explicitly positions itself as a workspace rather than just a multiplexer, with layouts-as-code, floating panes, and WASM plugins [^93^].

- **Rust displacing C for terminal infrastructure**: Zellij (Rust), WezTerm (Rust), and various new tools signal a language shift, though tmux (C) remains dominant due to ubiquity and size [^31^].

- **Session managers moving toward declarative configuration**: tmuxinator, tmuxp, smug, and Zellij layouts all use YAML/KDL files to define reproducible terminal environments [^114^] [^93^].

- **Remote session persistence decoupling from transport**: Mosh (UDP + SSP) and Eternal Terminal (resumable TCP) both decouple session lifetime from the underlying network connection, with ET specifically maintaining tmux control mode compatibility [^100^].

- **AI agent integration emerging**: Tools like Termdock (2026) and zellij-agent-tools for MCP (Model Context Protocol) suggest terminal multiplexers will become control surfaces for AI agents [^34^] [^118^].

---

## Controversies & Conflicting Claims

- **Zellij vs tmux**: Zellij advocates claim superior discoverability and modern UX; tmux advocates cite ubiquity on remote servers, smaller footprint (~2MB vs ~50MB), and mature ecosystem [^31^] [^32^]. One user reports Zellij "got too annoyingly bloated" and switched back to tmux [^31^].

- **Mosh vs Eternal Terminal**: Mosh provides predictive local echo and UDP roaming but does NOT support tmux control mode or native scrolling. ET supports tmux control mode but lacks Mosh's predictive echo and uses TCP rather than UDP [^100^] [^102^]. Both use SSH for initial authentication.

- **Whether terminal multiplexing belongs in the terminal emulator**: Some argue window management should be done by the terminal emulator/window manager, not tmux [^31^]. Modern GPU-accelerated terminals (WezTerm, Ghostty, iTerm2) now integrate multiplexing natively, potentially obsoleting standalone tools for local use [^32^].

- **Size vs features tradeoff**: Screen is 280KB, tmux ~2MB, Zellij ~50MB. For embedded/limited systems, this matters significantly [^31^].

---

## Recommended Deep-Dive Areas

1. **Distributed tmux session state**: The biggest gap in current technology. tmux server is single-node. Research how to replicate tmux's server state (session/window/pane hierarchy, PTY I/O) across cluster nodes. CRIU could checkpoint tmux server processes, but real-time state synchronization would require new protocol design.

2. **PTY I/O forwarding protocol**: Design a protocol for forwarding PTY master/slave I/O across the network that supports multiple backends (tmux, screen, Zellij, custom). Study Mosh's SSP for state synchronization patterns applicable to this problem.

3. **Multi-backend abstraction layer**: Create a unified interface that abstracts tmux control mode, Zellij IPC, screen commands, and custom session managers. libtmux provides a model (Server->Session->Window->Pane) but is tmux-specific.

4. **Session migration with transparent I/O**: Study Google's Borg migration approach (CRIU + gRPC reconnect) and DMTCP's distributed coordination for migrating terminal sessions between cluster nodes without dropping connections.

5. **Web-based cluster terminal gateway**: Combine ttyd's WebSocket relay with multi-node session management to provide browser access to cluster sessions running on any node. Zellij's web client architecture is a reference.

6. **Terminal state as distributed state machine**: Mosh's approach of synchronizing terminal screen state (not character streams) between client and server via SSP suggests a model for distributed terminal I/O where state is synchronized rather than streamed.

---

## Raw Evidence Log

### Finding: tmux Control Mode Protocol Format

**Claim:** tmux control mode provides a text-only protocol for external programs to receive events and send commands, with output formatted as `%`-prefixed lines [^142^]

**Source:** tmux/tmux Wiki — Control Mode

**URL:** https://github.com/tmux/tmux/wiki/Control-Mode

**Date:** 2020-09-01

**Excerpt:**
> "Control mode is a special mode that allows a tmux client to be used to talk to tmux using a simple text-only protocol. It was designed and written by George Nachman and allows his iTerm2 terminal to interface with tmux and show tmux panes using the iTerm2 UI. A control mode client is just like a normal tmux client except that instead of drawing the terminal, tmux communicates using text. Because control mode is text only, it can easily be parsed and used over ssh(1)."

**Context:** This is the canonical documentation for tmux control mode, referenced by all control mode implementations.

**Confidence:** HIGH

---

### Finding: tmux Client-Server Architecture

**Claim:** tmux's persistent server process enables session survival across SSH disconnects and client detachment [^36^]

**Source:** sebasblog.com — The Ultimate Guide to tmux

**URL:** https://sebasblog.com/p/the-ultimate-guide-to-tmux-supercharge-your-terminal-productivity/

**Date:** 2025-07-07

**Excerpt:**
> "When an SSH connection to a remote machine drops, it is only the client (your SSH terminal) that terminates. The tmux server, along with all the processes running within its sessions, continues to run completely unaffected on the remote machine."

**Context:** Fundamental tmux architecture description consistent across all sources.

**Confidence:** HIGH

---

### Finding: vasic-digital/tmux Fork Details

**Claim:** vasic-digital/tmux is a containerized, verified build of tmux with enhanced clipboard but NOT distributed cluster support [^109^]

**Source:** GitHub — vasic-digital/tmux

**URL:** https://github.com/vasic-digital/tmux

**Date:** 2026-05-29

**Excerpt:**
> "A reproducible, hardened build of tmux with built-in jemalloc support, OOM-protection helper, and a comprehensive verification gate. Runs natively on any Linux host (Ubuntu, ALT, Fedora, Arch, openSUSE, Alpine) where podman or docker is available."
> "Multi-line copy + paste-IN + Claude Code TUI support. New bind -n M-MouseDrag1Pane copy-mode -M (Alt-drag, macOS)"

**Context:** Our base project. Provides enhanced clipboard, verified builds, and documentation pipeline but does not modify tmux's core client-server architecture for distributed operation.

**Confidence:** HIGH

---

### Finding: Zellij Client-Server IPC Architecture

**Claim:** Zellij uses client-server IPC with daemonized server, and its web client connects to the same IPC channels as terminal clients [^108^]

**Source:** poor.dev blog — Building Zellij's web client

**URL:** https://poor.dev/blog/building-zellij-web-terminal/

**Date:** 2025-08-18

**Excerpt:**
> "Keeping sessions alive in the background involves a client/server architecture. The client runs in the user's terminal like any other program and communicates over IPC with a server holding the state of the terminal session... When Zellij first starts, the client spawns a new server process and daemonizes it so that it keeps running independently in the background."
> "Since browser clients - through the web-server - will be connected to the same IPC channels as terminal clients, they appear as regular users inside a terminal session: blurring the distinction between the two user interfaces."

**Context:** Zellij's architecture is very similar to tmux but with added web server component and WASM plugin runtime.

**Confidence:** HIGH

---

### Finding: Mosh State Synchronization Protocol

**Claim:** Mosh's SSP synchronizes terminal screen state over UDP with client-side predictive echo achieving 4.8ms median keystroke latency vs 503ms for SSH on 3G [^95^]

**Source:** USENIX ATC'12 — Mosh: An Interactive Remote Shell for Mobile Clients

**URL:** https://www.usenix.org/system/files/conference/atc12/atc12-final32.pdf

**Date:** 2012

**Excerpt:**
> "Mosh is built on the State Synchronization Protocol (SSP), a new UDP-based protocol that securely synchronizes client and server state, even across changes of the client's IP address. Mosh uses SSP to synchronize a character-cell terminal emulator, maintaining terminal state at both client and server to predictively echo keystrokes. Our evaluation analyzed keystroke traces from six different users covering a period of 40 hours of real-world usage. Mosh was able to immediately display the effects of 70% of the user keystrokes. Over a commercial EV-DO (3G) network, median keystroke response latency with Mosh was less than 5 ms, compared with 503 ms for SSH."

**Context:** The seminal academic paper on Mosh. SSP's design of synchronizing state objects rather than byte streams is directly relevant to distributed terminal I/O.

**Confidence:** HIGH

---

### Finding: Eternal Terminal Architecture

**Claim:** ET implements resumable TCP through BackedReader/BackedWriter that track sequence numbers and retransmit on reconnect [^104^]

**Source:** eternalterminal.dev — How it Works

**URL:** https://eternalterminal.dev/howitworks/

**Date:** Undated

**Excerpt:**
> "Eternal TCP implements a BackedReader class that keeps track of the number of bytes read (called the sequence number) and, upon reconnect, informs the other party of the sequence number. The BackedWriter class keeps an encrypted buffer of the last N bytes sent and the sequence number. Upon reconnect, the BackedWriter receives the sequence number from the BackedReader and re-sends the last M bytes, where M is the difference between the sequence numbers of the writer and the reader."

**Context:** ET's approach is simpler than Mosh's SSP — it wraps TCP rather than replacing it with UDP. The sequence number approach is directly applicable to reliable I/O forwarding.

**Confidence:** HIGH

---

### Finding: SSH ControlMaster Connection Multiplexing

**Claim:** ControlMaster creates a Unix domain socket for connection reuse, reducing subsequent connection times to milliseconds [^59^]

**Source:** dev.to — Multiplexing SSH Connections with Control Master

**URL:** https://dev.to/mahafuz/multiplexing-ssh-connections-with-control-master-speed-up-deployments-and-automation-26mh

**Date:** 2026-05-27

**Excerpt:**
> "With ControlMaster, connections 2 through N skip steps 1-5 entirely. They open a new channel on the existing multiplexed connection. Connection time drops to single-digit milliseconds regardless of network latency."
> "ControlMaster auto means: if no master connection exists, become one; if one already exists, reuse it."

**Context:** ControlMaster is the standard mechanism for SSH connection optimization and pairs naturally with tmux session management.

**Confidence:** HIGH

---

### Finding: ttyd Architecture and Features

**Claim:** ttyd is built on libwebsockets with C for speed, using xterm.js for browser terminal emulation, supporting SSL, authentication, ZMODEM file transfer [^43^]

**Source:** GitHub — tsl0922/ttyd

**URL:** https://github.com/tsl0922/ttyd

**Date:** 2016-09-13 (ongoing)

**Excerpt:**
> "Built on top of Libwebsockets with C for speed. Fully-featured terminal based on Xterm.js with CJK and IME support. SSL support based on OpenSSL. Run any custom command with options. Basic authentication support and many other custom options."

**Context:** ttyd is the most feature-complete C-based web terminal. Its architecture (WebSocket relay between PTY and browser) is the standard pattern.

**Confidence:** HIGH

---

### Finding: abduco Minimal Session Management

**Claim:** abduco provides session detach/reattach without multiplexing, designed to pair with dvtm for a minimal alternative to tmux/screen [^65^]

**Source:** man page — abduco

**URL:** https://linuxcommandlibrary.com/man/abduco

**Date:** Undated

**Excerpt:**
> "abduco is a lightweight session management tool that provides terminal session detachment and reattachment. It allows processes to continue running when you disconnect from a terminal and reattach later. Unlike screen or tmux, abduco focuses solely on session management without window multiplexing or split panes. This minimalist approach results in a small, fast, and reliable tool."

**Context:** The abduco+dvtm combination demonstrates the Unix philosophy of separating session management from window management.

**Confidence:** HIGH

---

### Finding: Zellij WASM Plugin System

**Claim:** Zellij includes a WASM plugin system allowing plugins in any language that compiles to WebAssembly [^93^]

**Source:** dev.to — Zellij Has a Free Terminal Multiplexer

**URL:** https://dev.to/0012303/zellij-has-a-free-terminal-multiplexer-the-modern-tmux-alternative-with-built-in-layouts-plugin-56jb

**Date:** 2026-03-28

**Excerpt:**
> "Zellij introduces features tmux doesn't have: a WASM plugin system (extend Zellij with Rust, Go, or any language that compiles to WASM), built-in floating panes, and a layout system that defines your workspace as code."

**Context:** WASM plugins provide a powerful extensibility model that could support cluster-specific extensions.

**Confidence:** HIGH

---

### Finding: libtmux Python API Object Model

**Claim:** libtmux provides typed Python objects mapping to tmux's hierarchy: Server -> Session -> Window -> Pane [^131^]

**Source:** GitHub — tmux-python/libtmux

**URL:** https://github.com/tmux-python/libtmux

**Date:** 2016-05-22 (ongoing)

**Excerpt:**
> "libtmux is a typed Python API over tmux, the terminal multiplexer. Stop shelling out and parsing `tmux ls`. Instead, interact with real Python objects: Server, Session, Window, and Pane."
> "|libtmux object|tmux concept|Notes|
> |Server|tmux server / socket|Entry point; owns sessions|
> |Session|tmux session ($0, $1,...)|Owns windows|
> |Window|tmux window (@1, @2,...)|Owns panes|
> |Pane|tmux pane (%1, %2,...)|Where commands run|"

**Context:** libtmux's object model is an excellent reference for designing a multi-backend session management abstraction.

**Confidence:** HIGH

---

### Finding: tmux Session Groups for Multiple Independent Views

**Claim:** tmux session groups allow multiple clients to share windows while maintaining independent focus [^127^]

**Source:** blog.nicholas.clooney.io — My Super Powered Tmux

**URL:** https://blog.nicholas.clooney.io/posts/my-super-powered-tmux-one-session-but-multiple-focuses/

**Date:** 2025-10-14

**Excerpt:**
> "Tmux has a not-so-secret feature called session groups. Sessions in the same group share windows but keep their own focus. In other words: one canonical set of panes, multiple independent views. The manual explains it this way: 'If -t is specified, the new session is grouped with the specified session. Sessions in the same group share the same set of windows.'"

**Context:** Session groups are a powerful but underutilized feature that could inform distributed session design.

**Confidence:** HIGH

---

### Finding: Google Borg Task Migration Using CRIU

**Claim:** Google uses CRIU for task migration at scale in Borg, with encrypted/compressed checkpoints and transparent gRPC reconnection [^141^]

**Source:** Linux Plumbers Conference 2018 — Task Migration at Scale Using CRIU

**URL:** https://lpc.events/event/2/contributions/69/attachments/205/374/Task_Migration_at_Scale_Using_CRIU_-_LPC_2018.pdf

**Date:** 2018

**Excerpt:**
> "The Migrator: Injected into task during a migration, orchestrates the migration. Manages execution of CRIU. Encrypts and compresses checkpoint on the fly. Google Pretends to be a CRIU pageserver."
> "Stubby/gRPC automatically reconnects. Reconnect is transparent to users."

**Context:** Google's approach demonstrates production-scale process migration for cluster environments.

**Confidence:** HIGH

---

### Finding: DMTCP Transparent Cluster Checkpointing

**Claim:** DMTCP provides transparent checkpointing for cluster computations with distributed coordination, supporting MPI through wrapper-based system call interception [^172^]

**Source:** IPDPS 2009 — DMTCP: Transparent Checkpointing for Cluster Computations

**URL:** https://people.csail.mit.edu/jansel/papers/2009ipdps-dmtcp.pdf

**Date:** 2009

**Excerpt:**
> "The user will typically use three DMTCP commands: dmtcp_checkpoint [options] <program>, dmtcp_command <command>, dmtcp_restart_script.sh. The restart script is generated at checkpoint time. Each invocation of dmtcp_checkpoint by the end user causes the corresponding process to be registered as one of the set of processes that will be checkpointed."

**Context:** DMTCP's distributed checkpoint coordination model is relevant for cluster-wide session migration.

**Confidence:** HIGH

---

### Finding: Zellij Web Terminal Security Model

**Claim:** Zellij's web server uses hashed login tokens in SQLite, HTTPS enforcement for external interfaces, and http-only session cookies [^108^]

**Source:** poor.dev blog — Building Zellij's web client

**URL:** https://poor.dev/blog/building-zellij-web-terminal/

**Date:** 2025-08-18

**Excerpt:**
> "Special care has been taken for this token never to be saved in its clear form in any storage. On the server-side, it is hashed and kept in a local SQLite database where it cannot be retrieved, only revoked. On the client-side, it is initially sent through the POST parameters of the handshake and then exchanged for a temporary session-token. This session-token is saved as an 'http only' cookie so that the client-side code cannot access it."

**Context:** Security model for web-based terminal access is directly relevant for cluster terminal gateway design.

**Confidence:** HIGH

---

### Finding: Tactic Remote tmux Integration for Claude Code

**Claim:** Tactic Remote uses tmux sessions for persistent Claude Code sessions with 200ms capture-pane intervals and WebSocket streaming to iPhone clients [^130^]

**Source:** clauderc.com — Why We Built Tactic Remote on tmux

**URL:** https://clauderc.com/blog/2026-02-28-tmux-architecture-and-session-persistence/

**Date:** 2026-02-28

**Excerpt:**
> "The Mac companion continuously captures the terminal output from the tmux pane and streams it to connected iPhone clients via WebSocket. This is the mechanism that lets you see Claude Code's output in real time on your phone. Output capture uses tmux capture-pane with coordinates that cover the visible pane content plus the scroll buffer. The companion runs a capture loop that: 1. Captures the current pane content at a configurable interval (default: 200ms). 2. Diff the captured content against the last known state. 3. Sends only the delta to connected clients."

**Context:** Shows a practical pattern for remote I/O forwarding from tmux to web clients, relevant for cluster session streaming.

**Confidence:** HIGH

---

### Finding: Terminal Multiplexer Size Comparison

**Claim:** Screen is ~280KB, tmux ~2MB, Zellij ~50MB — significant size differences impact embedded deployment [^31^]

**Source:** Hacker News — Make tmux pretty and usable

**URL:** https://news.ycombinator.com/item?id=47752819

**Date:** 2024

**Excerpt:**
> "I have a few embedded devices where flash space is limited. tmux is so much smaller than zellij, and it's not even close. Zellij is close to 50 megabytes, but tmux and all dependent libraries (minus libc, it's always there) is about 2 megabytes."
> "Screen is only 280Kb (armv7), statically compiled with curses. That's about 6-9 times smaller compared to tmux."

**Context:** Size constraints matter for cluster nodes with limited resources.

**Confidence:** HIGH

---

### Finding: PTY Architecture Fundamentals

**Claim:** A pseudo terminal is a pair of character devices (master and slave) where the slave provides a TTY-compatible interface and the master is manipulated by a controlling process [^119^]

**Source:** QNX Documentation — Pseudo terminals (ptys)

**URL:** https://www.qnx.com/developers/docs/8.0/com.qnx.doc.neutrino.sys_arch/topic/char_PTY.html

**Date:** Undated

**Excerpt:**
> "A pseudo terminal (pty) is a pair of character devices: a master device and a worker device. The worker device provides an interface identical to that of a tty device as defined by POSIX. However, while other tty devices represent hardware devices, the worker device instead has another process manipulating it through the master half of the pseudo terminal."

**Context:** Fundamental OS concept underlying all terminal multiplexing and I/O forwarding.

**Confidence:** HIGH

---

*Research compiled from 15+ independent searches across academic papers, official documentation, GitHub repositories, technical blogs, and community discussions. All citations verified as of research date.*
