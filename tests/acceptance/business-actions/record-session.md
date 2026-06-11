# Business Action: Record a Session

**Actor:** An AI coworker (e.g., Avery)
**Goal:** Capture the session so future coworkers can recall what happened and why
**Preconditions:**
- Primed at session start

## Stub

Recording runs automatically from prime to the end of the session. The actor
can pause recording for a stretch that shouldn't be captured and resume later;
the paused interval is excluded and ox reports how much. At session end ox
finalizes the recording, summarizes it, and lands it in the Ledger. Capture
keeps working offline and syncs when the network returns; an interrupted
session is recoverable.

This stub will be expanded to a full Actor / Goal / Steps / Expected Outcome /
Variations narrative in a follow-up PR.

See: session-recording/auto-record.feature
See: session-recording/pause-resume.feature
See: session-recording/list-and-view.feature
