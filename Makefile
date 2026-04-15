.PHONY: all dev build-server build-agent build-all test vet lint migrate-up migrate-down clean

BINARY_SERVER        := bin/netbox-tool
BINARY_SERVER_ARM64  := bin/netbox-tool-server-linux-arm64
BINARY_AGENT         := bin/netbox-agent
BINARY_AGENT_AMD64   := bin/netbox-agent-linux-amd64
BINARY_AGENT_ARM64   := bin/netbox-agent-linux-arm64

# ── Development ───────────────────────────────────────────────────────────────

dev: ## Run server in dev mode (requires .env)
	@echo "Starting server..."
	go run ./cmd/server

dev-frontend: ## Run Vite dev server with proxy to backend
	cd web && npm run dev

# ── Build ─────────────────────────────────────────────────────────────────────

build-server: build-frontend ## Build the server binary (embeds frontend)
	mkdir -p bin
	go build -o $(BINARY_SERVER) ./cmd/server

build-server-linux: build-frontend ## Cross-compile server for Linux arm64 (OrbStack/Raspberry Pi)
	mkdir -p bin
	GOOS=linux GOARCH=arm64 go build -o $(BINARY_SERVER_ARM64) ./cmd/server
	@echo "Server binary: $(BINARY_SERVER_ARM64)"

build-agent: ## Build the agent binary for the current OS
	mkdir -p bin
	go build -o $(BINARY_AGENT) ./cmd/agent

build-agent-linux: ## Cross-compile agent for Linux (amd64 + arm64)
	mkdir -p bin
	GOOS=linux GOARCH=amd64 go build -o $(BINARY_AGENT_AMD64) ./cmd/agent
	GOOS=linux GOARCH=arm64 go build -o $(BINARY_AGENT_ARM64) ./cmd/agent
	@echo "Agent binaries: $(BINARY_AGENT_AMD64) $(BINARY_AGENT_ARM64)"

build-all: build-server-linux build-agent-linux ## Build server (Linux arm64) + Linux agent binaries

build-frontend: ## Build the React frontend for production
	cd web && npm run build

# ── Quality ───────────────────────────────────────────────────────────────────

test: ## Run all Go tests
	go test ./...

vet: ## Run go vet
	go vet ./...

lint: ## Run golangci-lint (requires golangci-lint installed)
	golangci-lint run ./...

typecheck: ## TypeScript type check
	cd web && npx tsc --noEmit

# ── Database ──────────────────────────────────────────────────────────────────

migrate-up: ## Apply all pending migrations
	@test -n "$(DATABASE_URL)" || (echo "DATABASE_URL not set"; exit 1)
	migrate -path internal/server/db/migrations -database "$(DATABASE_URL)" up

migrate-down: ## Roll back the last migration
	@test -n "$(DATABASE_URL)" || (echo "DATABASE_URL not set"; exit 1)
	migrate -path internal/server/db/migrations -database "$(DATABASE_URL)" down 1

migrate-status: ## Show migration status
	@test -n "$(DATABASE_URL)" || (echo "DATABASE_URL not set"; exit 1)
	migrate -path internal/server/db/migrations -database "$(DATABASE_URL)" version

# ── Cleanup ───────────────────────────────────────────────────────────────────

clean: ## Remove build artifacts
	rm -rf bin/
	rm -rf web/dist/

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'
