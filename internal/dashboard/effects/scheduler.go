package effects

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// DefaultRefreshInterval is how often the dashboard polls for new data.
const DefaultRefreshInterval = 5 * time.Second

// RefreshTickCmd returns a tea.Cmd that sleeps for interval and then emits
// a RefreshTickMsg carrying gen. The root model uses gen to avoid acting on
// ticks that belong to a superseded refresh cycle.
func RefreshTickCmd(gen int, interval time.Duration) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(interval)
		return RefreshTickMsg{Gen: gen}
	}
}
