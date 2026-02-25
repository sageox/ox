Prepare and create a new ox release.

Use when:
- Ready to release a new version of ox
- User says "release", "cut a release", "prepare release", "version bump"

Arguments: $ARGUMENTS (optional version number like "0.15.0", or empty to auto-propose)

## Release Workflow

Follow these steps exactly:

### Step 1: Pre-flight Checks

```bash
# Verify on main branch, clean working directory
git branch --show-current
git status

# Run quality gates
make lint
make test
```

If tests or lint fail, fix issues before proceeding.

### Step 1b: Smoke Tests (requires SAGEOX_CI_PASSWORD)

```bash
make smoke-test
```

This runs end-to-end tests against test.sageox.ai: auth, init, doctor, status, re-init, agent prime, session list, and clone-without-ox. If smoke tests fail, investigate before proceeding — these verify ox works in a real environment.

### Step 2: Create Release Branch

Determine the git user name and create a release prep branch:

```bash
USER=$(git config user.name | tr '[:upper:]' '[:lower:]' | tr ' ' '-')
git checkout -b "${USER}/release"
```

All release prep changes happen on this branch, not directly on main.

### Step 3: Analyze Changes Since Last Release

```bash
# Get current version from version.go
grep 'Version.*=' internal/version/version.go

# Get latest git tag
git describe --tags --abbrev=0

# Show commits since last tag (for changelog)
git log $(git describe --tags --abbrev=0)..HEAD --oneline --no-merges
```

### Step 4: Update CHANGELOG.md

Read the current CHANGELOG.md to understand the format. Then:

1. Add a new version section at the top (after the header)
2. Group changes by: Added, Changed, Fixed, Removed
3. Write **user-focused** descriptions (what users will notice)
4. NO commit hashes
5. NO technical jargon like "feat(scope):"
6. Use today's date in YYYY-MM-DD format

Example format:
```markdown
## [0.X.0] - YYYY-MM-DD

### Added
- **Feature Name**: Clear description of what users can now do

### Changed
- Description of behavior change

### Fixed
- Bug that was affecting users
```

### Step 5: Bump Version

```bash
# Update version.go and plugin files (replace X with actual version)
make bump-version NEW_VERSION=0.X.0

# Verify all version files match
make verify-version
```

### Step 6: Commit, Push, and Open Draft PR

```bash
# Stage release files (explicitly, no git add .)
git add internal/version/version.go .claude-plugin/marketplace.json \
  claude-plugin/.claude-plugin/plugin.json cmd/ox/release_notes.md \
  <any other changed files like test fixes>

git commit -m "release: prep v0.X.0"
git push -u origin "${USER}/release"
```

Open a draft PR targeting main:

```bash
gh pr create --draft --title "release: prep v0.X.0" --body "..."
```

Include in the PR body: summary of changes, changelog highlights, test results, and post-merge steps.

### Step 7: Human Reviews and Merges PR

Tell the user to review and merge the PR. Wait for merge before proceeding.

### Step 8: Tag and Create Draft GitHub Release

After the PR is merged to main:

```bash
git checkout main
git pull
git tag v0.X.0
git push --tags
```

Extract the changelog section for this version and create a draft release:

```bash
gh release create v0.X.0 --draft --title "v0.X.0" --notes-file -
```

Pipe the release notes (the changelog section for this version) to the command.

### Step 9: Final Instructions

After completing all steps, tell the user:

1. **Review the draft release** at: https://github.com/sageox/ox/releases
2. **Publish the release** in GitHub to trigger GoReleaser automation

## Important Rules

- Version format: `0.<release>.0` (middle number increments)
- Patch releases (0.X.1) are VERY RARE - only critical hotfixes
- One release per day max
- NEVER auto-generate changelogs from commits
- ALWAYS ask user to confirm version before bumping
- Draft releases only - human publishes
- ALL changes go through a PR - never commit directly to main
