# Makefile for ox CLI tool

.PHONY: check-no-git-lfs-shell check-raw-writer-chokepoint check-session-meta-rmw check-codedb-guarded-open check-test-tiers test-tiers
.PHONY: help build build-ox build-adapters build-acceptance install install-adapters clean dev run test test-cover test-timings test-all test-slow test-fuzz test-browser test-integration test-acceptance test-acceptance-cover test-acceptance-run test-release test-agents test-preflight test-digital-twin test-digital-twin-cover test-cloud-api-twin test-ledger-twin test-benchmark test-sequential test-profile test-watch coverage coverage-report coverage-func coverage-baseline coverage-diff coverage-check coverage-ratchet coverage-ratchet-diff coverage-ratchet-test build-cover coverage-integration smoke-test lint lint-test-env format release release-snapshot dist install-hooks docs docs-check docs-publish refresh-friction-catalog bump-version verify-version check-release-drift beads-setup

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
ADAPTERS := ox-adapter-claude-code ox-adapter-gemini ox-adapter-codex ox-adapter-amp ox-adapter-opencode ox-adapter-pi ox-adapter-omp ox-adapter-aider ox-adapter-droid ox-adapter-goose
empty :=
space := $(empty) $(empty)
comma := ,
ACCEPTANCE_DIR := $(abspath tmp/acceptance)
ACCEPTANCE_OX_BIN ?= $(ACCEPTANCE_DIR)/$(BINARY_NAME)
ACCEPTANCE_GO_COVER_DIR ?=
ACCEPTANCE_INTEGRATION_COVER_FLAGS = $(if $(strip $(ACCEPTANCE_GO_COVER_DIR)),-coverprofile=$(ACCEPTANCE_GO_COVER_DIR)/integration-test.out -covermode=atomic,)
ACCEPTANCE_SLOW_COVER_FLAGS = $(if $(strip $(ACCEPTANCE_GO_COVER_DIR)),-coverprofile=$(ACCEPTANCE_GO_COVER_DIR)/slow-test.out -covermode=atomic,)
ACCEPTANCE_INTEGRATION_TESTS := TestCodeActivityE2E TestFreshInstall_MockServer_InitThenDoctor TestFreshInstall_MockServer_SyncUnavailableThenDoctorStillWorks
ifneq ($(filter darwin linux freebsd,$(shell $(GO) env GOOS)),)
ACCEPTANCE_INTEGRATION_TESTS += TestBackgroundDaemonSurvivesCommandCleanup
endif
ACCEPTANCE_SLOW_TESTS := TestIncrementalE2E_SingleAgent TestIncrementalE2E_CtrlC_AntiEntropy
TWIN_COVER_DIR ?=
CLOUD_TWIN_COVER_FLAGS = $(if $(strip $(TWIN_COVER_DIR)),-coverpkg=github.com/sageox/ox/internal/auth -coverprofile=$(TWIN_COVER_DIR)/cloud.out -covermode=atomic,)
LEDGER_TWIN_COVER_FLAGS = $(if $(strip $(TWIN_COVER_DIR)),-coverpkg=github.com/sageox/ox/internal/glance$(comma)github.com/sageox/ox/internal/ledger$(comma)github.com/sageox/ox/internal/carts -coverprofile=$(TWIN_COVER_DIR)/ledger.out -covermode=atomic,)
KB_TWIN_COVER_FLAGS = $(if $(strip $(TWIN_COVER_DIR)),-coverpkg=github.com/sageox/ox/internal/daemon$(comma)github.com/sageox/ox/internal/kb$(comma)github.com/sageox/ox/internal/gitserver -coverprofile=$(TWIN_COVER_DIR)/kb.out -covermode=atomic,)

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

build-acceptance: ## Build the exact ox + adapter binaries used by acceptance tests
	@rm -rf "$(ACCEPTANCE_DIR)"
	@mkdir -p "$(ACCEPTANCE_DIR)"
	@$(GO) build $(LDFLAGS) -o "$(ACCEPTANCE_OX_BIN)" ./cmd/ox
	@$(GO) build $(ADAPTER_LDFLAGS) -o "$(ACCEPTANCE_DIR)/ox-adapter-claude-code" ./cmd/ox-adapter-claude-code

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
	@rm -f coverage.out coverage.out.provenance.json coverage.html coverage-all.out coverage-all.out.provenance.json .coverage-baseline.out
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
#                                   Uses both testing.Short and the `short` build tag so files marked
#                                   `//go:build !short` are excluded. Runs on every commit. Target: <60s wall.
#   fast+cov (make test-cover)    — Same as fast but with coverage (~15-20% slower). Local use.
#   full  (make test-all)         — All unit tests including expensive ones (git clone, SQLite, LFS).
#                                   Coverage collection lives here. Target: <5min wall.
#   slow  (make test-slow)        — Tests requiring real ox binary (build tag: slow). No Claude needed.
#   integration — E2E with real Claude sessions. Lives in sageox/ox-test-harness (private).
#
# Tier criteria for `make test` (fast):
#   - No network or external services. Hermetic local subprocesses (for
#     example git in t.TempDir) are allowed when bounded and <500ms.
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
GOTESTSUM_VERSION := v1.13.0
GOTESTSUM := $(shell which gotestsum 2>/dev/null || echo "go run gotest.tools/gotestsum@$(GOTESTSUM_VERSION)")
# Leave empty to create a unique artifact per local invocation. Set explicitly
# when a caller needs a stable path (for example, CI uploads its artifact).
TEST_TIMINGS ?=
TEST_JUNIT ?=
TEST_METRICS := scripts/test_metrics.py
TEST_TIER_TOOL := python3 scripts/test_tiers.py
FAST_TEST_FLAGS = $(shell $(TEST_TIER_TOOL) flags fast)
FULL_TEST_FLAGS = $(shell $(TEST_TIER_TOOL) flags full)
SLOW_TEST_FLAGS = $(shell $(TEST_TIER_TOOL) flags slow)
# Same full tier, dialed down for a machine that is NOT dedicated to this run.
# Overridable per-invocation: make test-calm CALM_P=1 CALM_PARALLEL=4
# Deliberately NOT a $(shell ...) variable: these two values come from the
# operator, so the tier tool can legitimately reject them (CALM_P=0,
# CALM_PARALLEL=many). Make does not stop for a failed $(shell ...) — it
# substitutes the empty output — which would run the suite with no -race, no
# -timeout and no -count while still printing "full tier". The recipe below
# generates the flags itself so a rejection is fatal.
CALM_P ?= 2
CALM_PARALLEL ?= 8
SLOW_TEST_PACKAGES := ./cmd/ox ./internal/daemon ./internal/session ./tests/adapters
GOTESTSUM_JUNIT = $(if $(strip $(TEST_JUNIT)),--junitfile "$(TEST_JUNIT)",)
GOTESTSUM_TIMINGS = $(if $(strip $(TEST_TIMINGS)),--jsonfile-timing-events "$(TEST_TIMINGS)",)

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
check-test-tiers: ## Validate the machine-readable test-tier contract
	@$(TEST_TIER_TOOL) validate

test-tiers: check-test-tiers ## Print the executable test-tier contract
	@for tier in fast full slow acceptance digital_twin integration release; do $(TEST_TIER_TOOL) describe $$tier; echo; done

test: check-test-tiers ## Run fast tests — unit tests <500ms, race detection, no coverage (every commit)
	$(call say,"Running fast tests (skipping >500ms, no coverage)...")
	@timings='$(TEST_TIMINGS)'; \
	if [ -z "$$timings" ]; then mkdir -p tmp; timings=$$(mktemp -p tmp test-timings.XXXXXX); else mkdir -p "$$(dirname "$$timings")"; fi; \
	test_status=0; \
	$(TEST_GIT_ISOLATION) $(TIME_CMD) $(GOTESTSUM) --format $(GOTESTSUM_FMT) $(GOTESTSUM_LEAN) $(GOTESTSUM_JUNIT) --jsonfile-timing-events "$$timings" -- $(FAST_TEST_FLAGS) ./... || test_status=$$?; \
	echo "TEST_TIMING_ARTIFACT path=$$timings"; \
	metrics_status=0; \
	python3 $(TEST_METRICS) "$$timings" || metrics_status=$$?; \
	if [ "$$test_status" -ne 0 ]; then exit "$$test_status"; fi; \
	exit "$$metrics_status"

test-cover: check-test-tiers ## Run fast tests with coverage collection (~15-20% slower than `make test`)
	$(call say,"Running fast tests with coverage...")
	@$(TEST_GIT_ISOLATION) $(TIME_CMD) $(GOTESTSUM) --format $(GOTESTSUM_FMT) $(GOTESTSUM_LEAN) $(GOTESTSUM_JUNIT) $(GOTESTSUM_TIMINGS) -- $(FAST_TEST_FLAGS) -coverprofile=coverage.out -covermode=atomic ./...
	@python3 scripts/coverage_ratchet.py coverage.out --write-provenance coverage.out.provenance.json

test-timings: ## Reprint metrics from the latest fast-test timing artifact
	@test -n "$(TEST_TIMINGS)" || (echo "Set TEST_TIMINGS to an artifact printed by 'make test'." && exit 1)
	@test -f $(TEST_TIMINGS) || (echo "No fast-test timing artifact at $(TEST_TIMINGS)." && exit 1)
	@python3 $(TEST_METRICS) $(TEST_TIMINGS)

test-all: check-test-tiers ## Run all unit tests including expensive ones (git clone, SQLite, LFS) with coverage
	$(call say,"Running all tests including expensive tests...")
	@$(TEST_GIT_ISOLATION) $(TIME_CMD) $(GOTESTSUM) --format $(GOTESTSUM_FMT) $(GOTESTSUM_LEAN) $(GOTESTSUM_JUNIT) $(GOTESTSUM_TIMINGS) -- $(FULL_TEST_FLAGS) -coverprofile=coverage.out -covermode=atomic ./...
	@python3 scripts/coverage_ratchet.py coverage.out --write-provenance coverage.out.provenance.json

test-calm: check-test-tiers ## Run the full test tier at reduced concurrency (shared or already-loaded machine)
	@# The tier contract's -p 8 -parallel 32 assumes the runner owns the machine.
	@# Locally it runs once per agent session per worktree: two concurrent
	@# `make test-all` runs on an 18-core workstation measured load average 37
	@# and ~50% system time, at which point everything (including the tests)
	@# gets slower. Same coverage, same race detector, a quarter of the fan-out.
	$(call say,"Running full tests at reduced concurrency (-p $(CALM_P) -parallel $(CALM_PARALLEL))...")
	@calm_flags="$$(OX_TEST_P=$(CALM_P) OX_TEST_PARALLEL=$(CALM_PARALLEL) $(TEST_TIER_TOOL) flags full)" || exit $$?; \
		$(TEST_GIT_ISOLATION) $(TIME_CMD) $(GOTESTSUM) --format $(GOTESTSUM_FMT) $(GOTESTSUM_LEAN) $(GOTESTSUM_JUNIT) $(GOTESTSUM_TIMINGS) -- $$calm_flags -coverprofile=coverage.out -covermode=atomic ./...
	@python3 scripts/coverage_ratchet.py coverage.out --write-provenance coverage.out.provenance.json

test-slow: check-test-tiers ## Run slow tests (build tag: slow) — requires real ox binary, no Claude needed
	$(call say,"Running slow tests (requires built ox binary)...")
	@$(TEST_GIT_ISOLATION) $(TIME_CMD) $(GOTESTSUM) --format $(GOTESTSUM_FMT) $(GOTESTSUM_LEAN) -- $(SLOW_TEST_FLAGS) $(SLOW_TEST_PACKAGES)

test-fuzz: ## Run bounded fuzz smoke gates over untrusted parsers
	@$(GO) test -race -run '^$$' -fuzz '^FuzzParseLayerName$$' -fuzztime=5s ./internal/conversation/format
	@$(GO) test -race -run '^$$' -fuzz '^FuzzFolderName$$' -fuzztime=5s ./internal/conversation/read
	@$(GO) test -race -run '^$$' -fuzz '^FuzzParseHistoryEntry$$' -fuzztime=5s ./internal/session

test-browser: ## Run real-browser E2E (build tag: browser) — drives headless Chrome; skips if no Chrome installed
	$(call say,"Running real-browser E2E (requires Chrome/Chromium)...")
	@$(TEST_GIT_ISOLATION) $(TIME_CMD) $(GOTESTSUM) --format $(GOTESTSUM_FMT) $(GOTESTSUM_LEAN) -- -tags=browser -run TestBrowser -count=1 -timeout=5m ./cmd/ox/...

test-integration: ## Integration tests live in sageox/ox-test-harness
	@$(TEST_TIER_TOOL) describe integration
	@echo "For an in-repo real-agent smoke gate, run: make test-agents"
	@exit 1

test-acceptance: check-test-tiers build-acceptance ## Deterministic current-source compiled-binary acceptance journeys
	$(call say,"Running deterministic compiled-binary acceptance tests...")
	@$(MAKE) --no-print-directory test-acceptance-run "ACCEPTANCE_OX_BIN=$(ACCEPTANCE_OX_BIN)"

test-acceptance-cover: check-test-tiers build-cover ## Acceptance journeys through a coverage-instrumented current binary
	@rm -rf $(COVERDIR)/integration
	@mkdir -p $(COVERDIR)/integration
	@OX_TEST_GOCOVERDIR="$(abspath $(COVERDIR)/integration)" \
		$(MAKE) --no-print-directory test-acceptance-run \
			"ACCEPTANCE_OX_BIN=$(abspath bin/$(BINARY_NAME)-cover)" \
			"ACCEPTANCE_GO_COVER_DIR=$(COVERDIR)"
	@test -s $(COVERDIR)/integration-test.out || { echo "ERROR: integration acceptance produced no Go coverage profile"; exit 1; }
	@test -s $(COVERDIR)/slow-test.out || { echo "ERROR: session acceptance produced no Go coverage profile"; exit 1; }

# Internal runner shared by ordinary and coverage-instrumented acceptance. It
# asserts every required journey exists before execution so renames/deletions
# cannot turn the tier green with "no tests to run".
test-acceptance-run:
	@test -x "$(ACCEPTANCE_OX_BIN)" || { echo "ERROR: acceptance binary missing: $(ACCEPTANCE_OX_BIN)"; exit 1; }
	@listed=$$($(GO) test -tags=integration ./cmd/ox -list '.'); \
	 for required in $(ACCEPTANCE_INTEGRATION_TESTS); do \
	   printf '%s\n' "$$listed" | grep -Fx "$$required" >/dev/null || { echo "ERROR: required acceptance test missing: $$required"; exit 1; }; \
	 done
	@listed=$$($(GO) test -tags=slow ./cmd/ox -list '.'); \
	 for required in $(ACCEPTANCE_SLOW_TESTS); do \
	   printf '%s\n' "$$listed" | grep -Fx "$$required" >/dev/null || { echo "ERROR: required acceptance test missing: $$required"; exit 1; }; \
	 done
	@OX_TEST_OX_BINARY="$(ACCEPTANCE_OX_BIN)" $(TEST_GIT_ISOLATION) $(TIME_CMD) $(GOTESTSUM) --format $(GOTESTSUM_FMT) $(GOTESTSUM_LEAN) -- \
		-tags=integration -race -count=1 -p 1 -parallel 1 -timeout=10m \
		$(ACCEPTANCE_INTEGRATION_COVER_FLAGS) \
		-run '^($(subst $(space),|,$(strip $(ACCEPTANCE_INTEGRATION_TESTS))))$$' \
		./cmd/ox
	@OX_TEST_OX_BINARY="$(ACCEPTANCE_OX_BIN)" $(TEST_GIT_ISOLATION) $(TIME_CMD) $(GOTESTSUM) --format $(GOTESTSUM_FMT) $(GOTESTSUM_LEAN) -- \
		-tags=slow -race -count=1 -p 1 -parallel 1 -timeout=10m \
		$(ACCEPTANCE_SLOW_COVER_FLAGS) \
		-run '^($(subst $(space),|,$(strip $(ACCEPTANCE_SLOW_TESTS))))$$' \
		./cmd/ox

test-release: check-test-tiers ## Run every enforceable in-repo release tier sequentially
	@$(MAKE) coverage-ratchet-test
	@$(MAKE) coverage-integration
	@python3 scripts/coverage_ratchet.py coverage-all.out --require-provenance coverage-all.out.provenance.json
	@$(MAKE) test-slow
	@$(MAKE) test-fuzz

test-agents: ## Drive real coding agents and read their transcripts back through ox (opt-in, costs API calls)
	$(call say,"Driving real coding agents — requires each agent installed and authenticated...")
	@OX_TEST_REAL_AGENTS=1 $(GO) test -tags=agents -count=1 -v -timeout=20m ./internal/daemon/agentwork ./tests/agents/

check-no-git-lfs-shell: ## Ensure no code shells out to git-lfs binary (see .claude/rules/lfs-no-git-lfs-binary.md)
	@if grep -r --include='*.go' -nE 'exec\.(Command|CommandContext)\("git",\s*"lfs"|exec\.(Command|CommandContext)\("git-lfs"|LookPath\("git-lfs"\)' . 2>/dev/null \
		| grep -v '_test\.go:' | grep -v 'vendor/' | grep -v 'doc\.go:' \
		| grep -vE ':[0-9]+:[[:space:]]*//' ; then \
		echo "ERROR: ox must not shell out to git-lfs. See .claude/rules/lfs-no-git-lfs-binary.md"; \
		exit 1; \
	fi

check-session-meta-rmw: ## Ensure sessions/*/meta.json is only rewritten via lfs.MutateSessionMeta (ox-q42i, GH #710)
	@# Rebuilding meta.json from a builder, or reading/marshalling it without
	@# the flock, silently drops every field the writer doesn't set —
	@# summary_status, validation_error, summary_attempts, redactions,
	@# produced_commits, linked_prs, linkage_status. Because the ledger
	@# auto-resolves sessions/ conflicts to the LOCAL side, that stripping
	@# then propagates to origin and erases the fields for the whole team.
	@#
	@# ALLOWLIST below = the remaining ox-q42i sites, each a first-write or
	@# an interactive single-writer path. Do not add to it without closing
	@# that ticket's reasoning; the correct fix is lfs.MutateSessionMeta.
	@# Fail CLOSED on a scanner error: grep exits 0 (match), 1 (no match —
	@# the healthy case), or >1 (real error). Swallowing >1 would turn a
	@# broken scan into a silent pass, which is worse than no guard at all.
	@raw=$$(grep -rnE 'lfs\.WriteSessionMeta(Only)?\(' --include='*.go' .); rc=$$?; \
	if [ $$rc -gt 1 ]; then \
		echo "ERROR: check-session-meta-rmw scan failed (grep exit $$rc) — refusing to report success"; \
		exit 1; \
	fi; \
	violations=$$(printf '%s\n' "$$raw" | grep -v '^$$' \
		| grep -v '_test\.go:' | grep -v 'vendor/' \
		| grep -vE ':[0-9]+:[[:space:]]*//' \
		| grep -vE '^\./(cmd/ox/agent_session\.go|cmd/ox/agent_session_recover\.go|cmd/ox/session_regenerate\.go|cmd/ox/session_upload_cmd\.go|cmd/ox/session_repair_meta_summary\.go|cmd/ox/session_push_summary\.go):'); \
	if [ -n "$$violations" ]; then \
		echo "$$violations"; \
		echo "ERROR: rewrite sessions/*/meta.json via lfs.MutateSessionMeta, not WriteSessionMeta[Only]."; \
		echo "       A fresh-built or unlocked write drops fields it does not set (GH #710, ox-q42i)."; \
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
	@# Pass 1: literal raw.jsonl path passed straight to a writer call.
	@violations=$$(grep -rnE '(os\.OpenFile|os\.Create|os\.WriteFile)\([^)]*raw[._]?jsonl' \
		--include='*.go' . 2>/dev/null \
		| grep -v '_test\.go:' \
		| grep -v 'vendor/' \
		| grep -v 'internal/session/raw_writer\.go:' \
		| grep -vE ':[0-9]+:[[:space:]]*//') ; \
	if [ -n "$$violations" ] ; then \
		echo "ERROR: raw.jsonl must be written via session.RawWriter (see internal/session/raw_writer.go)"; \
		echo "Violations (literal path):"; \
		echo "$$violations"; \
		exit 1; \
	fi
	@# Pass 2: variable-indirection. The raw.jsonl path is usually held in
	@# a `rawPath` var or the `ledgerFileRaw` constant, so the literal
	@# "raw.jsonl" string never appears at the open site. Scoped to the two
	@# packages that record sessions (cmd/ox, internal/session) since those
	@# are where the bypass lived; the chokepoint and tests are exempt.
	@indirect=$$(grep -rnE '(os\.OpenFile|os\.Create|os\.WriteFile)\([^)]*(rawPath|ledgerFileRaw)' \
		--include='*.go' cmd/ox internal/session 2>/dev/null \
		| grep -v '_test\.go:' \
		| grep -v 'internal/session/raw_writer\.go:' \
		| grep -vE ':[0-9]+:[[:space:]]*//') ; \
	if [ -n "$$indirect" ] ; then \
		echo "ERROR: raw.jsonl path (rawPath / ledgerFileRaw) must be written via session.RawWriter"; \
		echo "Violations (variable indirection):"; \
		echo "$$indirect"; \
		exit 1; \
	fi

check-codedb-guarded-open: ## Ensure codedb opens user repos only via internal/codedb/gitopen (issue #819)
	@# codedb indexes the managed SOURCE checkout in place. A raw git.PlainOpen /
	@# unwrapped git.Open lets go-git rewrite that repo's .git/config (flips
	@# core.bare, drops extensions.worktreeConfig) and break every work-tree git
	@# command. All source-repo opens must route through gitopen.GuardedPlainOpen
	@# or gitopen.WrapReadOnlyConfig, which deny the config write. gitopen.go
	@# itself and tests are exempt. Fail CLOSED on a scanner error (grep >1).
	@# The sed strips ONLY the exact guarded form git.Open(gitopen.WrapReadOnlyConfig(
	@# (per-call, not line-wide) then flags any surviving raw open — so a raw
	@# git.Open sharing a line with a wrapper reference cannot slip through.
	@raw=$$(grep -rnE 'git\.(PlainOpen|Open)\(' --include='*.go' internal/codedb); rc=$$?; \
	if [ $$rc -gt 1 ]; then \
		echo "ERROR: check-codedb-guarded-open scan failed (grep exit $$rc) — refusing to report success"; \
		exit 1; \
	fi; \
	violations=$$(printf '%s\n' "$$raw" | grep -v '^$$' \
		| grep -v '_test\.go:' \
		| grep -v 'internal/codedb/gitopen/gitopen\.go:' \
		| grep -vE ':[0-9]+:[[:space:]]*//' \
		| sed 's/git\.Open(gitopen\.WrapReadOnlyConfig(/GUARDED_OPEN(/g' \
		| grep -E 'git\.(PlainOpen|Open)\('); \
	if [ -n "$$violations" ]; then \
		echo "$$violations"; \
		echo "ERROR: open user repos via internal/codedb/gitopen (GuardedPlainOpen / WrapReadOnlyConfig), not raw go-git — see issue #819."; \
		exit 1; \
	fi

test-preflight: check-no-git-lfs-shell check-raw-writer-chokepoint check-session-meta-rmw check-codedb-guarded-open ## Pre-PR quality gate: lint + all unit tests + slow tests (lint/test-all/test-slow run concurrently)
	$(call say,"Running lint, full tests, and slow tests concurrently...")
	@# lint and the test binaries don't share any output file (test-all writes
	@# coverage.out; test-slow and lint don't touch it), so running them
	@# concurrently via a sub-make -j is safe and turns a lint(2-4min) + test-all
	@# + test-slow SUM into a max() — lint's cost is absorbed into the test
	@# wall time instead of adding to it. On failure, already-started sibling
	@# jobs are allowed to finish (standard `make -j` behavior); only launching
	@# NEW prerequisites stops. Output may interleave under -j with GNU Make
	@# <4.0 (macOS system `make`); install `brew install make` (gmake) and use
	@# `--output-sync=target` if that's confusing.
	@$(MAKE) -j 3 lint test-all test-slow

test-digital-twin: test-cloud-api-twin test-ledger-twin test-kb-twin ## Deterministic cloud-auth, ledger, and KB twins

test-digital-twin-cover: ## Run every deterministic twin with distinct mergeable coverage profiles
	@rm -rf $(COVERDIR)/twins
	@mkdir -p $(COVERDIR)/twins
	@$(MAKE) --no-print-directory test-digital-twin "TWIN_COVER_DIR=$(COVERDIR)/twins"
	@for profile in cloud ledger kb; do \
	  test -s $(COVERDIR)/twins/$$profile.out || { echo "ERROR: $$profile twin produced no coverage profile"; exit 1; }; \
	 done
	@grep -q '^github.com/sageox/ox/internal/auth/' $(COVERDIR)/twins/cloud.out || { echo "ERROR: cloud twin profile contains no product auth coverage"; exit 1; }
	@grep -q '^github.com/sageox/ox/internal/glance/' $(COVERDIR)/twins/ledger.out || { echo "ERROR: ledger twin profile contains no product glance coverage"; exit 1; }
	@grep -q '^github.com/sageox/ox/internal/daemon/' $(COVERDIR)/twins/kb.out || { echo "ERROR: KB twin profile contains no product daemon coverage"; exit 1; }
	@if grep -q '^github.com/sageox/ox/tests/' $(COVERDIR)/twins/*.out; then \
	  echo "ERROR: twin coverage must measure product packages, not harness packages"; exit 1; \
	 fi

test-cloud-api-twin: ## Product auth client against in-process SageOx API twin
	@echo "Running cloud auth API digital twin tests..."
	@listed=$$($(GO) test ./internal/twinapi ./internal/auth -list '^Test'); \
	 for required in TestDeviceFlow_HappyPath TestIntrospect_FaultInjectionIsPathGeneric TestClockAdvance_JWTExpiry \
	   TestValidateTokenServerSide_ExpiredJWT TestValidateTokenServerSide_FaultInjection TestValidateTokenServerSide_UserNotFoundError; do \
	   printf '%s\n' "$$listed" | grep -Fx "$$required" >/dev/null || { echo "ERROR: required cloud twin test missing: $$required"; exit 1; }; \
	 done
	@$(TEST_GIT_ISOLATION) $(TIME_CMD) $(GOTESTSUM) --format $(GOTESTSUM_FMT) $(GOTESTSUM_LEAN) -- -race -count=1 -timeout=2m $(CLOUD_TWIN_COVER_FLAGS) ./internal/auth/... ./internal/twinapi/...

test-team-context-twin: ## Digital twin tests (generates fake team context for inspection)
	@echo "Running team context digital twin tests..."
	@time $(GOTESTSUM) --format pkgname-and-test-fails -- -tags=team_context_twin -v -count=1 -timeout=2m ./tests/team_context_twin/...

test-ledger-twin: ## Digital twin ledger tests (generates fake ledger for inspection)
	@echo "Running ledger digital twin tests..."
	@listed=$$($(GO) test -tags=ledger_twin ./tests/ledger_twin -list '^Test'); \
	 for required in TestCartAnalyzeWindows TestPreCrimePositive_HotZone TestSessionConflicts_MergedWithMurmurs TestFullEnrich; do \
	   printf '%s\n' "$$listed" | grep -Fx "$$required" >/dev/null || { echo "ERROR: required ledger twin test missing: $$required"; exit 1; }; \
	 done
	@$(TEST_GIT_ISOLATION) time $(GOTESTSUM) --format pkgname-and-test-fails -- -tags=ledger_twin -v -count=1 -timeout=2m $(LEDGER_TWIN_COVER_FLAGS) ./tests/ledger_twin/...

test-kb-twin: ## Digital twin kb tests (drives syncBubbles + GC against real bare repos)
	@command -v git >/dev/null 2>&1 || { echo "ERROR: git is required for the KB digital twin"; exit 1; }
	@echo "Running kb digital twin tests..."
	@listed=$$($(GO) test -tags=kb_twin ./tests/kb_twin -list '^Test'); \
	 for required in TestKBTwin TestKBTwin_MultiEndpoint TestKBTwin_APIUnavailable_NoSideEffects TestKBTwin_MetaJSONShape; do \
	   printf '%s\n' "$$listed" | grep -Fx "$$required" >/dev/null || { echo "ERROR: required KB twin test missing: $$required"; exit 1; }; \
	 done
	@$(TEST_GIT_ISOLATION) time $(GOTESTSUM) --format pkgname-and-test-fails -- -tags=kb_twin -v -count=1 -timeout=5m $(KB_TWIN_COVER_FLAGS) ./tests/kb_twin/...

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
	@which gotestsum > /dev/null || (echo "gotestsum not found. Install with: go install gotest.tools/gotestsum@$(GOTESTSUM_VERSION)" && exit 1)
	gotestsum --watch

# Coverage
COVERDIR := tmp/coverage
COVERAGE_THRESHOLD ?= 50
COVERAGE_BASE ?= origin/main

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

coverage-ratchet: test-all ## Fail if protected risk-package coverage regresses
	@python3 scripts/coverage_ratchet.py coverage.out --require-provenance coverage.out.provenance.json

coverage-ratchet-diff: test-all ## Enforce package + changed-line coverage vs COVERAGE_BASE
	@python3 scripts/coverage_ratchet.py coverage.out --require-provenance coverage.out.provenance.json --diff-base $(COVERAGE_BASE)

coverage-ratchet-test: ## Test the coverage ratchet parser and failure semantics
	@cd scripts && PYTHONDONTWRITEBYTECODE=1 python3 -m unittest -v coverage_ratchet_test.py test_tiers_test.py test_metrics_test.py

build-cover: ## Build ox binary with coverage instrumentation
	@rm -rf $(COVERDIR)/integration $(COVERDIR)/merged
	@mkdir -p bin $(COVERDIR)/integration
	@$(GO) build -cover -covermode=atomic $(LDFLAGS) -o bin/$(BINARY_NAME)-cover ./cmd/ox
	@$(GO) build $(ADAPTER_LDFLAGS) -o bin/ox-adapter-claude-code ./cmd/ox-adapter-claude-code
	@echo "Instrumented binary: bin/$(BINARY_NAME)-cover"
	@echo "Run with: GOCOVERDIR=$(COVERDIR)/integration bin/$(BINARY_NAME)-cover ..."

coverage-integration: ## Run acceptance through instrumented ox and merge full + binary coverage
	@rm -f coverage-all.out coverage-all.out.provenance.json
	@$(MAKE) --no-print-directory test-all
	@$(MAKE) --no-print-directory test-acceptance-cover
	@$(MAKE) --no-print-directory test-digital-twin-cover
	@count=$$(find $(COVERDIR)/integration -type f -name 'covcounters.*' | wc -l | tr -d ' '); \
	 if [ "$$count" -eq 0 ]; then \
	   echo "ERROR: instrumented ox produced no fresh coverage counters"; exit 1; \
	 fi; \
	 echo "Fresh integration coverage fragments: $$count"
	@echo "Converting integration profile..."
	@$(GO) tool covdata textfmt -i=$(COVERDIR)/integration -o=$(COVERDIR)/integration.out
	@echo "Merging profiles..."
	@awk 'FNR == 1 { next } /\/kb_twin_export\.go:/ { next } { counts[$$1 " " $$2] += $$3 } END { for (block in counts) print block, counts[block] }' \
	  coverage.out $(COVERDIR)/integration.out $(COVERDIR)/integration-test.out $(COVERDIR)/slow-test.out \
	  $(COVERDIR)/twins/cloud.out $(COVERDIR)/twins/ledger.out $(COVERDIR)/twins/kb.out \
	  | LC_ALL=C sort > $(COVERDIR)/merged-body.out
	@{ echo 'mode: atomic'; sed -n '1,$$p' $(COVERDIR)/merged-body.out; } > coverage-all.out
	@python3 scripts/coverage_ratchet.py coverage-all.out --write-provenance coverage-all.out.provenance.json
	@$(GO) tool cover -func=coverage-all.out | tail -1
	@echo "Combined profile: coverage-all.out"

smoke-test: build ## Run smoke tests against SageOx cloud (requires SAGEOX_CI_PASSWORD)
	@echo "Running smoke tests..."
	@./scripts/smoketest/smoke-test.sh

# Code quality
# Targets below are agent-friendly by default (quiet). V=1 for verbose.
lint: lint-test-env ## Run golangci-lint
	@which golangci-lint > /dev/null || (echo "golangci-lint not found. Install from https://golangci-lint.run/usage/install/" && exit 1)
	@# --allow-parallel-runners: multiple AI coding agent sessions routinely run
	@# `make lint` at the same time in this repo. golangci-lint's default file
	@# lock turns that into a hard failure ("parallel golangci-lint is running")
	@# instead of just queuing or racing harmlessly — each invocation has its
	@# own in-memory analysis, so concurrent runs don't corrupt shared state.
	@golangci-lint run -c .config/golangci.yml --allow-parallel-runners ./...

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
release: test-release ## Create release with goreleaser after enforceable test tiers (requires GITHUB_TOKEN)
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

docs-check: ## Fail when committed CLI reference docs differ from Cobra
	@docs_tmp=$$(mktemp -d); \
		trap 'rm -rf "$$docs_tmp"' EXIT; \
		cp docs/package.json "$$docs_tmp/package.json"; \
		$(GO) run ./cmd/ox docs --output "$$docs_tmp/reference"; \
		if ! diff -ru -x .gitkeep docs/reference "$$docs_tmp/reference"; then \
			echo "Generated CLI docs are stale. Run 'make docs' and commit the result."; \
			exit 1; \
		fi; \
		if ! cmp -s docs/package.json "$$docs_tmp/package.json"; then \
			echo "docs/package.json is stale. Run 'make docs' and commit the result."; \
			exit 1; \
		fi

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
		# COLORTERM=truecolor is required, not decorative: asciinema records through a
		# pty, so stdout IS a terminal and theme.Profile trusts terminal detection
		# rather than the forced-renderer branch. Without it the cast bakes in ANSI256
		# permanently. Matches the freeze invocation in catalog-build above.
		for name in $$(./bin/$(BINARY_NAME) dev catalog --json | jq -r '.components[] | select(.renderer=="asciinema") | .name'); do \
			CLICOLOR_FORCE=1 FORCE_COLOR=1 COLORTERM=truecolor asciinema rec --quiet --overwrite --cols=80 --rows=24 \
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

check-release-drift: ## Check version.go isn't ahead of the latest published GitHub release (needs network + gh auth)
	@bash scripts/check-release-drift.sh

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

# =============================================================================
# Security review — see security/README.md
# =============================================================================
# `make sec` runs the AI security pipeline. Source for the 6-phase shape:
# https://www.synthesia.io/post/automating-code-security-reviews-with-claude-mythos-level-capabilities
#
# These are explicit on-demand commands — NOT chained from build/test/lint
# (the multi-minute AI pipeline would compound across every agentic iteration).

sec: ## Run the AI security review pipeline (diff vs origin/main)
	@bash security/scripts/orchestrate.sh

sec-fast: ## Run only the deterministic OSS-tool tier (no AI cost)
	@bash security/scripts/deterministic.sh

sec-install: ## Install all security-review tool binaries to bin/ (no root)
	@bash security/scripts/install-bins.sh

sec-install-hook: ## Install opt-in pre-commit fast tier (run with SEC_PRECOMMIT=1 git commit)
	@mkdir -p .git/hooks
	@cat > .git/hooks/pre-commit <<'EOF'
	#!/usr/bin/env bash
	# Generated by `make sec-install-hook`. Opt-in via SEC_PRECOMMIT=1.
	bash "$(git rev-parse --show-toplevel)/security/scripts/precommit-fast-tier.sh" || true
	EOF
	@chmod +x .git/hooks/pre-commit
	@echo "Pre-commit fast tier installed. Run with: SEC_PRECOMMIT=1 git commit ..."

# Default target
.DEFAULT_GOAL := help
