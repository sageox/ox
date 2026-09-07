---
name: ox-cli-status
description: "Check SageOx project status including authentication, sync, and daemon health."
# Lifecycle and diagnostic surfaces stay EXPLICIT (ADR-023): the user
# invokes them, the model never does. This also keeps their descriptions
# out of context, so folding the command surface in costs no tokens.
disable-model-invocation: true
---

> **Explicit invocation only.** Run this when the user asks for it by name.
> It performs a lifecycle or diagnostic action with side effects, so never
> invoke it on your own initiative.

Use when:
- Checking if you're logged in or authenticated
- Verifying project initialization and configuration
- Checking sync status of ledger and team context
- Confirming the daemon is running and healthy
- Getting an overview of SageOx state for this repository

Keywords: status, auth, sync, health, logged in, initialized, daemon, check, state, connected

## Common Issues

### Not logged in
**Symptom:** Status shows authentication as missing or expired
**Solution:** Run `ox login` to authenticate with SageOx cloud

### Not initialized
**Symptom:** Status shows no SageOx configuration
**Solution:** Run `ox init` to initialize SageOx in this repository

### Daemon not running
**Symptom:** Status shows daemon as offline or unreachable
**Solution:** Run `ox daemon start` to start the background sync daemon

$ox status
