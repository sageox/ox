---
name: ox-cli-usage
description: "Show what ox did in this session: calls made, context injected, items surfaced, and which were used. Args: me | team."
# Lifecycle and diagnostic surfaces stay EXPLICIT (ADR-023): the user
# invokes them, the model never does. This also keeps their descriptions
# out of context, so folding the command surface in costs no tokens.
disable-model-invocation: true
---

> **Explicit invocation only.** Run this when the user asks for it by name.
> It performs a lifecycle or diagnostic action with side effects, so never
> invoke it on your own initiative.

<!-- Thin by design. The whole contract — what to self-report, when to re-run,
     and how to present the report — lives in the `guidance` field of
     `ox usage --json` (Layer 1), which reaches every AI coworker whether or
     not it has this native surface. This skill only maps the argument and
     relays the command. Do not grow this body. -->

Show what ox did for this AI coworker: the ox calls it made, the team context
ox put in front of it, the items ox surfaced, and which of them were used.

Map `$ARGUMENTS` to a flag: `me` → `--me`, `team` → `--team`, anything else →
no flag.

$ox usage --json <flags>

## Post-Command

- **`guidance`** — Follow it exactly; print the `display` block verbatim.
