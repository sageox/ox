#!/usr/bin/env bash
# smoke-test-sessions.sh - Test ox session list runs without errors
#
# Required environment variables (set by smoke-test.sh):
#   SAGEOX_ENDPOINT - API endpoint
#   SMOKE_TEST_WORKDIR - Temp directory for test
#   OX - Path to ox binary

set -euo pipefail

: "${SAGEOX_ENDPOINT:?SAGEOX_ENDPOINT is required}"
: "${SMOKE_TEST_WORKDIR:?SMOKE_TEST_WORKDIR is required}"
: "${OX:?OX is required}"

TEST_REPO="$SMOKE_TEST_WORKDIR/test-init-repo"

if [[ ! -d "$TEST_REPO/.sageox" ]]; then
    echo "error: test-init-repo not initialized (run init test first)"
    exit 1
fi

cd "$TEST_REPO"

echo "Testing ox session list..."

set +e
output=$($OX session list 2>&1)
exit_code=$?
set -e

if [[ $exit_code -ne 0 ]]; then
    echo "error: ox session list failed with exit code $exit_code"
    echo "$output"
    exit 1
fi

# check for panics
if echo "$output" | grep -qi "panic\|goroutine\|stack trace"; then
    echo "error: ox session list produced a panic"
    echo "$output"
    exit 1
fi

# output should contain either "No sessions" or session data
if echo "$output" | grep -qi "no sessions"; then
    echo "No sessions found (expected for fresh test repo)"
else
    echo "Session list output received (${#output} bytes)"
fi

echo "ox session list test passed"
exit 0
