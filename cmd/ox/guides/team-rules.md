---
title: Team Rules
description: How to share AI coworker conventions across your whole team via SageOx team context.
audience: both
---

# Team Rules

Team rules are conventions, policies, and decisions that apply to **every AI coworker** working on your team's repos — Claude, Codex, Amp, Cursor, and any other supported coding agent. They live in your team's SageOx team context, not in any single repo.

## Scope: who they reach

A SageOx team rule applies to:

- **All teammates who run `ox`** in a repo associated with the team. Teammates who don't use `ox` will not see it (same as `.claude/rules/` only reaching Claude users).
- **All coding agents those teammates use** — Claude, Codex, Amp, etc. SageOx is agent-agnostic; the rule loads via `ox agent prime` regardless of which coding tool is connected.

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
audience: ai
visibility: indexed
status: active
from-discussion: 2026-04-12-uuid7
---

**Why:** Q4 incident — mocked tests passed, prod migration broke.
**How to apply:** Spin up the test container in `internal/testdb`...
```

### Frontmatter fields

| Field | Required | Values | Purpose |
|---|---|---|---|
| `name` | yes | kebab-case identifier | Stable handle for cross-references and `superseded-by`. |
| `description` | yes | one short line | Shown in catalogs and indexed-tier prime output. |
| `repos` | no | list of `owner/repo` slugs | Empty/absent = all team repos. Non-empty = only those repos. |
| `audience` | no | `ai` \| `human` \| `both` | Default `ai`. Filters out human-only rules from agent context. |
| `visibility` | no | `always` \| `indexed` \| `hidden` | Default `indexed`. See below. |
| `status` | no | `active` \| `draft` \| `superseded-by:<other-name>` | Default `active`. |
| `from-discussion` | no | discussion id | Optional provenance link into `<team-context>/discussions/`. |

### Visibility tiers

- **`always`** — full body inlined into `ox agent prime` every session. Reserve for hot, short, universally-applicable rules (security, escalation). This costs context tokens for every teammate on every session.
- **`indexed`** (default, recommended) — only `name + description + path` appears in prime. Agents read the file on demand when relevant. Keeps prime context small as your library grows.
- **`hidden`** — not surfaced unless explicitly named. Use for drafts, archived rules, work-in-progress.

> **Why no path-scoping (`paths:` like Claude has):** Claude rule files support a `paths:` glob list that defers loading until Claude reads a matching file. SageOx's prime runs once at session start, before file access happens — we can't replicate Claude's per-file lazy loading. The closest scoping we offer is `repos:`, plus `visibility: indexed` for on-demand reads.

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

On their next `ox agent prime`, every teammate's AI coworker will see the new rule (full body if `always`, catalog entry if `indexed`).

## See also

- `ox guide agents-md` — how AGENTS.md and CLAUDE.md fit in (root index files vs. modular rules)
- `ox guide team-context` — what else lives in your team context (discussions, memory, distilled docs)
- `ox guide murmur-vs-rule` — when to murmur (transient, 24h) vs. publish a durable rule
