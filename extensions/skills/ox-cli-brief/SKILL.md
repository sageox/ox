---
name: ox-cli-brief
description: "Show what SageOx gave this session: ox calls made, estimated context tokens, what was read, and what was useful. Args: me | team."
# Lifecycle and diagnostic surfaces stay EXPLICIT (ADR-023): the user
# invokes them, the model never does. This also keeps their descriptions
# out of context, so folding the command surface in costs no tokens.
disable-model-invocation: true
---

> **Explicit invocation only.** Run this when the user asks for it by name.
> It performs a lifecycle or diagnostic action with side effects, so never
> invoke it on your own initiative.

<!-- Thin by design. The whole contract — how to present the report, what
     counts as used, and when to self-report use — lives in the `guidance` field of
     `ox brief --json` (Layer 1), which reaches every AI coworker whether or
     not it has this native surface. This skill only maps the argument and
     relays the command. Do not grow this body. -->

Show what SageOx gave this AI coworker: the ox calls it made, the estimated
context tokens, what it read by title, and which items were useful.

Map `$ARGUMENTS` to a flag: `me` → `--me`, `team` → `--team`, anything else →
no flag.

$ox brief --json <flags>

## Post-Command

- **`guidance`** — Follow it exactly; print the `display` block verbatim.
