# HXC Ticket Registry

**Revision:** 6
**Last modified:** 2026-05-30T20:09:59Z
**Description:** Master registry of all HXC workable items
**Authority:** Constitution §11.4.15
**Maintainer:** Operator + AI loop

---

## Phase Legend

| Range | Phase |
|-------|-------|
| HXC-001 – HXC-099 | Phase 0 (Foundation) |
| HXC-100 – HXC-199 | Phase 1 (Core Infrastructure) |
| HXC-200 – HXC-299 | Phase 2 (Resource Management) |
| HXC-300 – HXC-399 | Phase 3 (Session Manager) |
| HXC-400 – HXC-499 | Phase 4 (Build Service) |
| HXC-500 – HXC-599 | Phase 5 (LLM Brain) |
| HXC-600 – HXC-699 | Phase 6 (Security) |
| HXC-700 – HXC-799 | Phase 7 (QA & Testing) |
| HXC-800 – HXC-899 | Phase 8 (Polish & Release) |
| HXC-900+ | Cross-cutting / ongoing |

---

## Status Definitions

| Status | Meaning |
|--------|---------|
| Queued | Ticket accepted, not yet started |
| In progress | Actively being worked |
| Ready for testing | Implementation complete, awaiting test |
| In testing | Under active test / QA |
| Fixed | Completed and verified |

---

## Active Tickets

| HXC-ID | Phase | Type | Status | Title | Commit |
|--------|-------|------|--------|-------|--------|
| HXC-400 | Phase 4 | Feature | Queued | Build Service — Bazel RBE protocol | — |
| HXC-500 | Phase 5 | Feature | Queued | LLM Brain — inference engine | — |
| HXC-600 | Phase 6 | Feature | Queued | Security Hardening — mTLS everywhere | — |
| HXC-700 | Phase 7 | Feature | In progress | QA & Testing — integration test suite | — |
| HXC-800 | Phase 8 | Feature | Queued | Polish & Release — v1.0.0-dev-mvp tag | — |
| HXC-902 | Cross-cutting | Task | In progress | Documentation chain setup | — |
| HXC-903 | Cross-cutting | Task | In progress | Continuation document maintenance | — |

---

## Completed Tickets

| HXC-ID | Phase | Type | Status | Title | Commit |
|--------|-------|------|--------|-------|--------|
| HXC-001 | Phase 0 | Foundation | Fixed | Repository structure + go.work | 75bffec |
| HXC-002 | Phase 0 | Foundation | Fixed | Docker Compose 20-service stack | 75bffec |
| HXC-003 | Phase 0 | Foundation | Fixed | 30 pkg/ package stubs with tests | f1162a7 |
| HXC-004 | Phase 0 | Foundation | Fixed | Proto definitions + buf pipeline | 6035284 |
| HXC-005 | Phase 0 | Foundation | Fixed | web/ React+Vite scaffold | 84a1616 |
| HXC-006 | Phase 0 | Foundation | Fixed | Constitution + CodeGraph setup | 75bffec |
| HXC-007 | Phase 0 | Foundation | Fixed | CI/CD scaffolding | 75bffec |
| HXC-100 | Phase 1 | Infrastructure | Fixed | Containers submodule extensions | 623cab8 |
| HXC-101 | Phase 1 | Infrastructure | Fixed | Infrastructure orchestrator | 623cab8 |
| HXC-102 | Phase 1 | Infrastructure | Fixed | helix_infra CLI | 623cab8 |
| HXC-103 | Phase 1 | Infrastructure | Fixed | VM testing framework | 623cab8 |
| HXC-104 | Phase 1 | Chore | Fixed | snake_case compliance rename | 8185d0a |
| HXC-105 | Phase 1 | Chore | Fixed | Manual CI/CD disable | 335f4f7 |
| HXC-200 | Phase 2 | Feature | Fixed | SWIM gossip protocol | 2632b1c |
| HXC-201 | Phase 2 | Feature | Fixed | WireGuard mesh networking | 2632b1c |
| HXC-202 | Phase 2 | Feature | Fixed | Session manager with CRDT | 2632b1c |
| HXC-203 | Phase 2 | Feature | Fixed | Service discovery with TTL | 2632b1c |
| HXC-204 | Phase 2 | Feature | Fixed | Scheduler (Omega model) | 9982c59 |
| HXC-205 | Phase 2 | Feature | Fixed | Resource aggregator | 9982c59 |
| HXC-206 | Phase 2 | Feature | Fixed | Session backends (tmux, PTY) | 9982c59 |
| HXC-300 | Phase 3 | Guarantee | Fixed | Constitution PCS-6 guarantee | 8898518 |
| HXC-301 | Phase 3 | Verification | Fixed | Inheritance verification gate | e20c3ef |
| HXC-302 | Phase 3 | Test | Fixed | Paired mutation test | e20c3ef |
| HXC-303 | Phase 3 | Audit | Fixed | Anti-bluff audit | 9aaeb9f |
| HXC-304 | Phase 3 | Fix | Fixed | Stub fixes (8 packages) | 35de8ef |
| HXC-305 | Phase 3 | Test | Fixed | Mutation tests (15 packages) | 35de8ef |
| HXC-900 | Cross-cutting | Governance | Fixed | Constitution cascade to submodules | 2e861d1 |
| HXC-901 | Cross-cutting | Tracking | Fixed | MVP Progress tracker | f7dd99e |

---

## Commit-to-Ticket Mapping

| Commit | Message | Ticket |
|--------|---------|--------|
| 5f2d121 | Initial commit | HXC-001 |
| 7f02555 | Auto-commit | HXC-001 |
| 3336278 | Research material. | HXC-001 |
| 75bffec | Phase 0: Foundation — submodules, Constitution, CodeGraph, CI/CD, migrations, dev environment, project structure | HXC-001, HXC-002, HXC-006, HXC-007 |
| 5cf8e89 | Phase 0: Incremental — build.sh, lint.sh, format.sh, benchmark.sh, codegraph_setup.sh scripts | HXC-007 |
| 7e6a718 | Phase 0: Incremental — .gitignore, README update, codegraph_update.sh, script improvements | HXC-001 |
| 78f2c47 | Phase 0: Incremental — VERSION, CHANGELOG.md, .gitattributes, go.work updates | HXC-001 |
| b308456 | Phase 0: Incremental — go.sum, go.work.sum, grpcutil and workerpool test fixes, seed data updates | HXC-003 |
| f6d925c | Phase 0: Incremental — Phase 0 completion research docs | HXC-001 |
| 40e425c | Phase 0: Incremental — additional pkg/ stubs, classads, grpcutil, jwt, middleware, pubsub, serde, validator, websocket | HXC-003 |
| f1162a7 | Phase 0: Incremental — all pkg/ stubs complete, API proto definitions, events, lru, ratelimit, semaphore, workerpool, leader, discovery | HXC-003 |
| 6035284 | Phase 0: Incremental — advisory, health, security proto definitions | HXC-004 |
| 84a1616 | Phase 0: Incremental — Web UI scaffolding (React+Vite+TS), buf.gen.yaml | HXC-005 |
| 396fe86 | Phase 2 Research. | HXC-200 |
| 623cab8 | Phase 1: Core Infrastructure — container-native execution, helix-infra CLI, compose migration, VM testing | HXC-100, HXC-101, HXC-102, HXC-103 |
| 335f4f7 | Phase 1: Disable all GitHub Actions workflows — manual CI/CD only | HXC-105 |
| 8185d0a | Phase 1: §11.4.29 snake_case compliance — rename kebab-case files and references | HXC-104 |
| 8898518 | Constitution: PCS-6 End-User Usability Guarantee — §7.1 + §11.4.39 reinforcement | HXC-300 |
| 9bb47e2 | Phase 3 Research. | HXC-300 |
| 2632b1c | Phase 2: Core Infrastructure - SWIM protocol, WireGuard mesh, session manager, discovery race fixes, infra mutex | HXC-200, HXC-201, HXC-202, HXC-203 |
| 2e861d1 | chore: cascade Helix Constitution inheritance pointers to all submodules | HXC-900 |
| e20c3ef | Constitution §7 + §11.4.102: Add inheritance verification gate + paired mutation test + anti-bluff audit | HXC-301, HXC-302 |
| 9aaeb9f | Anti-bluff audit follow-up: Real JWT implementation + mutation tests for 6 packages | HXC-303 |
| 35de8ef | Anti-bluff follow-up: Fix 8 stub packages + mutation tests for 15 packages | HXC-304, HXC-305 |
| f7dd99e | docs: MVP implementation progress tracker | HXC-901 |
| 9982c59 | MVP Phase 2-3: Scheduler, Resource Aggregator, Session Backends | HXC-204, HXC-205, HXC-206 |

---

## Rules

1. **HXC numbers are permanent.** Never reuse a number, even if a ticket is abandoned.
2. **Status transitions:** Queued → In progress → Ready for testing → In testing → Fixed.
3. **Every commit to main MUST reference an HXC ticket** in the message body: `Refs: HXC-XXX`.
4. **This registry is the single source of truth.** All other trackers must sync from here.
5. **New tickets** must be created via `scripts/hxc.sh create` to ensure numbering integrity.
