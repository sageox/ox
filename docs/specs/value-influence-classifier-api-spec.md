# Value Influence Classifier API Specification

**Endpoint:** `POST /api/v1/value/influence-classify`
**Status:** Proposed
**Beads:** ox-bcgb.7 (epic ox-bcgb)
**Impl:** server-side (sageox-mono) — this repo owns the contract + the Go client only.

---

## Overview

Tier 3 of knowledge-flow attribution. The deterministic tiers (file reads, retrieval commands, whisper injections — see `internal/session/consultscan`) tag turns where the agent *provably* consulted SageOx knowledge. They cannot see **implicit** influence: a turn whose decision leaned on context the agent already held from prime, with no fresh retrieval.

This endpoint closes that gap the only way it can be closed — a post-hoc LLM read of the session against the context it was given. It is the lowest-precision, highest-cost tier, so its output is **always** graded `mechanism: "llm"` and never rendered as provable. It runs server-side per the secret-sauce policy; the CLI posts evidence and receives graded judgments.

---

## Request

```json
{
  "session_id": "ses_019f...",
  "repo_id": "repo_019c...",
  "turns": [
    { "seq": 14, "role": "assistant", "text": "…the turn's reasoning/decision…" }
  ],
  "available_context": [
    { "ref": "principles.md", "ref_type": "doc", "excerpt": "…what was in the agent's context…" }
  ],
  "already_tagged_seqs": [2, 9]
}
```

- `turns` — the turns NOT already deterministically tagged (`already_tagged_seqs` lets the server skip them; no point re-judging a provable consult).
- `available_context` — what prime/recall put in front of the agent (from `provided` context-trace events), so the classifier judges influence against real inputs, not guesses.

## Response

```json
{
  "influences": [
    {
      "seq": 14,
      "ref": "principles.md",
      "ref_type": "doc",
      "confidence": 0.72,
      "rationale": "decision restates the 'shared context compounds judgment' principle"
    }
  ],
  "model": "haiku-4.5",
  "generated_at": "2026-07-23T00:00:00Z"
}
```

The CLI writes each returned influence as a `contexttrace.Event{Type: influenced, Mechanism: llm, Seq, Ref, RefType}` into the session trace, so recap mines it exactly like the deterministic tiers but renders it grade-labeled.

## Honesty contract (load-bearing)

- The server may only reference `ref`s present in `available_context` — the client validates every returned `ref` against the submitted bundle and drops any it can't ground. The classifier physically cannot introduce a source the session didn't actually have.
- Every `influenced/llm` event is rendered by recap as *inferred*, never as a proven chain. A confidence below a floor (e.g. 0.5) is dropped.

## Fallback

Offline / unauthenticated / non-2xx: no Tier-3 events are written. The deterministic tiers stand alone — the report is simply less complete, never wrong.
