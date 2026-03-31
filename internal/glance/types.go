package glance

import "time"

// MurmurRecord represents a single murmur with its associated file paths.
// Replaces SessionRecord as the unit of work for pre-crime detection.
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

// AuthorSummary groups murmurs by author.
type AuthorSummary struct {
	Name         string         `json:"name"`
	MurmurCount  int            `json:"murmur_count"`
	FilesTouched int            `json:"files_touched"`
	WIPStatus    string         `json:"wip_status,omitempty"` // latest WIP murmur content
	Murmurs      []MurmurRecord `json:"murmurs"`
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
	TotalAuthors    int `json:"total_authors"`
	TotalConflicts  int `json:"total_conflicts"`
	WIPCount        int `json:"wip_count"`
	FileChangeCount int `json:"file_change_count"`
}
