package app

import (
	tea "charm.land/bubbletea/v2"

	"github.com/sageox/ox/internal/dashboard/effects"
)

// LoadAllCmd fires all data-fetch commands in a single batch.
// Called on Init and on each refresh cycle.
func LoadAllCmd(client effects.Client, gen int) tea.Cmd {
	return tea.Batch(
		effects.LoadDaemonStatusCmd(client, gen),
		effects.LoadSessionsCmd(client, gen),
		effects.LoadMurmursCmd(client, gen),
		effects.LoadTeamDiscussionsCmd(client, gen),
		effects.LoadInstancesCmd(client, gen),
		effects.LoadStoredErrorsCmd(client, gen),
		effects.LoadTeamContextsCmd(client, gen),
		effects.LoadCodeIndexStatsCmd(client, gen),
		effects.LoadWhisperHistoryCmd(client, gen),
	)
}

// StartRefreshTickCmd starts the self-rescheduling 5-second refresh timer.
func StartRefreshTickCmd(gen int) tea.Cmd {
	return effects.RefreshTickCmd(gen, effects.DefaultRefreshInterval)
}
