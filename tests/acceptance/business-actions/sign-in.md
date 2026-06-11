# Business Action: Sign In to SageOx

**Actor:** A developer (e.g., Devon, or Quinn when headless)
**Goal:** Authenticate ox with SageOx so it can sync the Ledger and Team Context and reach team knowledge
**Preconditions:**
- ox is installed and on PATH
- Has a verified SageOx account

## Stub

The actor runs `ox login`. ox uses a device-code flow: it shows a short user
code and a verification URL, opens the browser when it can, and polls until the
actor authorizes. In a headless shell it prints the URL and code instead of
opening a browser. On success ox greets the actor by name, syncs git
credentials for the team's repos, and confirms the active endpoint as a clean
slug. If already signed in, ox says so and makes re-auth opt-in.

This stub will be expanded to a full Actor / Goal / Steps / Expected Outcome /
Variations narrative in a follow-up PR.

See: auth/login.feature
See: auth/headless-login.feature
See: auth/logout.feature
