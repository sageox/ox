package usage

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/session/contexttrace"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files")

// goldenReport is a busy session: every section populated, including the
// optional use-evidence fields a trace may carry — a titled, linked, used
// discussion, a linked plan, team skills, and two murmurs of which one was
// used (the unused one must not be listed).
func goldenReport(width int) Report {
	prime := primeEvent()
	prime.FullBytes, prime.Deferred = 14200, []string{"team-docs"}
	q := invoked("searched team memory", "query", 900, 7)
	q.Query, q.RefType = "auth token refresh", "query"
	events := []contexttrace.Event{
		prime,
		{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamContext, Docs: []string{"AGENTS.md"}},
		memoryProvided(),
		invoked("checked murmurs", "murmur list", 300, 3),
		invoked("checked murmurs", "murmur list", 300, 5),
		invoked("checked murmurs", "murmur list", 300, 9),
		q,
		invoked("pulled plan", "plan view", 680, 11),
		whisper(),
		selfReport("MEMORY.md"),
	}
	r := Build(Input{
		TraceSource: TraceSourceSession, SessionName: "2026-10-23T10-00-dev-OxAb12",
		AgentID: "OxAb12", Harness: "codex", Recording: true, Turns: 31,
		ScoreCategory: "moderate", ProducedCommits: 2, Events: events, Width: width,
	})
	// built: AGENTS.md, MEMORY.md, query, murmur OxR9a1 (received, unused)
	built := r.Surfaced
	r.Surfaced = append([]Surfaced{}, built[:2]...)
	r.Surfaced = append(r.Surfaced,
		Surfaced{Ref: "review-pr", RefType: refTypeSkill, Via: "prime"},
		Surfaced{Ref: "release", RefType: refTypeSkill, Via: "prime"},
		Surfaced{Ref: "triage", RefType: refTypeSkill, Via: "prime"},
		Surfaced{Ref: "cnv_0192a3b4-5c6d-7e8f-9a0b-1c2d3e4f5a6b", RefType: "discussion", Via: "retrieval",
			Title: "Usage report design review", Seq: 12, Used: &ItemUsed{Evidence: EvidenceCited, Seq: 14, Grade: "provable"}},
		Surfaced{Ref: "pln_4d8e2f", RefType: "plan", Via: "retrieval", Title: "Usage receipts"},
	)
	r.Surfaced = append(r.Surfaced, built[2:]...)
	r.Surfaced = append(r.Surfaced, Surfaced{Ref: "mur_r", RefType: "murmur", From: "OxRy01", FromName: "Ryan", Topic: "wip",
		Via: "whisper", Used: &ItemUsed{Evidence: EvidenceRead}})
	fillURLs(r.Surfaced, "https://sageox.ai")
	r.SurfacedByType = countByType(r.Surfaced)
	r.Relevant = &Relevant{Count: 2, SurfacedCount: len(r.Surfaced)}
	r.Display = Display(r, width)
	return r
}

// TestDisplay_Golden pins the dense layout at 80 and 120 columns. Failure
// prevented: the block both harnesses print verbatim drifts, wraps past the
// terminal, grows past one screen, or leaks color into an AI coworker's text.
func TestDisplay_Golden(t *testing.T) {
	for _, width := range []int{80, 120} {
		t.Run(filepath.Base(goldenName(width)), func(t *testing.T) {
			got := goldenReport(width).Display
			assert.NotContains(t, got, "\x1b")
			lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
			assert.LessOrEqual(t, len(lines), maxDisplayLines)
			for _, l := range lines {
				assert.LessOrEqual(t, ansi.StringWidth(l), width, "line overflows %d cols: %q", width, l)
			}

			path := goldenName(width)
			if *updateGolden {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
			}
			want, err := os.ReadFile(path)
			require.NoError(t, err, "run with -update to create %s", path)
			assert.Equal(t, string(want), got)
		})
	}
}

func goldenName(width int) string {
	return filepath.Join("testdata", "display_"+map[int]string{80: "80", 120: "120"}[width]+".golden")
}

const exampleCnv = "cnv_01a17990-4cb6-7bf9-9584-4728f14b54f1"

// exampleReport is the brief's design target: three used items (a discussion
// cited in a commit, a plan the AI coworker reported following, a murmur it
// read), the rest only available, and a derived co-author verdict.
func exampleReport(width int) Report {
	read := &ItemUsed{Evidence: EvidenceRead}
	r := Report{
		View:    ViewSession,
		Session: SessionInfo{Name: "2026-10-26T09-00-dev-OxAb12", AgentID: "OxAb12", Recording: true, Turns: 42},
		Trace:   TraceInfo{Available: true, Source: TraceSourceSession},
		Calls: Calls{Total: 6, ByCommand: []CommandStat{
			{Label: "query", Count: 2}, {Label: "conversation show", Count: 1},
			{Label: "plan view", Count: 1}, {Label: "context used", Count: 2},
		}},
		Context: Context{Tokens: 6700, BySource: []SourceStat{
			{Source: "prime", Tokens: 3100}, {Source: "ox calls", Tokens: 3200}, {Source: "whispers", Tokens: 400},
		}},
		Surfaced: []Surfaced{
			{Ref: "AGENTS.md", RefType: "doc", Via: "prime"},
			{Ref: "MEMORY.md", RefType: "doc", Via: "prime"},
			{Ref: "review-pr", RefType: refTypeSkill, Via: "prime"},
			{Ref: "release", RefType: refTypeSkill, Via: "prime"},
			{Ref: "triage", RefType: refTypeSkill, Via: "prime"},
			{Ref: exampleCnv, RefType: "discussion", Via: "retrieval", Title: "Usage report design review", Used: &ItemUsed{Evidence: EvidenceCited}},
			{Ref: "cnv_0192a3b4-0000-7e8f-9a0b-1c2d3e4f5a6b", RefType: "discussion", Via: "retrieval", Title: "Ledger cache"},
			{Ref: "rec_0192a3b4-1111-7e8f-9a0b-1c2d3e4f5a6b", RefType: "discussion", Via: "retrieval", Title: "Pricing sync"},
			{Ref: "pln_4d8e2f", RefType: "plan", Via: "retrieval", Title: "Usage receipts", Used: &ItemUsed{Evidence: EvidenceReported}},
			{Ref: "pln_9a7b1c", RefType: "plan", Via: "retrieval", Title: "Statusline rows"},
			{Ref: "mur_r", RefType: "murmur", From: "OxRy01", FromName: "Ryan", Topic: "wip", Via: "whisper", Used: read},
		},
		Attribution: Attribution{
			WouldAttribute: true, Category: "significant", Source: AttributionDerived, Threshold: 0.5,
			Reason: "derived from context use: cited discussion cnv_01a17990 in commit 3e1c9d0; reported using plan pln_4d8e2f",
		},
	}
	fillURLs(r.Surfaced, "https://sageox.ai")
	r.Relevant = &Relevant{Count: 3, SurfacedCount: len(r.Surfaced), Items: []RelevantItem{
		{Ref: exampleCnv, RefType: "discussion", Title: "Usage report design review", URL: "https://sageox.ai/c/" + exampleCnv,
			Evidence: EvidenceCited, Detail: "cited_commit", Grade: "provable", Seq: 9, Commit: "3e1c9d0"},
		{Ref: "pln_4d8e2f", RefType: "plan", Title: "Usage receipts", URL: "https://sageox.ai/plan/pln_4d8e2f",
			Evidence: EvidenceReported, Detail: "reported", Grade: "reported", Seq: 14, Why: "followed its meta.json rollup shape"},
		{Ref: "mur_r", RefType: "murmur", Evidence: EvidenceRead, Detail: "read", Grade: "provable", Seq: 20},
	}}
	r.Display = Display(r, width)
	return r
}

// TestDisplay_ExampleGolden pins the design target at 80 and 120 columns.
// Failure prevented: used items lose their links or evidence, an unused
// discussion or plan is linked, or the block outgrows one screen.
func TestDisplay_ExampleGolden(t *testing.T) {
	for _, width := range []int{80, 120} {
		t.Run(fmt.Sprintf("%d", width), func(t *testing.T) {
			got := exampleReport(width).Display
			assertPlainFits(t, got, width)
			assert.Contains(t, got, "\n             https://sageox.ai/c/"+exampleCnv+"\n")
			assert.Contains(t, got, "\n             https://sageox.ai/plan/pln_4d8e2f\n")
			assert.Equal(t, 2, strings.Count(got, "https://"), "only the two used, linkable items carry links")
			assert.Contains(t, got, "2 discussions · 1 plan")

			path := filepath.Join("testdata", fmt.Sprintf("example_%d.golden", width))
			if *updateGolden {
				require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
			}
			want, err := os.ReadFile(path)
			require.NoError(t, err, "run with -update to create %s", path)
			assert.Equal(t, string(want), got)
		})
	}
}

// TestRender_EvidencePhrases: each raw evidence detail reads as words beside
// the used item. Failure prevented: "cited_commit" or "requery" plumbing in
// the brief, or a commit-cited item that does not say which commit.
func TestRender_EvidencePhrases(t *testing.T) {
	tests := []struct {
		name string
		it   RelevantItem
		want string
	}{
		{"read", RelevantItem{Detail: "read"}, "read"},
		{"requery", RelevantItem{Detail: "requery"}, "re-opened"},
		{"cited", RelevantItem{Detail: "cited"}, "cited"},
		{"cited in commit", RelevantItem{Detail: "cited_commit", Commit: "3e1c9d0"}, "cited in commit 3e1c9d0"},
		{"cited in unknown commit", RelevantItem{Detail: "cited_commit"}, "cited in a commit"},
		{"reported with why", RelevantItem{Detail: "reported", Why: "followed it"}, "reported: followed it"},
		{"reported without why", RelevantItem{Detail: "reported"}, "reported"},
		{"judged", RelevantItem{Detail: "judged"}, "judged"},
		{"bucket only", RelevantItem{Evidence: EvidenceCited}, "cited"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.it.Ref, tt.it.RefType, tt.it.Title = "pln_1", "plan", "Usage receipts"
			r := Report{Trace: TraceInfo{Available: true}, Relevant: &Relevant{Count: 1, Items: []RelevantItem{tt.it}}}
			assert.Contains(t, Display(r, 80), "used       plan  Usage receipts  "+tt.want+"\n")
		})
	}

	// a long --why is cut to fit beside the name, or to its own line
	long := RelevantItem{Ref: "pln_1", RefType: "plan", Title: strings.Repeat("t", 40), Detail: "reported", Why: strings.Repeat("why ", 60)}
	for _, width := range []int{60, 80, 120} {
		got := Display(Report{Trace: TraceInfo{Available: true}, Relevant: &Relevant{Count: 1, Items: []RelevantItem{long}}}, width)
		assertPlainFits(t, got, width)
		assert.Contains(t, got, "reported: why")
		assert.Contains(t, got, "…")
	}
}

// TestRender_UsedCapAndMore: more than eight used items show eight and say
// how many more. Failure prevented: a busy session pushes the co-author row
// off the one-screen brief.
func TestRender_UsedCapAndMore(t *testing.T) {
	rel := &Relevant{}
	for i := 0; i < maxUsedItems+3; i++ {
		rel.Items = append(rel.Items, RelevantItem{Ref: fmt.Sprintf("doc-%02d.md", i), RefType: "doc", Detail: "read"})
	}
	rel.Count = len(rel.Items)
	got := Display(Report{Trace: TraceInfo{Available: true}, Relevant: rel}, 80)
	assert.Contains(t, got, "doc-07.md  read")
	assert.NotContains(t, got, "doc-08.md")
	assert.Contains(t, got, "\n           +3 more\n")
	assertPlainFits(t, got, 80)
}

// TestRender_AvailableFoldsByKind: what was not used is one line — docs by
// name, skills folded, everything else counted by kind — and never linked.
func TestRender_AvailableFoldsByKind(t *testing.T) {
	r := Report{Trace: TraceInfo{Available: true}, Surfaced: []Surfaced{
		{Ref: "MEMORY.md", RefType: "doc"},
		{Ref: "a", RefType: refTypeSkill}, {Ref: "b", RefType: refTypeSkill},
		{Ref: testCnv, RefType: "discussion", Title: "T", URL: "https://sageox.ai/c/" + testCnv},
		{Ref: "q1", RefType: "query", Via: "retrieval"}, {Ref: "q2", RefType: "query", Via: "retrieval"},
		{Ref: "ses_1", RefType: "session", URL: "https://sageox.ai/c/ses_1"},
		{Ref: "x", RefType: ""},
	}}
	got := Display(r, 120)
	assert.Contains(t, got, "available  MEMORY.md · 2 team skills · 1 discussion · 2 queries · 1 session · 1 item\n")
	assert.NotContains(t, got, "https://")
	assert.NotContains(t, got, usedLegend, "no used section, no legend")

	// at 80 columns the one label wraps instead of overflowing
	assertPlainFits(t, Display(r, 40), 40)
}

// TestRender_CoauthorRow: the co-author row says the category, whether it
// clears the threshold, and why; a derived none is omitted. Failure
// prevented: the brief claims credit it would not get, or hides why.
func TestRender_CoauthorRow(t *testing.T) {
	tests := []struct {
		name string
		a    Attribution
		want []string
		none bool
	}{
		{"derived, would attribute",
			Attribution{WouldAttribute: true, Category: "significant", Source: AttributionDerived, Threshold: 0.5, Reason: "derived from context use: cited plan pln_1"},
			[]string{"co-author  significant · would attribute (threshold 0.5)\n           derived from context use: cited plan pln_1\n"}, false},
		{"self-reported with reason",
			Attribution{WouldAttribute: true, Category: "critical", Source: AttributionSelfReported, Threshold: 0.7, Reason: "followed the ADR"},
			[]string{"co-author  critical · would attribute (threshold 0.7)\n           reported by the AI coworker: followed the ADR\n"}, false},
		{"self-reported, no reason",
			Attribution{Category: "moderate", Source: AttributionSelfReported, Threshold: 0.7},
			[]string{"co-author  moderate · below threshold (threshold 0.7)\n           reported by the AI coworker\n"}, false},
		{"self-reported none is shown",
			Attribution{Category: "none", Source: AttributionSelfReported, Threshold: 0.5},
			[]string{"co-author  none · below threshold (threshold 0.5)\n"}, false},
		{"derived below threshold, no threshold known",
			Attribution{Category: "minor", Source: AttributionDerived},
			[]string{"co-author  minor · below threshold\n"}, false},
		{"derived none is omitted", Attribution{Category: "none", Source: AttributionDerived, Threshold: 0.5, Reason: "derived from context use: nothing used"}, nil, true},
		{"no category is omitted", Attribution{Source: AttributionDerived}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Display(Report{Trace: TraceInfo{Available: true}, Attribution: tt.a}, 80)
			for _, w := range tt.want {
				assert.Contains(t, got, w)
			}
			if tt.none {
				assert.NotContains(t, got, "co-author")
			}
		})
	}

	// no trace, no row — even with a self-reported score
	noTrace := Display(Report{Attribution: Attribution{Category: "critical", Source: AttributionSelfReported, WouldAttribute: true}}, 80)
	assert.NotContains(t, noTrace, "co-author")
}

// TestHuman_NoColorMatchesDisplay: with NO_COLOR the terminal path prints
// exactly the display text. Failure prevented: NO_COLOR users get escapes.
func TestHuman_NoColorMatchesDisplay(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	r := goldenReport(80)
	assert.Equal(t, r.Display, Human(r, 80))
}

func TestFmtK(t *testing.T) {
	for n, want := range map[int]string{0: "0", 87: "87", 220: "0.2k", 6400: "6.4k", 14200: "14.2k", 142000: "142k"} {
		assert.Equal(t, want, FmtK(n), "n=%d", n)
	}
}
