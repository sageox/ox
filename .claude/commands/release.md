Prepare and create a new ox release.

Use when:
- Ready to release a new version of ox
- User says "release", "cut a release", "prepare release", "version bump"

Arguments: $ARGUMENTS (optional version number like "0.15.0", or empty to auto-propose)

## Release Workflow

Follow these steps exactly:

### Step 1: Pre-flight Checks

```bash
# Verify on main branch
git branch --show-current

# Ensure working directory is clean (or only has expected changes)
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

### Step 2: Analyze Changes Since Last Release

```bash
# Get current version from version.go
grep 'Version.*=' internal/version/version.go

# Get latest git tag
git describe --tags --abbrev=0

# Show commits since last tag (for changelog)
git log $(git describe --tags --abbrev=0)..HEAD --oneline --no-merges
```

### Step 3: Update CHANGELOG.md

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

### Step 4: Bump Version

```bash
# Update version.go (replace X with actual version)
make bump-version NEW_VERSION=0.X.0

# Verify version matches
make verify-version
```

### Step 5: Commit Changes

```bash
git add CHANGELOG.md internal/version/version.go
git commit -m "release: v0.X.0"
```

### Step 6: Create Git Tag

```bash
git tag v0.X.0
```

### Step 7: Create Draft GitHub Release

Extract the changelog section for this version and create a draft release:

```bash
gh release create v0.X.0 --draft --title "v0.X.0" --notes-file -
```

Pipe the release notes (the changelog section for this version) to the command.

### Step 8: Final Instructions

After completing all steps, tell the user:

1. **Review the draft release** at: https://github.com/sageox/ox/releases
2. **Push when ready**: `git push && git push --tags`
3. **Publish the release** in GitHub to trigger GoReleaser automation

## Important Rules

- Version format: `0.<release>.0` (middle number increments)
- Patch releases (0.X.1) are VERY RARE - only critical hotfixes
- One release per day max
- NEVER auto-generate changelogs from commits
- ALWAYS ask user to confirm version before bumping
- Draft releases only - human publishes
