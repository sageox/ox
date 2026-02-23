# Makefile for ox CLI tool

.PHONY: help build install clean dev run test test-all test-slow test-integration test-benchmark test-sequential test-profile test-watch coverage smoke-test lint format release release-snapshot dist install-hooks docs docs-publish refresh-friction-catalog bump-version verify-version

# Variables
GO := go
BINARY_NAME := ox
# Single source of truth: internal/version/version.go
VERSION := $(shell grep 'Version.*=' internal/version/version.go | head -1 | sed 's/.*"\(.*\)"/\1/')
BUILD_TIME := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
GOPATH := $(shell go env GOPATH)
LDFLAGS := -ldflags "-X github.com/sageox/ox/internal/version.Version=$(VERSION) -X github.com/sageox/ox/internal/version.BuildDate=$(BUILD_TIME) -X github.com/sageox/ox/internal/version.GitCommit=$(GIT_COMMIT)"

# Build targets
build: ## Build the ox binary to bin/ox
	@echo "Building $(BINARY_NAME) $(VERSION)..."
	@mkdir -p bin
	$(GO) build $(LDFLAGS) -o bin/$(BINARY_NAME) ./cmd/ox
	@echo "Build complete: bin/$(BINARY_NAME)"

install: ## Install ox to $GOPATH/bin
	@echo "Installing $(BINARY_NAME) to $(GOPATH)/bin..."
	$(GO) install $(LDFLAGS) ./cmd/ox
	@echo "Installed $(BINARY_NAME) to $(GOPATH)/bin/$(BINARY_NAME)"

clean: ## Remove build artifacts
	@echo "Cleaning build artifacts..."
	@rm -rf bin/ dist/ tmp/
	@rm -f $(BINARY_NAME)
	@rm -f coverage.out coverage.html
	@echo "Clean complete"

# Development
dev: ## Run with air hot reload
	@which air > /dev/null || (echo "air not found. Install with: go install github.com/air-verse/air@latest" && exit 1)
	air -c .config/air.toml

run: build ## Build and run ox
	@./bin/$(BINARY_NAME)

# Testing (uses gotestsum for human-readable colorized output)
GOTESTSUM := $(shell which gotestsum 2>/dev/null || echo "go run gotest.tools/gotestsum@latest")

test: ## Run tests with race detection (skips slow tests >500ms)
	@echo "Running tests (skipping slow tests)..."
	@time $(GOTESTSUM) --format pkgname-and-test-fails -- -short -race -p 8 -parallel 32 -coverprofile=coverage.out -covermode=atomic ./...

test-all: ## Run all tests including slow tests (timeouts, delays)
	@echo "Running all tests including slow tests..."
	@time $(GOTESTSUM) --format pkgname-and-test-fails -- -race -p 8 -parallel 32 -coverprofile=coverage.out -covermode=atomic ./...

test-slow: ## Run slow tests (build tag: slow) - includes real Claude sessions
	@echo "Running slow tests (requires claude CLI and ANTHROPIC_API_KEY)..."
	@time $(GOTESTSUM) --format pkgname-and-test-fails -- -tags=slow -race -timeout=5m ./...

test-integration: ## Run integration tests (build tag: integration) - full E2E with Claude
	@echo "Running integration tests (requires claude CLI and ANTHROPIC_API_KEY)..."
	@time $(GOTESTSUM) --format pkgname-and-test-fails -- -tags=integration -race -timeout=10m ./...

test-benchmark: ## Run prime efficiency benchmarks (requires claude CLI) - ~80 min, ~40 API calls
	@echo "Running prime efficiency benchmarks..."
	@time $(GOTESTSUM) --format pkgname-and-test-fails -- -tags=integration -run TestPrimeEfficiency -timeout=90m ./tests/integration/agents/benchmark/...

test-sequential: ## Run tests sequentially (for debugging race conditions)
	@echo "Running tests sequentially..."
	@time $(GOTESTSUM) --format pkgname-and-test-fails -- -race -p 1 -parallel 1 -coverprofile=coverage.out -covermode=atomic ./...

test-profile: ## Visualize test execution timeline (requires vgt)
	@echo "Profiling test execution..."
	@which vgt > /dev/null 2>&1 || (echo "Installing vgt..." && go install github.com/roblaszczak/vgt@latest)
	$(GO) test -json -race ./... 2>&1 | vgt
	@echo "Profile complete"

test-watch: ## Run tests in watch mode (requires gotestsum)
	@which gotestsum > /dev/null || (echo "gotestsum not found. Install with: go install gotest.tools/gotestsum@latest" && exit 1)
	gotestsum --watch

coverage: test ## Generate coverage report
	@echo "Generating coverage report..."
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

smoke-test: build ## Run smoke tests against SageOx cloud (requires SAGEOX_CI_PASSWORD)
	@echo "Running smoke tests..."
	@./scripts/smoketest/smoke-test.sh

# Code quality
lint: ## Run golangci-lint
	@which golangci-lint > /dev/null || (echo "golangci-lint not found. Install from https://golangci-lint.run/usage/install/" && exit 1)
	golangci-lint run -c .config/golangci.yml ./...

format: ## Format code with gofmt and goimports
	@echo "Formatting code..."
	@which goimports > /dev/null || (echo "goimports not found. Install with: go install golang.org/x/tools/cmd/goimports@latest" && exit 1)
	@gofmt -s -w .
	@goimports -w .
	@echo "Format complete"

# Git hooks
install-hooks: ## Install git pre-commit hooks
	@echo "Installing git hooks..."
	@cp scripts/hooks/pre-commit .git/hooks/pre-commit
	@chmod +x .git/hooks/pre-commit
	@echo "Git hooks installed"

# Distribution
release: ## Create release with goreleaser (requires GITHUB_TOKEN)
	@which goreleaser > /dev/null || (echo "goreleaser not found. Install from https://goreleaser.com/install/" && exit 1)
	goreleaser release -f .config/goreleaser.yml --clean

release-snapshot: ## Create snapshot release (no publish)
	@which goreleaser > /dev/null || (echo "goreleaser not found. Install from https://goreleaser.com/install/" && exit 1)
	goreleaser release -f .config/goreleaser.yml --snapshot --clean

dist: ## Cross-compile for linux/darwin/windows (amd64 and arm64)
	@echo "Building distribution binaries..."
	@mkdir -p dist
	@echo "Building linux/amd64..."
	@GOOS=linux GOARCH=amd64 $(GO) build $(LDFLAGS) -o dist/$(BINARY_NAME)-linux-amd64 ./cmd/ox
	@echo "Building linux/arm64..."
	@GOOS=linux GOARCH=arm64 $(GO) build $(LDFLAGS) -o dist/$(BINARY_NAME)-linux-arm64 ./cmd/ox
	@echo "Building darwin/amd64..."
	@GOOS=darwin GOARCH=amd64 $(GO) build $(LDFLAGS) -o dist/$(BINARY_NAME)-darwin-amd64 ./cmd/ox
	@echo "Building darwin/arm64..."
	@GOOS=darwin GOARCH=arm64 $(GO) build $(LDFLAGS) -o dist/$(BINARY_NAME)-darwin-arm64 ./cmd/ox
	@echo "Building windows/amd64..."
	@GOOS=windows GOARCH=amd64 $(GO) build $(LDFLAGS) -o dist/$(BINARY_NAME)-windows-amd64.exe ./cmd/ox
	@echo "Building windows/arm64..."
	@GOOS=windows GOARCH=arm64 $(GO) build $(LDFLAGS) -o dist/$(BINARY_NAME)-windows-arm64.exe ./cmd/ox
	@echo "Distribution build complete: dist/"

# Documentation
docs: ## Generate CLI reference docs
	@echo "Generating CLI reference documentation..."
	$(GO) run ./cmd/ox docs --output docs/reference
	@echo "Documentation generated: docs/reference/"

docs-publish: docs ## Publish docs to GitHub Packages
	@echo "Publishing docs to GitHub Packages..."
	cd docs && npm publish
	@echo "Published @sageox/cli-docs"

# Friction catalog
refresh-friction-catalog: ## Fetch friction catalog from API and generate Go code
	@echo "Fetching friction catalog from API..."
	@mkdir -p internal/uxfriction
	@curl -sf -H "Authorization: Bearer $${INTERNAL_AUTH_TOKEN}" \
		"$${SAGEOX_API_URL:-https://api.sageox.ai}/api/internal/cli/friction/catalog" \
		> tmp/friction-catalog.json || (echo "Failed to fetch catalog. Set INTERNAL_AUTH_TOKEN and SAGEOX_API_URL." && exit 1)
	@echo "Generating catalog_generated.go..."
	@go run ./scripts/gen-friction-catalog/main.go < tmp/friction-catalog.json > internal/uxfriction/catalog_generated.go
	@gofmt -w internal/uxfriction/catalog_generated.go
	@echo "Catalog updated: internal/uxfriction/catalog_generated.go"

# Version management
bump-version: ## Bump version across all files (usage: make bump-version NEW_VERSION=0.10.0)
	@if [ -z "$(NEW_VERSION)" ]; then \
		echo "Usage: make bump-version NEW_VERSION=0.10.0"; \
		exit 1; \
	fi
	@./scripts/version-bump.sh $(NEW_VERSION)

verify-version: ## Verify all version files are in sync
	@./scripts/check-versions.sh

# Help
help: ## Display available targets
	@echo "Available targets for $(BINARY_NAME):"
	@echo ""
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "Variables:"
	@echo "  VERSION:    $(VERSION)"
	@echo "  BUILD_TIME: $(BUILD_TIME)"
	@echo "  GOPATH:     $(GOPATH)"

# Default target
.DEFAULT_GOAL := help
