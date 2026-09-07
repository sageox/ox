---
name: ox-cli-cart-start
description: "<!-- Keep this file thin. Portable behavioral guidance (the naming intent)"
# Lifecycle and diagnostic surfaces stay EXPLICIT (ADR-023): the user
# invokes them, the model never does. This also keeps their descriptions
# out of context, so folding the command surface in costs no tokens.
disable-model-invocation: true
---

> **Explicit invocation only.** Run this when the user asks for it by name.
> It performs a lifecycle or diagnostic action with side effects, so never
> invoke it on your own initiative.

     lives in the ox CLI JSON output (guidance field), not here — ox serves
     all agents (Codex, Droid). Only the host-specific /rename mechanism is a
     legitimate Layer-2 note below. -->
Claim a cart and start working on it.

## Post-Command

Follow the `guidance` field in the JSON output.

Layer-2 (Claude Code only): apply the name from `guidance` to this session via
`/rename` with a kebab-case name derived from the cart title (e.g. cart title
"Auth middleware rate limiting" → `/rename auth-rate-limiting`).

$ox carts start $ARGUMENTS --json
