#!/usr/bin/env bash
# smoke-test.sh - Main orchestrator for ox CLI smoke tests
# Runs core ox commands against a SageOx cloud endpoint to verify functionality.
#
# Required environment variables:
#   SAGEOX_CI_PASSWORD - Password for test-ox-cli@sageox.ai account
#
# Optional environment variables:
#   SAGEOX_ENDPOINT - API endpoint (default: https://test.sageox.ai)
#   OX_BINARY - Path to ox binary (default: ox in PATH or ./bin/ox)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
NC='\033[0m' # no color

# test account (public, password is secret)
export SAGEOX_CI_EMAIL="test-ox-cli@sageox.ai"

# defaults
export SAGEOX_ENDPOINT="${SAGEOX_ENDPOINT:-https://test.sageox.ai}"

# find ox binary
if [[ -n "${OX_BINARY:-}" ]]; then
    OX="$OX_BINARY"
elif [[ -x "$REPO_ROOT/bin/ox" ]]; then
    OX="$REPO_ROOT/bin/ox"
elif command -v ox &>/dev/null; then
    OX="ox"
else
    echo -e "${RED}error: ox binary not found. Run 'make build' first.${NC}" >&2
    exit 1
fi

# track results
TESTS_RUN=0
TESTS_PASSED=0
TESTS_FAILED=0
FAILED_TESTS=()

log_info() {
    echo -e "${BLUE}[INFO]${NC} $*"
}

log_pass() {
    echo -e "${GREEN}[PASS]${NC} $*"
    ((TESTS_PASSED++)) || true
}

log_fail() {
    echo -e "${RED}[FAIL]${NC} $*"
    ((TESTS_FAILED++)) || true
    FAILED_TESTS+=("$1")
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $*"
}

run_test() {
    local name="$1"
    local script="$2"
    ((TESTS_RUN++)) || true

    log_info "Running: $name"

    if bash "$SCRIPT_DIR/$script"; then
        log_pass "$name"
        return 0
    else
        log_fail "$name"
        return 1
    fi
}

cleanup() {
    log_info "Cleaning up test artifacts..."

    # remove temp directories created during tests
    if [[ -n "${SMOKE_TEST_WORKDIR:-}" && -d "$SMOKE_TEST_WORKDIR" ]]; then
        rm -rf "$SMOKE_TEST_WORKDIR"
    fi

    # remove test auth file
    if [[ -f ~/.config/sageox/auth.json.smoke-test-backup ]]; then
        mv ~/.config/sageox/auth.json.smoke-test-backup ~/.config/sageox/auth.json 2>/dev/null || true
    fi
}

trap cleanup EXIT

main() {
    echo ""
    echo "========================================"
    echo "  ox CLI Smoke Tests"
    echo "========================================"
    echo ""
    log_info "Endpoint: $SAGEOX_ENDPOINT"
    log_info "Binary: $OX"
    log_info "Test account: $SAGEOX_CI_EMAIL"
    echo ""

    # validate required env vars
    if [[ -z "${SAGEOX_CI_PASSWORD:-}" ]]; then
        log_fail "SAGEOX_CI_PASSWORD environment variable is required"
        exit 1
    fi

    # backup existing auth if present
    if [[ -f ~/.config/sageox/auth.json ]]; then
        cp ~/.config/sageox/auth.json ~/.config/sageox/auth.json.smoke-test-backup
    fi

    # create temp working directory
    SMOKE_TEST_WORKDIR=$(mktemp -d)
    export SMOKE_TEST_WORKDIR
    export OX="$OX"
    log_info "Working directory: $SMOKE_TEST_WORKDIR"
    echo ""

    # run tests in order (some depend on auth being set up first)
    run_test "Authentication" "smoke-test-auth.sh" || true
    run_test "ox init" "smoke-test-init.sh" || true
    run_test "ox doctor cloud" "smoke-test-doctor.sh" || true
    run_test "ox status" "smoke-test-status.sh" || true
    run_test "ox init (re-init)" "smoke-test-reinit.sh" || true
    run_test "ox agent prime" "smoke-test-prime.sh" || true
    run_test "ox session list" "smoke-test-sessions.sh" || true
    run_test "Clone without ox" "smoke-test-clone-no-ox.sh" || true
    # skip checkout test for now - requires team setup
    # run_test "ox team context add" "smoke-test-checkout.sh" || true

    # summary
    echo ""
    echo "========================================"
    echo "  Summary"
    echo "========================================"
    echo ""
    echo "Tests run:    $TESTS_RUN"
    echo -e "Tests passed: ${GREEN}$TESTS_PASSED${NC}"
    echo -e "Tests failed: ${RED}$TESTS_FAILED${NC}"

    if [[ $TESTS_FAILED -gt 0 ]]; then
        echo ""
        echo "Failed tests:"
        for test in "${FAILED_TESTS[@]}"; do
            echo "  - $test"
        done
        echo ""
        log_warn "Smoke tests completed with failures"
        exit 1
    else
        echo ""
        log_pass "All smoke tests passed"
        exit 0
    fi
}

main "$@"
