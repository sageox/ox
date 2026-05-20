# Makefile for ox CLI tool

.PHONY: help build build-ox build-adapters install install-adapters clean dev run test test-cover test-all test-slow test-integration test-preflight test-digital-twin test-ledger-twin test-benchmark test-sequential test-profile test-watch coverage coverage-report coverage-func coverage-baseline coverage-diff coverage-check build-cover coverage-integration smoke-test lint lint-test-env format release release-snapshot dist install-hooks docs docs-publish refresh-friction-catalog bump-version verify-version beads-setup

# Variables
GO := go
BINARY_NAME := ox
# Single source of truth: internal/version/version.go
VERSION := $(shell grep 'Version.*=' internal/version/version.go | head -1 | sed 's/.*"\(.*\)"/\1/')
BUILD_TIME := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
GOPATH := $(shell go env GOPATH)
LDFLAGS := -ldflags "-X github.com/sageox/ox/internal/version.Version=$(VERSION) -X github.com/sageox/ox/internal/version.BuildDate=$(BUILD_TIME) -X github.com/sageox/ox/internal/version.GitCommit=$(GIT_COMMIT)"
ADAPTER_LDFLAGS := -ldflags "-s -w"

# ── Agent-Friendly Output ──────────────────────────────────────────────
# Quiet by default. AI coding agents are the primary callers of
# test/lint/build targets. They parse exit codes — verbose progress
# wastes context window tokens. Humans: V=1 make <target>.
#
# V=0 (default): suppress echo lines, use failure-only test format,
#                hide skip summaries and empty packages.
# V=1 (verbose): full echo output, per-package test format, timing.
#
# Convention: Automake silent rules, Linux kernel V=, K8s KUBE_VERBOSE.
# ────────────────────────────────────────────────────────────────────────
V ?= 0
say = $(if $(filter 1,$(V)),@echo $(1),@:)
GOTESTSUM_FMT = $(if $(filter 1,$(V)),pkgname,pkgname-and-test-fails)
GOTESTSUM_LEAN = $(if $(filter 1,$(V)),,--hide-summary skipped --format-hide-empty-pkg)
TIME_CMD = $(if $(filter 1,$(V)),time,)

# Bundled adapters (shipped in release tarballs alongside ox)
ADAPTERS := ox-adapter-claude-code ox-adapter-gemini ox-adapter-codex ox-adapter-amp ox-adapter-opencode ox-adapter-pi ox-adapter-aider ox-adapter-droid

# Build targets
# Targets below are agent-friendly by default (quiet). V=1 for verbose.
build: build-ox build-adapters ## Build ox and all bundled adapters to bin/

build-ox: ## Build the ox binary to bin/ox
	$(call say,"Building $(BINARY_NAME) $(VERSION)...")
	@mkdir -p bin
	@$(GO) build $(LDFLAGS) -o bin/$(BINARY_NAME) ./cmd/ox
	$(call say,"Build complete: bin/$(BINARY_NAME)")

build-adapters: ## Build all bundled adapter binaries to bin/
	$(call say,"Building adapters...")
	@mkdir -p bin
	@for adapter in $(ADAPTERS); do \
		$(if $(filter 1,$(V)),echo "  Building $$adapter...";,) \
		$(GO) build $(ADAPTER_LDFLAGS) -o bin/$$adapter ./cmd/$$adapter; \
	done
	$(call say,"Adapters built: $(ADAPTERS)")

install: install-ox install-adapters ## Install ox and adapters to $GOPATH/bin

install-ox: ## Install ox to $GOPATH/bin
	@echo "Installing $(BINARY_NAME) to $(GOPATH)/bin..."
	$(GO) install $(LDFLAGS) ./cmd/ox
	@echo "Installed $(BINARY_NAME) to $(GOPATH)/bin/$(BINARY_NAME)"

install-adapters: ## Install bundled adapters to $GOPATH/bin
	@echo "Installing adapters to $(GOPATH)/bin..."
	@for adapter in $(ADAPTERS); do \
		$(GO) install $(ADAPTER_LDFLAGS) ./cmd/$$adapter; \
		echo "  Installed $$adapter"; \
	done

clean: ## Remove build artifacts
	@echo "Cleaning build artifacts..."
	@rm -rf bin/ dist/ tmp/
	@rm -f $(BINARY_NAME)
	@rm -f coverage.out coverage.html coverage-all.out .coverage-baseline.out
	@echo "Clean complete"

# Development
dev: ## Run with air hot reload
	@which air > /dev/null || (echo "air not found. Install with: go install github.com/air-verse/air@latest" && exit 1)
	air -c .config/air.toml

run: build ## Build and run ox
	@./bin/$(BINARY_NAME)

# Testing (uses gotestsum for human-readable colorized output)
#
# Test Tiers:
#   fast  (make test)             — Unit tests <500ms. No git clone, no network, NO coverage instrumentation.
#                                   Runs on every commit. Target: <60s wall.
#   fast+cov (make test-cover)    — Same as fast but with coverage (~15-20% slower). Local use.
#   full  (make test-all)         — All unit tests including expensive ones (git clone, SQLite, LFS).
#                                   Coverage collection lives here. Target: <5min wall.
#   slow  (make test-slow)        — Tests requiring real ox binary (build tag: slow). No Claude needed.
#   integration — E2E with real Claude sessions. Lives in sageox/ox-test-harness (private).
#
# Tier criteria for `make test` (fast):
#   - No exec.Command (git or otherwise) except via an in-process fake.
#   - No time.Sleep > 5ms on the success path.
#   - No real SQLite/Bleve file I/O.
#   - No os.Setenv (use t.Setenv); tests should call t.Parallel().
#   - httptest.NewServer OK; no reliance on real timeouts.
#   - Only env-gated t.Skip (e.g. git binary missing).
#
# Recommended usage:
#   Coding:    make test           — Fast feedback (target <60s, no coverage)
#   Pre-PR:    make test-preflight — Full + slow + lint (~3-5min, with coverage)
#   Release:   make test-all test-slow (+ integration tests from ox-test-harness)
#
# Output: quiet by default (agent-friendly). V=1 for verbose.
#
GOTESTSUM := $(shell which gotestsum 2>/dev/null || echo "go run gotest.tools/gotestsum@latest")

# Isolate test `git` invocations from the developer's global config.
# Many tests build scratch repos in t.TempDir() and run `git init` +
# `git commit`. Without isolation, git inherits the user's `~/.gitconfig`
# — including `commit.gpgsign=true` and `user.signingkey` pointing at an
# encrypted SSH key — and `git commit` blocks on a passphrase prompt
# that the test runner can't satisfy. This manifests as cascade failures
# in internal/codedb/index, internal/doctor, internal/repotools,
# internal/secrets, etc.
#
# Disabling global config also wipes the runner's `user.name`/`user.email`,
# so we supply identity via the GIT_{AUTHOR,COMMITTER}_{NAME,EMAIL} env
# vars — git respects these in lieu of config and they cannot be hijacked
# by signing/passphrase machinery.
#
# GIT_CONFIG_GLOBAL=/dev/null  → ignore $HOME/.gitconfig
# GIT_CONFIG_NOSYSTEM=1        → ignore /etc/gitconfig
# GIT_TERMINAL_PROMPT=0        → never prompt; fail fast on missing creds
# GIT_AUTHOR_*  / GIT_COMMITTER_*  → identity without needing config
TEST_GIT_ISOLATION := \
	GIT_CONFIG_GLOBAL=/dev/null \
	GIT_CONFIG_NOSYSTEM=1 \
	GIT_TERMINAL_PROMPT=0 \
	GIT_AUTHOR_NAME=ox-test \
	GIT_AUTHOR_EMAIL=test@test.sageox.ai \
	GIT_COMMITTER_NAME=ox-test \
	GIT_COMMITTER_EMAIL=test@test.sageox.ai

# Targets below are agent-friendly by default (quiet). V=1 for verbose.
test: ## Run fast tests — unit tests <500ms, race detection, no coverage (every commit)
	$(call say,"Running fast tests (skipping >500ms, no coverage)...")
	@$(TEST_GIT_ISOLATION) $(TIME_CMD) $(GOTESTSUM) --format $(GOTESTSUM_FMT) $(GOTESTSUM_LEAN) -- -short -race -p 8 -parallel 32 ./...

test-cover: ## Run fast tests with coverage collection (~15-20% slower than `make test`)
	$(call say,"Running fast tests with coverage...")
	@$(TEST_GIT_ISOLATION) $(TIME_CMD) $(GOTESTSUM) --format $(GOTESTSUM_FMT) $(GOTESTSUM_LEAN) -- -short -race -p 8 -parallel 32 -coverprofile=coverage.out -covermode=atomic ./...

test-all: ## Run all unit tests including expensive ones (git clone, SQLite, LFS) with coverage
	$(call say,"Running all tests including expensive tests...")
	@$(TEST_GIT_ISOLATION) $(TIME_CMD) $(GOTESTSUM) --format $(GOTESTSUM_FMT) $(GOTESTSUM_LEAN) -- -race -p 8 -parallel 32 -coverprofile=coverage.out -covermode=atomic ./...

test-slow: ## Run slow tests (build tag: slow) — requires real ox binary, no Claude needed
	$(call say,"Running slow tests (requires built ox binary)...")
	@$(TEST_GIT_ISOLATION) $(TIME_CMD) $(GOTESTSUM) --format $(GOTESTSUM_FMT) $(GOTESTSUM_LEAN) -- -tags=slow -race -timeout=10m ./...

test-integration: ## Integration tests live in sageox/ox-test-harness
	@echo "Coding agent integration tests are in sageox/ox-test-harness."
	@exit 1

check-no-git-lfs-shell: ## Ensure no code shells out to git-lfs binary (see .claude/rules/lfs-no-git-lfs-binary.md)
	@if grep -r --include='*.go' -nE 'exec\.(Command|CommandContext)\("git",\s*"lfs"|exec\.(Command|CommandContext)\("git-lfs"|LookPath\("git-lfs"\)' . 2>/dev/null \
		| grep -v '_test\.go:' | grep -v 'vendor/' | grep -v 'doc\.go:' \
		| grep -vE ':[0-9]+:[[:space:]]*//' ; then \
		echo "ERROR: ox must not shell out to git-lfs. See .claude/rules/lfs-no-git-lfs-binary.md"; \
		exit 1; \
	fi

sync-gitleaks-rules: ## Regenerate the gitleaks-derived detector catalog from a pinned gitleaks.toml
	@echo "Regenerating internal/session/gitleaks_generated.go..."
	@cd internal/session/cmd/gitleaks-port && go run . \
		-in gitleaks-v8.30.1.toml \
		-out ../../gitleaks_generated.go \
		-gitleaks-version v8.30.1

check-raw-writer-chokepoint: ## Ensure raw.jsonl is only opened via session.RawWriter (ox-h20u)
	@# Any os.OpenFile/Create/WriteFile of a raw.jsonl path outside the
	@# canonical chokepoint (internal/session/raw_writer.go) is a redaction
	@# bypass risk. Test files and the chokepoint itself are allowed.
	@violations=$$(grep -rnE '(os\.OpenFile|os\.Create|os\.WriteFile)\([^)]*raw[._]?jsonl' \
		--include='*.go' . 2>/dev/null \
		| grep -v '_test\.go:' \
		| grep -v 'vendor/' \
		| grep -v 'internal/session/raw_writer\.go:' \
		| grep -vE ':[0-9]+:[[:space:]]*//') ; \
	if [ -n "$$violations" ] ; then \
		echo "ERROR: raw.jsonl must be written via session.RawWriter (see internal/session/raw_writer.go)"; \
		echo "Violations:"; \
		echo "$$violations"; \
		exit 1; \
	fi

test-preflight: lint check-no-git-lfs-shell check-raw-writer-chokepoint test-all test-slow ## Pre-PR quality gate: lint + all unit tests + slow tests

test-digital-twin: test-ledger-twin test-kb-twin ## Digital twin tests (team_context_twin pending, see ox-au5)

test-team-context-twin: ## Digital twin tests (generates fake team context for inspection)
	@echo "Running team context digital twin tests..."
	@time $(GOTESTSUM) --format pkgname-and-test-fails -- -tags=team_context_twin -v -count=1 -timeout=2m ./tests/team_context_twin/...

test-ledger-twin: ## Digital twin ledger tests (generates fake ledger for inspection)
	@echo "Running ledger digital twin tests..."
	@time $(GOTESTSUM) --format pkgname-and-test-fails -- -tags=ledger_twin -v -count=1 -timeout=2m ./tests/ledger_twin/...

test-kb-twin: ## Digital twin kb tests (drives syncBubbles + GC against real bare repos)
	@echo "Running kb digital twin tests..."
	@time $(GOTESTSUM) --format pkgname-and-test-fails -- -tags=kb_twin -v -count=1 -timeout=5m ./tests/kb_twin/...

test-benchmark: ## Run prime efficiency benchmarks (requires claude CLI) - ~80 min, ~40 API calls
	@echo "Running prime efficiency benchmarks..."
	@time $(GOTESTSUM) --format pkgname-and-test-fails -- -tags=integration -run TestPrimeEfficiency -timeout=90m ./tests/integration/agents/benchmark/...

test-sequential: ## Run tests sequentially (for debugging race conditions)
	$(call say,"Running tests sequentially...")
	@$(TIME_CMD) $(GOTESTSUM) --format $(GOTESTSUM_FMT) $(GOTESTSUM_LEAN) -- -race -p 1 -parallel 1 -coverprofile=coverage.out -covermode=atomic ./...

test-profile: ## Visualize test execution timeline (requires vgt)
	@echo "Profiling test execution..."
	@which vgt > /dev/null 2>&1 || (echo "Installing vgt..." && go install github.com/roblaszczak/vgt@latest)
	$(GO) test -json -race ./... 2>&1 | vgt
	@echo "Profile complete"

test-watch: ## Run tests in watch mode (requires gotestsum)
	@which gotestsum > /dev/null || (echo "gotestsum not found. Install with: go install gotest.tools/gotestsum@latest" && exit 1)
	gotestsum --watch

# Coverage
COVERDIR := tmp/coverage
COVERAGE_THRESHOLD ?= 50

coverage: test-cover ## Run fast tests with coverage and open report
	@$(GO) tool cover -func=coverage.out | tail -1
	@$(GO) tool cover -html=coverage.out -o coverage.html
	@open coverage.html 2>/dev/null || xdg-open coverage.html 2>/dev/null || echo "Open coverage.html in your browser"

coverage-report: ## Open coverage report from last test run (no re-run)
	@test -f coverage.out || (echo "No coverage.out found. Run 'make test-cover' or 'make test-all' first." && exit 1)
	@$(GO) tool cover -func=coverage.out | tail -1
	@$(GO) tool cover -html=coverage.out -o coverage.html
	@open coverage.html 2>/dev/null || xdg-open coverage.html 2>/dev/null || echo "Open coverage.html in your browser"

coverage-func: ## Show per-function coverage in terminal
	@test -f coverage.out || (echo "No coverage.out found. Run 'make test-cover' or 'make test-all' first." && exit 1)
	@$(GO) tool cover -func=coverage.out

coverage-baseline: test-cover ## Save current coverage as baseline for diffs
	@cp coverage.out .coverage-baseline.out
	@$(GO) tool cover -func=.coverage-baseline.out | tail -1
	@echo "Baseline saved."

coverage-diff: test-cover ## Show coverage change vs saved baseline
	@test -f .coverage-baseline.out || (echo "No baseline. Run 'make coverage-baseline' first." && exit 1)
	@echo "=== Coverage: baseline → current ==="
	@echo "Baseline: $$($(GO) tool cover -func=.coverage-baseline.out | grep total: | awk '{print $$3}')"
	@echo "Current:  $$($(GO) tool cover -func=coverage.out | grep total: | awk '{print $$3}')"
	@echo ""
	@echo "Changed functions:"
	@diff <($(GO) tool cover -func=.coverage-baseline.out) <($(GO) tool cover -func=coverage.out) | grep '^[<>]' | head -30 || echo "  (none)"

coverage-check: ## Fail if coverage is below threshold (default: 50%)
	@test -f coverage.out || (echo "No coverage.out found. Run 'make test' first." && exit 1)
	@total=$$($(GO) tool cover -func=coverage.out | grep total: | awk '{print $$3}' | tr -d '%'); \
	 echo "Coverage: $${total}% (threshold: $(COVERAGE_THRESHOLD)%)"; \
	 if [ $$(echo "$${total} < $(COVERAGE_THRESHOLD)" | bc) -eq 1 ]; then \
	   echo "FAIL: coverage below threshold"; exit 1; \
	 fi

build-cover: ## Build ox binary with coverage instrumentation
	@mkdir -p bin $(COVERDIR)/integration
	@GOCOVERDIR=$(COVERDIR)/integration $(GO) build -cover $(LDFLAGS) -o bin/$(BINARY_NAME)-cover ./cmd/ox
	@echo "Instrumented binary: bin/$(BINARY_NAME)-cover"
	@echo "Run with: GOCOVERDIR=$(COVERDIR)/integration bin/$(BINARY_NAME)-cover ..."

coverage-integration: build-cover test ## Merge unit + integration coverage
	@echo "Converting integration profile..."
	@$(GO) tool covdata textfmt -i=$(COVERDIR)/integration -o=$(COVERDIR)/integration.out 2>/dev/null || true
	@echo "Merging profiles..."
	@if [ -f $(COVERDIR)/integration.out ]; then \
	  $(GO) tool covdata merge -i=$(COVERDIR)/integration -o=$(COVERDIR)/merged 2>/dev/null && \
	  $(GO) tool covdata textfmt -i=$(COVERDIR)/merged -o=coverage-all.out; \
	else \
	  cp coverage.out coverage-all.out; \
	fi
	@$(GO) tool cover -func=coverage-all.out | tail -1
	@echo "Combined profile: coverage-all.out"

smoke-test: build ## Run smoke tests against SageOx cloud (requires SAGEOX_CI_PASSWORD)
	@echo "Running smoke tests..."
	@./scripts/smoketest/smoke-test.sh

# Code quality
# Targets below are agent-friendly by default (quiet). V=1 for verbose.
lint: lint-test-env ## Run golangci-lint
	@which golangci-lint > /dev/null || (echo "golangci-lint not found. Install from https://golangci-lint.run/usage/install/" && exit 1)
	@golangci-lint run -c .config/golangci.yml ./...

lint-test-env: ## Check that test files use testguard instead of os.Environ()
	$(call say,"Checking for os.Environ() in test files...")
	@if grep -rn 'os\.Environ()' --include='*_test.go' . | grep -v '// safe:' | grep -v 'internal/testguard/' > /dev/null 2>&1; then \
		echo "ERROR: os.Environ() found in test files without '// safe:' annotation:"; \
		grep -rn 'os\.Environ()' --include='*_test.go' . | grep -v '// safe:' | grep -v 'internal/testguard/'; \
		echo ""; \
		echo "Use testguard.RunOx() for ox subprocesses, or add '// safe: <reason>' comment."; \
		exit 1; \
	fi
	$(call say,"OK: no unguarded os.Environ() in test files")

format: ## Format code with gofmt and goimports
	$(call say,"Formatting code...")
	@which goimports > /dev/null || (echo "goimports not found. Install with: go install golang.org/x/tools/cmd/goimports@latest" && exit 1)
	@gofmt -s -w .
	@goimports -w .
	$(call say,"Format complete")

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

# ── UX component catalog ────────────────────────────────────────────
# Build a self-contained catalog/cli/index.html that ships to
# sageox-design.netlify.app/catalog/cli/.
# Spec: sageox-design/catalog/README.md
# Rule: .claude/rules/design.md

CATALOG_OUT := .context/catalog-out/cli
CATALOG_ASSETS := $(CATALOG_OUT)/assets
CATALOG_VENDOR := tmp/catalog-vendor
ASCIINEMA_PLAYER_VERSION := 3.8.1

.PHONY: catalog-build catalog-svgs catalog-casts catalog-html catalog-vendor catalog-publish check-no-binary-bloat

catalog-build: catalog-vendor catalog-svgs catalog-casts catalog-html ## Build self-contained catalog/cli/index.html
	@echo "✓ catalog: $(CATALOG_OUT)/index.html"

catalog-vendor: ## Fetch asciinema-player into tmp/catalog-vendor (text-only)
	@mkdir -p $(CATALOG_VENDOR)
	@test -f $(CATALOG_VENDOR)/asciinema-player.min.js || \
		curl -sSfL "https://cdn.jsdelivr.net/npm/asciinema-player@$(ASCIINEMA_PLAYER_VERSION)/dist/bundle/asciinema-player.min.js" \
			-o $(CATALOG_VENDOR)/asciinema-player.min.js
	@test -f $(CATALOG_VENDOR)/asciinema-player.css || \
		curl -sSfL "https://cdn.jsdelivr.net/npm/asciinema-player@$(ASCIINEMA_PLAYER_VERSION)/dist/bundle/asciinema-player.css" \
			-o $(CATALOG_VENDOR)/asciinema-player.css

catalog-svgs: build-ox ## Render freeze SVG snapshots (light + dark) per entry
	@mkdir -p $(CATALOG_ASSETS)
	@if ! command -v freeze >/dev/null 2>&1; then \
		echo "  ⚠ freeze not installed; SVG snapshots skipped"; \
		echo "    install: brew install charmbracelet/tap/freeze"; \
	else \
		for name in $$(./bin/$(BINARY_NAME) dev catalog --json | jq -r '.components[].name'); do \
			CLICOLOR_FORCE=1 FORCE_COLOR=1 COLORTERM=truecolor TERM=xterm-256color \
				./bin/$(BINARY_NAME) dev catalog --component=$$name 2>/dev/null > $(CATALOG_ASSETS)/$$name.ans; \
			freeze --output $(CATALOG_ASSETS)/$$name.dark.svg \
				--language ansi \
				--font.family "JetBrains Mono" --font.size 13 \
				--padding 16 --margin 0 --line-height 1.4 \
				--background "#111518" --window=false $(CATALOG_ASSETS)/$$name.ans \
				&& echo "  freeze: $$name.dark.svg" || true; \
			freeze --output $(CATALOG_ASSETS)/$$name.light.svg \
				--language ansi \
				--theme github \
				--font.family "JetBrains Mono" --font.size 13 \
				--padding 16 --margin 0 --line-height 1.4 \
				--background "#f5f3ed" --window=false $(CATALOG_ASSETS)/$$name.ans \
				&& echo "  freeze: $$name.light.svg" || true; \
			rm -f $(CATALOG_ASSETS)/$$name.ans; \
		done; \
	fi

catalog-casts: build-ox ## Record asciinema .cast for animated components
	@mkdir -p $(CATALOG_ASSETS)
	@if ! command -v asciinema >/dev/null 2>&1; then \
		echo "  ⚠ asciinema not installed; .cast recordings skipped"; \
		echo "    install: brew install asciinema"; \
	else \
		for name in $$(./bin/$(BINARY_NAME) dev catalog --json | jq -r '.components[] | select(.renderer=="asciinema") | .name'); do \
			CLICOLOR_FORCE=1 FORCE_COLOR=1 asciinema rec --quiet --overwrite --cols=80 --rows=24 \
				--command="./bin/$(BINARY_NAME) dev catalog --component=$$name" \
				$(CATALOG_ASSETS)/$$name.cast 2>/dev/null \
				&& echo "  asciinema: $$name.cast" || true; \
		done; \
		$(MAKE) -s catalog-casts-v2; \
	fi

catalog-casts-v2: ## Rewrite v3 cast headers to v2 (player compat)
	@for f in $(CATALOG_ASSETS)/*.cast; do \
		[ -f "$$f" ] || continue; \
		header=$$(head -1 "$$f"); \
		case "$$header" in \
			*'"version":3'*|*'"version": 3'*) \
				v2=$$(echo "$$header" | jq -c '{version: 2, width: .term.cols, height: .term.rows, timestamp: .timestamp, env: .env, title: .title}'); \
				{ echo "$$v2"; tail -n +2 "$$f"; } > "$$f.tmp" && mv "$$f.tmp" "$$f"; \
				echo "  cast→v2: $$(basename $$f)"; \
				;; \
		esac; \
	done

catalog-html: build-ox ## Emit self-contained catalog/cli/index.html
	@mkdir -p $(CATALOG_OUT)
	@./bin/$(BINARY_NAME) dev catalog \
		--export=$(CATALOG_OUT)/.. \
		--assets-dir=$(CATALOG_ASSETS) \
		--player-js=$(CATALOG_VENDOR)/asciinema-player.min.js \
		--player-css=$(CATALOG_VENDOR)/asciinema-player.css
	@$(MAKE) -s check-no-binary-bloat CATALOG_PATH=$(CATALOG_OUT)

catalog-publish: catalog-build ## rsync catalog to local sageox-design checkout (set SAGEOX_DESIGN_REPO)
	@bash scripts/publish-design-catalog.sh

check-no-binary-bloat: ## Fail if catalog dirs contain raster/binary assets
	@found=$$( \
		find docs/design $(or $(CATALOG_PATH),$(CATALOG_OUT)) -type f \
			\( -name '*.png' -o -name '*.gif' -o -name '*.jpg' \
			   -o -name '*.jpeg' -o -name '*.mp4' -o -name '*.webm' \) \
			2>/dev/null \
	); \
	if [ -n "$$found" ]; then \
		echo "✗ binary assets detected in catalog paths (forbidden by .claude/rules/design.md rule #13):"; \
		echo "$$found" | sed 's/^/    /'; \
		exit 1; \
	fi
	@echo "✓ no binary catalog assets"

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

# Beads (issue tracking)
beads-setup: ## Bootstrap beads issue tracking (shared Dolt server + JSONL import)
	@echo "Setting up beads issue tracking..."
	@which bd > /dev/null || (echo "bd not found. Install with: brew install beads" && exit 1)
	@if [ -f .beads/issues.jsonl ]; then \
		echo "Found .beads/issues.jsonl ($$(wc -l < .beads/issues.jsonl | tr -d ' ') issues)"; \
	elif git show beads-sync:.beads/issues.jsonl > /dev/null 2>&1; then \
		echo "Extracting issues from beads-sync branch..."; \
		git show beads-sync:.beads/issues.jsonl > .beads/issues.jsonl; \
		echo "Extracted $$(wc -l < .beads/issues.jsonl | tr -d ' ') issues"; \
	else \
		echo "No issues found (fresh project)"; \
	fi
	@if bd list > /dev/null 2>&1; then \
		echo "Beads already working. Run 'bd doctor' to verify."; \
	else \
		echo "Initializing beads with shared server..."; \
		bd init --prefix ox --shared-server $$([ -f .beads/issues.jsonl ] && echo "--from-jsonl") --force; \
	fi
	@bd doctor --fix --yes
	@bd list --status=open > /dev/null || (echo "Beads setup failed. Run 'bd doctor' for details." && exit 1)
	@echo ""
	@echo "Beads setup complete. Run 'bd list --status=open' to see issues."

# Multi-agent compatibility testing (Docker-based clean-room environments)
## Agent integration tests and compatibility matrix live in sageox/ox-test-harness (private).
## See ~/Code/sageox/ox-test-harness/README.md for setup.

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

sec: ## Run AI security review (diff vs origin/main, $2 cost cap)
	$(call say,"Running security review (full 6-phase AI pipeline)...")
	@bash security/scripts/orchestrate.sh

sec-fast: ## Run deterministic security scanners only (no AI tier)
	$(call say,"Running deterministic security scanners...")
	@bash security/scripts/deterministic.sh

sec-install: ## Install security scanner binaries to ./bin/ (cached at ~/.cache/ox/security/bin)
	$(call say,"Installing security scanner binaries...")
	@bash security/scripts/install-bins.sh
