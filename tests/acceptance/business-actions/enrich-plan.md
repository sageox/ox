# Business Action: Enrich a Plan

**Actor:** An AI coworker (e.g., Avery)
**Goal:** Fold the team's context into an implementation plan before a human sees it
**Preconditions:**
- In a session for the team, drafting a plan in plan mode

## Stub

While still drafting, the actor runs `ox plan enrich` to get deterministic
team-context signals — collisions with in-flight work, prior art, expert
routing — computed locally with no LLM or network call. A once-per-entry
plan-mode hint reminds the actor to do this before presenting. The actor
reshapes the plan around the signals, then on presentation offers a SageOx
team-context-optimized HTML render via `ox plan render --open`; the plan-exit
nudge points the human to the render and the review loop.

This stub will be expanded to a full Actor / Goal / Steps / Expected Outcome /
Variations narrative in a follow-up PR.

See: plan-enrichment/enrich-while-drafting.feature
See: plan-enrichment/render-and-present.feature
See: plan-enrichment/review-loop.feature
