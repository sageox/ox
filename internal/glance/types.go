package glance

import "time"

// SessionRecord represents a session with its associated file paths.
// Extracted from raw.jsonl tool calls (Edit/Write/MultiEdit) and/or summary.json.
type SessionRecord struct {
	Name      string    `json:"name"`
	User      string    `json:"user"`
	Time      time.Time `json:"time"`
	TimeAgo   string    `json:"time_ago,omitempty"`
	Title     string    `json:"title"`
	Summary   string    `json:"summary,omitempty"`
	Files     []string  `json:"files,omitempty"`
	Recording bool      `json:"recording,omitempty"`
}

// MurmurRecord represents a single murmur with its associated file paths.
// Two flavors exist:
//   - topic="wip": AI coworker intent signal, free-text content, no Files
//   - topic="file-changes": daemon filesystem observation, structured content, has Files+Branch+Worktree
type MurmurRecord struct {
	ID         string    `json:"id"`
	User       string    `json:"user"`       // PrincipalID
	AgentID    string    `json:"agent_id"`
	Topic      string    `json:"topic"`
	Time       time.Time `json:"time"`
	TimeAgo    string    `json:"time_ago,omitempty"`
	Content    string    `json:"content"`
	Files      []string  `json:"files,omitempty"` // from Metadata["files"] (file-changes only)
	Branch     string    `json:"branch,omitempty"` // from Metadata["branch"] (file-changes only)
	Importance string    `json:"importance"`
}

// AuthorSummary groups murmurs and sessions by author.
type AuthorSummary struct {
	Name         string          `json:"name"`
	MurmurCount  int             `json:"murmur_count"`
	SessionCount int             `json:"session_count,omitempty"`
	FilesTouched int             `json:"files_touched"`
	WIPStatus    string          `json:"wip_status,omitempty"` // latest WIP murmur content
	Murmurs      []MurmurRecord  `json:"murmurs"`
	Sessions     []SessionRecord `json:"sessions,omitempty"`
}

// FileOverlap represents a file touched by multiple authors.
type FileOverlap struct {
	FilePath string              `json:"file"`
	Authors  map[string][]string `json:"authors"` // author → []murmur IDs
}

// ConflictReport is the internal result of conflict detection.
type ConflictReport struct {
	Overlaps     []FileOverlap
	ByAuthorPair map[string]int // "alice|bob" → overlap count
	TotalFiles   int
}

// OverlapPair is a serializable author-pair overlap entry.
type OverlapPair struct {
	Pair        [2]string `json:"pair"`
	SharedFiles int       `json:"shared_files"`
}

// Action is a concrete recommendation the user should act on.
type Action struct {
	Text   string   `json:"text"`   // what to do
	Risk   string   `json:"risk"`   // high, medium, low
	Files  []string `json:"files"`  // affected files
	People []string `json:"people"` // people to coordinate with
}

// ActivityData is the full JSON output.
type ActivityData struct {
	Actions   []Action        `json:"actions,omitempty"`
	Headline  string          `json:"headline"`
	Guidance  string          `json:"guidance,omitempty"`
	Since     time.Time       `json:"since"`
	Until     time.Time       `json:"until"`
	Repo      string          `json:"repo"`
	Authors   []AuthorSummary `json:"authors"`
	Conflicts []FileOverlap   `json:"conflicts"`
	Overlap   []OverlapPair   `json:"overlap_matrix"`
	Stats     Stats           `json:"stats"`
	Patterns  []Pattern       `json:"patterns,omitempty"`
	Velocity  []VelocityPoint `json:"velocity,omitempty"`
}

// Stats contains summary statistics.
type Stats struct {
	TotalMurmurs    int `json:"total_murmurs"`
	TotalSessions   int `json:"total_sessions,omitempty"`
	TotalAuthors    int `json:"total_authors"`
	TotalConflicts  int `json:"total_conflicts"`
	WIPCount        int `json:"wip_count"`
	FileChangeCount int `json:"file_change_count"`
}
