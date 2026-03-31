package effects

import tea "charm.land/bubbletea/v2"

// LoadDaemonStatusCmd returns a tea.Cmd that fetches the daemon status
// asynchronously and wraps the result in DaemonStatusLoadedMsg.
func LoadDaemonStatusCmd(c Client, gen int) tea.Cmd {
	return func() tea.Msg {
		data, err := c.GetDaemonStatus()
		return DaemonStatusLoadedMsg{Data: data, Gen: gen, Err: err}
	}
}

// LoadSessionsCmd returns a tea.Cmd that fetches recent sessions
// asynchronously and wraps the result in SessionsLoadedMsg.
func LoadSessionsCmd(c Client, gen int) tea.Cmd {
	return func() tea.Msg {
		sessions, err := c.ListSessions()
		return SessionsLoadedMsg{Sessions: sessions, Gen: gen, Err: err}
	}
}

// LoadMurmursCmd returns a tea.Cmd that fetches murmur entries
// asynchronously and wraps the result in MurmursLoadedMsg.
func LoadMurmursCmd(c Client, gen int) tea.Cmd {
	return func() tea.Msg {
		murmurs, err := c.ListMurmurs()
		return MurmursLoadedMsg{Murmurs: murmurs, Gen: gen, Err: err}
	}
}

// LoadTeamDiscussionsCmd returns a tea.Cmd that fetches team discussion
// previews asynchronously and wraps the result in TeamDiscussionsLoadedMsg.
func LoadTeamDiscussionsCmd(c Client, gen int) tea.Cmd {
	return func() tea.Msg {
		discussions, err := c.ListTeamDiscussions()
		return TeamDiscussionsLoadedMsg{Discussions: discussions, Gen: gen, Err: err}
	}
}

// LoadInstancesCmd returns a tea.Cmd that fetches active AI coworker instances
// asynchronously and wraps the result in InstancesLoadedMsg.
func LoadInstancesCmd(c Client, gen int) tea.Cmd {
	return func() tea.Msg {
		instances, err := c.ListInstances()
		return InstancesLoadedMsg{Instances: instances, Gen: gen, Err: err}
	}
}

// LoadStoredErrorsCmd returns a tea.Cmd that fetches unviewed stored errors
// asynchronously and wraps the result in StoredErrorsLoadedMsg.
func LoadStoredErrorsCmd(c Client, gen int) tea.Cmd {
	return func() tea.Msg {
		errors, err := c.ListStoredErrors()
		return StoredErrorsLoadedMsg{Errors: errors, Gen: gen, Err: err}
	}
}

// LoadTeamContextsCmd returns a tea.Cmd that fetches team context metadata
// asynchronously and wraps the result in TeamContextsLoadedMsg.
func LoadTeamContextsCmd(c Client, gen int) tea.Cmd {
	return func() tea.Msg {
		contexts, err := c.ListTeamContexts()
		return TeamContextsLoadedMsg{TeamContexts: contexts, Gen: gen, Err: err}
	}
}

// LoadCodeIndexStatsCmd returns a tea.Cmd that fetches code index statistics
// asynchronously and wraps the result in CodeIndexStatsLoadedMsg.
func LoadCodeIndexStatsCmd(c Client, gen int) tea.Cmd {
	return func() tea.Msg {
		stats, err := c.LoadCodeIndexStats()
		return CodeIndexStatsLoadedMsg{Stats: stats, Gen: gen, Err: err}
	}
}

// LoadWhisperHistoryCmd returns a tea.Cmd that fetches recent whisper history
// asynchronously and wraps the result in WhisperHistoryLoadedMsg.
func LoadWhisperHistoryCmd(c Client, gen int) tea.Cmd {
	return func() tea.Msg {
		entries, err := c.ListWhisperHistory()
		return WhisperHistoryLoadedMsg{Entries: entries, Gen: gen, Err: err}
	}
}
