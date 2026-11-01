package ui

import (
	"fmt"
	"strings"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/theme"
)

// init_dock.go — where `ox init` ends: a box that says what is now connected to
// what, and who can read what flows between. It is the terminal twin of the
// facts card in the repo-initialized email (Repository, Connected to, and who
// can read what you save), in the same order.

// InitPlanPrompt is the first thing to ask an AI coworker once a repo is
// initialized. It mirrors PLAN_PROMPT in the web app's RepoInitializedEmail:
// the same words in the terminal and in the inbox, so keep the two in step.
const InitPlanPrompt = "Plan the next thing you're building. Make it a SageOx enriched visual plan, save it, and render it for review."

// initDockTitle is the dock's heading. Scripts and agents grep for it.
const initDockTitle = "SageOx initialized successfully!"

// initDockWrapAt is the width past which a dock line splits in two, so the
// framed box stays inside an 80-column terminal (a box adds six columns).
const initDockWrapAt = 68

// SessionReach is where this repo's recorded sessions end up, which decides the
// one line about who can read them. Each state names a different truth; the
// dock never claims more than the repo's settings give.
type SessionReach int

const (
	// SessionReachUnknown prints no line: not on a team, so no Ledger to name.
	SessionReachUnknown SessionReach = iota
	// SessionsToTeam: recorded sessions upload to the team's Ledger on their own.
	SessionsToTeam
	// SessionsStayLocal: recorded sessions wait on this machine for an explicit upload.
	SessionsStayLocal
	// SessionsOff: nothing is recorded here.
	SessionsOff
)

// InitDock is the content of the docking box. Repo and Team arrive already
// scrubbed of terminal control sequences: they come from a git remote and from
// the server.
type InitDock struct {
	Repo     string
	Team     string // empty when the repo is not on a team
	Sessions SessionReach
}

// Render draws the dock. framed is the interactive terminal's rounded box; the
// unframed form is the same facts as labeled lines, for a pipe, CI, or an AI
// coworker, where a border is noise.
func (d InitDock) Render(framed bool) string {
	if !framed {
		return d.plain()
	}
	return RenderBox(initDockTitle, strings.Join(d.boxLines(), "\n"), BoxSuccess)
}

func (d InitDock) boxLines() []string {
	var lines []string
	route := "Repo  " + d.Repo
	switch {
	case d.Team == "":
	case lipgloss.Width(route)+lipgloss.Width("  →  Team  "+d.Team) > initDockWrapAt:
		lines = append(lines, route, "  →  Team  "+d.Team)
		route = ""
	default:
		route += "  →  Team  " + d.Team
	}
	if route != "" {
		lines = append(lines, route)
	}
	if reach := d.reach(); reach != "" {
		lines = append(lines, "")
		lines = append(lines, wrapAt(reach, "; ", initDockWrapAt)...)
	}
	return lines
}

func (d InitDock) plain() string {
	lines := []string{initDockTitle, "  Repo:  " + d.Repo}
	if d.Team != "" {
		lines = append(lines, "  Team:  "+d.Team)
	}
	if reach := d.reach(); reach != "" {
		lines = append(lines, "  "+reach)
	}
	return strings.Join(lines, "\n")
}

// reach is the sentence about who can read the sessions, or "" when there is
// nothing true to say.
func (d InitDock) reach() string {
	switch {
	case d.Sessions == SessionsOff:
		return "Session recording is off here; nothing goes to the Ledger."
	case d.Team == "":
		return ""
	case d.Sessions == SessionsToTeam:
		return fmt.Sprintf("Sessions go to %s's Ledger; teammates can read them.", d.Team)
	case d.Sessions == SessionsStayLocal:
		return fmt.Sprintf("Sessions stay on this machine until you upload them to %s's Ledger.", d.Team)
	}
	return ""
}

// wrapAt splits s into lines no wider than width. It prefers to break once at
// sep, which keeps a sentence's clauses together; a sentence with no sep breaks
// between words instead. A single word wider than width keeps its own line.
func wrapAt(s, sep string, width int) []string {
	if lipgloss.Width(s) <= width {
		return []string{s}
	}
	if head, tail, ok := strings.Cut(s, sep); ok {
		// Either clause can still be too wide (a long team name), so each one
		// wraps between words on its own.
		return append(wrapWords(head+strings.TrimRight(sep, " "), width), wrapWords(tail, width)...)
	}
	return wrapWords(s, width)
}

// wrapWords breaks s between words so no line is wider than width. A single
// word wider than width stays whole on its own line.
func wrapWords(s string, width int) []string {
	if lipgloss.Width(s) <= width {
		return []string{s}
	}
	var lines []string
	line := ""
	for _, word := range strings.Fields(s) {
		switch {
		case line == "":
			line = word
		case lipgloss.Width(line+" "+word) > width:
			lines = append(lines, line)
			line = word
		default:
			line += " " + word
		}
	}
	return append(lines, line)
}

// RenderTryFirst is the plan prompt to paste into an AI coworker. The prompt is
// one unwrapped line: the terminal soft-wraps it, so a click-and-copy takes the
// sentence and not a border or a hard line break.
func RenderTryFirst() string {
	return RenderCategory("Try this first") + "\n\n" +
		"  Paste into Claude Code or Codex:\n" +
		"  " + AccentStyle.Render(InitPlanPrompt) + "\n"
}

// AddonOffer is one optional Claude Code add-on `ox init` mentions.
type AddonOffer struct {
	Command string
	What    string
}

// addonCommandStyle matches how init's steps style a command (cli.StyleCommand).
var addonCommandStyle = lipgloss.NewStyle().Foreground(theme.Adapt(theme.ColorSecondary))

// RenderAddonOffers lists the add-ons `ox init` offers, once per machine: a
// command and what it gives, one line each, nothing pre-installed behind
// anyone's back.
func RenderAddonOffers(offers []AddonOffer) string {
	width := 0
	for _, o := range offers {
		width = max(width, lipgloss.Width(o.Command))
	}
	var b strings.Builder
	b.WriteString(RenderCategory("Optional for Claude Code") + "\n\n")
	for _, o := range offers {
		pad := strings.Repeat(" ", width-lipgloss.Width(o.Command))
		fmt.Fprintf(&b, "  %s%s  %s\n", addonCommandStyle.Render(o.Command), pad, o.What)
	}
	b.WriteString("  " + MutedStyle.Render("Shown once on this machine.") + "\n")
	return b.String()
}
