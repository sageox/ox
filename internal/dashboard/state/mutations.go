package state

import (
	"time"

	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/dashboard/domain"
	"github.com/sageox/ox/internal/session"
)

// ApplyDaemonStatus returns a new Store with the daemon status snapshot applied.
// If this is the first successful response (status != nil, no error), IsLoading
// is cleared. The LoadedAt timestamp is always updated on non-error responses.
func ApplyDaemonStatus(s Store, data *daemon.StatusData, err error) Store {
	s.DaemonStatus = data
	s.DaemonErr = err
	if err == nil {
		s.DaemonLoadedAt = time.Now()
		s.IsLoading = false
	}
	return s
}

// ApplySessions returns a new Store with the session list applied.
func ApplySessions(s Store, sessions []session.SessionInfo, err error) Store {
	s.Sessions = sessions
	s.SessionsErr = err
	if err == nil {
		s.SessionsLoadedAt = time.Now()
	}
	return s
}

// ApplyMurmurs returns a new Store with the murmur list applied.
func ApplyMurmurs(s Store, murmurs []domain.MurmurEntry, err error) Store {
	s.Murmurs = murmurs
	s.MurmursErr = err
	return s
}

// ApplyDiscussions returns a new Store with the team discussions applied.
func ApplyDiscussions(s Store, discussions []domain.TeamDiscussion, err error) Store {
	s.Discussions = discussions
	s.DiscussionsErr = err
	return s
}

// ApplyInstances returns a new Store with the AI coworker instance list applied.
func ApplyInstances(s Store, instances []daemon.InstanceInfo, err error) Store {
	s.Instances = instances
	s.InstancesErr = err
	return s
}

// ApplyStoredErrors returns a new Store with the stored error list applied.
func ApplyStoredErrors(s Store, errors []daemon.StoredError, err error) Store {
	s.StoredErrors = errors
	s.StoredErrorsErr = err
	return s
}

// ApplyTeamContexts returns a new Store with the team context metadata applied.
func ApplyTeamContexts(s Store, contexts []domain.TeamContextEntry, err error) Store {
	s.TeamContexts = contexts
	s.TeamContextsErr = err
	return s
}

// ApplyCodeIndexStats returns a new Store with the code index statistics applied.
func ApplyCodeIndexStats(s Store, stats *daemon.CodeDBStats, err error) Store {
	s.CodeIndexStats = stats
	s.CodeIndexStatsErr = err
	return s
}

// ApplyWhisperHistory returns a new Store with the whisper history applied.
func ApplyWhisperHistory(s Store, entries []domain.WhisperHistoryEntry, err error) Store {
	s.WhisperHistory = entries
	s.WhisperHistoryErr = err
	return s
}

// ApplySelection returns a new Store with the inspector target updated.
// Pass nil to clear the selection.
func ApplySelection(s Store, target *domain.InspectorTarget) Store {
	s.Selected = target
	return s
}

// IncrementGeneration returns a new Store with the generation counter bumped.
// The generation is used to discard in-flight async responses that belong to
// a previous refresh cycle.
func IncrementGeneration(s Store) Store {
	s.Generation++
	return s
}

// SetLoading returns a new Store with the loading flag set to true.
// Call this before dispatching the initial data-fetch commands.
func SetLoading(s Store) Store {
	s.IsLoading = true
	return s
}

// SetStatusMessage returns a new Store with the status bar message set.
func SetStatusMessage(s Store, msg string) Store {
	s.StatusMsg = msg
	return s
}

// MoveNavCursor returns a new Store with the nav cursor clamped to [0, max).
func MoveNavCursor(s Store, delta, max int) Store {
	s.NavCursorPos += delta
	if s.NavCursorPos < 0 {
		s.NavCursorPos = 0
	}
	if max > 0 && s.NavCursorPos >= max {
		s.NavCursorPos = max - 1
	}
	if max == 0 {
		s.NavCursorPos = 0
	}
	return s
}

// MoveTimelineCursor returns a new Store with the timeline cursor clamped to [0, max).
func MoveTimelineCursor(s Store, delta, max int) Store {
	s.TimelineCursorPos += delta
	if s.TimelineCursorPos < 0 {
		s.TimelineCursorPos = 0
	}
	if max > 0 && s.TimelineCursorPos >= max {
		s.TimelineCursorPos = max - 1
	}
	if max == 0 {
		s.TimelineCursorPos = 0
	}
	return s
}

// SetMurmurFilter returns a new Store with the murmur topic filter applied.
// Resets the timeline cursor to zero so the feed starts from the top.
func SetMurmurFilter(s Store, filter domain.MurmurTopicFilter) Store {
	s.MurmurTopic = filter
	s.TimelineCursorPos = 0
	return s
}

// SetMurmurSearch returns a new Store with the inline murmur search query updated.
func SetMurmurSearch(s Store, query string) Store {
	s.MurmurQuery = query
	s.TimelineCursorPos = 0
	return s
}

// SetMurmurSearchActive returns a new Store with the search input open/closed.
func SetMurmurSearchActive(s Store, active bool) Store {
	s.MurmurQueryOpen = active
	if !active {
		s.MurmurQuery = "" // clear query on close
	}
	return s
}
