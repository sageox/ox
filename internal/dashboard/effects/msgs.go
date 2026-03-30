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

// RefreshTickMsg is sent by the background refresh timer.
// It signals the root model to start a new data-fetch cycle.
type RefreshTickMsg struct {
	Gen int
}
