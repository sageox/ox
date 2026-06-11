# Business Action: Prime a Session

**Actor:** An AI coworker (e.g., Avery)
**Goal:** Start a session with the team's full context and recording underway
**Preconditions:**
- The repo is initialized for the team
- Running at session start, after a compaction, or after a context clear

## Stub

The actor runs `ox agent prime`. ox loads the team's context (conventions,
decisions, expert coworkers), issues an agent ID for the session, and begins
recording the session to the Ledger — surfacing a one-time transparency notice
the first time. Re-priming after a compaction or clear reloads the context and
continues the same session rather than starting a duplicate.

This stub will be expanded to a full Actor / Goal / Steps / Expected Outcome /
Variations narrative in a follow-up PR.

See: priming/prime-session.feature
See: session-recording/auto-record.feature
See: team-context/team-ctx.feature
