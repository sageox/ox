---
name: ox-cli-session-start
description: "Start recording this agent session to the project ledger."
# Lifecycle and diagnostic surfaces stay EXPLICIT (ADR-023): the user
# invokes them, the model never does. This also keeps their descriptions
# out of context, so folding the command surface in costs no tokens.
disable-model-invocation: true
---

> **Explicit invocation only.** Run this when the user asks for it by name.
> It performs a lifecycle or diagnostic action with side effects, so never
> invoke it on your own initiative.

Start recording this agent session to the project ledger.

## Post-Command (REQUIRED)

After the command completes, check the JSON output:
- **`notice`**: If present, display the notice text to the user verbatim. This is a one-time transparency notice about session recording.
- **`guidance`**: Follow this guidance throughout the session. It contains instructions about plan capture, session boundaries, and troubleshooting.

$ox agent session start
