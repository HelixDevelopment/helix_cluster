# Developer Guide

This guide covers the project structure, how to add a new service, running tests, code conventions, and generating protobuf definitions.

---

## Project Structure

```
helix-cluster-os/
├── api/                    # Protobuf definitions and generated code
├── bin/                    # Compiled binaries (gitignored)
├── cmd/                    # Entry points for each service
│   ├── helixd/
│   ├── helix-gateway/
│   ├── helix-build/
│   ├── helix-scheduler/
│   └── ...                 # One directory per service
├── internal/               # Private application code
│   ├── common/             # Shared utilities (logging, config, errors)
│   ├── etcd/               # etcd client wrappers
│   ├── gateway/            # Gateway routing and middleware
│   └── services/           # Service implementations
├── pkg/                    # Public libraries
│   ├── proto/              # Generated protobuf packages
│   └── client/             # Go client for the API
├── web-ui/                 # React-based Web UI
├── deployments/            # Kubernetes manifests and Dockerfiles
├── docs/                   # Documentation
├── scripts/                # Build and helper scripts
├── Makefile
├── go.mod
└── README.md
```

---

## Adding a New Service

1. **Create the service directory:**

   ```bash
   mkdir -p cmd/helix-<name> internal/services/<name>
   ```

2. **Define the protobuf API:**

   Add `api/<name>/v1/<name>.proto` with service methods and message types.

3. **Generate protobuf code:**

   ```bash
   make proto
   ```

4. **Implement the service:**

   Create `internal/services/<name>/server.go` implementing the generated gRPC interface.

5. **Add the CLI entry point:**

   Create `cmd/helix-<name>/main.go` that initializes the server and registers with etcd.

6. **Register in the build system:**

   Add the new binary target to the `Makefile`:

   ```makefile
   BINARIES += helix-<name>
   ```

7. **Add tests:**

   Create `internal/services/<name>/server_test.go`.

8. **Update documentation:**

   Add the service to the architecture diagram in `docs/guides/architecture.md`.

---

## Running Tests

Run the full test suite:

```bash
make test
```

Run tests for a specific package:

```bash
go test ./internal/services/build/...
```

Run with race detection:

```bash
make test-race
```

Run integration tests (requires local etcd):

```bash
make test-integration
```

---

## Code Conventions

- **Language:** Go 1.26+
- **Formatting:** `gofmt` and `goimports` are enforced in CI.
- **Linting:** Run `make lint` before committing. We use `golangci-lint`.
- **Error handling:** Use `internal/common/errors` for wrapped errors with codes.
- **Logging:** Use structured logging via `internal/common/log`. Include `service`, `request_id`, and `error` fields.
- **Naming:**
  - Packages: lowercase, no underscores (`buildservice`, not `build_service`)
  - Interfaces: end with `-er` where possible (`Builder`, `Scheduler`)
  - Test files: `*_test.go` adjacent to the code under test
- **Comments:** Export all public symbols with Go doc comments.

---

## Generating Protobufs

Prerequisites:

- `buf` (v1.30+)
- `protoc-gen-go` and `protoc-gen-go-grpc`

Install tools:

```bash
make tools
```

Generate all protobuf code:

```bash
make proto
```

Regenerate a single service:

```bash
buf generate --path api/build/v1
```

After generating, run tests to ensure compatibility:

```bash
make test
```

---

## Web UI Development

```bash
cd web-ui
npm install
npm run dev       # Start dev server
npm run build     # Production build
npm run test      # Run unit tests
npm run lint      # Run ESLint
```

The Web UI uses React 18, TypeScript, and Vite.

---

## Submitting Changes

1. Create a feature branch: `git checkout -b feature/<description>`
2. Make your changes and add tests.
3. Run `make precommit` (format, lint, test).
4. Open a pull request with a clear description and linked issues.
