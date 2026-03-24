// Package discussion defines types for server-generated discussion artifacts.
//
// # Discussion Directory Layout
//
// Each recorded discussion (audio or video) lives in a directory under
// the team context's discussions/ folder:
//
//	discussions/2026-03-20-1423-person/
//	  ├── metadata.json       # recording identity (title, created_at, user_id)
//	  ├── summary.md          # human-written prose summary from recording UI
//	  ├── transcript.vtt      # WebVTT transcript (speaker-tagged cues)
//	  ├── summary.json        # server-generated: structural skeleton (this package)
//	  ├── annotations.json    # server-generated: timestamped evidence (this package)
//	  └── keyframes.json      # server-generated: video-only visual frames (this package)
//
// Audio recordings produce the same artifacts minus keyframes.json.
//
// # summary.json vs annotations.json
//
// These two files serve different purposes in the progressive disclosure model.
// Understanding the distinction is critical for extending or consuming them.
//
// summary.json is the STRUCTURAL SKELETON — it answers "what was discussed,
// in what order, and how important was each segment?"
//   - Chapter-level granularity (segments of minutes)
//   - Contains synthesized summaries, importance rankings, topic tags
//   - AgentSummary provides pre-categorized facts (decisions, learnings, etc.)
//   - Primary use: L1 progressive disclosure — agents decide which chapter to drill into
//   - Think of it as: the table of contents with ratings
//
// annotations.json is ANCHORED EVIDENCE — it answers "where exactly did this
// decision/disagreement/insight occur, and what was on screen?"
//   - Moment-level granularity (specific cue ranges of seconds)
//   - Contains verbatim-adjacent extractions with VTT timestamps and keyframe links
//   - Primary use: L2 progressive disclosure — agents need specific evidence or provenance
//   - Think of it as: margin notes with page numbers
//
// The same fact (e.g., "use rotating tokens") may appear in BOTH files. This is
// by design: summary.json has it for filtering/categorization (no timestamp needed),
// annotations.json has it for provenance (with the exact cue range). When consuming
// both, prefer AgentSummary as the authoritative categorized source and treat
// annotations as supplementary evidence. Do NOT merge both into fact output —
// the server pipeline already incorporates annotations when building AgentSummary.
//
// # Placement Guide for New Content
//
//   - Synthesis, ranking, or classification → summary.json (Chapter or AgentSummary)
//   - Extraction with a timestamp anchor → annotations.json
//   - Visual frame with description → keyframes.json
//   - Human-written prose → summary.md (not this package)
//
// # Progressive Disclosure Layers
//
//	L0  DISCUSSIONS.md / distilled-discussions.md  — always loaded (~20-40 tokens/discussion)
//	L1  summary.json chapters                     — on topic match (~500-1000 tokens)
//	L2  annotations.json + keyframes.json index   — need details (~300 tokens/chapter)
//	L3  keyframe image via vision model           — need visual (~1000 tokens/image)
//	L4  VTT cue range                             — need verbatim (<200 tokens/range)
package discussion

// DiscussionSummary represents summary.json — the structural skeleton of a discussion.
// Chapters provide semantic sections with importance scoring for progressive disclosure (L1).
// AgentSummary provides pre-categorized facts when the server pipeline has produced them.
type DiscussionSummary struct {
	Chapters     []Chapter     `json:"chapters"`
	AgentSummary *AgentSummary `json:"agent_summary,omitempty"`
}

// AgentSummary contains pre-categorized facts derived by the server summarization pipeline.
// Categories align with the ox distill pipeline's DiscussionFactsPrompt output.
//
// When AgentSummary is present, the ox CLI uses it directly and skips the LLM —
// no token cost, no latency, deterministic output. The server already incorporates
// annotations.json content when building this, so consumers should NOT re-merge
// annotations to avoid duplicates.
type AgentSummary struct {
	Decisions     []string `json:"decisions,omitempty"`
	Learnings     []string `json:"learnings,omitempty"`
	OpenQuestions []string `json:"open_questions,omitempty"`
	ActionItems   []string `json:"action_items,omitempty"`
	KeyContext    []string `json:"key_context,omitempty"`
}

// Chapter is a semantic section of a discussion with importance scoring.
// Different from sessionsummary.ChapterSummary which tracks coding session tool usage.
//
// Agents use Importance to decide whether to drill deeper: chapters above 0.5 are
// considered significant. HasVisual signals that keyframes.json has frames linked
// to this chapter — the agent can check descriptions without loading images.
type Chapter struct {
	ID          string   `json:"id"`                    // e.g. "ch-1", "ch-2"
	Title       string   `json:"title"`                 // semantic title
	Summary     string   `json:"summary,omitempty"`     // brief chapter summary
	StartTime   float64  `json:"start_time"`            // seconds from start
	EndTime     float64  `json:"end_time"`              // seconds from start
	Importance  float64  `json:"importance"`             // 0.0-1.0; >0.5 is significant
	Topics      []string `json:"topics,omitempty"`       // topic tags
	HasVisual   bool     `json:"has_visual,omitempty"`   // true if chapter has keyframes
	VisualTypes []string `json:"visual_types,omitempty"` // e.g. ["diagram", "code"]
}

// KeyframeManifest represents keyframes.json — video-only, absent for audio recordings.
// Lets agents decide whether to load actual images based on text descriptions (L2).
type KeyframeManifest struct {
	Keyframes []Keyframe `json:"keyframes"`
}

// Keyframe is a single extracted video frame with semantic metadata.
// Description is the key field — it makes visual content searchable and lets agents
// decide whether to load the actual image (L3) or stop at the text description (L2).
type Keyframe struct {
	S3Key            string  `json:"s3_key"`
	TimestampSeconds float64 `json:"timestamp_seconds"`
	ExtractionMethod string  `json:"extraction_method"`            // e.g. "scene-change", "periodic"
	SceneChangeScore float64 `json:"scene_change_score,omitempty"`
	TranscriptCue    string  `json:"transcript_cue,omitempty"`     // nearby transcript text
	ChapterID        string  `json:"chapter_id,omitempty"`         // links to Chapter.ID
	Description      string  `json:"description,omitempty"`        // one-line text summary of what's visible
	ContentType      string  `json:"content_type,omitempty"`       // "diagram", "code", "terminal", "slide", "ui", "other"
}

// AnnotationsFile represents annotations.json — timestamped evidence from the discussion.
// Each annotation is anchored to a chapter and optionally to a VTT cue range, providing
// provenance for decisions, disagreements, and action items.
//
// Annotations may overlap with AgentSummary content — both might contain "use rotating
// tokens." This is by design: AgentSummary is for categorization, annotations are for
// provenance (the exact moment and keyframe where it was said).
type AnnotationsFile struct {
	Annotations []Annotation `json:"annotations"`
}

// Annotation is a structured extraction anchored to a specific moment in the discussion.
// ChapterID links to the relevant Chapter. CueStart/CueEnd mark the VTT timestamp range
// for verbatim lookup (L4). Keyframes lists related visual frames by s3_key.
type Annotation struct {
	Type      string   `json:"type"`                 // see Annotation* constants
	Content   string   `json:"content"`              // the annotation text
	ChapterID string   `json:"chapter_id,omitempty"` // links to Chapter.ID
	CueStart  float64  `json:"cue_start,omitempty"`  // VTT timestamp start (seconds)
	CueEnd    float64  `json:"cue_end,omitempty"`    // VTT timestamp end (seconds)
	Keyframes []string `json:"keyframes,omitempty"`  // s3_keys of related keyframes
}

// ContentTypes for Keyframe.ContentType filtering.
const (
	ContentTypeDiagram  = "diagram"
	ContentTypeCode     = "code"
	ContentTypeTerminal = "terminal"
	ContentTypeSlide    = "slide"
	ContentTypeUI       = "ui"
	ContentTypeOther    = "other"
)

// AnnotationTypes for Annotation.Type filtering.
const (
	AnnotationDecision     = "decision"
	AnnotationActionItem   = "action-item"
	AnnotationDisagreement = "disagreement"
	AnnotationInsight      = "insight"
	AnnotationLearning     = "learning"      // bridges server annotation → distill "learnings" category
	AnnotationOpenQuestion = "open-question"  // bridges server annotation → distill "open_questions" category
)
