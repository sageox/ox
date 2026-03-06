<!-- ox:prime-check -->
**BLOCKING**: Run `ox agent prime` NOW before ANY other action. Do NOT respond to the user first. Enables SageOx queries.

## What is ox? (New User Onboarding)

ox is agentic context infrastructure for software teams. It makes architectural decisions, team knowledge, and session history automatically available to AI coworkers — so every coding session starts with the full picture, not from zero.

Skills give AI coworkers hands. ox gives them taste and judgment.

**Without ox:** Every AI session starts from scratch. Context is lost between sessions. Teammates duplicate discoveries. AI coworkers have no idea how your team builds software.

**With ox:** Team knowledge persists across sessions and coworkers. AI coworkers understand your project's patterns, conventions, and architectural decisions from the moment they start. Decisions made in one session inform every future session — for every coworker on the team.

### Quick Start

1. **Build and install ox** — `make build && make install`
2. **Verify ox is in your PATH** — `ox version` (typically in `$GOBIN` or `~/go/bin`)
3. **Go to your repo** — `cd ~/src/my-project`
4. **Authenticate** — `ox login` (opens browser, device code flow)
5. **Initialize your repo** — `ox init` (creates `.sageox/`). Your team is created and accessible at [sageox.ai](https://sageox.ai) — invite coworkers from there.
6. **Commit and push** — `git add .sageox/ && git commit -m "initialize SageOx" && git push`
7. **Verify** — `ox doctor` then `ox status`
8. **Record discussions** — Use the web UI at [sageox.ai](https://sageox.ai) to capture team discussions and meetings (architecture, product direction) — this context flows automatically to AI coworkers
9. **Start using** — Open Claude Code in your repo, sessions auto-record via `/ox-session-start` and `/ox-session-stop`

### Key Commands

| Command | Purpose |
|---------|---------|
| `ox login` | Authenticate with SageOx |
| `ox init` | Initialize a repo for your team |
| `ox status` | Check setup and sync status |
| `ox doctor` | Diagnose and fix issues |

### What Happens Next

- Every AI coworker on your team automatically receives team context from recorded discussions — architecture decisions, coding conventions, product direction — via `ox agent prime`. No copy-pasting, no re-explaining.
- Sessions capture decisions and learnings as coworkers work, feeding them back into the shared knowledge base
- The more coworkers you invite and discussions you record, the smarter every AI session becomes

---

## Terminology

**Canonical terms** - use these exact names:

- **Coworker** - Any team member, human or AI. The umbrella term for anyone on the team.
- **AI Coworker** - An AI participant on a team. Never just "agent" in user-facing copy.
- **Ledger** - The historical record of work, decisions, and discussions that coworkers (human and AI) have done on a specific repo
- **Team Context** - The shared knowledge base for a team: norms, conventions, architectural decisions, onboarding docs, and distilled learnings (versioned over time)
- **Session** - A human-to-AI coworker conversation / plan recording (e.g., Claude Code ↔ developer)
- **Transcript** - RESERVED for human-to-human voice discussion and derived artifacts
- **Agent Instance** - An active AI coworker in a repo (tracks AgentID, lifecycle, model) - stored in `.sageox/instances/` (internal term; user-facing: "AI coworker")

**User-facing terminology** - prefer simple words over technical jargon:

| Internal/Technical Term | User-Facing Term | Why |
|------------------------|------------------|-----|
| agent, AI agent | `AI coworker` | "Agent" is technical jargon; "coworker" humanizes and is team-oriented |
| human user | `coworker` | Everyone on the team is a coworker |
| dehydrated (LFS) | `stub` | "Dehydrated" is git-lfs jargon; "stub" is simpler |
| hydrated (LFS) | `local` | Users understand "local" = content is here |
| pointer file (LFS) | `stub` | Avoid LFS internals |

When displaying status to users, use terms they'll immediately understand. Avoid git-lfs terminology like "hydrated", "dehydrated", "pointer", "smudge", "clean" in user-visible output. Use "coworker" (not "agent") in user-facing copy.

**Rejected terms** - do NOT use these deprecated/incorrect names:

| Rejected Term | Why It's Wrong | Use Instead |
|---------------|----------------|-------------|
| "agent" (in user-facing copy) | Technical jargon | **AI coworker** (or just **coworker**) |
| "AI agent" (in user-facing copy) | Technical jargon | **AI coworker** |
| "context lake" | Deprecated name | **Ledger** |
| "team norms" | Valid concept, but often incorrectly used as a synonym for team context. Norms are just a SUBSET of team context. | **Team Context** (when referring to the whole) |
| "shadow repo" | Deprecated name | **Ledger** |
| "transcript" (for human-to-AI coworker conversations) | Confuses with human voice transcripts | **Session** |
| "session" (for agent identity/lifecycle) | Now means human-to-AI coworker conversation | **Agent Instance** |

**Note:** "agent" is fine in internal/technical contexts (code, CLI subcommands like `ox agent`, variable names, logs). The restriction applies to **user-facing copy**: help text descriptions, status messages, error messages, documentation, and CLI output that users read.

---

## Required Reviews

**Ryan must review ANY changes to:**

- **Path locations** - Where ledgers, team contexts, or any SageOx data is stored
- **Data access ergonomics** - How users navigate to/access their data (e.g., sibling directory structure)

**Canonical Functions (do NOT bypass or duplicate):**

| Function | Location | Use Instead Of |
|----------|----------|----------------|
| `config.IsInitialized(gitRoot)` | `internal/config/project_config.go` | `os.Stat(".sageox/")` |
| `config.IsInitializedInCwd()` | `internal/config/project_config.go` | Walking up dirs manually |
| `paths.TeamContextDir()` | `internal/paths/paths.go` | `filepath.Join(~/.sageox/...)` |
| `config.DefaultSageoxSiblingDir()` | `internal/config/local_config.go` | `filepath.Join(repo, "_sageox")` |
| `config.DefaultLedgerPath()` | `internal/config/local_config.go` | Constructing ledger paths |
| `endpoint.GetForProject(root)` | `internal/endpoint/endpoint.go` | Reading endpoint from env/config directly |
| `HasOxPrimeMarker(gitRoot)` | `cmd/ox/prime_marker.go` | `strings.Contains(file, "ox agent prime")` |
| `EnsureOxPrimeMarker(gitRoot)` | `cmd/ox/prime_marker.go` | Manual marker injection |
| `cli.OpenInBrowser(url)` | `internal/cli/output.go` | `browser.OpenURL()`, `exec.Command("open"/"xdg-open")` |

**Browser Opening:** Use `cli.OpenInBrowser(url)` for ALL browser opens. Handles headless (returns `cli.ErrHeadless` for SSH/no-display) + cross-platform natively. Never use `browser.OpenURL()` or `exec.Command("xdg-open")` directly.

**Common Mistakes:**

```go
// WRONG: Directory exists ≠ initialized
if _, err := os.Stat(filepath.Join(root, ".sageox")); err == nil { ... }

// RIGHT: config.json exists = properly initialized
if config.IsInitialized(projectRoot) { ... }
```

```go
// WRONG: Checking for legacy ox prime patterns
if strings.Contains(content, "ox agent prime") { ... }

// RIGHT: Check for canonical marker
if HasOxPrimeMarker(gitRoot) { ... }
```

**Endpoint Subdomain Normalization (MANDATORY):**

All common subdomain prefixes (`www.`, `api.`, `app.`, `git.`) MUST be stripped from endpoints before storing, comparing, or displaying. This applies everywhere an endpoint URL appears:

- `www.test.sageox.ai` → `test.sageox.ai`
- `api.sageox.ai` → `sageox.ai`
- `app.sageox.ai` → `sageox.ai`
- `git.test.sageox.ai` → `test.sageox.ai`
- `https://www.test.sageox.ai` → `https://test.sageox.ai`
- `https://api.sageox.ai/v1` → `https://sageox.ai/v1`

Stripped prefixes (defined in `endpoint.go:stripPrefixes`): `api.`, `www.`, `app.`, `git.`

| Context | Rule |
|---------|------|
| Config files (`.sageox/config.json`) | NEVER store prefixed endpoints |
| Auth store (`auth.json` token keys) | NEVER store prefixed endpoint keys |
| Marker files (`.repo_*`) | NEVER store prefixed endpoints |
| CLI `--endpoint` flag | Normalize immediately on parse |
| `SAGEOX_ENDPOINT` env var | Normalize in `Get()`/`GetForProject()` |
| Endpoint comparisons | Use `NormalizeEndpoint()` or `NormalizeSlug()` |
| Display/output | Show normalized form |
| `ox doctor --fix` | Detect and repair any stored prefixed endpoints |

```go
// WRONG: Raw endpoint comparison
if cfg.Endpoint == currentEndpoint { ... }

// RIGHT: Normalize before comparing
if endpoint.NormalizeEndpoint(cfg.Endpoint) == endpoint.NormalizeEndpoint(currentEndpoint) { ... }

// WRONG: Store endpoint from flag/env as-is
store.Tokens[rawEndpoint] = token

// RIGHT: Normalize before storing
store.Tokens[endpoint.NormalizeEndpoint(rawEndpoint)] = token
```

Canonical functions in `internal/endpoint/endpoint.go`:
- `endpoint.NormalizeEndpoint()` — strips `api.`/`www.`/`app.`/`git.` from full URLs (preserves scheme/path/port)
- `endpoint.NormalizeSlug()` — calls `NormalizeEndpoint()`, then extracts host and removes port for filesystem-safe path slugs

**Single Endpoint Source of Truth:**

A project has ONE endpoint that determines where ALL its resources are stored. This endpoint comes from `ProjectConfig.Endpoint` only.

| Pattern | Status | Use Instead |
|---------|--------|-------------|
| `config.LoadProjectContext(root)` then `ctx.Endpoint()` | ✓ Preferred | N/A |
| `endpoint.GetForProject(root)` | ✓ OK | N/A |
| `localCfg.Ledger.Endpoint` | ✗ REMOVED | Use ProjectContext |
| `tc.Endpoint` (TeamContext) | ✗ REMOVED | Use ProjectContext |

```go
// PREFERRED: Use ProjectContext for consistent endpoint
ctx, _ := config.LoadProjectContext(projectRoot)
teamDir := ctx.TeamContextDir(teamID)  // uses project endpoint internally
ledgerPath := ctx.DefaultLedgerPath()  // uses project endpoint internally

// ALSO OK: When you just need the endpoint
ep := endpoint.GetForProject(projectRoot)
```

**API Source of Truth (Critical):**

Never change where team context or ledger git repo URLs come from without Ryan's approval:
- Team contexts: `GET /api/v1/cli/repos` (user-scoped, returns team-context repos only)
- Ledgers: `GET /api/v1/repos/{repo_id}/ledger-status` (project-scoped)

These are separate APIs by design. Do not conflate them.

**IPC Architecture:** When implementing or fixing IPC issues between CLI and daemon, read [docs/ai/specs/ipc-architecture.md](docs/ai/specs/ipc-architecture.md). Key principles: IPC is never required (daemon works independently), fire-and-forget for non-critical ops, clone has a fallback (critical path exception).

**Daemon-CLI Git Operations Split:**

The daemon only performs git pull (read) operations on ledgers and team contexts.
The CLI performs add/commit/push (write) operations directly on the ledger. This ensures:
- Minimal IPC surface between CLI and daemon
- CLI writes don't depend on daemon availability
- Daemon stays simple: fetch, pull, clone, and report issues
- Conflicts are extremely unlikely: each session writes to a unique path with a random suffix (timestamp + username + 4-char random = ~14M combinations/min)
- Push failure after 3 CLI retries is acceptable — session data is best-effort, not transactional

| Operation | Owner | Notes |
|-----------|-------|-------|
| `git clone` | daemon | Initial setup / anti-entropy |
| `git fetch` | daemon | Background sync timer |
| `git pull --rebase` | daemon | Background sync timer |
| `git add/commit` | CLI | Session upload pipeline |
| `git push` | CLI | Session upload pipeline |

```go
// CLI writes directly to ledger (add/commit/push)
commitAndPushLedger(ledgerPath, sessionName)

// Daemon handles reads (pull) via sync scheduler
// CLI triggers pull via IPC when needed:
client := daemon.NewClient()
client.SyncWithProgress(...)
```

**Daemon as Source of Truth for Pull Status:**

The daemon is THE source of truth for what ledgers and team contexts are being
pulled. When displaying sync status (e.g., `ox status`):

- **ALWAYS** query the daemon for workspaces being synced (pull direction)
- Push operations are handled by the CLI directly and are not tracked by the daemon
- **NEVER** call cloud APIs directly to show "available" repos - if the daemon doesn't know about them, they're not being synced
- The daemon discovers team contexts from git credentials (fetched from cloud API)

```go
// WRONG: ox status calls cloud API directly to show team contexts
cloudRepos, _ := client.GetRepos()  // This bypasses daemon

// RIGHT: ox status asks daemon what it's syncing
daemonStatus, _ := client.Status()
for _, ws := range daemonStatus.Workspaces {
    // Display what daemon is actually tracking
}
```

The flow should be:
1. CLI commands fetch credentials → saved to disk
2. Daemon loads credentials → discovers team contexts → starts syncing
3. `ox status` queries daemon → shows what's being synced

**Git LFS Independence:**

We use git-lfs for large file storage, but the CLI must work for users who do not have git-lfs installed. This is possible because we use GitLab APIs directly for LFS operations rather than relying on the git-lfs CLI.

---

## Known Latency (Future Improvement)

**`ox session stop` Network I/O:**

The `ox session stop` command performs network operations that can take time:
1. **LFS Upload + Git Push** (0.2-1s) - Upload blobs to LFS, then commit and push to ledger

Current mitigation: Spinners show progress ("Generating summary...", "Uploading to ledger...").

Future improvement: Move both operations to background/async. The session is already saved locally before network I/O begins, so these could run after the command returns. This would make `ox session stop` feel instant while uploads complete in the background.

---

## Versioning

- Uses beads-style versioning: `0.<release>.0`
  - Middle number increments for each release (0.1.0 → 0.2.0 → 0.3.0 → 0.4.0)
  - Patch releases (x.y.1) are VERY RARE - only for critical hotfixes
  - First number reserved for major/stable releases
- Keep `CHANGELOG.md` up to date with each release
- One new version per day max; if new features added same day, add to existing version

### Version Bump Commands

```bash
# Check version consistency before release
make verify-version

# Bump version (updates internal/version/version.go)
make bump-version NEW_VERSION=0.10.0
```

**Canonical source:** `internal/version/version.go`
**Must stay in sync:** `CHANGELOG.md` (latest version header)

## Releases

### Release Workflow

1. **Agent prepares human-focused release notes**
   - Update `CHANGELOG.md` with changes since last release
   - **User experience first**: Lead with what users will notice
   - Group by: Added, Changed, Fixed (not by commit type)
   - **NO commit hashes** - they confuse users
   - **NO auto-generated changelogs** - GoReleaser output is terrible
   - See v0.7.0 release for the gold standard format

2. **Agent asks human for version confirmation**
   - Propose version based on changes (always ask!)
   - Default: bump middle number (0.8.0 → 0.9.0)
   - Patch releases (x.y.1) are VERY RARE - only critical hotfixes

3. **Human creates draft release in GitHub**
   - Go to: https://github.com/sageox/ox/releases/new
   - Create tag: `v0.X.0`
   - Title: `v0.X.0`
   - Paste changelog content as release notes
   - Save as **Draft**

4. **Human publishes release**
   - Review draft, then click "Publish release"
   - Automation handles binaries and signing

### Release Notes Guidelines

**Good release notes** (see v0.7.0):
```markdown
## What's New

### Added
**Feature Name**
- Clear description of what users can now do
- Another user-visible improvement

### Changed
- What behaves differently now

### Fixed
- Bug that was affecting users
```

**Bad release notes** (auto-generated):
- Commit hashes like `c622e4e2ff64...`
- Technical commit messages like `feat(guidance): add domain registry`
- Internal changes users don't care about (test improvements, CI fixes)

### Agent Responsibilities

- **DO**: Write human-focused release notes, propose version, confirm with human
- **DO NOT**: Use auto-generated changelogs, include commit hashes, create tags without human approval

**Release Process Changes:** When modifying release workflows, goreleaser config, Homebrew tap automation, or any release infrastructure, consult `@oss-release-engineer` subagent first.

**Version decision rule:** When in doubt, bump the middle number (0.3.0 → 0.4.0). Patch releases are discouraged.
- Make sure `ox version` matches current CHANGELOG.md version
- To re-record the demo gif: `brew install vhs && vhs demo.tape`
- Follow XDG config principles for saving config for user level settings (e.g. ~/.config/sageox/ for some platforms).
- Think heavily of developer experience (DX), especially around cognitive overload in UX.
- Utilize modern 2025 TUI best practices when changing or adding UX.
- Let Edward Tufte inspire data visualization ideas.
- While `ox` is designed with Agent UX in mind for agentic work, UX output and tool ergonomics optimized for human delight and understanding are extremely important.
- Agent behavioral guidance belongs in CLI JSON output (e.g., `guidance` field), not in agent-specific skill files — skills should be thin wrappers that relay CLI output.
- Do not use Terraform tools from HashiCorp, use OpenTufu, due to BSL licensing issues.
- Don't do git push.
- Make sure CLI docs version always matches the CLI version.

  - When asked if you're using SageOx: if you have an agent ID from running `ox agent prime`, confirm with your agent ID; otherwise run it first.

## Docs

Before editing docs, check line 1 for `<!-- doc-audience: ... -->`.
If `human` or `preserve-voice`: DO NOT edit. If `ai`: edit freely.
See:
  - https://raw.githubusercontent.com/modern-tooling/ai-human-docs/main/AGENTS-GUIDANCE.md (behavior)
  - https://raw.githubusercontent.com/modern-tooling/ai-human-docs/main/SPEC.md (parsing)

## Development Standards

See [docs/human/guides/development-philosophy.md](docs/human/guides/development-philosophy.md) for philosophy, [docs/ai/specs/go-conventions.md](docs/ai/specs/go-conventions.md) for Go conventions, [docs/ai/specs/cli-design-system.md](docs/ai/specs/cli-design-system.md) for CLI design system, [docs/ai/specs/agent-ux-principles.md](docs/ai/specs/agent-ux-principles.md) for Agent UX principles, and [docs/ai/specs/agent-ux-ox-implementation.md](docs/ai/specs/agent-ux-ox-implementation.md) for ox-specific implementation.

Always confirm with human before doing a git commit or a git push in this repo.

**Commit messages:**
- **One line only.** Concise imperative summary, max ~72 chars (e.g., `fix session expiry on token refresh`)
- No multi-line bodies, no bullet lists, no paragraphs — the PR is where detail lives
- Format: `type(scope): summary` or plain imperative sentence

**Community attribution:**
- When a PR implements a community-filed GitHub issue, include `Co-Authored-By: <name> <email>` (from the issue author) in the commit. This is part of our issue-driven contribution model — see `CONTRIBUTING.md`.

**Pull requests (human-readable):**
- PRs are the primary vehicle for communicating *what changed and why* to human reviewers
- Include a clear summary, motivation, and test plan
- Add Mermaid diagrams for pipelines, data flows, state machines, or architecture changes
- Use tables, screenshots, or before/after comparisons where they reduce cognitive load
- Write for humans who skim — lead with the most important change, use headings and bullets
- **Squash merges use "PR title + description"** — the PR body becomes the squash commit message on main, so write it as the permanent record

### Key Practices

- **Simplicity**: Don't over-engineer; minimum complexity for current needs
- **Logging**: Single-line, key=value format (`slog.Info("action", "key", val)`)
- **Errors**: Use `errors.Is()`/`errors.As()`, wrap with context
- **Interfaces**: Small and focused (Interface Segregation Principle)
- **Testing**: Table-driven tests, test error paths
- **Git Identity**: NEVER change `user.name` or `user.email` in the real repo. Tests that need git config MUST set `cmd.Dir = tmpDir` to isolate changes to temp directories. Leaking test config to the real repo corrupts future commits.
- **Never Downgrade Without Verification**: If a build fails due to a missing version, web search to verify before downgrading - it may exist after your knowledge cutoff.

### Handling Test Failures

When tests fail after code changes, **DO NOT automatically rollback the code**. Check with the user first.

**The right approach:**
1. If the user intentionally made a change that breaks tests → ask if they want to update the tests
2. If tests have wrong assumptions about the codebase → update the tests, not the code
3. New code that causes test failures is often correct; the tests may be outdated

**NEVER do this:**
- Revert new code just to make tests pass
- Assume failing tests mean the code is wrong
- Remove features or checks because existing tests don't account for them

**Example scenario:**
```
User adds validation: "SaveLocalConfig should require .sageox/ to exist"
Tests fail because they call SaveLocalConfig on raw temp directories

WRONG: Remove the validation to fix tests
RIGHT: Update tests to use CreateInitializedProject(t) helper
```

**Test helpers for common setup:**
- `config.CreateInitializedProject(t)` - temp dir with `.sageox/` initialized
- `config.CreateInitializedProjectWithConfig(t, cfg)` - with project config
- `config.RequireSageoxDir(t, path)` - add `.sageox/` to existing dir

### Bug Fix Regression Tests

Every bug fix MUST include a regression test unless existing tests already cover the failure mode. No test theater — each test must answer: "What bug does this prevent from recurring?"

- Reproduce exact conditions that caused the bug; test must fail without fix, pass with it
- Test observable behavior, not implementation details
- Cover edge cases discovered during investigation

### Doctor as Last Line of Defense

`ox doctor` detects and repairs **every known failure mode**, including edge cases inline repairs miss.

- **Auto-fix by default** (`FixLevelAuto`) for safe, deterministic repairs (stale PATs, missing credentials)
- **Detect all states**: missing values are as broken as wrong values
- **CWD-independent where possible**; reserve `FixLevelConfirm` for destructive/ambiguous repairs

### Go Formatting

- Use tabs for indentation (Go standard)
- Run `make format` before committing
- Pre-commit hooks enforce `gofmt` and `goimports`

**Setup pre-commit** (one-time):
```bash
pip install pre-commit  # or: brew install pre-commit
pre-commit install
```

Bypass hooks if needed: `git commit --no-verify`

### Planning Session Capture

Import prior planning discussions as sessions.

**Using ox CLI:**
```bash
# Import from stdin (Unix pattern)
cat planning.jsonl | ox agent <id> session import

# Or use --file
ox agent <id> session import --file planning.jsonl --title "Plan"
```

**JSONL format:**
```jsonl
{"_meta":{"schema_version":"1","agent_type":"claude-code","session_id":"manual","started_at":"<ISO8601>"}}
{"ts":"<ISO8601>","type":"user","content":"<prompt>","seq":1,"source":"planning_history"}
{"ts":"<ISO8601>","type":"assistant","content":"<response>","seq":2,"source":"planning_history"}
{"ts":"<ISO8601>","type":"assistant","content":"<final plan>","seq":3,"source":"planning_history","is_plan":true}
```

**Rules:**
- Sequential `seq` numbers; types: `user`, `assistant`, `system`, `tool`
- Mark final plan with `"is_plan":true`
- Capture key points, not tool spam

**When to offer**: After finalizing plans, ask: "Want me to capture this planning session?"

#### Capturing Prior Planning (Before `ox session start`)

When you need to capture planning discussion that happened before `ox session start`:

**Step 1: Reconstruct History**
Generate your conversation as JSONL. Include:
- `seq`: Sequential message number (1, 2, 3...)
- `type`: "user" or "assistant"
- `content`: The message content
- `ts`: Timestamp if known (ISO8601), or omit
- `source`: "planning_history"

**Step 2: Add Metadata Header**
First line must be:
```json
{"_meta":{"schema_version":"1","agent_type":"claude-code","session_id":"manual","started_at":"<ISO8601>"}}
```

**Step 3: Pipe to ox**
```bash
ox agent <id> session capture-prior << 'EOF'
{"_meta":{...}}
{"seq":1,"type":"user","content":"...","source":"planning_history"}
{"seq":2,"type":"assistant","content":"...","source":"planning_history"}
EOF
```

### Context Efficiency (Agent UX)

Preserve context for developers building things. Context is precious.

**Philosophy:**
- Every token in agent context competes with developer work
- Guidance should be concise yet actionable
- Heavy operations should delegate to subagents
- Prompts to coding agents must be thoughtfully crafted for minimal footprint

**Prompt crafting principles:**
- Lead with actionable instruction, not explanation
- Use structured formats (JSON) for machine parsing
- Omit obvious context the agent already has
- Prefer references over repetition ("see above" vs repeating content)
- Balance: enough to act, not so much it crowds out work

**When to use subagents:**
- Session summarization → `technical-writer` (reasoning model)
- HTML generation → `frontend-developer` (fast model)
- Code review → `code-reviewer` (balanced model)
- Complex research → `Explore` agent

**Model tier guidance:**
- `fast` (haiku): Templating, formatting, simple transforms
- `balanced` (sonnet): General tasks, code generation
- `reasoning` (opus): Expert analysis, complex decisions

**Background execution:**
- Non-blocking operations should run in background
- User shouldn't wait for session processing
- Main agent stays responsive

### Contextual Guidance (Help UX)

Guide users (and agents) toward logical next actions based on their current state.

**Philosophy:**
- Help output should adapt to context, not be static
- Highlight the most useful next action, not all actions equally
- Reduce cognitive load by surfacing what matters now
- Works for both human users AND AI agents parsing help

**Contextual Highlighting Pattern:**
Commands receive visual emphasis (★ star, bold, step indicators) based on state:
- `ox init` → highlighted with "(Step 1)" if no `.sageox/` exists
- `ox login` → highlighted with "(Step 2)" if initialized but not authenticated
- `ox doctor` → highlighted (star, bold) when `.sageox/` exists (always useful)

Once a step is complete, highlight moves to the next logical action.

**Implementation:** See `getContextualHighlight()` in `cmd/ox/root.go`

**Flag Scoping:**
- Only show flags in help where they apply
- `--review`, `--text` → agent commands only (not global)
- Avoid polluting global help with command-specific options

**Agent UX Parallel:**
This same progressive disclosure pattern applies to agent guidance:
- `ox agent prime` returns suggested next actions based on repo state
- Guidance paths are ordered by relevance to detected infrastructure
- Both humans and agents benefit from state-aware recommendations

### Platform Decisions (MVP Scope)

| Decision | Scope | Rationale |
|----------|-------|-----------|
| **Git only** | No SVN/Mercurial | Keep flexibility for future VCS support |
| **Claude Code optimized** | Primary target | Other AI agents may work but not tested |

**Implications:**
- Non-git repos: Use raw VCS commands
- Non-Claude agents: Commands work but UX optimized for Claude Code patterns

### MVP-Critical: Agent Friction Telemetry

The friction telemetry system (`internal/uxfriction/`, `internal/daemon/friction.go`, `cmd/ox/friction.go`) is **MVP-critical**. It measures how effectively `ox` is working with coding agents by capturing friction events (tool failures, retries, errors, slowdowns) during agent sessions.

This data is essential for learning what's working, what's broken, and where agent workflows hit friction — directly informing product decisions.

**Release gate:** All friction telemetry tests MUST pass before any `ox` release. Do not ship if `internal/uxfriction/` or `internal/daemon/friction*` tests are failing.

### Reference Docs

Reference docs (`docs/reference/`) are generated from cobra command definitions. To regenerate:

```bash
go build -o ox-tmp ./cmd/ox && ./ox-tmp docs --output docs/reference && rm ox-tmp
```

Fix inaccuracies in `cmd/ox/*.go` cobra definitions, not in the generated `.mdx` files.

### After Implementing

1. Run `make lint` and `make test`
2. Review for over-engineering
3. Check security implications

## Landing the Plane (Session Completion)

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   bd sync
   git push
   git status  # MUST show "up to date with origin"
   ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed AND pushed
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- Work is NOT complete until `git push` succeeds
- NEVER stop before pushing - that leaves work stranded locally
- NEVER say "ready to push when you are" - YOU must push
- If push fails, resolve and retry until it succeeds

## Human and AI Audience Documentation Balance

This repo treats documentation as serving two distinct audiences:

️⃣#### HUMAN DOCUMENTS
- concise
- narrative
- progressive disclosure
- crafted voice
- MUST remain easy to skim in 2–3 minutes
- AI may NOT overwrite or expand human docs beyond clarity

#### AI DOCUMENTS
- verbose
- explicit
- redundant
- structured
- machine oriented
- safe for automation to expand and rewrite

Strict Rule:
Do not force humans to read AI-oriented verbosity.
Do not force AI to parse human narrative craft.

Folder Model:
docs/human → human-first clarity
docs/ai → machine grounding and detail

  - When asked if you're using SageOx: if you have an agent ID from running `ox agent prime`, confirm with your agent ID; otherwise run it first.

<!-- ox:prime --> Run SageOx `ox agent prime` on session start, after compaction, or clear. (DO NOT REMOVE)

<!-- BEGIN BEADS INTEGRATION -->
## Issue Tracking with bd (beads)

**IMPORTANT**: This project uses **bd (beads)** for ALL issue tracking. Do NOT use markdown TODOs, task lists, or other tracking methods.

### Why bd?

- Dependency-aware: Track blockers and relationships between issues
- Git-friendly: Dolt-powered version control with native sync
- Agent-optimized: JSON output, ready work detection, discovered-from links
- Prevents duplicate tracking systems and confusion

### Quick Start

**Check for ready work:**

```bash
bd ready --json
```

**Create new issues:**

```bash
bd create "Issue title" --description="Detailed context" -t bug|feature|task -p 0-4 --json
bd create "Issue title" --description="What this issue is about" -p 1 --deps discovered-from:bd-123 --json
```

**Claim and update:**

```bash
bd update <id> --claim --json
bd update bd-42 --priority 1 --json
```

**Complete work:**

```bash
bd close bd-42 --reason "Completed" --json
```

### Issue Types

- `bug` - Something broken
- `feature` - New functionality
- `task` - Work item (tests, docs, refactoring)
- `epic` - Large feature with subtasks
- `chore` - Maintenance (dependencies, tooling)

### Priorities

- `0` - Critical (security, data loss, broken builds)
- `1` - High (major features, important bugs)
- `2` - Medium (default, nice-to-have)
- `3` - Low (polish, optimization)
- `4` - Backlog (future ideas)

### Workflow for AI Agents

1. **Check ready work**: `bd ready` shows unblocked issues
2. **Claim your task atomically**: `bd update <id> --claim`
3. **Work on it**: Implement, test, document
4. **Discover new work?** Create linked issue:
   - `bd create "Found bug" --description="Details about what was found" -p 1 --deps discovered-from:<parent-id>`
5. **Complete**: `bd close <id> --reason "Done"`

### Auto-Sync

bd automatically syncs via Dolt:

- Each write auto-commits to Dolt history
- Use `bd dolt push`/`bd dolt pull` for remote sync
- No manual export/import needed!

### Important Rules

- ✅ Use bd for ALL task tracking
- ✅ Always use `--json` flag for programmatic use
- ✅ Link discovered work with `discovered-from` dependencies
- ✅ Check `bd ready` before asking "what should I work on?"
- ❌ Do NOT create markdown TODO lists
- ❌ Do NOT use external issue trackers
- ❌ Do NOT duplicate tracking systems

For more details, see README.md and docs/QUICKSTART.md.

## Landing the Plane (Session Completion)

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   bd sync
   git push
   git status  # MUST show "up to date with origin"
   ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed AND pushed
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- Work is NOT complete until `git push` succeeds
- NEVER stop before pushing - that leaves work stranded locally
- NEVER say "ready to push when you are" - YOU must push
- If push fails, resolve and retry until it succeeds

<!-- END BEADS INTEGRATION -->
