package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInitDock_Golden is the snapshot of the box `ox init` ends on, framed (an
// interactive terminal) and unframed (a pipe, CI, an AI coworker).
// Failure prevented: init finishing without telling anyone that sessions are
// recorded or that teammates can read them (the old output was bold text between
// separators; the fact lived only in .sageox/README.md), and the dock claiming
// more than the repo's settings give.
func TestInitDock_Golden(t *testing.T) {
	tests := []struct {
		name       string
		dock       InitDock
		wantFramed string
		wantPlain  string
	}{
		{
			name: "sessions upload to the team's Ledger on their own",
			dock: InitDock{Repo: "sageox/ox", Team: "Team A", Sessions: SessionsToTeam},
			wantFramed: "╭────────────────────────────────────────────────────────────╮\n" +
				"│  SageOx initialized successfully!                          │\n" +
				"│  Repo  sageox/ox  →  Team  Team A                          │\n" +
				"│                                                            │\n" +
				"│  Sessions go to Team A's Ledger; teammates can read them.  │\n" +
				"╰────────────────────────────────────────────────────────────╯",
			wantPlain: "SageOx initialized successfully!\n" +
				"  Repo:  sageox/ox\n" +
				"  Team:  Team A\n" +
				"  Sessions go to Team A's Ledger; teammates can read them.",
		},
		{
			name: "publishing is manual, so nobody can read a session yet",
			dock: InitDock{Repo: "sageox/ox", Team: "Team A", Sessions: SessionsStayLocal},
			wantPlain: "SageOx initialized successfully!\n" +
				"  Repo:  sageox/ox\n" +
				"  Team:  Team A\n" +
				"  Sessions stay on this machine until you upload them to Team A's Ledger.",
		},
		{
			name: "recording is off, so there is nothing to read",
			dock: InitDock{Repo: "sageox/ox", Team: "Team A", Sessions: SessionsOff},
			wantPlain: "SageOx initialized successfully!\n" +
				"  Repo:  sageox/ox\n" +
				"  Team:  Team A\n" +
				"  Session recording is off here; nothing goes to the Ledger.",
		},
		{
			name: "not on a team: no Ledger is named",
			dock: InitDock{Repo: "sageox/ox", Sessions: SessionsToTeam},
			wantPlain: "SageOx initialized successfully!\n" +
				"  Repo:  sageox/ox",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantPlain, ansi.Strip(tt.dock.Render(false)))
			if tt.wantFramed != "" {
				assert.Equal(t, tt.wantFramed, ansi.Strip(tt.dock.Render(true)))
			}
		})
	}
}

// TestInitDock_StaysInsideAnEightyColumnTerminal verifies long names wrap, not
// overflow.
// Failure prevented: a box wider than the terminal, whose right border wraps
// onto the next row and shears every line below it.
func TestInitDock_StaysInsideAnEightyColumnTerminal(t *testing.T) {
	dock := InitDock{
		Repo:     "a-large-organization/a-repository-with-a-rather-long-name",
		Team:     "Platform Engineering and Reliability",
		Sessions: SessionsToTeam,
	}
	framed := ansi.Strip(dock.Render(true))
	for _, line := range strings.Split(framed, "\n") {
		assert.LessOrEqual(t, ansi.StringWidth(line), 80, line)
	}
	assert.Contains(t, framed, "Platform Engineering and Reliability's Ledger;",
		"the sentence splits after the semicolon rather than being cut")
	assert.Contains(t, framed, "teammates can read them.")
}

// TestRenderTryFirst verifies the plan prompt prints as one unwrapped line, so
// copying it takes the sentence and nothing else.
func TestRenderTryFirst(t *testing.T) {
	got := ansi.Strip(RenderTryFirst())
	require.Contains(t, got, "TRY THIS FIRST")
	assert.Contains(t, got, "\n  "+InitPlanPrompt+"\n", "the prompt is on one line, indented, with no border around it")
	assert.Equal(t,
		"Plan the next thing you're building. Make it a SageOx enriched visual plan, save it, and render it for review.",
		InitPlanPrompt, "the same words as the repo-initialized email")
}

func TestRenderAddonOffers_Golden(t *testing.T) {
	got := ansi.Strip(RenderAddonOffers([]AddonOffer{
		{"ox pet enable", "Oxy beside Claude Code's thinking line"},
		{"ox session report pane enable", "/ox-session-report: the ox calls in a session"},
	}))
	want := "OPTIONAL FOR CLAUDE CODE\n\n" +
		"  ox pet enable                  Oxy beside Claude Code's thinking line\n" +
		"  ox session report pane enable  /ox-session-report: the ox calls in a session\n" +
		"  Shown once on this machine.\n"
	assert.Equal(t, want, got)
}
