# Business Action: Load Team Context

**Actor:** An AI coworker (e.g., Avery)
**Goal:** Plan against the team's accumulated judgment, not just the code in front of them
**Preconditions:**
- In a session for the team

## Stub

The actor requests the team's context with `ox agent team-ctx`, getting the
team's distilled discussions, decisions, and conventions — optionally scoped to
a specific area. They can also list the team's expert coworkers and load one to
bring a specialist's perspective into the session. This grounds the plan in
existing decisions instead of relighting settled questions.

This stub will be expanded to a full Actor / Goal / Steps / Expected Outcome /
Variations narrative in a follow-up PR.

See: team-context/team-ctx.feature
See: team-context/query.feature
