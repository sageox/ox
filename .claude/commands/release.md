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

# Run quality gates (run lint and tests in parallel)
make lint
make test-all          # all unit tests incl. expensive (git clone, SQLite, LFS)
make test-slow         # build tag: slow (real ox binary tests)
make test-digital-twin # ledger + team-context structural verification
```

If tests or lint fail, fix issues before proceeding.

### Step 1b: E2E Integration Tests (MANDATORY — requires claude CLI + ANTHROPIC_API_KEY)

```bash
make test-integration
```

**This is a hard release gate.** These tests launch real Claude Code instances, exercise real hooks, send real SIGINT signals, and verify the full session recording and anti-entropy pipelines end-to-end. Do NOT proceed if integration tests fail.

### Step 1c: Smoke Tests (requires SAGEOX_CI_PASSWORD)

```bash
make smoke-test
```

This runs end-to-end tests against test.sageox.ai: auth, init, doctor, status, re-init, agent prime, session list, and clone-without-ox. If smoke tests fail, investigate before proceeding — these verify ox works in a real environment.

### Step 1d: Run Walks (Human-Driven)

**Coordinate in the Slack channel.** A team member installs the release candidate and walks through core workflows on a real machine:

1. **Fresh setup** — `ox login` → `ox init` on a new repo → `ox doctor` → `ox status`
2. **Agent prime** — open Claude Code, verify `ox agent prime` loads team context
3. **Session recording** — start a session, do some work, stop it, verify `ox session list`
4. **Team context** — verify team knowledge appears via `ox agent team-ctx`
5. **Clone experience** — clone the repo in a fresh directory, run `ox init`
6. **Doctor recovery** — delete `.sageox/config.json`, run `ox doctor`, verify auto-repair
7. **Upgrade path** — if upgrading, verify `ox version` and existing config survive

Report pass/fail per step, OS/arch, and ox version back in Slack. Do NOT proceed to version bump until Run Walks pass.

**Tip:** Run lint, test-all, smoke-test, and test-integration in parallel background tasks to save time. See `docs/guides/release-testing-playbook.md` for the full testing reference.

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

Read `.claude/rules/releases.md`'s **Release Notes Guidelines** section first — it's the
canonical source for release-note style, this step just applies it. The one question every
bullet answers: **"As a user, what can I now do that I couldn't do before?"** Target bar:
Conductor, Wispr Flow, Raycast, Linear, Arc — a reader understands the whole release in
under a minute.

1. Add a new version section at the top (after the header)
2. Group changes by: `New`, `Improved`, `Fixed`, `Security` (security only when relevant)
3. **Collapse related work into themes.** Ten engineering fixes are often one user-visible
   improvement — merge aggressively rather than listing each one
4. **Capability first, mechanism never** (unless it changes something the user can see) —
   no internal architecture, algorithms, storage, migrations, daemon behavior, internal IDs
5. NO commit hashes, PR numbers, or commit prefixes like "feat(scope):"
6. NO internal jargon — spell out the user-visible effect, never protocol/impl detail (SSE,
   CSP, OTLP, askpass, HTTP status codes, struct/field/signal names, file paths). Name the
   command and any `SAGEOX_*` env var
7. Keep it crisp — one or two sentences per entry, usually one, never a paragraph
8. Use today's date in YYYY-MM-DD format
9. Target roughly 3–8 New, 2–5 Improved, 3–8 Fixed. More than ~20 bullets total means it
   hasn't been distilled enough — merge harder

Example format:
```markdown
## [0.X.0] - YYYY-MM-DD

### New
- **Feature Name** — clear description of what users can now do

### Improved
- Description of the user-visible improvement (often merging several fixes/changes)

### Fixed
- Bug that was affecting users, described by its user-visible effect
```

Capability-first vs. mechanism-first — the single most important rule:
- ✅ **Publish a plan as a Claude Code Artifact** — a self-contained page that renders with no network access.
- ❌ **CSP-safe artifact render** — drops the SSE loop and inlines the Mermaid CDN so the page passes Content-Security-Policy.
- ✅ **Sync is significantly more reliable, including recovery from interrupted sessions and diverged history.**
- ❌ "fixed sync / fixed retries / fixed divergence / fixed GPG / fixed daemon startup" (five bullets that are one user-visible theme)

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
