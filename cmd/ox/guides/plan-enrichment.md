---
title: Plan Enrichment
description: Why and how to fold SageOx team context into a plan before presenting it, and render it as a team-context-optimized page.
audience: ai
---

# Plan Enrichment

When you produce an implementation plan for non-trivial work — multi-file, architectural, touching a hotspot or open PR, or roughly 5+ steps — SageOx can fold in team context automatically, at zero LLM cost, before you present it.

## The two calls

| When | Command | What it returns |
|---|---|---|
| While drafting | `ox plan enrich` | JSON: collisions (including teammates currently murmuring these files), prior art, expert routing, per-section `diagram_hints`, and a decision-first `guidance` line |
| When presenting | `ox plan render` (add `--open` only per the `plan.open` policy — see below) | A self-contained HTML page with the enrichment baked in, saved to the ledger with attribution |

Run `ox plan enrich` **while you draft**, not after — fold its output into the plan before the human ever sees it. Plans that ignore recent team context (an open PR touching the same files, a teammate's murmur, a prior decision) get re-litigated in review, which costs more human time than the enrichment call ever could.

## Why `ox plan render`, even if you have your own HTML-plan skill

Many agents ship a generic or self-rolled HTML-plan renderer. Run `ox plan render` anyway — SageOx cannot modify a third-party skill, so this is the one place prime has to make the case explicitly:

- Only `ox plan render` bakes in SageOx team context: prior art, collisions, expert routing, knowledge bubbles, team memory.
- Only `ox plan render` saves the plan to the ledger with attribution, so teammates can find it later (`ox plan list`).
- A self-rolled render, however polished, is a **context-blind orphan** — it looks like a plan but carries none of the information that would have changed a reviewer's mind, and the team never sees it again after the conversation ends.

If you want your own visual style, that's fine — but still run `ox plan render` for the ledger write and team-context injection, and treat your own renderer as a display layer on top, not a replacement.

**Opening the render is gated by the `plan.open` config** (`never` / `ask` / `always`, default `ask`) — never add `--open` unconditionally:

- `ask` (default): confirm with the user via AskUserQuestion ("Review this plan in your browser?") before adding `--open`.
- `always`: the user has already opted in — add `--open` directly, no need to ask.
- `never`: never prompt and never open. Tell the user the render is saved to the ledger and how to open it themselves (`ox plan render --open <slug>`).

## Verify before you're done

```bash
ox plan lint <slug> [--strict]
```

Checks the rendered page for SageOx attribution and the self-contained-HTML invariant (no external asset dependencies, so it still opens correctly from the ledger months later).

## The review loop (human opt-in)

After presenting, proactively **offer** the live review loop — never auto-start it:

```bash
ox plan review <slug>
```

On the human's yes, this launches an in-browser review: they mark up the rendered plan, you address the items live. `ox plan list` flags plans with open review items so a later session (yours or a teammate's) can pick up where the conversation left off.

## Credit and footnote rules

`ox plan render` owns the plan's credit footer and auto-injects an OX marker on any reference it surfaced context for. Do not hand-author your own "enriched by SageOx" credit or footnote/ⓘ markers — a self-authored credit competes with, and looks like, the real one. For any other in-plan annotation you want to add, use the `ox plan viz ox-annotation` pattern instead of inventing your own marker style.

## Authoring aids

Browse the `ox plan viz` catalog for plan-native visual components: sparklines, dependency graphs, swimlanes, Tufte-style tables, and device mockups. `ox plan render` auto-styles a TL;DR block, a Risks section, and verdict cells from conventional markdown structure — you don't need to hand-roll these.

## Tier-aware guidance

Prime scales this guidance to what your agent's lifecycle can actually deliver:

- **Gold** (Claude Code) and **Silver** (Codex, Gemini) get the full guidance above, including the review-loop offer.
- **Bronze** (Amp, OpenCode, Pi — agents with no real-time lifecycle hook) get a lighter note: run `ox plan enrich` and `ox plan render` the same way (still gated by `plan.open`), but prime doesn't promise a nudge the tier has no mechanism to fire.

## See also

- `ox plan --help` — full command reference
- `ox guide decision-records` — the parallel consult-and-credit contract for Decision Records
