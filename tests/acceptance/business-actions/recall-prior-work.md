# Business Action: Recall Prior Work

**Actor:** A coworker (e.g., Riley, Avery)
**Goal:** Answer "have we done this before?" and "why did we decide it this way?"
**Preconditions:**
- The team has recorded discussions, decisions, and prior sessions

## Stub

The actor queries the team's knowledge with `ox query`, searching across
discussions, decisions, docs, and prior sessions. ox surfaces the relevant
prior context with enough provenance to trace each result to its source. Query
works offline against the locally-cached Ledger and returns structured results
usable from within a session. To read a specific session, the actor lists and
views it, fetching a stub to local first if needed.

This stub will be expanded to a full Actor / Goal / Steps / Expected Outcome /
Variations narrative in a follow-up PR.

See: team-context/query.feature
See: session-recording/list-and-view.feature
