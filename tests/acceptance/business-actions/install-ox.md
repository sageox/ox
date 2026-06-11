# Business Action: Install ox

**Actor:** An onboarding developer (e.g., Devon)
**Goal:** Get the `ox` command onto their PATH so it works in every shell and repo
**Preconditions:**
- Has cloned the ox repository (for a from-source build) or has a package manager available
- Has a working Go toolchain for the from-source path

## Stub

The actor builds ox from source and installs it, which puts the `ox` binary
(and its bundled adapters) onto their PATH. They confirm the install with `ox
version`. On first use inside a repo, ox guides them to the next step — `ox
login` if they're not authenticated, or `ox init` if the repo isn't yet set up
for SageOx — rather than failing opaquely.

This stub will be expanded to a full Actor / Goal / Steps / Expected Outcome /
Variations narrative in a follow-up PR.

See: install/build-and-install.feature
See: auth/login.feature
See: onboarding/init-repo.feature
