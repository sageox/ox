package glance

import "time"

// SessionRecord represents a single AI coworker session with its changed files.
type SessionRecord struct {
	Name      string    `json:"name"`
	User      string    `json:"user"`
	Title     string    `json:"title"`
	Summary   string    `json:"summary,omitempty"`
	Time      time.Time `json:"time"`
	TimeAgo   string    `json:"time_ago,omitempty"`
	Files     []string  `json:"files"`
	Recording bool      `json:"recording,omitempty"`
}

// AuthorSummary groups sessions by author.
type AuthorSummary struct {
	Name         string          `json:"name"`
	SessionCount int             `json:"session_count"`
	FilesTouched int             `json:"files_touched"`
	Sessions     []SessionRecord `json:"sessions"`
}

// FileOverlap represents a file touched by multiple authors.
type FileOverlap struct {
	FilePath string              `json:"file"`
	Authors  map[string][]string `json:"authors"` // author → []session names
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
	TotalSessions     int `json:"total_sessions"`
	TotalAuthors      int `json:"total_authors"`
	TotalConflicts    int `json:"total_conflicts"`
	SkippedDehydrated int `json:"skipped_dehydrated"`
}
