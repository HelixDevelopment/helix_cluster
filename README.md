# Helix Cluster OS

Helix Cluster OS is a next-generation distributed operating system for orchestrating compute workloads across heterogeneous nodes. It unifies HPC scheduling, container orchestration, AI/ML inference, and secure multi-tenant session management under a single control plane.

## Features

- **Distributed Node Management** — Register, monitor, and manage heterogeneous compute nodes with structured health scoring.
- **Session Orchestration** — Create and manage interactive and batch sessions with resource allocation.
- **Scheduler** — Pluggable job scheduler with constraint-based placement and real-time event streaming.
- **Security** — SPIFFE identity, JWT authentication, and capability-based authorization.
- **Observability** — Built-in metrics, distributed tracing, and structured logging.
- **Web UI** — React + TypeScript + Vite dashboard for cluster visualization and management.
- **Multi-Protocol APIs** — gRPC services with Protocol Buffer definitions for all subsystems.

## Project Structure

```
.
├── api/               # Protocol Buffer definitions
├── auth/              # Authentication service (submodule)
├── cache/             # Caching service (submodule)
├── challenges/        # Challenge platform (submodule)
├── concurrency/       # Concurrency utilities (submodule)
├── config/            # Configuration service (submodule)
├── containers/        # Container runtime (submodule)
├── database/          # Database layer (submodule)
├── discovery/         # Service discovery (submodule)
├── docs/              # Documentation
├── EventBus/          # Event bus service (submodule)
├── Filesystem/        # Distributed filesystem (submodule)
├── helixqa/           # QA framework (submodule)
├── Herald/            # Notification service (submodule)
├── http3/             # HTTP/3 gateway (submodule)
├── LLMOrchestrator/   # LLM orchestration (submodule)
├── LLMProvider/       # LLM provider abstraction (submodule)
├── LLMsVerifier/      # LLM verification (submodule)
├── mdns/              # mDNS discovery (submodule)
├── Messaging/         # Messaging service (submodule)
├── middleware/        # Middleware components (submodule)
├── migrations/        # Database migrations
├── observability/     # Observability stack (submodule)
├── Panoptic/          # Panoptic monitoring (submodule)
├── pkg/               # Shared core packages
├── ratelimiter/       # Rate limiter (submodule)
├── recovery/          # Recovery service (submodule)
├── scripts/           # Build, test, and utility scripts
├── security/          # Security service (submodule)
├── Storage/           # Storage service (submodule)
├── tmux/              # Terminal multiplexer integration (submodule)
├── upstreams/         # Upstream dependency scripts
├── VisionEngine/      # Vision engine (submodule)
└── web/               # React web UI
```

## Quick Start

### Prerequisites

- Go 1.24+
- Node.js 20+ (for web UI)
- Docker & Docker Compose
- Protocol Buffer compiler (optional, for API generation)

### Setup

```bash
./scripts/setup.sh
```

### Build

```bash
./scripts/build.sh
```

### Test

```bash
./scripts/test.sh
```

### Lint

```bash
./scripts/lint.sh
```

### Format

```bash
./scripts/format.sh
```

## API

Protocol Buffer definitions are located in `api/v1/`. Services include:

- `NodeService` — Node lifecycle management
- `SessionService` — Session CRUD operations
- `SchedulerService` — Job scheduling and monitoring
- `HealthService` — Health checks and reporting
- `AdvisoryService` — Distributed locks and advisory events
- `SecurityService` — Authentication and authorization
- `BuildService` — Build pipeline management

## Web UI

The web dashboard is built with React, TypeScript, and Vite.

```bash
cd web
npm install
npm run dev
```

## Contributing

1. Fork the repository
2. Create a feature branch
3. Commit your changes
4. Push to the branch
5. Open a Pull Request

## License

See [LICENSE](LICENSE) for details.
