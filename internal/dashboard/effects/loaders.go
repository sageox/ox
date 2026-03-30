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
