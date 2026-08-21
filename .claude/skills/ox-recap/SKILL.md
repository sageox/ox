---
name: ox-recap
description: >-
  Answer "what value am I getting from SageOx?" with receipts, not vibes.
  Auto-fire when the user asks "is SageOx worth it", "what has SageOx done
  for me", "what am I getting out of SageOx", "should I keep using this",
  "was SageOx worth it this week", or says "ox recap" / "give me a recap".
  Run `ox recap --json` and narrate a tight, personal, prose answer from the
  `guidance` field — never invent value, never cite a bare statistic or a
  time-saved/dollar figure, ground every claim in a receipt from the bundle.
---

<!-- Thin by design. The entire narration contract — which value axis to lead
     with (social: team knowledge that reached you; temporal/solo: your own
     ledger compounding as searchable memory), the honesty rules (never
     invent value, never lead with a bare statistic, ground every claim in a
     receipt), and the cold-start framing — lives in the `guidance` field of
     `ox recap --json` (Layer-1 floor), which reaches every primed AI
     coworker. This native skill adds discovery and activation
     ergonomics; it duplicates no reasoning. Do not grow this body. -->

## Use when

The user asks about SageOx's value, ROI, or whether it's worth keeping:
"what value am I getting from SageOx", "is SageOx worth it", "what has
SageOx done for me", "what's this actually doing for me", or "ox recap".

## Do

1. Run `ox recap --json` (add `--since <window>` or `--user <name>` only if
   the user asked for a scope other than the default last 30 days).
2. Narrate the answer by following the `guidance` string in the JSON output
   verbatim — it decides which value axis leads, enforces the honesty rules,
   and tells you how to ground every claim in a receipt (an artifact path, a
   session name, a plan slug, a commit SHA) from the bundle.
3. If the bundle is thin or the ledger is empty, do not invent value —
   `guidance` already tells you how to frame that case: lead with
   `next_actions` and say plainly that value starts the moment they begin.
