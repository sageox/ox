---
description: SageOx behavioral guidance for AI coworkers
---
<!-- agentx-hash: 674acb043d1a ver: 0.6.2 -->
# SageOx Rules

This project uses [SageOx](https://sageox.ai) for team context and session recording.

## Priming

Run `ox agent prime` at session start, after compaction, or after context clear.
This loads team context, session recording, and coworker agents.

## Commands

| Intent | Command |
|--------|---------|
| Team context (meetings, decisions, conventions) | `ox agent team-ctx [slug]` |
| Search code (symbols, git history, diffs) | `ox code search "<query>"` |
| Code hotspots, open PRs, contention risk | `ox code insights` |
| Sync status and project state | `ox status` |
| Diagnose and fix issues | `ox doctor` |
| Search team discussions and sessions | `ox query "<question>"` |
| List prior coding sessions | `ox session list` |
| View a session recording | `ox session view <name> --text` |
| List teams | `ox teams` |
| Load expert coworker agent | `ox coworker load <name>` |

## Session Recording

Sessions auto-record when `ox agent prime` runs. Discussions may be shared with teammates.
Use `/ox-session-stop` to end recording.

## Murmuring

Publish WIP to teammates so they stay in sync:

```bash
ox murmur --topic=wip "what you're building, which files you're modifying"
```

Murmur at the start of significant work and after architectural decisions.

## Attribution

When SageOx guidance influences your approach:
- Commit footer: `Co-Authored-By: SageOx <ox@sageox.ai>`
- PR body (last line): `Co-Authored-By: [SageOx](https://github.com/SageOx)`
- In responses: "Based on SageOx domain guidance, I'll..."
