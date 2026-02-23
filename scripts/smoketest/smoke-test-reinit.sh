#!/usr/bin/env bash
# smoke-test-reinit.sh - Test ox init on an already-initialized repo (upgrade path)
#
# Required environment variables (set by smoke-test.sh):
#   SAGEOX_ENDPOINT - API endpoint
#   SMOKE_TEST_WORKDIR - Temp directory for test
#   OX - Path to ox binary

set -euo pipefail

: "${SAGEOX_ENDPOINT:?SAGEOX_ENDPOINT is required}"
: "${SMOKE_TEST_WORKDIR:?SMOKE_TEST_WORKDIR is required}"
: "${OX:?OX is required}"

TEST_REPO="$SMOKE_TEST_WORKDIR/test-reinit-repo"

if [[ ! -d "$SMOKE_TEST_WORKDIR/test-init-repo/.sageox" ]]; then
    echo "error: test-init-repo not initialized (run init test first)"
    exit 1
fi

echo "Testing ox init re-initialization..."

# copy the init'd repo to avoid polluting the shared one
cp -r "$SMOKE_TEST_WORKDIR/test-init-repo" "$TEST_REPO"
cd "$TEST_REPO"

# record state before reinit
repo_marker_before=$(find .sageox -name ".repo_*" -type f 2>/dev/null | head -1)

echo "Running ox init --quiet on already-initialized repo..."
if ! $OX init --quiet; then
    echo "error: ox init failed on re-init"
    exit 1
fi

# verify .sageox still exists
if [[ ! -d ".sageox" ]]; then
    echo "error: .sageox directory missing after re-init"
    exit 1
fi

# verify config.json still exists and is valid JSON
if [[ -f ".sageox/config.json" ]]; then
    if ! jq . ".sageox/config.json" > /dev/null 2>&1; then
        echo "error: config.json is not valid JSON after re-init"
        exit 1
    fi
    echo "config.json preserved and valid"
else
    echo "warning: no config.json found after re-init"
fi

# verify repo marker still exists
repo_marker_after=$(find .sageox -name ".repo_*" -type f 2>/dev/null | head -1)
if [[ -z "$repo_marker_after" ]]; then
    echo "error: repo marker file missing after re-init"
    exit 1
fi

echo "ox init re-initialization test passed"
exit 0
