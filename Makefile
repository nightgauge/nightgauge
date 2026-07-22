.PHONY: generate generate-go generate-ts install-codegen \
	build-cli test-go test-integration vet lint-go build-all generate-ipc-client \
	test-platform-integration test-e2e-docker integration-gitlab \
	integration-gitlab-up integration-gitlab-down test-parity \
	integration-mattermost integration-mattermost-up integration-mattermost-down

# Install codegen tools
install-codegen:
	go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest

# Generate Go client types from OpenAPI spec
generate-go: install-codegen
	mkdir -p api/generated/go/platform
	oapi-codegen -generate types,client -package platform \
		-o api/generated/go/platform/types.gen.go api/openapi.yaml

# Generate TypeScript types from OpenAPI spec
generate-ts:
	npx openapi-typescript api/openapi.yaml -o api/generated/ts/platform-api.ts

# Generate all client types
generate: generate-go generate-ts

# Validate OpenAPI spec

# Generate TypeScript IPC client from Go method signatures
generate-ipc-client:
	go run ./cmd/ipc-codegen \
		--server internal/ipc/server.go \
		--protocol internal/ipc/protocol.go \
		--out packages/nightgauge-vscode/src/services/IpcClient.generated.ts

# --- Go CLI Binary ---

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-s -w -X main.version=$(VERSION)"
BIN_DIR := bin

# CGO_ENABLED=0 produces a statically linked binary, matching what GoReleaser
# ships for the standalone CLI. Without it, a NATIVE linux build (as on CI's
# ubuntu runner) links against glibc's loader, so the binary we bundle into the
# linux .vsix runs on glibc distros but fails on musl (Alpine) and minimal
# container images. The extension loads this binary directly, so it must be as
# portable as the released CLI.
CGO_ENABLED ?= 0
export CGO_ENABLED

# Build CLI for current platform
build-cli:
	go build $(LDFLAGS) -o $(BIN_DIR)/nightgauge ./cmd/nightgauge

# Build CLI for all target platforms
build-all: build-cli
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(BIN_DIR)/nightgauge-darwin-arm64 ./cmd/nightgauge
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(BIN_DIR)/nightgauge-darwin-amd64 ./cmd/nightgauge
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BIN_DIR)/nightgauge-linux-amd64 ./cmd/nightgauge

# Run Go tests
test-go:
	go test ./... -parallel 8 -count=1

# Run integration tests (requires Docker Compose platform — see docs/INTEGRATION_TESTING.md)
test-integration:
	go test -tags integration ./... -count=1

# Run go vet
vet:
	go vet ./...

# Run golangci-lint (requires: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest)
lint-go:
	golangci-lint run ./...

# Run platform integration tests (mock server mode, no Docker required)
test-platform-integration:
	go test ./internal/ipc/... -run TestIPCPlatform -v -count=1
	npx -w nightgauge-vscode vitest run tests/integration/authFlowsIntegration.test.ts

# Run E2E tests against real Docker Compose platform
test-e2e-docker:
	bash scripts/test-e2e-platform.sh

# --- GitLab CE integration harness (#3366, Wave 5-2 of #3349) ---

GITLAB_E2E_PORT ?= 8929
GITLAB_E2E_URL  ?= http://localhost:$(GITLAB_E2E_PORT)
GITLAB_ROOT_PASSWORD ?= nightgauge-test
GITLAB_COMPOSE := tests/integration/docker-compose.gitlab.yml

# Boot GitLab CE locally and wait for /-/health.
integration-gitlab-up:
	@echo "Starting GitLab CE harness..."
	docker compose -f $(GITLAB_COMPOSE) up -d
	@echo "Waiting for GitLab CE to become ready (max 5 min)..."
	@i=0; until curl -sf $(GITLAB_E2E_URL)/-/health > /dev/null 2>&1; do \
		i=$$((i+1)); if [ $$i -gt 60 ]; then \
			echo "ERROR: GitLab CE did not become healthy within 5 minutes — check 'docker logs gitlab-ce'"; exit 1; \
		fi; \
		echo "  waiting... ($$i/60)"; sleep 5; \
	done
	@echo "GitLab CE ready at $(GITLAB_E2E_URL)"

# Tear down the harness (idempotent).
integration-gitlab-down:
	docker compose -f $(GITLAB_COMPOSE) down -v

# Full local run: boot, seed, test, tear down.
integration-gitlab: integration-gitlab-up
	@echo "Generating root PAT via initial-root-password login..."
	@ROOT_TOKEN=$$(curl -sf -X POST "$(GITLAB_E2E_URL)/oauth/token" \
		-d "grant_type=password&username=root&password=$(GITLAB_ROOT_PASSWORD)" | jq -r '.access_token'); \
	if [ -z "$$ROOT_TOKEN" ] || [ "$$ROOT_TOKEN" = "null" ]; then \
		echo "ERROR: failed to obtain root oauth token"; exit 1; \
	fi; \
	echo "Running seeder..."; \
	GITLAB_URL=$(GITLAB_E2E_URL) GITLAB_ROOT_TOKEN=$$ROOT_TOKEN \
		go run ./tests/integration/cmd/seed > /tmp/gitlab-fixtures.json; \
	echo "Running integration tests..."; \
	GITLAB_E2E_URL=$(GITLAB_E2E_URL) \
	GITLAB_ROOT_TOKEN=$$ROOT_TOKEN \
	IB_FORGE=gitlab \
		go test -tags integration ./tests/integration/... -count=1 -timeout 600s -v
	$(MAKE) integration-gitlab-down

# --- Dockerized Mattermost integration harness (#3381) ---

MATTERMOST_E2E_PORT ?= 8065
MATTERMOST_E2E_URL  ?= http://localhost:$(MATTERMOST_E2E_PORT)
MATTERMOST_ADMIN_USER ?= admin
MATTERMOST_ADMIN_PASSWORD ?= Nightgauge-Test-1
MATTERMOST_COMPOSE := tests/integration/docker-compose.mattermost.yml

# Boot Mattermost + Postgres locally and wait for /api/v4/system/ping.
integration-mattermost-up:
	@echo "Starting Mattermost harness..."
	docker compose -f $(MATTERMOST_COMPOSE) up -d
	@echo "Waiting for Mattermost to become ready (max 90s)..."
	@i=0; until curl -sf $(MATTERMOST_E2E_URL)/api/v4/system/ping > /dev/null 2>&1; do \
		i=$$((i+1)); if [ $$i -gt 30 ]; then \
			echo "ERROR: Mattermost did not become healthy within 90s — check 'docker logs mattermost-ce'"; exit 1; \
		fi; \
		echo "  waiting... ($$i/30)"; sleep 3; \
	done
	@echo "Mattermost ready at $(MATTERMOST_E2E_URL)"

# Tear down the harness, removing named volumes (idempotent).
integration-mattermost-down:
	docker compose -f $(MATTERMOST_COMPOSE) down -v

# Full local run: boot, test (the suite seeds fixtures in TestMain), tear down.
integration-mattermost: integration-mattermost-up
	@echo "Running Mattermost integration tests..."
	MATTERMOST_E2E_URL=$(MATTERMOST_E2E_URL) \
	MATTERMOST_ADMIN_USER=$(MATTERMOST_ADMIN_USER) \
	MATTERMOST_ADMIN_PASSWORD=$(MATTERMOST_ADMIN_PASSWORD) \
		go test -tags integration ./tests/integration/mattermost/... -count=1 -timeout 300s -v
	$(MAKE) integration-mattermost-down

# Run cross-forge contract suite (TestParityContract_* + TestForgeContract_*)
# and print a per-method PASS/FAIL matrix. The matrix surfaces adapter+method
# pairs at a glance so a regression in either GitHub or GitLab adapters is
# attributed before reading the full test log.
test-parity:
	@go test -run 'TestParityContract|TestForgeContract' ./... -v -count=1 2>&1 | \
	  tee /tmp/parity-report.txt; \
	echo ""; \
	echo "=== Parity Pass/Fail Matrix ==="; \
	grep -E "^\s*--- (PASS|FAIL): " /tmp/parity-report.txt | \
	  sed 's/^[[:space:]]*--- //' | \
	  awk '{printf "%-8s %s\n", $$1, $$2}'

# Clean Go build artifacts
clean-go:
	rm -rf $(BIN_DIR)
