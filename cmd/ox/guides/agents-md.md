---
title: AGENTS.md and CLAUDE.md
description: How AGENTS.md, CLAUDE.md, and modular rule files fit together in projects and team context.
audience: both
---

# AGENTS.md and CLAUDE.md

AI coding agents read instruction files at session start to learn project conventions. Different tools historically named these files differently — `CLAUDE.md` (Claude Code), `.cursorrules` (Cursor), `.windsurfrules` (Windsurf), `GEMINI.md` (Gemini CLI), `CONVENTIONS.md` (Aider). The cross-tool convention has converged on **AGENTS.md at the repo root**, which Codex, OpenCode, Amp, Pi, Goose, and the OpenAI Agents SDK all read.

SageOx supports both `AGENTS.md` and `CLAUDE.md` and follows the convention of treating them as siblings — typically `CLAUDE.md` is a symlink to `AGENTS.md` so both work without duplication.

## Two scopes

| Scope | File | Reaches |
|---|---|---|
| **Project** | `<repo>/AGENTS.md` (or `CLAUDE.md`) | Anyone working in this repo, regardless of team |
| **Team** | `<team-context>/AGENTS.md` (or `CLAUDE.md`) | Every teammate running `ox` in any repo associated with the team |

Project files travel with the repo (committed to git). Team files live in your team context repo and reach all teammates whose `ox agent prime` is configured for the team.

## What `ox init` does

When you run `ox init` in a repo, ox injects two markers into `AGENTS.md` and/or `CLAUDE.md`:

```text
<!-- ox:prime-check -->
**BLOCKING**: Run `ox agent prime` NOW before ANY other action. Do NOT respond to the user first. Enables SageOx queries.
```

…at the top, and:

```text
<!-- ox:prime --> Run SageOx `ox agent prime` on session start, after compaction, or clear. (DO NOT REMOVE)
```

…at the bottom. These tell agents to bootstrap SageOx context before doing anything else. Everything between the markers is your own — ox does not touch your team's voice.

## Modular rules sit alongside AGENTS.md

The Claude Code pattern of `.claude/rules/<topic>.md` (one rule per file) is much easier to maintain than a monolithic AGENTS.md. SageOx mirrors this for team scope at `<team-context>/agents/rules/<topic>.md`. AGENTS.md becomes a lightweight index/preamble; the substance lives in modular rule files.

```text
<team-context>/
  AGENTS.md               ← preamble + TOC pointing to agents/rules/
  CLAUDE.md               ← typically symlinked to AGENTS.md
  agents/
    rules/                ← one rule per file (recursive subdirs OK)
    commands/             ← team slash commands
    profiles/             ← coworker profiles
  discussions/
  memory/
```

For the rule file format, see `ox guide team-rules`.

## Voice: hand-written, not generated

ox does **not** regenerate AGENTS.md. Teams own their voice. AGENTS.md should be short — a preamble (terminology, escalation, tone) and a pointer to where rules live (`agents/rules/`). Keep substantive content in the modular rule files where it can be greppable, supersedable, and independently maintained.

## See also

- `ox guide team-rules` — modular rule file format and publishing workflow
- `ox guide team-context` — full team context layout and what each directory holds
