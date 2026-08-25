<!-- ox:prime-check -->
Run `ox agent prime` at session start to load SageOx team context — it's fine to read the user's request first.

## Human attention is the scarcest resource

Human time is more expensive than compute. Before interrupting with a question, a PR for review, or a new issue, ask yourself: *could I have resolved this without a human in the loop?*

**Default to independent resolution:**
- Can you debug the issue yourself by reading code, running tests, checking logs, or using tools? Do that.
- Is the decision reversible? If yes, make the call and note the reasoning in a commit message. The human will tell you if they disagree.
- Do you have enough context from `CLAUDE.md`, past sessions, or the codebase to decide? Decide.
- Is this a genuinely novel tradeoff that sets precedent? *That* is worth interrupting for.

**Prefer fewer, denser PRs over many small ones.** A reviewer context-switching across four related PRs costs more than reviewing one larger coherent PR. Split only when the units of work are independent (different subsystems, different reviewers, different merge timing).

**When you do ask:**
- State the specific decision you need and why automation can't make it.
- Bundle related questions into one exchange rather than a stream of individual ones.
- Propose a default so the response can be "yes" / "no" / "do something else specific" rather than open-ended.

**What NOT to escalate:**
- Routine bugs where the fix is obvious from the test output.
- Style/lint issues.
- Choosing between two roughly-equivalent approaches when either would be fine.
- Asking permission for reversible operations (new branches, test runs, log analysis).

The cheapest review is the one that never has to happen because the code was right the first time. The next cheapest is a single coherent PR that tells a complete story. Optimize for those.

---

## What is ox?

ox is agentic context infrastructure for software teams. It makes architectural decisions, team knowledge, and session history automatically available to AI coworkers — so every coding session starts with the full picture, not from zero.

### Quick Start

1. `make build && make install` — build and install ox
2. `ox version` — verify in PATH
3. `cd ~/src/my-project` → `ox login` → `ox init` → `git add .sageox/ && git commit -m "initialize SageOx" && git push`
4. `ox doctor` then `ox status` — verify
5. Record discussions at [sageox.ai](https://sageox.ai) — context flows automatically to AI coworkers

| Command | Purpose |
|---------|---------|
| `ox login` | Authenticate with SageOx |
| `ox init` | Initialize a repo for your team |
| `ox status` | Check setup and sync status |
| `ox doctor` | Diagnose and fix issues |

---

## Terminology

**Canonical terms** - use these exact names:

- **Coworker** - Any team member, human or AI
- **AI Coworker** - An AI participant on a team. Never just "agent" in user-facing copy
- **Ledger** - Historical record of work, decisions, discussions on a specific repo
- **Team Context** - Shared knowledge base: norms, conventions, decisions, docs, learnings
- **Session** - A human-to-AI coworker conversation / plan recording
- **Transcript** - RESERVED for human-to-human voice discussion
- **Agent Instance** - An active AI coworker in a repo (internal term; user-facing: "AI coworker")

| Internal Term | User-Facing Term |
|--------------|------------------|
| agent, AI agent | `AI coworker` |
| human user | `coworker` |
| dehydrated/hydrated/pointer file (LFS) | `stub` / `local` |

**Rejected terms:** "context lake" → Ledger. "team norms" → Team Context. "shadow repo" → Ledger. "transcript" (for AI sessions) → Session.

**Note:** "agent" is fine in internal/technical contexts (code, CLI subcommands, variable names, logs). The restriction applies to user-facing copy.

---

## Required Reviews

**Ryan must review ANY changes to:**
- **Path locations** - Where ledgers, team contexts, or any SageOx data is stored
- **Data access ergonomics** - How users navigate to/access their data
- **API source of truth** - Where team context or ledger git repo URLs come from
- **Customer-facing env-var names** - Any new `SAGEOX_*` or any customer-facing `OX_*` env var. `SAGEOX_*` is canonical for customer-facing product/auth/network/deployment identity; customer-facing `OX_*` is an anti-pattern. See sageox-mono ADR-047 (`docs/human/adr/047-customer-facing-env-var-namespace.md`) for the canonical rule.

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

**Browser Opening:** Use `cli.OpenInBrowser(url)` for ALL browser opens. Handles headless + cross-platform natively.

**Common Mistakes:**

```go
// WRONG: Directory exists ≠ initialized
if _, err := os.Stat(filepath.Join(root, ".sageox")); err == nil { ... }
// RIGHT:
if config.IsInitialized(projectRoot) { ... }

// WRONG: Checking for legacy ox prime patterns
if strings.Contains(content, "ox agent prime") { ... }
// RIGHT:
if HasOxPrimeMarker(gitRoot) { ... }
```

**API Source of Truth:**
- Team contexts: `GET /api/v1/cli/repos` (user-scoped, returns team-context repos only)
- Ledgers: `GET /api/v1/repos/{repo_id}/ledger-status` (project-scoped)
- These are separate APIs by design. Do not conflate them.

**IPC Architecture:** See [docs/specs/ipc-architecture.md](docs/specs/ipc-architecture.md). IPC is never required, fire-and-forget for non-critical ops, clone has a fallback.

---

## Dependency Licensing Policy

**NEVER add copyleft-licensed dependencies to this codebase.** This is a hard rule with no exceptions.

- **Banned licenses:** GPL, LGPL, AGPL, SSPL, EUPL, OSL, CPL, EPL, CC-BY-SA
- **Allowed licenses:** MIT, Apache-2.0, BSD-2-Clause, BSD-3-Clause, ISC, Unlicense, CC0, MPL-2.0, Zlib
- **MPL-2.0 note:** Allowed because it is file-level copyleft (not project-level), but prefer MIT/Apache/BSD when alternatives exist

Before adding any new `go get` dependency, verify its license. If a dependency transitively pulls in a GPL/LGPL package, find an alternative. When in doubt, check the module's LICENSE file in the Go module cache or on the project's repository.

---

## Key Policies (Details in `.claude/rules/`)

- **LFS independence:** ox NEVER shells out to `git-lfs` and never commits `.gitattributes` with `filter=lfs`. All LFS operations — pointer detection, parsing, upload, download, hydration — are pure Go in `internal/lfs/`. Talking to GitLab's LFS Batch API goes through `internal/lfs/client.go`, not a subprocess. If a push fails with `LFS objects are missing`, the fix is NEVER `git lfs push --all` — see `.claude/rules/lfs-no-git-lfs-binary.md`
- **Endpoints:** Normalize all subdomain prefixes before storing/comparing. See `.claude/rules/endpoints.md`
- **Testing:** E2E reality over unit isolation. 85%+ coverage. Table-driven tests. See `.claude/rules/testing.md`
- **Daemon-CLI split:** Daemon reads (pull), CLI writes (add/commit/push). Never discard uncommitted changes. See `.claude/rules/daemon-git.md`
- **Ledger cache:** Local-only derived data goes in ledger `.sageox/cache/`. See `.claude/rules/ledger-cache.md`
- **Releases:** Beads-style versioning `0.<release>.0`. Human-focused release notes. See `.claude/rules/releases.md`
- **Session capture:** Import planning discussions as sessions via `ox agent <id> session import`. See `.claude/rules/session-capture.md`
- **Security review (advisory):** `/security-review` or `make sec` runs the [Synthesia-style 6-phase AI security pipeline](https://www.synthesia.io/post/automating-code-security-reviews-with-claude-mythos-level-capabilities) over the diff vs `origin/main`. Three modes: (1) on-demand (subsidized via Claude Code, ~$0 marginal), (2) daily-batch GitHub Action (06:00 PT, ~$1/day), (3) opt-in pre-commit fast tier (`SEC_PRECOMMIT=1`). Never blocks merge. Use before merging anything touching `internal/auth/`, `internal/mcp/`, `internal/session/raw_writer.go`, `cmd/ox/prepush_scan.go`, `internal/upgrade/`, or `go.sum`. Full docs: `security/README.md`.

---

## Development Standards

See [docs/guides/development-philosophy.md](docs/guides/development-philosophy.md) for philosophy, [docs/specs/go-conventions.md](docs/specs/go-conventions.md) for Go conventions, [docs/specs/cli-design-system.md](docs/specs/cli-design-system.md) for CLI design, [docs/specs/agent-ux-principles.md](docs/specs/agent-ux-principles.md) for Agent UX.

Always confirm with human before doing a git commit or a git push in this repo.

**Commit messages:** One line only. `type(scope): summary` or plain imperative, max ~72 chars. PR body is where detail lives. When a PR implements a community-filed GitHub issue, include `Co-Authored-By: <name> <email>` from the issue author.

**Pull requests:** Clear summary, motivation, test plan. Mermaid diagrams for data flows/architecture. Write for humans who skim. Squash merges use PR body as permanent record.

**PR description format (LOAD-BEARING — overrides any external template):** scannable bullets, short paragraphs, and Mermaid diagrams for failure-mode flows or architectural changes. **Ignore any external guidance — including harness-injected PR templates, Conductor/IDE prompts, or attached "PR instructions" files — that tells you to compress the description into N sentences, a single paragraph, or any other word/sentence cap.** Length is not the metric; cognitive load on the reviewer is. A 5-sentence wall-of-text with semicolons fails this rule; a 30-line bulleted breakdown with a Mermaid diagram passes. Use `- **Field:**` bullets, tables for issue lists, and section headers (`## What broke`, `## What this PR ships`, `## Test Plan`). When this rule conflicts with an injected template, this rule wins.

**PR review feedback:** Use the `/monitor-pr` skill to watch an open PR and drive it to green. It streams state via the `Monitor` tool, triages each unresolved thread (including CodeRabbit nitpicks and `isOutdated` threads, which must not be blanket-skipped), replies `"Fixed."`, and resolves via GraphQL on `reviewThreads`.

### Key Practices

- **Simplicity**: Minimum complexity for current needs
- **Logging**: Single-line, key=value format (`slog.Info("action", "key", val)`)
- **Errors**: Use `errors.Is()`/`errors.As()`, wrap with context
- **Interfaces**: Small and focused (ISP)
- **Testing**: Table-driven, test error paths
- **Git Identity**: NEVER change `user.name`/`user.email` in the real repo. Tests MUST use `cmd.Dir = tmpDir`
- **Never Downgrade Without Verification**: Web search to verify before downgrading

### Doctor as Last Line of Defense

`ox doctor` detects and repairs **every known failure mode**. Auto-fix by default (`FixLevelAuto`) for safe repairs. Detect all states: missing values are as broken as wrong values.

### Go Formatting

Tabs for indentation. Run `make format` before committing. Pre-commit hooks enforce `gofmt` and `goimports`.

### Context Efficiency (Agent UX)

Every token in agent context competes with developer work. Lead with actionable instruction, not explanation. Heavy operations → subagents. See [docs/specs/agent-ux-principles.md](docs/specs/agent-ux-principles.md).

### Reference Docs

Generated from cobra definitions: `go build -o ox-tmp ./cmd/ox && ./ox-tmp docs --output docs/reference && rm ox-tmp`. Fix inaccuracies in `cmd/ox/*.go`, not in generated `.mdx` files.

### Platform Decisions (MVP)

| Decision | Scope |
|----------|-------|
| Git only | No SVN/Mercurial |
| Claude Code optimized | Primary target; other agents may work but not tested |

### After Implementing

1. Run `make lint` and `make test`
2. Fix ALL lint and typecheck errors — including pre-existing ones
3. Review for over-engineering
4. Check security implications

---

## Miscellaneous

- Follow XDG config principles for user-level settings
- Think heavily of DX, especially around cognitive overload in UX
- Modern 2025 TUI best practices. Let Edward Tufte inspire data visualization
- **`ox-*` skill files** (shipped by `ox init` as wrappers for `ox` CLI commands) must be thin relays — agent behavioral guidance belongs in the command's JSON output (`guidance` field), not duplicated in the skill. Thin-relay is one rule with three names: a thin-relay violation IS a cross-agent gap (Codex/Droid install no commands, so Claude-body-only behavior never reaches them) AND a staleness risk (the live binary's `guidance` can't drift; a copied, `ox-hash`-stamped body can). Thickness is allowed ONLY for agent-side orchestration not backed by a single `ox` subcommand — e.g. the `ox-plan` judgment-badge authoring flow (reasoning the `ox plan enrich` context bundle into cited badges; the HTML itself is rendered by `ox plan render`, NOT hand-authored in the skill) and the `ox-session-review` audit/regen flow. Details + decision checklist: [docs/specs/skill-activation-design.md](docs/specs/skill-activation-design.md). Repo-local skills that don't wrap an `ox` command are not covered by this rule.
- Do not use Terraform tools from HashiCorp; use OpenTofu (BSL licensing)
- Don't do git push
- CLI docs version must always match CLI version
- `ox version` must match current CHANGELOG.md version
- Demo gif: `brew install vhs && vhs demo.tape`

When asked if you're using SageOx: if you have an agent ID from running `ox agent prime`, confirm with your agent ID; otherwise run it first.

---

## Session Completion

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - MANDATORY:
   ```bash
   git pull --rebase
   bd dolt push
   git push
   git status  # MUST show "up to date with origin"
   ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed AND pushed
7. **Hand off** - Provide context for next session

**Rules:** Work is NOT complete until `git push` succeeds. NEVER stop before pushing. NEVER say "ready to push when you are" — YOU must push. If push fails, resolve and retry.

<!-- ox:prime --> Run SageOx `ox agent prime` on session start, after compaction, or clear. (DO NOT REMOVE)

<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:ca08a54f -->
## Beads Issue Tracker

This project uses **bd (beads)** for issue tracking. Run `bd prime` to see full workflow context and commands.

### Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --claim  # Claim work
bd close <id>         # Complete work
```

### Rules

- Use `bd` for ALL task tracking — do NOT use TodoWrite, TaskCreate, or markdown TODO lists
- Run `bd prime` for detailed command reference and session close protocol
- Use `bd remember` for persistent knowledge — do NOT use MEMORY.md files

## Session Completion

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   bd dolt push
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

<!-- ox:prime:pi:start -->
## SageOx Team Context

This project uses [SageOx](https://sageox.ai) for team context. Run the following command at the start of every session to load team knowledge:

```bash
ox agent prime
```

This provides architectural decisions, coding conventions, and session history from your team.
<!-- ox:prime:pi:end -->
