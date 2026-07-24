# ADR: Consultation-Attribution Privacy

- **Status:** Proposed
- **Date:** 2026-07-23
- **Deciders:** Ryan Snodgrass (required review — data access ergonomics), SageOx Team
- **Relates to:** [Ledger Architecture](adr-ledger-architecture.md), [Whisper & Murmur Architecture](adr-whisper-murmur-architecture.md), epic `ox-bcgb` (knowledge-flow influence instrumentation)

## Context

The knowledge-flow instrumentation (epic `ox-bcgb`) tags session turns where an agent **consulted** SageOx knowledge — a read of a ledger/team-context file, an `ox` retrieval command, a whisper injection. These `consulted` events are written into the reader's own `context-trace.jsonl`, which syncs through the ledger to the whole team.

That makes a new class of data visible: **who read what, and when.** `ox recap` will mine it to show causal chains — "you read Dmitri's token-refresh session in turn 14, then shipped the fix," and eventually the team-lead view "auth knowledge: 3 authors → 7 consumers." Read-tracking is sensitive: done wrong it becomes a surveillance dashboard, and engineers who feel watched stop consulting — which destroys the very behavior the product exists to encourage.

This ADR sets the privacy principle before the cross-teammate flows (`ox-bcgb.8`) surface. It requires Ryan's sign-off because it governs data-access ergonomics.

## Decision

**A read is recorded at the same visibility level as the thing that was read, in the reader's own record.**

Rationale: the ledger already shares full session transcripts among teammates. "Session Y cited session X" is *strictly less* sensitive than content the team already syncs — it is a pointer, not new private content. It lives in Y's own session dir, where Y can see it, redact it (existing redaction pass), or abort the session before upload (existing lifecycle). No new sharing boundary is crossed.

Display rules layered on top:

1. **Individual recap shows names in both directions** — the data is mutual (both the reader and the cited author already share transcripts), so "your session was consulted by Maya" and "you consulted Dmitri's session" are both fair game *to the individual whose report it is*.
2. **Team recap shows aggregate flows only** — "auth knowledge: 3 authors → 7 consumers," never a who-reads-whom matrix, never per-person consultation counts, never a ranking. A value report that doubles as a productivity-surveillance tool poisons the well.
3. **Opt-out exists** — `recap.trace_consultations` (default on). Off means the reader's `consulted` events are not written, so nothing about their reads reaches the team.
4. **Refs, not content** — a `consulted` event records *what* was consulted (a path, a session name, a query subject) and its turn — never a second copy of the content, and (for the recall hook) never the prompt text.

## Consequences

- Cross-teammate attribution (`ox-bcgb.8`) may proceed once this is Accepted, bounded by rules 1–4.
- The team-lead surface must be built aggregate-first; a per-person consultation view is explicitly out of scope and should be rejected in review if proposed.
- `recap.trace_consultations` is a customer-facing config; its env override (if any) follows the `SAGEOX_*` namespace (ADR-047 in sageox-mono).

## Status note

**Proposed** — not yet Accepted. The instrumentation writes `consulted` events already (they are strictly less sensitive than synced transcripts), but the cross-teammate *surfacing* and the team-lead view must not ship until this ADR is Accepted by Ryan.
