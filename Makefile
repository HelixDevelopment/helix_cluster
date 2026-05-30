.PHONY: help dev dev-down dev-status dev-logs dev-compose dev-compose-down build-images vm-test test test-unit test-integration benchmark lint format build clean migrate-up migrate-down seed codegraph-index

help: ## List all targets
	@echo "Available targets:"
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

# Infrastructure management
dev: ## Start Helix infrastructure
	@echo "Starting Helix infrastructure..."
	@go run ./cmd/helix-infra up --wait --timeout 300s

dev-down: ## Stop Helix infrastructure
	@echo "Stopping Helix infrastructure..."
	@go run ./cmd/helix-infra down

dev-status: ## Show Helix infrastructure status
	@go run ./cmd/helix-infra status

dev-logs: ## Show logs for a service (use: make dev-logs service=helixd)
	@go run ./cmd/helix-infra logs $(service)

# Legacy direct compose (fallback)
dev-compose: ## Start Docker Compose environment directly
	docker compose -f docker-compose.yml up -d --wait

dev-compose-down: ## Stop Docker Compose environment directly
	docker compose -f docker-compose.yml down -v

# Build Docker images
build-images: ## Build all Docker images
	docker build -f deploy/docker/helixd.Dockerfile -t helixd:latest .
	docker build -f deploy/docker/helix-gateway.Dockerfile -t helix-gateway:latest .
	docker build -f deploy/docker/helix-agent.Dockerfile -t helix-agent:latest .

# VM testing
vm-test: ## Run VM node tests
	@go test -tags=vm ./tests/vm-nodes/...

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

clean: ## Clean build artifacts
	rm -rf bin/ build/ coverage.out
	zig build clean || true

migrate-up: ## Run database migrations
	@echo "Running migrations up..."
	# Placeholder: migrate -path migrations -database "postgres://helix:helix@localhost:5432/helix?sslmode=disable" up

migrate-down: ## Rollback migrations
	@echo "Rolling back migrations..."
	# Placeholder: migrate -path migrations -database "postgres://helix:helix@localhost:5432/helix?sslmode=disable" down 1

seed: ## Seed development data
	@echo "Seeding development data..."
	psql "postgres://helix:helix@localhost:5432/helix?sslmode=disable" -f scripts/seed-data.sql

codegraph-index: ## Re-index CodeGraph
	@echo "Re-indexing CodeGraph..."
	# Placeholder: invoke codegraph indexer
