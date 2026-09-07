---
name: ox-cli-init
description: "Initialize SageOx for this repository."
# Lifecycle and diagnostic surfaces stay EXPLICIT (ADR-023): the user
# invokes them, the model never does. This also keeps their descriptions
# out of context, so folding the command surface in costs no tokens.
disable-model-invocation: true
---

> **Explicit invocation only.** Run this when the user asks for it by name.
> It performs a lifecycle or diagnostic action with side effects, so never
> invoke it on your own initiative.

Use when:
- Setting up SageOx in a new repository for the first time
- Configuring ox CLI integration with your coding agent
- Installing hooks for automatic guidance loading

Keywords: init, initialize, setup, install, configure, first-time, onboarding

## Common Issues

### Already initialized
**Symptom:** `SageOx already initialized in this repository`
**Solution:** This is fine - ox is ready to use. Run `ox agent prime` to load guidance

### Permission denied
**Symptom:** Cannot create .sageox directory
**Solution:** Check write permissions for the repository directory

### Not a git repository
**Symptom:** `not a git repository`
**Solution:** Initialize git first: `git init`

$ox init
