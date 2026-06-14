---
name: ox-consult
description: >-
  Search SageOx team memory BEFORE answering from first-principles reasoning.
  Auto-fire when the user references recent or specific work or asks a
  before/after question: "I just pushed...", "did X fix Y?", "is the alert gone
  now?", "this request", or anything tied to a prior decision, a prod anomaly, or
  a metric/cost change. A confident answer that prior work contradicts is worse
  than a slow one — check first, then answer. Routes the cue to the right corpus:
  recency to `ox session list`, conceptual to `ox query`, code-provenance to
  `ox code search`.
---

<!-- Thin by design. The authoritative consult-first reflex — the cues, the
     "confident-wrong is worse than slow" reasoning, and the routing rationale —
     lives in the <consult-first> block of `ox agent prime` (Layer-1 floor), which
     reaches Claude, Codex, and Droid alike. This skill adds ONLY Claude-specific
     auto-activation ergonomics; it duplicates no reasoning. Every command it
     names is already reachable from the floor, so removing this skill loses
     ergonomics but never the floor. Do not grow this body. -->

## Use when

The user's message carries a consult cue — STOP and search before reasoning:

- References recent or specific work: "I just pushed...", "this request", "did X fix Y?", "is the alert gone now?"
- Touches a prior decision, a prod anomaly, or a metric/cost change — anything with a before/after.

## Route the cue (different retrieval modes — not interchangeable)

- Recency / "I just did X" → `ox session list --limit 20 --json` (chronological; the summary is in the list).
- Conceptual / "did we decide or discuss X?" → `ox query "<question>"` (semantic; add `--source=all` to include code).
- "Who or what touched this code?" → `ox code search "<pattern>"`.
