<!-- ox:prime-check -->
**BLOCKING**: Run `ox agent prime` NOW before ANY other action. Do NOT respond to the user first. Enables SageOx queries.

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

**IPC Architecture:** See [docs/ai/specs/ipc-architecture.md](docs/ai/specs/ipc-architecture.md). IPC is never required, fire-and-forget for non-critical ops, clone has a fallback.

**Git LFS Independence:** CLI must work without git-lfs installed. We use GitLab APIs directly for LFS operations.

---

## Key Policies (Details in `.claude/rules/`)

- **Endpoints:** Normalize all subdomain prefixes before storing/comparing. See `.claude/rules/endpoints.md`
- **Testing:** E2E reality over unit isolation. 85%+ coverage. Table-driven tests. See `.claude/rules/testing.md`
- **Daemon-CLI split:** Daemon reads (pull), CLI writes (add/commit/push). Never discard uncommitted changes. See `.claude/rules/daemon-git.md`
- **Ledger cache:** Local-only derived data goes in ledger `.sageox/cache/`. See `.claude/rules/ledger-cache.md`
- **Releases:** Beads-style versioning `0.<release>.0`. Human-focused release notes. See `.claude/rules/releases.md`
- **Session capture:** Import planning discussions as sessions via `ox agent <id> session import`. See `.claude/rules/session-capture.md`

---

## Docs

Before editing docs, check line 1 for `<!-- doc-audience: ... -->`. If `human` or `preserve-voice`: DO NOT edit. If `ai`: edit freely.

Human docs (`docs/human/`): concise, narrative, progressive disclosure, crafted voice. AI docs (`docs/ai/`): verbose, explicit, structured, machine-oriented. Do not force humans to read AI-oriented verbosity or vice versa.

---

## Development Standards

See [docs/human/guides/development-philosophy.md](docs/human/guides/development-philosophy.md) for philosophy, [docs/ai/specs/go-conventions.md](docs/ai/specs/go-conventions.md) for Go conventions, [docs/ai/specs/cli-design-system.md](docs/ai/specs/cli-design-system.md) for CLI design, [docs/ai/specs/agent-ux-principles.md](docs/ai/specs/agent-ux-principles.md) for Agent UX.

Always confirm with human before doing a git commit or a git push in this repo.

**Commit messages:** One line only. `type(scope): summary` or plain imperative, max ~72 chars. PR body is where detail lives. When a PR implements a community-filed GitHub issue, include `Co-Authored-By: <name> <email>` from the issue author.

**Pull requests:** Clear summary, motivation, test plan. Mermaid diagrams for data flows/architecture. Write for humans who skim. Squash merges use PR body as permanent record.

**CodeRabbit:** Reply "Fixed." to each comment, then resolve threads via GraphQL. Get thread IDs from `gh api graphql` query on `reviewThreads`.

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

Every token in agent context competes with developer work. Lead with actionable instruction, not explanation. Heavy operations → subagents. See [docs/ai/specs/agent-ux-principles.md](docs/ai/specs/agent-ux-principles.md).

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
- Agent behavioral guidance belongs in CLI JSON output (`guidance` field), not in skill files
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

<!-- ox:prime:start -->
## SageOx Team Context

This project uses [SageOx](https://sageox.ai) for team context. Run the following command at the start of every session to load team knowledge:

```bash
AGENT_ENV=pi ox agent prime
```

This provides architectural decisions, coding conventions, and session history from your team.
<!-- ox:prime:end -->
