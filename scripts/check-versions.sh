#!/bin/bash
# Check that all version files are in sync
# Run before releases to catch version drift

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

# Check we're in repo root
if [ ! -f "internal/version/version.go" ]; then
    echo -e "${RED}Error: Must run from repository root${NC}"
    exit 1
fi

# Get the canonical version from version.go
CANONICAL=$(grep 'Version.*=' internal/version/version.go | sed 's/.*"\(.*\)".*/\1/')

if [ -z "$CANONICAL" ]; then
    echo -e "${RED}Could not read version from internal/version/version.go${NC}"
    exit 1
fi

echo "Canonical version (from version.go): $CANONICAL"
echo ""

MISMATCH=0
WARNINGS=0

check_version() {
    local version=$1
    local description=$2

    if [ -z "$version" ]; then
        echo -e "${YELLOW}Warning: $description: not found${NC}"
        WARNINGS=$((WARNINGS + 1))
    elif [ "$version" != "$CANONICAL" ]; then
        echo -e "${RED}Mismatch: $description: $version (expected $CANONICAL)${NC}"
        MISMATCH=1
    else
        echo -e "${GREEN}Match: $description: $version${NC}"
    fi
}

# Check .claude-plugin/marketplace.json
if [ -f ".claude-plugin/marketplace.json" ]; then
    MARKETPLACE_VERSION=$(grep '"version"' .claude-plugin/marketplace.json 2>/dev/null | head -1 | sed 's/.*"\([0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\)".*/\1/')
    check_version "$MARKETPLACE_VERSION" ".claude-plugin/marketplace.json"
fi

# Check claude-plugin/.claude-plugin/plugin.json
if [ -f "claude-plugin/.claude-plugin/plugin.json" ]; then
    PLUGIN_VERSION=$(grep '"version"' claude-plugin/.claude-plugin/plugin.json 2>/dev/null | head -1 | sed 's/.*"\([0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\)".*/\1/')
    check_version "$PLUGIN_VERSION" "claude-plugin/.claude-plugin/plugin.json"
fi

# Check npm/package.json (@sageox/ox npm wrapper)
if [ -f "npm/package.json" ]; then
    NPM_VERSION=$(grep '"version"' npm/package.json 2>/dev/null | head -1 | sed 's/.*"\([0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\)".*/\1/')
    check_version "$NPM_VERSION" "npm/package.json"
fi

# Check pip/pyproject.toml (sageox PyPI wrapper skeleton)
if [ -f "pip/pyproject.toml" ]; then
    PIP_VERSION=$(grep '^version = ' pip/pyproject.toml 2>/dev/null | head -1 | sed 's/.*"\([0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\)".*/\1/')
    check_version "$PIP_VERSION" "pip/pyproject.toml"
fi

# Check CHANGELOG.md has the current version
if [ -f "CHANGELOG.md" ]; then
    # Extract first version from CHANGELOG.md (format: ## [X.Y.Z])
    CHANGELOG_VERSION=$(grep -o '\[[0-9]\+\.[0-9]\+\.[0-9]\+\]' CHANGELOG.md 2>/dev/null | head -1 | tr -d '[]')
    check_version "$CHANGELOG_VERSION" "CHANGELOG.md latest version"
else
    echo -e "${YELLOW}Warning: CHANGELOG.md not found${NC}"
    WARNINGS=$((WARNINGS + 1))
fi

echo ""

if [ $MISMATCH -eq 1 ]; then
    echo -e "${RED}Version mismatch detected!${NC}"
    echo ""
    echo "Run: ./scripts/version-bump.sh $CANONICAL"
    echo "Then update CHANGELOG.md with release notes."
    exit 1
elif [ $WARNINGS -gt 0 ]; then
    echo -e "${YELLOW}Some version files could not be checked${NC}"
    exit 0
else
    echo -e "${GREEN}All versions match: $CANONICAL${NC}"
fi
