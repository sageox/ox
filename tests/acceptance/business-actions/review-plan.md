# Business Action: Review a Plan

**Actor:** A developer reviewer (e.g., Devon)
**Goal:** Review a saved plan and drive its open items to resolution
**Preconditions:**
- A plan has been saved to the Ledger

## Stub

The actor opens `ox plan review <slug>`. ox serves the rendered plan, collects
feedback inline, and auto-reloads as the plan changes, so the back-and-forth
happens against a live page rather than copied text. Open review items keep the
plan flagged in `ox plan list` until they're resolved or the plan is approved.
Approval closes out the review.

This stub will be expanded to a full Actor / Goal / Steps / Expected Outcome /
Variations narrative in a follow-up PR.

See: plan-enrichment/review-loop.feature
See: plan-enrichment/browser-review-roundtrip.feature
See: plan-enrichment/render-and-present.feature
