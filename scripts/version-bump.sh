#!/bin/bash
set -e

# =============================================================================
# ox version bump utility
# =============================================================================
#
# Updates version numbers across all ox components.
# Canonical source: internal/version/version.go
#
# Usage: ./scripts/version-bump.sh 0.10.0
#
# =============================================================================

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

if [ $# -ne 1 ]; then
    echo "Usage: $0 <version>"
    echo ""
    echo "Updates version numbers across all components."
    echo ""
    echo "Example: $0 0.10.0"
    exit 1
fi

NEW_VERSION=$1

# Validate semantic versioning (ox uses 0.X.0 pattern)
if ! [[ $NEW_VERSION =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo -e "${RED}Error: Invalid version format '$NEW_VERSION'${NC}"
    echo "Expected: MAJOR.MINOR.PATCH (e.g., 0.10.0)"
    exit 1
fi

# Check we're in repo root
if [ ! -f "internal/version/version.go" ]; then
    echo -e "${RED}Error: Must run from repository root${NC}"
    exit 1
fi

# Get current version
CURRENT_VERSION=$(grep 'Version.*=' internal/version/version.go | sed 's/.*"\(.*\)".*/\1/')

if [ -z "$CURRENT_VERSION" ]; then
    echo -e "${RED}Error: Could not read current version${NC}"
    exit 1
fi

echo -e "${YELLOW}Bumping: $CURRENT_VERSION → $NEW_VERSION${NC}"
echo ""

# Cross-platform sed helper
update_file() {
    local file=$1
    local old=$2
    local new=$3
    if [[ "$OSTYPE" == "darwin"* ]]; then
        sed -i '' "s|$old|$new|g" "$file"
    else
        sed -i "s|$old|$new|g" "$file"
    fi
}

echo "Updating version files..."

# 1. internal/version/version.go (canonical source)
echo "  - internal/version/version.go"
update_file "internal/version/version.go" "Version   = \"$CURRENT_VERSION\"" "Version   = \"$NEW_VERSION\""

# 2. .claude-plugin/marketplace.json (plugin marketplace)
if [ -f ".claude-plugin/marketplace.json" ]; then
    echo "  - .claude-plugin/marketplace.json"
    update_file ".claude-plugin/marketplace.json" "\"version\": \"$CURRENT_VERSION\"" "\"version\": \"$NEW_VERSION\""
fi

# 3. claude-plugin/.claude-plugin/plugin.json (plugin manifest)
if [ -f "claude-plugin/.claude-plugin/plugin.json" ]; then
    echo "  - claude-plugin/.claude-plugin/plugin.json"
    update_file "claude-plugin/.claude-plugin/plugin.json" "\"version\": \"$CURRENT_VERSION\"" "\"version\": \"$NEW_VERSION\""
fi

# 4. npm/package.json (@sageox/ox npm wrapper — CI re-pins this from the tag at
#    publish, but keep the committed default in sync so local `npm pack` is honest)
if [ -f "npm/package.json" ]; then
    echo "  - npm/package.json"
    update_file "npm/package.json" "\"version\": \"$CURRENT_VERSION\"" "\"version\": \"$NEW_VERSION\""
fi

# 5. pip/ wrapper (follow-up skeleton — keep in sync so it's release-ready)
if [ -f "pip/pyproject.toml" ]; then
    echo "  - pip/pyproject.toml"
    update_file "pip/pyproject.toml" "version = \"$CURRENT_VERSION\"" "version = \"$NEW_VERSION\""
fi
if [ -f "pip/sageox/__init__.py" ]; then
    echo "  - pip/sageox/__init__.py"
    update_file "pip/sageox/__init__.py" "__version__ = \"$CURRENT_VERSION\"" "__version__ = \"$NEW_VERSION\""
fi

# 6. CHANGELOG.md - check if version already present
if [ -f "CHANGELOG.md" ]; then
    if ! grep -q "\[$NEW_VERSION\]" CHANGELOG.md; then
        echo -e "  - CHANGELOG.md ${YELLOW}(needs manual update)${NC}"
    else
        echo "  - CHANGELOG.md (already has $NEW_VERSION)"
    fi
fi

echo ""
echo -e "${GREEN}Version updated to $NEW_VERSION${NC}"
echo ""
echo "Changed files:"
git diff --stat 2>/dev/null || true
echo ""
echo -e "${YELLOW}Next steps:${NC}"
echo "  1. Update CHANGELOG.md with release notes for $NEW_VERSION"
echo "  2. Run: make verify-version"
echo "  3. Commit: git add internal/version/version.go CHANGELOG.md && git commit -m 'chore(release): bump version to $NEW_VERSION'"
echo "  4. Create release in GitHub UI"
