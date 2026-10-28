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
// session is labeled "this session", an anonymous murmur renders blank, or a
// narrow terminal overflows one screen.
func TestDisplay_SessionEdgeCases(t *testing.T) {
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
			name: "past session, anonymous used murmur, other ref",
			r: Report{
				Session: SessionInfo{Name: "2026-10-01T10-00-old-OxZz99"},
				Trace:   TraceInfo{Available: true, Source: TraceSourceSession},
				Surfaced: []Surfaced{
					{Ref: "mur_9", RefType: "murmur", Via: "whisper", Used: &ItemUsed{Evidence: EvidenceCited}},
					{Ref: "plan-x", RefType: "plan", Via: "prime"},
				},
			},
			want: []string{"ox session report · past session\n", "used       murmur  mur_9  cited\n", "available  1 plan\n"},
		},
		{
			name: "zero-token source draws no bar and narrow width caps the screen",
			r: func() Report {
				r := Report{Trace: TraceInfo{Available: true, Source: TraceSourceSession}}
				r.Context.BySource = []SourceStat{{Source: "big", Tokens: 500}, {Source: "empty", Tokens: 0}}
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
// Failure prevented: `ox session report --me` with no limit shows zero sessions.
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
	assert.Equal(t, []SourceStat{{Source: "ox calls", Tokens: 65}, {Source: "team-docs", Tokens: 65}}, r.Context[1:3], "equal tokens sort by source")
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
		Calls:   []LabelStat{{Label: "checked murmurs", Count: 1}},
		Context: []SourceStat{{Source: "ox calls", Tokens: 0}},
	}, 0)
	assert.Contains(t, traced, "team  recent")
	assert.Contains(t, traced, "no docs by name")
	assert.NotContains(t, traced, "▇")

	empty := DisplayTeam(TeamReport{Window: "7d"}, 80)
	assert.Contains(t, empty, "no usage traces in this window yet")
}

// TestRender_HeaderOmitsNameAndHarness: the header names the scope and the
// recording state, never the session name, agent id or harness. Failure
// prevented: the first line of the brief is plumbing.
func TestRender_HeaderOmitsNameAndHarness(t *testing.T) {
	tests := []struct {
		name string
		s    SessionInfo
		want string
	}{
		{"recording", SessionInfo{Name: "2026-10-23T10-00-dev-OxAb12", AgentID: "OxAb12", Harness: "codex", Recording: true, Turns: 1},
			"ox session report · this session  ● recording · 1 turn"},
		{"not recording", SessionInfo{AgentID: "OxAb12", Harness: "claude-code"}, "ox session report · this session  ○ not recording"},
		{"past session", SessionInfo{Name: "2026-10-01T10-00-old-OxZz99", AgentID: "OxZz99", Harness: "codex", Turns: 4},
			"ox session report · past session  4 turns"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Display(Report{Session: tt.s, Trace: TraceInfo{Available: true, Source: TraceSourceSession}}, 80)
			header := strings.SplitN(got, "\n", 2)[0]
			assert.Equal(t, tt.want, header)
			for _, banned := range []string{tt.s.Name, tt.s.AgentID, tt.s.Harness} {
				if banned != "" {
					assert.NotContains(t, header, banned)
				}
			}
		})
	}
}

// TestRender_UsedNameFallsBack: a used titled item reads "kind  title"; an
// untitled one keeps its ref-based name; an over-long title is cut to fit.
func TestRender_UsedNameFallsBack(t *testing.T) {
	used := &ItemUsed{Evidence: EvidenceRead}
	r := Report{Trace: TraceInfo{Available: true, Source: TraceSourceSession}, Surfaced: []Surfaced{
		{Ref: "pln_1", RefType: "plan", Title: "Usage receipts", Used: used},
		{Ref: "pln_2", RefType: "plan", Used: used},
		{Ref: "docs/x.md", RefType: "doc", Used: used},
		{Ref: "auth refresh", RefType: "query", Via: "retrieval", Used: used},
		{Ref: "rec_3", RefType: "discussion", Title: strings.Repeat("long title ", 20), Used: used},
	}}
	got := Display(r, 80)
	assert.Contains(t, got, "used       plan  Usage receipts  read\n")
	assert.Contains(t, got, "plan pln_2  read\n")
	assert.Contains(t, got, "docs/x.md  read\n")
	assert.Contains(t, got, `query "auth refresh"  read`)
	assert.Contains(t, got, "discussion  long title")
	assert.Contains(t, got, "…")
	assertPlainFits(t, got, 80)
}

// TestRender_URLWholeOnContinuationLine: a used item's link sits whole on
// its own line, two past the label column, and is never cut. Failure
// prevented: a truncated, unclickable link.
func TestRender_URLWholeOnContinuationLine(t *testing.T) {
	url := "https://sageox.ai/c/" + testCnv
	r := Report{Trace: TraceInfo{Available: true, Source: TraceSourceSession}, Surfaced: []Surfaced{
		{Ref: testCnv, RefType: "discussion", Title: "Usage report design review", URL: url, Used: &ItemUsed{Evidence: EvidenceCited}},
	}}
	lines := strings.Split(Display(r, 80), "\n")
	var at int
	for i, l := range lines {
		if strings.Contains(l, "Usage report design review") {
			at = i
		}
	}
	require.NotZero(t, at)
	assert.Equal(t, "used       discussion  Usage report design review  cited", lines[at])
	assert.Equal(t, strings.Repeat(" ", labelCol+2)+url, lines[at+1])
}

// TestRender_ReceivedMurmurIsOnlyACount: a murmur merely received is folded
// into "available"; a used one is named by sender and topic. Failure
// prevented: the brief lists every murmur received as if it were value.
func TestRender_ReceivedMurmurIsOnlyACount(t *testing.T) {
	base := Report{Trace: TraceInfo{Available: true, Source: TraceSourceSession}}
	received := base
	received.Surfaced = []Surfaced{{Ref: "mur_1", RefType: "murmur", From: "OxR9a1", Topic: "wip", Via: "whisper"}}
	got := Display(received, 80)
	assert.Contains(t, got, "available  1 murmur\n")
	assert.NotContains(t, got, "OxR9a1")
	assert.NotContains(t, got, "used ")

	used := base
	used.Surfaced = []Surfaced{
		{Ref: "mur_1", RefType: "murmur", From: "OxR9a1", Topic: "wip", Via: "whisper"},
		{Ref: "mur_2", RefType: "murmur", From: "OxRy01", FromName: "Ryan", Topic: "wip", Via: "whisper", Used: &ItemUsed{Evidence: EvidenceRead}},
		{Ref: "mur_3", RefType: "murmur", From: "OxSm02", Via: "whisper", Used: &ItemUsed{Evidence: EvidenceJudged}},
	}
	got = Display(used, 80)
	assert.Contains(t, got, "used       murmur  Ryan [wip]  read\n           murmur  OxSm02  judged\n")
	assert.Contains(t, got, "available  1 murmur\n")
	assert.NotContains(t, got, "OxR9a1")
}

// TestRender_LegendOnlyWithUsed: the legend appears only when the used
// section does.
func TestRender_LegendOnlyWithUsed(t *testing.T) {
	base := Report{Trace: TraceInfo{Available: true, Source: TraceSourceSession}}
	plainR := base
	plainR.Surfaced = []Surfaced{{Ref: "MEMORY.md", RefType: "doc"}}
	assert.NotContains(t, Display(plainR, 80), usedLegend)

	tests := []struct {
		name string
		s    []Surfaced
	}{
		{"used doc", []Surfaced{{Ref: "MEMORY.md", RefType: "doc", Used: &ItemUsed{Evidence: EvidenceRead}}}},
		{"used murmur", []Surfaced{{Ref: "mur_1", RefType: "murmur", FromName: "Ryan", Used: &ItemUsed{Evidence: EvidenceCited}}}},
		{"used skill beside an available one", []Surfaced{
			{Ref: "a", RefType: refTypeSkill, Used: &ItemUsed{Evidence: EvidenceReported}},
			{Ref: "b", RefType: refTypeSkill},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := base
			r.Surfaced = tt.s
			got := Display(r, 80)
			assert.Contains(t, got, usedLegend+"\n")
		})
	}
}

// TestRender_RelevantInHeader: "N of M used" appears only when the trace
// carries a relevant summary, and an explicit none report reads "none used".
func TestRender_RelevantInHeader(t *testing.T) {
	r := Report{Session: SessionInfo{Recording: true, Turns: 3}, Trace: TraceInfo{Available: true, Source: TraceSourceSession}}
	assert.NotContains(t, Display(r, 80), "used")

	r.Relevant = &Relevant{Count: 2, SurfacedCount: 7}
	assert.True(t, strings.HasPrefix(Display(r, 80), "ox session report · this session  ● recording · 3 turns · 2 of 7 used\n"))

	r.Relevant = &Relevant{SurfacedCount: 7, ReportedNone: true}
	got := Display(r, 80)
	assert.True(t, strings.HasPrefix(got, "ox session report · this session  ● recording · 3 turns · none used\n"), got)
	assert.NotContains(t, got, "0 of 7")
}

// TestRender_NoPlumbingStrings: none of the retired plumbing wording reaches
// any session display shape. Failure prevented: the brief regresses to
// "injected … tok" and "used: not yet reported".
func TestRender_NoPlumbingStrings(t *testing.T) {
	banned := []string{"injected", "not yet reported", "not recording — no self-report", " tok\n", " tok ", "you reported", "surfaced ",
		"grade", "provable", "surfaced_seq", "✓"}
	shapes := map[string]string{
		"golden 80":     goldenReport(80).Display,
		"golden 120":    goldenReport(120).Display,
		"example 80":    exampleReport(80).Display,
		"example 120":   exampleReport(120).Display,
		"recording":     Build(Input{TraceSource: TraceSourceSession, AgentID: "OxAb12", Recording: true, Events: []contexttrace.Event{primeEvent(), memoryProvided(), whisper()}}).Display,
		"not recording": Build(Input{TraceSource: TraceSourceAgentCache, AgentID: "OxAb12", Events: []contexttrace.Event{invoked("checked murmurs", "murmur list", 10, 1), selfReport("MEMORY.md")}}).Display,
		"no trace":      Build(Input{TraceSource: TraceSourceNone, TraceReason: "not recording"}).Display,
	}
	for name, got := range shapes {
		t.Run(name, func(t *testing.T) {
			for _, b := range banned {
				assert.NotContains(t, got, b)
			}
			assert.False(t, strings.HasSuffix(strings.TrimRight(got, "\n"), " tok"))
		})
	}
	ledger := t.TempDir()
	writeLedgerSession(t, ledger, alice, 1, nil, primeEvent(), invoked("checked murmurs", "murmur list", 300, 2))
	for _, got := range []string{BuildMe(rollupIn(ledger, aliceID())).Display, BuildTeam(rollupIn(ledger, recap.Identity{})).Display} {
		assert.NotContains(t, got, "injected")
		assert.NotContains(t, got, " tok ")
		assert.NotContains(t, got, "reported used")
	}
}

// TestBuildMe_UsefulCountsEvidence: useful is the relevant count when the
// trace carries one, else the surfaced items with use evidence.
func TestBuildMe_UsefulCountsEvidence(t *testing.T) {
	assert.Equal(t, 0, usefulCount(Report{Surfaced: []Surfaced{{Ref: "a"}}}))
	assert.Equal(t, 1, usefulCount(Report{Surfaced: []Surfaced{{Ref: "a"}, {Ref: "b", Used: &ItemUsed{Evidence: EvidenceRead}}}}))
	assert.Equal(t, 4, usefulCount(Report{Relevant: &Relevant{Count: 4}, Surfaced: []Surfaced{{Ref: "b", Used: &ItemUsed{Evidence: EvidenceRead}}}}))

	got := DisplayMe(MeReport{Window: "7d", Totals: MeTotals{Sessions: 1, Calls: 3, Tokens: 1200, Useful: 2},
		Sessions: []MeRow{{Name: "n", Date: "10-23", Title: "t", Trace: RowTraced, Calls: 3, Tokens: 1200, Surfaced: 5, Useful: 2}}}, 80)
	assert.Contains(t, got, "read useful")
	assert.Contains(t, got, "3 calls · ~1.2k tokens · 2 useful · co-author 0 of 1")

	attributed := DisplayMe(MeReport{Totals: MeTotals{Sessions: 3, Attributed: 2},
		Sessions: []MeRow{{Name: "n", Date: "10-23", Title: "t", Trace: RowTraced, WouldAttribute: true}}}, 80)
	assert.Contains(t, attributed, "co-author 2 of 3\n", "the total says how many sessions would credit SageOx")
	assertPlainFits(t, got, 80)
}

// TestRender_AvailableCountsFromFullTotals: the available line counts each
// kind from surfaced_by_type, not the capped list. Failure prevented: 11
// murmurs surfaced, 2 shown in the list, and the brief says "2 murmurs".
func TestRender_AvailableCountsFromFullTotals(t *testing.T) {
	r := Report{
		Trace: TraceInfo{Available: true, Source: TraceSourceSession},
		Surfaced: []Surfaced{
			{Ref: "doc-a.md", RefType: "doc", Via: "prime"},
			{Ref: "mur_1", RefType: "murmur", Via: "whisper"},
			{Ref: "mur_2", RefType: "murmur", Via: "whisper", FromName: "Ryan", Used: &ItemUsed{Evidence: EvidenceCited}},
			{Ref: "cnv_a", RefType: "discussion", Via: "retrieval"},
		},
		SurfacedByType: map[string]int{"doc": 4, "murmur": 11, "discussion": 2, "plan": 5, refTypeSkill: 1},
	}
	got := Display(r, 120)
	assert.Contains(t, got, "available  doc-a.md · 10 murmurs · 2 discussions · 5 plans · 1 team skill\n")
}
