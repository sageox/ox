# Vendored: the live session wire contract

This package is a verbatim copy of `packages/sessionstream` from the SageOx
monorepo (ADR-247: real-time session capture). The server, the Temporal
worker and the `session-capture` twin import the same code, so the bytes this
client signs, hashes and batches are exactly what api-go verifies.

- Source: `sageox-monorepo/packages/sessionstream` at commit `b98b6427d4` (PR #12438: client conformance fixes and `ErrStoredStreamBroken`).
- Plus one later struct, copied by hand: `OpenSessionRequest`'s optional
  `parent_session_id` and `continued_from_session_id` (monorepo branch
  `ryan/session-rollup`). Only that struct was taken; the rest of the monorepo
  package has moved on since `b98b6427d4` and is not part of this copy.
- Do not edit these files here. Change the monorepo package, then re-copy
  every `.go` file and update the commit above.
- The protocol logic belongs in ox-core long term (team rule
  `shared-logic-belongs-in-ox-core`); this copy is the client's interim home.
