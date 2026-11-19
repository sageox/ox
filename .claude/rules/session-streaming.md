# Real-Time Session Streaming — Consult the Spec First

Any work touching real-time streaming of coding sessions — `internal/livesession/`,
`internal/sessionstream/`, the capture-to-spool hand-off (`session.AppendedBatch`,
`livesession.SpoolCommitted`), `internal/moq/`, `internal/daemon/sessionstream/`, the stream
envelope, wire/entry seq semantics, `revision`/amendments, pause fencing, abort handling, or
the coordinates/cloud contract — MUST consult **`docs/specs/session-streaming.md`** before
designing or changing anything. That spec is the design canon; it was adversarially reviewed
and is mapped onto the sageox-mono conversation model (ADR-057/076/081/087). What ships is
ADR-247 HTTP streaming: its section in the spec says where it departs from the MoQ design.

Non-negotiables it encodes (do not re-derive or contradict them casually):

- **Streaming must be unable to block or corrupt capture.** Capture commits its batch to
  `raw.jsonl` first. Only then are the bytes that batch committed handed to the spool, and a
  spool failure becomes a declared `spool_write_failed` gap that never touches `raw.jsonl`
  or the capture cursor. The older wording, "tail `raw.jsonl`, never tee `RawWriter`",
  predates ADR-247: it was written for the MoQ tailer, which never shipped. ADR-247 HTTP
  streaming hands off committed bytes instead (spec: "ADR-247 HTTP streaming").
  Git/LFS upload stays the durability + fidelity authority.
- **Conversation-native envelope.** `conversation_id` = `cnv_` twin of `ses_` (pure prefix
  swap, bijective — never bind a foreign `cnv_`); legacy `turns.jsonl` shapes are
  server-side projections, never the wire format.
- **Dense wire `seq` fold.** Wire-seq assignment is a deterministic fold over the
  **canonical eligible-frame stream**: `raw.jsonl` content *including its persisted
  pause/lifecycle markers* (suspended entries are ineligible and consume no seq). Live
  send-commit assignment and replay reconstruction consume the same persisted marker state,
  so they cannot diverge. Liveness pings are seq-less; dedup key is
  `(conversation_id, seq, revision)`; `revision` = file rewrite generation.
- **Pause is fail-closed with a frame-atomic fence** (range membership on
  `suspended_from_entry_seq`, decided at send-commit; frames are never torn).
- **Honest acks.** moq-lite has no object ack; cursor advance = local send success, not
  durability. No receipt plane exists yet (ADR-087 confirms) — never design as if one does.
- **Abort persists a durable intent marker** that clears ONLY on server-confirmed tombstone
  persistence — a successful authenticated HTTPS abort response (or a future receipt-plane
  ack). Relay-level close delivery is best-effort notification and never clears the marker
  (same honest-ack doctrine: send success is not persistence). Aborted content is never
  projected.
- **Flag-gated, default off** (`FEATURE_SESSION_STREAMING`); the cloud coordinates call is
  the authoritative enable, and enablement is **dogfood-only (sageox team accounts) until
  the mono purge/re-redact API ships** — customer enablement is blocked on it (see the
  spec's "Retroactive redaction (policy)" section, which is authoritative). Streaming
  failures are log-only — never surfaced into session flow.

If a change genuinely requires breaking one of these, update the spec in the same PR and
say so explicitly in the PR body — the spec and the code must never disagree silently.
