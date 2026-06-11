# Business Action: Upgrade ox

**Actor:** A developer (e.g., Devon, Sam)
**Goal:** Move to a newer ox version and keep the team's setup consistent
**Preconditions:**
- ox is installed

## Stub

The actor runs `ox upgrade`. ox detects how it was installed (Homebrew, `go
install`, or a manual download) and upgrades the right way for that install,
then `ox version` reflects the new build. When the installed ox falls behind
what a project expects, ox surfaces the mismatch and points to upgrade. After
upgrading, `ox doctor` verifies compatibility and refreshes anything the new
version changed.

This stub will be expanded to a full Actor / Goal / Steps / Expected Outcome /
Variations narrative in a follow-up PR.

See: upgrade/upgrade-cli.feature
See: upgrade/version-mismatch.feature
