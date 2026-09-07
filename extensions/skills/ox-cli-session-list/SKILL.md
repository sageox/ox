---
name: ox-cli-session-list
description: "List recent sessions from the project ledger and offer to view one."
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
List recent sessions from the project ledger and offer to view one.

## Steps

Run `$ARGUMENTS` (or `ox session list --limit 5` if no arguments are given),
then present the results and ask which session to view. Follow the JSON
`guidance` field to view a session (`ox session view <name>`) and to hydrate
dehydrated entries first.
