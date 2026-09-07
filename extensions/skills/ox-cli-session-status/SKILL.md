---
name: ox-cli-session-status
description: "Check the status of all active session recordings in this project."
# Lifecycle and diagnostic surfaces stay EXPLICIT (ADR-023): the user
# invokes them, the model never does. This also keeps their descriptions
# out of context, so folding the command surface in costs no tokens.
disable-model-invocation: true
---

> **Explicit invocation only.** Run this when the user asks for it by name.
> It performs a lifecycle or diagnostic action with side effects, so never
> invoke it on your own initiative.

     belongs in the ox CLI JSON output (guidance field), not here.
     Skills are agent-specific wrappers; ox serves all agents (Codex, etc.). -->
Check the status of all active session recordings in this project.

## Post-Command

After the command completes, check the JSON output:
- **`recording: true`** — A session is active. Continue working normally.
- **`recording: false`** — No active session. Consider running `/ox-session-start` if you want to record.
- **`guidance`** — Follow any guidance returned by the CLI.
- **`entry_count`** — Number of entries captured so far.
- **`count > 1`** — Multiple concurrent recordings. The `sessions` array shows each one.
- **`agent_id`** — The agent ID of the recording. Compare with your own to identify your session.

$ox session status --json --current
