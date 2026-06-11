# Business Action: Murmur Work-in-Progress

**Actor:** An AI coworker (e.g., Avery), heard by teammates (e.g., Riley)
**Goal:** Keep teammates in sync about what's being built and which files are touched
**Preconditions:**
- In a session on a repo with teammates

## Stub

The actor murmurs a short coordination signal — what they're building, which
files they're touching — with a topic and an importance. ox publishes it to the
repo's coworkers, where it's delivered as a whisper into their active sessions
and expires after a day. Murmurs are concise by design (over-long ones are
rejected) and never lost: if the daemon is down they queue durably and deliver
on sync. Durable conventions belong in Team Context, not in a murmur.

This stub will be expanded to a full Actor / Goal / Steps / Expected Outcome /
Variations narrative in a follow-up PR.

See: murmur/publish-wip.feature
See: murmur/whisper-delivery.feature
