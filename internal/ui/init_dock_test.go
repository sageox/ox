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
			name:      "a team but no known session setting: no claim about readers",
			dock:      InitDock{Repo: "sageox/ox", Team: "Team A"},
			wantPlain: "SageOx initialized successfully!\n  Repo:  sageox/ox\n  Team:  Team A",
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
// overflow, for every sentence the dock can print.
// Failure prevented: a box wider than the terminal, whose right border wraps
// onto the next row and shears every line below it. The "stay local" and
// "recording off" sentences have no semicolon to split at, so they once passed
// through unwrapped.
func TestInitDock_StaysInsideAnEightyColumnTerminal(t *testing.T) {
	const longTeam = "Platform Engineering and Reliability"
	tests := []struct {
		name     string
		sessions SessionReach
		sentence string // what the box says once its line breaks are undone
	}{
		{
			name:     "to the team: splits after the semicolon",
			sessions: SessionsToTeam,
			sentence: "Sessions go to " + longTeam + "'s Ledger; teammates can read them.",
		},
		{
			name:     "stay local: no semicolon, splits between words",
			sessions: SessionsStayLocal,
			sentence: "Sessions stay on this machine until you upload them to " + longTeam + "'s Ledger.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dock := InitDock{
				Repo:     "a-large-organization/a-repository-with-a-rather-long-name",
				Team:     longTeam,
				Sessions: tt.sessions,
			}
			framed := ansi.Strip(dock.Render(true))
			for _, line := range strings.Split(framed, "\n") {
				assert.LessOrEqual(t, ansi.StringWidth(line), 80, line)
			}
			assert.Contains(t, unframe(framed), tt.sentence, "the sentence is split between words, not cut")
		})
	}
}

// unframe joins a rendered box's text lines back into one string, so a test can
// ask what the box says without caring where it broke the lines.
func unframe(box string) string {
	var words []string
	for _, line := range strings.Split(box, "\n") {
		line = strings.Trim(line, "╭╮╰╯─│ ")
		if line != "" {
			words = append(words, line)
		}
	}
	return strings.Join(words, " ")
}

// TestWrapAt pins the three ways a line can leave wrapAt.
func TestWrapAt(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		width int
		want  []string
	}{
		{"fits: untouched", "one two three", 20, []string{"one two three"}},
		{"too wide, has the separator: split there", "alpha beta; gamma delta", 12, []string{"alpha beta;", "gamma delta"}},
		{"too wide, no separator: split between words", "alpha beta gamma delta", 12, []string{"alpha beta", "gamma delta"}},
		{"a word wider than the width keeps its own line", "alpha extraordinarily beta", 8, []string{"alpha", "extraordinarily", "beta"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, wrapAt(tt.in, "; ", tt.width))
		})
	}
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
