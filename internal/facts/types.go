package facts

// Fact is the unified fact record across all sources (github, discussion, observation).
// All fact JSONL files use this schema for data lines.
//
// SourceRef is the stable canonical identifier (path or URL).
// SourceURL is the human-clickable URL for drill-down (added in PR for #476).
// SourceTitle is a short human label for citations (e.g., a discussion or PR title).
// SourceURL and SourceTitle are populated at extraction time when available;
// older fact files without these fields are still valid — the citation pipeline
// degrades gracefully (link omitted, label derived from other fields).
type Fact struct {
	Headline    string `json:"headline"`
	Summary     string `json:"summary,omitempty"`
	Rationale   string `json:"rationale,omitempty"`
	Who         string `json:"who,omitempty"`
	SourceType  string `json:"source_type"`
	SourceRef   string `json:"source_ref,omitempty"`
	SourceURL   string `json:"source_url,omitempty"`
	SourceTitle string `json:"source_title,omitempty"`
	Timestamp   string `json:"timestamp"`
	Category    string `json:"category,omitempty"`
}

// FileHeader is the first line of a v2 fact JSONL file.
type FileHeader struct {
	Meta FileMeta `json:"_meta"`
}

// FileMeta contains file-level metadata for a fact JSONL file.
type FileMeta struct {
	SchemaVersion string `json:"schema_version"`
	SourceType    string `json:"source_type"`
	RecordedAt    string `json:"recorded_at"`
	SourceHash    string `json:"source_hash,omitempty"`
	QuerySince    string `json:"query_since,omitempty"`
	QueryUntil    string `json:"query_until,omitempty"`
}

// Source type constants.
const (
	SourceGitHub      = "github"
	SourceDiscussion  = "discussion"
	SourceObservation = "observation"
	SourceSession     = "session"
)

// Category constants.
const (
	CategoryDecision        = "decision"
	CategoryLearning        = "learning"
	CategoryOpenQuestion    = "open_question"
	CategoryActionItem      = "action_item"
	CategoryContext         = "context"
	CategoryShip            = "ship"
	CategoryBlocker         = "blocker"
	CategoryDirectionChange = "direction_change"
)

// SchemaVersion is the current schema version for new fact files.
const SchemaVersion = "2"
