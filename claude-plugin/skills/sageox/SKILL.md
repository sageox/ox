---
name: sageox
description: "What SageOx is and how to use it from Claude Code: load team context at session start, search prior sessions and decisions, and record the session to the team ledger. Use when a repository has a .sageox/ directory, when the user mentions SageOx or the ox CLI, or when you need team conventions or prior decisions."
---

# SageOx

SageOx gives a team's human and AI coworkers one shared memory: conventions,
architectural decisions, and the history of prior sessions. A new session starts
with that picture, not from zero.

## Start every session with prime

```bash
ox agent prime
```

This plugin runs it for you at session start and before a context compaction. It
loads team conventions, decisions, prior sessions from teammates, and your agent
identity. Run it again by hand if the hook did not fire.

## Reach the team's knowledge

| When you need | Run |
|---|---|
| Team context at session start | `ox agent prime` |
| Prior work on a topic | `ox query "<question>"` |
| Code symbols and git history | `ox code search "<query>"` |
| Team decisions and conventions | `ox agent team-ctx` |
| Health check | `ox status` / `ox doctor` |

## Two ways in

- **`ox` CLI** — install with `brew install sageox/tap/ox` or see
  https://sageox.ai/install. Needed for the session hooks and the slash commands
  in this plugin.
- **SageOx MCP server** — this plugin registers it. It serves the same team
  context to Claude Code without the CLI. Sign in with SageOx when Claude Code
  prompts you.

If `ox` is missing, the session hook prints install instructions and does not
block your session. See the `ox` skill for the full command list.
