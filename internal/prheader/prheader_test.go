package prheader

import (
	"strings"
	"testing"
)

// baseInput is a fully-populated header: a team (distinct from the brand), two
// sessions, one plan, and material enrichment (2 prior art + 1 collision).
// Individual tests clone and mutate it to exercise one axis at a time.
func baseInput() Input {
	return Input{
		TeamName: "SageOx Internal",
		TeamURL:  "https://sageox.ai/t/sageox-internal",
		Sessions: []Session{{URL: "https://sageox.ai/c/ses_9f2a3c"}, {URL: "https://sageox.ai/c/ses_7c1b8d"}},
		Plans:    []Plan{{URL: "https://sageox.ai/plan/pln_4d8e2f"}},
		Signals:  Signals{PriorArt: 2, Collisions: 1, Material: true},
		ShowStat: true,
	}
}

func mustContain(t *testing.T, got string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(got, s) {
			t.Errorf("output missing %q\n---\n%s", s, got)
		}
	}
}

func mustNotContain(t *testing.T, got string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if strings.Contains(got, s) {
			t.Errorf("output unexpectedly contains %q\n---\n%s", s, got)
		}
	}
}

func TestRender_full(t *testing.T) {
	got := Render(baseInput())

	mustContain(t, got,
		markerStart, markerEnd,
		"\n> ",                          // blockquote card
		"<sub>guided&nbsp;by</sub><br>", // the "guided by" kicker above the mark
		`<source media="(prefers-color-scheme: dark)" srcset="https://sageox.ai/sageox-wordmark-dark.png">`,
		`src="https://sageox.ai/sageox-wordmark-light.png"`,
		`<a href="https://sageox.ai/t/sageox-internal">`, // wordmark links to the team page
		"<b>SageOx&nbsp;Internal</b>",                    // team name is muted text, not a link
		`<a href="https://sageox.ai/c/ses_9f2a3c">Session&nbsp;1</a>`,
		`<a href="https://sageox.ai/c/ses_7c1b8d">Session&nbsp;2</a>`,
		`<a href="https://sageox.ai/plan/pln_4d8e2f">Plan</a>`, // one plan => unnumbered
		"> <sub>2&nbsp;related&nbsp;sessions &middot; 1&nbsp;concurrent&nbsp;edit&nbsp;flagged</sub>",
	)
	// The team name is NOT a link (would go GitHub-blue like Session/Plan).
	mustNotContain(t, got,
		`<a href="https://sageox.ai/t/sageox-internal"><b>`,
		"Guided&nbsp;by&nbsp;SageOx", "prior&nbsp;art", "collision", "expert",
	)
	// Sessions grouped by whitespace, not a divider.
	mustContain(t, got, "Session&nbsp;1</a>&nbsp;&nbsp;<a")
	// Markers wrap the whole thing on their own lines.
	if !strings.HasPrefix(got, markerStart+"\n") || !strings.HasSuffix(got, "\n"+markerEnd) {
		t.Errorf("markers not wrapping content:\n%s", got)
	}
}

// TestRender_dedupBrandTeamName proves the brand is never rendered twice: when the
// team name equals the wordmark's brand (the dogfood team is literally "SageOx"),
// the team-name text is suppressed — the mark stands for it. Case-insensitive.
// Failure prevented: every SageOx PR reads "SageOx · SageOx".
func TestRender_dedupBrandTeamName(t *testing.T) {
	for _, name := range []string{"SageOx", "sageox", "  SageOx  "} {
		in := baseInput()
		in.TeamName = name
		got := Render(in)
		mustContain(t, got, "<picture>", `<a href="https://sageox.ai/c/ses_9f2a3c">`)
		mustNotContain(t, got, "<b>") // team-name text omitted entirely
	}
	// A team whose name merely CONTAINS the brand still renders (not an exact match).
	in := baseInput()
	in.TeamName = "SageOx Internal"
	mustContain(t, Render(in), "<b>SageOx&nbsp;Internal</b>")
}

// TestRender_whisperRequiresPlan proves stats render only when a Plan link is
// present — a reviewer needs somewhere to verify the claim.
// Failure prevented: unverifiable enrichment numbers on a plan-less PR.
func TestRender_whisperRequiresPlan(t *testing.T) {
	in := baseInput()
	in.Plans = nil // material signals, but nothing to verify against
	got := Render(in)
	// The kicker is also a "> <sub>", so assert on the stat text, not the tag.
	mustNotContain(t, got, "related", "concurrent")

	in.Plans = []Plan{{URL: "https://sageox.ai/plan/pln_x"}}
	mustContain(t, Render(in), "2&nbsp;related&nbsp;sessions", "1&nbsp;concurrent&nbsp;edit&nbsp;flagged")
}

func TestRender_degradedStates(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(Input) Input
		contains   []string
		notContain []string
	}{
		{
			name: "one session, no plans, no enrichment",
			mutate: func(in Input) Input {
				in.Sessions = []Session{{URL: "https://sageox.ai/c/ses_9f2a3c"}}
				in.Plans = nil
				in.Signals = Signals{}
				return in
			},
			contains:   []string{`>Session</a>`, "<sub>guided&nbsp;by</sub><br>"}, // singular, unnumbered
			notContain: []string{"Session&nbsp;1", "related", "concurrent", "Plan"},
		},
		{
			name: "bare: no sessions, no plans, no enrichment",
			mutate: func(in Input) Input {
				in.Sessions = nil
				in.Plans = nil
				in.Signals = Signals{}
				return in
			},
			contains:   []string{"<picture>", "<b>SageOx&nbsp;Internal</b>", "<sub>guided&nbsp;by</sub><br>"},
			notContain: []string{"Session", "Plan", "related", "concurrent"},
		},
		{
			name: "material but ShowStat off => no stats",
			mutate: func(in Input) Input {
				in.ShowStat = false
				return in
			},
			contains:   []string{"Session&nbsp;1"},
			notContain: []string{"related", "concurrent"},
		},
		{
			name: "no team name => team segment omitted",
			mutate: func(in Input) Input {
				in.TeamName = ""
				return in
			},
			contains:   []string{"<picture>", "Session&nbsp;1"},
			notContain: []string{"<b>"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Render(tc.mutate(baseInput()))
			mustContain(t, got, tc.contains...)
			mustNotContain(t, got, tc.notContain...)
		})
	}
}

func TestRender_teamNameEscaped(t *testing.T) {
	in := baseInput()
	in.TeamName = `Ac & <script>alert(1)</script>`
	got := Render(in)
	mustContain(t, got, "&amp;", "&lt;script&gt;")
	mustNotContain(t, got, "<script>alert")
}

func TestRender_noTeamURL_wordmarkUnlinked(t *testing.T) {
	in := baseInput()
	in.TeamURL = ""
	got := Render(in)
	// The wordmark <picture> must appear, but not wrapped in an anchor to a team.
	mustContain(t, got, "<picture>")
	mustNotContain(t, got, `<a href="https://sageox.ai/t/`)
}

func TestRender_tierB_floatedStripFirstNoSub(t *testing.T) {
	in := baseInput()
	in.Strip = &StripURLs{
		Light: "https://assets.sageox.ai/pr/enrich-2-1-light.svg",
		Dark:  "https://assets.sageox.ai/pr/enrich-2-1-dark.svg",
	}
	got := Render(in)

	mustContain(t, got,
		`align="right"`,
		`srcset="https://assets.sageox.ai/pr/enrich-2-1-dark.svg"`,
		`alt="2 related sessions · 1 concurrent edit flagged"`,
	)
	// The floated strip must precede the wordmark in source order (so it floats
	// onto the same line), and Tier B replaces the <sub> TEXT stats.
	stripIdx := strings.Index(got, `align="right"`)
	markIdx := strings.Index(got, `alt="SageOx"`)
	if stripIdx < 0 || markIdx < 0 || stripIdx > markIdx {
		t.Errorf("floated strip must come before the wordmark; strip=%d wordmark=%d", stripIdx, markIdx)
	}
	// The text-tier stats caption is not emitted under Tier B (the kicker's
	// <sub> is fine; the &nbsp;-joined stats markup must be absent).
	mustNotContain(t, got, "2&nbsp;related&nbsp;sessions")
}

func TestRender_tierB_ignoredWithoutWhisper(t *testing.T) {
	in := baseInput()
	in.Signals = Signals{} // no counts => no whisper => strip irrelevant
	in.Strip = &StripURLs{Light: "l.svg", Dark: "d.svg"}
	got := Render(in)
	mustNotContain(t, got, `align="right"`, "related")
}

func TestRender_tierB_ignoredWhenStripIncomplete(t *testing.T) {
	in := baseInput()
	in.Strip = &StripURLs{Light: "only-light.svg"} // missing dark => fall back to Tier A text
	got := Render(in)
	mustContain(t, got, "> <sub>", "2&nbsp;related&nbsp;sessions")
	mustNotContain(t, got, `align="right"`)
}
