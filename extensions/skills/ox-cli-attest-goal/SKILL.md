---
name: ox-cli-attest-goal
description: >-
  Pursue a customer capability to proven, Attest-backed BDD: author it from the
  customer's journey, drive the acceptance run to green, then hand to
  ox-cli-attest-create to mint the honest red/green proof. Use when a user asks to
  add, improve, prove, or review a BDD/customer capability, or mentions customer
  flow, acceptance criteria, Gherkin, Rules, or a capability that needs proof.
  Start from the desired journey, not the current implementation; use
  `ox attest status --json` to place it in the proof ladder.
---

<!-- Opt-in judgment skill (thickness allowed, like ox-cli-plan / ox-cli-session-review):
     it carries customer-journey product judgment the CLI cannot, then routes
     back to the Attest verbs. The attestation CLI stays the portable source of
     truth for the corpus and proof state. -->

## Customer journey before mechanics

1. State the customer's goal, starting point, natural action, feedback, and
   continuation or recovery.
2. Preserve an established journey. Do not silently redesign it to make a
   scenario easier to automate. If the journey is unclear, name the smallest
   proposed flow and the product decision it needs.
3. Write the `Rule:` as a customer-recognizable promise — one coherent promise
   per block. Write scenarios in observable language: what customers can do,
   see, retain, and recover from. No routes, status codes, database rows,
   selectors, or validation regexes in Gherkin.
4. Cover the happy path plus consequential failure, permission, and continuity
   cases. Derive component/API/concurrency tests separately for the mechanics
   that keep the promise true.

## Place it in the ladder

1. `ox attest status --json` — read the capability ladder and where this promise
   sits (untested → unproven → stamped → attested).
2. `ox attest proof <capability>` — read the current claim, verdict, and any
   prior evidence before touching the tree.

## Drive the capability to green

1. Compile and run the project's existing acceptance workflow. A capability is
   NOT proven until a terminal (`finalized`) run's evidence supports it — confirm
   with `ox attest results`.
2. When a scenario is red, close the gap in the PRODUCT, not by loosening the
   Gherkin. The promise is the fixed point; the implementation moves to meet it.
3. Repeat until the scenarios naming the promise pass on a clean tree.

## Prove, check, hand off

1. Mint the durable red/green proof with the sibling **ox-cli-attest-create**
   playbook (`ox attest record`). The honest verdict — clean vs ambiguous — is
   its job, not this one's.
2. Run `ox attest check --json` before handoff to catch proof the working diff
   invalidated.
3. Publish only evidence that stays current; explain any stale or unknown result
   rather than papering over it.
