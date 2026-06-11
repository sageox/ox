---
audience: human
ai_editing: prohibited
preserve_voice: true
---

# ADR-003: Smart Tips System for Feature Discovery

**Status**: Accepted
**Date**: 2025-12-10

## Context

Users don't discover `ox` features organically. Common pattern: they use `ox init` and `ox prime` but never learn about `ox review`, `ox doctor --fix`, or other capabilities.

## Decision

Add contextual tips that appear after command execution, helping users discover related features.

## Design Choices

| Aspect | Choice |
|--------|--------|
| When to show | After login/status/init, when output is minimal, 10-15% random |
| Content mix | 80% contextual to command, 20% general discovery |
| Disable | `--quiet` flag or config option |

## Rationale

Tips are the lowest-friction way to surface features. Unlike docs (users don't read), release notes (users miss), or onboarding flows (users skip), tips appear *in context* when users are already engaged.

## Alternatives Considered

1. **Interactive tutorial**: High friction, users skip it
2. **Newsletter**: Users don't read
3. **In-app notifications**: Requires UI, doesn't fit CLI

## Consequences

- Slight output clutter (mitigated by `--quiet`)
- Need to maintain tip content as features evolve
