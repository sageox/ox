---
title: Team Context
description: What lives in your team context, where it's stored, and how it syncs.
audience: both
---

# Team Context

Team context is the shared knowledge base for your team — meetings, decisions, conventions, rules, distilled summaries. It's separate from any single repo's history and reaches every teammate's AI coworkers via `ox agent prime`.

## Location on disk

Each team context is a separate git repository. After `ox login` and `ox init`, the daemon clones it to:

```text
~/.local/share/sageox/<endpoint>/teams/<team-id>/
```

`<endpoint>` is your normalized SageOx endpoint (e.g., `sageox.ai`). `<team-id>` is the stable identifier for the team.

You don't usually interact with this directory directly — `ox` commands manage it. But it is a real git repo, so you can `cd` in and inspect or edit when needed.

## Layout

```text
<team-context>/
  AGENTS.md                ← team-wide instruction file (cross-tool standard)
  CLAUDE.md                ← typically symlinked to AGENTS.md
  MEMORY.md                ← team memory entries (inlined into prime)
  SOUL.md                  ← team identity / values (referenced, not inlined)
  TEAM.md                  ← team handbook (referenced)
  agents/
    rules/                 ← modular team rules (one file per concern)
    commands/              ← team slash commands
    profiles/              ← AI coworker profiles
  coworkers/               ← legacy location for agents/, commands/ (still read for backward compat)
  discussions/             ← archived team meetings, transcripts, keyframes
  memory/                  ← daily / weekly / monthly distilled summaries
  documents/               ← imported docs (via `ox import`)
  agent-context/
    distilled-discussions.md  ← AI-focused synthesis of recent discussions
  data/
    murmurs/               ← transient WIP coordination signals (24h TTL)
```

## What gets loaded into AI coworker context

When a teammate runs `ox agent prime`, the following is delivered into their AI coworker's session:

| Source | How it appears |
|---|---|
| `AGENTS.md` and `CLAUDE.md` (root) | Inlined into prime as `<team-instructions>` |
| `agents/rules/**/*.md` with `visibility: always` | Body inlined into `<team-rules>` |
| `agents/rules/**/*.md` with `visibility: indexed` | Catalog entry only (name + description + path); agent reads on demand |
| `MEMORY.md` | Inlined into `<memory>` |
| `agent-context/distilled-discussions.md` | Available via `ox agent team-ctx` |
| `documents/*` | Available via search; not inlined |
| `discussions/*` | Searchable; not inlined |

Rules that include a `repos:` filter only load when the teammate is working in a matching repo.

## Sync

The ox daemon pulls and pushes the team-context git repo periodically. `ox status` shows last-sync time. `ox doctor` repairs sync issues. You don't need to push manually — the daemon handles it after `ox` commands write to team context.

When you edit team context files directly (e.g., dropping a new rule into `agents/rules/`), commit and push from inside the team-context directory. The daemon will pick it up on its next sync.

## Adding to team context

| Want to add | Use |
|---|---|
| A team-wide rule for AI coworkers | Drop a file in `agents/rules/`, commit, push. See `ox guide team-rules`. |
| An onboarding doc, architecture write-up, or video | `ox import <file-or-url>` |
| A short transient coordination signal (e.g., "rebuilding migrations now") | `ox murmur --scope=team --topic=<topic> "<message>"` (lasts 24h) |

## See also

- `ox guide team-rules` — file format and publishing workflow for modular rules
- `ox guide agents-md` — how the root AGENTS.md and CLAUDE.md fit in
- `ox guide murmur-vs-rule` — when to murmur (transient) vs. publish a durable rule
