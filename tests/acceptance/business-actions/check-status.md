# Business Action: Check Status

**Actor:** A developer (e.g., Devon)
**Goal:** Get an at-a-glance read on the current repo's SageOx health
**Preconditions:**
- ox is installed

## Stub

The actor runs `ox status`. ox reports whether they're signed in, whether the
repo is initialized, whether the Ledger and Team Context are syncing, and
whether the background daemon is healthy. Every not-OK line names the single
command that fixes it (`ox login`, `ox init`, start the daemon). A JSON form is
available for scripting.

This stub will be expanded to a full Actor / Goal / Steps / Expected Outcome /
Variations narrative in a follow-up PR.

See: onboarding/status.feature
See: onboarding/doctor.feature
