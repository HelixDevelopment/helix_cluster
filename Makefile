.PHONY: help dev dev-down dev-status dev-logs dev-compose dev-compose-down build-images vm-test test test-unit test-integration benchmark lint format build cross-agent clean migrate-up migrate-down seed codegraph-index docs docs-verify docs-update

help: ## List all targets
	@echo "Available targets:"
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

# Infrastructure management
dev: ## Start Helix infrastructure
	@echo "Starting Helix infrastructure..."
	@go run ./cmd/helix_infra up --wait --timeout 300s

dev-down: ## Stop Helix infrastructure
	@echo "Stopping Helix infrastructure..."
	@go run ./cmd/helix_infra down

dev-status: ## Show Helix infrastructure status
	@go run ./cmd/helix_infra status

dev-logs: ## Show logs for a service (use: make dev-logs service=helixd)
	@go run ./cmd/helix_infra logs $(service)

# Legacy direct compose (fallback)
dev-compose: ## Start Docker Compose environment directly
	docker compose -f docker_compose.yml up -d --wait

dev-compose-down: ## Stop Docker Compose environment directly
	docker compose -f docker_compose.yml down -v

# Build Docker images
build-images: ## Build all Docker images
	docker build -f deploy/docker/helixd.dockerfile -t helixd:latest .
	docker build -f deploy/docker/helix_gateway.dockerfile -t helix_gateway:latest .
	docker build -f deploy/docker/helix_agent.dockerfile -t helix_agent:latest .

# VM testing
vm-test: ## Run VM node tests
	@go test -tags=vm ./tests/vm_nodes/...

test: ## Run all tests
	go test -race -coverprofile=coverage.out ./...
	@echo "Coverage report: coverage.out"

test-unit: ## Run unit tests only
	go test -short ./...

test-integration: ## Run integration tests
	go test -tags=integration ./...

benchmark: ## Run benchmarks
	go test -bench=. -benchmem ./...

lint: ## Run all linters
	golangci-lint run ./...
	@echo "Lint complete."

format: ## Format all code
	gofumpt -w .
	zig fmt .
	find . -regextype posix-extended -regex '.*\.(c|h|cpp|hpp|cc|cxx)$' -exec clang-format -i {} +

build: ## Build all binaries
	go build -o bin/helix-cluster ./cmd/helix-cluster
	zig build
	cmake -B build -S . && cmake --build build

cross-agent: ## Reproducible ARM64 cross-build of helix-agent into dist/ (HXC-1167)
	bash scripts/cross-compile-agent.sh

clean: ## Clean build artifacts
	rm -rf bin/ build/ dist/ coverage.out
	zig build clean || true

# DATABASE_URL is honored when set (postgres://user:pass@host:port/db?sslmode=...).
# When unset it defaults to the local dev DSN; scripts/run-migrations.sh reads the
# parsed DB_* environment variables (never a hardcoded secret).
DATABASE_URL ?= postgres://helix:helix@localhost:5432/helix_cluster?sslmode=disable

migrate-up: ## Run database migrations (honors DATABASE_URL)
	@echo "Running migrations up..."
	@DATABASE_URL='$(DATABASE_URL)' bash scripts/run-migrations.sh up

migrate-down: ## Rollback one migration (honors DATABASE_URL)
	@echo "Rolling back migrations..."
	@DATABASE_URL='$(DATABASE_URL)' bash scripts/run-migrations.sh down 1

seed: ## Seed development data
	@echo "Seeding development data..."
	psql "postgres://helix:helix@localhost:5432/helix?sslmode=disable" -f scripts/seed-data.sql

codegraph-index: ## Re-index CodeGraph
	@echo "Re-indexing CodeGraph..."
	# Placeholder: invoke codegraph indexer

## Documentation targets
docs: ## Generate all documentation exports
	bash scripts/docs/generate.sh all

docs-verify: ## Verify documentation sync
	bash scripts/docs/verify.sh

docs-update: ## Update CONTINUATION.md
	bash scripts/docs/update_continuation.sh
