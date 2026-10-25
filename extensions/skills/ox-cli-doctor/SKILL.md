---
name: ox-cli-doctor
description: "Run diagnostic checks on SageOx configuration and integrations."
# Lifecycle and diagnostic surfaces stay EXPLICIT (ADR-023): the user
# invokes them, the model never does. This also keeps their descriptions
# out of context, so folding the command surface in costs no tokens.
disable-model-invocation: true
---

> **Explicit invocation only.** Run this when the user asks for it by name.
> It performs a lifecycle or diagnostic action with side effects, so never
> invoke it on your own initiative.

Use when:
- Something isn't working and you need to troubleshoot
- Verifying SageOx setup is correct after initialization
- Checking for configuration drift or broken integrations
- Debugging sync, auth, or daemon issues
- After upgrading ox CLI to verify compatibility

Keywords: doctor, diagnose, troubleshoot, fix, check, debug, health, verify, broken, issue

## Common Issues

### Setup required
**Symptom:** Doctor reports missing initialization
**Solution:** Run `ox init` to initialize SageOx in this repository

### Checks failed
**Symptom:** One or more diagnostic checks report failures
**Solution:** Follow the remediation steps doctor provides for each failed check

### Daemon not running
**Symptom:** Doctor reports daemon is unreachable
**Solution:** Run `ox daemon start` to start the background sync daemon

$ox doctor
