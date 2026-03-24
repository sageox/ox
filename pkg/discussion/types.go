package discussion

// DiscussionSummary represents a server-generated summary.json for a recorded discussion.
// Each discussion directory may contain this alongside metadata.json, summary.md, transcript.vtt.
// Consumed by ox CLI for progressive disclosure (L1 in the 5-layer model).
type DiscussionSummary struct {
	Chapters     []Chapter     `json:"chapters"`
	AgentSummary *AgentSummary `json:"agent_summary,omitempty"`
}

// AgentSummary contains pre-categorized facts from the server summarization pipeline.
// Categories align with DiscussionFactsPrompt output: decisions, learnings,
// open_questions, action_items, key_context.
type AgentSummary struct {
	Decisions     []string `json:"decisions,omitempty"`
	Learnings     []string `json:"learnings,omitempty"`
	OpenQuestions []string `json:"open_questions,omitempty"`
	ActionItems   []string `json:"action_items,omitempty"`
	KeyContext    []string `json:"key_context,omitempty"`
}

// Chapter is a semantic section of a discussion with importance scoring.
// Different from sessionsummary.ChapterSummary which tracks coding session tool usage.
type Chapter struct {
	ID          string   `json:"id"`                    // e.g. "ch-1", "ch-2"
	Title       string   `json:"title"`                 // semantic title
	Summary     string   `json:"summary,omitempty"`     // brief chapter summary
	StartTime   float64  `json:"start_time"`            // seconds from start
	EndTime     float64  `json:"end_time"`              // seconds from start
	Importance  float64  `json:"importance"`             // 0.0-1.0
	Topics      []string `json:"topics,omitempty"`       // topic tags
	HasVisual   bool     `json:"has_visual,omitempty"`   // true if chapter has keyframes
	VisualTypes []string `json:"visual_types,omitempty"` // e.g. ["diagram", "code"]
}

// KeyframeManifest represents keyframes.json for a discussion directory.
// Consumed by ox CLI for L2 progressive disclosure — lets agents decide
// whether to load actual images based on text descriptions.
type KeyframeManifest struct {
	Keyframes []Keyframe `json:"keyframes"`
}

// Keyframe is a single extracted video frame with semantic metadata.
// The Description field is the key unlock — it makes visual content
// searchable and lets agents decide whether to load the actual image.
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

// AnnotationsFile represents annotations.json for a discussion directory.
// Contains structured decisions, disagreements, and action items with chapter links.
// Consumed by ox CLI for L2 progressive disclosure.
type AnnotationsFile struct {
	Annotations []Annotation `json:"annotations"`
}

// Annotation is a structured annotation anchored to a discussion chapter.
type Annotation struct {
	Type      string   `json:"type"`                 // "decision", "action-item", "disagreement", "insight", "learning", "open-question"
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
