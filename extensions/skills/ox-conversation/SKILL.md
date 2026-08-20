---
name: ox-conversation
description: >-
  Read recorded team conversations locally and follow sageox:// citations back
  to what was actually said. Auto-fire when the user asks "what did we say in
  that meeting", "show me the discussion about X", "what's behind this claim",
  pastes a sageox:// citation URI or a cnv_/rec_ id, or wants a transcript,
  the topics, or the summary of a recorded conversation. Run
  `ox conversation list|show|topics|topic|transcript` — each JSON envelope's
  guidance field names the next rung and token_estimate reports its cost;
  pass a full sageox:// URI (quoted) to transcript to retrieve the cited
  cues with an honest pinning status.
---

<!-- Thin by design. The behavioral contract — the disclosure ladder, id
     forms, pinning semantics, and each next step — lives in the `guidance`
     field of every `ox conversation` JSON envelope and in
     `ox guide conversations` (Layer-1: the prime KB block names the verb),
     which reach every primed AI coworker. This native skill adds discovery
     and activation ergonomics; it duplicates no reasoning. Do not grow
     this body. -->

## Use when

The user references a recorded conversation or a citation from one: a
`sageox://` URI, a `cnv_`/`rec_` id, "what did we say about X in that
meeting", "what's the source behind this claim", or asks for a
conversation's summary, topics, or transcript.

## Do

1. Run the rung the question requires — `ox conversation list`,
   `show <id>`, `topics <id>`, `topic <id> <tp_id>`, or
   `transcript <id> --cues N-M` (a full `sageox://` URI, quoted, works as
   `<id>` and carries its own selectors).
2. Follow the `guidance` field in each JSON envelope — it names the next
   rung down and how to follow citations; `token_estimate` tells you what
   reading the payload costs. Descend only as deep as the question needs.
3. For the full workflow (id forms, disclosure ladder, pinning semantics),
   read `ox guide conversations --raw`.
