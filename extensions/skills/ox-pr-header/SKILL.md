---
name: ox-pr-header
description: >-
  Emit and paste the SageOx credit line at the TOP of a pull-request description
  — the human-facing counterpart to the machine `SageOx-Session:` trailer. Run
  `ox pr header` at PR-creation time; it renders a thin, on-brand, dark/light-aware
  line linking the session(s) and plan(s) that produced the change, naming the
  team, and whispering a subtle enrichment stat. The CLI owns the sanitizer-fragile
  markup — you never hand-author it. Use when opening a PR for AI coworker work, or
  when the user says "add the SageOx header", "credit the session on the PR", or
  "/ox-pr-header".
---

**You do not hand-write this markup — `ox pr header` does.** A GitHub PR body is
sanitized harder than repo markdown (no CSS, no styled tables); the line is built
from the exact primitives that survive (`<picture>` wordmark, `<a>` links, `<sub>`
caption). Hand-authoring it is how it silently renders as a bordered table or a
vanished wordmark. Run the command and paste its output verbatim.

## When

At PR-creation time, **only when SageOx-delivered team context measurably shaped the
work** (your contribution score is not `none`). If SageOx did not shape it, omit both
the header and the `SageOx-Session:` trailer — publishing SageOx credit for work it
did not influence is the fabrication #809 guards against. When it did shape the work,
the header is the **top** of the body and the `SageOx-Session:` trailer stays the
**last** line (they serve different readers: the header a human, the trailer the
reconciler).

## How

1. Gather what the PR credits (you already hold this from your session):
   - the plan ids you saved (`pln_…`), if any;
   - the enrichment counts from `ox plan enrich` (related sessions / concurrent
     edits), if any.
2. Run the command — the current session is auto-linked; add plans + enrichment:

   ```bash
   ox pr header \
     --plan pln_4d8e2f --plan pln_1a6b9c \
     --prior-art 2 --collisions 1
   ```

   `--session <url|ses_id>` adds/overrides sessions (default: the live session).
   `--no-stat` drops the whisper. `--style text|image|auto` follows `ox config`
   (`pr_visuals.style`) by default. `--json` returns the markup plus resolved
   inputs for verification.

3. Paste the output as the FIRST lines of the PR body, **above** your summary.
   Write the body via a **file**, never a heredoc — a heredoc mangles the markup:

   ```bash
   ox pr header > body.md
   printf '\n' >> body.md
   cat description.md >> body.md   # your written summary
   # ...keep the SageOx-Session: trailer as the last line...
   gh pr create --body-file body.md
   ```

## Rules

- **Paste verbatim.** Do not edit the emitted markup, retitle the links, or add
  the plan/session titles — the `/c/` and `/plan/` URLs are deliberately opaque
  so nothing about the work leaks into a public PR body.
- **Only link artifacts you know resolve.** ox verifies just the auto-linked
  current session (from local recording state) and withholds it until it is
  server-visible. Explicit `--session`/`--plan` ids you pass are included **as
  given** — ox cannot check an arbitrary id, so pass only ids you know exist or a
  reviewer may hit a 404; the command prints a stderr note when you do. Pass
  `--allow-unconfirmed` to accept possibly-unresolved links (the pending current
  session and explicit refs) and silence the note.
- **Honest enrichment only.** Pass the real `ox plan enrich` counts. The stats
  render only when a signal actually fired AND a plan is linked (so a reviewer can
  verify them) — never inflate them.
- **Respect config.** If `pr_visuals.header` is off (team/user opt-out) the command
  no-ops with a hint; don't force a header in that case.
- **Keep the trailer (only when attribution is non-`none`).** When SageOx shaped the
  work, the header does not replace `SageOx-Session:` — that line stays at the bottom
  as the machine linkage. If the work is unscored or scored `none`, emit neither the
  header nor the trailer.
