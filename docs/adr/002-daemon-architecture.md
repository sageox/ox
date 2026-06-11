---
audience: human
ai_editing: prohibited
preserve_voice: true
---

# ADR-002: Background Daemon for Session Management

**Status**: Proposed (V2)
**Date**: 2025-12-10

## Context

Every `ox` command currently makes fresh API calls. For agent workflows with rapid command execution, this creates:
- Network latency on every invocation
- Rate limiting risk with concurrent agents
- Session state scattered across CLI invocations

## Decision

Implement a background daemon that manages sessions, caching, and API optimization for all `ox` commands.

## Rationale

| Benefit | Why It Matters |
|---------|----------------|
| **Faster commands** | Cache hits eliminate network round trips |
| **Offline support** | Stale cache serves requests when network unavailable |
| **Cleaner UX** | No `--agent-id` flags cluttering commands |
| **Rate limit safety** | Single point manages API quotas across concurrent agents |

## Alternatives Considered

1. **Session files**: Simpler, but doesn't solve caching or rate limiting
2. **Per-command caching**: Fragmented, cache coherency issues
3. **Stateless approach**: Current design, has latency and rate limit problems

## Consequences

- Adds process management complexity
- Requires cross-platform IPC (Unix sockets, Windows named pipes)
- Daemon lifecycle becomes user-visible
