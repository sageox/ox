---
title: Getting Started
description: Five-minute walkthrough to set up SageOx and reach your team's AI coworker context.
audience: both
---

# Getting Started

SageOx makes your team's architectural decisions, conventions, and prior session history automatically available to every AI coworker on the team — Claude, Codex, Amp, and any other supported coding agent. Every coding session starts with the full picture, not from zero.

## Setup (one-time)

```bash
ox login                      # authenticate with SageOx
cd ~/code/your-repo
ox init                       # initialize this repo for your team
git add .sageox/ AGENTS.md CLAUDE.md
git commit -m "Initialize SageOx"
git push
```

`ox init` will:

- Create `.sageox/` with config, README, and `.gitignore`
- Inject `ox agent prime` markers into `AGENTS.md` / `CLAUDE.md`
- Install agent hooks and slash commands
- Associate the repo with your team (prompts if you have multiple teams)

Verify with `ox doctor` and `ox status`.

## Daily use

Once initialized, you usually don't need to think about ox — it runs in the background. The pieces that surface during normal work:

| What | When | Command |
|---|---|---|
| **Bootstrap context** | Start of every coding session, after compaction, after `/clear` | `ox agent prime` (auto-triggered by hooks) |
| **Read team knowledge** | Looking for prior decisions, meetings, conventions | `ox agent team-ctx` or `ox query "<question>"` |
| **Coordinate WIP** | Want teammates' AI coworkers to know what you're touching | `ox murmur --topic=wip "what you're building"` |
| **Find prior sessions** | Has someone solved this problem before in this repo? | `ox session list` then `ox session view <name> --text` |
| **Health check** | Something feels off | `ox doctor` |

## Sharing knowledge with the team

You'll naturally discover conventions and rules as you work. There are three places they can live:

| Scope | File | Reaches |
|---|---|---|
| Personal | `~/.claude/rules/<topic>.md` | Just you, just Claude |
| Project | `<repo>/.claude/rules/<topic>.md` | This repo only, just Claude |
| **Team** | `<team-context>/agents/rules/<topic>.md` | **All teammates running ox, all supported agents** |

When you add a rule that applies generally (not just this repo's specifics), your AI coworker should ask whether to also publish it to the team. Say yes for general rules; no for repo-specific ones.

For the file format and publishing workflow, run `ox guide team-rules`.

## What to read next

| Topic | Command |
|---|---|
| File format and publishing for team rules | `ox guide team-rules` |
| How AGENTS.md / CLAUDE.md fit in | `ox guide agents-md` |
| Full team context layout | `ox guide team-context` |
| When to murmur vs. publish a rule | `ox guide murmur-vs-rule` |

Or run `ox guide` (no arguments) to see all bundled topics.

## Help

- **In-CLI:** `ox <command> --help` for any command
- **Docs site:** https://sageox.ai/docs
- **Issues:** https://github.com/sageox/ox/issues
