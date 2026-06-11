# Discussion Artifacts: Progressive Disclosure Model

## Overview

Recorded discussions (audio and video) produce artifacts that AI coworkers consume through progressive disclosure — loading more detail only when relevant. This spec defines what each artifact contains, when to use it, and how to extend it.

## Discussion Directory Layout

```text
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
- `has_keyframes` / `has_annotations` flags signaling deeper layers exist
- Top-level categorized facts (decisions, learnings, action_items, open_questions, requirements)
- Technical context, constraints, non-goals

**When to read it:** L1 — agent has identified a relevant discussion and needs to decide which segment matters.

**Analogy:** Table of contents with ratings.

### annotations.json — Anchored Evidence

**Purpose:** "Where exactly did this decision/insight occur, and what was on screen?"

**Granularity:** Moment-level (specific VTT cue ranges of seconds)

**Contains:**
- Annotations with type classification (decision, action-item, disagreement, insight, learning, question, tangent, consensus)
- VTT cue range anchors (`cue_range: [start, end]`) for verbatim transcript lookup
- `chapter_id` linking back to summary.json chapters
- `importance` scoring and `speakers` attribution

**When to read it:** L2 — agent needs specific evidence, provenance, or the keyframe that was on screen during a decision.

**Analogy:** Margin notes with page numbers.

### Why Content Overlaps

The same fact (e.g., "use rotating tokens") may appear in both files:

| In summary.json | In annotations.json |
|---|---|
| `decisions: [{description: "use rotating tokens"}]` | `{type: "decision", content: "use rotating tokens", cue_range: [42, 45]}` |
| Categorized, no timestamp | Same content + temporal anchor + speaker attribution |

This is by design. summary.json provides the "what" for filtering. annotations.json provides the "where" for evidence.

## Consumption Rules

### When Categorized Facts Exist

Use top-level categorized facts (decisions, learnings, etc.) as the authoritative source. Do NOT also merge annotations — the server already incorporates annotations when building categorized facts. Merging both produces duplicates.

```text
HasCategorizedFacts()?
  YES → use top-level categories directly, skip annotations
  NO  → fall back to LLM extraction from transcript
```

### Key Context Derivation

Key context is derived from `TechnicalContext.Notes` + `Constraints` + `NonGoals` + high-importance chapter summaries. There is no explicit `KeyContext` field — it is assembled at extraction time.

### Importance Threshold

Chapters with `importance <= 0.5` are excluded from fact extraction. Only chapters above 0.5 contribute learnings and key context in the fallback path.

## Progressive Disclosure Layers

```text
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
| New fact category | Add to `DiscussionSummary` struct AND `DiscussionFactsPrompt` categories |
| New annotation type | Add constant to `pkg/discussion/types.go`, update `categorizeAnnotations()` in `distill_discussions.go` |

## Keyframe Enrichment

Four semantic fields on each `Keyframe` in `keyframes.json` enable AI coworkers to understand visual content without fetching images.

### Server Pipeline (sageox-mono)

The server populates enrichment fields across two processing stages:

**During keyframe extraction** (import-video pipeline, before summarization):
- **`transcript_cue`**: Correlated from WebVTT during frame extraction. Speaker-prefixed text from the nearest VTT cue. Empty if no transcript available at extraction time.
- **`content_type`**: Vision model classification via `AnalyzeKeyframesWithVision()`. Values: `wireframe`, `live-ui`, `diagram`, `code`, `slide`, `terminal`, `ui`, `transition`, `other`. Non-fatal — empty if vision analysis fails or is skipped.
- **`description`**: Vision model one-line summary of visible content. Same non-fatal behavior as `content_type`.

Model: `ModelForVision()` from `packages/llm-go` (Sonnet 4 via Bedrock). Batches of 6 frames.

**Not yet populated** (requires post-summarization step):
- **`chapter_id`**: Links keyframe to a `Chapter.ID` from `summary.json`. Cannot be populated at keyframe commit time because chapters are generated later by the summarization workflow. Requires a post-summary enrichment step that correlates `timestamp_seconds` against chapter `time_range`.

### Content Type Values

Server vision model produces these values (defined in `pkg/discussion/types.go`):

| Constant | Value | Description |
|----------|-------|-------------|
| `ContentTypeDiagram` | `diagram` | Architecture, flow, or data diagrams |
| `ContentTypeCode` | `code` | Source code or configuration |
| `ContentTypeTerminal` | `terminal` | CLI output, shell sessions |
| `ContentTypeSlide` | `slide` | Presentation slides |
| `ContentTypeUI` | `ui` | Generic UI (catch-all) |
| `ContentTypeWireframe` | `wireframe` | UI mockups, design prototypes |
| `ContentTypeLiveUI` | `live-ui` | Live application screenshots |
| `ContentTypeTransition` | `transition` | Loading screens, blank states (low value) |
| `ContentTypeOther` | `other` | Unclassified |

### CLI Helpers

| Function | Purpose |
|----------|---------|
| `KeyframesForChapter(manifest, chapterID)` | Filter keyframes by chapter, sorted by timestamp |
| `VisualTypes(manifest, chapterID)` | Unique content types for a chapter |
| `AllVisualTypes(manifest)` | Unique content types across all keyframes |

### Backward Compatibility

All four enrichment fields are `omitempty`. CLI consumers gate on non-empty values (e.g., `kf.Description != ""`) so pre-enrichment manifests work without changes.

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
