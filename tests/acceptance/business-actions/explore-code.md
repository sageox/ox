# Business Action: Explore Code Before Planning

**Actor:** An AI coworker (e.g., Avery)
**Goal:** Plan against the live shape of the codebase, not a stale mental model
**Preconditions:**
- In a session in the repo

## Stub

Before drafting, the actor searches the code and git history with `ox code
search` (compact results by default to conserve session context) and reads `ox
code insights` for hotspots, recent activity, open PRs, and contention. Seeing
where their change might collide with in-flight work lets the actor route the
plan — and the team-context signals that enrich it — around contention from the
start.

This stub will be expanded to a full Actor / Goal / Steps / Expected Outcome /
Variations narrative in a follow-up PR.

See: code-intelligence/code-search.feature
See: code-intelligence/insights.feature
