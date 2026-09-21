---
title: Team Rules
description: How to share AI coworker conventions across your whole team via SageOx team context.
audience: both
---

# Team Rules

Team rules are conventions, policies, and decisions that apply to **every AI coworker** working on your team's repos — Claude Code, Codex, Gemini, Droid, OpenCode, Amp, Cursor, Goose, and any other supported coding agent. They live in your team's SageOx team context, not in any single repo.

## Scope: who they reach

A SageOx team rule applies to:

- **All teammates who run `ox`** in a repo associated with the team. Teammates who don't use `ox` will not see it (same as `.claude/rules/` only reaching Claude users).
- **All AI coworkers those teammates use** — Claude Code, Codex, Gemini, Droid, OpenCode, Amp, etc. ox selects a native projection or `ox agent prime` fallback for each tool.

## When to use a team rule vs. a project-local rule

| Put it in | When |
|---|---|
| `.claude/rules/<topic>.md` (project) | Rule is specific to this one repo: paths, services, schemas, build steps unique to this codebase. |
| `<team-context>/agents/rules/<topic>.md` (team) | Rule applies to your team's work generally: testing philosophy, security policy, escalation rules, code review conventions, language idioms. |
| `~/.claude/rules/<topic>.md` (personal) | Rule is a personal preference that shouldn't be imposed on teammates. |

If you're editing a `.claude/rules/` file and the rule is generally applicable (not tied to this codebase's specifics), your AI coworker should ask whether to also publish it to the team. Say yes when the answer is yes; say no for repo-specific rules.

## File format

Rules live at `<team-context>/agents/rules/<topic>.md`. Subdirectories are walked recursively, so organize as your library grows:

```
agents/rules/
├── backend/
│   ├── postgres.md
│   └── api-conventions.md
├── frontend/
│   ├── react.md
│   └── styling.md
├── escalation-policy.md
└── integration-tests-no-db-mocks.md
```

Each file is markdown with YAML frontmatter:

```markdown
---
name: integration-tests-no-db-mocks
description: Integration tests must hit a real database, not mocks.
repos: ["sageox/ox", "sageox/cloud-api"]
globs: ["**/*_test.go"]
audience: ai
visibility: indexed
status: active
from-discussion: 2026-04-12-uuid7
---

**Why:** Q4 incident — mocked tests passed, prod migration broke.
**How to apply:** Spin up the test container in `internal/testdb`...
```

### `repos:` and `globs:` are different axes

`repos:` is **which repositories** get the rule. `globs:` is **which files inside them**
the rule is about. They are independent, and the combination most teams want is the one
that had no expression before: applies everywhere, but only to certain files.

| | Applies to any file | Scoped with `globs:` |
|---|---|---|
| **All team repos** (no `repos:`) | Escalation policy, review conventions | Go error-wrapping idioms (`**/*.go`), Terraform conventions (`**/*.tf`), migration rules (`migrations/**`) |
| **Some repos** (`repos:` set) | "The billing service is PCI scope" | Schema rules in the two repos that own schemas |

Before `globs:`, a Go-idioms rule had two bad options: load it in every session on every
repo including the frontend ones, or copy it into each Go repo's local rules and let the
copies drift. That second option is the problem team rules exist to solve, so the first
was the honest choice — and it spent context in every session that never touched Go.

Write globs either way; both forms parse:

The inline-list form matches the style `repos:` already uses:

```yaml
globs: ["**/*.go", "**/*.mod"]
```

The bare comma form matches what Cursor, Copilot, and Cline use, so a rule
copied out of `.cursor/rules` works unchanged:

```yaml
globs: **/*.go,**/*.mod
```

When the active tool has a faithful native glob field, ox projects the rule into its
existing rule root and the tool activates it for matching files. Claude, Cursor,
Copilot, Cline, and Kiro support that path. Droid and Windsurf have native rule roots
but no glob field, so ox keeps a scoped rule indexed through prime rather than
silently turning it into an always-on rule. Tools without a native rule root use the
same prime fallback.

### Frontmatter fields

| Field | Required | Values | Purpose |
|---|---|---|---|
| `name` | yes | kebab-case identifier | Stable handle for cross-references and `superseded-by`. |
| `description` | yes | one short line | Shown in catalogs and indexed-tier prime output. |
| `repos` | no | list of `owner/repo` slugs | Empty/absent = all team repos. Non-empty = only those repos. |
| `globs` | no | path patterns | Which FILES the rule is about. Empty/absent = any file. |
| `audience` | no | `ai` \| `human` \| `both` | Default `ai`. Filters out human-only rules from agent context. |
| `visibility` | no | `always` \| `indexed` \| `hidden` | Default `indexed`. See below. |
| `status` | no | `active` \| `draft` \| `superseded-by:<other-name>` | Default `active`. |
| `from-discussion` | no | discussion id | Optional provenance link into `<team-context>/discussions/`. |

### Visibility tiers

- **`always`** — full body is delivered every session, natively where possible and otherwise inline through `ox agent prime`. Reserve for hot, short, universally-applicable rules (security, escalation).
- **`indexed`** (default, recommended) — only `name + description + path` appears in prime, unless `globs:` enables faithful native path activation. AI coworkers read indexed rules on demand.
- **`hidden`** — not surfaced unless explicitly named. Use for drafts, archived rules, work-in-progress.

### One source, one delivery path

The Team Context file is always canonical. During `ox sync` and after Team Context
updates, ox mirrors native-compatible rules into reserved `sageox-team-*` files only
when that tool's rule root already exists and the managed path is ignored by git.
Removing a rule, changing its `repos:` filter, or superseding it removes the derived
projection. Do not edit a projection; edit the Team Context source.

At session start, prime knows which AI coworker is active. An existing native
projection is omitted from prime; everything else keeps its inline or indexed
fallback. This prevents duplicate native-plus-prime delivery. Scoped rules are never
flattened into an unscoped native format.

## Size guidance

There are no hard limits, but every `always`-tier rule loads on every session — its tokens compete with the user's actual work. Discipline:

- **`always` rules** — keep small. A paragraph or two at most. If it's substantive, it belongs in `indexed`.
- **`indexed` rules** — can be longer; they only load when the agent decides they're relevant.
- **One concern per file.** Same as `.claude/rules/`. Splitting big rules into focused files makes them easier to grep, supersede, and update.

Run `ox agent list` to see the running cumulative token cost of context delivered into AI coworker sessions, split into three buckets:

- **SageOx overhead** — what the ox tool itself injects (instructions, command lookups, attribution). SageOx is judged on this.
- **Team content** — your team's AGENTS.md, rules (`always`-tier bodies), memory, distilled discussions. You control this.
- **Project content** — the project's own AGENTS.md.

The split appears at the bottom of `ox agent list` and inside the prime XML's `<context-budget>` block. If your team's `always`-tier rules are climbing into thousands of tokens, demote some to `indexed`.

## Publishing a rule (manual workflow)

Until a dedicated `ox team rules add` command lands, the workflow is plain git:

```bash
# clone or update your team-context repo (path printed by `ox status`)
cd ~/.local/share/sageox/<endpoint>/teams/<team-id>/

# create the rule
mkdir -p agents/rules
$EDITOR agents/rules/integration-tests-no-db-mocks.md

# publish
git add agents/rules/integration-tests-no-db-mocks.md
git commit -m "Add rule: integration tests must hit real DB"
git push
```

The background Team Context sync applies the new rule automatically. Run `ox sync`
for immediate convergence. At the next session boundary, every AI coworker receives
the rule through its selected native, inline, or indexed path.

## See also

- `ox guide agents-md` — how AGENTS.md and CLAUDE.md fit in (root index files vs. modular rules)
- `ox guide team-context` — what else lives in your team context (discussions, memory, distilled docs)
- `ox guide murmur-vs-rule` — when to murmur (transient, 24h) vs. publish a durable rule
