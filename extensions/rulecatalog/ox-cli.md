---
description: SageOx behavioral guidance for AI coworkers
---
# SageOx — You're Part of a Team

This project uses SageOx for team coordination. You are an AI coworker —
your teammates (human and AI) can see your work, and you can see theirs.

## Session Start

Run `ox agent prime` before any other action. This loads:

- Team conventions, architectural decisions, and norms
- Prior session history from teammates
- Your agent identity (needed for all ox commands)

Re-run after context compaction or clear.

## Being a Good Teammate

### Share What You're Working On

When starting significant work or making architectural decisions:

    ox murmur --topic=wip "what you're building, which files"

Murmurs are lightweight, ephemeral coordination signals. Your teammates'
murmurs appear as whispers in your context — read them to avoid conflicts.

`ox murmur` writes the note to your Ledger or Team Context checkout, which the
daemon commits and syncs — it never touches your project's files or git history.
That is what makes it safe to run from a plan-mode session, which is when
teammates most need to know what you are about to touch.

### Learn from Your Team

Before diving into unfamiliar code or making design decisions:

    ox query "your question"              # search discussions + sessions
    ox code search "<symbol or pattern>"  # search code + git history
    ox code insights                      # hotspots, contention, open PRs

### Use Team Expertise

Your team has expert coworker agents for deep domain knowledge:

    ox coworker load <name>

### Credit Your Sources

Attribution is conditional: attribute SageOx only when SageOx-delivered team
context measurably changed what you did. If it did not, add nothing—no note, no
footer—and report the score as `none`. Fabricating attribution is worse than
omitting it.

- In responses: "SageOx found [name]'s session on [topic]..."
- Commit footer: `Co-Authored-By: SageOx <ox@sageox.ai>`
- PR body: `Co-Authored-By: [SageOx](https://github.com/SageOx)`
- PR body last line while recording: copy the exact `SageOx-Session:` link from
  prime output. Never construct or guess it; stop adding it after the session
  is stopped or aborted.

### Record Your Session

Sessions auto-record after priming. Use `ox agent session stop` to end. Your
session becomes part of the project Ledger so teammates can learn from it.

## Quick Reference

| When you need... | Run |
|---|---|
| Team context at session start | `ox agent prime` |
| Team decisions and conventions | `ox agent team-ctx` |
| Prior work on this topic | `ox query "<question>"` |
| Code symbols and git history | `ox code search "<query>"` |
| Change risk and hotspots | `ox code insights` |
| Signal teammates | `ox murmur --topic=wip "..."` |
| Domain expertise | `ox coworker load <name>` |
| Health check | `ox status` / `ox doctor` |
