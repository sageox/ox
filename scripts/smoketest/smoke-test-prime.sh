#!/usr/bin/env bash
# smoke-test-prime.sh - Test ox agent prime returns valid JSON with expected fields
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

echo "Testing ox agent prime (JSON mode)..."

# AGENT_ENV=claude-code simulates running inside a coding agent
# this is required by agentx.RequireAgent()
set +e
output=$(AGENT_ENV=claude-code $OX agent prime 2>&1)
exit_code=$?
set -e

if [[ $exit_code -ne 0 ]]; then
    echo "error: ox agent prime failed with exit code $exit_code"
    echo "$output"
    exit 1
fi

# validate JSON
if ! echo "$output" | jq . > /dev/null 2>&1; then
    echo "error: output is not valid JSON"
    echo "$output"
    exit 1
fi

# check status field
status=$(echo "$output" | jq -r '.status')
if [[ "$status" != "fresh" && "$status" != "unavailable" ]]; then
    echo "error: unexpected status: $status (expected 'fresh' or 'unavailable')"
    exit 1
fi

# check agent_id (required for fresh status)
agent_id=$(echo "$output" | jq -r '.agent_id // empty')
if [[ -z "$agent_id" && "$status" == "fresh" ]]; then
    echo "error: missing agent_id in fresh status"
    exit 1
fi

# check agent_supported field exists
if ! echo "$output" | jq -e 'has("agent_supported")' > /dev/null 2>&1; then
    echo "error: missing agent_supported field"
    exit 1
fi

# check attribution object exists
if ! echo "$output" | jq -e '.attribution' > /dev/null 2>&1; then
    echo "error: missing attribution field"
    exit 1
fi

# if session recording is active, validate the file path
session_file=$(echo "$output" | jq -r '.session.file // empty')
session_recording=$(echo "$output" | jq -r '.session.recording // false')
if [[ "$session_recording" == "true" && -n "$session_file" ]]; then
    # session file must be a regular file, not a directory
    if [[ -d "$session_file" ]]; then
        echo "error: session.file is a directory, not a file: $session_file"
        exit 1
    fi
    # session file should end in .jsonl
    if [[ "$session_file" != *.jsonl ]]; then
        echo "error: session.file does not end in .jsonl: $session_file"
        exit 1
    fi
    echo "Session file validated: $session_file"
fi

echo "JSON valid: status=$status agent_id=$agent_id"
echo "ox agent prime test passed"
exit 0
