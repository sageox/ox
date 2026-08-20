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
| When presenting | Author `plan.html`, then `ox plan save --file plan.html` and `ox plan render --file plan.html` (add `--open` only per the `plan.open` policy — see below) | The authored visual page preserved as the plan of record, with SageOx chrome injected and markdown derived |

Run `ox plan enrich` **while you draft**, not after — fold its output into the plan before the human ever sees it. Plans that ignore recent team context (an open PR touching the same files, a teammate's murmur, a prior decision) get re-litigated in review, which costs more human time than the enrichment call ever could.

## Why the authored HTML must be the plan of record

For material work, author a purpose-built visual `plan.html`; then pass that page through ox:

- `ox plan save --file plan.html` records the authored page as canonical (`primary=html`) and derives `plan.md` for terminal/search use.
- `ox plan render --file plan.html` injects prior art, collisions, expert routing, knowledge bubbles, team memory, attribution, and the review loop without rewriting the authored page.
- `ox plan review <slug>` reopens that same visual argument; it must never regenerate a generic page from the derived markdown.

Never use the rejected legacy `--plan + --html` pair. It creates two candidate
sources of truth and historically allowed review to discard the authored page.

## The authoring philosophy

Every plan is authored against one creed:

> **Don't waste human attention. Delight them. Educate them visually and crisply.**

- **Don't waste human attention.** The approver's time is the scarce resource — lead with
  the decision, relocate depth, never make them read the implementer's notes to approve.
- **Delight them.** A plan that does work a document cannot — an inspector, a toggleable
  timeline, a verdict card — in the warm SageOx register, reads as a first-class surface,
  not a generic dev-tool doc. Delight is what makes reviewers *want* the ox-rendered plan.
- **Educate them visually.** Teach the decision with a hero visualization (topology,
  sequence, state, comparison), not a wall of prose. Show the shape; don't describe it.
- **…crisply.** Conclusion and biggest risk first; every element earns its pixels.

The quality bar and the design register that make this concrete live in
`docs/specs/plan-authoring-html.md` (in the ox repo). Author against the creed, then verify
with `ox plan lint`.

## Two readers, two layers

The visible page is a decision surface: conclusion, trade-offs, biggest risk,
and one meaningful hero visualization. Put exact file edits, rollout mechanics,
and gotchas in one closed `<details><summary>Implementation notes</summary>` at
the end. This preserves implementation depth without turning the approver's
first ten minutes into a wall of text.

**Opening the render is gated by the `plan.open` config** (`never` / `ask` / `always`, default `ask`) — never add `--open` unconditionally:

- `ask` (default): confirm with the user via AskUserQuestion ("Review this plan in your browser?") before adding `--open`.
- `always`: the user has already opted in — add `--open` directly, no need to ask.
- `never`: never prompt and never open. Tell the user the render is saved to the ledger and how to open it themselves (`ox plan render --open <slug>`).

## Verify before you're done

```bash
ox plan lint <slug> [--strict]
```

Checks the rendered page for SageOx attribution, meaningful-visual realization,
collapsed implementation depth on material plans, and self-contained-HTML
invariants. Decorative OX SVG chrome does not satisfy the visualization check.

## The review loop (human opt-in)

After presenting, proactively **offer** the live review loop — never auto-start it:

```bash
ox plan review <slug>
```

On the human's yes, this launches an in-browser review: they mark up the rendered plan, you address the items live. `ox plan list` flags plans with open review items so a later session (yours or a teammate's) can pick up where the conversation left off.

## Credit and footnote rules

`ox plan render` owns the plan's credit footer and auto-injects an OX marker on any reference it surfaced context for. Do not hand-author your own "enriched by SageOx" credit or footnote/ⓘ markers — a self-authored credit competes with, and looks like, the real one. For any other in-plan annotation you want to add, use the `ox viz ox-annotation` pattern instead of inventing your own marker style.

## Authoring aids

Use `ox viz suggest "<what needs explaining>"` for architecture and flow diagrams, sparklines, dependency graphs, swimlanes, Tufte-style tables, and device mockups. The catalog works in plans, docs, PRs, reports, and design notes. The generic markdown renderer is a quick-path approximation for small, low-stakes plans; a material plan gets authored HTML.

## Tier-aware guidance

Prime scales this guidance to what your agent's lifecycle can actually deliver:

- **Gold** (Claude Code) and **Silver** (Codex, Gemini) get the full guidance above, including the review-loop offer.
- **Bronze** (Amp, OpenCode, Pi — agents with no real-time lifecycle hook) get a lighter note: run `ox plan enrich` and `ox plan render` the same way (still gated by `plan.open`), but prime doesn't promise a nudge the tier has no mechanism to fire.

## See also

- `ox plan --help` — full command reference
- `ox guide decision-records` — the parallel consult-and-credit contract for Decision Records
