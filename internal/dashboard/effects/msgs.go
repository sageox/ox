package effects

import (
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/dashboard/domain"
	"github.com/sageox/ox/internal/session"
)

// DaemonStatusLoadedMsg is sent to the BubbleTea model when a daemon status
// fetch completes. Gen matches the Store.Generation at the time the load was
// started; the model discards this message if its generation has advanced.
type DaemonStatusLoadedMsg struct {
	Data *daemon.StatusData
	Gen  int
	Err  error
}

// SessionsLoadedMsg is sent when the session list fetch completes.
type SessionsLoadedMsg struct {
	Sessions []session.SessionInfo
	Gen      int
	Err      error
}

// MurmursLoadedMsg is sent when the murmur list fetch completes.
type MurmursLoadedMsg struct {
	Murmurs []domain.MurmurEntry
	Gen     int
	Err     error
}

// TeamDiscussionsLoadedMsg is sent when the team discussion fetch completes.
type TeamDiscussionsLoadedMsg struct {
	Discussions []domain.TeamDiscussion
	Gen         int
	Err         error
}

// InstancesLoadedMsg is sent when the active AI coworker instance list fetch completes.
type InstancesLoadedMsg struct {
	Instances []daemon.InstanceInfo
	Gen       int
	Err       error
}

// StoredErrorsLoadedMsg is sent when the stored error list fetch completes.
type StoredErrorsLoadedMsg struct {
	Errors []daemon.StoredError
	Gen    int
	Err    error
}

// TeamContextsLoadedMsg is sent when the team context metadata fetch completes.
type TeamContextsLoadedMsg struct {
	TeamContexts []domain.TeamContextEntry
	Gen          int
	Err          error
}

// CodeIndexStatsLoadedMsg is sent when the code index statistics fetch completes.
type CodeIndexStatsLoadedMsg struct {
	Stats *daemon.CodeDBStats
	Gen   int
	Err   error
}

// WhisperHistoryLoadedMsg is sent when the whisper history fetch completes.
type WhisperHistoryLoadedMsg struct {
	Entries []domain.WhisperHistoryEntry
	Gen     int
	Err     error
}

// RefreshTickMsg is sent by the background refresh timer.
// It signals the root model to start a new data-fetch cycle.
type RefreshTickMsg struct {
	Gen int
}
