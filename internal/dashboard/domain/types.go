// Package domain defines the core value types the dashboard operates on.
// These types are intentionally thin — no behaviour, no I/O — so every layer
// of the TUI can import them without creating dependency cycles.
package domain

import (
	"time"

	"github.com/sageox/ox/internal/daemon"
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
)

// InspectorTarget is a tagged union of the entities the inspector pane can display.
// Only the field matching Kind is non-nil.
type InspectorTarget struct {
	Kind       InspectorTargetKind
	Session    *session.SessionInfo
	Workspace  *daemon.WorkspaceSyncStatus
	Issue      *daemon.DaemonIssue
	Murmur     *MurmurEntry
	Discussion *TeamDiscussion
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
	NavNodeSection    NavNodeKind = iota // collapsible heading row
	NavNodeLedger                        // the project ledger workspace
	NavNodeSession                       // a single recorded session
	NavNodeWorkspace                     // a synced workspace (team context, etc.)
	NavNodeIssue                         // a daemon-flagged issue
	NavNodeCodeIndex                     // codedb index status
	NavNodeMurmur                        // a murmur broadcast
	NavNodeDiscussion                    // a team discussion document
	NavNodeHint                          // non-interactive hint shown when a section is empty
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

// TimelineEntryKind categorises a row in the timeline pane.
type TimelineEntryKind int

const (
	TimelineSync    TimelineEntryKind = iota // a ledger/workspace sync event
	TimelineSession                          // a recorded agent session
	TimelineAgent                            // an agent lifecycle event (start/stop)
	TimelineMurmur                           // a murmur broadcast event
	TimelineIssue                            // a daemon issue first seen / resolved
)

// TimelineEntry is a single row in the centre timeline pane, ordered by timestamp descending.
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
