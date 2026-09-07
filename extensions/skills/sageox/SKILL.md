---
name: sageox
description: "What SageOx is and how to use it in this repository: load team context at session start, record the session, and reach the team's shared knowledge. Read this when you see a .sageox/ directory, when someone mentions SageOx or the ox CLI, or when ox-cli-* skills are referenced but missing."
---

# SageOx

This repository uses **SageOx**: shared context infrastructure for teams whose
coworkers are both human and AI. Architectural decisions, team conventions, and
the history of prior sessions are available to you here — you are not starting
from zero.

This is the one SageOx file committed to the repository. It is deliberately
small and stable, because it is the only thing that is guaranteed to be here on
a machine where the `ox` CLI is **not** installed.

## Start every session with prime

```bash
ox agent prime
```

Run it before anything else, and again after a context compaction or clear. It
loads team conventions, architectural decisions, prior sessions from teammates,
and your agent identity. Most other `ox` commands need that identity.

## The rest of the SageOx skills are installed locally, not committed

You may see references to skills named `ox-cli-*` — `ox-cli-plan`,
`ox-cli-consult`, `ox-cli-recap`, and others. Those are installed on **this
machine** by the `ox` CLI and are deliberately **not** committed to the
repository, so upgrading ox never puts vendor files into anyone's pull request.

If they are referenced but absent:

```bash
ox doctor --fix
```

That materializes the current set. If `ox` itself is missing, install it first —
everything below degrades gracefully without it, and this file is still the map.

## Reaching the team's knowledge

| When you need | Run |
|---|---|
| Team context at session start | `ox agent prime` |
| Prior work on a topic | `ox query "<question>"` |
| Code symbols and git history | `ox code search "<query>"` |
| Team decisions and conventions | `ox agent team-ctx` |
| Health check | `ox status` / `ox doctor` |

## Being a good teammate

Your work is visible to the team, and theirs to you. When you start something
substantial, say so — `ox murmur --topic=wip "<what you're building>"` — and read
the murmurs that reach you, so two coworkers don't rebuild the same thing twice.

When SageOx-delivered context measurably changed what you did, credit it. When it
did not, add nothing: invented attribution is worse than none.

## Beyond the CLI

SageOx is the product; `ox` is one surface of it. The same team context is
reachable through the SageOx MCP server and the web app, so a coworker who never
installs the CLI still gets the shared picture. That is why this skill is named
for the product rather than the binary.
