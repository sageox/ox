// Package domain defines the core value types the dashboard operates on.
// These types are intentionally thin — no behavior, no I/O — so every layer
// of the TUI can import them without creating dependency cycles.
package domain

import (
	"time"

	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/daemon/agentwork"
	"github.com/sageox/ox/internal/session"
)

// InspectorTargetKind identifies what kind of entity the inspector pane is showing.
type InspectorTargetKind int

const (
	TargetNone           InspectorTargetKind = iota
	TargetSession                            // a recorded agent session
	TargetWorkspace                          // a daemon-synced workspace (ledger or team context)
	TargetIssue                              // a DaemonIssue flagged by the daemon
	TargetMurmur                             // a WIP broadcast from an AI coworker
	TargetTeamDiscussion                     // a recorded team discussion document
	TargetAuth                               // authentication status and token info
	TargetCodeDB                             // code index (codedb) statistics
	TargetSyncHealth                         // daemon sync health overview
	TargetSOUL                               // SOUL.md team identity document
	TargetInstance                           // a daemon.InstanceInfo
	TargetDaemonErrors                       // []daemon.StoredError
	TargetAgentWork                          // *agentwork.AgentWorkStatus
	TargetCallers                            // []daemon.CallerInfo
	TargetTeamContext                        // *domain.TeamContextEntry
	TargetWhisperHistory                     // []WhisperHistoryEntry
)

// InspectorTarget is a tagged union of the entities the inspector pane can display.
// Only the field matching Kind is non-nil.
type InspectorTarget struct {
	Kind           InspectorTargetKind
	Session        *session.SessionInfo
	Workspace      *daemon.WorkspaceSyncStatus
	Issue          *daemon.DaemonIssue
	Murmur         *MurmurEntry
	Discussion     *TeamDiscussion
	Auth           *daemon.StatusData         // full status, auth fields used by renderer
	CodeDB         *daemon.CodeDBStats        // code index statistics
	SyncHealth     *daemon.StatusData         // full status, workspaces used by renderer
	SOUL           *SOULDocument              // SOUL.md content for a team context
	Instance       *daemon.InstanceInfo       // for TargetInstance
	StoredErrors   []daemon.StoredError       // for TargetDaemonErrors
	AgentWork      *agentwork.AgentWorkStatus // for TargetAgentWork
	Callers        []daemon.CallerInfo        // for TargetCallers
	TeamContext    *TeamContextEntry          // for TargetTeamContext
	WhisperHistory []WhisperHistoryEntry      // for TargetWhisperHistory
}

// WhisperHistoryEntry is a single whisper delivered to an AI coworker.
// Populated from the daemon's WhisperHistory IPC endpoint.
type WhisperHistoryEntry struct {
	AgentID   string
	Topic     string
	Content   string
	Source    string // source of the whisper (e.g. "murmur-nudge", "activity-summary")
	CreatedAt time.Time
	Delivered bool // true when the agent has read past this entry's cursor
}

// TeamContextEntry holds metadata about one synced team context workspace.
// Loaded daemon-independently by reading the filesystem directly.
type TeamContextEntry struct {
	TeamName    string
	TeamSlug    string
	Path        string
	SOULPreview string // first ~300 bytes of SOUL.md
	MemoryCount int    // number of .md files in memory/
	DocsCount   int    // number of .md files in docs/
}

// SOULDocument holds the raw markdown content of a team's SOUL.md file.
type SOULDocument struct {
	TeamName string
	TeamSlug string
	Path     string
	Content  string
}

// MurmurEntry is a WIP broadcast emitted by an AI coworker during an active session.
type MurmurEntry struct {
	AgentID   string
	Author    string
	Topic     string
	Content   string
	Timestamp time.Time
}

// TeamDiscussion is a recorded human or AI discussion document stored in the team context repo.
type TeamDiscussion struct {
	Title   string
	Path    string
	Preview string
	ModTime time.Time
}

// NavNodeKind identifies the semantic role of a node in the navigation tree.
type NavNodeKind int

const (
	NavNodeSection         NavNodeKind = iota // collapsible heading row
	NavNodeLedger                             // the project ledger workspace
	NavNodeSession                            // a single recorded session
	NavNodeWorkspace                          // a synced workspace (team context, etc.)
	NavNodeIssue                              // a daemon-flagged issue
	NavNodeCodeIndex                          // codedb index status
	NavNodeMurmur                             // a murmur broadcast
	NavNodeDiscussion                         // a team discussion document
	NavNodeHint                               // non-interactive hint shown when a section is empty
	NavNodeAuth                               // authentication status node
	NavNodeSyncHealth                         // sync health summary node
	NavNodeSOUL                               // SOUL.md team identity node
	NavNodeAICoworker                         // active AI coworker instance node
	NavNodeDaemon                             // daemon health section header
	NavNodeDaemonErrors                       // stored errors sub-node
	NavNodeAgentWork                          // agent work queue sub-node
	NavNodeCallers                            // connected callers sub-node
	NavNodeTeamContext                        // team context entry node
	NavNodeWhisper                            // a whisper history node
	NavNodeTeamFeedSection                    // combined murmurs + discussions feed section
)

// NavNode represents a single row in the left-hand navigation tree pane.
type NavNode struct {
	ID         string
	Kind       NavNodeKind
	Label      string
	Depth      int              // indentation level (0 = root)
	Expandable bool             // whether the node has children that can be toggled
	Expanded   bool             // current expansion state
	Target     *InspectorTarget // non-nil when selecting this node should update the inspector
}

// MurmurTopicFilter controls which murmur topics are shown in the timeline/nav.
// Empty string means "all topics".
type MurmurTopicFilter string

const (
	MurmurFilterAll      MurmurTopicFilter = ""
	MurmurFilterWIP      MurmurTopicFilter = "wip"
	MurmurFilterBlocked  MurmurTopicFilter = "blocked"
	MurmurFilterDecision MurmurTopicFilter = "decision"
	MurmurFilterReview   MurmurTopicFilter = "review"
)

// AllMurmurFilters is the ordered list used to cycle through filter tabs.
var AllMurmurFilters = []MurmurTopicFilter{
	MurmurFilterAll,
	MurmurFilterWIP,
	MurmurFilterBlocked,
	MurmurFilterDecision,
	MurmurFilterReview,
}

// TimelineEntryKind categorizes a row in the timeline pane.
type TimelineEntryKind int

const (
	TimelineSync    TimelineEntryKind = iota // a ledger/workspace sync event
	TimelineSession                          // a recorded agent session
	TimelineAgent                            // an agent lifecycle event (start/stop)
	TimelineMurmur                           // a murmur broadcast event
	TimelineIssue                            // a daemon issue first seen / resolved
)

// TimelineEntry is a single row in the center timeline pane, ordered by timestamp descending.
type TimelineEntry struct {
	ID        string
	Kind      TimelineEntryKind
	Actor     string // human or AI coworker that generated the event
	Summary   string // one-line description shown in the list
	Timestamp time.Time
	Target    *InspectorTarget // non-nil when selecting this entry updates the inspector
}

// HealthLevel maps to the visual indicator shown in the status bar and nav tree.
type HealthLevel int

const (
	HealthOK      HealthLevel = iota // everything is green
	HealthWarn                       // potential problem, not blocking
	HealthError                      // blocking issue requiring attention
	HealthUnknown                    // data not yet available
)
