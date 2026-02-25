#!/usr/bin/env bash
# smoke-test-clone-no-ox.sh - Verify git clone of a repo with .sageox/ works without ox installed
#
# Tests that users who clone a repo with .sageox/ committed don't see
# warnings or errors from .gitattributes, hooks, or LFS filters.
#
# Required environment variables (set by smoke-test.sh):
#   SMOKE_TEST_WORKDIR - Temp directory for test
#   OX - Path to ox binary (used to determine what to exclude from PATH)

set -euo pipefail

: "${SMOKE_TEST_WORKDIR:?SMOKE_TEST_WORKDIR is required}"
: "${OX:?OX is required}"

SOURCE_REPO="$SMOKE_TEST_WORKDIR/test-clone-source"
CLONE_TARGET="$SMOKE_TEST_WORKDIR/test-clone-target"

echo "Testing git clone of repo with .sageox/ (without ox in PATH)..."

# create a source repo with .sageox/ committed
mkdir -p "$SOURCE_REPO"
cd "$SOURCE_REPO"
git init -q
git config user.email "test@example.com"
git config user.name "Test User"
echo "# Test Repo" > README.md

# simulate what ox init creates (minimal .sageox/)
mkdir -p .sageox
cat > .sageox/config.json << 'JSON'
{"version":"1","repo_id":"test-repo-id","endpoint":"https://test.sageox.ai"}
JSON
touch ".sageox/.repo_00000000-0000-0000-0000-000000000000"

# add .gitattributes like ox init would
cat > .gitattributes << 'ATTRS'
.sageox/** linguist-language=SageOx
*.ox linguist-language=SageOx
ATTRS

git add README.md .sageox/ .gitattributes
git commit -q -m "initial commit with sageox"

# clone with ox removed from PATH
OX_DIR=$(dirname "$(readlink -f "$OX" 2>/dev/null || echo "$OX")")
CLEAN_PATH=$(echo "$PATH" | tr ':' '\n' | grep -v "^${OX_DIR}$" | tr '\n' ':' | sed 's/:$//')

echo "Cloning without ox in PATH..."
set +e
clone_stderr=$(PATH="$CLEAN_PATH" git clone "$SOURCE_REPO" "$CLONE_TARGET" 2>&1 >/dev/null)
clone_exit=$?
set -e

if [[ $clone_exit -ne 0 ]]; then
    echo "error: git clone failed with exit code $clone_exit"
    echo "$clone_stderr"
    exit 1
fi

# check stderr for unexpected warnings (filter out normal "Cloning into" message)
if echo "$clone_stderr" | grep -iE "error|warning|filter" | grep -vi "Cloning into"; then
    echo "error: git clone produced warnings/errors:"
    echo "$clone_stderr"
    exit 1
fi

# verify clone has .sageox/
if [[ ! -d "$CLONE_TARGET/.sageox" ]]; then
    echo "error: cloned repo missing .sageox/"
    exit 1
fi

# verify git status is clean in clone
cd "$CLONE_TARGET"
if [[ -n "$(git status --porcelain)" ]]; then
    echo "error: cloned repo has dirty git status"
    git status
    exit 1
fi

echo "git clone without ox test passed"
exit 0
