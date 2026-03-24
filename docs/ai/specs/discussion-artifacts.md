<!-- doc-audience: ai -->
# Discussion Artifacts: Progressive Disclosure Model

## Overview

Recorded discussions (audio and video) produce artifacts that AI coworkers consume through progressive disclosure — loading more detail only when relevant. This spec defines what each artifact contains, when to use it, and how to extend it.

## Discussion Directory Layout

```
discussions/2026-03-20-1423-person/
  metadata.json         # recording identity (always present)
  summary.md            # human-written prose summary (always present)
  transcript.vtt        # WebVTT transcript with speaker tags (optional)
  summary.json          # server-generated structural skeleton (optional)
  annotations.json      # server-generated timestamped evidence (optional)
  keyframes.json        # server-generated video frames (video-only, optional)
```

Audio recordings produce the same artifacts minus `keyframes.json`.

## The Two Server-Generated Files

### summary.json — Structural Skeleton

**Purpose:** "What was discussed, in what order, how important?"

**Granularity:** Chapter-level (segments of minutes)

**Contains:**
- Chapters with semantic titles, importance scores (0-1), topic tags
- `has_visual` / `visual_types` flags signaling visual content exists
- `AgentSummary` — pre-categorized facts (decisions, learnings, action_items, open_questions, key_context)

**When to read it:** L1 — agent has identified a relevant discussion and needs to decide which segment matters.

**Analogy:** Table of contents with ratings.

### annotations.json — Anchored Evidence

**Purpose:** "Where exactly did this decision/insight occur, and what was on screen?"

**Granularity:** Moment-level (specific VTT cue ranges of seconds)

**Contains:**
- Annotations with type classification (decision, action-item, disagreement, insight, learning, open-question)
- VTT timestamp anchors (`cue_start`, `cue_end`) for verbatim transcript lookup
- `chapter_id` linking back to summary.json chapters
- `keyframes` linking to keyframes.json entries by s3_key

**When to read it:** L2 — agent needs specific evidence, provenance, or the keyframe that was on screen during a decision.

**Analogy:** Margin notes with page numbers.

### Why Content Overlaps

The same fact (e.g., "use rotating tokens") may appear in both files:

| In summary.json | In annotations.json |
|---|---|
| `agent_summary.decisions: ["use rotating tokens"]` | `{type: "decision", content: "use rotating tokens", cue_start: 462.5}` |
| Categorized, no timestamp | Same content + temporal anchor + keyframe links |

This is by design. summary.json provides the "what" for filtering. annotations.json provides the "where" for evidence.

## Consumption Rules

### When AgentSummary Exists

Use `AgentSummary` as the authoritative source. Do NOT also merge annotations — the server already incorporates annotations when building `AgentSummary`. Merging both produces duplicates.

```
AgentSummary present?
  YES → use its categories directly, skip annotations
  NO  → fall back to annotations + chapter-based extraction
```

### Chapter-Based Key Context

When `AgentSummary.KeyContext` is non-empty, do not append chapter summaries to key context — the server already curated what matters. Only derive key context from chapters when `AgentSummary` is absent or its `KeyContext` is empty.

### Importance Threshold

Chapters with `importance <= 0.5` are excluded from fact extraction. Only chapters above 0.5 contribute learnings and key context in the fallback path.

## Progressive Disclosure Layers

```
Layer  Artifact                       Tokens/discussion  When loaded
-----  -----------------------------  -----------------  ----------------
L0     DISCUSSIONS.md / distilled     20-40              Always (100%)
L1     summary.json chapters          500-1000           Topic match (~10-20%)
L2     annotations.json + keyframes   ~300/chapter       Need details (~5-10%)
L3     keyframe image (vision)        ~1000/image        Need visual (~1-3%)
L4     VTT cue range                  ~200/range         Need verbatim (<1%)
```

Agents decide at each layer whether to go deeper. Most discussions stop at L0 or L1.

## Placement Guide for New Content

| What you're adding | Where it goes |
|---|---|
| Synthesis, ranking, classification | `summary.json` (Chapter or AgentSummary) |
| Extraction with timestamp anchor | `annotations.json` |
| Visual frame with description | `keyframes.json` |
| Human-written prose | `summary.md` (not server-generated) |
| New fact category | Add to `AgentSummary` struct AND `DiscussionFactsPrompt` categories |
| New annotation type | Add constant to `pkg/discussion/types.go`, update `categorizeAnnotations()` in `distill_discussions.go` |

## Code Locations

| Component | File |
|---|---|
| Types + package doc | `pkg/discussion/types.go` |
| Loaders (LoadSummary, LoadKeyframes, LoadAnnotations) | `pkg/discussion/loader.go` |
| Fact extraction (LLM bypass) | `cmd/ox/distill_discussions.go:extractFactsFromSummaryJSON` |
| Annotation categorization | `cmd/ox/distill_discussions.go:categorizeAnnotations` |
| Discussion listing with visual tags | `cmd/ox/agent_team_ctx.go:listRecentDiscussions` |
| Sparse checkout includes | `internal/manifest/fallback.go` |
| Query result visual fields | `internal/api/query.go:QueryResult` |
