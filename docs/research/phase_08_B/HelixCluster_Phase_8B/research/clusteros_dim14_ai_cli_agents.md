## Facet: AI CLI Agent Resource Provisioning (Claude Code Parallel Agents)

### Key Findings

- **Claude Code's parallel agent architecture** uses multiple mechanisms: Subagents (delegated workers inside one session), Agent View (background session monitoring via `claude agents`), Agent Teams (orchestrator-subagent model with shared task list), Worktrees (separate git checkouts for filesystem isolation), and `/batch` command (planned splits into 5-30 worktree-isolated subagents) [^1^]. Running several sessions or subagents at once multiplies token usage significantly [^1^].

- **Claude Code Agent Teams** uses an orchestrator-subagent model where a primary Claude instance decomposes work into subtasks on a shared task list. Subagents claim, execute, and complete tasks through real-time updates rather than direct agent-to-agent communication. Token costs scale super-linearly with agents — running four subagents costs significantly more than one agent, even if it completes faster [^2^].

- **Agent View** (accessed via `\` backslash) provides a centralized interface inside a single terminal to launch, monitor, and manage multiple Claude agents running concurrently with real-time status updates. Most developers work with 2-5 concurrent agents comfortably; orchestration-heavy workflows can go higher [^3^].

- **Claude Code rate limits** vary dramatically by tier: Free (~5 RPM), Tier 1 (50 RPM, 20K TPM), Tier 2 (1,000 RPM, 40K TPM), Tier 3 (2,000 RPM, 80K TPM), Tier 4 (4,000 RPM, 160K TPM). Input prompts over 200K tokens are billed at 2x standard rate. Peak-hour throttling reduces 5-hour limits weekdays 5-11 AM PT [^4^][^5^].

- **Anthropic increased token limits 10x in 2025** — Tier 1 now supports 500K input TPM and 80K output TPM, enabling 200 completions/minute per agent. Tier 4 reaches 10M input TPM, making the practical limit application architecture rather than API constraints [^6^].

- **Local LLM inference** via Ollama uses llama.cpp backend with GGUF quantization, enabling 70B parameter models to run in ~40GB with Q4 quantization. On 16GB RAM without GPU, a 7B model generates 12-18 tokens/sec. vLLM with continuous batching handles 10-20x more concurrent requests than Ollama [^7^][^8^].

- **vLLM's PagedAttention** treats KV cache like virtual memory — breaking it into small fixed-size pages allocated on demand. This reduces memory waste to near zero and enables 2-4x more concurrent requests. Combined with continuous batching, vLLM achieves 2,200-2,400 tok/s at 128 concurrent requests on H100 for Llama 3.3 70B FP8 — 3-4x above naive PyTorch loops [^9^][^10^].

- **Git worktrees** are the canonical filesystem isolation mechanism for parallel AI agents. Each worktree provides a separate checkout with its own branch, working directory, and index while sharing the underlying git object store. Claude Code v2.1.49+ adds `--worktree` flag and subagent `isolation: worktree` frontmatter [^11^][^12^]. Without worktree isolation, parallel agents face silent file overwrites and git lock contention [^13^].

- **Container isolation** exists on a spectrum: Docker containers (~500ms startup, tens of MB) provide process-level isolation suitable for trusted workloads; gVisor (~100ms) provides syscall interception for multi-tenant SaaS; Firecracker microVMs (~125ms, <5MiB overhead) provide hardware-level isolation for untrusted code; Kata Containers (~200ms) orchestrate microVMs through Kubernetes APIs [^14^][^15^].

- **Context window management** is critical: system prompt + tool schemas form a fixed cost floor of 2,000-4,000 tokens per API call. Agentic systems consume 5-30x more tokens per task than standard chat. Average trajectory to solve a single GitHub issue contains 48.4K tokens in 40 steps, with 1M accumulated tokens due to repeated re-sending [^16^][^17^].

- **Prompt caching** reduces input token costs by ~90%. Cache writes cost 1.25x, cache reads cost 0.1x. Break-even at turn 2. Claude Code handles caching automatically for system prompts, tool definitions, and conversation history [^18^][^19^].

- **Tree-sitter based code indexing** (Codebase-Memory) parses 66 languages into a knowledge graph in SQLite, exposed via 14 MCP tools. Evaluated across 31 repositories: 83% answer quality vs 92% for file-exploration agents, at 10x fewer tokens and 2.1x fewer tool calls. Query latency under 1ms [^20^].

- **Claude Code hooks** provide 14 lifecycle events for automation: SessionStart, UserPromptSubmit, PreToolUse, PermissionRequest, PostToolUse, PostToolUseFailure, Notification, SubagentStart, SubagentStop, Stop, TeammateIdle, TaskCompleted, PreCompact, SessionEnd. Hooks can block actions (PreToolUse), auto-format after edits (PostToolUse), and enforce quality gates (Stop) [^21^].

- **Permission modes**: `default` (ask for dangerous ops), `acceptEdits` (auto-accept file edits), `plan` (read-only), `dontAsk` (auto-deny unless pre-approved), `bypassPermissions` (skip all checks — isolated environments only). Auto mode (March 2026) uses a classifier model for contextual auto-approval [^22^][^23^].

- **MCP (Model Context Protocol)** is Anthropic's standard for tool ecosystem integration. Official Anthropic collection has 320+ servers; ecosystem totals 4,774+ servers across MCP.so, Glama, PulseMCP, Smithery. SDKs available in TypeScript, Python, Java, Kotlin, C# [^24^].

- **Multi-agent coordination** challenges include: deadlocks when agents mutually block resource access, fairness through round-robin or quota systems, and scalability via hybrid approaches (local negotiation + global coordinator). Leader election selects a central planner; consensus requires all agents to agree on a single value [^25^][^26^].

- **GPU scheduling for AI workloads** on Kubernetes uses the NVIDIA GPU Operator as prerequisite, with Volcano for gang scheduling (distributed training) and Kueue for quota management. Dynamic Resource Allocation (DRA) graduated to GA in Kubernetes 1.34, replacing integer GPU counts with attribute-based resource claims [^27^][^28^].

- **Kimi Code CLI** (Moonshot AI) supports subagents for parallel work with isolated contexts. Kimi K2.5 features "Agent Swarm" — dynamically generating up to 100 subagents with parallel execution, coordinating up to 1,500 tool calls, reducing execution time by up to 4.5x [^29^][^30^].

- **inotify** for file system watching has default limits: max_user_instances=128, max_user_watches=8192, max_queued_events=16384. Large codebases require tuning to 524,288+ watches. Each inotify watch consumes kernel memory (~1KB per watch) [^31^][^32^].

- **ripgrep** is 5-13x faster than grep and used by Claude Code, Cursor, Codex CLI, and Aider. AI agents run 10-30 search operations per task. On repos over 1M files, even ripgrep takes 15+ seconds; pre-built indexed approaches complete in 0.013 seconds [^33^].

### Major Players & Sources

- **Anthropic**: Claude Code CLI tool with multi-agent support; MCP protocol creator; Rate limiting and prompt caching infrastructure
- **Moonshot AI**: Kimi Code CLI with Agent Swarm (up to 100 subagents); open-source Kimi K2.5 model
- **vLLM project**: Berkeley-origin open-source inference engine; PagedAttention and continuous batching
- **Ollama**: Local LLM serving; llama.cpp backend; developer-friendly CLI
- **Codebase-Memory (DeusData)**: Tree-sitter knowledge graph indexing; 14 MCP tools; 66 languages
- **NVIDIA**: GPU Operator, Triton Inference Server, TensorRT-LLM; KAI Scheduler
- **Firecracker (AWS)**: MicroVMs for sandboxing; 125ms boot, <5MiB overhead
- **Kubernetes ecosystem**: Volcano (gang scheduling), Kueue (quota management), DRA (GPU resource allocation)

### Trends & Signals

- **Multi-agent parallelization becoming standard**: Both Claude Code (Agent Teams, /batch) and Kimi Code (Agent Swarm, 100 subagents) are investing heavily in parallel agent execution, signaling industry direction [^1^][^29^]

- **Filesystem isolation via git worktrees becoming standard practice**: Anthropic added `--worktree` flag and `isolation: worktree` to subagents. Augment Code's Intent product builds on this natively. Worktrees move conflict detection from runtime to merge time [^11^][^13^][^34^]

- **Prompt caching now automatic**: Claude Code caches system prompts, tool definitions, and conversation history automatically. Cuts token costs by up to 90%. Cache reads cost 0.1x normal price [^18^][^19^]

- **Structural code indexing replacing brute-force search**: Codebase-Memory demonstrates 10x token reduction via Tree-sitter knowledge graphs. MCP-based tool interfaces becoming standard [^20^]

- **vLLM becoming de facto inference standard**: 2,000+ contributors, 200+ model architectures. PagedAttention + continuous batching deliver 3-4x throughput over naive implementations [^10^]

- **GPU scheduling maturing on Kubernetes**: DRA graduated GA, KAI Scheduler gaining attention, Gateway API Inference Extension standardizing model serving ingress [^27^][^28^]

- **Permission systems evolving from regex to AI classifiers**: Claude Code's Auto mode (March 2026) uses Sonnet 4.6 classifier for contextual permission decisions rather than pattern matching [^23^]

### Controversies & Conflicting Claims

- **Container vs microVM isolation for AI agents**: Docker containers are "suitable only for trusted, vetted code in single-tenant environments" [^14^] while Firecracker/Kata provide hardware-level isolation. But some practitioners argue containers with seccomp, AppArmor, and capability dropping are sufficient for most AI coding workloads. Northflank uses Kata Containers + gVisor for production AI agent sandboxing [^14^].

- **Token cost vs speed tradeoff in parallel agents**: Running 4 parallel agents "won't cost the same as one agent; it will typically cost more overall, even if it completes faster" [^2^]. Some users question whether the speedup justifies the super-linear cost increase.

- **Rate limiting as business model vs technical necessity**: Critics note that "you don't control the upper bounds; throughput varies based on Anthropic's internal policies" and "at $200/month, you're just buying more throttled access, not control" [^35^]. TrueFoundry offers enterprise gateways to pool contracts and route across providers.

- **Pre-built indexes vs on-demand search**: For repos under 100K files, ripgrep is optimal. For enterprise monorepos 1M+ files, Cursor's "Instant Grep" pre-built index is 1,000x faster. Codebase-Memory amortizes indexing cost once across all queries [^33^][^20^].

- **Local inference vs API for agent workloads**: vLLM handles nearly 800 req/s while Ollama maxes at 41 req/s under load [^36^]. But local inference changes economics from per-token to electricity-only, enabling long agent loops developers avoid when paying per-token [^8^].

### Recommended Deep-Dive Areas

- **Git worktree orchestration at scale**: How to manage 10-100 worktrees, their lifecycle, cleanup, and branch naming. Currently manual/scripted — needs orchestration layer. Critical for parallel agent filesystem isolation.

- **Context window budgeting across parallel agents**: Each agent consumes 2,000-4,000 tokens fixed overhead + growing history. With 10+ agents, aggregate token consumption becomes massive. Need intelligent context compression and agent-specific budget allocation.

- **GPU memory sharing for local inference serving multiple agents**: vLLM's PagedAttention enables concurrent request handling, but agent-specific KV cache isolation and memory quotas need investigation. How to share one GPU across 10+ agents fairly?

- **MCP server resource isolation**: MCP servers run as separate processes consuming CPU/memory. With 10+ MCP servers (codebase-memory, GitHub, browsers, databases), aggregate resource usage becomes significant. Need per-agent MCP server sandboxing.

- **Subprocess resource management**: Agents spawn compilers, linters, test runners, git operations. Need cgroup-based limits to prevent fork bombs, CPU exhaustion, or memory leaks from agent-spawned children.

- **Prompt caching coordination across agents**: When agents share common context (system prompts, tool definitions), cache efficiency matters. How to structure shared vs agent-specific cacheable content?

- **Real-time file watching for 100K+ file repos**: inotify limits, performance tuning, and per-worktree watcher management. Codebase-memory uses adaptive polling + XXH3 hashing for incremental sync.

### Raw Evidence Log

**Claim:** Claude Code supports 5 parallel mechanisms: Subagents, Agent View, Agent Teams, Worktrees, and /batch command.
**Source:** Claude Code Docs — Run agents in parallel
**URL:** https://code.claude.com/docs/en/agents
**Date:** 2026-05-11
**Excerpt:** "Subagents, agent view, agent teams, and worktrees each parallelize work in a different way. The right one depends on whether you want to stay in each conversation yourself, hand tasks off and check back later, or have Claude coordinate a group of workers for you."
**Context:** Official Anthropic documentation
**Confidence:** high

---

**Claim:** Claude Code Agent Teams uses orchestrator-subagent model with shared task list for coordination.
**Source:** MindStudio Blog
**URL:** https://www.mindstudio.ai/blog/claude-code-agent-teams-parallel-agents/
**Date:** 2026-04-10
**Excerpt:** "Claude Code Agent Teams is a multi-agent orchestration feature in Claude Code that lets multiple Claude instances work on a single project in parallel. An orchestrator agent breaks the overall goal into subtasks and creates a shared task list. Subagents then claim, execute, and complete tasks from that list — coordinating through real-time updates rather than direct communication with each other."
**Context:** Third-party analysis of Claude Code features
**Confidence:** high

---

**Claim:** Claude Code rate limits: Tier 1 = 50 RPM, 20K TPM; Tier 4 = 4,000 RPM, 160K TPM. Input over 200K tokens billed at 2x.
**Source:** TrueFoundry Blog
**URL:** https://www.truefoundry.com/blog/claude-code-limits-explained
**Date:** 2025-11-03
**Excerpt:** "Tier 1 ($5 credits): ~50 RPM, ~20,000 TPM input... Tier 4 ($400 credits): ~4,000 RPM, ~160,000 TPM input, ~32,000 TPM output... Input prompts over 200K tokens are billed at 2x the standard API rate."
**Context:** Third-party analysis of Claude Code pricing
**Confidence:** high

---

**Claim:** Anthropic increased API token limits 10x — Tier 1 now 500K input TPM, enabling 200 completions/minute.
**Source:** MindStudio Blog
**URL:** https://www.mindstudio.ai/blog/claude-api-token-limits-increase-tier-breakdown/
**Date:** 2026-05-08
**Excerpt:** "At the old 8,000 output tokens per minute on Tier 1, if each agent response averaged 400 tokens, you could sustain about 20 completions per minute. At 80,000, that's 200 completions per minute — assuming your input tokens also fit, which at 500k/min they almost certainly do for most agent patterns."
**Context:** Analysis of Anthropic API rate limit changes
**Confidence:** high

---

**Claim:** vLLM's PagedAttention + continuous batching achieves 2,200-2,400 tok/s at 128 concurrent requests on H100, 3-4x above naive PyTorch.
**Source:** Spheron Network Blog
**URL:** https://www.spheron.network/blog/llm-serving-optimization-continuous-batching-paged-attention/
**Date:** 2026-04-03
**Excerpt:** "At 128+ concurrent requests on H100 SXM5, the combination of all three techniques typically delivers 2,200-2,400 tok/s for Llama 3.3 70B FP8. That's roughly 25% above the default vLLM configuration and 3-4x above a naive PyTorch inference loop."
**Context:** Technical benchmark of vLLM optimization techniques
**Confidence:** high

---

**Claim:** Firecracker microVMs boot in ~125ms with <5MiB memory overhead; Kata Containers in ~200ms.
**Source:** Northflank Blog
**URL:** https://northflank.com/blog/how-to-sandbox-ai-agents
**Date:** 2026-02-02
**Excerpt:** "Firecracker: Boots in ~125ms, less than 5 MiB overhead per VM, up to 150 VMs per second per host... Kata Containers: Boots in ~200ms, minimal memory overhead."
**Context:** Comparison of isolation technologies for AI agents
**Confidence:** high

---

**Claim:** Git worktrees give each agent its own working directory and git index while sharing a single object store, preventing file-level conflicts.
**Source:** Augment Code Guides
**URL:** https://www.augmentcode.com/guides/git-worktrees-parallel-ai-agent-execution
**Date:** 2026-04-07
**Excerpt:** "Git worktrees let you check out multiple branches of the same repository into separate directories simultaneously. For AI coding agents, this is essential because each agent session needs its own isolated filesystem. Without worktrees, two agents working in the same directory will overwrite each other's changes, corrupt file state, and produce unreliable output."
**Context:** Technical guide from Augment Code (IDE with native worktree support)
**Confidence:** high

---

**Claim:** Agentic systems consume 5-30x more tokens per task than standard chat. Average GitHub issue trajectory: 48.4K tokens in 40 steps, 1M accumulated tokens.
**Source:** arXiv paper — Improving the Efficiency of LLM Agent Systems through Trajectory Reduction
**URL:** https://arxiv.org/html/2509.23586v1
**Date:** 2025-09-28
**Excerpt:** "The average trajectory to solve a single GitHub issue, as we collected from the SWE-bench Verified benchmark, contains 48.4K tokens in 40 steps. Breaking down these tokens, tool messages use 30.4K tokens, assistant messages use 13.7K tokens... Since each token concatenated into the trajectory is included in every subsequent input to the LLM, the accumulated token usage per issue reaches 1.0M."
**Context:** Academic paper on LLM agent efficiency
**Confidence:** high

---

**Claim:** Prompt caching cuts token costs by ~90% — cache writes cost 1.25x, reads cost 0.1x. Break-even at turn 2.
**Source:** Iron Mind Blog
**URL:** https://iron-mind.ai/blog/prompt-caching-claude-production
**Date:** 2026-05-27
**Excerpt:** "Prompt caching is the single highest-leverage optimization for any production system running on Claude — done right, it cuts input token costs by ~90% and trims latency by up to 80% on cached prefixes... Cache writes cost 1.25x normal input tokens, cache reads cost 0.1x. The default TTL is 5 minutes, refreshed on every hit."
**Context:** Production engineering blog on Claude API optimization
**Confidence:** high

---

**Claim:** Codebase-Memory achieves 83% answer quality at 10x fewer tokens and 2.1x fewer tool calls vs file exploration, with <1ms query latency.
**Source:** arXiv paper — Codebase-Memory: Tree-Sitter-Based Knowledge Graphs for LLM Code Exploration via MCP
**URL:** https://arxiv.org/html/2603.27277v1
**Date:** 2026-03-28
**Excerpt:** "Evaluated across 31 real-world repositories, Codebase-Memory achieves 83% answer quality versus 92% for a file-exploration agent, at ten times fewer tokens and 2.1 times fewer tool calls. For graph-native queries such as hub detection and caller ranking, it matches or exceeds the explorer on 19 of 31 languages."
**Context:** Academic preprint on code indexing for LLM agents
**Confidence:** high

---

**Claim:** Claude Code hooks fire at 14 lifecycle events. PreToolUse can block actions; Stop hooks enforce quality gates.
**Source:** MorphLLM Blog
**URL:** https://www.morphllm.com/claude-code-hooks
**Date:** 2026-02-15
**Excerpt:** "Hooks fire at 14 different lifecycle events... PreToolUse runs before a tool call executes and can block it... Stop hooks fire when Claude finishes responding. They can prevent Claude from stopping if conditions are not met."
**Context:** Community documentation of Claude Code hooks
**Confidence:** high

---

**Claim:** Claude Code has 5 permission modes: default, acceptEdits, plan, dontAsk, bypassPermissions. Auto mode uses classifier model.
**Source:** ClaudeFast Blog
**URL:** https://claudefa.st/blog/guide/development/permission-management
**Date:** 2026-05-28
**Excerpt:** "Claude Code supports five permission modes, each optimized for different development scenarios... Auto Mode uses an AI classifier to determine what to auto-approve versus what to prompt for."
**Context:** Third-party guide to Claude Code permissions
**Confidence:** high

---

**Claim:** MCP ecosystem has 4,774+ servers (MCP.so), 3,356 (Glama), 3,164 (PulseMCP). Anthropic official: 320.
**Source:** arXiv paper — Model Context Protocol (MCP): Landscape, Security
**URL:** https://arxiv.org/pdf/2503.23278
**Date:** 2025 (assumed)
**Excerpt:** "MCP.so: 4774 servers; Glama: 3356 servers; PulseMCP: 3164 servers; Smithery: 2942 servers; Official Collection (Anthropic): 320 servers"
**Context:** Academic paper on MCP protocol landscape and security
**Confidence:** high

---

**Claim:** Kimi K2.5 Agent Swarm generates up to 100 subagents, coordinates 1,500 tool calls, reduces execution time by 4.5x.
**Source:** IT Home (Chinese tech media)
**URL:** https://www.ithome.com.tw/news/173630
**Date:** 2026-01-28
**Excerpt:** "K2.5 can dynamically generate up to 100 subagents, executing searches, programming, data organization, and verification in parallel... The entire agent swarm is automatically created and scheduled by the model... Compared to single-agent mode, the swarm can reduce overall execution time by up to 4.5x."
**Context:** News coverage of Moonshot AI Kimi K2.5 release
**Confidence:** medium (secondary source)

---

**Claim:** ripgrep is 5-13x faster than grep; AI agents run 10-30 searches per task. On 1M+ file repos, pre-built indexes are 1,000x faster.
**Source:** CodeAnt Blog
**URL:** https://www.codeant.ai/blogs/ripgrep-vs-grep-performance
**Date:** 2025-11-18
**Excerpt:** "AI agents run 10-30 search operations per task to find function definitions, trace dependencies, and understand call patterns. At ripgrep's speeds, 30 searches complete in under one second. At grep's speeds, the same 30 searches take 20-90 seconds... On codebases north of one million files, even ripgrep takes 15+ seconds per search. Cursor's pre-built indexed approach completes the same search in 0.013 seconds — a 1,000x speedup."
**Context:** Technical comparison of code search tools
**Confidence:** high

---

**Claim:** inotify default limits: max_user_watches=8192, max_user_instances=128. Need 524,288+ for large projects.
**Source:** GitHub Gist — Increasing inotify watchers
**URL:** https://gist.github.com/coenraadhuman/fa7345e95a9b4dea851dbe9e8f011470
**Date:** 2026-01-22 (updated)
**Excerpt:** "Ubuntu Lucid's (64bit) inotify limit is set to 8192... echo fs.inotify.max_user_watches=524288 | sudo tee -a /etc/sysctl.conf && sudo sysctl -p"
**Context:** Community guide for inotify tuning
**Confidence:** high

---

**Claim:** Claude Code v2.1.50 added `--worktree` flag, `isolation: worktree` for subagents, `WorktreeCreate`/`WorktreeRemove` hooks.
**Source:** Blake Crosley Guide
**URL:** https://blakecrosley.com/guides/claude-code
**Date:** 2026-05-28
**Excerpt:** "Added: v2.1.50 — WorktreeCreate/WorktreeRemove hook events for custom VCS setup/teardown, isolation: worktree in agent definitions, claude agents CLI command"
**Context:** Comprehensive Claude Code community guide
**Confidence:** high

---

**Claim:** GPU scheduling on Kubernetes: NVIDIA GPU Operator prerequisite; Volcano for gang scheduling; Kueue for quota. DRA graduated GA in K8s 1.34.
**Source:** CloudOptimo Blog
**URL:** https://www.cloudoptimo.com/blog/kubernetes-ai-infrastructure-in-2026-gpu-scheduling-and-production-realities/
**Date:** 2026-05-29
**Excerpt:** "Dynamic Resource Allocation (DRA) graduated to General Availability in Kubernetes 1.34 and Red Hat OpenShift 4.21. It replaces the legacy Device Plugin framework, which could only express GPU requests as integer counts, with a structured system for declaring hardware requirements with richer attributes."
**Context:** Analysis of Kubernetes AI infrastructure trends
**Confidence:** high

---

**Claim:** 4% of public GitHub commits (~135,000/day) are authored by Claude Code — 42,896x growth in 13 months. 90% of Anthropic's own code is AI-written.
**Source:** Blake Crosley Guide
**URL:** https://blakecrosley.com/guides/claude-code
**Date:** 2026-05-28
**Excerpt:** "As of February 2026, 4% of public GitHub commits (~135,000 per day) are authored by Claude Code—a 42,896x growth in 13 months since the research preview—and 90% of Anthropic's own code is AI-written."
**Context:** Community guide citing Anthropic statistics
**Confidence:** medium (single source, unverified)

---

**Claim:** Multi-agent systems handle shared resources through centralized controllers, decentralized negotiation, and token-based systems. Deadlocks occur when agents mutually block each other.
**Source:** Milvus AI Quick Reference
**URL:** https://milvus.io/ai-quick-reference/how-do-multiagent-systems-handle-shared-resources
**Date:** 2026-03-13
**Excerpt:** "Multi-agent systems handle shared resources through coordination mechanisms that balance efficiency and fairness while preventing conflicts... Challenges include avoiding deadlocks, ensuring fairness, and scaling efficiently."
**Context:** Technical reference on multi-agent resource coordination
**Confidence:** medium

---

**Claim:** Agent sandboxing requires defense-in-depth: isolation boundaries, resource limits, network controls, permission scoping, monitoring. "83% of companies plan to deploy AI agents."
**Source:** Northflank Blog
**URL:** https://northflank.com/blog/how-to-sandbox-ai-agents
**Date:** 2026-02-02
**Excerpt:** "When 83% of companies plan to deploy AI agents, understanding sandboxing becomes essential for preventing security breaches that traditional cybersecurity tools weren't designed to handle."
**Context:** Vendor blog on AI agent sandboxing
**Confidence:** medium (vendor source, statistic unverified)
