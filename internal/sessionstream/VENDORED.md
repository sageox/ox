# Vendored: the live session wire contract

This package is a verbatim copy of `packages/sessionstream` from the SageOx
monorepo (ADR-247: real-time session capture). The server, the Temporal
worker and the `session-capture` twin import the same code, so the bytes this
client signs, hashes and batches are exactly what api-go verifies.

- Source: `sageox-monorepo/packages/sessionstream` at commit `1776751325` (PR #12369, which moved chunk keys to `.jsonl`).
- Do not edit these files here. Change the monorepo package, then re-copy
  every `.go` file and update the commit above.
- The protocol logic belongs in ox-core long term (team rule
  `shared-logic-belongs-in-ox-core`); this copy is the client's interim home.
