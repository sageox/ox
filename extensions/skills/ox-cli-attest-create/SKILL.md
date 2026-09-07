---
name: ox-cli-attest-create
description: >-
  Turn a demonstrated red/green Attest run pair into an honest portable proof.
  Use when a user asks to attest, prove, stamp, record, publish, or explain a
  BDD capability's evidence. Inspect `ox attest proof <capability>` and the
  run artifacts first; use `ox attest record` only after a real red failure and
  a green recovery demonstrate the customer claim.
---

<!-- This opt-in skill protects the meaning of an attestation. The CLI owns
     record validation and freshness; this layer helps an AI coworker choose
     evidence honestly before invoking it. -->

## Create evidence, not a decorative stamp

1. Start with `ox attest proof <capability>` and read the customer claim,
   current verdict, and any prior evidence.
2. Make the promised behavior fail in a way that lands on the claim step; save
   the unedited failure text and its run ID.
3. Restore the behavior, run the same scenario green, and retain that run ID.
4. Describe the break in the language of the customer's promise. Name only
   product surfaces the run actually exercised.
5. Use `ox attest record` with both run IDs, the verbatim red failure, the
   failed step, and the exercised surfaces. If the evidence is ambiguous or
   incomplete, record that honestly rather than forcing a clean verdict.
6. Run `ox attest status` and `ox attest check --json` afterward. Publish only
   evidence that remains current and explain any stale or unknown result.
