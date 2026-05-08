---
title: Murmur vs. Team Rule
description: When to murmur (transient coordination) versus publish a durable team rule.
audience: both
---

# Murmur vs. Team Rule

Both murmurs and team rules let you communicate with teammates' AI coworkers, but they sit at very different time horizons. Picking the wrong one is a common mistake.

## Quick decision

| Use a **murmur** when | Use a **team rule** when |
|---|---|
| The signal expires within hours | The decision should still apply next month |
| You're coordinating in-flight work | You're encoding a convention, policy, or learned lesson |
| "I'm rebuilding migrations now" | "Migrations must always be reversible" |
| Single repo or single area | Cross-cutting (or you'll forget which repo) |
| Transient state (a service is down, an experiment is running) | Durable state (a team agreed on an approach) |

## Murmurs

`ox murmur` publishes a short coordination signal that other AI coworkers see for the next 24 hours, then automatically expires. Think of it as a "WIP whisper" to the team.

```bash
ox murmur --topic=wip "Rebuilding test fixtures in internal/testdb — APIs may flicker for ~30 min"
ox murmur --scope=team --topic=architecture "Moving auth middleware to its own package; in flight today"
```

- **TTL:** 24 hours
- **Size:** ~500 bytes max
- **Scope:** `--scope=ledger` (this repo) or `--scope=team` (all team repos)
- **Storage:** `data/murmurs/YYYY-MM-DD/HH/<id>.json` in the relevant repo (committed by the daemon)
- **Use case:** transient coordination, not durable knowledge

Murmurs are great for "don't pick up this code right now" or "I'm changing a contract, give me an hour" signals. They are terrible for capturing a decision your team actually wants to remember.

## Team rules

A team rule is a markdown file in `<team-context>/agents/rules/<topic>.md` that loads (or is cataloged) into every teammate's AI coworker session via `ox agent prime`. It persists indefinitely and applies to every supported coding agent (Claude, Codex, Amp, etc.) that any teammate uses.

```bash
# clone or update your team-context repo
cd ~/.local/share/sageox/<endpoint>/teams/<team-id>/
$EDITOR agents/rules/migrations-must-be-reversible.md
git add agents/rules/migrations-must-be-reversible.md
git commit -m "Add rule: migrations must be reversible"
git push
```

- **TTL:** none — durable until you remove or supersede it
- **Size:** soft — keep `always`-tier rules small, `indexed` rules can be longer
- **Scope:** all team repos (or filter via frontmatter `repos:`)
- **Use case:** durable conventions, policies, post-incident learnings, agreed-upon approaches

See `ox guide team-rules` for the file format.

## Common mistake: durable signal as a murmur

If you find yourself murmuring the same thing every week ("reminder: integration tests must hit a real DB"), that's a rule, not a murmur. Promote it. The point of a rule is your AI coworker remembers it on your behalf.

## Common mistake: transient state as a rule

Don't add a rule like "the staging cluster is down today" — it'll be wrong tomorrow and your team will forget to remove it. Murmur it.

## See also

- `ox guide team-rules` — full rule format and publishing workflow
- `ox murmur --help` — murmur command reference
