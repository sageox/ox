# Business Action: Run Doctor

**Actor:** A developer (e.g., Devon, Sam)
**Goal:** Detect and repair anything wrong with the project's SageOx setup
**Preconditions:**
- ox is installed

## Stub

The actor runs `ox doctor`. As the last line of defense, doctor detects every
known failure mode — treating a missing value as just as broken as a wrong one
— and auto-fixes the safe ones (restoring the prime marker, normalizing an
endpoint, refreshing outdated wrappers). For anything it won't touch on its
own, it explains the problem and gives the exact remediation step, and it
reassures that not-yet-synced session data is safe.

This stub will be expanded to a full Actor / Goal / Steps / Expected Outcome /
Variations narrative in a follow-up PR.

See: onboarding/doctor.feature
See: upgrade/version-mismatch.feature
