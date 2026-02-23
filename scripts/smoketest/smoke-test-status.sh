#!/usr/bin/env bash
# smoke-test-status.sh - Test ox status command (text and JSON modes)
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

echo "Testing ox status..."
set +e
output=$($OX status 2>&1)
exit_code=$?
set -e

if [[ $exit_code -ne 0 ]]; then
    echo "error: ox status failed with exit code $exit_code"
    echo "$output"
    exit 1
fi

echo "Status output received (${#output} bytes)"

echo "Testing ox status --json..."
set +e
json_output=$($OX status --json 2>&1)
json_exit=$?
set -e

if [[ $json_exit -ne 0 ]]; then
    echo "error: ox status --json failed with exit code $json_exit"
    echo "$json_output"
    exit 1
fi

# validate JSON
if ! echo "$json_output" | jq . > /dev/null 2>&1; then
    echo "error: --json output is not valid JSON"
    echo "$json_output"
    exit 1
fi

# check auth.authenticated
auth_val=$(echo "$json_output" | jq -r '.auth.authenticated')
if [[ "$auth_val" != "true" ]]; then
    echo "error: auth.authenticated is not true (got: $auth_val)"
    exit 1
fi

# check project.initialized
init_val=$(echo "$json_output" | jq -r '.project.initialized')
if [[ "$init_val" != "true" ]]; then
    echo "error: project.initialized is not true (got: $init_val)"
    exit 1
fi

echo "JSON validated: authenticated=true, initialized=true"
echo "ox status test passed"
exit 0
