
# PRD: GitHub extractor

## Goal

Build the GitHub extractor stage of the alignment feed pipeline. It takes a batch of pre-assembled GitHub event clusters and produces raw facts — structured records that capture meaningful events (decisions, ships, blockers, direction changes) for downstream distillation.

## Input

A JSON array of event clusters, pre-assembled by the data pipeline. Each cluster is one of:

- **pull_request** — includes nested `related_issue` (with comments), `commits` (with messages), and `reviews` (with comment threads). Status is `open`, `merged`, or `closed`.
- **issue** — standalone (no associated PR yet). Includes comment thread.
- **commit** — standalone (not part of a PR). Includes message only.

Relationships are pre-resolved. The extractor does not query the GitHub API.

## Output

A JSON array of raw fact objects:

```json
{
  "headline": "string — one sentence, framed as outcome not GitHub event",
  "summary": "string — 2-3 sentences, specific about modules/endpoints/interfaces touched",
  "rationale": "string — why this choice, what alternatives rejected. 'No rationale captured.' if absent",
  "who": "string — author, optionally '(reviewed by X)' if reviewer shaped outcome",
  "source_type": "github",
  "source_ref": "string — URL of primary GitHub object",
  "timestamp": "string — ISO 8601, most recent meaningful event time"
}
```

Empty array if no meaningful events in batch.

## Core logic

**Cross-object synthesis.** When a cluster has nested objects describing the same event, produce ONE fact that pulls the best signal from each: headline from outcome, rationale from wherever the why lives (often issue or review comments, not PR description), summary combines implementation detail with context. Never produce one fact per GitHub object.

**Signal categories to extract:**
1. Substantive decisions in review threads (not cosmetic nits)
2. New/changed public interfaces, shared config, DB schema, dependencies
3. Constraints and context from issues that the PR omits
4. Status signals: merged = ship, closed without merge = possible direction change, stale with unresolved reviews = potential blocker
5. Implicit decisions: significant changes with no pushback = tacit agreement; first use of a new pattern = precedent

**Skip:** routine refactors with no interface change, bot PRs (unless breaking), test-only changes, metadata-only events (labels, assignments), cosmetic review feedback.

**Uncertainty:** if significance is borderline, include with `[Uncertain significance]` prefix in summary. The distiller decides.

## Prompt

The system prompt is in `github-extractor-prompt.md`. It contains the full extraction instructions, signal categories, skip list, output schema, and a worked example showing cross-object synthesis from a rate-limiting PR.

Implement a function that:
1. Accepts a batch of event clusters (JSON)
2. Constructs the LLM call with the system prompt and the batch as user content
3. Parses the returned JSON array of raw facts
4. Validates each fact against the schema (all fields present, source_type is "github")
5. Returns validated facts

## Constraints

- LLM call should use structured output / JSON mode where available
- Handle batches that exceed context window by splitting into sub-batches and merging results
- Log token usage per batch for cost tracking
- No GitHub API calls — all data arrives pre-assembled
- Extraction is stateless — no memory across batches

## Not in scope

- The data pipeline that assembles clusters (exists separately)
- Exact dedup (happens after extraction)
- Distillation, significance tiers, feed composition (downstream stages)
- Delivery (Slack, web)
