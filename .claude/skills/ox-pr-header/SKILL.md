---
name: ox-pr-header
description: >-
  Emit and paste the SageOx credit line at the TOP of a pull-request description
  — the human-facing counterpart to the machine `SageOx-Session:` trailer. Run
  `ox pr header` at PR-creation time; it renders a thin, on-brand, dark/light-aware
  single line linking the session(s), plan(s), and discussion(s) that produced the
  change and naming the team. The CLI owns the sanitizer-fragile markup — you never
  hand-author it. Use when opening a PR for AI coworker work, or when the user says
  "add the SageOx header", "credit the session on the PR", or "/ox-pr-header".
---

**Run the command and follow the `guidance` field it returns.** This skill is a
thin relay: the behavioral rules ship in `ox pr header --json`, so every AI
coworker gets them from the live binary — not just the ones that install skills.

```bash
ox pr header --json
```

Add what the PR credits (the current session is auto-linked):

- `--plan pln_…` — a plan you saved this session. Repeatable.
- `--discussion cnv_…` — a recorded discussion the PR came **directly** out of.
  Rare, and never auto-discovered: only you can judge direct relevance. Ids come
  from `ox conversation list`.
- `--session <url|ses_id>` — adds or overrides sessions.
- `--allow-unconfirmed` — accept links that may not resolve yet.

Then write the body via a **file, never a heredoc** — a heredoc mangles the markup:

```bash
ox pr header > body.md
printf '\n' >> body.md
cat description.md >> body.md   # your written summary
# ...keep the SageOx-Session: trailer as the last line...
gh pr create --body-file body.md
```

## When nothing is emitted

The header renders **only** when it can link at least one artifact a reviewer can
open. With no session, plan, or discussion, `ox pr header` prints nothing on
stdout and says why on stderr — a team name alone is not a credit. That is the
correct outcome, not an error: paste nothing and move on.

It also no-ops when `pr_visuals.header` is off (a team or user opt-out). Don't
force a header in that case.
