# Business Action: Initialize a Repository

**Actor:** An onboarding developer (e.g., Devon)
**Goal:** Bring a repository onto SageOx and share the setup with the team
**Preconditions:**
- Signed in to SageOx
- Inside a git repository

## Stub

The actor runs `ox init`. ox wires the repo to the team's endpoint, sets up the
Ledger and Team Context, installs the agent hooks, and injects the prime marker
so any AI coworker knows to run `ox agent prime` at session start. The actor
then commits the `.sageox/` directory and pushes, so teammates inherit the same
SageOx setup when they pull. Re-running init is safe and non-destructive.

This stub will be expanded to a full Actor / Goal / Steps / Expected Outcome /
Variations narrative in a follow-up PR.

See: onboarding/init-repo.feature
See: onboarding/status.feature
See: onboarding/doctor.feature
