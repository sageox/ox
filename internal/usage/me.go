package usage

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/sageox/ox/internal/recap"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/session/contextuse"
	"github.com/sageox/ox/internal/theme"
)

// Row trace states for the --me rollup.
const (
	RowTraced      = "traced"
	RowNoTrace     = "no_usage_trace" // the session predates usage recording
	RowNotHydrated = "not_hydrated"   // the trace exists only as an LFS stub
)

// maxMeDisplayRows keeps the --me display on one screen.
const maxMeDisplayRows = 16

// guidanceMe narrates the --me rollup.
const guidanceMe = "Show the coworker what SageOx gave their recent sessions. Print the display field verbatim " +
	"inside a fenced ```text block, then at most 3 sentences of your own. Rows marked \"no usage trace\" " +
	"predate usage recording; rows marked \"—\" are not hydrated locally and can be read with " +
	"`ox session view <name> --context`. Token counts are estimates (~4 bytes per token) — say so once. " +
	"\"useful\" counts only items with use evidence; never claim more, and never state or " +
	"imply time saved or dollar figures."

// RollupInput scopes the --me and --team views.
type RollupInput struct {
	LedgerPath string
	Identity   recap.Identity
	Since      time.Time
	Until      time.Time
	Window     string // the --since value as typed, for the header ("7d")
	Limit      int
	Width      int

	// ScoreThreshold and AllowJudged are the project's attribution settings,
	// as in Input, so a row's would_attribute matches its session view.
	ScoreThreshold float64
	AllowJudged    bool
}

// MeReport is the `ox session report --me --json` payload.
type MeReport struct {
	View        string       `json:"view"`
	Window      string       `json:"window"`
	Sessions    []MeRow      `json:"sessions"`
	Totals      MeTotals     `json:"totals"`
	Display     string       `json:"display"`
	Guidance    string       `json:"guidance"`
	NextActions []NextAction `json:"next_actions"`
}

// MeRow is one of the coworker's sessions.
type MeRow struct {
	Name     string `json:"name"`
	Date     string `json:"date"`
	Title    string `json:"title,omitempty"`
	Trace    string `json:"trace"` // traced | no_usage_trace | not_hydrated
	Calls    int    `json:"calls"`
	Tokens   int    `json:"tokens"`
	Surfaced int    `json:"surfaced"`
	Useful   int    `json:"useful"` // surfaced items with use evidence
	Score    string `json:"score,omitempty"`
	// WouldAttribute — the session's attribution clears the project
	// threshold (display-only; the session view's attribution.would_attribute).
	WouldAttribute bool `json:"would_attribute"`
}

// MeTotals sums the rows.
type MeTotals struct {
	Sessions    int `json:"sessions"`
	Traced      int `json:"traced"`
	NotHydrated int `json:"not_hydrated"`
	Calls       int `json:"calls"`
	Tokens      int `json:"tokens"`
	Useful      int `json:"useful"`
	Attributed  int `json:"attributed"` // rows whose would_attribute is true
}

// windowSessions returns the ledger sessions in the window, newest first.
// recap.ScanSessions skips draft placeholders.
func windowSessions(in RollupInput) []recap.SessionFacts {
	facts := recap.ScanSessions(in.LedgerPath, in.Since, in.Until, in.Identity)
	sort.SliceStable(facts, func(i, j int) bool { return facts[i].CreatedAt.After(facts[j].CreatedAt) })
	return facts
}

// hasInvoked reports whether a trace carries any usage events.
func hasInvoked(events []contexttrace.Event) bool {
	for _, ev := range events {
		if ev.Type == contexttrace.EventInvoked {
			return true
		}
	}
	return false
}

// BuildMe rolls the coworker's recent sessions up into one row each. It reads
// traces through recap.ReadTrace, which never hydrates.
func BuildMe(in RollupInput) MeReport {
	r := MeReport{View: ViewMe, Window: in.Window, Sessions: []MeRow{}, Guidance: guidanceMe, NextActions: []NextAction{}}
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	for _, s := range windowSessions(in) {
		if !s.Mine {
			continue
		}
		if len(r.Sessions) >= limit {
			break
		}
		row := MeRow{Name: s.Name, Date: s.CreatedAt.Local().Format("01-02"), Title: s.Title, Score: s.ScoreCategory}
		sin := Input{
			ScoreCategory: s.ScoreCategory, ProducedCommits: len(s.ProducedCommits),
			ScoreThreshold: in.ScoreThreshold, AllowJudged: in.AllowJudged,
		}
		events, dehydrated := recap.ReadTrace(in.LedgerPath, s.Name)
		switch {
		case dehydrated:
			row.Trace = RowNotHydrated
			r.Totals.NotHydrated++
			// No readable trace: only a self-reported score can attribute.
			row.WouldAttribute = buildAttribution(sin, contextuse.Rollup{}).WouldAttribute
		case !hasInvoked(events):
			row.Trace = RowNoTrace
			row.WouldAttribute = buildAttribution(sin, contextuse.Rollup{}).WouldAttribute
		default:
			row.Trace = RowTraced
			sin.Events, sin.TraceSource = events, TraceSourceSession
			sr := Build(sin)
			row.WouldAttribute = sr.Attribution.WouldAttribute
			row.Calls = sr.Calls.Total
			row.Tokens = sr.Context.Tokens
			row.Surfaced = len(sr.Surfaced)
			row.Useful = usefulCount(sr)
			r.Totals.Traced++
			r.Totals.Calls += row.Calls
			r.Totals.Tokens += row.Tokens
			r.Totals.Useful += row.Useful
		}
		if row.WouldAttribute {
			r.Totals.Attributed++
		}
		r.Sessions = append(r.Sessions, row)
	}
	r.Totals.Sessions = len(r.Sessions)
	if r.Totals.NotHydrated > 0 {
		r.NextActions = append(r.NextActions, NextAction{Action: "ox session view <name> --context", Why: "read a session whose trace is not hydrated locally"})
	}
	if r.Totals.Sessions == 0 {
		r.NextActions = append(r.NextActions, NextAction{Action: "ox session start", Why: "record a session so ox session report can report it"})
	}
	r.Display = DisplayMe(r, in.Width)
	return r
}

// usefulCount is the number of surfaced items with use evidence: the
// relevant summary's count when the trace carries one, else the items whose
// used evidence is present.
func usefulCount(r Report) int {
	if r.Relevant != nil {
		return r.Relevant.Count
	}
	n := 0
	for _, s := range r.Surfaced {
		if s.Used != nil {
			n++
		}
	}
	return n
}

// DisplayMe renders the --me rollup as plain text (no ANSI).
func DisplayMe(r MeReport, width int) string { return renderMe(r, width, plain) }

// HumanMe is DisplayMe with theme colors when the terminal supports them.
func HumanMe(r MeReport, width int) string {
	if !theme.ColorEnabled() {
		return DisplayMe(r, width)
	}
	return renderMe(r, width, themed())
}

func renderMe(r MeReport, width int, st styler) string {
	if width <= 0 {
		width = 80
	}
	window := "last " + r.Window
	if r.Window == "" {
		window = "recent"
	}
	lines := []string{st.head("ox session report") + st.dim(fmt.Sprintf(" · my sessions  %s · %d session%s", window, r.Totals.Sessions, plural(r.Totals.Sessions, "", "s")))}
	if len(r.Sessions) == 0 {
		lines = append(lines, st.dim("no recorded sessions in this window — start one: ox session start"))
		return finish(lines)
	}

	// date, gaps, the numeric columns and the longest score ("significant")
	// take a fixed width; the title gets the rest.
	const fixed = 5 + 2 + 2 + (5 + 1 + 7 + 1 + 4 + 1 + 6) + 2 + 11
	titleW := max(12, width-fixed)
	lines = append(lines, st.dim(fmt.Sprintf("%-5s  %-*s  %5s %7s %4s %6s  %s", "date", titleW, "title", "calls", "tokens", "read", "useful", "score")))
	for i, row := range r.Sessions {
		if i == maxMeDisplayRows {
			lines = append(lines, st.dim(fmt.Sprintf("+%d more in the JSON report", len(r.Sessions)-maxMeDisplayRows)))
			break
		}
		title := row.Title
		if title == "" {
			title = row.Name
		}
		title = padRight(truncate(title, titleW), titleW)
		var rest string
		switch row.Trace {
		case RowNotHydrated:
			rest = fmt.Sprintf("%5s %7s %4s %6s", "—", "—", "—", "—")
		case RowNoTrace:
			rest = st.dim("no usage trace")
		default:
			rest = fmt.Sprintf("%5d %7s %4d %6d", row.Calls, "~"+FmtK(row.Tokens), row.Surfaced, row.Useful)
		}
		line := fmt.Sprintf("%-5s  %s  %s", row.Date, title, rest)
		if row.Score != "" && row.Trace != RowNoTrace {
			line += "  " + row.Score
		}
		lines = append(lines, strings.TrimRight(line, " "))
	}
	t := r.Totals
	lines = append(lines, st.label("total")+fmt.Sprintf("  %d calls · ~%s tokens · %d useful · co-author %d of %d",
		t.Calls, FmtK(t.Tokens), t.Useful, t.Attributed, t.Sessions))
	if t.NotHydrated > 0 {
		lines = append(lines, st.dim(fmt.Sprintf("%d not hydrated; `ox session view <name> --context`", t.NotHydrated)))
	}
	return finish(lines)
}

func padRight(s string, w int) string {
	if n := ansi.StringWidth(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}
