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
//
// summary.json is the STRUCTURAL SKELETON — it answers "what was discussed,
// in what order, and how important was each segment?"
//   - Chapter-level granularity (segments of minutes)
//   - Contains synthesized summaries, importance rankings, topic tags
//   - Top-level categorized facts (decisions, learnings, etc.)
//   - Primary use: L1 progressive disclosure — agents decide which chapter to drill into
//
// annotations.json is ANCHORED EVIDENCE — it answers "where exactly did this
// decision/disagreement/insight occur?"
//   - Moment-level granularity (specific cue ranges)
//   - Contains verbatim-adjacent extractions with VTT timestamps
//   - Primary use: L2 progressive disclosure — agents need specific evidence
//
// # Progressive Disclosure Layers
//
//	L0  DISCUSSIONS.md / distilled-discussions.md  — always loaded (~20-40 tokens/discussion)
//	L1  summary.json chapters                     — on topic match (~500-1000 tokens)
//	L2  annotations.json + keyframes.json index   — need details (~300 tokens/chapter)
//	L3  keyframe image via vision model           — need visual (~1000 tokens/image)
//	L4  VTT cue range                             — need verbatim (<200 tokens/range)
package discussion

// DiscussionSummary represents a server-generated summary.json.
// Facts are FLAT at the top level — there is no agent_summary wrapper.
// Source of truth: packages/transcript-go/schema.go in the monorepo.
type DiscussionSummary struct {
	SchemaVersion    int              `json:"schema_version"`
	RecordingID      string           `json:"recording_id"`
	Title            string           `json:"title"`
	HumanSummary     string           `json:"human_summary"`
	RecordedAt       string           `json:"recorded_at,omitempty"`
	DurationSeconds  float64          `json:"duration_seconds,omitempty"`
	Decisions        []Decision       `json:"decisions,omitempty"`
	ActionItems      []ActionItem     `json:"action_items,omitempty"`
	Requirements     []Requirement    `json:"requirements,omitempty"`
	OpenQuestions    []OpenQuestion   `json:"open_questions,omitempty"`
	Learnings        []Learning       `json:"learnings,omitempty"`
	TechnicalContext TechnicalContext `json:"technical_context,omitempty"`
	Constraints      []string         `json:"constraints,omitempty"`
	NonGoals         []string         `json:"non_goals,omitempty"`
	Topics           []string         `json:"topics,omitempty"`
	Chapters         []Chapter        `json:"chapters,omitempty"`
	HasAnnotations   bool             `json:"has_annotations,omitempty"`
	HasKeyframes     bool             `json:"has_keyframes"`
}

// HasCategorizedFacts returns true when the server pipeline has populated at least
// one top-level fact category. When true, consumers should use these directly and
// skip LLM extraction.
func (s *DiscussionSummary) HasCategorizedFacts() bool {
	return len(s.Decisions) > 0 ||
		len(s.Learnings) > 0 ||
		len(s.OpenQuestions) > 0 ||
		len(s.ActionItems) > 0 ||
		len(s.Requirements) > 0
}

// Decision records a decision made during the discussion.
type Decision struct {
	Description string `json:"description"`
	Owner       string `json:"owner,omitempty"`
	Context     string `json:"context,omitempty"`
}

// Text returns a one-line representation for the LLM-bypass fast path.
func (d Decision) Text() string { return d.Description }

// Requirement records a requirement identified during the discussion.
type Requirement struct {
	Description string `json:"description"`
	Priority    string `json:"priority,omitempty"`
	Source      string `json:"source,omitempty"`
}

// Text returns a one-line representation for the LLM-bypass fast path.
func (r Requirement) Text() string { return r.Description }

// ActionItem records a task identified during the discussion.
type ActionItem struct {
	Description string `json:"description"`
	Assignee    string `json:"assignee,omitempty"`
	DueDate     string `json:"due_date,omitempty"`
}

// Text returns a one-line representation for the LLM-bypass fast path.
func (a ActionItem) Text() string { return a.Description }

// OpenQuestion records an unresolved question from the discussion.
type OpenQuestion struct {
	Question string `json:"question"`
	Context  string `json:"context,omitempty"`
}

// Text returns a one-line representation for the LLM-bypass fast path.
func (q OpenQuestion) Text() string { return q.Question }

// Learning records something the team learned during the discussion.
type Learning struct {
	Description string `json:"description"`
	Source      string `json:"source,omitempty"`
}

// Text returns a one-line representation for the LLM-bypass fast path.
func (l Learning) Text() string { return l.Description }

// TechnicalContext captures the technical landscape discussed.
// Notes holds free-form context that doesn't fit the three sub-categories.
type TechnicalContext struct {
	Technologies []string `json:"technologies,omitempty"`
	Architecture []string `json:"architecture,omitempty"`
	Integrations []string `json:"integrations,omitempty"`
	Notes        []string `json:"notes,omitempty"`
}

// Chapter is a semantic section of a discussion with importance scoring.
// Different from sessionsummary.ChapterSummary which tracks coding session tool usage.
//
// Agents use Importance to decide whether to drill deeper: chapters above the
// high-importance threshold are considered significant.
type Chapter struct {
	ID         string     `json:"id"`               // e.g. "ch-1", "ch-2"
	Title      string     `json:"title"`            // semantic title
	Summary    string     `json:"summary"`          // brief chapter summary
	CueRange   [2]int     `json:"cue_range"`        // [start, end] VTT cue indices
	TimeRange  [2]float64 `json:"time_range"`       // [start, end] seconds from start
	Importance float64    `json:"importance"`       // 0.0-1.0; >0.5 is significant
	Topics     []string   `json:"topics,omitempty"` // topic tags
}

// KeyframesManifest represents keyframes.json — video-only, absent for audio recordings.
// Lets agents decide whether to load actual images based on text descriptions (L2).
type KeyframesManifest struct {
	RecordingID      string     `json:"recording_id"`
	SourceURL        string     `json:"source_url,omitempty"`
	Duration         float64    `json:"duration"`
	KeyframeCount    int        `json:"keyframe_count"`
	TotalBeforeDedup int        `json:"total_before_dedup"`
	Keyframes        []Keyframe `json:"keyframes"`
}

// Keyframe is a single extracted video frame with semantic metadata.
// Description is the key field — it makes visual content searchable and lets agents
// decide whether to load the actual image (L3) or stop at the text description (L2).
type Keyframe struct {
	S3Key            string  `json:"s3_key"`
	TimestampSeconds float64 `json:"timestamp_seconds"`
	ExtractionMethod string  `json:"extraction_method"` // e.g. "scene-change", "periodic"
	SceneChangeScore float64 `json:"scene_change_score,omitempty"`
	TranscriptCue    string  `json:"transcript_cue,omitempty"` // nearby transcript text
	ChapterID        string  `json:"chapter_id,omitempty"`     // links to Chapter.ID
	ContentType      string  `json:"content_type,omitempty"`   // "diagram", "code", "terminal", "slide", "ui", "other"
	Description      string  `json:"description,omitempty"`    // one-line text summary of what's visible
}

// AnnotationsFile represents annotations.json — timestamped evidence from the discussion.
// Each annotation is anchored to a chapter and a VTT cue range, providing
// provenance for decisions, disagreements, and action items.
type AnnotationsFile struct {
	SchemaVersion int          `json:"schema_version"`
	RecordingID   string       `json:"recording_id"`
	GeneratedAt   string       `json:"generated_at"`
	Annotations   []Annotation `json:"annotations"`
}

// Annotation is a structured extraction anchored to a specific moment in the discussion.
// ChapterID links to the relevant Chapter. CueRange marks the VTT cue indices
// for verbatim lookup (L4).
type Annotation struct {
	Type       string   `json:"type"`                 // see Annotation* constants
	Content    string   `json:"content"`              // the annotation text
	ChapterID  string   `json:"chapter_id,omitempty"` // links to Chapter.ID
	CueRange   [2]int   `json:"cue_range"`            // [start, end] VTT cue indices
	Importance float64  `json:"importance,omitempty"` // 0.0-1.0
	Speakers   []string `json:"speakers,omitempty"`   // who spoke
}

// ContentTypes for Keyframe.ContentType filtering.
// Server vision model (sageox-mono analyze_keyframes_vision.go) produces these values.
const (
	ContentTypeDiagram    = "diagram"
	ContentTypeCode       = "code"
	ContentTypeTerminal   = "terminal"
	ContentTypeSlide      = "slide"
	ContentTypeUI         = "ui"
	ContentTypeWireframe  = "wireframe"
	ContentTypeLiveUI     = "live-ui"
	ContentTypeTransition = "transition"
	ContentTypeOther      = "other"
)

// AnnotationTypes for Annotation.Type filtering (kebab-case per AGENTS.md convention).
const (
	AnnotationDecision     = "decision"
	AnnotationActionItem   = "action-item"
	AnnotationDisagreement = "disagreement"
	AnnotationQuestion     = "question"
	AnnotationInsight      = "insight"
	AnnotationLearning     = "learning"
	AnnotationTangent      = "tangent"
	AnnotationConsensus    = "consensus"
)
