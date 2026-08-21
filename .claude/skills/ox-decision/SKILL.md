---
name: ox-decision
description: >-
  Consult team context BEFORE creating or editing a Decision Record (ADR, DDR,
  architecture/design decision doc). Auto-fire when the user asks to "write an
  ADR", "add a decision record", "amend/update ADR-NNN", cites a DR by number,
  or edits a file under docs/adr/ or docs/decisions/. Run `ox decision enrich
  --topic "<subject>"` before drafting a new DR and `ox decision enrich --file
  <dr.md>` before editing an existing one — related decisions, numbering,
  conventions, drift, and ready-to-paste citations at zero LLM cost. Follow the
  returned `guidance`.
---

<!-- Thin by design. The authoritative DR contract — consult-before-drafting,
     the crediting rules (paste SOURCE refs verbatim, never compose by hand,
     SageOx credit subtle and capped), amendment semantics, and ref
     verification — lives in the <decision-record-guidance> block of
     `ox agent prime` (Layer-1 floor) and in the `guidance` field of
     `ox decision enrich` JSON, which reach every primed AI coworker.
     This native skill adds discovery and activation ergonomics; it
     duplicates no behavior. Do not grow this body. -->

## Use when

The task creates, edits, amends, or cites a Decision Record.

## Do

1. New DR: `ox decision enrich --topic "<subject>"` — BEFORE drafting.
2. Existing DR: `ox decision enrich --file <path>` — BEFORE editing, and again
   after your edit (it re-verifies every ref).
3. Follow the `guidance` string in the JSON output — it carries the corpus
   conventions, crediting contract, and verification rule for this repo.

DR full text is searchable via `ox code search "<terms>"`.
