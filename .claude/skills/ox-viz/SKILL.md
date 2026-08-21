---
name: ox-viz
description: >-
  Choose review-friendly visual explanations with ox viz. Use when creating or
  updating a PR description, especially for architecture, lifecycle, data flow,
  before/after, or multi-component changes; also use for plans, docs, reports,
  and design notes. Start a material PR description with `ox viz pr`; ask a
  known reviewer question through `ox viz pr --intent "<question>" --json`.
---

<!-- Thin by design. The portable behavior lives in the visualization-guidance
     floor of `ox agent prime` and the live `ox viz pr` output, which reach every
     AI coworker. This skill adds only native activation ergonomics. Do not grow
     this body into a second catalog or authoring guide. -->

For PR work, run `ox viz pr` before drafting. For a reviewer question, run
`ox viz pr --intent "<question>" --json`; apply the selected result's
`guidance`, then pull its recipe with `ox viz <id>`. For all other artifacts,
use `ox viz suggest "<intent>"` and do the same.
