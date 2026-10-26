package usage

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/recap"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/theme"
)

// forceColor makes theme.ColorEnabled report true for the test.
func forceColor(t *testing.T) {
	t.Helper()
	prev := theme.Profile
	theme.Profile = colorprofile.TrueColor
	t.Cleanup(func() { theme.Profile = prev })
	require.True(t, theme.ColorEnabled())
}

// TestHuman_ColorIsOnlyDecoration: on a color terminal every view is styled,
// and stripping the escapes gives back exactly the plain display. Failure
// prevented: the terminal view says something different from what the AI
// coworker prints, or wraps differently because escapes were counted as width.
func TestHuman_ColorIsOnlyDecoration(t *testing.T) {
	forceColor(t)
	ledger := t.TempDir()
	writeLedgerSession(t, ledger, alice, 1, nil, primeEvent(), memoryProvided(), invoked("checked murmurs", "murmur list", 300, 2))
	me := BuildMe(rollupIn(ledger, aliceID()))
	team := BuildTeam(rollupIn(ledger, recap.Identity{}))
	session := goldenReport(80)

	tests := []struct {
		name         string
		human, plain string
	}{
		{"session", Human(session, 80), session.Display},
		{"me", HumanMe(me, 80), me.Display},
		{"team", HumanTeam(team, 80), team.Display},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Contains(t, tt.human, "\x1b[", "styled on a color terminal")
			assert.Equal(t, tt.plain, ansi.Strip(tt.human))
		})
	}

	// a pipe or NO_COLOR terminal gets the plain display byte for byte
	theme.Profile = colorprofile.NoTTY
	assert.Equal(t, me.Display, HumanMe(me, 80))
	assert.Equal(t, team.Display, HumanTeam(team, 80))
}

// TestDisplay_SessionEdgeCases covers the less common session shapes.
// Failure prevented: a missing reason prints an empty sentence, a past
// session is labeled "this session", an anonymous murmur shows as a bare
// "murmur", a long self-report list or narrow terminal overflows one screen.
func TestDisplay_SessionEdgeCases(t *testing.T) {
	long := strings.Repeat("decision ", 20)
	tests := []struct {
		name  string
		r     Report
		width int
		want  []string
	}{
		{
			name: "no trace and no reason",
			r:    Report{Trace: TraceInfo{Source: TraceSourceNone}},
			want: []string{"no usage trace for this session: nothing recorded yet"},
		},
		{
			name: "past session, anonymous murmur, other ref, many self-reports",
			r: Report{
				Session: SessionInfo{Name: "2026-10-01T10-00-old-OxZz99"},
				Trace:   TraceInfo{Available: true, Source: TraceSourceSession},
				Surfaced: []Surfaced{
					{Ref: "mur_9", RefType: "murmur", Via: "whisper"},
					{Ref: "plan-x", RefType: "plan", Via: "prime"},
				},
				Used: Used{Status: UsedReported, SelfReported: []SelfReported{
					{Ref: "a", Decision: long}, {Ref: "b"}, {Ref: "c"}, {Ref: "d"}, {Ref: "e"},
				}},
			},
			want: []string{"2026-10-01T10-00-old-OxZz99", "murmur mur_9", "plan plan-x", "+2 more", "…\")"},
		},
		{
			name: "zero-token source draws no bar and narrow width caps the screen",
			r: func() Report {
				r := Report{Trace: TraceInfo{Available: true, Source: TraceSourceSession}}
				r.Injected.BySource = []SourceStat{{Source: "big", Tokens: 500}, {Source: "empty", Tokens: 0}}
				for i := 0; i < 40; i++ {
					r.Surfaced = append(r.Surfaced, Surfaced{Ref: fmt.Sprintf("doc-%02d.md", i), RefType: "doc"})
				}
				return r
			}(),
			width: 20,
			want:  []string{"empty 0", "…"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Display(tt.r, tt.width)
			for _, w := range tt.want {
				assert.Contains(t, got, w)
			}
			lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
			assert.LessOrEqual(t, len(lines), maxDisplayLines)
			assert.NotContains(t, got, "not metered", "no unmetered note when nothing is unmetered")
		})
	}
}

// TestDisplayMe_EveryRowKind pins the --me table for each trace state.
// Failure prevented: a stub row reads as zero usage, a score is hidden, a
// long history spills past one screen without saying more exist, or an
// untitled session renders a blank title.
func TestDisplayMe_EveryRowKind(t *testing.T) {
	r := MeReport{Totals: MeTotals{Sessions: maxMeDisplayRows + 3, NotHydrated: 1}}
	r.Sessions = []MeRow{
		{Name: "untitled-OxAa11", Date: "10-23", Trace: RowTraced, Calls: 3, Tokens: 1200, Score: "significant"},
		{Name: "stub", Date: "10-22", Title: "stub", Trace: RowNotHydrated, Score: "low"},
		{Name: "old", Date: "10-21", Title: "old", Trace: RowNoTrace, Score: "hidden-score"},
		{Name: "wide", Date: "10-20", Title: strings.Repeat("w", 200), Trace: RowTraced},
	}
	for len(r.Sessions) < maxMeDisplayRows+3 {
		r.Sessions = append(r.Sessions, MeRow{Name: "n", Date: "10-01", Title: "filler", Trace: RowTraced})
	}

	got := DisplayMe(r, 0)

	assert.Contains(t, got, "my sessions  recent")
	assert.Contains(t, got, "untitled-OxAa11")
	assert.Contains(t, got, "significant")
	assert.Contains(t, got, "—")
	assert.Contains(t, got, "no usage trace")
	assert.NotContains(t, got, "hidden-score", "a score is not shown on a row with no trace")
	assert.Contains(t, got, "+3 more in the JSON report")
	assert.Contains(t, got, "1 not hydrated")
	assertPlainFits(t, got, 80)
}

// TestBuildMe_DefaultLimit: a zero --limit means the default of 10, not none.
// Failure prevented: `ox usage --me` with no limit shows zero sessions.
func TestBuildMe_DefaultLimit(t *testing.T) {
	ledger := t.TempDir()
	for h := 1; h <= 12; h++ {
		writeLedgerSession(t, ledger, alice, h, nil, invoked("checked murmurs", "murmur list", 10, 1))
	}
	in := rollupIn(ledger, aliceID())
	in.Limit = 0
	assert.Len(t, BuildMe(in).Sessions, 10)
}

// TestBuildTeam_RanksCapsAndCountsStubs: ties sort deterministically, each
// list is capped, and dehydrated sessions are counted but not read. Failure
// prevented: the team display reorders between runs, grows unbounded, or
// silently drops sessions that exist only as LFS stubs.
func TestBuildTeam_RanksCapsAndCountsStubs(t *testing.T) {
	ledger := t.TempDir()
	var events []contexttrace.Event
	for i := 0; i < maxTeamItems+2; i++ {
		events = append(events, invoked(fmt.Sprintf("label %02d", i), fmt.Sprintf("cmd%02d", i), 5, i))
		ev := memoryProvided()
		ev.Doc = fmt.Sprintf("doc-%02d.md", i)
		ev.InlineTokens = 100
		events = append(events, ev)
	}
	writeLedgerSession(t, ledger, alice, 1, nil, events...)
	// a second session shares doc-00 (so docs rank by count) and gives
	// team-docs the same token total as ox calls (so sources tie on tokens)
	shared := invoked("label 00", "cmd00", 5, 1)
	sharedDoc := contexttrace.Event{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamDocs, Doc: "doc-00.md", InlineTokens: 5 * (maxTeamItems + 3)}
	writeLedgerSession(t, ledger, bob, 3, nil, shared, sharedDoc)
	stub := writeLedgerSession(t, ledger, bob, 2, nil)
	writePointerTrace(t, ledger, stub)

	r := BuildTeam(rollupIn(ledger, recap.Identity{}))

	assert.Equal(t, 3, r.Sessions)
	assert.Equal(t, 2, r.Traced)
	assert.Equal(t, 1, r.NotHydrated)
	require.Len(t, r.Calls, maxTeamItems)
	assert.Equal(t, LabelStat{Label: "label 00", Count: 2, Tokens: 10}, r.Calls[0])
	assert.Equal(t, "label 01", r.Calls[1].Label, "equal counts sort by label")
	assert.Equal(t, []SourceStat{{Source: "ox calls", Tokens: 65}, {Source: "team-docs", Tokens: 65}}, r.Injected[1:3], "equal tokens sort by source")
	require.Len(t, r.TopSurfaced, maxTeamItems)
	assert.Equal(t, DocStat{Ref: "doc-00.md", Sessions: 2}, r.TopSurfaced[0])
	assert.Equal(t, "doc-01.md", r.TopSurfaced[1].Ref, "equal counts sort by ref")
	assert.Contains(t, r.Display, "1 session not hydrated locally, not counted")
}

// TestDisplayTeam_SparseShapes covers a traced window with no named docs and
// an empty window. Failure prevented: an empty "surfaced" line, a zero-token
// source drawn with a bar, or a blank header window.
func TestDisplayTeam_SparseShapes(t *testing.T) {
	traced := DisplayTeam(TeamReport{
		Sessions: 1, Traced: 1,
		Calls:    []LabelStat{{Label: "checked murmurs", Count: 1}},
		Injected: []SourceStat{{Source: "ox calls", Tokens: 0}},
	}, 0)
	assert.Contains(t, traced, "team  recent")
	assert.Contains(t, traced, "no docs by name")
	assert.NotContains(t, traced, "▇")

	empty := DisplayTeam(TeamReport{Window: "7d"}, 80)
	assert.Contains(t, empty, "no usage traces in this window yet")
}
